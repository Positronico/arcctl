//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// checkWrites fails on any packet that reached the device and that neither
// the Edit policy nor the probe accounts for.
func (r *rig) checkWrites(probes int) {
	r.t.Helper()
	bad := 0
	for _, w := range r.dev.Writes() {
		err := wire.Edit.Check(w.Packet, wire.Mouse)
		switch {
		case err == nil:
		case errors.Is(err, wire.ErrChecksum) && w.Packet.Cmd() == wire.CmdWrite:
			bad++
		default:
			r.t.Errorf("packet %v reached the device: %v", w.Packet, err)
		}
	}
	if bad != probes {
		r.t.Errorf("%d packets with a bad checksum reached the device, want %d", bad, probes)
	}
}

func (r *rig) unchanged() {
	r.t.Helper()
	if !bytes.Equal(r.dev.Image().Bytes(), r.start.Bytes()) {
		r.t.Error("the stage left the device changed")
	}
}

func (r *rig) passed(res *Result) {
	r.t.Helper()
	if !res.Passed || res.Err != nil {
		for _, s := range res.Steps {
			r.t.Logf("%s: %v %v", s.Title, s.OK, s.Detail)
		}
		for _, a := range res.Answers {
			r.t.Logf("%s: %s (%v)", a.ID, a.Answer, a.OK)
		}
		r.t.Fatalf("stage %s did not pass: %v", res.Stage, res.Err)
	}
}

func (r *rig) verified() []verification {
	r.t.Helper()
	var vs []verification
	must(r.t, json.Unmarshal([]byte(r.read(verifiedPath)), &vs))
	return vs
}

func strs(ps []wire.Packet) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

func TestH1RunsAndPromotes(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h1.run", "h1.speed", "h1.speed-back").set("h1.confirm", "write untested")
	res := r.run("H1")
	r.passed(res)
	r.unchanged()
	r.checkWrites(1)

	got := strs(r.logicalCmd7())
	wantSeq := []string{
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb",
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 ea",
		"07 00 00 04 02 02 53 00 00 00 00 00 00 00 00 eb",
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb",
	}
	if !slices.Equal(got, wantSeq) {
		t.Fatalf("cmd 7 sent\n got %v\nwant %v", got, wantSeq)
	}
	joined := strings.Join(res.Findings, "\n")
	for _, f := range []string{"one reply, the request itself", "wrong packet checksum gets a NAK (status 1)"} {
		if !strings.Contains(joined, f) {
			t.Errorf("findings lack %q:\n%s", f, joined)
		}
	}
	if strings.Contains(joined, "came after it stopped listening") {
		t.Errorf("the emulator answers every packet once, yet H1 found late replies:\n%s", joined)
	}
	if vs := r.verified(); len(vs) != 1 || vs[0] != (verification{"7B04", "dpi.current", "v0.42", "H1", "2026-09-27"}) {
		t.Errorf("verified.json: %+v", vs)
	}
	if r.generated != 1 || len(res.Promoted) != 1 {
		t.Errorf("generated %d times, promoted %v", r.generated, res.Promoted)
	}
	r.checkRedacted(res)
	log := r.read(logPath)
	for _, s := range []string{"### 2026-09-27 H1 (settings pairs, echo and NAK behaviour): passed", "| H1 | settings pairs, echo and NAK behaviour | passed 2026-09-27 (v0.42) |",
		"- Promoted: `dpi.current` for 7B04 on v0.42", "`testdata/transcripts/2026-09-27-h1.jsonl`"} {
		if !strings.Contains(log, s) {
			t.Errorf("the log lacks %q:\n%s", s, log)
		}
	}
	if strings.Contains(log, noRuns) {
		t.Error("the log still says no runs")
	}
	if len(res.Backups) != 1 {
		t.Fatalf("backups %v", res.Backups)
	}
	f, err := backup.Load(res.Backups[0])
	must(t, err)
	if !f.Full || len(f.Missing()) != 0 || f.Label != "hwtest H1 before" {
		t.Errorf("the fresh backup: full %v, missing %v, label %q", f.Full, f.Missing(), f.Label)
	}
}

// A second reply to every cmd 7, as H1 asks about, is recorded and kept
// from the session, which never sees a reply it did not ask for.
func TestH1SeesDuplicates(t *testing.T) {
	r := newRig(t, func(c *emu.Config) { c.Behavior.DoubleWrite = true })
	r.script.yes("h1.run", "h1.speed", "h1.speed-back").set("h1.confirm", "write untested")
	res := r.run("H1")
	r.passed(res)
	if !strings.Contains(strings.Join(res.Findings, "\n"), "cmd 7 at 4: 2 replies") {
		t.Errorf("findings %v", res.Findings)
	}
}

func TestH2Runs(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h2.run", "h2.stage1", "h2.count").set("h2.confirm", "write untested")
	res := r.run("H2")
	r.passed(res)
	r.unchanged()
	r.checkWrites(0)
	vs := r.verified()
	if len(vs) != 2 || vs[0].Feature != "dpi.stages" || vs[1].Feature != "dpi.value" {
		t.Errorf("verified.json %+v", vs)
	}
}

func TestH3RunsWithTheRevert(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h3.run", "h3.inert", "h3.scroll", "h3.restored").set("h3.confirm", "write experimental")
	res := r.run("H3")
	r.passed(res)
	r.unchanged()
	r.checkWrites(0)
	if vs := r.verified(); len(vs) != 1 || vs[0].Feature != "button.system" {
		t.Errorf("verified.json %+v", vs)
	}
}

func TestH3bRecordsTheMap(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h3b.run").set("h3b.confirm", "write experimental")
	names := map[int]string{6: "none", 7: "DPI button", 8: "wheel tilt left", 9: "wheel tilt right", 10: "top front", 11: "top back"}
	for slot, n := range names {
		r.script.set(fmt.Sprintf("h3b.slot%d", slot), n)
	}
	res := r.run("H3b")
	r.passed(res)
	r.unchanged()
	if len(res.Promoted) != 1 || res.Promoted[0].Feature != string(mouse.FeatureUnmappedSlot) || r.generated != 1 {
		t.Errorf("H3b promoted %v, generated %d times", res.Promoted, r.generated)
	}
	log := r.read(logPath)
	if !strings.Contains(log, "wheel tilt left") || !strings.Contains(log, "DPI button") {
		t.Errorf("the log lacks the slot map:\n%s", log)
	}
}

// Without --allow-untested the stage stops before its first write.
func TestMissingFlagsStopBeforeWriting(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Gates = safety.Gates{}
	res := r.run("H1")
	if res.Passed || !errors.Is(res.Err, ErrFlags) || !strings.Contains(res.Err.Error(), "--allow-untested") {
		t.Fatalf("passed %v, err %v", res.Passed, res.Err)
	}
	if n := len(r.cmd7()); n != 0 {
		t.Fatalf("%d cmd-7 packets sent", n)
	}
	if res.Recorded || strings.Contains(r.read(logPath), "### 2026-09-27 H1") || len(res.Transcripts) != 0 {
		t.Error("a stage that never started is in the records")
	}
}

func TestWrongConfirmationWritesNothing(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h1.run").set("h1.confirm", "write")
	res := r.run("H1")
	if !errors.Is(res.Err, ErrConfirm) || len(r.cmd7()) != 0 || r.generated != 0 || res.Recorded {
		t.Fatalf("err %v, %d cmd-7 packets, generated %d", res.Err, len(r.cmd7()), r.generated)
	}
}

// An unexpected answer fails the stage, and the steps still undo every
// change.
func TestUnexpectedAnswerStillReverts(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h3.run", "h3.inert", "h3.restored").set("h3.scroll", false).set("h3.confirm", "write experimental")
	res := r.run("H3")
	if res.Passed || res.Err != nil {
		t.Fatalf("passed %v, err %v", res.Passed, res.Err)
	}
	r.unchanged()
	if len(res.Promoted) != 0 || r.generated != 0 || len(r.verified()) != 0 {
		t.Error("a failed stage promoted")
	}
	if !strings.Contains(r.read(logPath), "no (not the expected answer)") {
		t.Error("the log does not flag the answer")
	}
}

// When the user's input ends after a write, the owed revert still runs.
func TestStoppedStageUndoesItsWrite(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h3.run", "h3.inert").set("h3.confirm", "write experimental")
	r.script.eof = true
	res := r.run("H3")
	if res.Passed || !errors.Is(res.Err, ErrNoInput) {
		t.Fatalf("passed %v, err %v", res.Passed, res.Err)
	}
	r.unchanged()
}

func TestRehearsalPromotesNothing(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Source = backup.SourceEmulator
	r.cfg.Repo = filepath.Join(t.TempDir(), "rehearsal")
	r.repo = r.cfg.Repo
	r.script.yes("h1.run", "h1.speed", "h1.speed-back").set("h1.confirm", "write untested")
	res := r.run("H1")
	if !res.Passed || !res.Rehearsal || len(res.Promoted) != 0 || r.generated != 0 {
		t.Fatalf("passed %v, rehearsal %v, promoted %v", res.Passed, res.Rehearsal, res.Promoted)
	}
	if log := r.read(logPath); !strings.Contains(log, "a rehearsal, not a hardware result") {
		t.Errorf("log:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(r.repo, filepath.FromSlash(verifiedPath))); err == nil {
		t.Error("a rehearsal wrote verified.json")
	}
}

func TestRefusesAnotherModel(t *testing.T) {
	r := newRig(t, func(c *emu.Config) {
		m, _ := catalog.ByKey("7B01")
		c.Mouse.Model = m
		c.Mouse.Image = nil
	})
	r.script.yes("h1.run")
	if res := r.run("H1"); !errors.Is(res.Err, ErrModel) {
		t.Fatalf("H1 on an EM25: %v", res.Err)
	}
}

// --debug-abort-after-chunk ends the process right after the chunk; the
// journal then settles the write on the next start.
func TestDebugAbortLeavesTheJournalToSettle(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h1.run").set("h1.confirm", "write untested")
	type exit struct{ code int }
	var code int
	r.cfg.AbortAfterChunk = 1
	r.cfg.Exit = func(c int) {
		code = c
		panic(exit{c})
	}
	res := r.run("H1")
	if code != abortCode || res.Passed || res.Recorded || !errors.Is(res.Err, ErrEnded) {
		t.Fatalf("exit %d, passed %v, recorded %v, err %v", code, res.Passed, res.Recorded, res.Err)
	}
	cur, _ := r.dev.Image().Get(pairExtent(4))
	if !bytes.Equal(cur, []byte{0x02, 0x53}) {
		t.Fatalf("the device holds % x at 4+2", cur)
	}
	opt := r.cfg.Session
	opt.Devices = newDevices(r.bus)
	s := session.New(opt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	sn, err := session.Await(wctx, s, func(sn *session.Snapshot) bool {
		return sn.State == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
	})
	if err != nil || len(sn.Journal.Open) != 0 || sn.Journal.Last == nil || sn.Journal.Last.Resolved != safety.Forward {
		t.Fatalf("after the restart: %v, journal %+v", err, sn.Journal)
	}
}

// H0 end to end: the physical steps are acted out on the emulator, and the
// stage never writes.
func TestH0RunsReadOnly(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.TraceFor = 300 * time.Millisecond
	var chrome *emu.Competitor
	var cable *emu.Device
	s := r.script
	s.yes("h0.run").set("h0.revoke", false).set("h0.typed", confirmWord).
		set("h0.dump", "the DPI button pressed during the trace")
	s.on("h0.trace-dpi", func() { must(t, r.dev.PressDPI()) }).
		on("h0.trace-sleep", r.dev.Sleep).
		on("h0.trace-wake", r.dev.Wake).
		on("h0.sleep", r.dev.Sleep).
		on("h0.wake", r.dev.Wake).
		on("h0.unplug", r.dev.Unplug).
		on("h0.plug", r.dev.Plug).
		on("h0.hub", func() { chrome = r.dev.Chrome(2 * time.Millisecond) }).
		on("h0.hub-close", func() { chrome.Close() }).
		on("h0.cable", func() {
			var err error
			cable, err = r.bus.Add(emu.Config{Mouse: &emu.Mouse{Model: em11Model(t), Image: dumpImage(t), Firmware: testFirmware}})
			must(t, err)
		}).
		on("h0.cable-off", func() { cable.Unplug() })
	res := r.run("H0")
	r.passed(res)
	for _, w := range r.dev.Writes() {
		if err := wire.ReadOnly.Check(w.Packet, wire.Mouse); err != nil {
			t.Errorf("H0 sent %v: %v", w.Packet, err)
		}
	}
	joined := strings.Join(res.Findings, "\n")
	for _, f := range []string{
		"trace: DPI button -> cmd 10 flags 01 00",
		"info: mid 4",
		"latency: 200 reads of 96+10",
		"full backups took",
		"host traffic kept the mouse awake: yes",
		"0..256 differs from flash-dump.bin at 4+2: the DPI button pressed during the trace",
		"interfaces answering cmd 3: 1",
		"50 open/close cycles: 0 without an answer",
		"cmd-3 address across sleep and wake: unchanged",
		"unplug while idle: seen yes; address across a replug unchanged",
		"replies are broadcast",
		"the scan names Google Chrome Helper; a session ends up conflict",
		"with the cable in, 2 interfaces answer cmd 3",
		"a typed confirmation in the CLI leaves Secure Input off",
		"two full backups identical: yes; 0..256 equals flash-dump.bin or explained: yes; no reply lost beyond the retries: yes",
	} {
		if !strings.Contains(joined, f) {
			t.Errorf("findings lack %q", f)
		}
	}
	if len(res.Transcripts) != 2 || !strings.HasSuffix(res.Transcripts[0], "-h0-info.jsonl") || !strings.HasSuffix(res.Transcripts[1], "-h0.jsonl") {
		t.Errorf("transcripts %v", res.Transcripts)
	}
	r.checkRedacted(res)
	if len(res.Promoted) != 0 || r.generated != 0 {
		t.Error("H0 promoted")
	}
	if len(res.Backups) != 2 {
		t.Errorf("backups %v", res.Backups)
	}
	if !strings.Contains(r.read(logPath), "### 2026-09-27 H0 (read-only session, latency, coexistence): passed") {
		t.Error("H0 is not logged")
	}
}

// The info transcript H0 commits plays back through a session like the
// device it was recorded from.
func TestH0InfoTranscriptReplays(t *testing.T) {
	r := newRig(t, nil)
	// A replay follows the recorded packets one by one, so a resend on either
	// side diverges; both sessions wait for replies as long as on hardware.
	hw := session.DefaultTiming()
	r.cfg.Session.Timing.Try, r.cfg.Session.Timing.ProbeTry = hw.Try, hw.ProbeTry
	r.script.eof = true
	r.script.yes("h0.run")
	for _, id := range []string{"h0.trace-dpi", "h0.trace-sleep", "h0.trace-wake", "h0.trace-lock"} {
		r.script.on(id, func() {})
	}
	res := r.run("H0")
	if len(res.Transcripts) == 0 || !strings.HasSuffix(res.Transcripts[0], "-h0-info.jsonl") {
		t.Fatalf("transcripts %v (err %v)", res.Transcripts, res.Err)
	}
	data := []byte(r.read(res.Transcripts[0]))
	c := r.dev.Candidates()[1]
	c.Backend = "replay"
	devs := &replayDevices{c: c, data: data}
	s := session.New(session.Options{Devices: devs, Device: c.Path, Timing: r.cfg.Session.Timing})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	sn, err := session.Await(wctx, s, func(sn *session.Snapshot) bool { return sn.State == session.Ready && sn.Progress.Job == "" })
	if err != nil {
		t.Fatalf("the replay did not reach Ready: %v (state %v, err %v)", err, sn.State, sn.Err)
	}
	if err := devs.rp.Diverged(); err != nil {
		t.Fatal(err)
	}
	if sn.Model == nil || sn.Model.Key != em11 || sn.Versions.Mouse != testFirmware.String() {
		t.Errorf("replayed %v %+v", sn.Model, sn.Versions)
	}
}

type replayDevices struct {
	c    hidio.Candidate
	data []byte
	rp   *hidio.Replay
}

func (d *replayDevices) Enumerate() ([]hidio.Candidate, error) { return []hidio.Candidate{d.c}, nil }

func (d *replayDevices) Open(c hidio.Candidate, g *hidio.Guard, _ *hidio.Recorder) (hidio.Transport, error) {
	tr, rp, err := hidio.OpenReplay(bytes.NewReader(d.data), g)
	d.rp = rp
	return tr, err
}
