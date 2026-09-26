package plan_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

type step struct {
	phase    plan.Phase
	addr     int
	old, new []byte
	tier     catalog.Tier
	desc     string
}

func checkSteps(t *testing.T, p plan.Plan, want []step) {
	t.Helper()
	if len(p.Ops) != len(want) {
		t.Fatalf("%d ops, want %d: %+v", len(p.Ops), len(want), p.Ops)
	}
	for i, w := range want {
		o := p.Ops[i]
		got := step{o.Phase, o.Extent.Addr, o.Old, o.New, o.Tier, o.Desc}
		if o.Seq != i+1 || o.Extent.Len != len(w.new) || !reflect.DeepEqual(got, w) {
			t.Errorf("op %d = seq %d %+v, want %+v", i, o.Seq, got, w)
		}
	}
}

func TestNewTwoPhase(t *testing.T) {
	im := dumpImage(t)
	p, err := plan.New(em11, nil, im, em11Layout(), []plan.Change{
		{Addr: shortcutAddr(3), New: playPause, Desc: "button 3: Play/Pause", Tier: catalog.Untested},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p, []step{
		{plan.Neutralise, bindAddr(3), shortcut, disable, catalog.Untested, "disable binding 3 while its body is rewritten"},
		{plan.Body, shortcutAddr(3), ctrlTab[:8], playPause, catalog.Untested, "button 3: Play/Pause"},
		{plan.Bind, bindAddr(3), disable, shortcut, catalog.Untested, "restore binding 3"},
	})
	if p.Device != em11 || p.Profile != nil {
		t.Errorf("device %+v profile %v", p.Device, p.Profile)
	}
	after := apply(t, im, p)
	if got := get(t, after, shortcutAddr(3), 8); !reflect.DeepEqual(got, playPause) {
		t.Errorf("body after apply = % x", got)
	}
}

func TestNewBodyBeforeFirstBinding(t *testing.T) {
	ab := macroAB(t)
	p, err := plan.New(em11, nil, dumpImage(t), em11Layout(), []plan.Change{
		{Addr: bindAddr(4), New: macroBinding(4, 1), Desc: "bind", Tier: catalog.Untested},
		{Addr: macroAddr(4), New: ab, Desc: "body", Tier: catalog.Untested},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p, []step{
		{plan.Body, macroAddr(4), fill(0xFF, len(ab)), ab, catalog.Untested, "body"},
		{plan.Bind, bindAddr(4), shortcut, macroBinding(4, 1), catalog.Untested, "bind"},
	})
}

func TestNewUserBindingDuringTwoPhase(t *testing.T) {
	ab := macroAB(t)
	cases := []struct {
		name    string
		binding []byte
		macro   bool
		want    []step
	}{
		{"rebound elsewhere", right, false, []step{
			{plan.Neutralise, bindAddr(3), shortcut, disable, catalog.Experimental, "disable binding 3 while its body is rewritten"},
			{plan.Body, shortcutAddr(3), ctrlTab[:8], playPause, catalog.Experimental, "body"},
			{plan.Bind, bindAddr(3), disable, right, catalog.Untested, "user"},
		}},
		{"disabled", disable, false, []step{
			{plan.Neutralise, bindAddr(3), shortcut, disable, catalog.Experimental, "user"},
			{plan.Body, shortcutAddr(3), ctrlTab[:8], playPause, catalog.Experimental, "body"},
		}},
		{"rebound to a macro", macroBinding(3, 2), true, []step{
			{plan.Neutralise, bindAddr(3), shortcut, disable, catalog.Experimental, "disable binding 3 while its body is rewritten"},
			{plan.Body, shortcutAddr(3), ctrlTab[:8], playPause, catalog.Experimental, "body"},
			{plan.Body, macroAddr(3), fill(0xFF, len(ab)), ab, catalog.Verified, "macro"},
			{plan.Bind, bindAddr(3), disable, macroBinding(3, 2), catalog.Untested, "user"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			changes := []plan.Change{
				{Addr: bindAddr(3), New: c.binding, Desc: "user", Tier: catalog.Untested},
				{Addr: shortcutAddr(3), New: playPause, Desc: "body", Tier: catalog.Experimental},
			}
			if c.macro {
				changes = append(changes, plan.Change{Addr: macroAddr(3), New: ab, Desc: "macro", Tier: catalog.Verified})
			}
			p, err := plan.New(em11, nil, dumpImage(t), em11Layout(), changes)
			if err != nil {
				t.Fatal(err)
			}
			checkSteps(t, p, c.want)
		})
	}
}

func TestNewNeutralisesEveryBindingOfABody(t *testing.T) {
	im := dumpImage(t)
	ab, ab2 := macroAB(t), macroAB(t)
	ab2[1] = 'x'
	set(t, im, macroAddr(6), ab)
	set(t, im, macroAddr(9), ab)
	set(t, im, bindAddr(6), macroBinding(6, 1))
	set(t, im, bindAddr(9), macroBinding(6, 3))
	p, err := plan.New(em11, nil, im, em11Layout(), []plan.Change{
		{Addr: macroAddr(6), New: ab2, Desc: "rename", Tier: catalog.Untested},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p, []step{
		{plan.Neutralise, bindAddr(6), macroBinding(6, 1), disable, catalog.Untested, "disable binding 6 while its body is rewritten"},
		{plan.Neutralise, bindAddr(9), macroBinding(6, 3), disable, catalog.Untested, "disable binding 9 while its body is rewritten"},
		{plan.Body, macroAddr(6), ab, ab2, catalog.Untested, "rename"},
		{plan.Bind, bindAddr(6), disable, macroBinding(6, 1), catalog.Untested, "restore binding 6"},
		{plan.Bind, bindAddr(9), disable, macroBinding(6, 3), catalog.Untested, "restore binding 9"},
	})
}

func TestNewNeutraliseTakesTheLowestTier(t *testing.T) {
	im := dumpImage(t)
	ab, ab2 := macroAB(t), macroAB(t)
	ab2[1] = 'x'
	set(t, im, macroAddr(3), ab)
	set(t, im, macroAddr(7), ab)
	set(t, im, bindAddr(7), macroBinding(3, 1))
	p, err := plan.New(em11, nil, im, em11Layout(), []plan.Change{
		{Addr: macroAddr(7), New: ab2, Tier: catalog.Experimental},
		{Addr: macroAddr(3), New: ab2, Tier: catalog.Verified},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range p.Ops {
		if o.Extent.Addr == bindAddr(7) && o.Tier != catalog.Experimental {
			t.Errorf("%v op on binding 7 has tier %v, want experimental", o.Phase, o.Tier)
		}
	}
	if len(p.Ops) != 4 {
		t.Errorf("%d ops, want neutralise, two bodies and a rebind", len(p.Ops))
	}
}

func TestNewOrder(t *testing.T) {
	im := dumpImage(t)
	set(t, im, bindAddr(7), shortcut)
	set(t, im, shortcutAddr(7), playPause)
	ab := macroAB(t)
	p, err := plan.New(em11, nil, im, em11Layout(), []plan.Change{
		{Addr: 4, New: h("02 53"), Desc: "current 2", Tier: catalog.Untested},
		{Addr: bindAddr(0), New: right, Desc: "0 right", Tier: catalog.Untested},
		{Addr: macroAddr(9), New: ab, Desc: "macro 9", Tier: catalog.Untested},
		{Addr: 32, New: h("3f 3f 11 c6"), Desc: "dpi 5", Tier: catalog.Untested},
		{Addr: bindAddr(1), New: leftClick, Desc: "1 left", Tier: catalog.Untested},
		{Addr: shortcutAddr(7), New: ctrlTab, Desc: "shortcut 7", Tier: catalog.Untested},
		{Addr: 2, New: h("05 50"), Desc: "stages", Tier: catalog.Untested},
		{Addr: bindAddr(9), New: macroBinding(9, 1), Desc: "9 macro", Tier: catalog.Untested},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range p.Ops {
		got = append(got, o.Phase.String()+" "+o.Desc)
	}
	want := []string{
		"neutralise disable binding 7 while its body is rewritten",
		"body shortcut 7",
		"body macro 9",
		"bind 1 left",
		"bind 0 right",
		"bind restore binding 7",
		"bind 9 macro",
		"record current 2",
		"record dpi 5",
		"record stages",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order:\n got %q\nwant %q", got, want)
	}
}

func TestNewDropsNoOps(t *testing.T) {
	im := dumpImage(t)
	profile := byte(2)
	p, err := plan.New(em11, &profile, im, em11Layout(), []plan.Change{
		{Addr: 12, New: dpi800, Tier: catalog.Untested},
		{Addr: shortcutAddr(3), New: ctrlTab, Tier: catalog.Untested},
		{Addr: bindAddr(0), New: leftClick, Tier: catalog.Untested},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Ops) != 0 {
		t.Fatalf("ops = %+v, want none", p.Ops)
	}
	profile = 3
	if p.Profile == nil || *p.Profile != 2 {
		t.Errorf("profile = %v, want a copy of 2", p.Profile)
	}
}

func TestNewCopiesItsInput(t *testing.T) {
	b := append([]byte(nil), dpi1600...)
	p, err := plan.New(em11, nil, dumpImage(t), em11Layout(), []plan.Change{{Addr: 12, New: b, Tier: catalog.Untested}})
	if err != nil {
		t.Fatal(err)
	}
	b[0] = 0
	if !reflect.DeepEqual(p.Ops[0].New, dpi1600) {
		t.Errorf("plan follows the caller's slice: % x", p.Ops[0].New)
	}
}

func TestNewErrors(t *testing.T) {
	cases := []struct {
		name    string
		dev     plan.Identity
		im      func(t *testing.T) *flash.Image
		l       func(*plan.Layout)
		changes []plan.Change
		want    error
	}{
		{"bad layout", em11, nil, func(l *plan.Layout) { l.Buttons = nil }, nil, plan.ErrMalformed},
		{"empty change", em11, nil, nil, []plan.Change{{Addr: 12}}, plan.ErrExtent},
		{"outside the image", em11, nil, nil, []plan.Change{{Addr: flash.Size - 1, New: dpi1600}}, plan.ErrExtent},
		{"misaligned", em11, nil, nil, []plan.Change{{Addr: bindAddr(1) + 1, New: right}}, plan.ErrExtent},
		{"overlapping changes", em11, nil, nil, []plan.Change{
			{Addr: 12, New: dpi1600, Tier: catalog.Untested}, {Addr: 14, New: h("2a 2b"), Tier: catalog.Untested},
		}, plan.ErrExtent},
		{"overlapping no-op", em11, nil, nil, []plan.Change{
			{Addr: 12, New: dpi800, Tier: catalog.Untested}, {Addr: 12, New: dpi1600, Tier: catalog.Untested},
		}, plan.ErrOverlap},
		{"outside every record", em11, nil, nil, []plan.Change{{Addr: 7000, New: h("00 55"), Tier: catalog.Untested}}, plan.ErrExtent},
		{"across two pairs", em11, nil, nil, []plan.Change{{Addr: 2, New: h("06 4f 03 fd"), Tier: catalog.Untested}}, plan.ErrExtent},
		{"half a record", em11, nil, nil, []plan.Change{{Addr: 42, New: h("00 55"), Tier: catalog.Untested}}, plan.ErrExtent},
		{"unmapped block", em11, nil, nil, []plan.Change{{Addr: 84, New: append(fill(0, 11), 0x55), Tier: catalog.Untested}}, plan.ErrExtent},
		{"unmapped pair", em11, nil, nil, []plan.Change{{Addr: 6, New: h("01 54"), Tier: catalog.Untested}}, plan.ErrExtent},
		{"unmapped pair left as it is", em11, nil, nil, []plan.Change{{Addr: 6, New: h("00 55"), Tier: catalog.Untested}}, plan.ErrExtent},
		{"optional field", em11, nil, nil, []plan.Change{{Addr: 189, New: h("01 54"), Tier: catalog.Untested}}, plan.ErrExtent},
		{"unread", em11, func(t *testing.T) *flash.Image {
			im := dumpImage(t)
			out := flash.New()
			set(t, out, 0, get(t, im, 0, 12))
			set(t, out, 16, get(t, im, 16, knownEnd-16))
			return out
		}, nil, []plan.Change{{Addr: 12, New: dpi1600, Tier: catalog.Untested}}, plan.ErrUnread},
		{"nil image", em11, func(*testing.T) *flash.Image { return nil }, nil,
			[]plan.Change{{Addr: 12, New: dpi1600, Tier: catalog.Untested}}, plan.ErrUnread},
		{"body with an unread binding", em11, func(t *testing.T) *flash.Image {
			full := dumpImage(t)
			im := flash.New()
			set(t, im, 0, get(t, full, 0, bindAddr(15)))
			set(t, im, shortcutBase, get(t, full, shortcutBase, knownEnd-shortcutBase))
			return im
		}, nil, []plan.Change{{Addr: shortcutAddr(3), New: playPause, Tier: catalog.Untested}}, plan.ErrUnread},
		{"keyboard", plan.Identity{CID: 3, MID: 4}, nil, nil,
			[]plan.Change{{Addr: 12, New: dpi1600, Tier: catalog.Untested}}, plan.ErrDevice},
		{"bad record", em11, nil, nil, []plan.Change{{Addr: 12, New: h("2a 2a 00 00"), Tier: catalog.Untested}}, plan.ErrChecksum},
		{"read-only tier", em11, nil, nil, []plan.Change{{Addr: 12, New: dpi1600, Tier: catalog.ReadOnly}}, plan.ErrTier},
		{"KeyOperation", em11, nil, nil, []plan.Change{{Addr: 8, New: h("01 54"), Tier: catalog.Untested}}, plan.ErrFrozen},
		{"last left click", em11, nil, nil, []plan.Change{{Addr: bindAddr(0), New: right, Tier: catalog.Untested}}, plan.ErrLeftClick},
		{"body cleared under its binding", em11, nil, nil,
			[]plan.Change{{Addr: shortcutAddr(3), New: fill(0, 14), Tier: catalog.Untested}}, plan.ErrBinding},
		{"binding to an empty body", em11, nil, nil,
			[]plan.Change{{Addr: bindAddr(7), New: shortcut, Tier: catalog.Untested}}, plan.ErrBinding},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			im := dumpImage(t)
			if c.im != nil {
				im = c.im(t)
			}
			l := em11Layout()
			if c.l != nil {
				c.l(&l)
			}
			p, err := plan.New(c.dev, nil, im, l, c.changes)
			if !errors.Is(err, c.want) {
				t.Fatalf("New = %v, want %v", err, c.want)
			}
			if !reflect.DeepEqual(p, plan.Plan{}) {
				t.Errorf("failed New returned %+v", p)
			}
		})
	}
}

func TestRevertPlan(t *testing.T) {
	im := dumpImage(t)
	l := em11Layout()
	ab := macroAB(t)
	p, err := plan.New(em11, nil, im, l, []plan.Change{
		{Addr: shortcutAddr(3), New: playPause, Tier: catalog.Untested},
		{Addr: macroAddr(4), New: ab, Tier: catalog.Untested},
		{Addr: bindAddr(4), New: macroBinding(4, 1), Tier: catalog.Untested},
		{Addr: bindAddr(1), New: leftClick, Tier: catalog.Untested},
		{Addr: 12, New: dpi1600, Tier: catalog.Untested},
	})
	if err != nil {
		t.Fatal(err)
	}
	after := apply(t, im, p)
	var back []plan.Change
	seen := map[flash.Extent]bool{}
	for _, o := range p.Ops {
		if !seen[o.Extent] {
			seen[o.Extent] = true
			back = append(back, plan.Change{Addr: o.Extent.Addr, New: o.Old, Tier: o.Tier})
		}
	}
	r, err := plan.New(em11, nil, after, l, back)
	if err != nil {
		t.Fatal(err)
	}
	if got := apply(t, after, r); !reflect.DeepEqual(got.Bytes(), im.Bytes()) {
		t.Fatal("revert does not restore the image")
	}
	var phases []plan.Phase
	for _, o := range r.Ops {
		phases = append(phases, o.Phase)
	}
	want := []plan.Phase{plan.Neutralise, plan.Neutralise, plan.Body, plan.Body, plan.Bind, plan.Bind, plan.Bind, plan.Record}
	if !reflect.DeepEqual(phases, want) {
		t.Errorf("revert phases = %v, want %v", phases, want)
	}
}
