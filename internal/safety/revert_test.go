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

func (f *fixture) revertLast() (safety.Result, error) {
	f.t.Helper()
	st := f.status()
	if st.Last == nil {
		f.t.Fatal("no run to revert")
	}
	return f.x.Revert(context.Background(), st.Last, f.device(), nil)
}

// Apply, revert, revert the revert: the device goes back to where it
// started, then forward to the plan again, through validated two-phase plans.
func TestRevertRoundTrip(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(slices.Concat(mixedChanges(t, f), macroChanges(t, 3, "fresh", 20)))
	applied, err := f.apply(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	post := f.image()

	res, err := f.revertLast()
	if err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("revert left extents %v", d)
	}
	f.checkOutside(p)
	checkBindings(t, f.image())
	st := f.status()
	rev := st.Find(res.Run)
	if !st.Clean() || rev.Kind != safety.KindRevert || rev.Of != applied.Run || st.Last.ID != res.Run {
		t.Fatalf("revert run %+v, last %+v", rev, st.Last)
	}
	// Two-phase holds on the way back: bindings to rewritten bodies are
	// disabled first.
	var phases []plan.Phase
	for _, o := range rev.Ops {
		phases = append(phases, o.Phase)
	}
	if !slices.Contains(phases, plan.Neutralise) || !slices.IsSorted(phases) {
		t.Fatalf("revert phases %v", phases)
	}

	if _, err := f.revertLast(); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), post, p); len(d) > 0 {
		t.Fatalf("redo left extents %v", d)
	}
	checkBindings(t, f.image())
}

// The device changed after the run: revert refuses instead of writing over
// the change.
func TestRevertRefusesADeviceThatChanged(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.dev.PressDPI(); err != nil {
		t.Fatal(err)
	}
	f.link.awaitPush(t)
	f.link.Pending()
	before := len(f.dev.Writes())
	if _, err := f.revertLast(); !errors.Is(err, safety.ErrDiverged) {
		t.Fatalf("err = %v, want ErrDiverged", err)
	}
	for _, w := range f.dev.Writes()[before:] {
		if w.Packet.Cmd() == wire.CmdWrite {
			t.Fatal("revert wrote after refusing")
		}
	}
}

func TestRevertOnlyTheLastRunOfACleanJournal(t *testing.T) {
	f := newFixture(t, setup{})
	if _, err := f.apply(f.plan(pairChange(t)), nil); err != nil {
		t.Fatal(err)
	}
	first := f.status().Last
	if _, err := f.apply(f.plan(dpiChange(t, f.model)), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.x.Revert(context.Background(), first, f.device(), nil); !errors.Is(err, safety.ErrNotLast) {
		t.Fatalf("err = %v, want ErrNotLast", err)
	}
	f.applyKilled(f.plan(shortcutChange(t)), 3)
	if _, err := f.x.Revert(context.Background(), f.status().Last, f.device(), nil); !errors.Is(err, safety.ErrNotClean) {
		t.Fatalf("err = %v, want ErrNotClean", err)
	}
	d := f.device()
	d.Profile = ptr(byte(2))
	if _, err := safety.RevertPlan(first, d); !errors.Is(err, safety.ErrProfile) {
		t.Fatalf("RevertPlan on another profile = %v, want ErrProfile", err)
	}
}

// Only the ops a run really wrote are reverted: here the first of two, the
// run having been stopped and left.
func TestRevertPartialRun(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 1 {
			f.link.abort()
		}
	}
	if _, err := f.apply(p, nil); !errors.Is(err, safety.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	f.link.afterWrite = nil
	if _, err := f.x.Recover(context.Background(), f.status().Open[0], safety.Leave, f.device(), nil); err != nil {
		t.Fatal(err)
	}
	rp := must[plan.Plan](t)(safety.RevertPlan(f.status().Last, f.device()))
	if len(rp.Ops) != 1 || rp.Ops[0].Extent != p.Ops[0].Extent {
		t.Fatalf("revert plan %+v, want only the DPI stage", rp.Ops)
	}
	if _, err := f.revertLast(); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("extents %v are not back", d)
	}
}

// A body written for the first time is reverted to the erased bytes it
// replaced, after its binding is disabled and before the old binding returns.
func TestRevertFirstBody(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(macroChanges(t, 3, "first", 8))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	st := f.status()
	rp := must[plan.Plan](t)(safety.RevertPlan(st.Last, f.device()))
	var phases []plan.Phase
	for _, op := range rp.Ops {
		phases = append(phases, op.Phase)
	}
	if !slices.Equal(phases, []plan.Phase{plan.Neutralise, plan.Body, plan.Bind}) {
		t.Fatalf("revert phases %v", phases)
	}
	if _, err := f.revertLast(); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("extents %v are not back", d)
	}
	checkBindings(t, f.image())
	fn, err := mouse.DecodeKeyFn(must[[]byte](t)(bytesAt(f, mouse.KeyFnExtent, 3)))
	if err != nil || fn.Type != mouse.TypeShortcut {
		t.Fatalf("binding 3 = %+v, %v; want its shortcut back", fn, err)
	}
}

func bytesAt(f *fixture, table func(int) (flash.Extent, bool), i int) ([]byte, error) {
	e, _ := table(i)
	b, ok := f.image().Get(e)
	if !ok {
		return nil, errors.New("unknown bytes")
	}
	return b, nil
}

// An abort that arrives while Revert reads the run's records again stops the
// revert before its first packet.
func TestAbortDuringRevertInspection(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	sent := cmd7(f)
	f.link.abort()
	if _, err := f.revertLast(); !errors.Is(err, safety.ErrAborted) || cmd7(f) != sent {
		t.Fatalf("revert after an abort: err %v, %d cmd 7 sent", err, cmd7(f)-sent)
	}
	if st := f.status(); !st.Clean() {
		t.Fatalf("open runs %+v", st.Open)
	}
}

// Reverting a run that moved slot 3 off its shortcut binds the shortcut
// again from an image that never held its body, as the image after a reload
// is (H3's slot 3 to Scroll Up and back).
func TestRevertRebindsABodyTheImageLacks(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan([]plan.Change{{Addr: extentOf(t, mouse.KeyFnExtent, 3).Addr, New: scrollUp, Tier: catalog.Untested, Desc: "slot 3: Scroll Up"}})
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	d := f.device()
	d.Image = loaded(t, f.image())
	if _, err := f.x.Revert(context.Background(), f.status().Last, d, nil); err != nil {
		t.Fatalf("revert: %v", err)
	}
	sameImage(t, "device after the revert", f.image(), f.start)
}

// Reverting a macro rewritten with fewer events needs the old record's tail,
// which a load after the rewrite does not read.
func TestRevertReadsTheOldRecordsTail(t *testing.T) {
	f := newFixture(t, setup{seed: func(t testing.TB, im *flash.Image) {
		for _, c := range macroChanges(t, 5, "long", 40) {
			if err := im.Set(c.Addr, c.New); err != nil {
				t.Fatal(err)
			}
		}
	}})
	p := f.plan(macroChanges(t, 5, "short", 4))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	d := f.device()
	d.Image = loaded(t, f.image())
	if _, err := f.x.Revert(context.Background(), f.status().Last, d, nil); err != nil {
		t.Fatalf("revert: %v", err)
	}
	sameImage(t, "device after the revert", f.image(), f.start)
	checkBindings(t, f.image())
}

// Leave settles a run whose unverified body reached the device whole. Revert
// then undoes every record the run changed, that body included, so the old
// binding never comes back pointing at the new body.
func TestRevertAfterLeaveRestoresEveryRecordTheRunChanged(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 20))
	body := slices.IndexFunc(p.Ops, func(o plan.Op) bool { return o.Phase == plan.Body })
	last := 0
	for _, o := range p.Ops[:body+1] {
		last += (o.Extent.Len + 9) / 10
	}
	f.applyKilled(p, last)
	if _, err := f.x.Recover(context.Background(), f.status().Open[0], safety.Leave, f.device(), nil); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := f.revertLast(); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("after Leave and Revert, extents %v still hold what the run wrote", d)
	}
	checkBindings(t, f.image())
}
