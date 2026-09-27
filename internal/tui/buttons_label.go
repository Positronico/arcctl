package tui

import (
	"fmt"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

// pickFn is a key function the picker offers by name, with the catalog
// label ids of its name and group.
type pickFn struct {
	fn      mouse.KeyFn
	label   string
	group   string
	feature mouse.Feature
}

// systemFns are the System group (§7.4), in the order the picker lists them.
var systemFns = []pickFn{
	{mouse.KeyFn{Type: mouse.TypeDisable}, "button.disable", "button.group.system", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft}, "button.left", "button.group.mouse", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamRight}, "button.right", "button.group.mouse", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamMiddle}, "button.middle", "button.group.mouse", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack}, "button.back", "button.group.mouse", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamForward}, "button.forward", "button.group.mouse", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeScroll, Param: mouse.ParamScrollLeft}, "button.scroll_left", "button.group.hscroll", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeScroll, Param: mouse.ParamScrollRight}, "button.scroll_right", "button.group.hscroll", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeWheel, Param: mouse.ParamScrollUp}, "button.scroll_up", "button.group.vscroll", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeWheel, Param: mouse.ParamScrollDown}, "button.scroll_down", "button.group.vscroll", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}, "button.dpi_cycle", "button.group.dpi", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}, "button.dpi_up", "button.group.dpi", mouse.FeatureSystem},
	{mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIDown}, "button.dpi_down", "button.group.dpi", mouse.FeatureSystem},
}

// specialFns follow the presets in the Special group. Each has its own
// feature, so it shows only where its tier allows (D5).
var specialFns = []pickFn{
	{mouse.KeyFn{Type: mouse.TypeRateSwitch}, "button.rate_switch", "", mouse.FeatureRateSwitch},
	{mouse.KeyFn{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}, "button.drag_scroll", "", mouse.FeatureDragScroll},
	{mouse.KeyFn{Type: mouse.TypeProfile}, "button.profile_switch", "", mouse.FeatureProfileSwitch},
}

// buttonOwnLabels name what the catalog has no label for.
var buttonOwnLabels = map[string]string{
	"button.profile_switch": "Profile Switch",
	"button.dpi_lock":       "DPI Lock",
}

func buttonLabel(id string) string {
	if s, ok := catalog.Label(id); ok {
		return s
	}
	if s, ok := buttonOwnLabels[id]; ok {
		return s
	}
	return id
}

// keyFnName names a key function that needs no body.
func keyFnName(m *catalog.Model, fn mouse.KeyFn) string {
	for _, list := range [][]pickFn{systemFns, specialFns} {
		for _, e := range list {
			if e.fn == fn {
				return buttonLabel(e.label)
			}
		}
	}
	switch fn.Type {
	case mouse.TypeFire:
		return fmt.Sprintf("%s, %d ms, %d times", buttonLabel("button.fire_key"), fn.Param>>8, fn.Param&0xFF)
	case mouse.TypeDPILock:
		if m != nil {
			if d, ok := fn.LockedDPI(m.Sensor); ok {
				return fmt.Sprintf("%s %d", buttonLabel("button.dpi_lock"), d)
			}
		}
		return fmt.Sprintf("%s raw 0x%04X", buttonLabel("button.dpi_lock"), fn.Param)
	}
	return fmt.Sprintf("%s 0x%04X", fn.Type, fn.Param)
}

// comboText shows a shortcut body in the naming of os: a media key by its
// HID usage label, a combo in its stored order, with the name of the preset
// it matches in any order.
func comboText(c keys.Combo, os keys.OS) string {
	if len(c) == 1 && c[0].Kind == keys.KindConsumer {
		return c[0].Name(os)
	}
	s := c.Format(os)
	if p, ok := keys.MatchPreset(os, c); ok {
		s += " (" + p.Label + ")"
	}
	return s
}

func cycleText(cycle int) string {
	switch cycle {
	case 253:
		return "until pressed again"
	case 254:
		return "while held"
	case 255:
		return "until any key"
	}
	return "×" + strconv.Itoa(cycle)
}

// slotState is what the State column says about a slot.
type slotState uint8

const (
	slotValid slotState = iota
	slotEmpty
	slotInvalid
	slotUnread
	slotPending
)

var slotStateTexts = [...]string{"valid", "empty", "invalid", "unread", "pending"}

func (s slotState) String() string { return slotStateTexts[s] }

// slotFunction names what slot k does on the mouse now.
func slotFunction(m *catalog.Model, cfg *mouse.Config, k int, os keys.OS) (string, slotState) {
	if cfg == nil {
		return "not read", slotUnread
	}
	b := cfg.Keys[k]
	switch b.Field.State {
	case flash.Unknown:
		return "not read", slotUnread
	case flash.Erased:
		return "empty", slotEmpty
	case flash.OK:
	default:
		return "invalid binding", slotInvalid
	}
	switch b.Fn.Type {
	case mouse.TypeShortcut:
		return bodyFunction("shortcut", cfg.Slots[k], func() string { return comboText(cfg.Shortcuts[k], os) })
	case mouse.TypeMacro:
		src, cycle := int(b.Fn.Param>>8), int(b.Fn.Param&0xFF)
		return bodyFunction("macro", cfg.MacroClass[src], func() string {
			s := "Macro " + strconv.Quote(cfg.Macros[src].Name)
			if src != k {
				s += " of slot " + strconv.Itoa(src)
			}
			return s + " " + cycleText(cycle)
		})
	}
	return keyFnName(m, b.Fn), slotValid
}

func bodyFunction(kind string, class flash.SlotClass, valid func() string) (string, slotState) {
	switch class {
	case flash.SlotValid:
		return valid(), slotValid
	case flash.SlotEmpty:
		return kind + ", empty body", slotEmpty
	case flash.SlotInvalid:
		return kind + ", invalid body", slotInvalid
	}
	return kind + " (not read)", slotUnread
}

// editText names what a staged edit gives its slot.
func editText(m *catalog.Model, e mouse.Edit, os keys.OS) string {
	switch e := e.(type) {
	case mouse.SetKey:
		return keyFnName(m, e.Fn)
	case mouse.SetShortcut:
		return comboText(e.Combo, os)
	case mouse.SetMedia:
		return keys.Stroke{Kind: keys.KindConsumer, Value: e.Usage}.Name(os)
	case mouse.SetMacro:
		return "Macro " + strconv.Quote(e.Macro.Name) + " " + cycleText(e.Cycle)
	}
	return "an edit"
}

// editFn is the key function an edit binds to its slot.
func editFn(e mouse.Edit) (mouse.KeyFn, bool) {
	switch e := e.(type) {
	case mouse.SetKey:
		return e.Fn, true
	case mouse.SetShortcut, mouse.SetMedia:
		return mouse.KeyFn{Type: mouse.TypeShortcut}, true
	case mouse.SetMacro:
		fn, err := mouse.MacroBinding(e.Slot, e.Cycle)
		return fn, err == nil
	}
	return mouse.KeyFn{}, false
}

// gateNote says what a tier asks of a write under the run's flags and mode.
func gateNote(c *Context, t catalog.Tier) string {
	switch {
	case t < catalog.Experimental:
	case c.Mode == ModeDryRun:
		return t.String() + "; dry run: nothing reaches the mouse"
	case c.Mode == ModeReadOnly:
		return t.String() + "; read-only session"
	}
	switch t {
	case catalog.Verified:
		return "verified on this firmware"
	case catalog.Untested:
		if c.Gates.AllowUntested {
			return "untested: the review asks you to type a confirmation"
		}
		return "untested: writes need --allow-untested"
	case catalog.Experimental:
		if c.Gates.Experimental {
			return "experimental: the review asks you to type a confirmation"
		}
		return "experimental: writes need --experimental"
	}
	return t.String() + ": never written"
}
