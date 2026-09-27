package backup

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

// Summary is a decoded mouse configuration in words: enough to read a backup
// without arcctl, or to set a mouse up again by hand. Stages count from 1;
// slots count from 0, as in flash.
type Summary struct {
	OS        string     `json:"os"`
	Rate      *int       `json:"rate_hz"`
	Stages    *int       `json:"stages"`
	Current   *int       `json:"current_stage"`
	DPI       []DPIStage `json:"dpi"`
	Buttons   []Button   `json:"buttons"`
	Shortcuts []Shortcut `json:"shortcuts"`
	Macros    []Macro    `json:"macros"`
	Settings  []Setting  `json:"settings"`
	Invalid   []Invalid  `json:"invalid"`
}

type DPIStage struct {
	Stage   int    `json:"stage"`
	X       int    `json:"x,omitempty"`
	Y       int    `json:"y,omitempty"`
	Color   string `json:"color,omitempty"`
	Active  bool   `json:"active"`
	Current bool   `json:"current,omitempty"`
	State   string `json:"state"`
}

// Button is one key-function slot. Button is the physical button the model
// puts there, "" for a slot it does not list.
type Button struct {
	Slot   int    `json:"slot"`
	Button string `json:"button,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
	Action string `json:"action"`
	State  string `json:"state"`
}

type Shortcut struct {
	Slot   int    `json:"slot"`
	Keys   string `json:"keys,omitempty"`
	Preset string `json:"preset,omitempty"`
	Bound  bool   `json:"bound"`
	State  string `json:"state"`
}

type Macro struct {
	Slot    int     `json:"slot"`
	Name    string  `json:"name,omitempty"`
	Events  []Event `json:"events,omitempty"`
	BoundBy []int   `json:"bound_by,omitempty"`
	State   string  `json:"state"`
}

type Event struct {
	Press bool   `json:"press"`
	Key   string `json:"key"`
	Delay int    `json:"delay_ms"`
}

// Setting is a hidden or optional field, or a block of bytes no field maps
// ("unmapped"), that holds something other than erased flash.
type Setting struct {
	Name  string `json:"name"`
	Addr  int    `json:"addr"`
	Raw   string `json:"raw"`
	Value *int   `json:"value,omitempty"`
	State string `json:"state"`
}

type Invalid struct {
	Name  string `json:"name"`
	Addr  int    `json:"addr"`
	Len   int    `json:"len"`
	Raw   string `json:"raw"`
	Error string `json:"error,omitempty"`
}

const maxRaw = 16

// Summarize decodes im as a mouse of model m. Key names follow os.
func Summarize(m *catalog.Model, im *flash.Image, os keys.OS) *Summary {
	c := mouse.Decode(m, im)
	s := &Summary{
		OS: os.String(), Rate: value(c.Rate), Stages: value(c.Stages),
		Shortcuts: []Shortcut{}, Macros: []Macro{}, Settings: []Setting{}, Invalid: []Invalid{},
	}
	if v := value(c.Current); v != nil {
		*v++
		s.Current = v
	}
	s.invalid(c.Rate, "ReportRate")
	s.invalid(c.Stages, "MaxDpiStage")
	s.invalid(c.Current, "CurrentDPI")
	for i := range c.DPI {
		s.DPI = append(s.DPI, stage(&c, i, s.Stages))
		s.invalid(c.DPI[i].DPIField, fmt.Sprintf("DPI stage %d", i+1))
		s.invalid(c.DPI[i].ColorField, fmt.Sprintf("DPI colour %d", i+1))
	}
	for k := range c.Keys {
		s.Buttons = append(s.Buttons, button(m, &c, k, os))
		s.invalid(c.Keys[k].Field, fmt.Sprintf("KeyFunction %d", k))
	}
	for k := range mouse.Slots {
		s.shortcut(im, &c, k, os)
	}
	for k := range mouse.Slots {
		s.macro(im, &c, k, os)
	}
	for _, f := range c.Hidden {
		s.setting(f)
	}
	return s
}

func value(f mouse.Field) *int {
	if f.State != flash.OK {
		return nil
	}
	v := f.Value
	return &v
}

func stage(c *mouse.Config, i int, count *int) DPIStage {
	st := c.DPI[i]
	d := DPIStage{Stage: i + 1, State: st.DPIField.State.String(), Active: count != nil && i < *count}
	if st.DPIField.State == flash.OK {
		d.X, d.Y = st.DPI.X, st.DPI.Y
	}
	if st.ColorField.State == flash.OK {
		d.Color = fmt.Sprintf("#%02x%02x%02x", st.Color[0], st.Color[1], st.Color[2])
	}
	if c.Current.State == flash.OK && c.Current.Value == i {
		d.Current = true
	}
	return d
}

func button(m *catalog.Model, c *mouse.Config, k int, os keys.OS) Button {
	b := Button{Slot: k, State: c.Keys[k].Field.State.String(), Action: Action(m, c, k, os)}
	for _, pb := range m.Buttons {
		if pb.Slot == k {
			b.Button, b.Hidden = pb.Label, !pb.Visible
		}
	}
	return b
}

func (s *Summary) shortcut(im *flash.Image, c *mouse.Config, k int, os keys.OS) {
	class := c.ShortcutClass[k]
	if class != flash.SlotValid && class != flash.SlotInvalid {
		return
	}
	sc := Shortcut{Slot: k, Bound: c.Keys[k].Field.State == flash.OK && c.Keys[k].Fn.Type == mouse.TypeShortcut, State: class.String()}
	if class == flash.SlotValid {
		sc.Keys, sc.Preset = ComboLabel(c.Shortcuts[k], os)
	} else {
		e, _ := mouse.ShortcutExtent(k)
		b := knownPrefix(im, e)
		_, err := mouse.DecodeShortcut(b)
		s.add(fmt.Sprintf("Shortcut %d", k), e, b, err)
	}
	s.Shortcuts = append(s.Shortcuts, sc)
}

func (s *Summary) macro(im *flash.Image, c *mouse.Config, k int, os keys.OS) {
	class := c.MacroClass[k]
	if class != flash.SlotValid && class != flash.SlotInvalid {
		return
	}
	mc := Macro{Slot: k, State: class.String()}
	for slot, b := range c.Keys {
		if b.Field.State == flash.OK && b.Fn.Type == mouse.TypeMacro && int(b.Fn.Param>>8) == k {
			mc.BoundBy = append(mc.BoundBy, slot)
		}
	}
	if mac := c.Macros[k]; class == flash.SlotValid && mac != nil {
		mc.Name = mac.Name
		for _, e := range mac.Events {
			mc.Events = append(mc.Events, Event{Press: e.Press, Key: e.Stroke.Name(os), Delay: int(e.Delay)})
		}
	} else {
		e, _ := mouse.MacroExtent(k)
		b := knownPrefix(im, e)
		_, err := mouse.DecodeMacro(b)
		s.add(fmt.Sprintf("Macro %d", k), e, b, err)
	}
	s.Macros = append(s.Macros, mc)
}

// optional fields exist only on some models; an invalid pair there means the
// model does not have the feature, so it is left out rather than flagged.
var optional = map[int]bool{
	mouse.AddrAngleTune: true, mouse.AddrAngleTuneState: true, mouse.AddrSensorFPS20K: true,
	mouse.AddrWheelDebounceTime: true, mouse.AddrDebounceReleaseTime: true, mouse.AddrFlywheelState: true,
	mouse.AddrFlywheelMaxSpeed: true, mouse.AddrLeftTrigger: true, mouse.AddrLeftFastTrigger: true,
	mouse.AddrLeftTactileFeedback: true, mouse.AddrRightTrigger: true, mouse.AddrRightFastTrigger: true,
	mouse.AddrRightTactileFeedback: true, mouse.AddrDynamicSensitivity: true,
	mouse.AddrDynamicSensitivityMode: true, mouse.AddrVirtualCenter: true,
}

func (s *Summary) setting(f mouse.Field) {
	switch {
	case f.Raw == nil, f.State == flash.Erased:
		return
	case f.State == flash.Invalid && optional[f.Extent.Addr]:
		return
	case f.State == flash.Invalid:
		s.invalid(f, f.Name)
		return
	}
	st := Setting{Name: f.Name, Addr: f.Extent.Addr, Raw: rawHex(f.Raw), State: f.State.String()}
	if f.Name == "unmapped" && f.State == flash.Unknown {
		st.State = "raw"
	}
	if len(f.Raw) == 2 && (f.State == flash.OK || f.State == flash.Unset) {
		v := f.Value
		st.Value = &v
	}
	s.Settings = append(s.Settings, st)
}

func (s *Summary) invalid(f mouse.Field, name string) {
	if f.State == flash.Invalid {
		s.add(name, f.Extent, f.Raw, f.Err)
	}
}

func (s *Summary) add(name string, e flash.Extent, raw []byte, err error) {
	iv := Invalid{Name: name, Addr: e.Addr, Len: e.Len, Raw: rawHex(raw)}
	if err != nil {
		iv.Error = strings.TrimPrefix(err.Error(), "mouse: ")
	}
	s.Invalid = append(s.Invalid, iv)
}

func rawHex(b []byte) string {
	if len(b) > maxRaw {
		return spaced(b[:maxRaw]) + " ..."
	}
	return spaced(b)
}

func spaced(b []byte) string {
	h := hex.EncodeToString(b)
	var out strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(h[i : i+2])
	}
	return out.String()
}

func knownPrefix(im *flash.Image, e flash.Extent) []byte {
	n := 0
	for n < e.Len {
		if _, ok := im.Byte(e.Addr + n); !ok {
			break
		}
		n++
	}
	b, _ := im.Get(flash.Extent{Addr: e.Addr, Len: n})
	return b
}

var fnLabels = map[mouse.KeyFn]string{
	{Type: mouse.TypeDisable}:                                  "button.disable",
	{Type: mouse.TypeMouse, Param: mouse.ParamLeft}:            "button.left",
	{Type: mouse.TypeMouse, Param: mouse.ParamRight}:           "button.right",
	{Type: mouse.TypeMouse, Param: mouse.ParamMiddle}:          "button.middle",
	{Type: mouse.TypeMouse, Param: mouse.ParamBack}:            "button.back",
	{Type: mouse.TypeMouse, Param: mouse.ParamForward}:         "button.forward",
	{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}:          "button.dpi_cycle",
	{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp}:             "button.dpi_up",
	{Type: mouse.TypeDPI, Param: mouse.ParamDPIDown}:           "button.dpi_down",
	{Type: mouse.TypeScroll, Param: mouse.ParamScrollLeft}:     "button.scroll_left",
	{Type: mouse.TypeScroll, Param: mouse.ParamScrollRight}:    "button.scroll_right",
	{Type: mouse.TypeWheel, Param: mouse.ParamScrollUp}:        "button.scroll_up",
	{Type: mouse.TypeWheel, Param: mouse.ParamScrollDown}:      "button.scroll_down",
	{Type: mouse.TypeRateSwitch}:                               "button.rate_switch",
	{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}: "button.drag_scroll",
}

// Action says in words what key-function slot k does.
func Action(m *catalog.Model, c *mouse.Config, k int, os keys.OS) string {
	b := c.Keys[k]
	switch b.Field.State {
	case flash.Unknown:
		return "not read"
	case flash.Erased:
		return "erased"
	case flash.Invalid:
		return "invalid (" + rawHex(b.Field.Raw) + ")"
	}
	fn := b.Fn
	if key, ok := fnLabels[fn]; ok {
		if s, ok := catalog.Label(key); ok {
			return s
		}
	}
	switch fn.Type {
	case mouse.TypeShortcut:
		switch c.Slots[k] {
		case flash.SlotValid:
			keys, preset := ComboLabel(c.Shortcuts[k], os)
			if preset != "" {
				return preset + " (" + keys + ")"
			}
			return keys
		case flash.SlotEmpty:
			return "shortcut with an empty body"
		case flash.SlotInvalid:
			return "shortcut with an invalid body"
		}
		return "shortcut (body not read)"
	case mouse.TypeMacro:
		slot := int(fn.Param >> 8)
		out := "Macro " + strconv.Itoa(slot)
		if mac := c.Macros[slot]; mac != nil {
			out += " " + strconv.Quote(mac.Name)
		} else if c.MacroClass[slot] == flash.SlotUnknown {
			out += " (not read)"
		} else {
			out += " (" + c.MacroClass[slot].String() + ")"
		}
		return out + ", " + Repeat(byte(fn.Param))
	case mouse.TypeFire:
		return fmt.Sprintf("Fire key, every %d ms, %d times", fn.Param>>8, fn.Param&0xFF)
	case mouse.TypeProfile:
		return "Profile switch"
	case mouse.TypeDPILock:
		var s *catalog.Sensor
		if m != nil {
			s = m.Sensor
		}
		if dpi, ok := fn.LockedDPI(s); ok {
			return fmt.Sprintf("DPI lock %d", dpi)
		}
		return fmt.Sprintf("DPI lock (raw %#04x)", fn.Param)
	}
	return fn.Type.String() + fmt.Sprintf(" %#04x", fn.Param)
}

// Repeat says how often a macro binding plays its macro.
func Repeat(cycle byte) string {
	for _, o := range keys.RepeatOptions() {
		if o.Value != cycle {
			continue
		}
		switch o.Mode {
		case keys.RepeatUntilPressedAgain:
			return "until pressed again"
		case keys.RepeatUntilReleased:
			return "while held"
		case keys.RepeatUntilAnyKey:
			return "until any key"
		}
	}
	if cycle == 1 {
		return "once"
	}
	return strconv.Itoa(int(cycle)) + " times"
}

// ComboLabel names the keys of a shortcut body and the preset it matches.
// A single consumer usage is a media key.
func ComboLabel(c keys.Combo, os keys.OS) (combo, preset string) {
	if len(c) == 1 && c[0].Kind == keys.KindConsumer {
		return "Media: " + c[0].Name(os), ""
	}
	if p, ok := keys.MatchPreset(os, c); ok {
		preset = p.Label
	}
	return c.Format(os), preset
}
