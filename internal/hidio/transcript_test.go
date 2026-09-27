package hidio_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

func TestFrameText(t *testing.T) {
	tests := []struct {
		text string
		f    hidio.Frame
	}{
		{"03 00 ff", hidio.Frame{Bytes: []byte{3, 0, 0xff}}},
		{"03 xx 0a", hidio.Frame{Bytes: []byte{3, 0, 0x0a}, Mask: 1 << 1}},
		{"", hidio.Frame{Bytes: []byte{}}},
	}
	for _, tt := range tests {
		b, err := tt.f.MarshalText()
		if err != nil || string(b) != tt.text {
			t.Errorf("MarshalText(%v) = %q, %v; want %q", tt.f, b, err, tt.text)
		}
		var f hidio.Frame
		if err := f.UnmarshalText([]byte(tt.text)); err != nil || !slices.Equal(f.Bytes, tt.f.Bytes) || f.Mask != tt.f.Mask {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", tt.text, f, err, tt.f)
		}
	}
	for _, bad := range []string{"3", "0g", "123", strings.Repeat("00 ", 65)} {
		var f hidio.Frame
		if err := f.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("UnmarshalText(%q) accepted", bad)
		}
	}
}

func TestReadTranscriptRejects(t *testing.T) {
	tests := map[string]string{
		"no header":         `{"t":"2026-09-26T10:00:00Z","dir":"in","id":7,"data":"00"}` + "\n",
		"wrong version":     `{"transcript":2,"started":"2026-09-26T10:00:00Z"}` + "\n",
		"unknown field":     `{"transcript":1,"started":"2026-09-26T10:00:00Z","serial":"x"}` + "\n",
		"unknown dir":       `{"transcript":1,"started":"2026-09-26T10:00:00Z"}` + "\n" + `{"t":"2026-09-26T10:00:00Z","dir":"sideways"}` + "\n",
		"short out":         `{"transcript":1,"started":"2026-09-26T10:00:00Z"}` + "\n" + `{"t":"2026-09-26T10:00:00Z","dir":"out","id":8,"data":"03 00"}` + "\n",
		"error without err": `{"transcript":1,"started":"2026-09-26T10:00:00Z"}` + "\n" + `{"t":"2026-09-26T10:00:00Z","dir":"out-error"}` + "\n",
		"empty":             "",
	}
	for name, src := range tests {
		if _, _, err := hidio.ReadTranscript(strings.NewReader(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Placeholder values of the kind a real session would record; none of them
// belong to a real device.
var (
	fakeAddr     = []byte{0xde, 0xad, 0xbe}
	fakeShortcut = []byte{0x04, 0x80, 0x08, 0x00, 0x81, 0x06, 0x00, 0x41, 0x06, 0x00}
	fakeSettings = []byte{0x02, 0x53, 0x06, 0x4f, 0x03, 0x52, 0x15, 0x15, 0x00, 0x2b}
	errLocked    = errors.New("set usb hid output report failed [rid=8]: (iokit/common) not permitted (0xe00002e2)")
)

// fakeReceiver answers like a receiver with an awake mouse: cmd 3 with the
// online flag and address, the handshake with a model, and reads from a tiny
// flash. It refuses the first write after lockNext is set.
type fakeReceiver struct {
	pipe     *hidio.Pipe
	lockNext bool
}

func newFakeReceiver() *fakeReceiver {
	d := &fakeReceiver{}
	d.pipe = hidio.NewPipe(d.handle)
	return d
}

// reply builds a reply the way the device frames it: wire.Build only builds
// requests, and a read request carries no data.
func (d *fakeReceiver) reply(c wire.Cmd, addr uint16, data []byte) {
	var p wire.Packet
	p[0] = byte(c)
	p[2], p[3] = byte(addr>>8), byte(addr)
	p[4] = byte(len(data))
	copy(p[5:], data)
	p[wire.Size-1] = p.Checksum()
	d.pipe.Deliver(wire.ReportID, p[:])
}

func (d *fakeReceiver) handle(p wire.Packet) error {
	if d.lockNext {
		d.lockNext = false
		return errLocked
	}
	switch p.Cmd() {
	case wire.CmdOnline:
		d.reply(wire.CmdOnline, 0, append([]byte{1}, fakeAddr...))
	case wire.CmdHandshake:
		d.reply(wire.CmdHandshake, 0, []byte{9, 9, 9, 9, 0x7b, 0x04, 0x01, 0})
	case wire.CmdRead:
		data := fakeSettings
		if p.Addr() == 256 {
			data = fakeShortcut
		}
		d.reply(wire.CmdRead, p.Addr(), data[:p.Len()])
		d.pipe.Deliver(7, []byte{0, 0, 1, 0, 0, 0, 0})
	}
	return nil
}

func handshake(nonce byte) wire.Packet {
	return wire.MustBuild(wire.Mouse, wire.CmdHandshake, 0, []byte{nonce, nonce + 1, nonce + 2, nonce + 3, 0, 0, 0, 0})
}

func mustRead(t *testing.T, addr uint16) wire.Packet {
	t.Helper()
	p, err := wire.BuildRead(wire.Mouse, addr, 10)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type step struct {
	p       wire.Packet
	lock    bool
	replies int
}

// session drives tr through the steps and returns the report-8 packets it got.
func session(t *testing.T, tr hidio.Transport, steps []step, lock func()) ([]wire.Packet, []error) {
	t.Helper()
	var got []wire.Packet
	var errs []error
	for _, s := range steps {
		if s.lock && lock != nil {
			lock()
		}
		errs = append(errs, tr.Write(s.p))
		for range s.replies {
			r, ok := receive(t, tr.Reports())
			if !ok {
				t.Fatal("Reports closed")
			}
			p, _ := r.Packet()
			got = append(got, p)
		}
	}
	return got, errs
}

func steps(t *testing.T) []step {
	return []step{
		{p: wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil), replies: 1},
		{p: handshake(0x10), replies: 1},
		{p: mustRead(t, 0), replies: 1},
		{p: mustRead(t, 256), replies: 1},
		{p: wire.MustBuild(wire.Mouse, wire.CmdBattery, 0, nil), lock: true},
	}
}

func record(t *testing.T, redacted bool) (string, []wire.Packet, []error) {
	t.Helper()
	var buf bytes.Buffer
	rec, err := hidio.NewRecorder(&buf, hidio.Header{Source: "test receiver", Redacted: redacted})
	if err != nil {
		t.Fatal(err)
	}
	dev := newFakeReceiver()
	tr := hidio.Guarded(rec.Wrap(dev.pipe), hidio.NewGuard(wire.Mouse))
	rec.Note("start")
	got, errs := session(t, tr, steps(t), func() { dev.lockNext = true })
	dev.pipe.Fail(errors.New("(iokit/common) no such device (0xe00002c0)"))
	if _, ok := receive(t, tr.Reports()); ok {
		t.Fatal("Reports open after the device went away")
	}
	tr.Close()
	if err := rec.Err(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), got, errs
}

func replay(t *testing.T, transcript string, st []step) (*hidio.Replay, hidio.Transport, []wire.Packet, []error) {
	t.Helper()
	tr, rp, err := hidio.OpenReplay(strings.NewReader(transcript), hidio.NewGuard(wire.Mouse))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	got, errs := session(t, tr, st, nil)
	return rp, tr, got, errs
}

func TestRecordReplayRoundTrip(t *testing.T) {
	transcript, live, liveErrs := record(t, false)
	if !strings.Contains(transcript, `"dir":"note","text":"start"`) || !strings.Contains(transcript, `"dir":"in","id":7`) {
		t.Fatalf("transcript lacks the note or the mouse report:\n%s", transcript)
	}
	st := steps(t)
	st[1].p = handshake(0x80)
	rp, tr, got, errs := replay(t, transcript, st)
	if !slices.Equal(got, live) {
		t.Fatalf("replayed replies differ:\n got %v\nwant %v", got, live)
	}
	for i := range errs {
		if (errs[i] == nil) != (liveErrs[i] == nil) || errs[i] != nil && errs[i].Error() != liveErrs[i].Error() {
			t.Fatalf("step %d: replayed error %v, live %v", i, errs[i], liveErrs[i])
		}
	}
	if hidio.Classify(errs[len(errs)-1]) != hidio.ClassLocked {
		t.Fatalf("replayed lock error classifies as %s", hidio.Classify(errs[len(errs)-1]))
	}
	receive(t, tr.Wake())
	if _, ok := receive(t, tr.Reports()); ok {
		t.Fatal("replayed Reports open after the recorded read error")
	}
	if hidio.Classify(tr.Err()) != hidio.ClassGone {
		t.Fatalf("replayed input error %v classifies as %s", tr.Err(), hidio.Classify(tr.Err()))
	}
	if rp.Remaining() != 0 || rp.Diverged() != nil {
		t.Fatalf("remaining %d, diverged %v", rp.Remaining(), rp.Diverged())
	}
}

func TestRedactedTranscript(t *testing.T) {
	transcript, live, _ := record(t, true)
	for _, secret := range [][]byte{fakeAddr, fakeShortcut[:4], fakeShortcut[5:9]} {
		if hex := (hidio.Frame{Bytes: secret}).String(); strings.Contains(transcript, hex) {
			t.Fatalf("redacted transcript contains %s:\n%s", hex, transcript)
		}
	}
	if !strings.Contains(transcript, (hidio.Frame{Bytes: fakeSettings}).String()) {
		t.Fatalf("redaction removed the settings bytes too:\n%s", transcript)
	}
	_, _, got, _ := replay(t, transcript, steps(t))
	if len(got) != len(live) {
		t.Fatalf("replayed %d replies, want %d", len(got), len(live))
	}
	online := got[0]
	if !online.Valid() || !bytes.Equal(online[6:9], []byte{0x33, 0x22, 0x11}) || online[5] != 1 {
		t.Fatalf("replayed cmd-3 reply %s, want the placeholder address and a valid checksum", online)
	}
	if got[2] != live[2] {
		t.Fatalf("settings read replayed as %s, want %s", got[2], live[2])
	}
	shortcut := got[3]
	if !shortcut.Valid() || !bytes.Equal(shortcut.Data(), bytes.Repeat([]byte{0xff}, 10)) {
		t.Fatalf("replayed shortcut read %s, want erased bytes with a valid checksum", shortcut)
	}
}

func TestRedactedRecordingHidesOtherReports(t *testing.T) {
	var buf bytes.Buffer
	started := time.Date(2026, 9, 26, 10, 0, 0, 0, time.FixedZone("PDT", -7*3600))
	rec, err := hidio.NewRecorder(&buf, hidio.Header{Redacted: true, Started: started})
	if err != nil {
		t.Fatal(err)
	}
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	tr := hidio.Guarded(rec.Wrap(pipe), hidio.NewGuard(wire.Mouse))
	keys := []byte{0x08, 0x00, 0x13, 0x04, 0x16, 0x16, 0x00, 0x00}
	pipe.Deliver(2, keys)
	receive(t, tr.Wake())
	tr.Close()
	text := buf.String()
	if strings.Contains(text, "-07:00") {
		t.Errorf("redacted transcript keeps the local UTC offset:\n%s", text)
	}
	h, entries, err := hidio.ReadTranscript(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if !h.Started.Equal(started) || h.Started.Location() != time.UTC {
		t.Errorf("started %v, want %v in UTC", h.Started, started)
	}
	var in []hidio.Entry
	for _, e := range entries {
		if e.At.Location() != time.UTC {
			t.Errorf("entry time %v is not UTC", e.At)
		}
		if e.Dir == hidio.DirIn {
			in = append(in, e)
		}
	}
	if len(in) != 1 || in[0].ID != 2 || len(in[0].Data.Bytes) != len(keys) {
		t.Fatalf("recorded %+v, want report 2 with %d bytes", in, len(keys))
	}
	for i := range keys {
		if !in[0].Data.Redacted(i) || in[0].Data.Bytes[i] != 0 {
			t.Errorf("byte %d of keyboard report %s is not masked", i, in[0].Data)
		}
	}
}

// The event count at +31 of a macro header decides how much the session
// reads next, so a redacted transcript keeps it to stay replayable.
func TestRedactKeepsMacroEventCount(t *testing.T) {
	const slot = 3
	addr := uint16(mouse.AddrMacro + slot*mouse.MacroSize + 22)
	var p wire.Packet
	p[0] = byte(wire.CmdRead)
	p[2], p[3] = byte(addr>>8), byte(addr)
	p[4] = wire.MaxData
	copy(p[5:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 4})
	p[wire.Size-1] = p.Checksum()
	e := hidio.Redact(hidio.Entry{Dir: hidio.DirIn, ID: wire.ReportID, Data: hidio.Frame{Bytes: p[:]}})
	for i := range wire.MaxData {
		if got, want := e.Data.Redacted(5+i), i != wire.MaxData-1; got != want {
			t.Errorf("macro byte +%d redacted = %v, want %v", 22+i, got, want)
		}
	}
	if e.Data.Bytes[14] != 4 {
		t.Errorf("event count redacted to %d, want 4", e.Data.Bytes[14])
	}
}

func TestRedactTranscriptMatchesRedactedRecording(t *testing.T) {
	plain, _, _ := record(t, false)
	var out bytes.Buffer
	if err := hidio.RedactTranscript(&out, strings.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	h, entries, err := hidio.ReadTranscript(&out)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Redacted {
		t.Fatal("header not marked redacted")
	}
	for _, e := range entries {
		if again := hidio.Redact(e); again.Data.Mask != e.Data.Mask || !slices.Equal(again.Data.Bytes, e.Data.Bytes) {
			t.Fatalf("Redact is not idempotent on %+v", e)
		}
	}
}

func TestReplayDiverges(t *testing.T) {
	transcript, _, _ := record(t, false)
	tr, rp, err := hidio.OpenReplay(strings.NewReader(transcript), hidio.NewGuard(wire.Mouse))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); err != nil {
		t.Fatal(err)
	}
	err = tr.Write(mustRead(t, 0))
	if !errors.Is(err, hidio.ErrDiverged) || !strings.Contains(err.Error(), "differs at bytes") {
		t.Fatalf("Write = %v, want a divergence", err)
	}
	if !errors.Is(rp.Diverged(), hidio.ErrDiverged) || rp.Remaining() == 0 {
		t.Fatalf("Diverged %v, remaining %d", rp.Diverged(), rp.Remaining())
	}
	if err := tr.Write(handshake(1)); !errors.Is(err, hidio.ErrDiverged) {
		t.Fatalf("Write after a divergence = %v", err)
	}
}

func TestReplayPastTheEnd(t *testing.T) {
	const transcript = `{"transcript":1,"started":"2026-09-26T10:00:00Z"}` + "\n"
	tr, _, err := hidio.OpenReplay(strings.NewReader(transcript), hidio.NewGuard(wire.Mouse))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); !errors.Is(err, hidio.ErrDiverged) {
		t.Fatalf("Write = %v, want a divergence", err)
	}
}

func TestReplayedStallClassifies(t *testing.T) {
	const transcript = `{"transcript":1,"started":"2026-09-26T10:00:00Z"}
{"t":"2026-09-26T10:00:00Z","dir":"out","id":8,"data":"03 00 00 00 00 00 00 00 00 00 00 00 00 00 00 4a"}
{"t":"2026-09-26T10:00:02Z","dir":"out-error","err":"hidio: write stalled"}
`
	tr, _, err := hidio.OpenReplay(strings.NewReader(transcript), hidio.NewGuard(wire.Mouse))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); !errors.Is(err, hidio.ErrStalled) {
		t.Fatalf("Write = %v, want the recorded stall", err)
	}
}

// checkRedacted fails a transcript that RedactTranscript would still change.
func checkRedacted(r io.Reader) error {
	h, entries, err := hidio.ReadTranscript(r)
	if err != nil {
		return err
	}
	if !h.Redacted || h.Started.Location() != time.UTC {
		return fmt.Errorf("header %+v is not marked redacted in UTC", h)
	}
	for i, e := range entries {
		again := hidio.Redact(e)
		if !again.At.Equal(e.At) || e.At.Location() != time.UTC || again.Data.Mask != e.Data.Mask || !slices.Equal(again.Data.Bytes, e.Data.Bytes) {
			return fmt.Errorf("entry %d (%s %s) is not redacted", i+1, e.Dir, e.Data)
		}
	}
	return nil
}

// Committed transcripts are public: each must be a redacted copy.
func TestCommittedTranscriptsAreRedacted(t *testing.T) {
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "testdata", "transcripts", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkRedacted(bytes.NewReader(b)); err != nil {
			t.Errorf("%s: %v; commit only copies made with 'arcctl redact'", f, err)
		}
	}
	plain, _, _ := record(t, false)
	if checkRedacted(strings.NewReader(plain)) == nil {
		t.Error("checkRedacted accepts an unredacted transcript")
	}
	var red bytes.Buffer
	if err := hidio.RedactTranscript(&red, strings.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	if err := checkRedacted(&red); err != nil {
		t.Errorf("checkRedacted refuses a redacted copy: %v", err)
	}
}
