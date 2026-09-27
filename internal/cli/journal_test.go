package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// addWritable plugs in a device that journal recovery may write to. When the
// test ends it checks that nothing the Edit policy refuses reached it.
func (h *harness) addWritable(c emu.Config) *emu.Device {
	h.t.Helper()
	d, err := h.bus.Add(c)
	must(h.t, err)
	h.t.Cleanup(func() {
		for _, w := range d.Writes() {
			if err := wire.Edit.Check(w.Packet, wire.Mouse); err != nil {
				h.t.Errorf("packet %v reached the device: %v", w.Packet, err)
			}
		}
	})
	return d
}

// crash applies edits to d in a session of its own, as the TUI does, and
// kills the write when kill says so, which leaves its run open in the
// journal. The mouse sleeps from the kill until that session has stopped, so
// the session cannot look at the journal again.
func (h *harness) crash(d *emu.Device, kill func(plan.Plan, safety.OpEvent) bool, edits ...mouse.Edit) (string, plan.Plan) {
	t := h.t
	t.Helper()
	s := session.New(session.Options{Devices: h.bus, Timing: fast(), Writes: &session.Writes{
		Journal:  h.paths.Journal,
		Backups:  backup.Store{Root: h.paths.Backups, Tool: "arcctl test", Source: backup.SourceDevice, Now: func() time.Time { return now.Add(-time.Hour) }},
		Lock:     func() error { return nil },
		Executor: execOptions(),
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() {
		cancel()
		<-done
		d.Wake()
	}()
	sn, err := session.Await(ctx, s, func(sn *session.Snapshot) bool { return sn.State == session.Ready && sn.Progress.Job == "" })
	must(t, err)
	var profile *byte
	if sn.Profile.Supported {
		profile = &sn.Profile.Value
	}
	p, err := mouse.PlanEdits(sn.Model, sn.Image, edits, mouse.Options{
		Device: sn.Identity, Profile: profile, Firmware: sn.Versions.Mouse, Verified: catalog.VerifiedStages(),
	})
	must(t, err)
	var run string
	gates := safety.Gates{AllowUntested: true, Experimental: true, Confirm: safety.ConfirmPhrase(p.Ops)}
	_, err = s.Apply(ctx, p, gates, func(e safety.OpEvent) {
		run = e.Run
		if kill(p, e) {
			d.Sleep()
			panic("killed")
		}
	})
	if !errors.Is(err, session.ErrPanic) || run == "" {
		t.Fatalf("apply: %v, run %q; want it killed", err, run)
	}
	return run, p
}

var cmdAltTab = keys.Combo{keys.LMeta.Stroke(), keys.LAlt.Stroke(), {Kind: keys.KindKey, Value: 0x2B}}

// tornEdits change DPI stage 1 and rewrite the shortcut of slot 4, which
// takes the two-phase path: disable the binding, write the body, bind again.
var tornEdits = []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 900}, mouse.SetShortcut{Slot: 4, Combo: cmdAltTab}}

// inBody kills the write after the first chunk of the shortcut body.
func inBody(_ plan.Plan, e safety.OpEvent) bool {
	return e.Kind == safety.EventChunk && e.Op.Phase == plan.Body && e.Chunk == 1
}

func afterPlan(t *testing.T, im *flash.Image, p plan.Plan) *flash.Image {
	t.Helper()
	out := im.Clone()
	for _, op := range p.Ops {
		must(t, out.Set(op.Extent.Addr, op.New))
	}
	return out
}

// holds fails unless d's flash holds want over every extent of p.
func holds(t *testing.T, d *emu.Device, want *flash.Image, p plan.Plan) {
	t.Helper()
	im := d.Image()
	for _, op := range p.Ops {
		got, _ := im.Get(op.Extent)
		w, _ := want.Get(op.Extent)
		if !bytes.Equal(got, w) {
			t.Errorf("%v holds % x, want % x", op.Extent, got, w)
		}
	}
}

func cmd7(d *emu.Device) int {
	n := 0
	for _, w := range d.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			n++
		}
	}
	return n
}

func withStderr(out, errs string) string {
	if errs == "" {
		return out
	}
	return out + "--- stderr\n" + errs
}

func TestJournalStatusWithoutJournal(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
	if out != "Journal $TMP/journal\nEmpty: arcctl has not written to any device.\n" {
		t.Errorf("status:\n%s", out)
	}
}

// A write killed in the middle of a shortcut body: status lists it, recover
// shows what each record holds and the choices, a dry run shows the packets
// and writes nothing, leaving it is refused, and finishing it writes what
// the run meant to leave and cleans the journal.
func TestJournalRecoverForward(t *testing.T) {
	h := newHarness(t)
	start := richImage(t, h.root)
	d := h.addWritable(receiver(em11Mouse(t, start.Clone())))
	_, p := h.crash(d, inBody, tornEdits...)
	sent := cmd7(d)

	out, errs, code := h.run("journal", "status")
	expect(t, code, cli.ExitVerify, errs)
	golden(t, "journal-status-torn", withStderr(out, errs))

	out, errs, code = h.run("journal", "recover")
	expect(t, code, cli.ExitVerify, errs)
	golden(t, "journal-recover-choices", withStderr(out, errs))

	out, errs, code = h.run("--dry-run", "journal", "recover", "--forward")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "journal-recover-dry-run", withStderr(out, errs))
	if cmd7(d) != sent {
		t.Fatal("the dry run wrote to the mouse")
	}

	out, errs, code = h.run("journal", "recover", "--leave")
	expect(t, code, cli.ExitVerify, errs)
	if !strings.Contains(errs, "a record is torn") || cmd7(d) != sent {
		t.Errorf("leave:\n%s", withStderr(out, errs))
	}

	out, errs, code = h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "journal-recover-forward", withStderr(out, errs))
	holds(t, d, afterPlan(t, start, p), p)

	list, err := backup.List(h.paths.Backups)
	must(t, err)
	var labels []string
	for _, l := range list {
		must(t, l.Err)
		labels = append(labels, l.File.Label)
	}
	if strings.Join(labels, ",") != session.LabelFirstWrite+","+session.LabelBeforeWrite {
		t.Errorf("backups %v; want the crashed session's full one, then the recovery's", labels)
	}

	out, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "journal-status-clean", withStderr(out, errs))

	out, errs, code = h.run("journal", "recover")
	expect(t, code, cli.ExitOK, errs)
	if !strings.HasSuffix(out, "Nothing to recover: the journal of this mouse is clean.\n") {
		t.Errorf("recover on a clean journal:\n%s", out)
	}
}

// Rolling back puts every record back as it was before the run, and leaves
// nothing to revert.
func TestJournalRecoverBack(t *testing.T) {
	h := newHarness(t)
	start := richImage(t, h.root)
	d := h.addWritable(receiver(em11Mouse(t, start.Clone())))
	run, p := h.crash(d, inBody, tornEdits...)

	out, errs, code := h.run("journal", "recover", "--back", "--run", run)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "journal-recover-back", withStderr(out, errs))
	holds(t, d, start, p)

	out, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(out, "Clean: nothing to recover.\n  Last change: none\n") {
		t.Errorf("status after rolling back:\n%s", out)
	}
}

// A run killed after its last record was verified left the mouse as it
// meant to: recover settles it without writing.
func TestJournalRecoverSettlesOnLoad(t *testing.T) {
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	h.crash(d, func(p plan.Plan, e safety.OpEvent) bool {
		return e.Kind == safety.EventVerified && e.Op.Seq == len(p.Ops)
	}, tornEdits...)
	sent := cmd7(d)

	out, errs, code := h.run("journal", "recover", "--back")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "journal-recover-settled", withStderr(out, errs))
	if cmd7(d) != sent {
		t.Error("settling a finished run wrote")
	}
}

// The preflight blocks a recovery while another program holds the receiver
// or the screen is locked, and lists every reason; --allow-foreign-client
// lifts the first.
func TestJournalRecoverBlocked(t *testing.T) {
	h := newHarness(t)
	start := richImage(t, h.root)
	d := h.addWritable(receiver(em11Mouse(t, start.Clone())))
	_, p := h.crash(d, inBody, tornEdits...)
	sent := cmd7(d)
	d.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
	h.host.console = platform.Console{ScreenLocked: true}

	out, errs, code := h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitBlocked, errs)
	golden(t, "journal-recover-blocked", withStderr(out, errs))

	h.host.console = platform.Console{}
	_, errs, code = h.run("journal", "recover", "--forward", "--allow-foreign-client")
	expect(t, code, cli.ExitOK, errs)
	if cmd7(d) == sent {
		t.Error("the recovery wrote nothing")
	}
	holds(t, d, afterPlan(t, start, p), p)
}

// A recovery that stops leaves a recovery attempt in the journal; the next
// one settles both.
func TestJournalRecoveryStops(t *testing.T) {
	h := newHarness(t)
	start := richImage(t, h.root)
	d := h.addWritable(receiver(em11Mouse(t, start.Clone())))
	_, p := h.crash(d, inBody, tornEdits...)
	d.Inject(emu.Fault{Cmd: wire.CmdWrite, Action: emu.NAK, Times: 1})

	out, errs, code := h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitVerify, errs)
	golden(t, "journal-recover-stopped", withStderr(out, errs))

	out, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitVerify, errs)
	if !strings.Contains(out, "    recovery $RUN2\n") {
		t.Errorf("status lacks the stopped recovery:\n%s", out)
	}

	_, errs, code = h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitOK, errs)
	holds(t, d, afterPlan(t, start, p), p)
	_, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
}

// An interrupt during a recovery lets the record being written finish, then
// stops before the next one.
func TestJournalRecoverInterrupted(t *testing.T) {
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	h.crash(d, inBody, tornEdits...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	d.Inject(emu.Fault{Cmd: wire.CmdWrite, Match: func(wire.Packet) bool {
		once.Do(cancel)
		return false
	}})

	out, errs, code := h.runCtx(ctx, "journal", "recover", "--forward")
	expect(t, code, cli.ExitAborted, errs)
	golden(t, "journal-recover-interrupted", withStderr(out, errs))
}

// While another arcctl holds the lock, a run it is writing looks unfinished;
// status says so.
func TestJournalStatusWhileLocked(t *testing.T) {
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	h.crash(d, inBody, tornEdits...)
	l, err := platform.AcquireLock(h.paths.Lock)
	must(t, err)
	defer l.Release()
	out, errs, code := h.run("journal", "status")
	expect(t, code, cli.ExitVerify, errs)
	if want := "Another arcctl (pid $PID) is running: a run it is still writing shows here as\nunfinished.\n"; !strings.Contains(out, want) {
		t.Errorf("status lacks %q:\n%s", want, out)
	}
}

func TestJournalRecoverWithoutMouse(t *testing.T) {
	h := newHarness(t)
	m := em11Mouse(t, richImage(t, h.root))
	m.Asleep = true
	h.add(receiver(m))
	_, errs, code := h.run("--wait", "100ms", "journal", "recover", "--forward")
	expect(t, code, cli.ExitOffline, errs)
}

func TestJournalStatusCorrupt(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.paths.Journal, "260d-1282-7b04")
	must(t, os.MkdirAll(dir, 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "20260926T120000.000000000Z-1.jsonl"), []byte(`{"type":"op"}`+"\n"), 0o600))
	out, errs, code := h.run("journal", "status")
	expect(t, code, cli.ExitFailure, errs)
	golden(t, "journal-status-corrupt", withStderr(out, errs))
}

// The mouse falling asleep in the middle of a recovery pauses it; once the
// mouse wakes, the record is written again from its first chunk.
func TestJournalRecoverPausesWhileAsleep(t *testing.T) {
	h := newHarness(t)
	start := richImage(t, h.root)
	d := h.addWritable(receiver(em11Mouse(t, start.Clone())))
	_, p := h.crash(d, inBody, tornEdits...)
	var asleep atomic.Bool
	d.Inject(emu.Fault{Cmd: wire.CmdWrite, Action: emu.Asleep, Times: 1, Match: func(wire.Packet) bool {
		asleep.Store(true)
		return true
	}})
	// The cmd 3 that finds the mouse asleep is answered before the mouse
	// wakes: faults are counted, and packets answered, under the bus lock.
	offline := make(chan struct{}, 1)
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Match: func(wire.Packet) bool {
		if asleep.Load() {
			select {
			case offline <- struct{}{}:
			default:
			}
		}
		return false
	}})
	go func() {
		<-offline
		d.Wake()
	}()

	out, errs, code := h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitOK, errs)
	for _, want := range []string{
		"arcctl: the mouse went to sleep; waiting up to 300ms for it to wake (move it)\n",
		"arcctl: the mouse answers again; the write goes on\n",
		"arcctl: writing 384+20 again from its first chunk\n",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, withStderr(out, errs))
		}
	}
	holds(t, d, afterPlan(t, start, p), p)
}

// A run written on another onboard profile is neither inspected nor
// recovered until the mouse is back on that profile (I11).
func TestJournalRecoverOtherProfile(t *testing.T) {
	h := newHarness(t)
	start := richImage(t, h.root)
	d := h.addWritable(receiver(em11Mouse(t, start.Clone())))
	_, p := h.crash(d, inBody, tornEdits...)
	must(t, d.SwitchProfile(1))
	sent := cmd7(d)

	out, errs, code := h.run("journal", "recover")
	expect(t, code, cli.ExitVerify, errs)
	flat := strings.Join(strings.Fields(out+errs), " ")
	for _, want := range []string{
		"its records could not be read again: the onboard profile differs: run $RUN1 was written to profile 0, the active one is 1",
		"arcctl: the records of the run could not be read again",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("output lacks %q:\n%s", want, withStderr(out, errs))
		}
	}

	_, errs, code = h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitFailure, errs)
	if want := "- the run was written on onboard profile 0, and the mouse is on another one now; switch it back to 0, then retry"; !strings.Contains(strings.Join(strings.Fields(errs), " "), want) {
		t.Errorf("stderr lacks %q:\n%s", want, errs)
	}
	if cmd7(d) != sent {
		t.Fatal("a recovery on another profile wrote")
	}

	must(t, d.SwitchProfile(0))
	_, errs, code = h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitOK, errs)
	holds(t, d, afterPlan(t, start, p), p)
}

// A recovery that waits for a mouse that stays asleep gives up with the
// exit code of a sleeping mouse (PLAN §8: 4), not a failed verify.
func TestJournalRecoverGivesUpOnASleepingMouse(t *testing.T) {
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	h.crash(d, inBody, tornEdits...)
	d.Inject(emu.Fault{Cmd: wire.CmdWrite, Action: emu.Asleep, Times: 1})
	_, errs, code := h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitOffline, errs)
	if !strings.Contains(strings.Join(strings.Fields(errs), " "), "the mouse is offline") {
		t.Errorf("stderr does not say the mouse sleeps:\n%s", errs)
	}
}

// A browser that opens the receiver while the recovery waits for the mouse
// stops it before its next packet, with the exit code of a blocked device.
func TestJournalRecoverStopsForAClientThatCameDuringAPause(t *testing.T) {
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	h.crash(d, inBody, tornEdits...)
	var asleep atomic.Bool
	d.Inject(emu.Fault{Cmd: wire.CmdWrite, Action: emu.Asleep, Times: 1, Match: func(wire.Packet) bool {
		asleep.Store(true)
		return true
	}})
	offline := make(chan struct{}, 1)
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Match: func(wire.Packet) bool {
		if asleep.Load() {
			select {
			case offline <- struct{}{}:
			default:
			}
		}
		return false
	}})
	var woke atomic.Bool
	go func() {
		<-offline
		d.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
		d.Wake()
		woke.Store(true)
	}()
	var sent atomic.Int32
	d.Inject(emu.Fault{Cmd: wire.CmdWrite, Match: func(wire.Packet) bool {
		if woke.Load() {
			sent.Add(1)
		}
		return false
	}})
	_, errs, code := h.run("journal", "recover", "--forward")
	expect(t, code, cli.ExitBlocked, errs)
	if !strings.Contains(strings.Join(strings.Fields(errs), " "), "another program has the receiver open") {
		t.Errorf("stderr does not name the other client:\n%s", errs)
	}
	if n := sent.Load(); n > 0 {
		t.Errorf("%d cmd 7 after the browser opened the receiver", n)
	}
}

// A factory reset whose process ended before its check shows in journal
// status as unchecked, and journal recover checks it: the mouse read again
// and compared with the backup the run names.
func TestJournalChecksAnInterruptedReset(t *testing.T) {
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	path := filepath.Join(h.tmp, "before.json")
	_, errs, code := h.run("backup", "--full", "-o", path)
	expect(t, code, cli.ExitOK, errs)
	f, err := backup.Load(path)
	must(t, err)
	j, err := safety.OpenJournal(h.paths.Journal, f.Identity())
	must(t, err)
	defer j.Close()
	var profile *byte
	if p := f.Device.Profile; p != nil && p.Supported {
		profile = &p.Value
	}
	reset := wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil)
	_, _, err = safety.SendReset(context.Background(), j, f.Identity(), profile, path, reset, func(context.Context, wire.Packet) (wire.Packet, error) {
		return reset, d.Store(mouse.AddrCurrentDPI, []byte{0x00, 0x55})
	})
	must(t, err)

	out, errs, code := h.run("journal", "status")
	if code != cli.ExitVerify || !strings.Contains(out, "Unchecked reset $RUN1") ||
		!strings.Contains(out, "compares it with the backup taken before") {
		t.Fatalf("status: exit %d\n%s%s", code, out, errs)
	}
	out, errs, code = h.run("journal", "recover")
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(out, "Checked reset") || !strings.Contains(out, "it changed 4+2") {
		t.Errorf("recover:\n%s", out)
	}
	out, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(out, "Clean: nothing to recover.") {
		t.Errorf("status after the check:\n%s", out)
	}
}
