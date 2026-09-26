package mouse_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
)

func TestWebCompat(t *testing.T) {
	rCtrl, lShift := keys.RCtrl.Stroke(), keys.LShift.Stroke()
	menu := keys.Stroke{Kind: keys.KindMenu, Value: 0x01}
	experimental := []mouse.Feature{mouse.FeatureHiddenSlot, mouse.FeatureFireKey, mouse.FeatureProfileSwitch,
		mouse.FeatureRateSwitch, mouse.FeatureDragScroll, mouse.FeatureDPILock, mouse.FeatureShortcutCustom,
		mouse.FeatureMediaCustom, mouse.FeatureMacroForeign, "setting.sleep"}
	dpiUp := mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}
	ab := macroOf("ab", 50)
	cases := []struct {
		name  string
		key   string
		setup func(*testing.T, *flash.Image)
		edits []mouse.Edit
		want  []mouse.Reason
	}{
		{"DPI and stages", "7B04", nil, []mouse.Edit{mouse.SetDPI{Stage: 2, DPI: 1700}, mouse.SetStages{Count: 5}}, nil},
		{"system function", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 3, Fn: dpiUp}}, nil},
		{"hidden slot", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 7, Fn: dpiUp}}, []mouse.Reason{mouse.HiddenSlot}},
		{"hidden slot body and binding", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 9, Combo: cmdC}}, []mouse.Reason{mouse.HiddenSlot}},
		{"first unscanned slot", "7B02", nil, []mouse.Edit{mouse.SetKey{Slot: 8, Fn: dpiUp}}, []mouse.Reason{mouse.UnscannedSlot}},
		{"unscanned slot", "7B06", nil, []mouse.Edit{mouse.SetKey{Slot: 12, Fn: dpiUp}}, []mouse.Reason{mouse.UnscannedSlot}},
		{"fire key", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeFire, Param: 0x1401}}}, []mouse.Reason{mouse.KeyTypeHidden}},
		{"profile switch", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeProfile}}}, []mouse.Reason{mouse.KeyTypeHidden}},
		{"DPI lock", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDPILock, Param: 0x15}}}, []mouse.Reason{mouse.KeyTypeHidden}},
		{"rate switch on an office mouse", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeRateSwitch}}}, []mouse.Reason{mouse.KeyTypeHidden}},
		{"rate switch on a gaming mouse", "7B01", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeRateSwitch}}}, nil},
		{"drag scroll off the trackball", "7B04", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}}}, []mouse.Reason{mouse.KeyTypeHidden}},
		{"drag scroll on the trackball", "7B02", nil, []mouse.Edit{mouse.SetKey{Slot: 5, Fn: mouse.KeyFn{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}}}, nil},
		{"right modifier", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{rCtrl, keyC}}}, []mouse.Reason{mouse.RightModifier}},
		{"menu key", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, menu}}}, []mouse.Reason{mouse.MenuKey}},
		{"two plain keys", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keyA, keyC}}}, []mouse.Reason{mouse.CustomCombo}},
		{"modifiers only", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, lShift}}}, []mouse.Reason{mouse.CustomCombo}},
		{"consumer in a combo", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lCtrl, {Kind: keys.KindConsumer, Value: 0xCD}}}}, []mouse.Reason{mouse.CustomCombo}},
		{"left modifiers in any order", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{lShift, lCtrl, lMeta, keyC}}}, nil},
		{"single key", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keyA}}}, nil},
		{"win preset", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: preset(t, keys.Win, "diy5")}}, nil},
		{"mac preset", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: preset(t, keys.Mac, "diy5")}}, nil},
		{"web media", "7B04", nil, []mouse.Edit{mouse.SetMedia{Slot: 3, Usage: 0xCD}}, nil},
		{"other media", "7B04", nil, []mouse.Edit{mouse.SetMedia{Slot: 3, Usage: 0x0301}}, []mouse.Reason{mouse.MediaCode}},
		{"macro on its own slot", "7B04", nil, []mouse.Edit{mouse.SetMacro{Slot: 4, Macro: ab, Cycle: 1}}, nil},
		{"macro from another slot", "7B04", func(t *testing.T, im *flash.Image) {
			put(t, im, macroAddr(t, 3), encodedMacro(t, ab))
			put(t, im, macroAddr(t, 5), encodedMacro(t, ab))
		}, []mouse.Edit{mouse.SetKey{Slot: 3, Fn: mouse.KeyFn{Type: mouse.TypeMacro, Param: 0x0501}}}, []mouse.Reason{mouse.ForeignMacro}},
		{"hidden setting", "7B04", nil, []mouse.Edit{mouse.SetSetting{Addr: mouse.AddrSleepTime, Value: 30}}, []mouse.Reason{mouse.HiddenSetting}},
		{"two-phase on a shown slot", "7B04", nil, []mouse.Edit{mouse.SetShortcut{Slot: 3, Combo: cmdC}}, nil},
		{"two-phase on a hidden slot", "7B04", func(t *testing.T, im *flash.Image) {
			put(t, im, keyAddr(t, 7), shortcutRecord)
			put(t, im, shortcutAddr(t, 7), vector(t, "Ctrl+Tab shortcut"))
		}, []mouse.Edit{mouse.SetShortcut{Slot: 7, Combo: keys.Combo{rCtrl, keyC}}}, []mouse.Reason{mouse.HiddenSlot, mouse.RightModifier}},
		{"several", "7B04", nil, []mouse.Edit{
			mouse.SetSetting{Addr: mouse.AddrSleepTime, Value: 30},
			mouse.SetKey{Slot: 7, Fn: mouse.KeyFn{Type: mouse.TypeFire, Param: 0x1401}},
			mouse.SetShortcut{Slot: 3, Combo: keys.Combo{keyA, keyC}},
		}, []mouse.Reason{mouse.CustomCombo, mouse.HiddenSlot, mouse.KeyTypeHidden, mouse.HiddenSetting}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := maintainerImage(t)
			if tc.setup != nil {
				tc.setup(t, im)
			}
			m := model(t, tc.key)
			p := planFor(t, tc.key, im, allow(tc.key, experimental...), tc.edits...)
			ws := mouse.WebCompat(m, p)
			var got []mouse.Reason
			for i, w := range ws {
				got = append(got, w.Reason)
				if w.Seq < 1 || w.Seq > len(p.Ops) || w.Text == "" || i > 0 && w.Seq < ws[i-1].Seq {
					t.Errorf("warning %+v", w)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("warnings %+v, want %v", ws, tc.want)
			}
		})
	}
}

func TestWebCompatNetEffect(t *testing.T) {
	im := maintainerImage(t)
	put(t, im, keyAddr(t, 7), shortcutRecord)
	put(t, im, shortcutAddr(t, 7), vector(t, "Ctrl+Tab shortcut"))
	p := planFor(t, "7B04", im, allow("7B04", mouse.FeatureHiddenSlot), mouse.SetShortcut{Slot: 7, Combo: cmdC})
	ws := mouse.WebCompat(model(t, "7B04"), p)
	if len(p.Ops) != 3 || len(ws) != 1 || ws[0].Seq != 2 || !strings.Contains(ws[0].Text, "slot 7") {
		t.Errorf("ops %+v warnings %+v", p.Ops, ws)
	}
}

func TestWebCompatRawShortcuts(t *testing.T) {
	combos := []keys.Combo{
		{(keys.LCtrl | keys.LShift).Stroke(), keyC},
		{lCtrl, lCtrl, keyC},
		slices.Repeat(keys.Combo{keyA}, 5),
	}
	for _, c := range combos {
		body, err := mouse.EncodeShortcut(c)
		if err != nil {
			t.Fatal(err)
		}
		e := at(t, mouse.ShortcutExtent, 9)
		p := plan.Plan{Ops: []plan.Op{{Seq: 1, Extent: flash.Extent{Addr: e.Addr, Len: len(body)}, Old: fill(0xFF, len(body)), New: body,
			Phase: plan.Body, Tier: catalog.Experimental}}}
		ws := mouse.WebCompat(model(t, "7B04"), p)
		if len(ws) != 2 || ws[0].Reason != mouse.HiddenSlot || ws[1].Reason != mouse.CustomCombo {
			t.Errorf("%v: %+v", c, ws)
		}
	}
}

func TestWebCompatRawOps(t *testing.T) {
	op := func(addr int, old, new []byte) plan.Plan {
		return plan.Plan{Ops: []plan.Op{{Seq: 1, Extent: flash.Extent{Addr: addr, Len: len(new)}, Old: old, New: new,
			Phase: plan.Record, Tier: catalog.Experimental}}}
	}
	cases := []struct {
		name string
		key  string
		p    plan.Plan
		want string
	}{
		{"rate on an office mouse", "7B04", op(mouse.AddrReportRate, pairOf(4), pairOf(1)), "ReportRate"},
		{"rate on a gaming mouse", "7B01", op(mouse.AddrReportRate, pairOf(4), pairOf(1)), ""},
		{"colour without the DPI light", "7B04", op(mouse.AddrDPIColor, mouse.EncodeColor([3]byte{1, 2, 3}), mouse.EncodeColor([3]byte{3, 2, 1})), "DPIColor"},
		{"colour with the DPI light", "7B01", op(mouse.AddrDPIColor, mouse.EncodeColor([3]byte{1, 2, 3}), mouse.EncodeColor([3]byte{3, 2, 1})), ""},
		{"unmapped", "7B04", op(6, pairOf(0), pairOf(1)), "the unmapped bytes at 6+2"},
		{"KeyOperation", "7B04", op(mouse.AddrKeyOperation, pairOf(0), pairOf(1)), "KeyOperation"},
		{"unchanged", "7B04", op(mouse.AddrKeyOperation, pairOf(0), pairOf(0)), ""},
	}
	for _, tc := range cases {
		ws := mouse.WebCompat(model(t, tc.key), tc.p)
		switch {
		case tc.want == "" && len(ws) != 0,
			tc.want != "" && (len(ws) != 1 || ws[0].Reason != mouse.HiddenSetting || !strings.HasSuffix(ws[0].Text, tc.want)):
			t.Errorf("%s: %+v, want %q", tc.name, ws, tc.want)
		}
	}
	for _, m := range []*catalog.Model{nil, model(t, "0301")} {
		if ws := mouse.WebCompat(m, op(mouse.AddrReportRate, pairOf(4), pairOf(1))); ws != nil {
			t.Errorf("model %v: %+v", m, ws)
		}
	}
}
