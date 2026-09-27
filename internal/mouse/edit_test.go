package mouse_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
)

type opWant struct {
	phase    plan.Phase
	addr     int
	old, new []byte
	tier     catalog.Tier
}

func checkOps(t *testing.T, p plan.Plan, want []opWant) {
	t.Helper()
	if len(p.Ops) != len(want) {
		t.Fatalf("%d ops, want %d: %+v", len(p.Ops), len(want), p.Ops)
	}
	for i, w := range want {
		o := p.Ops[i]
		if o.Seq != i+1 || o.Phase != w.phase || o.Extent != (flash.Extent{Addr: w.addr, Len: len(w.new)}) ||
			!bytes.Equal(o.Old, w.old) || !bytes.Equal(o.New, w.new) || o.Tier != w.tier {
			t.Errorf("op %d = %+v\nwant %v %d % x -> % x %v", i, o, w.phase, w.addr, w.old, w.new, w.tier)
		}
	}
}

func planFor(t *testing.T, key string, im *flash.Image, opt mouse.Options, edits ...mouse.Edit) plan.Plan {
	t.Helper()
	p, err := mouse.PlanEdits(model(t, key), im, edits, opt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// allow returns the options of a device of model key running v1.05, with a hardware
// test recorded for each feature in fs.
func allow(key string, fs ...mouse.Feature) mouse.Options {
	m, ok := catalog.ByKey(key)
	if !ok {
		panic("model " + key + " not in the catalog")
	}
	opt := mouse.Options{Device: plan.Identity{CID: m.CID, MID: m.MIDs[0]}, Firmware: "v1.05"}
	for _, f := range fs {
		opt.Verified = append(opt.Verified, catalog.Verification{
			Model: key, Feature: string(f), Firmware: opt.Firmware, Stage: "H8", Date: "2026-09-26",
		})
	}
	return opt
}

func onFirmware(opt mouse.Options, firmware string) mouse.Options {
	opt.Firmware = firmware
	return opt
}

func dpiRecord(t testing.TB, dpi int) []byte {
	t.Helper()
	r, err := mouse.EncodeDPI(sensor(t, "3104"), dpi)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pairOf(v byte) []byte {
	p := flash.NewPair(v)
	return p[:]
}

func macroOf(name string, delay uint16) mouse.Macro {
	return mouse.Macro{Name: name, Events: []mouse.Event{
		{Press: true, Stroke: keyA, Delay: delay},
		{Stroke: keyA, Delay: 10},
	}}
}

func encodedMacro(t testing.TB, m mouse.Macro) []byte {
	t.Helper()
	b, err := mouse.EncodeMacro(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func macroBinding(t testing.TB, slot, cycle int) []byte {
	t.Helper()
	fn, err := mouse.MacroBinding(slot, cycle)
	if err != nil {
		t.Fatal(err)
	}
	return record(t, fn)
}

var (
	disableRecord  = []byte{0x00, 0x00, 0x00, 0x55}
	shortcutRecord = []byte{0x05, 0x00, 0x00, 0x50}
	cmdC           = keys.Combo{lMeta, keyC}
	menuKey        = keys.Stroke{Kind: keys.KindMenu, Value: 0x01}
)

func keyAddr(t testing.TB, k int) int      { return bodyAddr(t, mouse.KeyFnExtent, k) }
func shortcutAddr(t testing.TB, k int) int { return bodyAddr(t, mouse.ShortcutExtent, k) }
func macroAddr(t testing.TB, k int) int    { return bodyAddr(t, mouse.MacroExtent, k) }

func TestPlanDPIStage(t *testing.T) {
	im := maintainerImage(t)
	p := planFor(t, "7B04", im, allow("7B04"), mouse.SetDPI{Stage: 2, DPI: 1700})
	checkOps(t, p, []opWant{{plan.Record, 20, dump(t)[20:24], dpiRecord(t, 1700), catalog.Untested}})
	if p.Ops[0].Extent != (flash.Extent{Addr: 20, Len: 4}) || p.Ops[0].Desc != "DPI stage 3: 1700" {
		t.Errorf("op = %+v", p.Ops[0])
	}
	if p.Device != (plan.Identity{CID: 0x7B, MID: 4}) || p.Profile != nil {
		t.Errorf("device %+v profile %v", p.Device, p.Profile)
	}
	if p := planFor(t, "7B04", im, allow("7B04"), mouse.SetDPI{Stage: 2, DPI: 1600}); len(p.Ops) != 0 {
		t.Errorf("unchanged DPI planned %+v", p.Ops)
	}
}

func TestPlanBodyBeforeBinding(t *testing.T) {
	im := maintainerImage(t)
	back := record(t, mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack})
	put(t, im, keyAddr(t, 3), back)
	put(t, im, shortcutAddr(t, 3), fill(0xFF, mouse.ShortcutSize))
	p := planFor(t, "7B04", im, allow("7B04"), mouse.SetMedia{Slot: 3, Usage: 0x00CD})
	checkOps(t, p, []opWant{
		{plan.Body, shortcutAddr(t, 3), fill(0xFF, 8), vector(t, "Media Play/Pause"), catalog.Untested},
		{plan.Bind, keyAddr(t, 3), back, shortcutRecord, catalog.Untested},
	})
	if p.Ops[0].Desc != "media 3: Play/Pause" || p.Ops[1].Desc != "slot 3: shortcut" {
		t.Errorf("descs %q, %q", p.Ops[0].Desc, p.Ops[1].Desc)
	}
}

func TestPlanTwoPhase(t *testing.T) {
	ab, ab2 := macroOf("ab", 50), macroOf("ab", 60)
	boundMacro := func(t *testing.T, im *flash.Image) {
		put(t, im, keyAddr(t, 4), macroBinding(t, 4, 1))
		put(t, im, macroAddr(t, 4), encodedMacro(t, ab))
	}
	ctrlTab := vector(t, "Ctrl+Tab shortcut")
	cases := []struct {
		name  string
		setup func(*testing.T, *flash.Image)
		edit  mouse.Edit
		want  func(t *testing.T) []opWant
	}{
		{"media over a bound shortcut", nil, mouse.SetMedia{Slot: 3, Usage: 0x00CD}, func(t *testing.T) []opWant {
			return []opWant{
				{plan.Neutralise, keyAddr(t, 3), shortcutRecord, disableRecord, catalog.Untested},
				{plan.Body, shortcutAddr(t, 3), ctrlTab[:8], vector(t, "Media Play/Pause"), catalog.Untested},
				{plan.Bind, keyAddr(t, 3), disableRecord, shortcutRecord, catalog.Untested},
			}
		}},
		{"shortcut over a bound shortcut", nil, mouse.SetShortcut{Slot: 3, Combo: cmdC}, func(t *testing.T) []opWant {
			return []opWant{
				{plan.Neutralise, keyAddr(t, 3), shortcutRecord, disableRecord, catalog.Untested},
				{plan.Body, shortcutAddr(t, 3), ctrlTab, vector(t, "Cmd+C shortcut"), catalog.Untested},
				{plan.Bind, keyAddr(t, 3), disableRecord, shortcutRecord, catalog.Untested},
			}
		}},
		{"macro over a bound macro", boundMacro, mouse.SetMacro{Slot: 4, Macro: ab2, Cycle: 1}, func(t *testing.T) []opWant {
			return []opWant{
				{plan.Neutralise, keyAddr(t, 4), macroBinding(t, 4, 1), disableRecord, catalog.Untested},
				{plan.Body, macroAddr(t, 4), encodedMacro(t, ab), encodedMacro(t, ab2), catalog.Untested},
				{plan.Bind, keyAddr(t, 4), disableRecord, macroBinding(t, 4, 1), catalog.Untested},
			}
		}},
		{"macro onto a shortcut button", nil, mouse.SetMacro{Slot: 2, Macro: ab, Cycle: 1}, func(t *testing.T) []opWant {
			return []opWant{
				{plan.Body, macroAddr(t, 2), fill(0xFF, len(encodedMacro(t, ab))), encodedMacro(t, ab), catalog.Untested},
				{plan.Bind, keyAddr(t, 2), shortcutRecord, macroBinding(t, 2, 1), catalog.Untested},
			}
		}},
		{"new cycle only", boundMacro, mouse.SetMacro{Slot: 4, Macro: ab, Cycle: 254}, func(t *testing.T) []opWant {
			return []opWant{{plan.Bind, keyAddr(t, 4), macroBinding(t, 4, 1), macroBinding(t, 4, 254), catalog.Untested}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := maintainerImage(t)
			if tc.setup != nil {
				tc.setup(t, im)
			}
			p := planFor(t, "7B04", im, allow("7B04"), tc.edit)
			checkOps(t, p, tc.want(t))
		})
	}
}

func TestPlanTwoPhaseDescs(t *testing.T) {
	p := planFor(t, "7B04", maintainerImage(t), allow("7B04"), mouse.SetMedia{Slot: 3, Usage: 0x00CD})
	var got []string
	for _, o := range p.Ops {
		got = append(got, o.Desc)
	}
	want := []string{"disable binding 3 while its body is rewritten", "media 3: Play/Pause", "restore binding 3"}
	if !slices.Equal(got, want) {
		t.Errorf("descs %q, want %q", got, want)
	}
}

func TestPlanStages(t *testing.T) {
	type rec struct {
		addr     int
		old, new []byte
	}
	stageTwoInvalid := func(t *testing.T, im *flash.Image) {
		put(t, im, mouse.AddrMaxDpiStage, pairOf(2))
		put(t, im, mouse.AddrCurrentDPI, pairOf(1))
		put(t, im, 20, []byte{0x2a, 0x2a, 0x00, 0x02})
	}
	cases := []struct {
		name  string
		setup func(*testing.T, *flash.Image)
		edits []mouse.Edit
		want  []rec
		err   error
	}{
		{"count only", nil, []mouse.Edit{mouse.SetStages{Count: 4}}, []rec{{2, pairOf(6), pairOf(4)}}, nil},
		{"count below current", nil, []mouse.Edit{mouse.SetStages{Count: 3}}, nil, mouse.ErrValue},
		{"count below old current", nil, []mouse.Edit{mouse.SetStages{Count: 3}, mouse.SetCurrent{Stage: 1}},
			[]rec{{4, pairOf(3), pairOf(1)}, {2, pairOf(6), pairOf(3)}}, nil},
		{"count above old current", nil, []mouse.Edit{mouse.SetCurrent{Stage: 4}, mouse.SetStages{Count: 5}},
			[]rec{{2, pairOf(6), pairOf(5)}, {4, pairOf(3), pairOf(4)}}, nil},
		{"current only", nil, []mouse.Edit{mouse.SetCurrent{Stage: 5}}, []rec{{4, pairOf(3), pairOf(5)}}, nil},
		{"current past the model", nil, []mouse.Edit{mouse.SetCurrent{Stage: 6}}, nil, mouse.ErrValue},
		{"DPI before current", nil, []mouse.Edit{mouse.SetCurrent{Stage: 0}, mouse.SetDPI{Stage: 0, DPI: 900}},
			[]rec{{12, dump(t)[12:16], dpiRecord(t, 900)}, {4, pairOf(3), pairOf(0)}}, nil},
		{"count past the model", nil, []mouse.Edit{mouse.SetStages{Count: 7}}, nil, mouse.ErrValue},
		{"count twice", nil, []mouse.Edit{mouse.SetStages{Count: 2}, mouse.SetStages{Count: 3}}, nil, mouse.ErrValue},
		{"current twice", nil, []mouse.Edit{mouse.SetCurrent{Stage: 2}, mouse.SetCurrent{Stage: 3}}, nil, mouse.ErrValue},
		{"activates an invalid stage", stageTwoInvalid, []mouse.Edit{mouse.SetStages{Count: 4}}, nil, mouse.ErrInvalid},
		{"activates a fixed stage", stageTwoInvalid, []mouse.Edit{mouse.SetStages{Count: 4}, mouse.SetDPI{Stage: 2, DPI: 1000}},
			[]rec{{20, []byte{0x2a, 0x2a, 0x00, 0x02}, dpiRecord(t, 1000)}, {2, pairOf(2), pairOf(4)}}, nil},
		{"current never read", func(t *testing.T, im *flash.Image) {
			*im = flash.Image{}
			put(t, im, 0, dump(t)[:4])
		}, []mouse.Edit{mouse.SetStages{Count: 4}}, nil, plan.ErrUnread},
		{"stage DPI never read", func(t *testing.T, im *flash.Image) {
			*im = flash.Image{}
			put(t, im, 0, dump(t)[:12])
		}, []mouse.Edit{mouse.SetCurrent{Stage: 0}}, []rec{{4, pairOf(3), pairOf(0)}}, nil},
		{"current invalid", func(t *testing.T, im *flash.Image) {
			put(t, im, mouse.AddrCurrentDPI, pairOf(9))
		}, []mouse.Edit{mouse.SetStages{Count: 4}}, nil, mouse.ErrInvalid},
		{"current invalid and fixed", func(t *testing.T, im *flash.Image) {
			put(t, im, mouse.AddrCurrentDPI, pairOf(9))
		}, []mouse.Edit{mouse.SetStages{Count: 4}, mouse.SetCurrent{Stage: 0}},
			[]rec{{4, pairOf(9), pairOf(0)}, {2, pairOf(6), pairOf(4)}}, nil},
		{"current pair broken, count raised past it", func(t *testing.T, im *flash.Image) {
			put(t, im, mouse.AddrMaxDpiStage, pairOf(3))
			put(t, im, mouse.AddrCurrentDPI, []byte{0x03, 0x53})
		}, []mouse.Edit{mouse.SetStages{Count: 6}, mouse.SetCurrent{Stage: 4}},
			[]rec{{2, pairOf(3), pairOf(6)}, {4, []byte{0x03, 0x53}, pairOf(4)}}, nil},
		{"count pair broken, current past the new count", func(t *testing.T, im *flash.Image) {
			put(t, im, mouse.AddrMaxDpiStage, []byte{0x06, 0x50})
		}, []mouse.Edit{mouse.SetStages{Count: 2}, mouse.SetCurrent{Stage: 1}},
			[]rec{{4, pairOf(3), pairOf(1)}, {2, []byte{0x06, 0x50}, pairOf(2)}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := maintainerImage(t)
			if tc.setup != nil {
				tc.setup(t, im)
			}
			p, err := mouse.PlanEdits(model(t, "7B04"), im, tc.edits, allow("7B04"))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := make([]opWant, len(tc.want))
			for i, r := range tc.want {
				want[i] = opWant{plan.Record, r.addr, r.old, r.new, catalog.Untested}
			}
			checkOps(t, p, want)
		})
	}
}

func TestPlanRefusals(t *testing.T) {
	ab := macroOf("ab", 50)
	unboundAB := func(t *testing.T, im *flash.Image) { put(t, im, macroAddr(t, 9), encodedMacro(t, ab)) }
	cases := []struct {
		name  string
		key   string
		setup func(*testing.T, *flash.Image)
		opt   mouse.Options
		edits []mouse.Edit
		err   error
	}{
		{"keyboard", "0301", nil, allow("0301"), []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 800}}, mouse.ErrRefused},
		{"KeyOperation", "7B04", nil, allow("7B04", "setting.key-operation"),
			[]mouse.Edit{mouse.SetSetting{Addr: mouse.AddrKeyOperation, Value: 1}}, mouse.ErrRefused},
		{"hidden setting", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrSleepTime, Value: 30}}, mouse.ErrRefused},
		{"optional setting", "7B04", nil, allow("7B04", "setting.angle-tune"),
			[]mouse.Edit{mouse.SetSetting{Addr: mouse.AddrAngleTune, Value: 1}}, mouse.ErrRefused},
		{"setting record", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrLight, Value: 1}}, mouse.ErrValue},
		{"unmapped setting", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetSetting{Addr: 6, Value: 1}}, mouse.ErrValue},
		{"setting unset", "7B04", nil, allow("7B04", "setting.sensor-mode"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrSensorMode, Value: 0xFF}}, mouse.ErrValue},
		{"setting off its list", "7B04", nil, allow("7B04", "setting.sleep"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrSleepTime, Value: 0}}, mouse.ErrValue},
		{"switch past 1", "7B04", nil, allow("7B04", "setting.motion-sync"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrMotionSync, Value: 2}}, mouse.ErrValue},
		{"debounce past the model", "7B04", nil, allow("7B04", "setting.debounce"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrDebounceTime, Value: 21}}, mouse.ErrValue},
		{"brightness off the table", "7B04", nil, allow("7B04", "setting.dpi-light-brightness"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrDPIEffectBrightness, Value: 17}}, mouse.ErrValue},
		{"DPI light speed 0", "7B04", nil, allow("7B04", "setting.dpi-light-speed"), []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrDPIEffectSpeed, Value: 0}}, mouse.ErrValue},
		{"hidden slot", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 7, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}}}, mouse.ErrRefused},
		{"invisible cfg button", "7B05", nil, allow("7B05"), []mouse.Edit{mouse.SetKey{Slot: 14, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}}}, mouse.ErrRefused},
		{"hidden slot shortcut", "7B04", nil, allow("7B04", mouse.FeatureShortcut), []mouse.Edit{mouse.SetShortcut{Slot: 12, Combo: cmdC}}, mouse.ErrRefused},
		{"fire key", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeFire, Param: 0x1401}}}, mouse.ErrRefused},
		{"rate switch on an office mouse", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeRateSwitch}}}, mouse.ErrRefused},
		{"drag scroll off the trackball", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}}}, mouse.ErrRefused},
		{"slot out of range", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 16}}, mouse.ErrValue},
		{"body slot out of range", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetShortcut{Slot: 16, Combo: cmdC}}, mouse.ErrValue},
		{"no stages", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetStages{Count: 0}}, mouse.ErrValue},
		{"negative current", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetCurrent{Stage: -1}}, mouse.ErrValue},
		{"bad param", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: 0x0300}}}, mouse.ErrValue},
		{"DPI lock off the table", "7B04", nil, allow("7B04", mouse.FeatureDPILock), []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDPILock, Param: 0x3FF}}}, mouse.ErrValue},
		{"last Left Click", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 0, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamRight}}}, plan.ErrLeftClick},
		{"DPI stage past the model", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetDPI{Stage: 6, DPI: 800}}, mouse.ErrValue},
		{"DPI above the model", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 7400}}, mouse.ErrValue},
		{"DPI below the sensor", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 100}}, mouse.ErrValue},
		{"DPI off the step", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 4100}}, mouse.ErrValue},
		{"joined modifier stroke", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{(keys.LCtrl | keys.LShift).Stroke(), keyA}}}, mouse.ErrValue},
		{"repeated modifier", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, lCtrl, keyA}}}, mouse.ErrValue},
		{"repeated key", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: slices.Repeat(keys.Combo{keyA}, 5)}}, mouse.ErrValue},
		{"shortcut too long", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: slices.Repeat(cmdC, 3)}}, mouse.ErrValue},
		{"media zero", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetMedia{Slot: 3}}, mouse.ErrValue},
		{"macro delay 9", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: macroOf("ab", 9), Cycle: 1}}, mouse.ErrValue},
		{"macro delay 0", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: macroOf("ab", 0), Cycle: 1}}, mouse.ErrValue},
		{"macro delay 10", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: macroOf("ab", 10), Cycle: 1}}, nil},
		{"macro cycle", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: ab, Cycle: 251}}, mouse.ErrValue},
		{"macro name", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: mouse.Macro{Name: ""}, Cycle: 1}}, mouse.ErrValue},
		{"macro name taken", "7B04", unboundAB, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: macroOf("ab", 60), Cycle: 1}}, mouse.ErrValue},
		{"same macro on two slots", "7B04", unboundAB, allow("7B04"), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: ab, Cycle: 1}}, nil},
		{"macro name taken in the plan", "7B04", nil, allow("7B04"), []mouse.Edit{
			mouse.SetMacro{Slot: 4, Macro: ab, Cycle: 1}, mouse.SetMacro{Slot: 3, Macro: macroOf("ab", 60), Cycle: 1}}, mouse.ErrValue},
		{"same slot twice", "7B04", nil, allow("7B04"), []mouse.Edit{
			mouse.SetMedia{Slot: 3, Usage: 0xCD}, mouse.SetKey{Slot: 3, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}}}, plan.ErrOverlap},
		{"nil edit", "7B04", nil, allow("7B04"), []mouse.Edit{nil}, mouse.ErrValue},
		{"other device", "7B04", nil, mouse.Options{Device: plan.Identity{CID: 0x7B, MID: 1}}, []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 900}}, plan.ErrDevice},
		{"unknown device", "7B04", nil, mouse.Options{Device: plan.Identity{CID: 0x7B, MID: 9}}, nil, plan.ErrDevice},
		{"no device", "7B04", nil, mouse.Options{Firmware: "v1.05"}, []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 900}}, plan.ErrDevice},
		{"no device, no edits", "7B04", nil, mouse.Options{}, nil, plan.ErrDevice},
		{"body never read", "7B04", func(t *testing.T, im *flash.Image) {
			*im = flash.Image{}
			put(t, im, 0, dump(t))
		}, allow("7B04"), []mouse.Edit{mouse.SetMedia{Slot: 2, Usage: 0xCD}}, plan.ErrUnread},
		{"binding points at an invalid body", "7B04", nil, allow("7B04"), []mouse.Edit{mouse.SetKey{Slot: 2, Fn: mouse.KeyFn{Type: mouse.TypeMacro, Param: 0x0201}}}, plan.ErrBinding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := maintainerImage(t)
			if tc.setup != nil {
				tc.setup(t, im)
			}
			p, err := mouse.PlanEdits(model(t, tc.key), im, tc.edits, tc.opt)
			if tc.err == nil {
				if err != nil || len(p.Ops) == 0 {
					t.Fatalf("err = %v, ops %+v", err, p.Ops)
				}
				return
			}
			if !errors.Is(err, tc.err) || len(p.Ops) != 0 {
				t.Fatalf("err = %v, ops %d; want %v", err, len(p.Ops), tc.err)
			}
		})
	}
	if _, err := mouse.PlanEdits(nil, maintainerImage(t), nil, mouse.Options{}); !errors.Is(err, mouse.ErrRefused) {
		t.Errorf("nil model: %v", err)
	}
	if p, err := mouse.PlanEdits(model(t, "7B04"), nil, nil, allow("7B04")); err != nil || len(p.Ops) != 0 {
		t.Errorf("nil image, no edits: %+v %v", p, err)
	}
	if _, err := mouse.PlanEdits(model(t, "7B04"), nil, []mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 900}}, allow("7B04")); !errors.Is(err, plan.ErrUnread) {
		t.Errorf("nil image: %v", err)
	}
}

func TestPlanErrorNamesTheEdit(t *testing.T) {
	_, err := mouse.PlanEdits(model(t, "7B04"), maintainerImage(t),
		[]mouse.Edit{mouse.SetDPI{Stage: 0, DPI: 900}, mouse.SetDPI{Stage: 1, DPI: 7400}}, allow("7B04"))
	if err == nil || !strings.HasPrefix(err.Error(), "edit 1: ") {
		t.Errorf("err = %v", err)
	}
}

func TestPlanTiers(t *testing.T) {
	im := maintainerImage(t)
	cases := []struct {
		name string
		key  string
		opt  mouse.Options
		edit mouse.Edit
		want []opWant
	}{
		{"setting allowed", "7B04", allow("7B04", "setting.sleep"), mouse.SetSetting{Addr: mouse.AddrSleepTime, Value: 30},
			[]opWant{{plan.Record, mouse.AddrSleepTime, pairOf(1), pairOf(30), catalog.Experimental}}},
		{"debounce at the model limit", "7B04", allow("7B04", "setting.debounce"), mouse.SetSetting{Addr: mouse.AddrDebounceTime, Value: 20},
			[]opWant{{plan.Record, mouse.AddrDebounceTime, pairOf(2), pairOf(20), catalog.Experimental}}},
		{"brightness level 10", "7B04", allow("7B04", "setting.dpi-light-brightness"), mouse.SetSetting{Addr: mouse.AddrDPIEffectBrightness, Value: 0xFF},
			[]opWant{{plan.Record, mouse.AddrDPIEffectBrightness, pairOf(0x80), pairOf(0xFF), catalog.Experimental}}},
		{"hidden slot allowed", "7B04", allow("7B04", mouse.FeatureHiddenSlot), mouse.SetKey{Slot: 7, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}},
			[]opWant{{plan.Bind, keyAddr(t, 7), dump(t)[124:128], record(t, mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}), catalog.Experimental}}},
		{"verified", "7B04", allow("7B04", mouse.FeatureDPI), mouse.SetDPI{Stage: 0, DPI: 900},
			[]opWant{{plan.Record, 12, dump(t)[12:16], dpiRecord(t, 900), catalog.Verified}}},
		{"verified on other firmware", "7B04", onFirmware(allow("7B04", mouse.FeatureDPI), "v1.06"),
			mouse.SetDPI{Stage: 0, DPI: 900}, []opWant{{plan.Record, 12, dump(t)[12:16], dpiRecord(t, 900), catalog.Untested}}},
		{"right modifier stays untested", "7B04", allow("7B04", mouse.FeatureShortcut), mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keys.RCtrl.Stroke(), keyC}},
			nil},
		{"menu key stays untested", "7B04", allow("7B04", mouse.FeatureShortcut, mouse.FeatureShortcutRightModifier),
			mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, menuKey}}, nil},
		{"rate switch on a gaming mouse", "7B01", allow("7B01"), mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeRateSwitch}},
			[]opWant{{plan.Bind, keyAddr(t, 5), shortcutRecord, vector(t, "Rate Switch"), catalog.Untested}}},
		{"drag scroll on the trackball", "7B02", allow("7B02"), mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}},
			[]opWant{{plan.Bind, keyAddr(t, 5), shortcutRecord, vector(t, "Drag Scroll"), catalog.Untested}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planFor(t, tc.key, im, tc.opt, tc.edit)
			if tc.want == nil {
				for _, o := range p.Ops {
					if o.Tier != catalog.Untested {
						t.Errorf("op %+v, want untested", o)
					}
				}
				return
			}
			checkOps(t, p, tc.want)
		})
	}
	both := allow("7B04", mouse.FeatureShortcut, mouse.FeatureShortcutRightModifier)
	for _, o := range planFor(t, "7B04", im, both, mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keys.RCtrl.Stroke(), keyC}}).Ops {
		if o.Tier != catalog.Verified {
			t.Errorf("op %+v, want verified", o)
		}
	}
}

func TestPlanTiersFollowWebShapes(t *testing.T) {
	volumeUp := keys.Stroke{Kind: keys.KindConsumer, Value: 0xE9}
	web := []mouse.Feature{mouse.FeatureShortcut, mouse.FeatureMedia, mouse.FeatureMacro}
	foreign, err := mouse.MacroBinding(4, 1)
	if err != nil {
		t.Fatal(err)
	}
	im := maintainerImage(t)
	put(t, im, macroAddr(t, 2), encodedMacro(t, macroOf("ab", 50)))
	put(t, im, macroAddr(t, 4), encodedMacro(t, macroOf("ab", 50)))
	cases := []struct {
		name  string
		edit  mouse.Edit
		extra mouse.Feature
		tier  catalog.Tier
	}{
		{"web combo", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, keyA}}, "", catalog.Verified},
		{"web media", mouse.SetMedia{Slot: 3, Usage: 0xCD}, "", catalog.Verified},
		{"web media as a shortcut", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{volumeUp}}, "", catalog.Verified},
		{"own macro", mouse.SetKey{Slot: 4, Fn: foreign}, "", catalog.Verified},
		{"right modifier", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keys.RCtrl.Stroke(), keyA}}, mouse.FeatureShortcutRightModifier, catalog.Verified},
		{"menu key", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, menuKey}}, mouse.FeatureShortcutMenu, catalog.Verified},
		{"two plain keys", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keyA, keyC}}, mouse.FeatureShortcutCustom, catalog.Experimental},
		{"modifier only", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl}}, mouse.FeatureShortcutCustom, catalog.Experimental},
		{"key before modifier", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keyA, lCtrl}}, mouse.FeatureShortcutCustom, catalog.Experimental},
		{"consumer in a combo", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, volumeUp}}, mouse.FeatureShortcutCustom, catalog.Experimental},
		{"media off the web list", mouse.SetMedia{Slot: 3, Usage: 0x0FFF}, mouse.FeatureMediaCustom, catalog.Experimental},
		{"media off the web list as a shortcut", mouse.SetShortcut{Slot: 3, Combo: keys.Combo{{Kind: keys.KindConsumer, Value: 0x0FFF}}}, mouse.FeatureMediaCustom, catalog.Experimental},
		{"foreign macro", mouse.SetKey{Slot: 2, Fn: foreign}, mouse.FeatureMacroForeign, catalog.Experimental},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model(t, "7B04")
			p, err := mouse.PlanEdits(m, im, []mouse.Edit{tc.edit}, allow("7B04", web...))
			switch {
			case tc.extra == "" || tc.tier == catalog.Verified:
				want := catalog.Verified
				if tc.extra != "" {
					want = catalog.Untested
				}
				if err != nil || len(p.Ops) == 0 {
					t.Fatalf("web features only: err %v, ops %+v", err, p.Ops)
				}
				for _, o := range p.Ops {
					if o.Tier != want {
						t.Errorf("web features only: op %+v, want %v", o, want)
					}
				}
			case !errors.Is(err, mouse.ErrRefused):
				t.Fatalf("web features only: err %v, want a refusal", err)
			}
			if tc.extra == "" {
				return
			}
			p = planFor(t, "7B04", im, allow("7B04", append(web, tc.extra)...), tc.edit)
			for _, o := range p.Ops {
				if o.Tier != tc.tier {
					t.Errorf("with %s: op %+v, want %v", tc.extra, o, tc.tier)
				}
			}
		})
	}
}

func TestFeatureTier(t *testing.T) {
	covered := func(f mouse.Feature) mouse.Options { return allow("7B04", f) }
	cases := []struct {
		key      string
		feature  mouse.Feature
		opt      mouse.Options
		tier     catalog.Tier
		writable bool
	}{
		{"7B04", mouse.FeatureDPI, mouse.Options{}, catalog.Untested, true},
		{"7B04", mouse.FeatureDPI, covered(mouse.FeatureDPI), catalog.Verified, true},
		{"7B04", mouse.FeatureShortcutRightModifier, mouse.Options{}, catalog.Untested, true},
		{"7B04", mouse.FeatureShortcutMenu, mouse.Options{}, catalog.Untested, true},
		{"7B04", mouse.FeatureShortcutCustom, mouse.Options{}, catalog.Experimental, false},
		{"7B04", mouse.FeatureMediaCustom, covered(mouse.FeatureMediaCustom), catalog.Experimental, true},
		{"7B04", mouse.FeatureMacroForeign, mouse.Options{}, catalog.Experimental, false},
		{"7B04", mouse.FeatureFireKey, mouse.Options{}, catalog.Experimental, false},
		{"7B04", mouse.FeatureFireKey, covered(mouse.FeatureFireKey), catalog.Experimental, true},
		{"7B04", mouse.FeatureRateSwitch, mouse.Options{}, catalog.Experimental, false},
		{"7B01", mouse.FeatureRateSwitch, mouse.Options{}, catalog.Untested, true},
		{"7B02", mouse.FeatureDragScroll, mouse.Options{}, catalog.Untested, true},
		{"7B04", mouse.FeatureDragScroll, mouse.Options{}, catalog.Experimental, false},
		{"7B04", mouse.FeatureHiddenSlot, mouse.Options{}, catalog.Experimental, false},
		{"7B04", "setting.sleep", mouse.Options{}, catalog.Experimental, false},
		{"7B04", "setting.key-operation", covered("setting.key-operation"), catalog.ReadOnly, false},
		{"7B04", "setting.angle-tune", covered("setting.angle-tune"), catalog.Off, false},
		{"7B04", "no.such-feature", covered("no.such-feature"), catalog.Off, false},
		{"0301", mouse.FeatureDPI, mouse.Options{}, catalog.ReadOnly, false},
		{"7B04", mouse.FeatureRestore, mouse.Options{}, catalog.Untested, true},
		{"7B04", mouse.FeatureRestore, covered(mouse.FeatureRestore), catalog.Verified, true},
		{"7B04", mouse.FeatureReset, mouse.Options{}, catalog.Off, false},
		{"7B04", mouse.FeatureReset, covered(mouse.FeatureReset), catalog.Verified, false},
		{"0301", mouse.FeatureReset, covered(mouse.FeatureReset), catalog.ReadOnly, false},
	}
	for _, tc := range cases {
		tier, ok := tc.feature.Tier(model(t, tc.key), tc.opt)
		if tier != tc.tier || ok != tc.writable {
			t.Errorf("%s on %s = %v %v, want %v %v", tc.feature, tc.key, tier, ok, tc.tier, tc.writable)
		}
	}
	if tier, ok := mouse.FeatureDPI.Tier(nil, mouse.Options{}); tier != catalog.Off || ok {
		t.Errorf("nil model = %v %v", tier, ok)
	}
}

func TestLayout(t *testing.T) {
	l := mouse.Layout(model(t, "7B04"))
	want := plan.Layout{
		Bindings:  plan.Table{Base: 96, Stride: 4, Count: 16},
		Shortcuts: plan.Table{Base: 256, Stride: 32, Count: 16},
		Macros:    plan.Table{Base: 768, Stride: 384, Count: 16},
		Buttons:   []int{0, 1, 2, 4, 3, 5},
		Frozen:    []flash.Extent{{Addr: 8, Len: 2}},
	}
	if l.Bindings != want.Bindings || l.Shortcuts != want.Shortcuts || l.Macros != want.Macros ||
		!slices.Equal(l.Buttons, want.Buttons) || !slices.Equal(l.Frozen, want.Frozen) {
		t.Errorf("layout = %+v, want %+v", l, want)
	}
	var records []flash.Extent
	for _, a := range []int{0, 2, 4, 10, 76, 78, 80, 82, 169, 171, 173, 175, 177, 179, 181, 183, 185} {
		records = append(records, flash.Extent{Addr: a, Len: 2})
	}
	for i := range 16 {
		records = append(records, flash.Extent{Addr: 12 + 4*i, Len: 4})
	}
	records = append(records, flash.Extent{Addr: 160, Len: 7})
	got := slices.SortedFunc(slices.Values(l.Records), func(a, b flash.Extent) int { return a.Addr - b.Addr })
	slices.SortFunc(records, func(a, b flash.Extent) int { return a.Addr - b.Addr })
	if !slices.Equal(got, records) {
		t.Errorf("records = %v, want %v", got, records)
	}
	buttons := map[string][]int{
		"7B05": {0, 1, 2, 4, 3, 5},
		"7B06": {0, 1, 2, 4, 3, 5, 12, 13},
		"7B02": {0, 1, 2, 4, 3, 5, 11, 8},
	}
	for key, want := range buttons {
		if got := mouse.Layout(model(t, key)).Buttons; !slices.Equal(got, want) {
			t.Errorf("%s buttons = %v, want %v", key, got, want)
		}
	}
}

func TestPlanOptions(t *testing.T) {
	dev := plan.Identity{CID: 0x7B, MID: 4, Addr: [3]byte{1, 2, 3}, AddrTrusted: true, VID: 0x260D, PID: 0x1282}
	profile := byte(1)
	p := planFor(t, "7B04", maintainerImage(t), mouse.Options{Device: dev, Profile: &profile}, mouse.SetDPI{Stage: 0, DPI: 900})
	if p.Device != dev || p.Profile == nil || *p.Profile != 1 || p.Profile == &profile {
		t.Errorf("device %+v profile %v", p.Device, p.Profile)
	}
}

func TestPlanWriteVectors(t *testing.T) {
	hexOf := map[string]string{}
	for _, v := range loadVectors(t) {
		hexOf[v.Name] = v.Hex
	}
	cfgDefaultSlot5 := func(t *testing.T, im *flash.Image) {
		put(t, im, keyAddr(t, 5), record(t, mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}))
		put(t, im, shortcutAddr(t, 5), fill(0xFF, mouse.ShortcutSize))
	}
	cases := []struct {
		name    string
		setup   func(*testing.T, *flash.Image)
		edit    mouse.Edit
		op      int
		vectors []string
	}{
		{"Cmd+C on slot 5", cfgDefaultSlot5, mouse.SetShortcut{Slot: 5, Combo: cmdC}, 0, []string{"btn5 Cmd+C write/1", "btn5 Cmd+C write/2"}},
		{"DPI cycle on slot 5", nil, mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}}, 0, []string{"btn5 DPI cycle"}},
		{"800 DPI on stage 1", func(t *testing.T, im *flash.Image) { put(t, im, 12, dpiRecord(t, 900)) },
			mouse.SetDPI{Stage: 0, DPI: 800}, 0, []string{"DPI 800 at stage 0"}},
		{"4800 DPI on stage 6", nil, mouse.SetDPI{Stage: 5, DPI: 4800}, 0, []string{"DPI 4800 at stage 5"}},
		{"six stages", func(t *testing.T, im *flash.Image) { put(t, im, mouse.AddrMaxDpiStage, pairOf(4)) },
			mouse.SetStages{Count: 6}, 0, []string{"ND(6)"}},
		{"current stage 3", nil, mouse.SetCurrent{Stage: 2}, 0, []string{"FD(2)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := maintainerImage(t)
			if tc.setup != nil {
				tc.setup(t, im)
			}
			o := planFor(t, "7B04", im, allow("7B04"), tc.edit).Ops[tc.op]
			var want []string
			for _, name := range tc.vectors {
				want = append(want, hexOf[name])
			}
			if got := packets(t, o.Extent.Addr, o.New); !slices.Equal(got, want) {
				t.Errorf("packets %q, want %q", got, want)
			}
		})
	}
	records := []struct {
		edit   mouse.Edit
		vector []string
	}{
		{mouse.SetMacro{Slot: 4, Macro: macroOf("ab", 50), Cycle: 1}, []string{`Macro "ab"`, "Macro binding"}},
		{mouse.SetShortcut{Slot: 3, Combo: preset(t, keys.Win, "diy5")}, []string{"P0 win diy5 (LWin, LShift, S)"}},
		{mouse.SetShortcut{Slot: 3, Combo: preset(t, keys.Mac, "diy17")}, []string{"P0 mac diy17 (LCtrl, LWin, Q)"}},
	}
	for _, r := range records {
		p := planFor(t, "7B04", maintainerImage(t), allow("7B04"), r.edit)
		var got [][]byte
		for _, o := range p.Ops {
			if o.Phase == plan.Body || (o.Phase == plan.Bind && !bytes.Equal(o.New, shortcutRecord)) {
				got = append(got, o.New)
			}
		}
		if len(got) != len(r.vector) {
			t.Fatalf("%T: ops %+v", r.edit, p.Ops)
		}
		for i, name := range r.vector {
			if !bytes.Equal(got[i], vector(t, name)) {
				t.Errorf("%s: % x", name, got[i])
			}
		}
	}
}

var protected = []flash.Extent{{Addr: 6, Len: 2}, {Addr: 8, Len: 2}, {Addr: 84, Len: 12}, {Addr: 187, Len: 2}}

// settingPairs lists every pair Decode reports outside the known records: the hidden
// and optional settings, KeyOperation and the unmapped 2-byte gaps.
func settingPairs(t *testing.T) []int {
	var out []int
	for _, f := range mouse.Decode(model(t, "7B04"), maintainerImage(t)).Hidden {
		if f.Extent.Len == 2 {
			out = append(out, f.Extent.Addr)
		}
	}
	return out
}

func TestPlanPreservesUnmapped(t *testing.T) {
	im := maintainerImage(t)
	opt := allow("7B04", mouse.FeatureHiddenSlot, "setting.sleep")
	p := planFor(t, "7B04", im, opt,
		mouse.SetDPI{Stage: 0, DPI: 900},
		mouse.SetDPI{Stage: 5, DPI: 5000},
		mouse.SetStages{Count: 5},
		mouse.SetCurrent{Stage: 1},
		mouse.SetKey{Slot: 2, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamMiddle}},
		mouse.SetShortcut{Slot: 3, Combo: cmdC},
		mouse.SetMedia{Slot: 4, Usage: 0xCD},
		mouse.SetMacro{Slot: 5, Macro: macroOf("ab", 50), Cycle: 1},
		mouse.SetKey{Slot: 7, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}},
		mouse.SetSetting{Addr: mouse.AddrSleepTime, Value: 6},
	)
	if len(p.Ops) < 12 {
		t.Fatalf("only %d ops", len(p.Ops))
	}
	for _, o := range p.Ops {
		for _, e := range protected {
			if e.Overlaps(o.Extent) {
				t.Errorf("op %+v touches %v", o, e)
			}
		}
	}
	if err := p.Validate(im, mouse.Layout(model(t, "7B04"))); err != nil {
		t.Error(err)
	}
}

func randomEdits(rng *rand.Rand, t *testing.T, settings []int) []mouse.Edit {
	s := sensor(t, "3104")
	var dpis []int
	for _, d := range mouse.DPIs(s) {
		if d <= 7200 {
			dpis = append(dpis, d)
		}
	}
	fns := []mouse.KeyFn{
		{}, {Type: mouse.TypeMouse, Param: mouse.ParamLeft}, {Type: mouse.TypeMouse, Param: mouse.ParamRight},
		{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}, {Type: mouse.TypeWheel, Param: mouse.ParamScrollDown},
		{Type: mouse.TypeShortcut}, {Type: mouse.TypeMacro, Param: 0x0301}, {Type: mouse.TypeFire, Param: 0x1402},
	}
	media := catalog.MouseMedia()
	presets := keys.MacPresets()
	out := make([]mouse.Edit, 1+rng.IntN(4))
	for i := range out {
		slot := rng.IntN(mouse.Slots)
		switch rng.IntN(8) {
		case 0:
			out[i] = mouse.SetDPI{Stage: rng.IntN(6), DPI: dpis[rng.IntN(len(dpis))]}
		case 1:
			out[i] = mouse.SetStages{Count: 1 + rng.IntN(6)}
		case 2:
			out[i] = mouse.SetCurrent{Stage: rng.IntN(6)}
		case 3:
			out[i] = mouse.SetKey{Slot: slot, Fn: fns[rng.IntN(len(fns))]}
		case 4:
			out[i] = mouse.SetShortcut{Slot: slot, Combo: presets[rng.IntN(len(presets))].Combo()}
		case 5:
			out[i] = mouse.SetMedia{Slot: slot, Usage: media[rng.IntN(len(media))].Code}
		case 6:
			out[i] = mouse.SetMacro{Slot: slot, Macro: macroOf(string(rune('a'+slot)), uint16(10+rng.IntN(90))), Cycle: 1}
		default:
			values := []byte{0, 1, 6, 30}
			out[i] = mouse.SetSetting{Addr: settings[rng.IntN(len(settings))], Value: values[rng.IntN(len(values))]}
		}
	}
	return out
}

func TestPlanNeverTouchesUnknownBytes(t *testing.T) {
	m := model(t, "7B04")
	full := maintainerImage(t)
	fb := full.Bytes()
	end := mouse.AddrMacro + mouse.Slots*mouse.MacroSize
	fs := []mouse.Feature{mouse.FeatureHiddenSlot, mouse.FeatureFireKey}
	for _, f := range mouse.Features() {
		if strings.HasPrefix(string(f), "setting.") {
			fs = append(fs, f)
		}
	}
	opt := allow("7B04", fs...)
	settings := settingPairs(t)
	written := map[int]bool{}
	rng := rand.New(rand.NewPCG(7, 11))
	outcomes := map[string]int{}
	for range 3000 {
		im := flash.New()
		for a := 0; a < end; {
			n := min(1+rng.IntN(48), end-a)
			if rng.IntN(10) != 0 {
				put(t, im, a, fb[a:a+n])
			}
			a += n
		}
		o := allow("7B04")
		if rng.IntN(2) == 0 {
			o = opt
		}
		p, err := mouse.PlanEdits(m, im, randomEdits(rng, t, settings), o)
		switch {
		case errors.Is(err, plan.ErrUnread):
			outcomes["unread"]++
			continue
		case err != nil:
			outcomes["other"]++
			continue
		}
		outcomes["planned"]++
		seen := map[flash.Extent]bool{}
		for _, op := range p.Ops {
			b, known := im.Get(op.Extent)
			if !known {
				t.Fatalf("op %+v touches bytes never read", op)
			}
			if !seen[op.Extent] && !bytes.Equal(b, op.Old) {
				t.Fatalf("op %+v: old bytes differ from the image % x", op, b)
			}
			seen[op.Extent] = true
			written[op.Extent.Addr] = true
			for _, e := range protected {
				if e.Overlaps(op.Extent) {
					t.Fatalf("op %+v touches %v", op, e)
				}
			}
		}
		if err := p.Validate(im, mouse.Layout(m)); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("outcomes %v", outcomes)
	if outcomes["planned"] < 300 || outcomes["unread"] < 300 {
		t.Errorf("outcomes %v: too few plans or unread refusals", outcomes)
	}
	for _, a := range []int{mouse.AddrPerformanceState, mouse.AddrSensorMode, mouse.AddrSleepTime} {
		if !written[a] {
			t.Errorf("no plan wrote the setting at %d", a)
		}
	}
}

// Slots the web app shows past the six physical buttons H3 covers (12 and
// 13 on mid 6) stay Untested until H3b maps them, whatever H3 promoted.
func TestUnmappedSlotsStayUntested(t *testing.T) {
	im := maintainerImage(t)
	edit := []mouse.Edit{mouse.SetKey{Slot: 12, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}}}
	p := planFor(t, "7B06", im, allow("7B06", mouse.FeatureSystem), edit...)
	if p.Ops[0].Tier != catalog.Untested {
		t.Errorf("slot 12 on mid 6 with button.system verified is %v", p.Ops[0].Tier)
	}
	p = planFor(t, "7B06", im, allow("7B06", mouse.FeatureSystem, mouse.FeatureUnmappedSlot), edit...)
	if p.Ops[0].Tier != catalog.Verified {
		t.Errorf("slot 12 on mid 6 once H3b passed is %v", p.Ops[0].Tier)
	}
	p = planFor(t, "7B06", im, allow("7B06", mouse.FeatureSystem), mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}})
	if p.Ops[0].Tier != catalog.Verified {
		t.Errorf("slot 5 on mid 6 with button.system verified is %v", p.Ops[0].Tier)
	}
}

// Refusals number macro events from 1, as the editor shows them.
func TestMacroRefusalsCountFromOne(t *testing.T) {
	keyA := keys.Stroke{Kind: keys.KindKey, Value: 0x04}
	m := mouse.Macro{Name: "fast", Events: []mouse.Event{{Press: true, Stroke: keyA, Delay: 20}, {Stroke: keyA, Delay: 4}}}
	_, err := mouse.PlanEdits(model(t, "7B04"), maintainerImage(t), []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: m, Cycle: 1}}, allow("7B04"))
	if err == nil || !strings.Contains(err.Error(), "macro event 2 delay 4 ms") {
		t.Fatalf("err %v", err)
	}
	m.Events[1].Stroke = keys.Stroke{Kind: keys.KindConsumer, Value: 0xCD}
	if _, err := mouse.EncodeMacro(m); err == nil || !strings.Contains(err.Error(), "macro event 2:") {
		t.Fatalf("encode: %v", err)
	}
}
