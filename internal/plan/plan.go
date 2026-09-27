package plan

import (
	"bytes"
	"cmp"
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

type Phase uint8

const (
	Neutralise Phase = iota + 1
	Body
	Bind
	Record
	// Captured writes bytes a backup captured, as they were, over an extent
	// the layout gives no valid record (Layout.Capturable).
	Captured
)

var phaseNames = [...]string{"", "neutralise", "body", "bind", "record", "captured"}

func (p Phase) String() string {
	if p >= Neutralise && p <= Captured {
		return phaseNames[p]
	}
	return "Phase(" + strconv.Itoa(int(p)) + ")"
}

type Op struct {
	Seq    int
	Extent flash.Extent
	Old    []byte
	New    []byte
	Phase  Phase
	Desc   string
	Tier   catalog.Tier
}

type Change struct {
	Addr int
	New  []byte
	Desc string
	Tier catalog.Tier
	// Captured asks for New to be written as a backup captured it, whether
	// or not it is a valid record; Tier must then be Experimental.
	Captured bool
}

type Plan struct {
	Device  Identity
	Profile *byte
	Ops     []Op
}

// New turns record changes into a validated plan. It reads the old bytes from im,
// drops changes that write what is already there, disables every binding that points
// at a body being rewritten and binds it again afterwards, and orders the ops, with
// captured bytes last.
func New(dev Identity, profile *byte, im *flash.Image, l Layout, changes []Change) (Plan, error) {
	if err := l.check(); err != nil {
		return Plan{}, err
	}
	if im == nil {
		im = flash.New()
	}
	var (
		ops       []Op
		seen      []flash.Extent
		bindings  = map[int]int{}
		rewritten = map[ref]catalog.Tier{}
	)
	for i, c := range changes {
		e := flash.Extent{Addr: c.Addr, Len: len(c.New)}
		where := "change " + strconv.Itoa(i) + " at " + e.String()
		if !inImage(e) {
			return Plan{}, newError(ErrExtent, where+" is outside the image")
		}
		if slices.ContainsFunc(l.Frozen, e.Overlaps) {
			return Plan{}, newError(ErrFrozen, where+" touches a frozen extent")
		}
		r, ok := l.classify(e)
		switch {
		case c.Captured && !l.Capturable(e):
			return Plan{}, newError(ErrExtent, where+" is not in the settings page clear of the tables and records")
		case c.Captured:
			r = ref{kind: kindCaptured}
		case !ok:
			return Plan{}, newError(ErrExtent, where+" is not a whole slot or record")
		}
		for _, s := range seen {
			if s.Overlaps(e) {
				return Plan{}, newError(ErrOverlap, where+" overlaps "+s.String())
			}
		}
		seen = append(seen, e)
		old, ok := im.Get(e)
		if !ok {
			return Plan{}, newError(ErrUnread, where)
		}
		if bytes.Equal(old, c.New) {
			continue
		}
		ops = append(ops, Op{Extent: e, Old: old, New: slices.Clone(c.New), Phase: phaseOf(r.kind), Desc: c.Desc, Tier: c.Tier})
		switch r.kind {
		case kindBinding:
			bindings[r.slot] = len(ops) - 1
		case kindShortcut, kindMacro:
			rewritten[r] = c.Tier
		}
	}
	if len(rewritten) > 0 {
		ops = neutralise(ops, im, l, bindings, rewritten)
	}
	slices.SortStableFunc(ops, compareOps)
	for i := range ops {
		ops[i].Seq = i + 1
	}
	if profile != nil {
		v := *profile
		profile = &v
	}
	p := Plan{Device: dev, Profile: profile, Ops: ops}
	if err := p.Validate(im, l); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func neutralise(ops []Op, im *flash.Image, l Layout, bindings map[int]int, rewritten map[ref]catalog.Tier) []Op {
	for k := range l.Bindings.Count {
		s := l.Bindings.slot(k)
		cur, ok := im.Get(s)
		if !ok {
			continue
		}
		tier, hit := catalog.Verified, false
		for _, t := range targets(k, cur) {
			if bt, ok := rewritten[t]; ok {
				tier, hit = min(tier, bt), true
			}
		}
		if !hit {
			continue
		}
		name := "binding " + strconv.Itoa(k)
		n := Op{Extent: s, Old: cur, New: slices.Clone(disable), Phase: Neutralise,
			Desc: "disable " + name + " while its body is rewritten", Tier: tier}
		j, changed := bindings[k]
		switch {
		case changed && bytes.Equal(ops[j].New, disable):
			n.Desc, n.Tier = ops[j].Desc, min(tier, ops[j].Tier)
			ops[j] = n
			continue
		case changed:
			ops[j].Old = slices.Clone(disable)
		default:
			ops = append(ops, Op{Extent: s, Old: slices.Clone(disable), New: cur, Phase: Bind,
				Desc: "restore " + name, Tier: tier})
		}
		ops = append(ops, n)
	}
	return ops
}

func compareOps(a, b Op) int {
	if c := cmp.Compare(a.Phase, b.Phase); c != 0 || a.Phase == Record {
		return c
	}
	if a.Phase == Bind {
		if c := cmp.Compare(gainsLeftClick(b), gainsLeftClick(a)); c != 0 {
			return c
		}
	}
	return cmp.Compare(a.Extent.Addr, b.Extent.Addr)
}

func gainsLeftClick(o Op) int {
	if bytes.Equal(o.New, leftClick) {
		return 1
	}
	return 0
}

func phaseOf(k kind) Phase {
	switch k {
	case kindBinding:
		return Bind
	case kindShortcut, kindMacro:
		return Body
	case kindCaptured:
		return Captured
	}
	return Record
}
