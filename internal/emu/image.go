package emu

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

var ErrNoDefaults = errors.New("emu: the model has no mouse defaults")

// Defaults builds the flash a mouse model ships with, from the catalog's
// defaults: report rate, DPI stages and colours, the DPI light, the button
// functions (a media button with its body), and the setting pairs the catalog
// gives in the web app's units. Buttons the model does not list are Disable;
// every other byte reads as erased flash.
func Defaults(m *catalog.Model) (*flash.Image, error) {
	if m == nil || m.Defaults == nil || m.Family != catalog.FamilyMouse {
		return nil, ErrNoDefaults
	}
	df := m.Defaults
	im := flash.New()
	w := writer{im: im}
	w.pair(mouse.AddrReportRate)(mouse.EncodeRate(df.ReportRate))
	w.pair(mouse.AddrMaxDpiStage)(mouse.EncodeStageCount(len(df.DPIs)))
	w.pair(mouse.AddrCurrentDPI)(mouse.EncodeCurrentStage(df.CurrentDPI))
	for i, s := range df.DPIs {
		e, _ := mouse.DPIExtent(i)
		w.record(e.Addr)(mouse.EncodeDPI(m.Sensor, s.DPI))
		e, _ = mouse.ColorExtent(i)
		w.record(e.Addr)(mouse.EncodeColor(s.Color), nil)
	}
	w.pair(mouse.AddrDPIEffectBrightness)(mouse.EncodeDPILight(df.DPIEffect.Brightness))
	raw := map[int]int{
		mouse.AddrDPIEffectMode:    df.DPIEffect.Mode,
		mouse.AddrDPIEffectSpeed:   df.DPIEffect.Speed,
		mouse.AddrDebounceTime:     df.Debounce,
		mouse.AddrMotionSync:       b2i(df.MotionSync),
		mouse.AddrSleepTime:        df.SleepTime,
		mouse.AddrAngle:            b2i(df.Angle),
		mouse.AddrRipple:           b2i(df.Ripple),
		mouse.AddrMovingOffLight:   b2i(df.LightEffect.MovingOffState),
		mouse.AddrPerformanceState: b2i(df.PerformanceState),
		mouse.AddrPerformance:      df.Performance,
		mouse.AddrSensorMode:       df.SensorMode,
	}
	if df.LOD != nil {
		raw[mouse.AddrLOD] = *df.LOD
	}
	for _, addr := range slices.Sorted(maps.Keys(raw)) {
		w.pair(addr)(rawPair(raw[addr]))
	}
	for slot := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(slot)
		w.record(e.Addr)(mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDisable}))
	}
	for _, b := range m.Buttons {
		fn := mouse.KeyFn{Type: mouse.KeyType(b.Type), Param: b.Param}
		if b.Media != 0 {
			fn = mouse.KeyFn{Type: mouse.TypeShortcut}
			e, _ := mouse.ShortcutExtent(b.Slot)
			w.record(e.Addr)(mouse.EncodeMedia(b.Media))
		}
		e, _ := mouse.KeyFnExtent(b.Slot)
		w.record(e.Addr)(mouse.EncodeKeyFn(fn))
	}
	if w.err != nil {
		return nil, fmt.Errorf("emu: defaults of %s: %w", m.Key, w.err)
	}
	return im, nil
}

type writer struct {
	im  *flash.Image
	err error
}

func (w *writer) pair(addr int) func(flash.Pair, error) {
	return func(p flash.Pair, err error) { w.set(addr, p[:], err) }
}

func (w *writer) record(addr int) func(flash.Record, error) {
	return func(r flash.Record, err error) { w.set(addr, r, err) }
}

func (w *writer) set(addr int, b []byte, err error) {
	if err == nil {
		err = w.im.Set(addr, b)
	}
	if err != nil && w.err == nil {
		w.err = fmt.Errorf("at %d: %w", addr, err)
	}
}

func rawPair(v int) (flash.Pair, error) {
	if v < 0 || v > 0xFF {
		return flash.Pair{}, fmt.Errorf("setting value %d", v)
	}
	return flash.NewPair(byte(v)), nil
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Placeholder bodies for WithBodies. None of them is anyone's real shortcut.
var (
	placeholderShortcuts = []keys.Combo{
		{keys.LMeta.Stroke(), {Kind: keys.KindKey, Value: 0x06}},                       // Cmd+C
		{keys.LMeta.Stroke(), {Kind: keys.KindKey, Value: 0x19}},                       // Cmd+V
		{keys.LMeta.Stroke(), {Kind: keys.KindKey, Value: 0x2B}},                       // Cmd+Tab
		{keys.LCtrl.Stroke(), {Kind: keys.KindKey, Value: 0x2B}},                       // Ctrl+Tab
		{keys.LMeta.Stroke(), keys.LShift.Stroke(), {Kind: keys.KindKey, Value: 0x2B}}, // Cmd+Shift+Tab
		{{Kind: keys.KindConsumer, Value: 0xCD}},                                       // Play/Pause
	}
	placeholderMacro = mouse.Macro{Name: "emu", Events: []mouse.Event{
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 50},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 10},
	}}
)

// WithBodies returns a copy of im in which every shortcut or macro slot that
// a binding points at, and whose first byte im does not know, holds a
// placeholder body built with the mouse encoders. It turns a dump of the
// settings page into an image the emulator can serve whole.
func WithBodies(im *flash.Image) (*flash.Image, error) {
	out := im.Clone()
	macro, err := mouse.EncodeMacro(placeholderMacro)
	if err != nil {
		return nil, err
	}
	for slot := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(slot)
		b, ok := im.Get(e)
		if !ok {
			continue
		}
		fn, err := mouse.DecodeKeyFn(b)
		if err != nil {
			continue
		}
		switch fn.Type {
		case mouse.TypeShortcut:
			body, err := mouse.EncodeShortcut(placeholderShortcuts[slot%len(placeholderShortcuts)])
			if err != nil {
				return nil, err
			}
			fill(out, mouse.ShortcutExtent, slot, body)
		case mouse.TypeMacro:
			fill(out, mouse.MacroExtent, slot, macro)
			fill(out, mouse.MacroExtent, int(fn.Param>>8), macro)
		}
	}
	return out, nil
}

func fill(im *flash.Image, table func(int) (flash.Extent, bool), slot int, body []byte) {
	e, ok := table(slot)
	if !ok {
		return
	}
	if _, known := im.Byte(e.Addr); !known {
		_ = im.Set(e.Addr, body)
	}
}
