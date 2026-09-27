package safety

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

// Inspection is what the extents of a run hold now, read again from the
// device.
type Inspection struct {
	Run *Run
	// Image is the device image the inspection started from with every
	// extent of the run read again.
	Image   *flash.Image
	Extents []ExtentState
	Ops     []OpState
}

// ExtentState is one extent a run wrote: the bytes before the run's first op
// on it, the bytes its last op meant to leave, and what it holds now.
type ExtentState struct {
	Extent flash.Extent
	Before []byte
	After  []byte
	Found  []byte
	Class  Class // ClassOld, ClassNew, ClassMid or ClassTorn
	Tier   catalog.Tier
	Desc   string // the last op's: what the run meant to leave there
	mids   [][]byte
}

// OpState is one op of the run and what its extent holds compared with that
// op's own bytes: ClassNew is done, ClassOld not applied, ClassTorn neither.
type OpState struct {
	OpRecord
	Now Class
}

// Torn reports whether an extent holds bytes no op of the run left there.
func (in *Inspection) Torn() bool {
	return slices.ContainsFunc(in.Extents, func(e ExtentState) bool { return e.Class == ClassTorn })
}

// Inspect reads every extent of r from the device and classifies it. It
// also reads the records the bindings point at, as they were before r, as r
// meant to leave them and as they are now, so that RecoveryPlan can check
// either way back. The device must be the one r was written to, with the
// same active profile.
func (x *Executor) Inspect(ctx context.Context, r *Run, d Device) (*Inspection, error) {
	return x.inspect(x.runner(ctx, nil), r, d)
}

func (x *Executor) inspect(rd *runner, r *Run, d Device) (*Inspection, error) {
	if err := r.Matches(d.Identity, d.Profile); err != nil {
		return nil, err
	}
	ss := spans(r.Ops)
	im, err := x.fresh(rd, d, ss)
	if err != nil {
		return nil, err
	}
	in := &Inspection{Run: r, Image: im}
	for _, s := range ss {
		s.Found, _ = im.Get(s.Extent)
		s.Class = s.classify()
		in.Extents = append(in.Extents, s)
	}
	for _, o := range r.Ops {
		found, _ := in.Image.Get(o.Extent)
		in.Ops = append(in.Ops, OpState{OpRecord: o, Now: classify(o.Op, found)})
	}
	return in, nil
}

// fresh reads the extents of ss from the device over a copy of d.Image,
// after a fresh cmd 3. It then reads what checking a plan that gives those
// extents their bytes from before the run, from after it, or their bytes now
// needs: the records every binding then points at (§6.2). A reload after a
// run reads only the bodies bound at that moment, and a body rewritten with
// fewer events hides the tail of the record it replaced.
func (x *Executor) fresh(rd *runner, d Device, ss []ExtentState) (*flash.Image, error) {
	if err := rd.poll(); err != nil {
		return nil, err
	}
	if err := x.link.OnlineCheck(rd.ctx); err != nil {
		return nil, err
	}
	out := flash.New()
	if d.Image != nil {
		out = d.Image.Clone()
	}
	read := func(e flash.Extent) error {
		found, err := rd.read(e, false)
		if err != nil {
			return fmt.Errorf("reading %s: %w", e, err)
		}
		return out.Set(e.Addr, found)
	}
	for _, s := range ss {
		if err := read(s.Extent); err != nil {
			return nil, err
		}
	}
	t := d.Layout.Bindings
	for _, bytesOf := range []func(ExtentState) []byte{
		func(s ExtentState) []byte { return s.Before },
		func(s ExtentState) []byte { return s.After },
		func(s ExtentState) []byte { return nil },
	} {
		for k := range t.Count {
			for range maxBodyReads {
				sim := out.Clone()
				for _, s := range ss {
					if b := bytesOf(s); b != nil {
						_ = sim.Set(s.Extent.Addr, b)
					}
				}
				b, ok := sim.Get(flash.Extent{Addr: t.Base + k*t.Stride, Len: t.Stride})
				if !ok {
					break
				}
				e, ok := d.Layout.Unread(sim, k, b)
				missing := unknown(out, e)
				if !ok || len(missing) == 0 {
					break
				}
				for _, u := range missing {
					if err := read(u); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return out, nil
}

// maxBodyReads bounds the reads for one binding: the count of each of its
// bodies, then each record.
const maxBodyReads = 4

// unknown returns the runs of e that im does not know.
func unknown(im *flash.Image, e flash.Extent) []flash.Extent {
	var out []flash.Extent
	start := -1
	for a := e.Addr; a <= e.End(); a++ {
		_, known := im.Byte(a)
		missing := a < e.End() && !known
		switch {
		case missing && start < 0:
			start = a
		case !missing && start >= 0:
			out = append(out, flash.Extent{Addr: start, Len: a - start})
			start = -1
		}
	}
	return out
}

// spans groups ops by extent, in the order the extents are first written. The
// ops of one plan either share an extent exactly or do not overlap.
func spans(ops []OpRecord) []ExtentState {
	var out []ExtentState
	for _, o := range ops {
		i := slices.IndexFunc(out, func(s ExtentState) bool { return s.Extent == o.Extent })
		if i < 0 {
			out = append(out, ExtentState{Extent: o.Extent, Before: o.Old, After: o.New, Tier: o.Tier, Desc: o.Desc})
			continue
		}
		out[i].After, out[i].Tier, out[i].Desc = o.New, min(out[i].Tier, o.Tier), o.Desc
	}
	for i := range out {
		for _, o := range ops {
			if o.Extent == out[i].Extent {
				out[i].mids = append(out[i].mids, o.Old, o.New)
			}
		}
	}
	return out
}

func (s ExtentState) classify() Class {
	switch {
	case bytes.Equal(s.Found, s.After):
		return ClassNew
	case bytes.Equal(s.Found, s.Before):
		return ClassOld
	case slices.ContainsFunc(s.mids, func(b []byte) bool { return bytes.Equal(b, s.Found) }):
		return ClassMid
	}
	return ClassTorn
}

// RecoveryPlan builds the plan that settles an inspected run: Forward gives
// every extent the bytes the run meant to leave, Back the bytes it held
// before. plan.New orders it, disables every binding whose body it rewrites
// first, and validates it, so no binding ends up pointing at a torn or
// invalid body. Extents that already hold their target are left out.
func RecoveryPlan(in *Inspection, how Strategy, d Device) (plan.Plan, error) {
	if how != Forward && how != Back {
		return plan.Plan{}, fmt.Errorf("%w: %s", ErrStrategy, how)
	}
	changes := make([]plan.Change, 0, len(in.Extents))
	for _, e := range in.Extents {
		c := plan.Change{Addr: e.Extent.Addr, New: e.After, Tier: e.Tier, Desc: "finish: " + e.Desc}
		if how == Back {
			c.New, c.Desc = e.Before, "roll back: "+e.Desc
		}
		changes = append(changes, c)
	}
	return plan.New(d.Identity, d.Profile, in.Image, d.Layout, changes)
}

// Recover settles the open run r. Forward and Back inspect it, then write and
// verify a recovery run through the same path as an apply; Leave only records
// the decision, and is refused while an extent is torn. On success r and its
// unfinished recovery runs are marked settled.
func (x *Executor) Recover(ctx context.Context, r *Run, how Strategy, d Device, on func(OpEvent)) (Result, error) {
	if !r.Open() {
		return Result{}, fmt.Errorf("%w: %s", ErrNotOpen, r.ID)
	}
	if how != Forward && how != Back && how != Leave {
		return Result{}, fmt.Errorf("%w: %s", ErrStrategy, how)
	}
	rd := x.runner(ctx, on)
	in, err := x.inspect(rd, r, d)
	if err != nil {
		return Result{}, err
	}
	if how == Leave {
		if in.Torn() {
			return Result{}, fmt.Errorf("%w: write the old or the new bytes instead", ErrTorn)
		}
		return Result{}, x.j.resolve(r, Leave, "", in.kept())
	}
	p, err := RecoveryPlan(in, how, d)
	if err != nil {
		return Result{}, err
	}
	d.Image = in.Image
	res, err := x.run(rd, KindRecover, r.ID, p, d)
	if err != nil {
		return res, err
	}
	return res, x.j.resolve(r, how, res.Run, nil)
}

// kept lists the ops whose bytes the run's extents hold now: for each extent
// that does not hold its old bytes, the last op that left what was found
// there. A run settled with Leave counts these as what it wrote.
func (in *Inspection) kept() []int {
	var out []int
	for _, e := range in.Extents {
		if e.Class == ClassOld {
			continue
		}
		for _, o := range slices.Backward(in.Run.Ops) {
			if o.Extent == e.Extent && bytes.Equal(o.New, e.Found) {
				out = append(out, o.Seq)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}
