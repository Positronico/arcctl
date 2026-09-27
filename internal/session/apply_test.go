package session_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// The first write ever to a device runs a full backup first, writes every
// op under Edit, reads each one back, returns the guard to ReadOnly, and
// reloads. Later writes of the session need no new backup; a new session
// saves what it loaded before its first write.
func TestApplyWritesVerifiesAndReloads(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	want := afterPlan(t, sn.Image, p)

	var during []session.Snapshot
	var run string
	out, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		run = e.Run
		if e.Kind == safety.EventWritten {
			during = append(during, *s.Snapshot())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verified != len(p.Ops) || out.Run == "" || out.Run != run || out.DryRun || len(out.Packets) != 0 {
		t.Fatalf("outcome %+v for %d ops", out, len(p.Ops))
	}
	for _, d := range during {
		if d.Policy != wire.Edit || d.State != session.Applying || d.Progress.Job != "apply" || d.Progress.Total != len(p.Ops) {
			t.Fatalf("while writing: policy %v, state %v, progress %+v", d.Policy, d.State, d.Progress)
		}
	}
	if got := s.Snapshot().Policy; got != wire.ReadOnly {
		t.Fatalf("policy after the apply is %v", got)
	}
	if got := r.cmd7(); !slices.Equal(got, chunksOf(p)) {
		t.Fatalf("cmd 7 sent\n got %v\nwant %v", got, chunksOf(p))
	}
	holds(t, "device", r.dev.Image(), want, p)

	if len(out.Backups) != 1 {
		t.Fatalf("backups %v, want the full one", out.Backups)
	}
	list := backupsOf(t, r, sn)
	if len(list) != 1 || list[0].Path != out.Backups[0] || list[0].File.Label != session.LabelFirstWrite || !list[0].File.Full {
		t.Fatalf("backups on disk %+v", list)
	}
	if f := list[0].File; !bytes.Equal(f.Image().Bytes(), r.start.Bytes()) {
		t.Error("the first-write backup is not the device as it was")
	}

	sn = await(t, s, "the reload after the apply", idle)
	holds(t, "reloaded image", sn.Image, want, p)
	if sn.Journal == nil || len(sn.Journal.Open) != 0 || sn.Journal.Last == nil || sn.Journal.Last.ID != out.Run {
		t.Fatalf("journal state %+v", sn.Journal)
	}
	if st := journalOf(t, r, sn); !st.Clean() || !st.Last.Complete {
		t.Fatalf("journal: open %v, last %+v", st.Open, st.Last)
	}

	again := planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1})
	out, err = s.Apply(ctxT(t), again, allow(again), nil)
	if err != nil || len(out.Backups) != 0 {
		t.Fatalf("second apply: %v, backups %v", err, out.Backups)
	}

	s2, sn2 := r.ready(nil)
	third := planOn(t, sn2, sn2.Image, mouse.SetCurrent{Stage: 0})
	out, err = s2.Apply(ctxT(t), third, allow(third), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Backups) != 1 {
		t.Fatalf("a new session saved %v, want one backup of what it loaded", out.Backups)
	}
	f, err := backup.Load(out.Backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if f.Label != session.LabelBeforeWrite || f.Full || !f.Automatic() {
		t.Fatalf("second session's backup: label %q, full %v", f.Label, f.Full)
	}
	if b, _ := f.Image().Get(flash.Extent{Addr: mouse.AddrCurrentDPI, Len: 2}); !slices.Equal(b, again.Ops[0].New) {
		t.Errorf("the backup holds % x at the current stage, the device held % x", b, again.Ops[0].New)
	}
}

// A dry run reads from the device and writes into the overlay: the exact
// packets come back, nothing reaches the device, the guard never leaves
// ReadOnly, no backup is made and the journal stays empty. Dry runs add up
// until a real write.
func TestDryRun(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	var policies []wire.Policy
	out, err := s.Apply(ctxT(t), p, safety.Gates{DryRun: true}, func(safety.OpEvent) {
		policies = append(policies, s.Snapshot().Policy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.DryRun || out.Verified != len(p.Ops) || len(out.Backups) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if !slices.Equal(out.Packets, chunksOf(p)) {
		t.Fatalf("packets\n got %v\nwant %v", out.Packets, chunksOf(p))
	}
	if slices.Contains(policies, wire.Edit) {
		t.Fatal("the guard was Edit during a dry run")
	}
	if len(r.cmd7()) != 0 || !bytes.Equal(r.dev.Image().Bytes(), r.start.Bytes()) {
		t.Fatal("the dry run wrote to the device")
	}
	want := afterPlan(t, sn.Image, p)
	holds(t, "outcome image", out.Image, want, p)
	sn = s.Snapshot()
	if sn.DryRun == nil {
		t.Fatal("no dry-run image in the snapshot")
	}
	holds(t, "snapshot dry-run image", sn.DryRun, want, p)
	if len(backupsOf(t, r, sn)) != 0 || len(journalOf(t, r, sn).Runs) != 0 {
		t.Fatal("a dry run left a backup or a journal run")
	}

	next := planOn(t, sn, sn.DryRun, mouse.SetCurrent{Stage: 1})
	out, err = s.Apply(ctxT(t), next, safety.Gates{DryRun: true}, nil)
	if err != nil || !slices.Equal(out.Packets, chunksOf(next)) {
		t.Fatalf("second dry run: %v, packets %v", err, out.Packets)
	}
	if _, err := s.Apply(ctxT(t), p, safety.Gates{DryRun: true}, nil); !errors.Is(err, safety.ErrStale) {
		t.Fatalf("a plan made before the first dry run: %v, want ErrStale", err)
	}

	real := planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1})
	if _, err := s.Apply(ctxT(t), real, allow(real), nil); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().DryRun != nil {
		t.Error("a real write kept the dry runs")
	}
}

// Every check blocks the write on its own, before any cmd 7 and with the
// guard left at ReadOnly.
func TestApplyPreflightFailures(t *testing.T) {
	tests := []struct {
		name   string
		writes func(*session.Writes)
		before func(t *testing.T, r *rig, s *session.Session, p *plan.Plan, g *safety.Gates)
		want   error
	}{
		{name: "identity", want: safety.ErrIdentity, before: func(_ *testing.T, _ *rig, _ *session.Session, p *plan.Plan, _ *safety.Gates) {
			p.Device.PID = 0x1283
		}},
		{name: "profile", want: safety.ErrProfile, before: func(t *testing.T, r *rig, s *session.Session, _ *plan.Plan, _ *safety.Gates) {
			must(t, r.dev.SwitchProfile(1))
			await(t, s, "the reload on profile 1", func(sn *session.Snapshot) bool { return idle(sn) && sn.Profile.Value == 1 })
		}},
		{name: "stale", want: safety.ErrStale, before: func(t *testing.T, r *rig, _ *session.Session, _ *plan.Plan, _ *safety.Gates) {
			m := em11(t)
			e, _ := mouse.DPIExtent(0)
			rec, err := mouse.EncodeDPI(m.Model.Sensor, 1200)
			must(t, err)
			must(t, m.Image.Set(e.Addr, rec))
			must(t, r.dev.Pair(m))
		}},
		{name: "offline", want: safety.ErrOffline, before: func(_ *testing.T, r *rig, _ *session.Session, _ *plan.Plan, _ *safety.Gates) {
			r.dev.Sleep()
		}},
		{name: "foreign client", want: safety.ErrForeignClient, before: func(_ *testing.T, r *rig, _ *session.Session, _ *plan.Plan, _ *safety.Gates) {
			r.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
		}},
		{name: "no lock", want: safety.ErrNoLock, writes: func(w *session.Writes) { w.Lock = nil }},
		{name: "lock held elsewhere", want: safety.ErrNoLock, writes: func(w *session.Writes) {
			w.Lock = func() error { return errors.New("another arcctl (pid 9) holds it") }
		}},
		{name: "screen locked", want: safety.ErrScreenLocked, writes: func(w *session.Writes) {
			w.Console = func() (safety.Console, error) { return safety.Console{ScreenLocked: true}, nil }
		}},
		{name: "secure input", want: safety.ErrSecureInput, writes: func(w *session.Writes) {
			w.Console = func() (safety.Console, error) { return safety.Console{SecureInput: "Terminal (pid 77)"}, nil }
		}},
		{name: "untested", want: safety.ErrUntested, before: func(_ *testing.T, _ *rig, _ *session.Session, _ *plan.Plan, g *safety.Gates) {
			g.AllowUntested = false
		}},
		{name: "unconfirmed", want: safety.ErrConfirm, before: func(_ *testing.T, _ *rig, _ *session.Session, _ *plan.Plan, g *safety.Gates) {
			g.Confirm = ""
		}},
		{name: "conflict", want: safety.ErrConflict, before: func(t *testing.T, r *rig, s *session.Session, _ *plan.Plan, _ *safety.Gates) {
			time.Sleep(2 * fast().Window)
			r.dev.Deliver(wire.MustBuild(wire.Mouse, wire.CmdBattery, 0, nil))
			await(t, s, "conflict", in(session.Conflict))
		}},
		{name: "no backup folder", want: safety.ErrNoBackup, writes: func(w *session.Writes) { w.Backups = nil }},
		{name: "backup fails", want: safety.ErrNoBackup, writes: func(w *session.Writes) { w.Backups = backup.Store{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			if tt.writes != nil {
				tt.writes(&r.writes)
			}
			s, sn := r.ready(nil)
			p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
			g := allow(p)
			if tt.before != nil {
				tt.before(t, r, s, &p, &g)
			}
			_, err := s.Apply(ctxT(t), p, g, nil)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var pe *safety.PreflightError
			if !errors.As(err, &pe) {
				t.Errorf("err %T is not a *safety.PreflightError", err)
			}
			if len(r.cmd7()) != 0 {
				t.Fatal("a cmd 7 reached the device")
			}
			if got := s.Snapshot().Policy; got != wire.ReadOnly {
				t.Fatalf("policy %v", got)
			}
		})
	}
}

// With the override, a foreign client no longer blocks the write.
func TestAllowForeignClient(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	r.dev.AddClient(emu.Client{PID: 4242, Process: "karabiner_grabber"})
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	g := allow(p)
	g.AllowForeignClient = true
	if _, err := s.Apply(ctxT(t), p, g, nil); err != nil {
		t.Fatal(err)
	}
}

// A full backup that cannot read every range is saved and reported with its
// gaps. Accepted, it lets the first write through without reading again.
func TestPartialFullBackupNeedsAcceptance(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	gap := flash.Extent{Addr: 9504, Len: 10}
	r.dev.Inject(emu.Fault{Cmd: wire.CmdRead, Match: func(p wire.Packet) bool { return int(p.Addr()) == gap.Addr }, Action: emu.NAK})
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	out, err := s.Apply(ctxT(t), p, allow(p), nil)
	var partial *safety.PartialError
	if !errors.As(err, &partial) || !slices.Equal(partial.Missing, []flash.Extent{gap}) {
		t.Fatalf("err = %v, want a *PartialError lacking %v", err, gap)
	}
	if len(out.Backups) != 1 || partial.Path != out.Backups[0] || len(r.cmd7()) != 0 {
		t.Fatalf("backups %v, error names %s, %d writes", out.Backups, partial.Path, len(r.cmd7()))
	}
	before := len(reads(r.dev.Writes()))
	g := allow(p)
	g.AcceptPartial = true
	out, err = s.Apply(ctxT(t), p, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Backups) != 0 {
		t.Errorf("the accepted apply saved %v again", out.Backups)
	}
	if n := len(reads(r.dev.Writes())) - before; n > 2*len(p.Ops)+2 {
		t.Errorf("%d reads for the accepted apply; the full backup ran again", n)
	}
	list := backupsOf(t, r, sn)
	if len(list) != 1 || !slices.Equal(list[0].File.Missing(), []flash.Extent{gap}) {
		t.Fatalf("backups on disk: %+v", list)
	}
}

// A write that fails puts the guard back to ReadOnly, reloads, and leaves
// the run for recovery.
func TestFailedApplyLeavesRecovery(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	r.dev.Inject(emu.Fault{Cmd: wire.CmdWrite, Skip: 3, Times: 1, Action: emu.NAK})
	_, err := s.Apply(ctxT(t), p, allow(p), nil)
	var st *safety.StopError
	if !errors.As(err, &st) || !errors.Is(err, wire.ErrNAK) {
		t.Fatalf("err = %v, want a StopError with the NAK", err)
	}
	if got := s.Snapshot().Policy; got != wire.ReadOnly {
		t.Fatalf("policy %v after a failed apply", got)
	}
	sn = await(t, s, "recovering", in(session.Recovering))
	if sn.Link != session.Ready || len(sn.Journal.Open) != 1 || sn.Journal.Open[0].Inspection == nil {
		t.Fatalf("journal state %+v", sn.Journal)
	}
	next := planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1})
	if _, err := s.Apply(ctxT(t), next, allow(next), nil); !errors.Is(err, safety.ErrNotClean) {
		t.Fatalf("apply while recovering: %v, want ErrNotClean", err)
	}
	run := sn.Journal.Open[0].Run.ID
	if _, err := s.Recover(ctxT(t), run, safety.Back, safety.Gates{}, nil); err != nil {
		t.Fatal(err)
	}
	sn = await(t, s, "ready after recovery", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Journal != nil && len(sn.Journal.Open) == 0
	})
	holds(t, "device after rolling back", r.dev.Image(), r.start, p)
	if st := journalOf(t, r, sn); !st.Clean() {
		t.Fatalf("journal still open: %v", st.Open)
	}
}

// A panic inside the write, here from the caller's callback, ends it like a
// crash: the guard goes back to ReadOnly, the session carries on, and the
// run it interrupted waits for recovery.
func TestPanicDuringApply(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		if e.Kind == safety.EventChunk && e.Op.Phase == plan.Body && e.Chunk == 1 {
			panic("callback bug")
		}
	})
	if !errors.Is(err, session.ErrPanic) {
		t.Fatalf("err = %v, want ErrPanic", err)
	}
	if got := s.Snapshot().Policy; got != wire.ReadOnly {
		t.Fatalf("policy %v after a panic", got)
	}
	sn = await(t, s, "recovering", in(session.Recovering))
	in := sn.Journal.Open[0].Inspection
	if in == nil || !in.Torn() {
		t.Fatalf("inspection %+v, want the torn body", in)
	}
	if _, err := s.Recover(ctxT(t), in.Run.ID, safety.Leave, safety.Gates{}, nil); !errors.Is(err, safety.ErrTorn) {
		t.Fatalf("leaving a torn run: %v", err)
	}
	if err := s.Reload(ctxT(t)); err != nil {
		t.Fatalf("the session no longer works after the panic: %v", err)
	}
}

// Abort stops the write after the op that is running.
func TestAbortStopsAfterTheCurrentOp(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		if e.Kind == safety.EventChunk && e.Op.Seq == 1 {
			s.Abort()
		}
	})
	var st *safety.StopError
	if !errors.As(err, &st) || !errors.Is(err, safety.ErrAborted) || st.Op.Seq != 2 || st.Written {
		t.Fatalf("err = %v, want an abort before op 2", err)
	}
	if got := len(r.cmd7()); got != len(chunksOf(plan.Plan{Ops: p.Ops[:1]})) {
		t.Fatalf("%d cmd 7 sent, want only op 1's", got)
	}
}

// A push that re-reads a range the plan has yet to write stops the write
// after the current op; a profile switch stops it before the next packet.
func TestPushesDuringApply(t *testing.T) {
	for _, tt := range []struct {
		name string
		push func(d *emu.Device) error
		want error
	}{
		{"dpi button", (*emu.Device).PressDPI, safety.ErrOverlap},
		{"profile switch", func(d *emu.Device) error { return d.SwitchProfile(1) }, safety.ErrProfile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			s, sn := r.ready(nil)
			p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900}, mouse.SetCurrent{Stage: 1})
			pushed := false
			_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
				if e.Kind == safety.EventWritten && !pushed {
					pushed = true
					must(t, tt.push(r.dev))
				}
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			await(t, s, "the reload", func(sn *session.Snapshot) bool { return sn.Link == session.Ready && sn.Progress.Job == "" })
		})
	}
}

// Revert undoes the last run through the same checks and executor, and a
// dry run of it only lists the packets.
func TestRevert(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); err != nil {
		t.Fatal(err)
	}
	await(t, s, "the reload", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil })
	n := len(r.cmd7())
	out, err := s.Revert(ctxT(t), safety.Gates{DryRun: true}, nil)
	if err != nil || len(out.Packets) == 0 || len(r.cmd7()) != n {
		t.Fatalf("dry-run revert: %v, %d packets, %d writes", err, len(out.Packets), len(r.cmd7())-n)
	}
	if _, err := s.Revert(ctxT(t), safety.Gates{}, nil); !errors.Is(err, safety.ErrUntested) {
		t.Fatalf("revert without the gates: %v", err)
	}
	out, err = s.Revert(ctxT(t), allow(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.cmd7()[n:]; !slices.Equal(got, out.Packets) && len(got) == 0 {
		t.Fatal("the revert wrote nothing")
	}
	holds(t, "device after revert", r.dev.Image(), r.start, p)
	sn = await(t, s, "the reload", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil && sn.Journal.Last != nil })
	if sn.Journal.Last.Kind != safety.KindRevert {
		t.Errorf("last run is a %v", sn.Journal.Last.Kind)
	}
}

func TestWriteBusyAndReadOnly(t *testing.T) {
	b := newBus(t, emu.Options{})
	add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	sn := await(t, s, "ready", idle)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	for _, call := range []func() error{
		func() error { _, err := s.Apply(ctxT(t), p, allow(p), nil); return err },
		func() error { _, err := s.Revert(ctxT(t), allow(p), nil); return err },
		func() error { _, err := s.Recover(ctxT(t), "x", safety.Back, safety.Gates{}, nil); return err },
	} {
		if err := call(); !errors.Is(err, session.ErrReadOnly) {
			t.Errorf("err = %v, want ErrReadOnly", err)
		}
	}
}

// A mouse that sleeps between two ops pauses the write; once it wakes, it is
// identified again and the write goes on.
func TestSleepDuringApplyResumes(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	var kinds []safety.EventKind
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		kinds = append(kinds, e.Kind)
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			r.dev.Sleep()
			time.AfterFunc(50*time.Millisecond, r.dev.Wake)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(kinds, safety.EventPaused) || !slices.Contains(kinds, safety.EventResumed) {
		t.Fatalf("events %v, want a pause and a resume", kinds)
	}
	holds(t, "device", r.dev.Image(), afterPlan(t, sn.Image, p), p)
	handshakes := count(r.dev.Writes(), wire.CmdHandshake)
	if handshakes < 3 {
		t.Errorf("%d handshakes; the mouse was not identified again after it woke", handshakes)
	}
}

// Another mouse that wakes up in place of the one being written stops the
// write before any packet reaches it.
func TestAnotherMouseWakesDuringApply(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	other := em11(t)
	other.MID = 6
	n := 0
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			n = len(r.cmd7())
			r.dev.Sleep()
			time.AfterFunc(50*time.Millisecond, func() { _ = r.dev.Pair(other) })
		}
	})
	if !errors.Is(err, safety.ErrIdentity) {
		t.Fatalf("err = %v, want ErrIdentity", err)
	}
	if len(r.cmd7()) != n {
		t.Fatal("the other mouse was written")
	}
	if s.Snapshot().Policy != wire.ReadOnly {
		t.Fatal("the guard stayed Edit")
	}
	sn = await(t, s, "the other mouse loaded", func(sn *session.Snapshot) bool { return idle(sn) && sn.Identity.MID == 6 })
	if sn.Journal == nil || len(sn.Journal.Open) != 0 {
		t.Errorf("the other mouse inherited the journal: %+v", sn.Journal)
	}
}

// A device changed behind the session's back blocks the plan made before,
// and the fresh bytes reach the snapshot, so planning again works.
func TestStaleDeviceShowsTheFreshBytes(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	m := em11(t)
	e, _ := mouse.DPIExtent(0)
	rec, err := mouse.EncodeDPI(m.Model.Sensor, 1200)
	must(t, err)
	must(t, m.Image.Set(e.Addr, rec))
	must(t, r.dev.Pair(m))
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); !errors.Is(err, safety.ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	sn = s.Snapshot()
	if b, _ := sn.Image.Get(e); !slices.Equal(b, rec) {
		t.Fatalf("snapshot holds % x at %v, the device % x", b, e, rec)
	}
	p = planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); err != nil {
		t.Fatal(err)
	}
}

// A reply nobody asked for during a write puts the session in Conflict, and
// no cmd 7 goes out after it.
func TestConflictDuringApplyStops(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	n := 0
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			n = len(r.cmd7())
			r.dev.Deliver(wire.MustBuild(wire.Mouse, wire.CmdRead, 0x3000, []byte{0}))
		}
	})
	var st *safety.StopError
	if !errors.As(err, &st) || !errors.Is(err, safety.ErrConflict) || st.Op.Seq != 2 {
		t.Fatalf("err = %v, want a stop at op 2 for the conflict", err)
	}
	if len(r.cmd7()) != n {
		t.Fatalf("%d cmd 7 after the conflict", len(r.cmd7())-n)
	}
	if sn := s.Snapshot(); sn.State != session.Conflict || sn.Policy != wire.ReadOnly {
		t.Fatalf("state %v, policy %v", sn.State, sn.Policy)
	}
}

// A plan that writes nothing needs no checks and no backups.
func TestEmptyPlanDoesNothing(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	n := len(r.dev.Writes())
	out, err := s.Apply(ctxT(t), plan.Plan{Device: sn.Identity, Profile: profileOf(sn)}, safety.Gates{}, nil)
	if err != nil || out.Run != "" || len(out.Backups) != 0 || len(r.dev.Writes()) != n {
		t.Fatalf("outcome %+v, err %v, %d packets", out, err, len(r.dev.Writes())-n)
	}
}
