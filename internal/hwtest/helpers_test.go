//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

var testTime = time.Date(2026, 9, 27, 14, 2, 3, 0, time.UTC)

// testFirmware is a version no hardware test will record, so what the
// committed verified.json holds never changes the tiers these tests see.
var testFirmware = emu.Version{Major: 0, Minor: 0x42}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func em11Model(t testing.TB) *catalog.Model {
	t.Helper()
	m, ok := catalog.ByKey(em11)
	if !ok {
		t.Fatal("no EM11 Pro in the catalog")
	}
	return m
}

func moduleRoot(t testing.TB) string {
	t.Helper()
	root, err := vectors.ModuleRoot(".")
	must(t, err)
	return root
}

// dumpImage is the committed settings page with placeholder bodies in the
// slots it binds.
func dumpImage(t testing.TB) *flash.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), "testdata", "flash-dump.bin"))
	must(t, err)
	im, err := flash.FromDump(0, b)
	must(t, err)
	im, err = emu.WithBodies(im)
	must(t, err)
	return im
}

func fast() session.Timing {
	return session.Timing{
		Try:           25 * time.Millisecond,
		ProbeTry:      25 * time.Millisecond,
		Window:        150 * time.Millisecond,
		Debounce:      5 * time.Millisecond,
		Rescan:        20 * time.Millisecond,
		RescanMax:     50 * time.Millisecond,
		Retry:         20 * time.Millisecond,
		RetryMax:      50 * time.Millisecond,
		Offline:       40 * time.Millisecond,
		Online:        time.Hour,
		Battery:       time.Hour,
		Suspect:       30 * time.Millisecond,
		ConflictQuiet: 100 * time.Millisecond,
		LoadWatchdog:  10 * time.Second,
	}
}

// rig is an emulated EM11 Pro holding the committed dump, and everything a
// stage needs around it: a checkout with the log and verified.json, and
// folders for the journal, the backups and the transcripts.
type rig struct {
	t         *testing.T
	bus       *emu.Bus
	dev       *emu.Device
	start     *flash.Image
	repo      string
	generated int
	script    *script
	cfg       Config
}

func newRig(t *testing.T, tweak func(*emu.Config)) *rig {
	t.Helper()
	b := emu.New(emu.Options{})
	t.Cleanup(b.Close)
	c := emu.Config{
		RxVersion: &emu.Version{Major: 1, Minor: 2},
		Mouse: &emu.Mouse{
			Model:    em11Model(t),
			Image:    dumpImage(t),
			Firmware: testFirmware,
			Battery:  emu.Battery{Level: 80, MilliVolts: 3900},
		},
	}
	if tweak != nil {
		tweak(&c)
	}
	d, err := b.Add(c)
	must(t, err)
	r := &rig{t: t, bus: b, dev: d, start: d.Image(), repo: t.TempDir()}
	r.script = &script{t: t, answers: map[string]any{}, actions: map[string]func(){}}
	r.checkout()
	store := backup.Store{Root: t.TempDir(), Tool: "arcctl test", Source: backup.SourceEmulator}
	r.cfg = Config{
		Raw: b,
		Session: session.Options{
			Clients: clientsOf(b),
			Timing:  fast(),
			Writes: &session.Writes{
				Journal:  t.TempDir(),
				Backups:  store,
				Lock:     func() error { return nil },
				Executor: safety.Options{OfflineWait: time.Second, LockWait: time.Second, Poll: 5 * time.Millisecond},
			},
		},
		Host:     &fakeHost{bus: b},
		Prompt:   r.script,
		Repo:     r.repo,
		Logs:     t.TempDir(),
		Backups:  t.TempDir(),
		Source:   backup.SourceDevice,
		Tool:     "arcctl test",
		OS:       keys.Mac,
		Gates:    safety.Gates{AllowUntested: true, Experimental: true},
		Wait:     5 * time.Second,
		TraceFor: 50 * time.Millisecond,
		Poll:     20 * time.Millisecond,
		Listen:   50 * time.Millisecond,
		Exit:     func(int) { t.Error("the stage ended the process") },
		Generate: func(context.Context, string) error { r.generated++; return nil },
		Now:      func() time.Time { return testTime },
	}
	return r
}

// checkout makes a folder that passes for the arcctl checkout.
func (r *rig) checkout() {
	root := moduleRoot(r.t)
	copyFile := func(rel string) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		must(r.t, err)
		dst := filepath.Join(r.repo, rel)
		must(r.t, os.MkdirAll(filepath.Dir(dst), 0o755))
		must(r.t, os.WriteFile(dst, b, 0o644))
	}
	copyFile("go.mod")
	copyFile(logPath)
	copyFile("testdata/flash-dump.bin")
	must(r.t, os.MkdirAll(filepath.Join(r.repo, "internal", "catalog"), 0o755))
	must(r.t, os.WriteFile(filepath.Join(r.repo, filepath.FromSlash(verifiedPath)), []byte("[]\n"), 0o644))
}

func (r *rig) run(stage string) *Result {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, r.cfg, stage)
	if err != nil {
		r.t.Fatalf("Run(%s): %v", stage, err)
	}
	return res
}

// cmd7 lists the cmd-7 packets that reached the device.
func (r *rig) cmd7() []wire.Packet {
	var out []wire.Packet
	for _, w := range r.dev.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			out = append(out, w.Packet)
		}
	}
	return out
}

func (r *rig) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(r.repo, filepath.FromSlash(rel)))
	must(r.t, err)
	return string(b)
}

// checkRedacted fails unless every committed transcript is a redacted copy.
func (r *rig) checkRedacted(res *Result) {
	r.t.Helper()
	if len(res.Transcripts) == 0 {
		r.t.Fatal("no transcript committed")
	}
	for _, rel := range res.Transcripts {
		if !strings.HasPrefix(rel, transcriptsDir+"/2026-09-27-") {
			r.t.Errorf("transcript %s", rel)
		}
		h, entries, err := hidio.ReadTranscript(strings.NewReader(r.read(rel)))
		must(r.t, err)
		if !h.Redacted {
			r.t.Errorf("%s: header not redacted", rel)
		}
		for i, e := range entries {
			again := hidio.Redact(e)
			if again.Data.Mask != e.Data.Mask || !bytes.Equal(again.Data.Bytes, e.Data.Bytes) {
				r.t.Errorf("%s: entry %d is not redacted", rel, i+1)
			}
		}
	}
}

func clientsOf(b *emu.Bus) func(hidio.Candidate) ([]session.Client, error) {
	return func(c hidio.Candidate) ([]session.Client, error) {
		var out []session.Client
		for _, x := range b.Clients() {
			if x.Path == c.Path && x.PID != os.Getpid() {
				out = append(out, session.Client{PID: x.PID, Name: x.Process, Seized: x.Seized})
			}
		}
		return out, nil
	}
}

type fakeHost struct {
	bus    *emu.Bus
	secure string
}

func (h *fakeHost) Doctor(w io.Writer) (Doctor, error) {
	fmt.Fprintln(w, "doctor: all good")
	return Doctor{Access: "granted", App: "Terminal"}, nil
}

func (h *fakeHost) Console() (safety.Console, error) {
	return safety.Console{SecureInput: h.secure}, nil
}

func (h *fakeHost) Clients() ([]safety.Client, error) {
	var out []safety.Client
	for _, c := range h.bus.Clients() {
		if c.PID != os.Getpid() && !slices.ContainsFunc(out, func(x safety.Client) bool { return x.PID == c.PID }) {
			out = append(out, safety.Client{PID: c.PID, Name: c.Process})
		}
	}
	return out, nil
}

func (h *fakeHost) Permission() (string, error) { return "granted to Terminal", nil }

// script answers the stage's questions by id and acts out the physical
// steps.
type script struct {
	t       *testing.T
	eof     bool // unscripted questions end the input instead of failing the test
	mu      sync.Mutex
	answers map[string]any
	actions map[string]func()
	asked   []string
	said    strings.Builder
}

func (s *script) Say(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said.WriteString(text + "\n")
}

func (s *script) take(id string) (any, bool) {
	s.mu.Lock()
	v, ok := s.answers[id]
	act := s.actions[id]
	s.asked = append(s.asked, id)
	s.mu.Unlock()
	if act != nil {
		act()
	}
	return v, ok
}

func (s *script) Wait(id, text string) error {
	s.mu.Lock()
	_, scripted := s.actions[id]
	s.mu.Unlock()
	if s.eof && !scripted {
		return ErrNoInput
	}
	s.take(id)
	return nil
}

func (s *script) Ask(id, text string) (bool, error) {
	v, ok := s.take(id)
	b, isBool := v.(bool)
	if !ok || !isBool {
		s.unscripted(id, text)
		return false, ErrNoInput
	}
	return b, nil
}

func (s *script) Line(id, text string) (string, error) {
	v, ok := s.take(id)
	str, isStr := v.(string)
	if !ok || !isStr {
		s.unscripted(id, text)
		return "", ErrNoInput
	}
	return str, nil
}

func (s *script) unscripted(id, text string) {
	if !s.eof {
		s.t.Errorf("unscripted question %s: %s", id, text)
	}
}

func (s *script) set(id string, v any) *script {
	s.answers[id] = v
	return s
}

func (s *script) yes(ids ...string) *script {
	for _, id := range ids {
		s.answers[id] = true
	}
	return s
}

func (s *script) on(id string, f func()) *script {
	s.actions[id] = f
	return s
}
