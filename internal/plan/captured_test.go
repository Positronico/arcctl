package plan_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

func captured(addr int, b []byte) plan.Change {
	return plan.Change{Addr: addr, New: b, Tier: catalog.Experimental, Captured: true, Desc: "captured"}
}

// Captured bytes go over what the layout gives no valid record, whole, as
// they are, after every other op and in address order.
func TestNewCaptured(t *testing.T) {
	im := dumpImage(t)
	old := func(addr, n int) []byte { return get(t, im, addr, n) }
	block := append(fill(0x11, 11), 0x22)
	p, err := plan.New(em11, nil, im, em11Layout(), []plan.Change{
		captured(185, h("07 07")),
		captured(84, block),
		{Addr: 12, New: dpi1600, Tier: catalog.Untested, Desc: "dpi"},
		captured(6, h("01 54")),
		captured(189, h("ff 00")),
		captured(251, h("aa bb cc dd ee")),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkSteps(t, p, []step{
		{plan.Record, 12, old(12, 4), dpi1600, catalog.Untested, "dpi"},
		{plan.Captured, 6, old(6, 2), h("01 54"), catalog.Experimental, "captured"},
		{plan.Captured, 84, old(84, 12), block, catalog.Experimental, "captured"},
		{plan.Captured, 185, old(185, 2), h("07 07"), catalog.Experimental, "captured"},
		{plan.Captured, 189, old(189, 2), h("ff 00"), catalog.Experimental, "captured"},
		{plan.Captured, 251, old(251, 5), h("aa bb cc dd ee"), catalog.Experimental, "captured"},
	})
}

func TestNewCapturedRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change plan.Change
		want   error
	}{
		{"a binding", captured(bindAddr(3), h("0b 01 00 49")), plan.ErrExtent},
		{"a body", captured(shortcutAddr(3), playPause), plan.ErrExtent},
		{"past the settings page", captured(252, h("00 00 00 00 00")), plan.ErrExtent},
		{"the extended block", captured(6960, h("01 54")), plan.ErrExtent},
		{"half a record", captured(186, h("00 00")), plan.ErrExtent},
		{"a record and a gap", captured(185, h("00 00 00 00")), plan.ErrExtent},
		{"KeyOperation", captured(8, h("01 54")), plan.ErrFrozen},
		{"untested", plan.Change{Addr: 6, New: h("01 54"), Tier: catalog.Untested, Captured: true}, plan.ErrTier},
		{"verified", plan.Change{Addr: 185, New: h("07 07"), Tier: catalog.Verified, Captured: true}, plan.ErrTier},
		{"invalid without Captured", plan.Change{Addr: 185, New: h("07 07"), Tier: catalog.Experimental}, plan.ErrChecksum},
		{"a gap without Captured", plan.Change{Addr: 6, New: h("01 54"), Tier: catalog.Experimental}, plan.ErrExtent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := plan.New(em11, nil, dumpImage(t), em11Layout(), []plan.Change{c.change})
			if !errors.Is(err, c.want) {
				t.Fatalf("New = %v, want %v", err, c.want)
			}
			if !reflect.DeepEqual(p, plan.Plan{}) {
				t.Errorf("failed New returned %+v", p)
			}
		})
	}
}

// Validate holds a plan it did not make to the same rules: a captured op
// needs a capturable extent and the experimental tier, and comes last.
func TestValidateCaptured(t *testing.T) {
	im := dumpImage(t)
	gap := op(plan.Captured, 84, get(t, im, 84, 12), fill(0x33, 12))
	gap.Tier = catalog.Experimental
	record := op(plan.Record, 12, dpi800, dpi1600)
	cases := []struct {
		name string
		ops  []plan.Op
		want error
	}{
		{"accepted", numbered(record, gap), nil},
		{"before a record", numbered(gap, record), plan.ErrOrder},
		{"untested", numbered(func() plan.Op { o := gap; o.Tier = catalog.Untested; return o }()), plan.ErrTier},
		{"over a binding", numbered(func() plan.Op {
			o := op(plan.Captured, bindAddr(3), shortcut, h("00 00 00 00"))
			o.Tier = catalog.Experimental
			return o
		}()), plan.ErrExtent},
		{"a gap as a record", numbered(func() plan.Op { o := gap; o.Phase = plan.Record; return o }()), plan.ErrExtent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := plan.Plan{Device: em11, Ops: c.ops}.Validate(im, em11Layout())
			if !errors.Is(err, c.want) {
				t.Fatalf("Validate = %v, want %v", err, c.want)
			}
		})
	}
}

func TestCapturable(t *testing.T) {
	l := em11Layout()
	for e, want := range map[flash.Extent]bool{
		{Addr: 6, Len: 2}: true, {Addr: 84, Len: 12}: true, {Addr: 185, Len: 2}: true, {Addr: 187, Len: 2}: true,
		{Addr: 160, Len: 7}: true, {Addr: 193, Len: 63}: true, {Addr: 0, Len: 2}: true,
		{Addr: 84, Len: 13}: false, {Addr: 8, Len: 2}: false, {Addr: 7, Len: 2}: false, {Addr: 160, Len: 6}: false,
		{Addr: 255, Len: 2}: false, {Addr: 6912, Len: 2}: false, {Addr: -1, Len: 2}: false, {Addr: 6, Len: 0}: false,
	} {
		if got := l.Capturable(e); got != want {
			t.Errorf("Capturable(%v) = %v, want %v", e, got, want)
		}
	}
}
