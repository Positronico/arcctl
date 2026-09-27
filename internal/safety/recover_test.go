package safety_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// applyKilled applies p and kills the executor right after the k-th cmd 7
// was answered, as a crash would, then restarts.
func (f *fixture) applyKilled(p plan.Plan, k int) {
	f.t.Helper()
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == k {
			panic(errKilled)
		}
	}
	func() {
		defer func() {
			if r := recover(); r != errKilled {
				f.t.Fatalf("recovered %v, want the kill", r)
			}
		}()
		f.apply(p, nil)
	}()
	f.restart()
}

func classes(in *safety.Inspection) []safety.Class {
	var out []safety.Class
	for _, e := range in.Extents {
		out = append(out, e.Class)
	}
	return out
}

func TestKillMidBodyThenRecoverForward(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 70))
	f.applyKilled(p, 21)

	st := f.status()
	if len(st.Open) != 1 {
		t.Fatalf("open runs %+v", st.Open)
	}
	r := st.Open[0]
	if r.Ended || r.Kind != safety.KindApply || len(r.Ops) != 3 {
		t.Fatalf("run %+v", r)
	}
	states := []safety.State{r.Ops[0].State, r.Ops[1].State, r.Ops[2].State}
	if !slices.Equal(states, []safety.State{safety.StateVerified, safety.StateSending, safety.StatePlanned}) {
		t.Fatalf("op states %v", states)
	}
	checkBindings(t, f.image())

	in := must[*safety.Inspection](t)(f.x.Inspect(context.Background(), r, f.device()))
	if got := classes(in); !slices.Equal(got, []safety.Class{safety.ClassMid, safety.ClassTorn}) || !in.Torn() {
		t.Fatalf("extent classes %v", got)
	}
	var now []safety.Class
	for _, o := range in.Ops {
		now = append(now, o.Now)
	}
	if !slices.Equal(now, []safety.Class{safety.ClassNew, safety.ClassTorn, safety.ClassOld}) {
		t.Fatalf("op classes %v", now)
	}
	if _, err := f.x.Recover(context.Background(), r, safety.Leave, f.device(), nil); !errors.Is(err, safety.ErrTorn) {
		t.Fatalf("Leave over a torn body = %v, want ErrTorn", err)
	}

	res, err := f.x.Recover(context.Background(), r, safety.Forward, f.device(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), after(t, f.start, p), p); len(d) > 0 {
		t.Fatalf("extents %v are not the plan's", d)
	}
	f.checkOutside(p)
	checkBindings(t, f.image())
	st = f.status()
	rec := st.Find(res.Run)
	if !st.Clean() || rec == nil || rec.Kind != safety.KindRecover || rec.Of != r.ID || !rec.Complete {
		t.Fatalf("after recovery: open %v, recovery run %+v", st.Open, rec)
	}
	if got := st.Find(r.ID); got.Resolved != safety.Forward || got.By != res.Run {
		t.Fatalf("recovered run %+v", got)
	}
	if st.Last == nil || st.Last.ID != r.ID {
		t.Fatalf("last run %+v, want the apply finished by recovery", st.Last)
	}
}

// Rolling back a torn body with its binding disabled writes the old body
// before binding it again.
func TestKillMidBodyThenRollBack(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 70))
	f.applyKilled(p, 10)
	r := f.status().Open[0]
	in := must[*safety.Inspection](t)(f.x.Inspect(context.Background(), r, f.device()))
	rp := must[plan.Plan](t)(safety.RecoveryPlan(in, safety.Back, f.device()))
	var phases []plan.Phase
	for _, op := range rp.Ops {
		phases = append(phases, op.Phase)
	}
	if !slices.Equal(phases, []plan.Phase{plan.Body, plan.Bind}) {
		t.Fatalf("roll-back phases %v", phases)
	}
	if _, err := f.x.Recover(context.Background(), r, safety.Back, f.device(), nil); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("extents %v did not roll back", d)
	}
	checkBindings(t, f.image())
	if st := f.status(); !st.Clean() || st.Last != nil {
		t.Fatalf("open %v, last %+v; a rolled-back run changed nothing", st.Open, st.Last)
	}
}

// A crash during recovery leaves both runs unfinished; the next recovery
// settles the original run and the one that failed.
func TestKillDuringRecovery(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 70))
	f.applyKilled(p, 15)
	r := f.status().Open[0]

	writes := f.link.writes
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == writes+8 {
			panic(errKilled)
		}
	}
	func() {
		defer func() {
			if rec := recover(); rec != errKilled {
				t.Fatalf("recovered %v, want the kill", rec)
			}
		}()
		f.x.Recover(context.Background(), r, safety.Forward, f.device(), nil)
	}()
	f.restart()

	st := f.status()
	if len(st.Open) != 1 || st.Open[0].ID != r.ID || len(st.Open[0].Recoveries) != 1 {
		t.Fatalf("open %+v", st.Open)
	}
	failed := st.Open[0].Recoveries[0]
	if _, err := f.x.Recover(context.Background(), st.Open[0], safety.Back, f.device(), nil); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("extents %v did not roll back", d)
	}
	f.checkOutside(p)
	checkBindings(t, f.image())
	st = f.status()
	if !st.Clean() || st.Find(failed.ID).Resolved != safety.Back {
		t.Fatalf("open %v, failed recovery %+v", st.Open, st.Find(failed.ID))
	}
}

// A run cut short between ops is left as it is when the user says so; the
// verified ops stay revertible.
func TestLeave(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(append(dpiChange(t, f.model), pairChange(t)...))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 1 {
			f.link.abort()
		}
	}
	if _, err := f.apply(p, nil); !errors.Is(err, safety.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	r := f.status().Open[0]
	res, err := f.x.Recover(context.Background(), r, safety.Leave, f.device(), nil)
	if err != nil || res.Run != "" {
		t.Fatalf("Leave: %+v, %v", res, err)
	}
	st := f.status()
	if !st.Clean() || st.Find(r.ID).Resolved != safety.Leave || st.Last == nil || st.Last.ID != r.ID {
		t.Fatalf("open %v, last %+v", st.Open, st.Last)
	}
	if _, err := f.x.Recover(context.Background(), st.Find(r.ID), safety.Forward, f.device(), nil); !errors.Is(err, safety.ErrNotOpen) {
		t.Fatalf("recovering a settled run = %v, want ErrNotOpen", err)
	}
}

// A crash after the last read-back leaves a run that already holds its new
// bytes: recovery writes nothing and settles it.
func TestRecoverWritesNothingWhenDone(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	st := f.status()
	r := st.Last
	r.Complete, r.Ended = false, false
	before := len(f.dev.Writes())
	res, err := f.x.Recover(context.Background(), r, safety.Forward, f.device(), nil)
	if err != nil || res.Run != "" {
		t.Fatalf("Recover: %+v, %v", res, err)
	}
	for _, w := range f.dev.Writes()[before:] {
		if w.Packet.Cmd() == wire.CmdWrite {
			t.Fatal("recovery wrote to a finished run")
		}
	}
}

func TestRecoverRefusesAnotherDevice(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	f.applyKilled(p, 1)
	r := f.status().Open[0]
	d := f.device()
	d.Identity.MID = 6
	if _, err := f.x.Recover(context.Background(), r, safety.Forward, d, nil); !errors.Is(err, safety.ErrIdentity) {
		t.Fatalf("err = %v, want ErrIdentity", err)
	}
	if _, err := f.x.Recover(context.Background(), r, safety.Strategy(9), f.device(), nil); !errors.Is(err, safety.ErrStrategy) {
		t.Fatalf("err = %v, want ErrStrategy", err)
	}
}

// An abort that arrives while Recover reads the run's records again still
// stops the recovery before its first packet (the CLI's first Ctrl-C).
func TestAbortDuringRecoverInspection(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.applyKilled(p, 1)
	open := f.status().Open
	if len(open) != 1 {
		t.Fatalf("open runs %+v", open)
	}
	sent := cmd7(f)
	f.link.abort()
	_, err := f.x.Recover(context.Background(), open[0], safety.Forward, f.device(), nil)
	if !errors.Is(err, safety.ErrAborted) || cmd7(f) != sent {
		t.Fatalf("recover after an abort: err %v, %d cmd 7 sent", err, cmd7(f)-sent)
	}
	if st := f.status(); len(st.Open) != 1 || st.Open[0].ID != open[0].ID {
		t.Fatalf("open runs %+v, want only %s", st.Open, open[0].ID)
	}
}

// A push that re-reads a range of the run while Recover reads the run's
// records again stops the recovery before its first packet: the records may
// have been read before the change the push reports.
func TestPushDuringRecoverInspection(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.applyKilled(p, 1)
	open := f.status().Open
	sent := cmd7(f)
	f.dev.Push(0x01, 0)
	f.link.awaitPush(t)
	_, err := f.x.Recover(context.Background(), open[0], safety.Forward, f.device(), nil)
	if !errors.Is(err, safety.ErrOverlap) || cmd7(f) != sent {
		t.Fatalf("recover after a push: err %v, %d cmd 7 sent", err, cmd7(f)-sent)
	}
}

// scrollUp is the binding of the system function Scroll Up.
var scrollUp = []byte{0x0b, 0x01, 0x00, 0x49}

// loaded is what a working load of the device reads (PLAN §4.6): the
// settings page, the extended range, and the bodies the bindings point at.
// A body no binding points at is not in it.
func loaded(t testing.TB, dev *flash.Image) *flash.Image {
	t.Helper()
	im := flash.New()
	copyIn := func(e flash.Extent) {
		b, ok := dev.Get(e)
		if !ok {
			t.Fatalf("the device image lacks %s", e)
		}
		if err := im.Set(e.Addr, b); err != nil {
			t.Fatal(err)
		}
	}
	copyIn(flash.Extent{Addr: 0, Len: 256})
	copyIn(flash.Extent{Addr: mouse.AddrSensor3955DPI, Len: mouse.AddrEndEeprom - mouse.AddrSensor3955DPI})
	for k := range mouse.Slots {
		b, _ := dev.Get(extentOf(t, mouse.KeyFnExtent, k))
		switch mouse.KeyType(b[0]) {
		case mouse.TypeShortcut:
			copyIn(extentOf(t, mouse.ShortcutExtent, k))
		case mouse.TypeMacro:
			for _, s := range []int{k, int(b[1])} {
				e := extentOf(t, mouse.MacroExtent, s)
				h, _ := dev.Get(flash.Extent{Addr: e.Addr, Len: 32})
				copyIn(flash.Extent{Addr: e.Addr, Len: 32})
				if n := int(h[31]); n >= 1 && n <= mouse.MaxMacroEvents {
					copyIn(flash.Extent{Addr: e.Addr + 32, Len: 1 + 5*n})
				}
			}
		}
	}
	return im
}

// Rolling back a run that moved slot 3 off its shortcut binds the shortcut
// again, though the image the recovery starts from never held its body: the
// inspection reads the bodies the old and new bindings point at.
func TestRecoverBackRebindsABodyTheImageLacks(t *testing.T) {
	f := newFixture(t, setup{})
	bind := plan.Change{Addr: extentOf(t, mouse.KeyFnExtent, 3).Addr, New: scrollUp, Tier: catalog.Untested, Desc: "slot 3: Scroll Up"}
	p := f.plan(slices.Concat([]plan.Change{bind}, dpiChange(t, f.model)))
	if len(p.Ops) != 2 || p.Ops[0].Phase != plan.Bind {
		t.Fatalf("ops %+v", p.Ops)
	}
	f.applyKilled(p, 2)
	d := f.device()
	d.Image = loaded(t, f.image())
	if _, ok := d.Image.Get(extentOf(t, mouse.ShortcutExtent, 3)); ok {
		t.Fatal("the load read shortcut 3, which nothing binds")
	}
	if _, err := f.x.Recover(context.Background(), f.status().Open[0], safety.Back, d, nil); err != nil {
		t.Fatalf("recover back: %v", err)
	}
	sameImage(t, "device after rolling back", f.image(), f.start)
}

// A record that a run disabled and then bound again is described by what
// the run meant to leave there, not by the step in between.
func TestRecoveryDescribesTheBindingItRestores(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 70))
	if p.Ops[0].Phase != plan.Neutralise || p.Ops[2].Phase != plan.Bind || p.Ops[0].Extent != p.Ops[2].Extent {
		t.Fatalf("plan phases %v, %v", p.Ops[0].Phase, p.Ops[2].Phase)
	}
	f.applyKilled(p, 21)
	r := f.status().Open[0]
	in := must[*safety.Inspection](t)(f.x.Inspect(context.Background(), r, f.device()))
	if got := in.Extents[0].Desc; got != p.Ops[2].Desc {
		t.Errorf("the binding is described as %q, want %q", got, p.Ops[2].Desc)
	}
	for how, word := range map[safety.Strategy]string{safety.Forward: "finish: ", safety.Back: "roll back: "} {
		rp := must[plan.Plan](t)(safety.RecoveryPlan(in, how, f.device()))
		for _, op := range rp.Ops {
			if op.Extent == p.Ops[0].Extent && op.Phase == plan.Bind && op.Desc != word+p.Ops[2].Desc {
				t.Errorf("%v binds with %q", how, op.Desc)
			}
		}
	}
}
