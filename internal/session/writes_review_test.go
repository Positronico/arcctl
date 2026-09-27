package session_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// A browser tab that opens the receiver while the mouse sleeps in the middle
// of a write blocks the rest of it: the checks the preflight asked the OS run
// again after the pause (I10, §6.4 step 2).
func TestForeignClientDuringPauseStopsTheWrite(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	n := 0
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			n = len(r.cmd7())
			r.dev.Sleep()
			r.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
			time.AfterFunc(50*time.Millisecond, r.dev.Wake)
		}
	})
	var st *safety.StopError
	if !errors.As(err, &st) || !errors.Is(err, safety.ErrForeignClient) || st.Written {
		t.Fatalf("err = %v, want a stop before op 2 for the foreign client", err)
	}
	if extra := len(r.cmd7()) - n; extra > 0 {
		t.Fatalf("Chrome opened the receiver during the pause, yet %d more cmd 7 went out", extra)
	}
	if s.Snapshot().Policy != wire.ReadOnly {
		t.Fatal("the guard stayed Edit")
	}
}

// The same client with the override does not stop the write.
func TestAllowedForeignClientDuringPause(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	g := allow(p)
	g.AllowForeignClient = true
	_, err := s.Apply(ctxT(t), p, g, func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			r.dev.Sleep()
			r.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
			time.AfterFunc(50*time.Millisecond, r.dev.Wake)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	holds(t, "device", r.dev.Image(), afterPlan(t, sn.Image, p), p)
}

// Backups hold one onboard profile (I1, I11). The first write to another
// profile saves a backup of that profile, although the device was written
// to before on the first one.
func TestWriteToAnotherProfileIsBackedUp(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); err != nil {
		t.Fatal(err)
	}
	must(t, r.dev.SwitchProfile(1))
	sn = await(t, s, "the reload on profile 1", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Profile.Value == 1 && sn.Journal != nil
	})
	p1 := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 1, DPI: 1000})
	out, err := s.Apply(ctxT(t), p1, allow(p1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Backups) == 0 {
		t.Fatal("the first write to profile 1 saved no backup")
	}
	for _, path := range out.Backups {
		f, err := backup.Load(path)
		must(t, err)
		if pr := f.Device.Profile; pr == nil || !pr.Supported || pr.Value != 1 {
			t.Fatalf("backup %s holds profile %+v, want 1", path, pr)
		}
	}
}

// A profile switch the session never heard of, found only by a reload,
// still gets the new profile its own backups: they are keyed by profile.
func TestSilentProfileSwitchIsBackedUp(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); err != nil {
		t.Fatal(err)
	}
	await(t, s, "the reload after the write", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil })
	must(t, r.dev.SetProfile(1))
	must(t, s.Reload(ctxT(t)))
	sn = await(t, s, "the reload on profile 1", func(sn *session.Snapshot) bool { return idle(sn) && sn.Profile.Value == 1 })
	p1 := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 1, DPI: 1000})
	out, err := s.Apply(ctxT(t), p1, allow(p1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Backups) == 0 {
		t.Fatal("the first write to profile 1 saved no backup")
	}
	for _, path := range out.Backups {
		f, err := backup.Load(path)
		must(t, err)
		if pr := f.Device.Profile; pr == nil || pr.Value != 1 {
			t.Fatalf("backup %s holds profile %+v, want 1", path, pr)
		}
	}
}

// A reload that finds other bytes than the session loaded before, as after
// another mouse of the same model was paired, makes the next write save
// what it loaded again.
func TestReloadThatFindsOtherBytesBacksUpAgain(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); err != nil {
		t.Fatal(err)
	}
	await(t, s, "the reload after the write", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil })

	m := em11(t)
	stage1, _ := mouse.DPIExtent(1)
	rec, err := mouse.EncodeDPI(m.Model.Sensor, 1300)
	must(t, err)
	must(t, m.Image.Set(stage1.Addr, rec))
	must(t, r.dev.Pair(m))
	must(t, s.Reload(ctxT(t)))
	sn = await(t, s, "the reload", idle)

	again := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 1000})
	out, err := s.Apply(ctxT(t), again, allow(again), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Backups) != 1 {
		t.Fatalf("backups %v, want one of the bytes the reload found", out.Backups)
	}
	f, err := backup.Load(out.Backups[0])
	must(t, err)
	if b, _ := f.Image().Get(stage1); f.Label != session.LabelBeforeWrite || !slices.Equal(b, rec) {
		t.Fatalf("backup %q holds % x at %v, the device % x", f.Label, b, stage1, rec)
	}

	must(t, s.Reload(ctxT(t)))
	await(t, s, "a reload that finds the same bytes", idle)
	third := planOn(t, s.Snapshot(), s.Snapshot().Image, mouse.SetDPI{Stage: 0, DPI: 1100})
	if out, err := s.Apply(ctxT(t), third, allow(third), nil); err != nil || len(out.Backups) != 0 {
		t.Fatalf("after a reload of the same bytes: %v, backups %v", err, out.Backups)
	}
}

// A session that cannot talk to the device because it is locked, seized or
// stalled reports that, not a device that is merely not ready (exit 6).
func TestLockedSessionBlocksWrites(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	r.dev.SetLocked(true)
	go func() { _, _ = s.Read(ctxT(t), flash.Extent{Addr: 9504, Len: 10}) }()
	await(t, s, "locked", in(session.Locked))
	_, err := s.Apply(ctxT(t), p, allow(p), nil)
	var pe *safety.PreflightError
	if !errors.As(err, &pe) || !errors.Is(err, safety.ErrBlocked) {
		t.Fatalf("err = %v, want a preflight failure wrapping ErrBlocked", err)
	}
	if len(r.cmd7()) != 0 {
		t.Fatal("a cmd 7 reached the device")
	}
}

// Preflight runs every check of an apply and writes nothing: no cmd 7, no
// backup, no journal run.
func TestPreflightWritesNothing(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	if err := s.Preflight(ctxT(t), p, allow(p)); err != nil {
		t.Fatal(err)
	}
	r.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
	var pe *safety.PreflightError
	if err := s.Preflight(ctxT(t), p, allow(p)); !errors.As(err, &pe) || !errors.Is(err, safety.ErrForeignClient) {
		t.Fatalf("err = %v, want the foreign client", err)
	}
	g := allow(p)
	g.AllowUntested = false
	if err := s.Preflight(ctxT(t), p, g); !errors.Is(err, safety.ErrUntested) {
		t.Fatalf("err = %v, want the untested gate", err)
	}
	if len(r.cmd7()) != 0 || len(backupsOf(t, r, sn)) != 0 || len(journalOf(t, r, sn).Runs) != 0 {
		t.Fatal("the preflight wrote")
	}
	if s.Snapshot().Policy != wire.ReadOnly {
		t.Fatal("the guard left ReadOnly")
	}
}

// PreflightRevert runs the checks of a revert of the last run, its tier
// gates included, and writes nothing.
func TestPreflightRevertWritesNothing(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900})
	if err := s.PreflightRevert(ctxT(t), allow(p)); !errors.Is(err, safety.ErrNothing) {
		t.Fatalf("with no run: err = %v, want nothing to revert", err)
	}
	out, err := s.Apply(ctxT(t), p, allow(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	await(t, s, "the reload", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Journal != nil && sn.Journal.Last != nil && sn.Journal.Last.ID == out.Run
	})
	n := len(r.cmd7())
	if err := s.PreflightRevert(ctxT(t), allow(p)); err != nil {
		t.Fatal(err)
	}
	g := allow(p)
	g.AllowUntested = false
	if err := s.PreflightRevert(ctxT(t), g); !errors.Is(err, safety.ErrUntested) {
		t.Fatalf("err = %v, want the untested gate", err)
	}
	r.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
	if err := s.PreflightRevert(ctxT(t), allow(p)); !errors.Is(err, safety.ErrForeignClient) {
		t.Fatalf("err = %v, want the foreign client", err)
	}
	if len(r.cmd7()) != n || len(journalOf(t, r, sn).Runs) != 1 {
		t.Fatal("the preflight wrote")
	}
}

// Trusting the address changes the identity key and the journal folder. A
// torn run left under the old key still blocks writes (I6), and shows as
// unfinished.
func TestOpenRunSurvivesTrustingTheAddress(t *testing.T) {
	r := newRig(t)
	s, stop := r.run(nil)
	sn := await(t, s, "ready", idle)
	p := mixed(t, sn)
	r.dev.Inject(emu.Fault{Match: emu.Nth(wire.CmdWrite, 4), Times: 1, Action: emu.NAK})
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); !errors.Is(err, wire.ErrNAK) {
		t.Fatalf("err = %v, want the NAK", err)
	}
	await(t, s, "recovering", in(session.Recovering))
	stop()

	s2, _ := r.run(func(o *session.Options) { o.TrustAddress = true })
	sn2 := await(t, s2, "loaded", func(sn *session.Snapshot) bool {
		return sn.Link == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
	})
	if !sn2.Identity.AddrTrusted || len(sn2.Journal.Open) != 1 || !errors.Is(sn2.Journal.Open[0].Err, safety.ErrNotClean) || sn2.State != session.Recovering {
		t.Fatalf("with the address trusted (key %s): state %v, journal %+v", sn2.Identity.Key(), sn2.State, sn2.Journal)
	}
	q := planOn(t, sn2, sn2.Image, mouse.SetDPI{Stage: 1, DPI: 1000})
	n := len(r.cmd7())
	if _, err := s2.Apply(ctxT(t), q, allow(q), nil); !errors.Is(err, safety.ErrNotClean) || len(r.cmd7()) != n {
		t.Fatalf("apply with the old run open: %v", err)
	}
}

var scrollUp = mouse.KeyFn{Type: mouse.TypeWheel, Param: mouse.ParamScrollUp}

// Undo of slot 3 to Scroll Up (H3, M4's acceptance): the reload after the
// apply no longer reads shortcut 3, which the revert binds again.
func TestRevertOfARebindAfterReload(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := planOn(t, sn, sn.Image, mouse.SetKey{Slot: 3, Fn: scrollUp})
	out, err := s.Apply(ctxT(t), p, allow(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	sn = await(t, s, "the reload", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Journal != nil && sn.Journal.Last != nil && sn.Journal.Last.ID == out.Run
	})
	e, _ := mouse.ShortcutExtent(3)
	if sn.Image.Known(e) {
		t.Fatal("the reload read shortcut 3, which nothing binds")
	}
	dry, err := s.Revert(ctxT(t), safety.Gates{DryRun: true}, nil)
	if err != nil || len(dry.Packets) != 1 {
		t.Fatalf("dry-run revert: %v, packets %v", err, dry.Packets)
	}
	if _, err := s.Revert(ctxT(t), allow(p), nil); err != nil {
		t.Fatalf("revert of the last apply: %v", err)
	}
	holds(t, "device after the revert", r.dev.Image(), r.start, p)
}

// Rolling back a run whose first op moved slot 3 off its shortcut, and whose
// second op never went out.
func TestRecoverBackOfARebindAfterReload(t *testing.T) {
	r := newRig(t)
	s, stop := r.run(nil)
	sn := await(t, s, "ready", idle)
	p := planOn(t, sn, sn.Image, mouse.SetKey{Slot: 3, Fn: scrollUp}, mouse.SetDPI{Stage: 0, DPI: 900})
	if len(p.Ops) != 2 || p.Ops[0].Phase != plan.Bind {
		t.Fatalf("ops %+v", p.Ops)
	}
	run := crash(t, s, p, func(e safety.OpEvent) bool { return e.Kind == safety.EventStart && e.Op.Seq == 2 })
	stop()

	s2, _ := r.run(nil)
	sn = await(t, s2, "recovering", in(session.Recovering))
	if len(sn.Journal.Open) != 1 || sn.Journal.Open[0].Inspection == nil {
		t.Fatalf("open %+v", sn.Journal.Open)
	}
	if _, err := safety.RecoveryPlan(sn.Journal.Open[0].Inspection, safety.Back, safety.Device{
		Identity: sn.Identity, Profile: profileOf(sn), Layout: mouse.Layout(sn.Model),
	}); err != nil {
		t.Fatalf("the back plan the CLI shows: %v", err)
	}
	if _, err := s2.Recover(ctxT(t), run, safety.Back, safety.Gates{}, nil); err != nil {
		t.Fatalf("recover back: %v", err)
	}
	holds(t, "device after rolling back", r.dev.Image(), r.start, p)
}

// Finishing a run whose unwritten binding points at a body no binding
// pointed at when the device was loaded again.
func TestRecoverForwardOntoAnUnreadBody(t *testing.T) {
	r := newRig(t)
	body12, _ := mouse.ShortcutExtent(12)
	body13, _ := mouse.ShortcutExtent(13)
	cmdC := []byte{0x04, 0x80, 0x08, 0x00, 0x81, 0x06, 0x00, 0x41, 0x06, 0x00, 0x40, 0x08, 0x00, 0xb3}
	cmdV := []byte{0x04, 0x80, 0x08, 0x00, 0x81, 0x19, 0x00, 0x41, 0x19, 0x00, 0x40, 0x08, 0x00, 0x8d}
	must(t, r.dev.Store(body12.Addr, cmdC))
	r.start = r.dev.Image()
	s, stop := r.run(nil)
	sn := await(t, s, "ready", idle)
	full, err := flash.FromDump(0, r.dev.Image().Bytes())
	must(t, err)
	bind12, _ := mouse.KeyFnExtent(12)
	p, err := plan.New(sn.Identity, profileOf(sn), full, mouse.Layout(sn.Model), []plan.Change{
		{Addr: body13.Addr, New: cmdV, Tier: catalog.Untested, Desc: "shortcut 13"},
		{Addr: bind12.Addr, New: []byte{0x05, 0x00, 0x00, 0x50}, Tier: catalog.Untested, Desc: "slot 12: its shortcut"},
	})
	must(t, err)
	if len(p.Ops) != 2 || p.Ops[1].Phase != plan.Bind {
		t.Fatalf("ops %+v", p.Ops)
	}
	want := afterPlan(t, full, p)
	run := crash(t, s, p, func(e safety.OpEvent) bool { return e.Kind == safety.EventStart && e.Op.Seq == 2 })
	stop()

	s2, _ := r.run(nil)
	sn = await(t, s2, "recovering", in(session.Recovering))
	if sn.Image.Known(body12) {
		t.Fatal("the load read shortcut 12, which nothing binds")
	}
	if _, err := s2.Recover(ctxT(t), run, safety.Forward, safety.Gates{}, nil); err != nil {
		t.Fatalf("recover forward: %v", err)
	}
	holds(t, "device after finishing the run", r.dev.Image(), want, p)
}
