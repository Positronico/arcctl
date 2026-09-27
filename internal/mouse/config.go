package mouse

import (
	"cmp"
	"errors"
	"slices"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
)

// Field is one decoded pair or record. Raw is nil only when the bytes were never read.
// An unmapped block that is neither erased nor a pair stays Unknown even when read.
type Field struct {
	Name   string
	Extent flash.Extent
	Raw    []byte
	State  flash.FieldState
	Value  int
	Err    error
}

type Stage struct {
	DPI        DPI
	Color      [3]byte
	DPIField   Field
	ColorField Field
}

type Binding struct {
	Fn    KeyFn
	Field Field
}

// Config is a decoded image. Slots is the class of the body each binding uses;
// ShortcutClass and MacroClass class every body slot, bound or not.
type Config struct {
	Rate          Field
	Stages        Field
	Current       Field
	DPI           [MaxStages]Stage
	Keys          [Slots]Binding
	Shortcuts     [Slots]keys.Combo
	Macros        [Slots]*Macro
	Slots         [Slots]flash.SlotClass
	ShortcutClass [Slots]flash.SlotClass
	MacroClass    [Slots]flash.SlotClass
	Hidden        []Field
}

const (
	pairSize     = 2
	lightSize    = 7
	flywheelSize = 4
	unmapped     = "unmapped"
)

type access uint8

const (
	optional access = iota
	setting
	frozen
)

type hiddenSpec struct {
	name    string
	addr    int
	size    int
	access  access
	feature Feature
	domain  func(m *catalog.Model, v byte) bool
}

// FieldFeature is the feature of the hidden field at addr: the one a write
// of it takes; false when no hidden field starts there.
func FieldFeature(addr int) (Feature, bool) {
	for _, h := range hiddenFields {
		if h.addr == addr {
			return h.feature, true
		}
	}
	return "", false
}

// The setting domains are the values the web app's own setters write.
var (
	onOff      = oneOf(0, 1)
	timeSteps  = oneOf(1, 6, 30, 90, 180)
	brightness = func(_ *catalog.Model, v byte) bool { _, ok := DPILightLevel(v); return ok }
	debounce   = func(m *catalog.Model, v byte) bool { return m.Defaults != nil && int(v) <= m.Defaults.MaxDebounce }
)

func oneOf(vs ...byte) func(*catalog.Model, byte) bool {
	return func(_ *catalog.Model, v byte) bool { return slices.Contains(vs, v) }
}

var hiddenFields = [...]hiddenSpec{
	{"KeyOperation", AddrKeyOperation, pairSize, frozen, "setting.key-operation", nil},
	{"LOD", AddrLOD, pairSize, setting, "setting.lod", oneOf(1, 2)},
	{"DPIEffectMode", AddrDPIEffectMode, pairSize, setting, "setting.dpi-light-mode", oneOf(1, 2)},
	{"DPIEffectBrightness", AddrDPIEffectBrightness, pairSize, setting, "setting.dpi-light-brightness", brightness},
	{"DPIEffectSpeed", AddrDPIEffectSpeed, pairSize, setting, "setting.dpi-light-speed", oneOf(1, 2, 3, 4, 5)},
	{"DPIEffectState", AddrDPIEffectState, pairSize, setting, "setting.dpi-light-state", onOff},
	{"Light", AddrLight, lightSize, setting, "setting.light", nil},
	{"DebounceTime", AddrDebounceTime, pairSize, setting, "setting.debounce", debounce},
	{"MotionSync", AddrMotionSync, pairSize, setting, "setting.motion-sync", onOff},
	{"SleepTime", AddrSleepTime, pairSize, setting, "setting.sleep", timeSteps},
	{"Angle", AddrAngle, pairSize, setting, "setting.angle", onOff},
	{"Ripple", AddrRipple, pairSize, setting, "setting.ripple", onOff},
	{"MovingOffLight", AddrMovingOffLight, pairSize, setting, "setting.moving-off-light", onOff},
	{"PerformanceState", AddrPerformanceState, pairSize, setting, "setting.performance-state", onOff},
	{"Performance", AddrPerformance, pairSize, setting, "setting.performance", timeSteps},
	{"SensorMode", AddrSensorMode, pairSize, setting, "setting.sensor-mode", onOff},
	{"AngleTune", AddrAngleTune, pairSize, optional, "setting.angle-tune", nil},
	{"AngleTuneState", AddrAngleTuneState, pairSize, optional, "setting.angle-tune-state", nil},
	{"SensorFPS20K", AddrSensorFPS20K, pairSize, optional, "setting.sensor-fps20k", nil},
	{"WheelDebounceTime", AddrWheelDebounceTime, pairSize, optional, "setting.wheel-debounce", nil},
	{"DebounceReleaseTime", AddrDebounceReleaseTime, pairSize, optional, "setting.debounce-release", nil},
	{"FlywheelState", AddrFlywheelState, pairSize, optional, "setting.flywheel-state", nil},
	{"Flywheel", AddrFlywheelMaxSpeed, flywheelSize, optional, "setting.flywheel", nil},
	{"LeftTrigger", AddrLeftTrigger, pairSize, optional, "setting.left-trigger", nil},
	{"LeftFastTrigger", AddrLeftFastTrigger, pairSize, optional, "setting.left-fast-trigger", nil},
	{"LeftTactileFeedback", AddrLeftTactileFeedback, pairSize, optional, "setting.left-tactile-feedback", nil},
	{"RightTrigger", AddrRightTrigger, pairSize, optional, "setting.right-trigger", nil},
	{"RightFastTrigger", AddrRightFastTrigger, pairSize, optional, "setting.right-fast-trigger", nil},
	{"RightTactileFeedback", AddrRightTactileFeedback, pairSize, optional, "setting.right-tactile-feedback", nil},
	{"DynamicSensitivity", AddrDynamicSensitivity, pairSize, optional, "setting.dynamic-sensitivity", nil},
	{"DynamicSensitivityMode", AddrDynamicSensitivityMode, pairSize, optional, "setting.dynamic-sensitivity-mode", nil},
	{"VirtualCenter", AddrVirtualCenter, pairSize, optional, "setting.virtual-center", nil},
}

var unmappedExtents = settingsGaps()

func settingsGaps() []flash.Extent {
	var covered [AddrShortcutKey]bool
	mark := func(addr, n int) {
		for a := addr; a < addr+n && a < len(covered); a++ {
			covered[a] = true
		}
	}
	for _, a := range []int{AddrReportRate, AddrMaxDpiStage, AddrCurrentDPI} {
		mark(a, pairSize)
	}
	mark(AddrDPIValue, MaxStages*recordSize)
	mark(AddrDPIColor, MaxStages*recordSize)
	mark(AddrKeyFunction, Slots*recordSize)
	for _, h := range hiddenFields {
		mark(h.addr, h.size)
	}
	var out []flash.Extent
	for a := 0; a < len(covered); a++ {
		if covered[a] {
			continue
		}
		start := a
		for a < len(covered) && !covered[a] {
			a++
		}
		out = append(out, flash.Extent{Addr: start, Len: a - start})
	}
	return out
}

func Decode(m *catalog.Model, im *flash.Image) Config {
	if im == nil {
		im = flash.New()
	}
	var s *catalog.Sensor
	if m != nil {
		s = m.Sensor
	}
	c := Config{
		Rate:    field("ReportRate", im, pairExtent(AddrReportRate), pairDecoder(DecodeRate)),
		Stages:  field("MaxDpiStage", im, pairExtent(AddrMaxDpiStage), pairDecoder(DecodeStageCount)),
		Current: field("CurrentDPI", im, pairExtent(AddrCurrentDPI), pairDecoder(DecodeCurrentStage)),
		Hidden:  decodeHidden(im),
	}
	for i := range c.DPI {
		st := &c.DPI[i]
		e, _ := DPIExtent(i)
		st.DPIField = field("DPIValue", im, e, func(b []byte) (int, error) {
			if s == nil {
				return rawRecord(b)
			}
			d, err := DecodeDPI(s, b)
			st.DPI = d
			return 0, err
		})
		e, _ = ColorExtent(i)
		st.ColorField = field("DPIColor", im, e, func(b []byte) (int, error) {
			col, err := DecodeColor(b)
			st.Color = col
			return 0, err
		})
	}
	for k := range c.Keys {
		bnd := &c.Keys[k]
		e, _ := KeyFnExtent(k)
		bnd.Field = field("KeyFunction", im, e, func(b []byte) (int, error) {
			fn, err := DecodeKeyFn(b)
			bnd.Fn = fn
			return 0, err
		})
		e, _ = ShortcutExtent(k)
		combo, err := DecodeShortcut(knownPrefix(im, e))
		c.ShortcutClass[k] = Class(err)
		if err == nil {
			c.Shortcuts[k] = combo
		}
		e, _ = MacroExtent(k)
		mac, err := DecodeMacro(knownPrefix(im, e))
		c.MacroClass[k] = Class(err)
		if err == nil {
			c.Macros[k] = &mac
		}
		switch bnd.Fn.Type {
		case TypeShortcut:
			c.Slots[k] = c.ShortcutClass[k]
		case TypeMacro:
			c.Slots[k] = c.MacroClass[k]
		}
	}
	return c
}

func decodeHidden(im *flash.Image) []Field {
	out := make([]Field, 0, len(hiddenFields)+len(unmappedExtents))
	for _, h := range hiddenFields {
		decode := rawPair
		if h.size != pairSize {
			decode = rawRecord
		}
		out = append(out, field(h.name, im, flash.Extent{Addr: h.addr, Len: h.size}, decode))
	}
	for _, e := range unmappedExtents {
		if e.Len == pairSize {
			out = append(out, field(unmapped, im, e, rawPair))
			continue
		}
		f := Field{Name: unmapped, Extent: e}
		if b, ok := im.Get(e); ok {
			f.Raw = b
			if all(b, 0xFF) {
				f.State, f.Err = flash.Erased, ErrEmpty
			}
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Field) int { return cmp.Compare(a.Extent.Addr, b.Extent.Addr) })
	return out
}

func field(name string, im *flash.Image, e flash.Extent, decode func([]byte) (int, error)) Field {
	f := Field{Name: name, Extent: e}
	b, ok := im.Get(e)
	if !ok {
		return f
	}
	f.Raw = b
	f.Value, f.Err = decode(b)
	f.State = State(f.Err)
	return f
}

func pairExtent(addr int) flash.Extent { return flash.Extent{Addr: addr, Len: pairSize} }

func pairDecoder(decode func(flash.Pair) (int, error)) func([]byte) (int, error) {
	return func(b []byte) (int, error) { return decode(flash.Pair(b)) }
}

func rawPair(b []byte) (int, error) {
	p := flash.Pair(b)
	_, err := pairValue(p)
	if err != nil && !errors.Is(err, ErrUnset) {
		return 0, err
	}
	return int(p.Value()), err
}

func rawRecord(b []byte) (int, error) {
	_, err := record(b, len(b))
	return 0, err
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
