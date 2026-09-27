package backup

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
)

// Why a restore does not go ahead as asked.
var (
	ErrNotSource    = errors.New("backup: not a restore source")
	ErrOtherModel   = errors.New("backup: the source is for another model")
	ErrOtherDevice  = errors.New("backup: the source is from another device")
	ErrOtherProfile = errors.New("backup: the source is from another onboard profile")
)

// Target is the mouse a restore writes to, as the session loaded it.
type Target struct {
	Model *catalog.Model
	// Image is what the mouse holds: the loaded image, plus every extent
	// RestoreReads lists.
	Image *flash.Image
	// Options are the identity, the onboard profile (nil without profiles),
	// the firmware and the verified stages, as mouse.PlanEdits takes them.
	Options mouse.Options
	// Conn is the handshake's connection type; nil when unknown.
	Conn *byte
	// Source is where the mouse comes from: SourceDevice for a real one.
	Source string
}

type RestoreOptions struct {
	// IncludeUnknown (--include-unknown) also writes back, as the source
	// captured them, the records arcctl knows no valid value for where
	// plan.Layout.Capturable allows, but no hidden setting before its H8
	// test is recorded (D5).
	IncludeUnknown bool
	OtherDevice    bool // --other-device
	OtherProfile   bool // --other-profile
}

// Fate is what a restore does with a record the source and the mouse hold
// differently.
type Fate uint8

const (
	FateWrite       Fate = iota + 1 // written by the plan
	FateUnknown                     // unmapped, or invalid in the source: only --include-unknown writes it
	FateNotCaptured                 // the source did not capture it; never written
	FateRefused                     // arcctl does not write it here
	FateUnread                      // the mouse's bytes could not be read
)

var fateNames = [...]string{FateWrite: "write", FateUnknown: "unknown", FateNotCaptured: "not captured",
	FateRefused: "not written", FateUnread: "not read"}

func (f Fate) String() string {
	if int(f) < len(fateNames) && fateNames[f] != "" {
		return fateNames[f]
	}
	return "Fate(" + strconv.Itoa(int(f)) + ")"
}

// RestoreRecord is one record the source and the mouse hold differently.
type RestoreRecord struct {
	Name   string
	Extent flash.Extent
	Device []byte // nil when the mouse's bytes are not known
	Source []byte // nil when the source did not capture them
	Fate   Fate
	Why    string
	Tier   catalog.Tier // of a write
	// Eligible marks a record --include-unknown writes back as the source
	// captured it, and Captured one it writes that way: a FateWrite at the
	// experimental tier.
	Eligible, Captured bool
}

// Restore is a restore laid out: the plan that writes the records it can,
// and every record that differs with what happens to it.
type Restore struct {
	Plan    plan.Plan
	Records []RestoreRecord
	// Equal counts the records the mouse already holds as the source does,
	// and Uncaptured those the source never captured, which are left alone.
	Equal      int
	Uncaptured int
	Clamps     []Clamp
	// Notes say what the options allowed.
	Notes []string
}

// Captured lists the records written back as the source captured them.
func (r *Restore) Captured() []RestoreRecord {
	var out []RestoreRecord
	for _, x := range r.Records {
		if x.Captured {
			out = append(out, x)
		}
	}
	return out
}

// Count is the number of records with fate f.
func (r *Restore) Count(f Fate) int {
	n := 0
	for _, x := range r.Records {
		if x.Fate == f {
			n++
		}
	}
	return n
}

// CheckSource says whether src may be restored to t (I11): the same device
// and onboard profile unless o allows another, and another device only of
// the same model and sensor. Two addresses that differ mean another device
// even while the identity key leaves them out (untrusted until H0). A web .bin records neither, so it needs
// OtherDevice, and OtherProfile on a mouse with profiles. A backup the
// emulator or a replay made never goes to a real mouse. The notes say what
// the options allowed.
func CheckSource(src *Source, t Target, o RestoreOptions) ([]string, error) {
	m := t.Model
	if m == nil || m.Family != catalog.FamilyMouse || m.Sensor == nil {
		return nil, fmt.Errorf("%w: arcctl restores only to a known mouse", ErrOtherModel)
	}
	id := t.Options.Device.Key()
	var notes []string
	var bp *byte
	profileKnown := false
	switch src.Kind {
	case KindBackup:
		f := src.File
		if f.Source != SourceDevice && t.Source == SourceDevice {
			return nil, fmt.Errorf("%w: it was made by the %s, not read from a mouse", ErrNotSource, f.Source)
		}
		if f.Model == nil || f.Model.Key != m.Key || f.Model.Sensor != m.Sensor.ID {
			return nil, fmt.Errorf("%w: the backup is of %s; the mouse is %s", ErrOtherModel, backupModel(f.Model), modelText(m))
		}
		switch a, b := f.Identity().Addr, t.Options.Device.Addr; {
		case f.Key() != id:
			if !o.OtherDevice {
				return nil, fmt.Errorf("%w: the backup is of %s, the mouse answering is %s", ErrOtherDevice, f.Key(), id)
			}
			notes = append(notes, "The backup is of another "+m.Name+" ("+f.Key()+"); --other-device allows it.")
		case a != [3]byte{} && b != [3]byte{} && a != b:
			if !o.OtherDevice {
				return nil, fmt.Errorf("%w: the backup was read from a %s with another address than the mouse answering", ErrOtherDevice, m.Name)
			}
			notes = append(notes, "The backup was read from another "+m.Name+" (its address differs); --other-device allows it.")
		}
		if p := f.Device.Profile; p != nil {
			profileKnown = true
			if p.Supported {
				v := p.Value
				bp = &v
			}
		}
	case KindBin:
		if src.Bin.Sensor != m.Sensor.ID {
			return nil, fmt.Errorf("%w: the .bin is for sensor %s; the mouse is %s", ErrOtherModel, src.Bin.Sensor, modelText(m))
		}
		if !o.OtherDevice {
			return nil, fmt.Errorf("%w: a web .bin does not record which mouse it came from", ErrOtherDevice)
		}
		notes = append(notes, "A web .bin does not record which mouse it came from; --other-device allows it.")
	default:
		return nil, fmt.Errorf("%w: a raw dump records no device; restore from a backup or a web .bin", ErrNotSource)
	}
	switch tp := t.Options.Profile; {
	case profileKnown && sameProfile(bp, tp), !profileKnown && tp == nil:
	case !o.OtherProfile:
		return nil, fmt.Errorf("%w: the source was read on %s, the mouse is on %s", ErrOtherProfile, profileName(bp, profileKnown), profileName(tp, true))
	default:
		notes = append(notes, "The source was read on "+profileName(bp, profileKnown)+", the mouse is on "+profileName(tp, true)+"; --other-profile allows it.")
	}
	return notes, nil
}

func backupModel(m *Model) string {
	if m == nil {
		return "an unknown model"
	}
	return m.Name + " (" + m.Key + ", sensor " + m.Sensor + ")"
}

func modelText(m *catalog.Model) string {
	return m.Name + " (" + m.Key + ", sensor " + m.Sensor.ID + ")"
}

func sameProfile(a, b *byte) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func profileName(p *byte, known bool) string {
	switch {
	case !known:
		return "an unknown onboard profile"
	case p == nil:
		return "no onboard profiles"
	}
	return "onboard profile " + strconv.Itoa(int(*p))
}

// Clamp is a value of a web .bin the mouse cannot take, brought into its
// range with its complement recomputed.
type Clamp struct {
	Name     string
	Extent   flash.Extent
	From, To []byte
}

// ClampBin brings into m's range, in im, the pairs the web app's import
// clamps: the stage count to m's stages, the current stage to the last of
// them and, when the connection type conn is known, the report rate to what
// the connection carries. A clamped pair gets its complement recomputed; a
// pair that does not decode is left as it is.
func ClampBin(m *catalog.Model, conn *byte, im *flash.Image) []Clamp {
	var out []Clamp
	clamp := func(name string, addr int, decode func(flash.Pair) (int, error), limit int, encode func(int) (flash.Pair, error)) {
		e := flash.Extent{Addr: addr, Len: 2}
		b, ok := im.Get(e)
		if !ok {
			return
		}
		v, err := decode(flash.Pair(b))
		if err != nil || v <= limit {
			return
		}
		p, err := encode(limit)
		if err != nil {
			return
		}
		_ = im.Set(addr, p[:])
		out = append(out, Clamp{Name: name, Extent: e, From: b, To: p[:]})
	}
	if conn != nil {
		if hz, ok := mouse.MaxRate(*conn); ok {
			clamp("Report rate", mouse.AddrReportRate, mouse.DecodeRate, hz, mouse.EncodeRate)
		}
	}
	if m != nil && m.Stages > 0 {
		clamp("DPI stages", mouse.AddrMaxDpiStage, mouse.DecodeStageCount, m.Stages, mouse.EncodeStageCount)
		clamp("Current stage", mouse.AddrCurrentDPI, mouse.DecodeCurrentStage, m.Stages-1, mouse.EncodeCurrentStage)
	}
	return out
}

// RestoreImage is what a restore of src to t compares with the mouse: the
// source's captured bytes and, for a web .bin, its clamps.
func RestoreImage(src *Source, t Target) (*flash.Image, []Clamp) {
	im := src.Image.Clone()
	if src.Kind != KindBin {
		return im, nil
	}
	return im, ClampBin(t.Model, t.Conn, im)
}

// bodyTable is the shortcut or the macro table, as the restore walks it.
type bodyTable struct {
	name   string
	extent func(int) (flash.Extent, bool)
	head   int // bytes before the event count
	event  int // bytes per event
	macro  bool
}

var bodyTables = [...]bodyTable{
	{name: "Shortcut", extent: mouse.ShortcutExtent, head: 0, event: 3},
	{name: "Macro", extent: mouse.MacroExtent, head: mouse.MaxNameLen + 1, event: 5, macro: true},
}

// declared is the length of the record a header count n declares in a slot
// of stride bytes; 0 when n is not a count.
func (t bodyTable) declared(n byte, stride int) int {
	size := t.head + 2 + t.event*int(n)
	switch {
	case n == 0, size > stride:
		return 0
	case !t.macro && (n%2 != 0 || int(n) > 2*mouse.MaxShortcutKeys):
		return 0
	case t.macro && int(n) > mouse.MaxMacroEvents:
		return 0
	}
	return size
}

func (t bodyTable) decode(b []byte) flash.SlotClass {
	if t.macro {
		_, err := mouse.DecodeMacro(b)
		return mouse.Class(err)
	}
	_, err := mouse.DecodeShortcut(b)
	return mouse.Class(err)
}

// RestoreReads lists what dev must hold before a restore from src can be
// planned: every settings record src captured, and for each body src
// captured, the mouse's header, then what the restore would write there.
// Read them and call it again until it returns nothing.
func RestoreReads(src, dev *flash.Image) []flash.Extent {
	var out []flash.Extent
	need := func(e flash.Extent) {
		if e.Len > 0 && !dev.Known(e) {
			out = append(out, e)
		}
	}
	for _, e := range SettingsRecords() {
		if src.Known(e) {
			need(e)
		}
	}
	for _, t := range bodyTables {
		for k := range mouse.Slots {
			slot, _ := t.extent(k)
			if knownLen(src, slot) <= t.head {
				continue
			}
			if !dev.Known(flash.Extent{Addr: slot.Addr, Len: t.head + 1}) {
				need(flash.Extent{Addr: slot.Addr, Len: t.head + 1})
				continue
			}
			need(t.write(src, dev, slot))
		}
	}
	return out
}

// write is the extent a restore compares and writes in slot: the record src
// declares or, over an empty src slot, the record dev declares, stretched
// to the longer of the two where src knows the bytes, so that no tail of
// the mouse's record outlives the restore.
func (t bodyTable) write(src, dev *flash.Image, slot flash.Extent) flash.Extent {
	ls := knownLen(src, slot)
	sn, _ := src.Byte(slot.Addr + t.head)
	dn, _ := dev.Byte(slot.Addr + t.head)
	n := max(t.declared(sn, slot.Len), t.declared(dn, slot.Len), t.head+1)
	return flash.Extent{Addr: slot.Addr, Len: min(n, ls)}
}

// knownLen is how many bytes from the start of e im knows in a row.
func knownLen(im *flash.Image, e flash.Extent) int {
	n := 0
	for n < e.Len {
		if _, ok := im.Byte(e.Addr + n); !ok {
			break
		}
		n++
	}
	return n
}

// RestoreLeavesOut names what a restore to m never writes back, whatever
// the backup holds, with the records opt verifies: the records of m's
// layout no feature writes here, the button slots whose tier is closed, and
// the bytes arcctl knows no field for. A factory reset that changes them
// leaves them changed.
func RestoreLeavesOut(m *catalog.Model, opt mouse.Options) []string {
	out := []string{"the report rate", "the DPI colours"}
	if m.Stages < mouse.MaxStages {
		out = append(out, fmt.Sprintf("DPI stages %d to %d", m.Stages+1, mouse.MaxStages))
	}
	var slots []int
	for k := range mouse.Slots {
		for _, f := range append([]mouse.Feature{mouse.FeatureSystem}, mouse.SlotFeatures(m, k)...) {
			if _, ok := f.Tier(m, opt); !ok {
				slots = append(slots, k)
				break
			}
		}
	}
	if len(slots) > 0 {
		out = append(out, "button slots "+numberRuns(slots))
	}
	l := mouse.Layout(m)
	for _, f := range mouse.Decode(m, flash.New()).Hidden {
		switch fe, known := mouse.FieldFeature(f.Extent.Addr); {
		case slices.Contains(l.Frozen, f.Extent):
			out = append(out, f.Name)
		case known && slices.Contains(l.Records, f.Extent):
			if _, ok := fe.Tier(m, opt); !ok {
				out = append(out, f.Name)
			}
		}
	}
	return append(out, "the bytes arcctl knows no field for")
}

// numberRuns writes ns, ascending, with runs of three or more as ranges.
func numberRuns(ns []int) string {
	var parts []string
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		switch {
		case j-i >= 2:
			parts = append(parts, strconv.Itoa(ns[i])+" to "+strconv.Itoa(ns[j]))
		default:
			for k := i; k <= j; k++ {
				parts = append(parts, strconv.Itoa(ns[k]))
			}
		}
		i = j + 1
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// PlanRestore lays out a restore of src to t: the records the source and
// the mouse hold differently, what happens to each, and the plan that
// writes those it can (plan.New, validated). It never writes a byte the
// source did not capture and never splits a record: see Fate. An unknown or
// invalid record goes back only with o.IncludeUnknown, as its captured
// bytes, and only where plan.Layout.Capturable allows. t.Image must hold
// what RestoreReads lists.
func PlanRestore(src *Source, t Target, o RestoreOptions) (*Restore, error) {
	notes, err := CheckSource(src, t, o)
	if err != nil {
		return nil, err
	}
	if t.Image == nil {
		return nil, fmt.Errorf("%w: nothing is loaded from the mouse", plan.ErrUnread)
	}
	im, clamps := RestoreImage(src, t)
	r := &restorer{t: t, m: t.Model, src: im, dev: t.Image, o: o, bin: src.Kind == KindBin, layout: mouse.Layout(t.Model)}
	r.out = &Restore{Clamps: clamps, Notes: notes}
	r.settings()
	r.bodies()
	r.settle()
	p, err := plan.New(t.Options.Device, t.Options.Profile, t.Image, r.layout, r.changes())
	if err != nil {
		return nil, err
	}
	r.out.Plan = p
	for _, c := range r.cands {
		r.out.Records = append(r.out.Records, c.rec)
	}
	slices.SortStableFunc(r.out.Records, func(a, b RestoreRecord) int { return cmp.Compare(a.Extent.Addr, b.Extent.Addr) })
	return r.out, nil
}

type recordKind uint8

const (
	kindRate recordKind = iota + 1
	kindStages
	kindCurrent
	kindDPI
	kindColor
	kindBinding
	kindSetting
	kindFrozen
	kindUnmapped
	kindOptional
	kindShortcut
	kindMacro
)

type candidate struct {
	rec    RestoreRecord
	kind   recordKind
	index  int // the stage or slot
	change plan.Change
}

type restorer struct {
	t      Target
	m      *catalog.Model
	src    *flash.Image
	dev    *flash.Image
	o      RestoreOptions
	bin    bool
	layout plan.Layout
	out    *Restore
	cands  []*candidate
}

// settings compares the records of the settings page and the extended
// block.
func (r *restorer) settings() {
	sc := mouse.Decode(r.m, r.src)
	r.field("Report rate", sc.Rate, kindRate, 0)
	r.field("DPI stages", sc.Stages, kindStages, 0)
	r.field("Current stage", sc.Current, kindCurrent, 0)
	for i, s := range sc.DPI {
		r.field("DPI stage "+strconv.Itoa(i+1), s.DPIField, kindDPI, i)
		r.field("Colour "+strconv.Itoa(i+1), s.ColorField, kindColor, i)
	}
	for k, b := range sc.Keys {
		r.field("Button slot "+strconv.Itoa(k), b.Field, kindBinding, k)
	}
	var covered []flash.Extent
	for _, f := range sc.Hidden {
		kind := kindOptional
		switch {
		case slices.Contains(r.layout.Frozen, f.Extent):
			kind = kindFrozen
		case slices.Contains(r.layout.Records, f.Extent):
			kind = kindSetting
		case f.Name == "unmapped":
			kind = kindUnmapped
		}
		covered = append(covered, f.Extent)
		r.field(fmt.Sprintf("%s @%d", f.Name, f.Extent.Addr), f, kind, 0)
	}
	for _, e := range SettingsRecords() {
		if e.Addr >= mouse.AddrSensor3955DPI && !slices.Contains(covered, e) {
			f := mouse.Field{Name: "unmapped", Extent: e}
			f.Raw, _ = r.src.Get(e)
			r.field("unmapped "+e.String(), f, kindUnmapped, 0)
		}
	}
}

// field compares one settings record and decides its fate.
func (r *restorer) field(name string, f mouse.Field, kind recordKind, index int) {
	e := f.Extent
	c := &candidate{rec: RestoreRecord{Name: name, Extent: e}, kind: kind, index: index}
	c.rec.Device, _ = r.dev.Get(e)
	sb, sok := r.src.Get(e)
	if !sok {
		if knownLen(r.src, e) == 0 && !(r.bin && c.rec.Device != nil && !allFF(c.rec.Device)) {
			r.out.Uncaptured++
			return
		}
		c.rec.Fate, c.rec.Why = FateNotCaptured, r.uncapturedWhy()
		r.cands = append(r.cands, c)
		return
	}
	c.rec.Source = sb
	switch {
	case c.rec.Device == nil:
		c.rec.Fate, c.rec.Why = FateUnread, "the mouse's bytes could not be read"
	case bytes.Equal(sb, c.rec.Device):
		r.out.Equal++
		return
	default:
		r.decideField(c, f)
		r.capture(c)
	}
	r.cands = append(r.cands, c)
}

func (r *restorer) uncapturedWhy() string {
	if r.bin {
		return "the .bin holds 0xFF here, which arcctl treats as not captured"
	}
	return "the backup could not read all of it"
}

func (r *restorer) decideField(c *candidate, f mouse.Field) {
	m := r.m
	refuse := func(why string) { c.rec.Fate, c.rec.Why = FateRefused, why }
	unknown := func(why string) {
		c.rec.Fate, c.rec.Why, c.rec.Eligible = FateUnknown, why, r.layout.Capturable(c.rec.Extent)
	}
	invalid := func() bool {
		if f.State == flash.OK {
			return false
		}
		unknown("the source holds " + f.State.String() + " bytes; the planner writes only valid values")
		return true
	}
	switch c.kind {
	case kindRate:
		if !invalid() {
			refuse("no feature writes the report rate in this build")
		}
	case kindColor:
		if !invalid() {
			refuse("no feature writes the DPI colours in this build")
		}
	case kindFrozen:
		refuse("arcctl never writes " + f.Name + " (D5)")
	case kindOptional:
		if !r.layout.Capturable(c.rec.Extent) {
			refuse(f.Name + " is not a setting of " + m.Key + " that arcctl writes")
			return
		}
		unknown(f.Name + " is not a field of " + m.Key + "; arcctl knows nothing of these bytes here")
	case kindUnmapped:
		unknown("unmapped: no field of " + m.Key + " is known here")
		if !c.rec.Eligible {
			c.rec.Why += "; arcctl writes nothing of it outside the settings page"
		}
	case kindStages:
		if invalid() {
			return
		}
		if f.Value > m.Stages {
			refuse(fmt.Sprintf("%d stages is past the %d of %s", f.Value, m.Stages, m.Key))
			return
		}
		r.write(c, "restore the stage count: "+strconv.Itoa(f.Value), mouse.FeatureStages)
	case kindCurrent:
		if invalid() {
			return
		}
		if f.Value >= m.Stages {
			refuse(fmt.Sprintf("stage %d is past the %d of %s", f.Value+1, m.Stages, m.Key))
			return
		}
		r.write(c, "restore the current stage: "+strconv.Itoa(f.Value+1), mouse.FeatureCurrent)
	case kindDPI:
		if invalid() {
			return
		}
		if c.index >= m.Stages {
			refuse(fmt.Sprintf("stage %d is past the %d stages of %s; only a hardware test writes it", c.index+1, m.Stages, m.Key))
			return
		}
		d, _ := mouse.DecodeDPI(m.Sensor, c.rec.Source)
		if limit := maxDPI(m); d.X > limit || d.Y > limit {
			refuse(fmt.Sprintf("above the %d DPI limit of %s", limit, m.Key))
			return
		}
		desc := fmt.Sprintf("restore DPI stage %d: %d", c.index+1, d.X)
		if d.Y != d.X {
			desc += fmt.Sprintf(" x %d", d.Y)
		}
		r.write(c, desc, mouse.FeatureDPI)
	case kindBinding:
		if invalid() {
			return
		}
		c.rec.Fate = FateWrite
		c.change = plan.Change{Addr: c.rec.Extent.Addr, New: c.rec.Source, Desc: "restore button slot " + strconv.Itoa(c.index)}
	case kindSetting:
		if invalid() {
			if fe, _ := mouse.FieldFeature(c.rec.Extent.Addr); !r.open(fe) {
				c.rec.Eligible = false
				c.rec.Why += "; D5 keeps " + string(fe) + " read-only until its own H8 test is recorded"
			}
			return
		}
		p, err := mouse.PlanEdits(m, r.dev, []mouse.Edit{mouse.SetSetting{Addr: c.rec.Extent.Addr, Value: byte(f.Value)}}, r.t.Options)
		switch {
		case err != nil:
			refuse(planText(err))
		case len(p.Ops) != 1 || !bytes.Equal(p.Ops[0].New, c.rec.Source):
			refuse("the planner does not write these bytes")
		default:
			t, why := r.tier()
			if why != "" {
				refuse(why)
				return
			}
			t = min(t, p.Ops[0].Tier)
			c.rec.Fate, c.rec.Tier = FateWrite, t
			c.change = plan.Change{Addr: c.rec.Extent.Addr, New: c.rec.Source, Tier: t, Desc: "restore " + p.Ops[0].Desc}
		}
	}
}

// capture makes an eligible record, when --include-unknown asks for it, a
// write of the source's bytes as they are, at the experimental tier.
func (r *restorer) capture(c *candidate) {
	if !c.rec.Eligible || !r.o.IncludeUnknown {
		return
	}
	if _, why := r.tier(); why != "" {
		c.rec.Fate, c.rec.Why = FateRefused, why
		return
	}
	c.rec.Fate, c.rec.Tier, c.rec.Captured = FateWrite, catalog.Experimental, true
	c.change = plan.Change{Addr: c.rec.Extent.Addr, New: c.rec.Source, Tier: catalog.Experimental, Captured: true,
		Desc: "write back the captured bytes of " + c.rec.Name}
}

// write makes c a write, described as desc, at the lowest tier of fs, or
// refuses it when one of them is not written on this mouse.
func (r *restorer) write(c *candidate, desc string, fs ...mouse.Feature) {
	t, why := r.tier(fs...)
	if why != "" {
		c.rec.Fate, c.rec.Why = FateRefused, why
		return
	}
	c.rec.Fate, c.rec.Tier, c.rec.Why = FateWrite, t, ""
	c.change = plan.Change{Addr: c.rec.Extent.Addr, New: c.rec.Source, Tier: t, Desc: desc}
}

// tier is the lowest tier of fs and of the restore itself on this mouse,
// or why one of them is not written, in the planner's words. The restore's
// own feature keeps every restored record below Verified until H6 records
// it (§3.3).
func (r *restorer) tier(fs ...mouse.Feature) (catalog.Tier, string) {
	t := catalog.Verified
	for _, f := range append(fs, mouse.FeatureRestore) {
		ft, ok := f.Tier(r.m, r.t.Options)
		if !ok {
			why := string(f) + " is " + ft.String() + " on " + r.m.Key
			if ft == catalog.Experimental {
				why += " and has no recorded hardware test for firmware " + strconv.Quote(r.t.Options.Firmware)
			}
			return 0, why
		}
		t = min(t, ft)
	}
	return t, ""
}

// open reports whether f's tier on this mouse lets a plan write it.
func (r *restorer) open(f mouse.Feature) bool {
	_, ok := f.Tier(r.m, r.t.Options)
	return ok
}

// bodies compares the shortcut and macro slots.
func (r *restorer) bodies() {
	for _, t := range bodyTables {
		for k := range mouse.Slots {
			r.body(t, k)
		}
	}
}

func (r *restorer) body(t bodyTable, k int) {
	slot, _ := t.extent(k)
	name := t.name + " " + strconv.Itoa(k)
	kind := kindShortcut
	if t.macro {
		kind = kindMacro
	}
	c := &candidate{rec: RestoreRecord{Name: name}, kind: kind, index: k}
	ls := knownLen(r.src, slot)
	if ls <= t.head {
		r.out.Uncaptured++
		return
	}
	prefix, _ := r.src.Get(flash.Extent{Addr: slot.Addr, Len: ls})
	class := t.decode(prefix)
	if _, ok := r.dev.Byte(slot.Addr + t.head); !ok {
		c.rec.Extent = flash.Extent{Addr: slot.Addr, Len: t.head + 1}
		c.rec.Source = prefix[:t.head+1]
		c.rec.Fate, c.rec.Why = FateUnread, "the mouse's slot could not be read"
		r.cands = append(r.cands, c)
		return
	}
	e := t.write(r.src, r.dev, slot)
	c.rec.Extent = e
	c.rec.Source = prefix[:e.Len]
	c.rec.Device, _ = r.dev.Get(e)
	switch {
	case class == flash.SlotUnknown:
		c.rec.Fate, c.rec.Why = FateNotCaptured, "the source holds only the start of its record"
	case c.rec.Device == nil:
		c.rec.Fate, c.rec.Why = FateUnread, "the mouse's slot could not be read"
	case bytes.Equal(c.rec.Source, c.rec.Device):
		r.out.Equal++
		return
	case class == flash.SlotEmpty && t.decode(c.rec.Device) == flash.SlotEmpty:
		r.out.Equal++
		return
	case class == flash.SlotInvalid:
		c.rec.Fate, c.rec.Why = FateUnknown, "invalid in the source; arcctl never writes an invalid body"
	default:
		fs := []mouse.Feature{mouse.FeatureShortcut}
		desc := "restore " + strings.ToLower(name)
		switch {
		case class == flash.SlotEmpty:
			desc += " (empty)"
			if t.macro {
				fs[0] = mouse.FeatureMacro
			}
		case t.macro:
			fs[0] = mouse.FeatureMacro
			if mac, err := mouse.DecodeMacro(prefix); err == nil {
				desc += " " + strconv.Quote(mac.Name)
			}
		default:
			fs, _ = comboFeatures(r.m, k, c.rec.Source)
		}
		r.write(c, desc, append(fs, mouse.SlotFeatures(r.m, k)...)...)
	}
	r.cands = append(r.cands, c)
}

var reasonFeature = map[mouse.Reason]mouse.Feature{
	mouse.RightModifier: mouse.FeatureShortcutRightModifier,
	mouse.MenuKey:       mouse.FeatureShortcutMenu,
	mouse.CustomCombo:   mouse.FeatureShortcutCustom,
	mouse.MediaCode:     mouse.FeatureMediaCustom,
}

// comboFeatures are the features writing the shortcut body b to slot k
// needs, by the same test the planner and the web-compat lint use.
func comboFeatures(m *catalog.Model, k int, b []byte) ([]mouse.Feature, bool) {
	c, err := mouse.DecodeShortcut(b)
	if err != nil {
		return nil, false
	}
	fs := []mouse.Feature{mouse.FeatureShortcut}
	if len(c) == 1 && c[0].Kind == keys.KindConsumer {
		fs[0] = mouse.FeatureMedia
	}
	e, _ := mouse.ShortcutExtent(k)
	trial := plan.Plan{Ops: []plan.Op{{Seq: 1, Extent: flash.Extent{Addr: e.Addr, Len: len(b)}, New: b}}}
	for _, w := range mouse.WebCompat(m, trial) {
		if f, ok := reasonFeature[w.Reason]; ok {
			fs = append(fs, f)
		}
	}
	return fs, true
}

func typeFeature(t mouse.KeyType) mouse.Feature {
	switch t {
	case mouse.TypeFire:
		return mouse.FeatureFireKey
	case mouse.TypeShortcut:
		return mouse.FeatureShortcut
	case mouse.TypeMacro:
		return mouse.FeatureMacro
	case mouse.TypeRateSwitch:
		return mouse.FeatureRateSwitch
	case mouse.TypeDragScroll:
		return mouse.FeatureDragScroll
	case mouse.TypeProfile:
		return mouse.FeatureProfileSwitch
	case mouse.TypeDPILock:
		return mouse.FeatureDPILock
	}
	return mouse.FeatureSystem
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

// settle refuses the writes that would leave the mouse in a state the
// planner refuses: a binding whose body is not valid afterwards, a body
// emptied while a binding still runs it, no Left Click on a physical
// button, or a current stage past the stage count, after every write or
// before the captured bytes, which go last. Each refusal changes what the
// others see, so it goes on until nothing changes.
func (r *restorer) settle() {
	for range len(r.cands) + 1 {
		final := r.after(true)
		changed := false
		for _, c := range r.cands {
			if c.rec.Fate != FateWrite {
				continue
			}
			var why string
			switch c.kind {
			case kindBinding:
				why = r.binding(c, final)
			case kindShortcut, kindMacro:
				why = r.bodyUsers(c, final)
			}
			if why != "" {
				c.rec.Fate, c.rec.Why = FateRefused, why
				changed = true
			}
		}
		if !changed {
			changed = r.leftClick(final) || r.stages(final, "") || r.stages(r.after(false), " until the captured bytes, written last, go back")
		}
		if !changed {
			return
		}
	}
}

// after is the mouse's image with every write on top, the captured bytes
// only when captured is set.
func (r *restorer) after(captured bool) *flash.Image {
	im := r.dev.Clone()
	for _, c := range r.cands {
		if c.rec.Fate == FateWrite && (captured || !c.rec.Captured) {
			_ = im.Set(c.rec.Extent.Addr, c.rec.Source)
		}
	}
	return im
}

// binding sets the tier of a binding write from what it runs in final, or
// says why it is refused.
func (r *restorer) binding(c *candidate, final *flash.Image) string {
	k := c.index
	fn, err := mouse.DecodeKeyFn(c.rec.Source)
	if err != nil {
		return "invalid in the source"
	}
	cfg := mouse.Decode(r.m, final)
	fs := []mouse.Feature{typeFeature(fn.Type)}
	switch fn.Type {
	case mouse.TypeShortcut:
		if cfg.ShortcutClass[k] != flash.SlotValid {
			return "shortcut " + strconv.Itoa(k) + " would be " + cfg.ShortcutClass[k].String() + " after the restore"
		}
		e, _ := mouse.ShortcutExtent(k)
		n, _ := final.Byte(e.Addr)
		body, _ := final.Get(flash.Extent{Addr: e.Addr, Len: 3*int(n) + 2})
		fs, _ = comboFeatures(r.m, k, body)
	case mouse.TypeMacro:
		for _, s := range []int{k, int(fn.Param >> 8)} {
			if cfg.MacroClass[s] != flash.SlotValid {
				return "macro " + strconv.Itoa(s) + " would be " + cfg.MacroClass[s].String() + " after the restore"
			}
		}
		if int(fn.Param>>8) != k {
			fs = append(fs, mouse.FeatureMacroForeign)
		}
	case mouse.TypeDPILock:
		if d, ok := fn.LockedDPI(r.m.Sensor); !ok || d > maxDPI(r.m) {
			return "its DPI lock is not a DPI of " + r.m.Key
		}
	}
	t, why := r.tier(append(fs, mouse.SlotFeatures(r.m, k)...)...)
	if why != "" {
		return why
	}
	c.rec.Tier, c.change.Tier = t, t
	return ""
}

// bodyUsers refuses a body write the bindings around it would not survive:
// one that leaves the body empty or invalid while a binding still runs it,
// or one whose binding plan.New disables and binds again when that binding
// also runs a body that is not valid.
func (r *restorer) bodyUsers(c *candidate, final *flash.Image) string {
	t := bodyTables[0]
	if c.kind == kindMacro {
		t = bodyTables[1]
	}
	runs := func(k int, fn mouse.KeyFn) bool {
		switch fn.Type {
		case mouse.TypeShortcut:
			return !t.macro && k == c.index
		case mouse.TypeMacro:
			return t.macro && (k == c.index || int(fn.Param>>8) == c.index)
		}
		return false
	}
	valid := t.decode(c.rec.Source) == flash.SlotValid
	cfg := mouse.Decode(r.m, final)
	for k := range mouse.Slots {
		if fn, ok := binding(final, k); ok && runs(k, fn) && !valid {
			return "button slot " + strconv.Itoa(k) + " still runs it after the restore"
		}
		fn, ok := binding(r.dev, k)
		if !ok || !runs(k, fn) || r.writesBinding(k) {
			continue
		}
		bodies := []string{"shortcut " + strconv.Itoa(k)}
		classes := []flash.SlotClass{cfg.ShortcutClass[k]}
		if fn.Type == mouse.TypeMacro {
			bodies, classes = nil, nil
			for _, s := range []int{k, int(fn.Param >> 8)} {
				bodies = append(bodies, "macro "+strconv.Itoa(s))
				classes = append(classes, cfg.MacroClass[s])
			}
		}
		for i, cl := range classes {
			if cl != flash.SlotValid {
				return fmt.Sprintf("button slot %d runs it and %s, which would be %s, so it could not be bound again", k, bodies[i], cl)
			}
		}
	}
	return ""
}

func binding(im *flash.Image, k int) (mouse.KeyFn, bool) {
	e, _ := mouse.KeyFnExtent(k)
	b, ok := im.Get(e)
	if !ok {
		return mouse.KeyFn{}, false
	}
	fn, err := mouse.DecodeKeyFn(b)
	return fn, err == nil
}

func (r *restorer) writesBinding(k int) bool {
	return slices.ContainsFunc(r.cands, func(c *candidate) bool {
		return c.kind == kindBinding && c.index == k && c.rec.Fate == FateWrite
	})
}

var leftClick = []byte{0x01, 0x01, 0x00, 0x53}

// leftClick keeps the last Left Click of the physical buttons (I9): when
// the writes would leave none, the first one that takes it away is refused.
func (r *restorer) leftClick(final *flash.Image) bool {
	count := func(im *flash.Image) int {
		n := 0
		for _, s := range r.layout.Buttons {
			e, _ := mouse.KeyFnExtent(s)
			if b, ok := im.Get(e); ok && bytes.Equal(b, leftClick) {
				n++
			}
		}
		return n
	}
	if count(r.dev) == 0 || count(final) > 0 {
		return false
	}
	for _, c := range r.cands {
		if c.rec.Fate == FateWrite && c.kind == kindBinding && slices.Contains(r.layout.Buttons, c.index) && bytes.Equal(c.rec.Device, leftClick) {
			c.rec.Fate, c.rec.Why = FateRefused, "it holds the last Left Click, which arcctl never takes away (I9)"
			return true
		}
	}
	return false
}

// stages refuses the stage count and current stage writes when they would
// leave im on a current stage past the count, or an active stage invalid;
// when says for how long.
func (r *restorer) stages(im *flash.Image, when string) bool {
	cfg := mouse.Decode(r.m, im)
	if cfg.Stages.State != flash.OK || cfg.Current.State != flash.OK {
		return false
	}
	why := ""
	switch {
	case cfg.Current.Value >= cfg.Stages.Value:
		why = fmt.Sprintf("the current stage %d would be past the stage count %d", cfg.Current.Value+1, cfg.Stages.Value)
	default:
		for i := range cfg.Stages.Value {
			if cfg.DPI[i].DPIField.State != flash.OK {
				why = fmt.Sprintf("DPI stage %d would be active but is %s", i+1, cfg.DPI[i].DPIField.State)
				break
			}
		}
	}
	if why == "" {
		return false
	}
	changed := false
	for _, c := range r.cands {
		if c.rec.Fate == FateWrite && !c.rec.Captured && (c.kind == kindStages || c.kind == kindCurrent) {
			c.rec.Fate, c.rec.Why = FateRefused, why+when
			changed = true
		}
	}
	return changed
}

// changes are the writes in the order the planner keeps for records: the
// DPI values, then the stage count and the current stage in the order whose
// state in between is safest, then the hidden settings. Bodies, bindings
// and captured bytes go in any order; plan.New orders them.
func (r *restorer) changes() []plan.Change {
	var dpi, stages, settings, rest []plan.Change
	var count, current *candidate
	for _, c := range r.cands {
		if c.rec.Fate != FateWrite {
			continue
		}
		if c.rec.Captured {
			rest = append(rest, c.change)
			continue
		}
		switch c.kind {
		case kindDPI:
			dpi = append(dpi, c.change)
		case kindStages:
			count = c
		case kindCurrent:
			current = c
		case kindSetting:
			settings = append(settings, c.change)
		default:
			rest = append(rest, c.change)
		}
	}
	switch {
	case count != nil && current != nil:
		dc := mouse.Decode(r.m, r.dev)
		oldCount, errCount := fieldValue(dc.Stages)
		oldCur, errCur := fieldValue(dc.Current)
		newCount, newCur := int(count.rec.Source[0]), int(current.rec.Source[0])
		if between(newCount, oldCur, errCur) >= between(oldCount, newCur, errCount) {
			stages = []plan.Change{count.change, current.change}
		} else {
			stages = []plan.Change{current.change, count.change}
		}
	case count != nil:
		stages = []plan.Change{count.change}
	case current != nil:
		stages = []plan.Change{current.change}
	}
	return slices.Concat(dpi, stages, settings, rest)
}

func fieldValue(f mouse.Field) (int, error) {
	if f.State != flash.OK {
		return 0, errors.New(f.State.String())
	}
	return f.Value, nil
}

// between rates the mouse's state after the first of the two stage writes,
// as the planner does: 2 when current < count, 1 when the pair left
// untouched is invalid, 0 otherwise.
func between(count, current int, untouched error) int {
	switch {
	case untouched != nil:
		return 1
	case current < count:
		return 2
	}
	return 0
}

// planText is a planner's error without its package prefixes and edit index.
func planText(err error) string {
	return strings.NewReplacer("edit 0: ", "", "mouse: ", "", "plan: ", "", "edit refused: ", "").Replace(err.Error())
}
