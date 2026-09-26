package plan

import (
	"bytes"
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

func (p Plan) Validate(im *flash.Image, l Layout) error {
	if err := l.check(); err != nil {
		return err
	}
	if len(p.Ops) == 0 {
		return nil
	}
	if m, ok := catalog.Resolve(p.Device.CID, p.Device.MID); !ok || m.Family != catalog.FamilyMouse {
		return newError(ErrDevice, "cid "+hexBytes(p.Device.CID)+" mid "+hexBytes(p.Device.MID))
	}
	if im == nil {
		im = flash.New()
	}
	refs := make([]ref, len(p.Ops))
	rewritten := map[ref]bool{}
	for i, op := range p.Ops {
		r, err := l.checkOp(i, op)
		if err != nil {
			return err
		}
		if i > 0 && op.Phase < p.Ops[i-1].Phase {
			return newError(ErrOrder, describe(op)+" follows "+describe(p.Ops[i-1]))
		}
		for _, prev := range p.Ops[:i] {
			if prev.Extent.Overlaps(op.Extent) && !twoPhase(prev, op) {
				return newError(ErrOverlap, describe(op)+" overlaps "+describe(prev))
			}
		}
		refs[i] = r
		if op.Phase == Body {
			rewritten[r] = true
		}
	}

	sim := im.Clone()
	lefts := l.leftClicks(sim)
	bound := map[int]bool{}
	for i, op := range p.Ops {
		r := refs[i]
		cur, ok := sim.Get(op.Extent)
		if !ok {
			return newError(ErrUnread, describe(op))
		}
		if !bytes.Equal(cur, op.Old) {
			return newError(ErrStale, describe(op))
		}
		switch op.Phase {
		case Neutralise:
			if !hitsAny(targets(r.slot, op.Old), rewritten) {
				return newError(ErrPhase, describe(op)+": none of its bodies is rewritten")
			}
		case Body:
			if err := l.unbound(sim, r, op); err != nil {
				return err
			}
		case Bind:
			bound[r.slot] = true
		}
		_ = sim.Set(op.Extent.Addr, op.New)
		if op.Phase == Body && !empty(op.New) {
			switch known, valid := l.body(sim, r); {
			case !known:
				return newError(ErrUnread, describe(op)+": the record it declares runs into unread bytes")
			case !valid:
				return newError(ErrChecksum, describe(op)+": "+r.String()+" holds no valid record afterwards")
			}
		}
		if (op.Phase == Neutralise || op.Phase == Bind) && lefts > 0 && l.leftClicks(sim) == 0 {
			return newError(ErrLeftClick, describe(op))
		}
	}

	for k := range l.Bindings.Count {
		if !bound[k] {
			continue
		}
		b, _ := sim.Get(l.Bindings.slot(k))
		for _, t := range targets(k, b) {
			switch known, valid := l.body(sim, t); {
			case !known:
				return newError(ErrUnread, "binding "+strconv.Itoa(k)+" points at "+t.String())
			case !valid:
				return newError(ErrBinding, "binding "+strconv.Itoa(k)+" points at "+t.String()+", which holds no valid record")
			}
		}
	}
	return nil
}

func (l Layout) checkOp(i int, op Op) (ref, error) {
	where := describe(op)
	if op.Seq != i+1 {
		return ref{}, newError(ErrMalformed, where+" is at position "+strconv.Itoa(i+1))
	}
	if op.Phase < Neutralise || op.Phase > Record {
		return ref{}, newError(ErrMalformed, where+": unknown phase")
	}
	if !inImage(op.Extent) {
		return ref{}, newError(ErrExtent, where+" is outside the image")
	}
	if len(op.Old) != op.Extent.Len || len(op.New) != op.Extent.Len {
		return ref{}, newError(ErrMalformed, where+": old and new bytes must match the extent length")
	}
	if bytes.Equal(op.Old, op.New) {
		return ref{}, newError(ErrMalformed, where+" writes the bytes already there")
	}
	if op.Tier < catalog.Experimental {
		return ref{}, newError(ErrTier, where+" is "+op.Tier.String())
	}
	for _, f := range l.Frozen {
		if f.Overlaps(op.Extent) {
			return ref{}, newError(ErrFrozen, where+" touches "+f.String())
		}
	}
	r, ok := l.classify(op.Extent)
	if !ok {
		return ref{}, newError(ErrExtent, where+" is not a whole slot or record")
	}
	if want := phaseOf(r.kind); op.Phase != want && (op.Phase != Neutralise || want != Bind) {
		return ref{}, newError(ErrPhase, where+" writes "+r.String())
	}
	switch op.Phase {
	case Neutralise:
		if !bytes.Equal(op.New, disable) {
			return ref{}, newError(ErrPhase, where+" must write Disable")
		}
	case Bind, Record:
		if sum(op.New) != 0x55 {
			return ref{}, newError(ErrChecksum, where)
		}
	}
	return r, nil
}

func (l Layout) unbound(sim *flash.Image, body ref, op Op) error {
	for k := range l.Bindings.Count {
		b, ok := sim.Get(l.Bindings.slot(k))
		if !ok {
			return newError(ErrUnread, describe(op)+": binding "+strconv.Itoa(k)+" is unread")
		}
		if slices.Contains(targets(k, b), body) {
			return newError(ErrBinding, describe(op)+": binding "+strconv.Itoa(k)+" points at "+body.String())
		}
	}
	return nil
}

func twoPhase(first, second Op) bool {
	return first.Phase == Neutralise && second.Phase == Bind && first.Extent == second.Extent
}

func hitsAny(ts []ref, set map[ref]bool) bool {
	for _, t := range ts {
		if set[t] {
			return true
		}
	}
	return false
}

func describe(op Op) string {
	return "op " + strconv.Itoa(op.Seq) + " (" + op.Phase.String() + " " + op.Extent.String() + ")"
}
