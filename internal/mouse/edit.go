package mouse

import (
	"errors"
	"math/bits"
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/plan"
)

var ErrRefused = errors.New("mouse: edit refused")

type Feature string

const (
	FeatureStages                Feature = "dpi.stages"
	FeatureCurrent               Feature = "dpi.current"
	FeatureDPI                   Feature = "dpi.value"
	FeatureSystem                Feature = "button.system"
	FeatureMedia                 Feature = "button.media"
	FeatureMediaCustom           Feature = "button.media.custom"
	FeatureShortcut              Feature = "button.shortcut"
	FeatureShortcutRightModifier Feature = "button.shortcut.right-modifier"
	FeatureShortcutMenu          Feature = "button.shortcut.menu"
	FeatureShortcutCustom        Feature = "button.shortcut.custom"
	FeatureMacro                 Feature = "button.macro"
	FeatureMacroForeign          Feature = "button.macro.foreign"
	FeatureRateSwitch            Feature = "button.rate-switch"
	FeatureDragScroll            Feature = "button.drag-scroll"
	FeatureFireKey               Feature = "button.fire-key"
	FeatureProfileSwitch         Feature = "button.profile-switch"
	FeatureDPILock               Feature = "button.dpi-lock"
	FeatureHiddenSlot            Feature = "button.hidden-slot"
	FeatureUnmappedSlot          Feature = "button.unmapped-slot"
)

func Features() []Feature {
	out := []Feature{FeatureStages, FeatureCurrent, FeatureDPI, FeatureSystem, FeatureMedia, FeatureMediaCustom,
		FeatureShortcut, FeatureShortcutRightModifier, FeatureShortcutMenu, FeatureShortcutCustom, FeatureMacro,
		FeatureMacroForeign, FeatureRateSwitch, FeatureDragScroll, FeatureFireKey, FeatureProfileSwitch,
		FeatureDPILock, FeatureHiddenSlot, FeatureUnmappedSlot}
	for _, h := range hiddenFields {
		out = append(out, h.feature)
	}
	return out
}

type Options struct {
	Device   plan.Identity
	Profile  *byte
	Firmware string
	Verified catalog.Verifications
}

// Tier reports the feature's tier on m and whether PlanEdits writes it. An Untested
// feature becomes Verified once its hardware stage is recorded for this firmware; an
// Experimental one is written only after that record exists.
func (f Feature) Tier(m *catalog.Model, opt Options) (catalog.Tier, bool) {
	t := baseTier(m, f)
	covered := t >= catalog.Experimental && opt.Verified.Covers(m.Key, string(f), opt.Firmware)
	switch t {
	case catalog.Untested:
		if covered {
			return catalog.Verified, true
		}
		return t, true
	case catalog.Experimental:
		return t, covered
	}
	return t, false
}

func baseTier(m *catalog.Model, f Feature) catalog.Tier {
	switch {
	case m == nil:
		return catalog.Off
	case m.Family != catalog.FamilyMouse:
		return catalog.ReadOnly
	}
	switch f {
	case FeatureStages, FeatureCurrent, FeatureDPI, FeatureSystem, FeatureMedia,
		FeatureShortcut, FeatureShortcutRightModifier, FeatureShortcutMenu, FeatureMacro, FeatureUnmappedSlot:
		return catalog.Untested
	case FeatureRateSwitch:
		return shownTier(!m.UI.Office)
	case FeatureDragScroll:
		return shownTier(m.UI.GameRoller)
	case FeatureFireKey, FeatureProfileSwitch, FeatureDPILock, FeatureHiddenSlot,
		FeatureShortcutCustom, FeatureMediaCustom, FeatureMacroForeign:
		return catalog.Experimental
	}
	for _, h := range hiddenFields {
		if h.feature == f {
			return [...]catalog.Tier{optional: catalog.Off, setting: catalog.Experimental, frozen: catalog.ReadOnly}[h.access]
		}
	}
	return catalog.Off
}

func shownTier(shown bool) catalog.Tier {
	if shown {
		return catalog.Untested
	}
	return catalog.Experimental
}

type Edit interface {
	apply(p *planner) error
}

type SetStages struct{ Count int }

type SetCurrent struct{ Stage int }

type SetDPI struct{ Stage, DPI int }

type SetKey struct {
	Slot int
	Fn   KeyFn
}

type SetShortcut struct {
	Slot  int
	Combo keys.Combo
}

type SetMedia struct {
	Slot  int
	Usage uint16
}

type SetMacro struct {
	Slot  int
	Macro Macro
	Cycle int
}

type SetSetting struct {
	Addr  int
	Value byte
}

const minDelay = 10

func Layout(m *catalog.Model) plan.Layout {
	l := plan.Layout{
		Bindings:  plan.Table{Base: AddrKeyFunction, Stride: recordSize, Count: Slots},
		Shortcuts: plan.Table{Base: AddrShortcutKey, Stride: ShortcutSize, Count: Slots},
		Macros:    plan.Table{Base: AddrMacro, Stride: MacroSize, Count: Slots},
		Frozen:    []flash.Extent{pairExtent(AddrKeyOperation)},
		Records:   []flash.Extent{pairExtent(AddrReportRate), pairExtent(AddrMaxDpiStage), pairExtent(AddrCurrentDPI)},
	}
	for i := range MaxStages {
		d, _ := DPIExtent(i)
		c, _ := ColorExtent(i)
		l.Records = append(l.Records, d, c)
	}
	for _, h := range hiddenFields {
		if h.access == setting {
			l.Records = append(l.Records, flash.Extent{Addr: h.addr, Len: h.size})
		}
	}
	if m != nil {
		for _, b := range m.Buttons {
			if b.Visible {
				l.Buttons = append(l.Buttons, b.Slot)
			}
		}
	}
	return l
}

type planner struct {
	m        *catalog.Model
	opt      Options
	cfg      Config
	dpi      []plan.Change
	dpiSet   [MaxStages]bool
	count    []plan.Change
	current  []plan.Change
	settings []plan.Change
	bindings []plan.Change
	macros   map[int]Macro
}

// PlanEdits turns edits into a validated plan against im. Record ops come out in a safe
// order: DPI values, then stage count and current stage, then hidden settings.
func PlanEdits(m *catalog.Model, im *flash.Image, edits []Edit, opt Options) (plan.Plan, error) {
	if m == nil || m.Family != catalog.FamilyMouse {
		return plan.Plan{}, newError(ErrRefused, "writes need a known mouse model")
	}
	dev := opt.Device
	if dev == (plan.Identity{}) {
		return plan.Plan{}, newError(plan.ErrDevice, "no device identity for "+m.Key)
	}
	if r, ok := catalog.Resolve(dev.CID, dev.MID); !ok || r != m {
		return plan.Plan{}, newError(plan.ErrDevice, "device "+dev.Key()+" is not model "+m.Key)
	}
	if im == nil {
		im = flash.New()
	}
	p := &planner{m: m, opt: opt, cfg: Decode(m, im), macros: map[int]Macro{}}
	for i, e := range edits {
		if e == nil {
			return plan.Plan{}, &editError{i, newError(ErrValue, "nil edit")}
		}
		if err := e.apply(p); err != nil {
			return plan.Plan{}, &editError{i, err}
		}
	}
	stages, err := p.stageChanges()
	if err != nil {
		return plan.Plan{}, err
	}
	if err := p.checkMacroNames(); err != nil {
		return plan.Plan{}, err
	}
	return plan.New(dev, opt.Profile, im, Layout(m), slices.Concat(p.dpi, stages, p.settings, p.bindings))
}

type editError struct {
	index int
	err   error
}

func (e *editError) Error() string { return "edit " + strconv.Itoa(e.index) + ": " + e.err.Error() }

func (e *editError) Unwrap() error { return e.err }

func (p *planner) tier(fs ...Feature) (catalog.Tier, error) {
	t := catalog.Verified
	for _, f := range fs {
		ft, ok := f.Tier(p.m, p.opt)
		if !ok {
			why := string(f) + " is " + ft.String() + " on " + p.m.Key
			if ft == catalog.Experimental {
				why += " and has no recorded hardware test for firmware " + strconv.Quote(p.opt.Firmware)
			}
			return 0, newError(ErrRefused, why)
		}
		t = min(t, ft)
	}
	return t, nil
}

func (p *planner) slotTier(slot int, fs ...Feature) (catalog.Tier, error) {
	return p.tier(append(fs, SlotFeatures(p.m, slot)...)...)
}

// mappedSlots are the slots H3 covers, 0 to 5, which every model's web app
// shows; a slot past them needs the physical map of H3b first (§3.1).
const mappedSlots = 6

// SlotFeatures are the features that writing slot k of m needs besides
// those of what it writes there: button.hidden-slot for a slot the web app
// does not show, button.unmapped-slot for one it shows past the six H3
// covers (12 and 13 on mid 6).
func SlotFeatures(m *catalog.Model, slot int) []Feature {
	switch {
	case !slices.ContainsFunc(m.Buttons, func(b catalog.Button) bool { return b.Slot == slot && b.Visible }):
		return []Feature{FeatureHiddenSlot}
	case slot >= mappedSlots:
		return []Feature{FeatureUnmappedSlot}
	}
	return nil
}

func (p *planner) record(to *[]plan.Change, f Feature, addr int, b []byte, desc string) error {
	t, err := p.tier(f)
	if err != nil {
		return err
	}
	*to = append(*to, plan.Change{Addr: addr, New: b, Tier: t, Desc: desc})
	return nil
}

func (e SetDPI) apply(p *planner) error {
	ext, ok := DPIExtent(e.Stage)
	if !ok || e.Stage >= p.m.Stages {
		return newError(ErrValue, "DPI stage "+strconv.Itoa(e.Stage+1)+" is outside 1-"+strconv.Itoa(p.m.Stages))
	}
	if limit := maxDPI(p.m); e.DPI > limit {
		return newError(ErrValue, strconv.Itoa(e.DPI)+" DPI is above the "+strconv.Itoa(limit)+" DPI limit of "+p.m.Key)
	}
	r, err := EncodeDPI(p.m.Sensor, e.DPI)
	if err != nil {
		return err
	}
	p.dpiSet[e.Stage] = true
	return p.record(&p.dpi, FeatureDPI, ext.Addr, r, "DPI stage "+strconv.Itoa(e.Stage+1)+": "+strconv.Itoa(e.DPI))
}

func maxDPI(m *catalog.Model) int {
	limit := 0
	if m.Sensor != nil && len(m.Sensor.Ranges) > 0 {
		limit = m.Sensor.Ranges[len(m.Sensor.Ranges)-1].Max
	}
	if m.MaxDPI > 0 && m.MaxDPI < limit {
		limit = m.MaxDPI
	}
	return limit
}

func (e SetStages) apply(p *planner) error {
	return p.stagePair(&p.count, FeatureStages, AddrMaxDpiStage, e.Count, e.Count-1, "stage count: ", EncodeStageCount)
}

func (e SetCurrent) apply(p *planner) error {
	return p.stagePair(&p.current, FeatureCurrent, AddrCurrentDPI, e.Stage, e.Stage, "current stage: ", EncodeCurrentStage)
}

func (p *planner) stagePair(to *[]plan.Change, f Feature, addr, v, index int, desc string, encode func(int) (flash.Pair, error)) error {
	if len(*to) > 0 {
		return newError(ErrValue, desc+"set twice")
	}
	if index >= p.m.Stages {
		return newError(ErrValue, desc+strconv.Itoa(index+1)+" is past the "+strconv.Itoa(p.m.Stages)+" stages of "+p.m.Key)
	}
	pr, err := encode(v)
	if err != nil {
		return err
	}
	return p.record(to, f, addr, pr[:], desc+strconv.Itoa(index+1))
}

func (p *planner) stageChanges() ([]plan.Change, error) {
	if len(p.count) == 0 && len(p.current) == 0 {
		return nil, nil
	}
	oldCount, errCount := known(p.cfg.Stages)
	oldCur, errCur := known(p.cfg.Current)
	newCount, newCur := oldCount, oldCur
	switch {
	case len(p.count) > 0:
		newCount = int(p.count[0].New[0])
	case errCount != nil:
		return nil, errCount
	}
	switch {
	case len(p.current) > 0:
		newCur = int(p.current[0].New[0])
	case errCur != nil:
		return nil, errCur
	}
	if newCur >= newCount {
		return nil, newError(ErrValue, "current stage "+strconv.Itoa(newCur+1)+" is past the stage count "+strconv.Itoa(newCount))
	}
	for i := range newCount {
		if p.dpiSet[i] || (errCount == nil && i < oldCount) {
			continue
		}
		if f := p.cfg.DPI[i].DPIField; f.State != flash.OK {
			if f.State == flash.Unknown {
				return nil, newError(plan.ErrUnread, "DPI stage "+strconv.Itoa(i+1)+" at "+f.Extent.String())
			}
			return nil, newError(ErrInvalid, "DPI stage "+strconv.Itoa(i+1)+" would become active but is "+f.State.String())
		}
	}
	if between(newCount, oldCur, errCur) >= between(oldCount, newCur, errCount) {
		return slices.Concat(p.count, p.current), nil
	}
	return slices.Concat(p.current, p.count), nil
}

// between rates the device state after the first of the two stage writes: 2 when
// current < count, 1 when the pair left untouched is invalid, 0 when current >= count.
func between(count, current int, untouched error) int {
	switch {
	case untouched != nil:
		return 1
	case current < count:
		return 2
	}
	return 0
}

func known(f Field) (int, error) {
	switch f.State {
	case flash.OK:
		return f.Value, nil
	case flash.Unknown:
		return 0, newError(plan.ErrUnread, f.Name+" at "+f.Extent.String())
	}
	return 0, newError(ErrInvalid, f.Name+" at "+f.Extent.String()+" is "+f.State.String())
}

func (e SetKey) apply(p *planner) error {
	if e.Fn.Type == TypeDPILock {
		if d, ok := e.Fn.LockedDPI(p.m.Sensor); !ok || d > maxDPI(p.m) {
			return newError(ErrValue, "DPI lock raw "+hex16(e.Fn.Param)+" is not a DPI of "+p.m.Key)
		}
	}
	fs := []Feature{typeFeature(e.Fn.Type)}
	if foreignMacro(e.Slot, e.Fn) {
		fs = append(fs, FeatureMacroForeign)
	}
	return p.bind(e.Slot, e.Fn, fs...)
}

func (p *planner) bind(slot int, fn KeyFn, fs ...Feature) error {
	ext, ok := KeyFnExtent(slot)
	if !ok {
		return badSlot(slot)
	}
	r, err := EncodeKeyFn(fn)
	if err != nil {
		return err
	}
	t, err := p.slotTier(slot, fs...)
	if err != nil {
		return err
	}
	p.bindings = append(p.bindings, plan.Change{Addr: ext.Addr, New: r, Tier: t, Desc: bindingDesc(slot, fn)})
	return nil
}

func (p *planner) body(slot int, table func(int) (flash.Extent, bool), body []byte, fn KeyFn, desc string, fs ...Feature) error {
	ext, ok := table(slot)
	if !ok {
		return badSlot(slot)
	}
	t, err := p.slotTier(slot, fs...)
	if err != nil {
		return err
	}
	p.bindings = append(p.bindings, plan.Change{Addr: ext.Addr, New: body, Tier: t, Desc: desc})
	return p.bind(slot, fn, fs...)
}

func badSlot(slot int) error {
	return newError(ErrValue, "slot "+strconv.Itoa(slot)+" is outside 0-"+strconv.Itoa(Slots-1))
}

func typeFeature(t KeyType) Feature {
	switch t {
	case TypeFire:
		return FeatureFireKey
	case TypeShortcut:
		return FeatureShortcut
	case TypeMacro:
		return FeatureMacro
	case TypeRateSwitch:
		return FeatureRateSwitch
	case TypeDragScroll:
		return FeatureDragScroll
	case TypeProfile:
		return FeatureProfileSwitch
	case TypeDPILock:
		return FeatureDPILock
	}
	return FeatureSystem
}

func (e SetShortcut) apply(p *planner) error {
	body, err := EncodeShortcut(e.Combo)
	if err != nil {
		return err
	}
	for i, s := range e.Combo {
		switch {
		case s.Kind == keys.KindModifier && bits.OnesCount16(s.Value) != 1:
			return newError(ErrValue, "shortcut stroke "+strconv.Itoa(i)+" holds several modifiers")
		case slices.Contains(e.Combo[:i], s):
			return newError(ErrValue, "shortcut stroke "+strconv.Itoa(i)+" repeats an earlier stroke")
		}
	}
	return p.body(e.Slot, ShortcutExtent, body, KeyFn{Type: TypeShortcut}, "shortcut "+strconv.Itoa(e.Slot), comboFeatures(e.Combo)...)
}

func (e SetMedia) apply(p *planner) error {
	body, err := EncodeMedia(e.Usage)
	if err != nil {
		return err
	}
	s := keys.Stroke{Kind: keys.KindConsumer, Value: e.Usage}
	desc := "media " + strconv.Itoa(e.Slot) + ": " + s.Name(keys.Win)
	return p.body(e.Slot, ShortcutExtent, body, KeyFn{Type: TypeShortcut}, desc, comboFeatures(keys.Combo{s})...)
}

var reasonFeature = map[Reason]Feature{
	RightModifier: FeatureShortcutRightModifier,
	MenuKey:       FeatureShortcutMenu,
	CustomCombo:   FeatureShortcutCustom,
	MediaCode:     FeatureMediaCustom,
}

// comboFeatures lists the features a shortcut body needs: a shape WebCompat warns
// about adds its own feature, so a body the web app cannot build never shares the tier
// of the ones it can.
func comboFeatures(c keys.Combo) []Feature {
	fs := []Feature{FeatureShortcut}
	if mediaCombo(c) {
		fs[0] = FeatureMedia
	}
	for _, r := range comboReasons(c) {
		fs = append(fs, reasonFeature[r])
	}
	return fs
}

func (e SetMacro) apply(p *planner) error {
	for i, ev := range e.Macro.Events {
		if ev.Delay < minDelay {
			return newError(ErrValue, "macro event "+strconv.Itoa(i)+" delay "+strconv.Itoa(int(ev.Delay))+" ms is under 10 ms")
		}
	}
	body, err := EncodeMacro(e.Macro)
	if err != nil {
		return err
	}
	fn, err := MacroBinding(e.Slot, e.Cycle)
	if err != nil {
		return err
	}
	if err := p.body(e.Slot, MacroExtent, body, fn, "macro "+strconv.Itoa(e.Slot)+": "+strconv.Quote(e.Macro.Name), FeatureMacro); err != nil {
		return err
	}
	p.macros[e.Slot] = e.Macro
	return nil
}

func (p *planner) checkMacroNames() error {
	final := p.cfg.Macros
	for k, m := range p.macros {
		final[k] = &m
	}
	for k := range Slots {
		m, ok := p.macros[k]
		if !ok {
			continue
		}
		for j, o := range final {
			if j != k && o != nil && o.Name == m.Name && !slices.Equal(o.Events, m.Events) {
				return newError(ErrValue, "macro name "+strconv.Quote(m.Name)+" in slot "+strconv.Itoa(k)+
					" is already used by a different macro in slot "+strconv.Itoa(j))
			}
		}
	}
	return nil
}

func (e SetSetting) apply(p *planner) error {
	i := slices.IndexFunc(hiddenFields[:], func(h hiddenSpec) bool { return h.addr == e.Addr && h.size == pairSize })
	if i < 0 {
		return newError(ErrValue, "no setting pair at "+strconv.Itoa(e.Addr))
	}
	h := hiddenFields[i]
	if h.domain != nil && !h.domain(p.m, e.Value) {
		return newError(ErrValue, strconv.Itoa(int(e.Value))+" is not a value of "+h.name+" on "+p.m.Key)
	}
	pr := flash.NewPair(e.Value)
	return p.record(&p.settings, h.feature, e.Addr, pr[:], h.name+": "+strconv.Itoa(int(e.Value)))
}

var keyFnNames = map[KeyFn]string{
	{TypeDisable, 0}:                  "disable",
	{TypeMouse, ParamLeft}:            "Left Click",
	{TypeMouse, ParamRight}:           "Right Click",
	{TypeMouse, ParamMiddle}:          "Wheel Click",
	{TypeMouse, ParamBack}:            "Back",
	{TypeMouse, ParamForward}:         "Forward",
	{TypeDPI, ParamDPICycle}:          "DPI cycle",
	{TypeDPI, ParamDPIUp}:             "DPI up",
	{TypeDPI, ParamDPIDown}:           "DPI down",
	{TypeScroll, ParamScrollLeft}:     "scroll left",
	{TypeScroll, ParamScrollRight}:    "scroll right",
	{TypeWheel, ParamScrollUp}:        "scroll up",
	{TypeWheel, ParamScrollDown}:      "scroll down",
	{TypeShortcut, 0}:                 "shortcut",
	{TypeRateSwitch, 0}:               "rate switch",
	{TypeProfile, 0}:                  "profile switch",
	{TypeDragScroll, ParamDragScroll}: "drag scroll",
}

func bindingDesc(slot int, fn KeyFn) string {
	name, ok := keyFnNames[fn]
	switch {
	case ok:
	case fn.Type == TypeMacro:
		name = "macro " + strconv.Itoa(int(fn.Param>>8)) + ", cycle " + strconv.Itoa(int(fn.Param&0xFF))
	default:
		name = fn.Type.String() + " " + hex16(fn.Param)
	}
	return "slot " + strconv.Itoa(slot) + ": " + name
}
