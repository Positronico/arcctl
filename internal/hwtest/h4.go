//go:build hwtest

package hwtest

import (
	"errors"
	"fmt"
	"slices"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

// The H4 slots: the wheel click's body is written back as it is, Forward's
// shortcut changes two-phase, and Backward takes the media and extra cases.
const (
	h4IdentitySlot = 2
	h4ShortcutSlot = 4
	h4CaseSlot     = 3
)

const (
	usagePlayPause = 0xCD
	usageTab       = 0x2B
	menuKey        = 0x01
)

func h4Reads() []flash.Extent {
	var out []flash.Extent
	for _, k := range []int{h4IdentitySlot, h4CaseSlot, h4ShortcutSlot} {
		e, _ := mouse.ShortcutExtent(k)
		out = append(out, e)
	}
	return out
}

// H4: an identity write of the wheel click's shortcut body; Forward's
// shortcut with Shift added (or taken out), two-phase, tried and reverted;
// Backward to Play/Pause, tried and reverted; and the two extra cases on
// Backward, each promoted only when the user sees its effect: a right-side
// modifier and the Menu key (kind 7).
func buildH4(b *builder) error {
	if err := b.bodyIdentity(h4IdentitySlot); err != nil {
		return err
	}
	if err := b.shiftChange(h4ShortcutSlot); err != nil {
		return err
	}

	label, action := b.button(h4CaseSlot)
	was := fmt.Sprintf("Before the stage %s ran %s.", label, b.was(h4CaseSlot))
	play, _ := keys.ConsumerByUsage(usagePlayPause)
	i, err := b.edit(fmt.Sprintf("slot %d (%s, now %s) -> %s", h4CaseSlot, label, action, play.Label), mouse.SetMedia{Slot: h4CaseSlot, Usage: usagePlayPause})
	if err != nil {
		return err
	}
	b.ask("media", fmt.Sprintf("Start some audio or a video, then press %s twice. Did the first press pause it and the second play it again, "+
		"or the other way round?", label), true)
	if err := b.revert(fmt.Sprintf("revert: slot %d back to %s", h4CaseSlot, action), i); err != nil {
		return err
	}

	tab := keys.Stroke{Kind: keys.KindKey, Value: usageTab}
	right, left := keys.Combo{keys.RMeta.Stroke(), tab}, keys.Combo{keys.LMeta.Stroke(), tab}
	if i, err = b.edit(fmt.Sprintf("slot %d (%s) -> %s, a right-side modifier", h4CaseSlot, label, right.Format(b.os)),
		mouse.SetShortcut{Slot: h4CaseSlot, Combo: right}); err != nil {
		return err
	}
	b.extra("right-modifier", fmt.Sprintf("%s now sends %s. Press it once: does it switch apps as %s does?", label, right.Format(b.os), left.Format(b.os)),
		fmt.Sprintf("%s acts as %s", right.Format(b.os), left.Format(b.os)), mouse.FeatureShortcutRightModifier)
	if err := b.revert(fmt.Sprintf("revert: slot %d back to %s", h4CaseSlot, action), i); err != nil {
		return err
	}

	menu := keys.Combo{{Kind: keys.KindMenu, Value: menuKey}}
	if i, err = b.edit(fmt.Sprintf("slot %d (%s) -> %s (kind 7)", h4CaseSlot, label, menu.Format(b.os)),
		mouse.SetShortcut{Slot: h4CaseSlot, Combo: menu}); err != nil {
		return err
	}
	b.extra("menu", fmt.Sprintf("%s now sends the %s key. Open Karabiner-EventViewer, press %s, then press it over a file in the Finder. "+
		"Did the viewer list the key (it calls it application), or a context menu open?", label, menu.Format(b.os), label),
		fmt.Sprintf("the %s key (kind 7) reaches the Mac", menu.Format(b.os)), mouse.FeatureShortcutMenu)
	if err := b.revert(fmt.Sprintf("revert: slot %d back to %s", h4CaseSlot, action), i); err != nil {
		return err
	}
	b.askAside("restored", fmt.Sprintf("Press %s again. Does it do what it did before the stage?", label), was, true)
	return nil
}

// bodyIdentity lays out an identity write of the record slot's shortcut
// body declares, and asks whether the button still runs it. The bytes are
// the user's shortcut, so the records name neither them nor the keys.
func (b *builder) bodyIdentity(slot int) error {
	combo, err := b.shortcutOf(slot)
	if err != nil {
		return err
	}
	body, err := mouse.EncodeShortcut(combo)
	if err != nil {
		return err
	}
	e, _ := mouse.ShortcutExtent(slot)
	e.Len = len(body)
	label, _ := b.button(slot)
	chunks := (e.Len + wire.MaxData - 1) / wire.MaxData
	title := fmt.Sprintf("identity write of slot %d's shortcut body at %s (%s, %d chunks)", slot, e, label, chunks)
	if err := b.identity(title, e, slot, b.bodyFeatures(e, combo, body)...); err != nil {
		return err
	}
	b.askAside("identity", fmt.Sprintf("Press %s where its shortcut has an effect you can see. Does it still do what it did before the stage?", label),
		fmt.Sprintf("%s runs %s.", label, b.was(slot)), true)
	return nil
}

// shiftChange lays out slot's shortcut with Left Shift added, or taken out
// when it holds it, written two-phase since the binding runs that body, then
// tried and reverted.
func (b *builder) shiftChange(slot int) error {
	combo, err := b.shortcutOf(slot)
	if err != nil {
		return err
	}
	to, added, err := toggleShift(combo)
	if err != nil {
		return fmt.Errorf("H4: slot %d: %w", slot, err)
	}
	label, action := b.button(slot)
	how := map[bool]string{true: "added", false: "taken out"}[added]
	aside := fmt.Sprintf("%s now sends %s; before the stage it sent %s.", label, to.Format(b.os), combo.Format(b.os))
	i, err := b.edit(fmt.Sprintf("slot %d (%s, now %s): the same shortcut with Shift %s, two-phase", slot, label, action, how),
		mouse.SetShortcut{Slot: slot, Combo: to})
	if err != nil {
		return err
	}
	if p := b.steps[i].plan; !twoPhase(p) {
		return fmt.Errorf("H4: the change of slot %d is not written two-phase: %v", slot, phaseList(p))
	}
	b.askAside("shift", fmt.Sprintf("Press %s where its shortcut has an effect you can see (Karabiner-EventViewer lists the keys a button sends). "+
		"Does it now send its old shortcut with Shift %s?", label, how), aside, true)
	if err := b.revert(fmt.Sprintf("revert: slot %d back to %s, two-phase", slot, action), i); err != nil {
		return err
	}
	if p := b.steps[len(b.steps)-1].plan; !twoPhase(p) {
		return fmt.Errorf("H4: the revert of slot %d is not written two-phase: %v", slot, phaseList(p))
	}
	b.askAside("shift-back", fmt.Sprintf("Press %s again. Does it do what it did before the stage?", label),
		fmt.Sprintf("%s runs %s again.", label, combo.Format(b.os)), true)
	return nil
}

// edit plans e with the release planner, as the Buttons tab would, and
// lays it out as an apply step.
func (b *builder) edit(title string, e mouse.Edit) (int, error) {
	p, err := mouse.PlanEdits(b.m, b.img, []mouse.Edit{e}, b.opt)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", title, err)
	}
	return b.applied(title, p)
}

// shortcutOf decodes the shortcut body slot's binding runs.
func (b *builder) shortcutOf(slot int) (keys.Combo, error) {
	ke, _ := mouse.KeyFnExtent(slot)
	rec, err := b.current(ke)
	if err != nil {
		return nil, err
	}
	if fn, err := mouse.DecodeKeyFn(rec); err != nil || fn.Type != mouse.TypeShortcut {
		label, action := b.button(slot)
		return nil, fmt.Errorf("H4 needs slot %d (%s) bound to its shortcut body; it runs %s", slot, label, action)
	}
	e, _ := mouse.ShortcutExtent(slot)
	body, err := b.current(e)
	if err != nil {
		return nil, err
	}
	combo, err := mouse.DecodeShortcut(body)
	if err != nil {
		return nil, fmt.Errorf("H4: slot %d's shortcut body: %w", slot, err)
	}
	return combo, nil
}

var errShift = errors.New("the shortcut must end with a key and leave room for Shift")

// toggleShift is c with Left Shift pressed right before its key, or c
// without it when it holds it; added says which.
func toggleShift(c keys.Combo) (to keys.Combo, added bool, err error) {
	n := len(c)
	if n == 0 || c[n-1].Kind != keys.KindKey {
		return nil, false, errShift
	}
	shift := keys.LShift.Stroke()
	if i := slices.Index(c, shift); i >= 0 {
		return slices.Delete(slices.Clone(c), i, i+1), false, nil
	}
	if n >= mouse.MaxShortcutKeys {
		return nil, false, errShift
	}
	return slices.Insert(slices.Clone(c), n-1, shift), true, nil
}

// reasonFeatures are the features PlanEdits takes for what keeps the web
// app from building a body.
var reasonFeatures = map[mouse.Reason]mouse.Feature{
	mouse.RightModifier: mouse.FeatureShortcutRightModifier,
	mouse.MenuKey:       mouse.FeatureShortcutMenu,
	mouse.CustomCombo:   mouse.FeatureShortcutCustom,
	mouse.MediaCode:     mouse.FeatureMediaCustom,
}

// bodyFeatures are the features of writing body, combo's encoding, at e,
// as PlanEdits takes them: shortcut or media, and one per shape the web app
// cannot build.
func (b *builder) bodyFeatures(e flash.Extent, combo keys.Combo, body []byte) []mouse.Feature {
	fs := []mouse.Feature{mouse.FeatureShortcut}
	if len(combo) == 1 && combo[0].Kind == keys.KindConsumer {
		fs[0] = mouse.FeatureMedia
	}
	p := plan.Plan{Ops: []plan.Op{{Extent: e, New: body}}}
	for _, w := range mouse.WebCompat(b.m, p) {
		if f, ok := reasonFeatures[w.Reason]; ok {
			fs = append(fs, f)
		}
	}
	return fs
}

func twoPhase(p plan.Plan) bool {
	return slices.Equal(phaseList(p), []plan.Phase{plan.Neutralise, plan.Body, plan.Bind})
}

func phaseList(p plan.Plan) []plan.Phase {
	out := make([]plan.Phase, len(p.Ops))
	for i, op := range p.Ops {
		out[i] = op.Phase
	}
	return out
}
