package safety

import (
	"bytes"
	"context"
	"fmt"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

// RevertPlan builds the plan that undoes run r: every extent r changed gets
// back the bytes it held before r's first op on it. Only ops whose bytes the
// device holds because of r count: all of them once r completed or was
// finished, the verified ones otherwise. d.Image must hold what the device
// holds now, and each of those extents must still hold what r left there. The
// plan goes through plan.New, so it is ordered, two-phase and validated like
// any other.
func RevertPlan(r *Run, d Device) (plan.Plan, error) {
	if err := r.Matches(d.Identity, d.Profile); err != nil {
		return plan.Plan{}, err
	}
	targets := revertSpans(r)
	if len(targets) == 0 {
		return plan.Plan{}, fmt.Errorf("%w: %s", ErrNothing, r.ID)
	}
	im := d.Image
	if im == nil {
		im = flash.New()
	}
	changes := make([]plan.Change, 0, len(targets))
	for _, s := range targets {
		cur, ok := im.Get(s.Extent)
		if !ok {
			return plan.Plan{}, fmt.Errorf("%w: %s", plan.ErrUnread, s.Extent)
		}
		if !bytes.Equal(cur, s.After) {
			return plan.Plan{}, fmt.Errorf("%w: %s holds % x, run %s left % x", ErrDiverged, s.Extent, cur, r.ID, s.After)
		}
		changes = append(changes, plan.Change{Addr: s.Extent.Addr, New: s.Before, Tier: s.Tier, Desc: "revert: " + s.Desc, Captured: s.Captured})
	}
	return plan.New(d.Identity, d.Profile, im, d.Layout, changes)
}

// revertSpans gives, for each extent an effective op of r wrote, the bytes
// before r's first op on it and after its last effective one.
func revertSpans(r *Run) []ExtentState {
	all := spans(r.Ops)
	eff := r.effective()
	var out []ExtentState
	for _, s := range all {
		var last *plan.Op
		for i := range eff {
			if eff[i].Extent == s.Extent {
				last = &eff[i]
			}
		}
		if last == nil {
			continue
		}
		s.After = last.New
		out = append(out, s)
	}
	return out
}

// Revert undoes st.Last, the newest run that changed the device, once the
// journal is clean. It reads the run's extents again, and the records the
// bindings point at before and after it, builds the plan with RevertPlan and
// writes it as a revert run.
func (x *Executor) Revert(ctx context.Context, r *Run, d Device, on func(OpEvent)) (Result, error) {
	st, err := x.j.Status()
	if err != nil {
		return Result{}, err
	}
	switch {
	case !st.Clean():
		return Result{}, ErrNotClean
	case st.Last == nil || st.Last.ID != r.ID:
		return Result{}, fmt.Errorf("%w: %s", ErrNotLast, r.ID)
	}
	rd := x.runner(ctx, on)
	p, d, err := x.planRevert(rd, r, d)
	if err != nil {
		return Result{}, err
	}
	return x.run(rd, KindRevert, r.ID, p, d)
}

// PlanRevert reads what Revert reads and returns the plan it would write,
// with the device that plan was made on. It writes nothing and needs no
// journal, so a dry run can apply the plan elsewhere.
func (x *Executor) PlanRevert(ctx context.Context, r *Run, d Device) (plan.Plan, Device, error) {
	return x.planRevert(x.runner(ctx, nil), r, d)
}

func (x *Executor) planRevert(rd *runner, r *Run, d Device) (plan.Plan, Device, error) {
	if err := r.Matches(d.Identity, d.Profile); err != nil {
		return plan.Plan{}, d, err
	}
	im, err := x.fresh(rd, d, revertSpans(r))
	if err != nil {
		return plan.Plan{}, d, err
	}
	d.Image = im
	p, err := RevertPlan(r, d)
	return p, d, err
}
