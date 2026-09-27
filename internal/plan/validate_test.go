package plan_test

import (
	"errors"
	"math"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

type fixture struct {
	p  plan.Plan
	im *flash.Image
	l  plan.Layout
}

var (
	ctrlTab   = h("04 80 01 00 81 2b 00 41 2b 00 40 01 00 77")
	playPause = h("02 82 cd 00 42 cd 00 f5")
	dpi800    = h("15 15 00 2b")
	dpi1600   = h("2a 2a 00 01")
)

func baseOps() []plan.Op {
	return numbered(
		op(plan.Neutralise, bindAddr(3), shortcut, disable),
		op(plan.Body, shortcutAddr(3), ctrlTab[:8], playPause),
		op(plan.Bind, bindAddr(3), disable, shortcut),
		op(plan.Record, 12, dpi800, dpi1600),
	)
}

func macroAB(t testing.TB) []byte { return loadVector(t, `Macro "ab"`) }

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, f *fixture)
		want error
	}{
		{"two-phase trio and a record", nil, nil},
		{"empty plan", func(_ *testing.T, f *fixture) { f.p.Ops = nil }, nil},
		{"empty plan for a keyboard", func(_ *testing.T, f *fixture) {
			f.p.Ops, f.p.Device = nil, plan.Identity{CID: 3, MID: 1}
		}, nil},
		{"nil image and no ops", func(_ *testing.T, f *fixture) { f.p.Ops, f.im = nil, nil }, nil},
		{"experimental tier", func(_ *testing.T, f *fixture) { f.p.Ops[3].Tier = catalog.Experimental }, nil},
		{"verified tier", func(_ *testing.T, f *fixture) { f.p.Ops[3].Tier = catalog.Verified }, nil},
		{"mid 6", func(_ *testing.T, f *fixture) { f.p.Device.MID = 6 }, nil},
		{"media body into an empty slot", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 8), playPause))
		}, nil},
		{"prefix write restores a longer record", func(t *testing.T, f *fixture) {
			set(t, f.im, shortcutAddr(3), playPause)
			f.p.Ops = numbered(
				op(plan.Neutralise, bindAddr(3), shortcut, disable),
				op(plan.Body, shortcutAddr(3), playPause, ctrlTab[:8]),
				op(plan.Bind, bindAddr(3), disable, shortcut),
			)
		}, nil},
		{"clear a body with zeros", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 32), fill(0, 32)))
		}, nil},
		{"erase a body", func(t *testing.T, f *fixture) {
			set(t, f.im, shortcutAddr(7), playPause)
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), playPause, fill(0xFF, 8)))
		}, nil},
		{"macro body then its binding", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			f.p.Ops = numbered(
				op(plan.Body, macroAddr(4), fill(0xFF, len(ab)), ab),
				op(plan.Bind, bindAddr(4), shortcut, macroBinding(4, 1)),
			)
		}, nil},
		{"disable one of two left clicks", func(t *testing.T, f *fixture) {
			set(t, f.im, bindAddr(1), leftClick)
			f.p.Ops = numbered(op(plan.Bind, bindAddr(0), leftClick, disable))
		}, nil},
		{"no left click to protect", func(t *testing.T, f *fixture) {
			set(t, f.im, bindAddr(0), right)
			f.p.Ops = numbered(op(plan.Bind, bindAddr(1), right, disable))
		}, nil},
		{"swap left and right, gain first", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(
				op(plan.Bind, bindAddr(1), right, leftClick),
				op(plan.Bind, bindAddr(0), leftClick, right),
			)
		}, nil},
		{"record with most bindings unread", func(t *testing.T, f *fixture) {
			im := flash.New()
			set(t, im, 0, get(t, f.im, 0, bindAddr(1)))
			f.im = im
			f.p.Ops = numbered(op(plan.Record, 12, dpi800, dpi1600))
		}, nil},
		{"record over an invalid pair", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 185, h("03 53"), h("00 55")))
		}, nil},

		{"bad seq", func(_ *testing.T, f *fixture) { f.p.Ops[1].Seq = 5 }, plan.ErrMalformed},
		{"zero seq", func(_ *testing.T, f *fixture) { f.p.Ops[0].Seq = 0 }, plan.ErrMalformed},
		{"phase zero", func(_ *testing.T, f *fixture) { f.p.Ops[3].Phase = 0 }, plan.ErrMalformed},
		{"phase out of range", func(_ *testing.T, f *fixture) { f.p.Ops[3].Phase = 9 }, plan.ErrMalformed},
		{"old too short", func(_ *testing.T, f *fixture) { f.p.Ops[3].Old = dpi800[:3] }, plan.ErrMalformed},
		{"new too long", func(_ *testing.T, f *fixture) { f.p.Ops[3].New = append(dpi1600, 0) }, plan.ErrMalformed},
		{"no-op", func(_ *testing.T, f *fixture) { f.p.Ops[3].New = dpi800 }, plan.ErrMalformed},
		{"layout with a bad binding stride", func(_ *testing.T, f *fixture) { f.l.Bindings.Stride = 3 }, plan.ErrMalformed},
		{"layout without buttons", func(_ *testing.T, f *fixture) { f.l.Buttons = nil }, plan.ErrMalformed},
		{"layout with a repeated button", func(_ *testing.T, f *fixture) { f.l.Buttons = []int{0, 1, 0} }, plan.ErrMalformed},
		{"layout with a button out of range", func(_ *testing.T, f *fixture) { f.l.Buttons = []int{0, 16} }, plan.ErrMalformed},
		{"layout with overlapping tables", func(_ *testing.T, f *fixture) { f.l.Shortcuts.Base = 150 }, plan.ErrMalformed},
		{"layout past the image", func(_ *testing.T, f *fixture) { f.l.Macros.Base = flash.Size - 384 }, plan.ErrMalformed},
		{"layout with a negative base", func(_ *testing.T, f *fixture) { f.l.Bindings.Base = -4 }, plan.ErrMalformed},
		{"layout with a huge table", func(_ *testing.T, f *fixture) { f.l.Macros.Count = math.MaxInt / 2 }, plan.ErrMalformed},
		{"layout with tiny macro slots", func(_ *testing.T, f *fixture) { f.l.Macros.Stride = 32 }, plan.ErrMalformed},
		{"layout with too few shortcut slots", func(_ *testing.T, f *fixture) { f.l.Shortcuts.Count = 8 }, plan.ErrMalformed},
		{"layout with a frozen extent outside the image", func(_ *testing.T, f *fixture) {
			f.l.Frozen = []flash.Extent{{Addr: flash.Size, Len: 2}}
		}, plan.ErrMalformed},
		{"layout checked even for an empty plan", func(_ *testing.T, f *fixture) { f.p.Ops, f.l.Buttons = nil, nil }, plan.ErrMalformed},
		{"layout without records", func(_ *testing.T, f *fixture) { f.l.Records = nil }, plan.ErrMalformed},
		{"layout without frozen extents", func(_ *testing.T, f *fixture) { f.l.Frozen = nil }, plan.ErrMalformed},
		{"layout with a frozen record", func(_ *testing.T, f *fixture) {
			f.l.Records = append(f.l.Records, flash.Extent{Addr: 8, Len: 2})
		}, plan.ErrMalformed},
		{"layout with a record in a table", func(_ *testing.T, f *fixture) {
			f.l.Records = append(f.l.Records, flash.Extent{Addr: bindAddr(3), Len: 4})
		}, plan.ErrMalformed},
		{"layout with a record outside the image", func(_ *testing.T, f *fixture) {
			f.l.Records = append(f.l.Records, flash.Extent{Addr: flash.Size, Len: 2})
		}, plan.ErrMalformed},
		{"layout with a one-byte record", func(_ *testing.T, f *fixture) {
			f.l.Records = append(f.l.Records, flash.Extent{Addr: 6, Len: 1})
		}, plan.ErrMalformed},
		{"layout with overlapping records", func(_ *testing.T, f *fixture) {
			f.l.Records = append(f.l.Records, flash.Extent{Addr: 13, Len: 2})
		}, plan.ErrMalformed},

		{"keyboard", func(_ *testing.T, f *fixture) { f.p.Device = plan.Identity{CID: 3, MID: 1} }, plan.ErrDevice},
		{"unknown model", func(_ *testing.T, f *fixture) { f.p.Device.MID = 9 }, plan.ErrDevice},

		{"negative address", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, -2, h("00 55"), h("01 54")))
		}, plan.ErrExtent},
		{"past the image", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, flash.Size-1, h("00 55"), h("01 54")))
		}, plan.ErrExtent},
		{"zero length", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(plan.Op{Extent: flash.Extent{Addr: 0}, Phase: plan.Record, Tier: catalog.Untested})
		}, plan.ErrExtent},
		{"overflowing length", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(plan.Op{Extent: flash.Extent{Addr: 10, Len: math.MaxInt}, Phase: plan.Record, Tier: catalog.Untested})
		}, plan.ErrExtent},
		{"misaligned binding", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, bindAddr(0)+2, h("00 53"), h("01 52")))
		}, plan.ErrExtent},
		{"half a binding", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, bindAddr(0), h("01 01"), h("01 02")))
		}, plan.ErrExtent},
		{"body away from its slot base", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7)+4, fill(0xFF, 8), playPause))
		}, plan.ErrExtent},
		{"body across two slots", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 40), fill(0, 40)))
		}, plan.ErrExtent},
		{"record running into the bindings", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 94, h("00 55 01 01"), h("01 54 01 01")))
		}, plan.ErrExtent},
		{"second half of a DPI record", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 42, h("00 83"), h("00 55")))
		}, plan.ErrExtent},
		{"write across two pairs", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 2, h("06 4f 03 52"), h("06 4f 03 fd")))
		}, plan.ErrExtent},
		{"unmapped block", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 84, fill(0, 12), append(fill(0, 11), 0x55)))
		}, plan.ErrExtent},
		{"unmapped pair", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 6, h("00 55"), h("01 54")))
		}, plan.ErrExtent},
		{"optional field", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 189, h("ff ff"), h("01 54")))
		}, plan.ErrExtent},
		{"one-byte record", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 12, h("15"), h("55")))
		}, plan.ErrExtent},

		{"record phase on a binding", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, bindAddr(7), h("02 01 00 52"), disable))
		}, plan.ErrPhase},
		{"bind phase on a record", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, 12, dpi800, dpi1600))
		}, plan.ErrPhase},
		{"body phase on a binding", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, bindAddr(7), h("02 01 00 52"), disable))
		}, plan.ErrPhase},
		{"bind phase on a body", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, shortcutAddr(7), fill(0xFF, 8), playPause))
		}, plan.ErrPhase},
		{"neutralise on a record", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Neutralise, 12, dpi800, disable))
		}, plan.ErrPhase},
		{"neutralise writing something else", func(_ *testing.T, f *fixture) { f.p.Ops[0].New = right }, plan.ErrPhase},
		{"neutralise without a body rewrite", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(
				op(plan.Neutralise, bindAddr(3), shortcut, disable),
				op(plan.Bind, bindAddr(3), disable, shortcut),
			)
		}, plan.ErrPhase},
		{"neutralise of a binding pointing elsewhere", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(
				op(plan.Neutralise, bindAddr(2), shortcut, disable),
				op(plan.Neutralise, bindAddr(3), shortcut, disable),
				op(plan.Body, shortcutAddr(3), ctrlTab[:8], playPause),
				op(plan.Bind, bindAddr(2), disable, shortcut),
				op(plan.Bind, bindAddr(3), disable, shortcut),
			)
		}, plan.ErrPhase},

		{"bind before its body", func(_ *testing.T, f *fixture) {
			ops := baseOps()
			ops[1], ops[2] = ops[2], ops[1]
			f.p.Ops = numbered(ops...)
		}, plan.ErrOrder},
		{"record first", func(_ *testing.T, f *fixture) {
			ops := baseOps()
			f.p.Ops = numbered(append(ops[3:], ops[:3]...)...)
		}, plan.ErrOrder},
		{"body before neutralise", func(_ *testing.T, f *fixture) {
			ops := baseOps()
			ops[0], ops[1] = ops[1], ops[0]
			f.p.Ops = numbered(ops...)
		}, plan.ErrOrder},

		{"same record twice", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 12, dpi800, dpi1600), op(plan.Record, 12, dpi1600, dpi800))
		}, plan.ErrOverlap},
		{"records overlapping in part", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 12, dpi800, dpi1600), op(plan.Record, 14, h("00 2b"), h("2a 2b")))
		}, plan.ErrExtent},
		{"binding bound twice", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, bindAddr(7), h("02 01 00 52"), disable), op(plan.Bind, bindAddr(7), disable, right))
		}, plan.ErrOverlap},
		{"binding neutralised twice", func(_ *testing.T, f *fixture) {
			ops := baseOps()
			f.p.Ops = numbered(ops[0], ops[0], ops[1], ops[2])
		}, plan.ErrOverlap},
		{"trio with a second bind", func(_ *testing.T, f *fixture) {
			ops := baseOps()
			f.p.Ops = numbered(ops[0], ops[1], ops[2], ops[2])
		}, plan.ErrOverlap},

		{"tier off", func(_ *testing.T, f *fixture) { f.p.Ops[3].Tier = catalog.Off }, plan.ErrTier},
		{"tier read-only", func(_ *testing.T, f *fixture) { f.p.Ops[0].Tier = catalog.ReadOnly }, plan.ErrTier},

		{"KeyOperation", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 8, h("00 55"), h("01 54")))
		}, plan.ErrFrozen},
		{"record spanning KeyOperation", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Record, 6, h("00 55 00 55"), h("01 54 00 55")))
		}, plan.ErrFrozen},

		{"record with a bad sum", func(_ *testing.T, f *fixture) { f.p.Ops[3].New = h("2a 2a 00 02") }, plan.ErrChecksum},
		{"binding with a bad sum", func(_ *testing.T, f *fixture) { f.p.Ops[2].New = h("05 00 00 51") }, plan.ErrChecksum},
		{"body with a bad sum", func(_ *testing.T, f *fixture) { f.p.Ops[1].New = h("02 82 cd 00 42 cd 00 f6") }, plan.ErrChecksum},
		{"body declaring more events than fit", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 8), cks(11, 0x82, 0xcd, 0, 0x42, 0xcd, 0)))
		}, plan.ErrChecksum},
		{"body shorter than its header says", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 8), cks(4, 0x82, 0xcd, 0, 0x42, 0xcd, 0)))
		}, plan.ErrChecksum},
		{"body with no events", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 2), h("00 55")))
		}, plan.ErrChecksum},
		{"macro with 71 events", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			ab[31] = 71
			f.p.Ops = numbered(op(plan.Body, macroAddr(4), fill(0xFF, len(ab)), ab))
		}, plan.ErrChecksum},
		{"macro with a bad sum", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			ab[len(ab)-1]++
			f.p.Ops = numbered(op(plan.Body, macroAddr(4), fill(0xFF, len(ab)), ab))
		}, plan.ErrChecksum},

		{"unread record", func(t *testing.T, f *fixture) {
			im := flash.New()
			set(t, im, bindBase, get(t, f.im, bindBase, 4*slots))
			f.im = im
			f.p.Ops = numbered(op(plan.Record, 12, dpi800, dpi1600))
		}, plan.ErrUnread},
		{"nil image", func(_ *testing.T, f *fixture) { f.im = nil }, plan.ErrUnread},
		{"body while a binding is unread", func(t *testing.T, f *fixture) {
			im := flash.New()
			set(t, im, 0, get(t, f.im, 0, bindAddr(15)))
			set(t, im, shortcutBase, get(t, f.im, shortcutBase, knownEnd-shortcutBase))
			f.im = im
		}, plan.ErrUnread},
		{"binding pointing at an unread body", func(t *testing.T, f *fixture) {
			im := flash.New()
			set(t, im, 0, get(t, f.im, 0, shortcutAddr(7)))
			f.im = im
			f.p.Ops = numbered(op(plan.Bind, bindAddr(7), h("02 01 00 52"), shortcut))
		}, plan.ErrUnread},
		{"body running into unread bytes", func(t *testing.T, f *fixture) {
			im := flash.New()
			set(t, im, 0, get(t, f.im, 0, shortcutAddr(7)+8))
			f.im = im
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(7), fill(0xFF, 8), cks(4, 0x82, 0xcd, 0, 0x42, 0xcd, 0)))
		}, plan.ErrUnread},

		{"stale record", func(_ *testing.T, f *fixture) { f.p.Ops[3].Old = h("2a 2a 00 00") }, plan.ErrStale},
		{"bind ignoring its neutralise", func(_ *testing.T, f *fixture) {
			f.p.Ops[2].Old, f.p.Ops[2].New = shortcut, right
		}, plan.ErrStale},

		{"body rewritten under its binding", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Body, shortcutAddr(3), ctrlTab[:8], playPause))
		}, plan.ErrBinding},
		{"macro rewritten under another slot's binding", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			set(t, f.im, bindAddr(7), macroBinding(3, 1))
			f.p.Ops = numbered(op(plan.Body, macroAddr(3), fill(0xFF, len(ab)), ab))
		}, plan.ErrBinding},
		{"macro rewritten under its own slot's binding", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			set(t, f.im, bindAddr(7), macroBinding(3, 1))
			f.p.Ops = numbered(op(plan.Body, macroAddr(7), fill(0xFF, len(ab)), ab))
		}, plan.ErrBinding},
		{"binding to an empty body", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, bindAddr(7), h("02 01 00 52"), shortcut))
		}, plan.ErrBinding},
		{"rebinding to a cleared body", func(_ *testing.T, f *fixture) { f.p.Ops[1].New = fill(0, 8) }, plan.ErrBinding},
		{"macro binding to a slot past the table", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			f.p.Ops = numbered(
				op(plan.Body, macroAddr(4), fill(0xFF, len(ab)), ab),
				op(plan.Bind, bindAddr(4), shortcut, macroBinding(0x20, 1)),
			)
		}, plan.ErrBinding},
		{"macro binding to another empty slot", func(t *testing.T, f *fixture) {
			ab := macroAB(t)
			f.p.Ops = numbered(
				op(plan.Body, macroAddr(4), fill(0xFF, len(ab)), ab),
				op(plan.Bind, bindAddr(4), shortcut, macroBinding(5, 1)),
			)
		}, plan.ErrBinding},

		{"last left click disabled", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(op(plan.Bind, bindAddr(0), leftClick, disable))
		}, plan.ErrLeftClick},
		{"swap left and right, loss first", func(_ *testing.T, f *fixture) {
			f.p.Ops = numbered(
				op(plan.Bind, bindAddr(0), leftClick, right),
				op(plan.Bind, bindAddr(1), right, leftClick),
			)
		}, plan.ErrLeftClick},
		{"left click on a slot without a button", func(t *testing.T, f *fixture) {
			set(t, f.im, bindAddr(12), leftClick)
			f.p.Ops = numbered(op(plan.Bind, bindAddr(0), leftClick, disable))
		}, plan.ErrLeftClick},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fixture{p: plan.Plan{Device: em11, Ops: baseOps()}, im: dumpImage(t), l: em11Layout()}
			if c.edit != nil {
				c.edit(t, f)
			}
			err := f.p.Validate(f.im, f.l)
			if c.want == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("Validate = %v, want %v", err, c.want)
			}
		})
	}
}

func TestValidateLeavesInputsAlone(t *testing.T) {
	im := dumpImage(t)
	before := im.Bytes()
	p := plan.Plan{Device: em11, Ops: baseOps()}
	if err := p.Validate(im, em11Layout()); err != nil {
		t.Fatal(err)
	}
	if string(im.Bytes()) != string(before) {
		t.Fatal("Validate changed the image")
	}
}

func TestErrorsNameTheOp(t *testing.T) {
	p := plan.Plan{Device: em11, Ops: baseOps()}
	p.Ops[3].Old = h("2a 2a 00 00")
	err := p.Validate(dumpImage(t), em11Layout())
	want := "plan: old bytes differ from the image: op 4 (record 12+4)"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestPhaseString(t *testing.T) {
	for p, want := range map[plan.Phase]string{
		plan.Neutralise: "neutralise", plan.Body: "body", plan.Bind: "bind", plan.Record: "record", plan.Captured: "captured",
		0: "Phase(0)", 6: "Phase(6)",
	} {
		if got := p.String(); got != want {
			t.Errorf("Phase(%d).String() = %q, want %q", p, got, want)
		}
	}
}
