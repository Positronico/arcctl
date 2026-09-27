//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

// em11 is the model §11 of the plan writes its stages for.
const em11 = "7B04"

type stageDef struct {
	name     string
	title    string
	promotes []mouse.Feature
	// extras are promoted each only when the user saw its effect (the
	// extra cases of H4), and only when the stage passes.
	extras []mouse.Feature
	// build lays out a write stage on the image of a fresh backup; run is a
	// stage that writes nothing.
	build func(b *builder) error
	run   func(ctx context.Context, r *runner) error
	// reads are what a dry run reads besides the loaded image before it lays
	// the steps out; a run has them from its full backup.
	reads []flash.Extent
	// prepare are asked before the stage asks to run; an answer other than
	// the one wanted stops it before anything is written.
	prepare []question
	// phrase is typed, after the tier phrase, before the stage runs.
	phrase string
	// what replaces the count of records in the question before the run.
	what string
	// selfCheck stages check the device themselves at the end, instead of
	// comparing every touched extent with the fresh backup.
	selfCheck bool
}

type question struct {
	id, text string
}

var stages = []*stageDef{
	{name: "H0", title: "read-only session, latency, coexistence", run: runH0},
	{name: "H1", title: "settings pairs, echo and NAK behaviour", promotes: []mouse.Feature{mouse.FeatureCurrent}, build: buildH1},
	{name: "H2", title: "4-byte records (DPI)", promotes: []mouse.Feature{mouse.FeatureDPI, mouse.FeatureStages}, build: buildH2},
	{name: "H3", title: "button system functions", promotes: []mouse.Feature{mouse.FeatureSystem}, build: buildH3},
	{name: "H3b", title: "physical slot map", promotes: []mouse.Feature{mouse.FeatureUnmappedSlot}, build: buildH3b},
	{name: "H4", title: "shortcut and media bodies", promotes: []mouse.Feature{mouse.FeatureShortcut, mouse.FeatureMedia},
		extras: []mouse.Feature{mouse.FeatureShortcutRightModifier, mouse.FeatureShortcutMenu}, build: buildH4, reads: h4Reads()},
	{name: "H5", title: "macros and journal recovery", promotes: []mouse.Feature{mouse.FeatureMacro}, build: buildH5, reads: h5Reads()},
	{name: "H6", title: "restore round trip", promotes: []mouse.Feature{mouse.FeatureRestore}, build: buildH6, reads: h6Reads()},
	{name: "H7", title: "factory reset", promotes: []mouse.Feature{mouse.FeatureReset}, build: buildH7, prepare: h7Prepare,
		phrase: resetPhrase, what: "It sends the factory reset once, then writes back from the fresh backup what the reset changed.", selfCheck: true},
	{name: "H9", title: "robustness: lock, sleep, unplug, button press during writes", build: buildH9, reads: h9Reads()},
}

type stepKind uint8

const (
	stepIdentity stepKind = iota + 1 // raw path: write the bytes already there, read them back
	stepProbe                        // raw path: the identity write with a wrong checksum
	stepApply                        // the executor writes plan
	stepRevert                       // the session reverts step of, whose revert plan is plan
	stepAsk                          // a yes/no question; want passes, unless observe
	stepName                         // a question answered with a name
	stepCheck                        // extent must still hold bytes
	stepWait                         // an instruction the user carries out, then presses Enter
	stepCustom                       // the stage's own code: a drill, a restore, the reset
)

type step struct {
	kind   stepKind
	title  string
	extent flash.Extent
	bytes  []byte
	tier   catalog.Tier // of an identity write or the probe
	plan   plan.Plan
	of     int
	id     string
	text   string
	want   bool
	// observe records a question's answer as the finding what; any answer
	// passes. A yes to one with extra promotes that feature.
	observe bool
	what    string
	extra   mouse.Feature
	// aside is said before the question and kept out of the records: it
	// may name the user's own shortcuts.
	aside  string
	custom *custom
}

// custom is a step the stage runs with its own code. plans are what the
// preview prints and dry-runs, in order, and what the gates count; raw are
// the packets it sends past the executor; need adds the tiers of writes it
// plans only while it runs. touched are the extents it may leave changed
// when it fails, which the stage then puts back from the fresh backup.
type custom struct {
	// abort marks H5's torn-write drill, which needs --debug-abort-after-chunk.
	abort   bool
	plans   []plan.Plan
	lines   []string
	raw     []wire.Packet
	need    []catalog.Tier
	touched []flash.Extent
	run     func(ctx context.Context, r *runner, c *conn) error
}

// ops are the records the step writes: a plan's ops, or the one op an
// identity write or the probe amounts to, whose old and new bytes are equal.
func (s step) ops() []plan.Op {
	switch s.kind {
	case stepApply, stepRevert:
		return s.plan.Ops
	case stepIdentity, stepProbe:
		return []plan.Op{{Seq: 1, Extent: s.extent, Old: s.bytes, New: s.bytes, Phase: plan.Record, Desc: s.title, Tier: s.tier}}
	case stepCustom:
		var ops []plan.Op
		for _, p := range s.custom.plans {
			ops = append(ops, p.Ops...)
		}
		for _, t := range s.custom.need {
			ops = append(ops, plan.Op{Desc: s.title, Tier: t})
		}
		return ops
	}
	return nil
}

// builder lays out a stage's steps. img is the device as it will be after
// the steps so far.
type builder struct {
	stage   string
	m       *catalog.Model
	dev     plan.Identity
	profile *byte
	opt     mouse.Options
	os      keys.OS
	img     *flash.Image
	layout  plan.Layout
	steps   []step
	// src is the fresh backup as a restore source, for H6 and H7.
	src *backup.Source
	// h7 is H7's state, which a run resumed after its reset takes up.
	h7 *h7State
}

var errPrimary = errors.New("hwtest: slots 0 and 1 (left and right button) are never touched")

// touchesPrimary reports whether e reaches the binding, shortcut or macro
// slot of the left or right button.
func touchesPrimary(e flash.Extent) bool {
	for _, table := range []func(int) (flash.Extent, bool){mouse.KeyFnExtent, mouse.ShortcutExtent, mouse.MacroExtent} {
		for slot := range 2 {
			if s, _ := table(slot); s.Overlaps(e) {
				return true
			}
		}
	}
	return false
}

func (b *builder) add(s step) int {
	b.steps = append(b.steps, s)
	return len(b.steps) - 1
}

func (b *builder) current(e flash.Extent) ([]byte, error) {
	if touchesPrimary(e) {
		return nil, fmt.Errorf("%w: %s", errPrimary, e)
	}
	cur, ok := b.img.Get(e)
	if !ok {
		return nil, fmt.Errorf("%w: %s", plan.ErrUnread, e)
	}
	return cur, nil
}

// identity lays out an identity write of e, at the tier of the features
// that write e (slot as for tier).
func (b *builder) identity(title string, e flash.Extent, slot int, fs ...mouse.Feature) error {
	cur, err := b.current(e)
	if err != nil {
		return err
	}
	t, err := b.tier(slot, fs...)
	if err != nil {
		return err
	}
	b.add(step{kind: stepIdentity, title: title, extent: e, bytes: cur, tier: t})
	return nil
}

func (b *builder) probe(title string, e flash.Extent, slot int, fs ...mouse.Feature) error {
	cur, err := b.current(e)
	if err != nil {
		return err
	}
	if _, err := probePacket(e, cur); err != nil {
		return err
	}
	t, err := b.tier(slot, fs...)
	if err != nil {
		return err
	}
	b.add(step{kind: stepProbe, title: title, extent: e, bytes: cur, tier: t})
	return nil
}

func (b *builder) check(title string, e flash.Extent) error {
	cur, err := b.current(e)
	if err != nil {
		return err
	}
	b.add(step{kind: stepCheck, title: title, extent: e, bytes: cur})
	return nil
}

func (b *builder) ask(id, text string, want bool) {
	b.add(step{kind: stepAsk, id: b.id(id), text: text, want: want})
}

// askAside is ask with a line said first that the records leave out.
func (b *builder) askAside(id, text, aside string, want bool) {
	b.add(step{kind: stepAsk, id: b.id(id), text: text, want: want, aside: aside})
}

func (b *builder) name(id, text string) {
	b.add(step{kind: stepName, id: b.id(id), text: text})
}

// observe asks a question whose answer is a finding: what, yes or no.
func (b *builder) observe(id, text, what string) {
	b.add(step{kind: stepAsk, id: b.id(id), text: text, observe: true, what: what})
}

// extra asks whether an extra case had its effect, as observe does; a yes
// promotes f once the stage passes.
func (b *builder) extra(id, text, what string, f mouse.Feature) {
	b.add(step{kind: stepAsk, id: b.id(id), text: text, observe: true, what: what, extra: f})
}

func (b *builder) wait(id, text string) {
	b.add(step{kind: stepWait, id: b.id(id), text: text})
}

func (b *builder) custom(title string, c *custom) {
	b.add(step{kind: stepCustom, title: title, custom: c})
}

func (b *builder) id(s string) string { return stageID(b.stage) + "." + s }

// apply plans changes with plan.New, as a release plan would be, at the
// tiers the changes carry.
func (b *builder) apply(title string, changes ...plan.Change) (int, error) {
	p, err := b.plan(changes)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", title, err)
	}
	if len(p.Ops) == 0 {
		return 0, fmt.Errorf("%s: the device already holds these bytes", title)
	}
	return b.add(step{kind: stepApply, title: title, plan: p}), nil
}

// revert lays out the undo of step of: every extent it wrote goes back to
// the bytes before it, planned again on the image after it, which is how
// the executor builds a revert.
func (b *builder) revert(title string, of int) error {
	var changes []plan.Change
	var seen []flash.Extent
	for _, op := range b.steps[of].plan.Ops {
		if slices.Contains(seen, op.Extent) {
			continue
		}
		seen = append(seen, op.Extent)
		changes = append(changes, plan.Change{Addr: op.Extent.Addr, New: op.Old, Desc: "revert: " + op.Desc, Tier: op.Tier})
	}
	p, err := b.plan(changes)
	if err != nil {
		return fmt.Errorf("%s: %w", title, err)
	}
	b.add(step{kind: stepRevert, title: title, plan: p, of: of})
	return nil
}

func (b *builder) plan(changes []plan.Change) (plan.Plan, error) {
	p, err := plan.New(b.dev, b.profile, b.img, b.layout, changes)
	if err != nil {
		return p, err
	}
	next := b.img.Clone()
	for _, op := range p.Ops {
		if touchesPrimary(op.Extent) {
			return p, fmt.Errorf("%w: op %d writes %s", errPrimary, op.Seq, op.Extent)
		}
		if err := next.Set(op.Extent.Addr, op.New); err != nil {
			return p, err
		}
	}
	b.img = next
	return p, nil
}

// tier is the lowest tier of the features, plus the slot's own, as the
// planner adds them; slot -1 is no slot.
func (b *builder) tier(slot int, fs ...mouse.Feature) (catalog.Tier, error) {
	if slot >= 0 {
		fs = append(fs, mouse.SlotFeatures(b.m, slot)...)
	}
	t := catalog.Verified
	for _, f := range fs {
		ft, _ := f.Tier(b.m, b.opt)
		t = min(t, ft)
	}
	if t < catalog.Experimental {
		return t, fmt.Errorf("hwtest: %v is %s on %s", fs, t, b.m.Key)
	}
	return t, nil
}

func (b *builder) change(e flash.Extent, data []byte, desc string, slot int, fs ...mouse.Feature) (plan.Change, error) {
	t, err := b.tier(slot, fs...)
	return plan.Change{Addr: e.Addr, New: data, Desc: desc, Tier: t}, err
}

func pairExtent(addr int) flash.Extent { return flash.Extent{Addr: addr, Len: 2} }

// stagesOf decodes the stage count and the current stage, 0-based.
func stagesOf(im *flash.Image) (count, current int, err error) {
	p, _ := im.Pair(mouse.AddrMaxDpiStage)
	if count, err = mouse.DecodeStageCount(p); err != nil {
		return 0, 0, fmt.Errorf("stage count: %w", err)
	}
	p, _ = im.Pair(mouse.AddrCurrentDPI)
	if current, err = mouse.DecodeCurrentStage(p); err != nil {
		return 0, 0, fmt.Errorf("current stage: %w", err)
	}
	if current >= count {
		return 0, 0, fmt.Errorf("current stage %d is past the stage count %d", current+1, count)
	}
	return count, current, nil
}

func (b *builder) dpiText(stage int) string {
	e, _ := mouse.DPIExtent(stage)
	rec, _ := b.img.Get(e)
	d, err := mouse.DecodeDPI(b.m.Sensor, rec)
	switch {
	case err != nil:
		return fmt.Sprintf("% x", rec)
	case d.X != d.Y:
		return fmt.Sprintf("%dx%d DPI", d.X, d.Y)
	}
	return fmt.Sprintf("%d DPI", d.X)
}

// dpiRecord encodes dpi, or other when the stage already holds dpi.
func (b *builder) dpiRecord(stage, dpi, other int) (flash.Extent, []byte, int, error) {
	e, _ := mouse.DPIExtent(stage)
	for _, d := range []int{dpi, other} {
		rec, err := mouse.EncodeDPI(b.m.Sensor, d)
		if err != nil {
			return e, nil, 0, err
		}
		if cur, _ := b.img.Get(e); !bytes.Equal(cur, rec) {
			return e, rec, d, nil
		}
	}
	return e, nil, 0, fmt.Errorf("stage %d holds neither %d nor %d DPI apart", stage+1, dpi, other)
}

// H1: an identity write of the current stage and the NAK probe on it, then
// the current stage one step down and back.
func buildH1(b *builder) error {
	e := pairExtent(mouse.AddrCurrentDPI)
	count, cur, err := stagesOf(b.img)
	if err != nil {
		return err
	}
	if err := b.identity("identity write of the current stage at "+e.String(), e, -1, mouse.FeatureCurrent); err != nil {
		return err
	}
	if err := b.probe("NAK probe: the same packet with a wrong checksum", e, -1, mouse.FeatureCurrent); err != nil {
		return err
	}
	to := cur - 1
	if cur == 0 {
		to = 1
	}
	if to >= count {
		return fmt.Errorf("H1 needs two DPI stages; the mouse has %d", count)
	}
	pr, err := mouse.EncodeCurrentStage(to)
	if err != nil {
		return err
	}
	before, after := b.dpiText(cur), b.dpiText(to)
	c, err := b.change(e, pr[:], fmt.Sprintf("current stage: %d", to+1), -1, mouse.FeatureCurrent)
	if err != nil {
		return err
	}
	i, err := b.apply(fmt.Sprintf("current stage %d -> %d", cur+1, to+1), c)
	if err != nil {
		return err
	}
	b.ask("speed", fmt.Sprintf("The mouse is now on DPI stage %d (%s); it was on stage %d (%s). Move the pointer: did its speed change?",
		to+1, after, cur+1, before), true)
	if err := b.revert(fmt.Sprintf("revert: current stage back to %d", cur+1), i); err != nil {
		return err
	}
	b.ask("speed-back", fmt.Sprintf("The mouse is back on stage %d. Move the pointer: is its speed what it was before the stage?", cur+1), true)
	return nil
}

// H2: an identity write of the inactive stage 8, stage 8 and stage 1 to
// other values and back, and the stage count one lower and back.
func buildH2(b *builder) error {
	count, cur, err := stagesOf(b.img)
	if err != nil {
		return err
	}
	if count >= mouse.MaxStages {
		return fmt.Errorf("H2 needs stage 8 inactive; the mouse uses all %d stages", count)
	}
	e8, _ := mouse.DPIExtent(mouse.MaxStages - 1)
	if err := b.identity("identity write of the inactive stage 8 at "+e8.String(), e8, -1, mouse.FeatureDPI); err != nil {
		return err
	}
	e, rec, dpi, err := b.dpiRecord(mouse.MaxStages-1, 1000, 1100)
	if err != nil {
		return err
	}
	c, err := b.change(e, rec, fmt.Sprintf("DPI stage 8: %d", dpi), -1, mouse.FeatureDPI)
	if err != nil {
		return err
	}
	was := b.dpiText(mouse.MaxStages - 1)
	i, err := b.apply(fmt.Sprintf("stage 8 %s -> %d DPI", was, dpi), c)
	if err != nil {
		return err
	}
	if err := b.revert("revert: stage 8 back to "+was, i); err != nil {
		return err
	}

	e, rec, dpi, err = b.dpiRecord(0, 900, 800)
	if err != nil {
		return err
	}
	if c, err = b.change(e, rec, fmt.Sprintf("DPI stage 1: %d", dpi), -1, mouse.FeatureDPI); err != nil {
		return err
	}
	was = b.dpiText(0)
	if i, err = b.apply(fmt.Sprintf("stage 1 %s -> %d DPI", was, dpi), c); err != nil {
		return err
	}
	b.ask("stage1", fmt.Sprintf("Stage 1 now holds %d DPI; it held %s. Press the DPI button until stage 1 is active, move the pointer, "+
		"then press it until stage %d is active again. Did the pointer speed at stage 1 differ from before?", dpi, was, cur+1), true)
	if err := b.revert("revert: stage 1 back to "+was, i); err != nil {
		return err
	}

	if count < 2 || cur >= count-1 {
		return fmt.Errorf("H2 lowers the stage count to %d, which needs the current stage (%d) below it; switch to a lower stage first", count-1, cur+1)
	}
	pc := pairExtent(mouse.AddrMaxDpiStage)
	pr, err := mouse.EncodeStageCount(count - 1)
	if err != nil {
		return err
	}
	if c, err = b.change(pc, pr[:], fmt.Sprintf("stage count: %d", count-1), -1, mouse.FeatureStages); err != nil {
		return err
	}
	last, _ := mouse.DPIExtent(count - 1)
	if i, err = b.apply(fmt.Sprintf("stage count %d -> %d", count, count-1), c); err != nil {
		return err
	}
	if err := b.check(fmt.Sprintf("stage %d keeps its value while inactive", count), last); err != nil {
		return err
	}
	b.ask("count", fmt.Sprintf("Press the DPI button through a full cycle and end on stage %d, where it started. Did it skip stage %d, cycling through %d stages?",
		cur+1, count, count-1), true)
	if err := b.revert(fmt.Sprintf("revert: stage count back to %d", count), i); err != nil {
		return err
	}
	return b.check(fmt.Sprintf("stage %d still holds its value", count), last)
}

// H3: an identity write of slot 15, slot 15 to Left Click and back (no
// control uses it), and the Backward button to Scroll Up and back through
// the revert.
func buildH3(b *builder) error {
	e15, _ := mouse.KeyFnExtent(15)
	if err := b.identity("identity write of slot 15 at "+e15.String(), e15, 15, mouse.FeatureSystem); err != nil {
		return err
	}
	left, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft})
	if err != nil {
		return err
	}
	c, err := b.change(e15, left, "slot 15: left click", 15, mouse.FeatureSystem)
	if err != nil {
		return err
	}
	i, err := b.apply("slot 15 -> Left Click", c)
	if err != nil {
		return err
	}
	b.ask("inert", "Slot 15 now holds Left Click. No control on this mouse uses slot 15. Try every button: does each one work as before?", true)
	if err := b.revert("revert: slot 15 back", i); err != nil {
		return err
	}

	const slot = 3
	e3, _ := mouse.KeyFnExtent(slot)
	label, action := b.button(slot)
	up, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeWheel, Param: mouse.ParamScrollUp})
	if err != nil {
		return err
	}
	if c, err = b.change(e3, up, fmt.Sprintf("slot %d: scroll up", slot), slot, mouse.FeatureSystem); err != nil {
		return err
	}
	if i, err = b.apply(fmt.Sprintf("slot %d (%s, now %s) -> Scroll Up", slot, label, action), c); err != nil {
		return err
	}
	b.ask("scroll", fmt.Sprintf("Open a long page and press the %s button a few times. Does the page scroll up?", label), true)
	if err := b.revert(fmt.Sprintf("revert: slot %d back to %s", slot, action), i); err != nil {
		return err
	}
	b.ask("restored", fmt.Sprintf("Press the %s button again. Does it do what it did before the stage (%s)?", label, action), true)
	return nil
}

// H3b: slots 6 to 11, and the slots from 12 on the model shows (12 and 13
// on mid 6), one at a time to Disable; the user names the control that
// stopped working, and the slot goes back.
func buildH3b(b *builder) error {
	slots := []int{6, 7, 8, 9, 10, 11}
	for _, x := range b.m.Buttons {
		if x.Slot >= 12 && x.Visible && !slices.Contains(slots, x.Slot) {
			slots = append(slots, x.Slot)
		}
	}
	for _, slot := range slots {
		e, _ := mouse.KeyFnExtent(slot)
		cur, err := b.current(e)
		if err != nil {
			return err
		}
		if bytes.Equal(cur, disabled) {
			continue
		}
		_, action := b.button(slot)
		c, err := b.change(e, disabled, fmt.Sprintf("slot %d: disable", slot), slot, mouse.FeatureSystem)
		if err != nil {
			return err
		}
		i, err := b.apply(fmt.Sprintf("slot %d (%s) -> Disable", slot, action), c)
		if err != nil {
			return err
		}
		b.name(fmt.Sprintf("slot%d", slot), fmt.Sprintf("Slot %d (%s) is now disabled. Try every button, the wheel and its tilt. "+
			"Which control stopped working? Name it, or type none:", slot, action))
		if err := b.revert(fmt.Sprintf("revert: slot %d back to %s", slot, action), i); err != nil {
			return err
		}
	}
	if len(b.steps) == 0 {
		return fmt.Errorf("H3b: slots %v are all disabled already", slots)
	}
	return nil
}

var disabled = []byte{0x00, 0x00, 0x00, 0x55}

// button names slot's physical button, when the model shows one, and what
// the slot does now. The keys of a shortcut or a macro are the user's own
// and go into the log, so they are not named.
// was is what slot does now, in words, shortcuts and macro names included;
// only the terminal shows it.
func (b *builder) was(slot int) string {
	cfg := mouse.Decode(b.m, b.img)
	return backup.Action(b.m, &cfg, slot, b.os)
}

// label is the name of slot's button, or "slot k".
func (b *builder) label(slot int) string {
	for _, x := range b.m.Buttons {
		if x.Slot == slot && x.Visible && x.Label != "" {
			return x.Label
		}
	}
	return fmt.Sprintf("slot %d", slot)
}

func (b *builder) button(slot int) (label, action string) {
	label = b.label(slot)
	e, _ := mouse.KeyFnExtent(slot)
	cur, _ := b.img.Get(e)
	switch mouse.KeyType(cur[0]) {
	case mouse.TypeShortcut:
		return label, "its shortcut"
	case mouse.TypeMacro:
		return label, "its macro"
	}
	cfg := mouse.Decode(b.m, b.img)
	return label, backup.Action(b.m, &cfg, slot, b.os)
}
