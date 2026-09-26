package mouse

import (
	"bytes"
	"cmp"
	"math/bits"
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/plan"
)

type Reason uint8

const (
	HiddenSlot Reason = iota + 1
	UnscannedSlot
	KeyTypeHidden
	RightModifier
	MenuKey
	CustomCombo
	MediaCode
	ForeignMacro
	HiddenSetting
)

type Warning struct {
	Seq    int
	Reason Reason
	Text   string
}

type netChange struct {
	extent   flash.Extent
	old, new []byte
	seq      int
}

// WebCompat lists what the plan leaves on the device that the vendor web app cannot
// show, recreate or edit. Only the net effect per extent counts, so the Disable of a
// two-phase rebind and a binding restored to its old bytes raise nothing.
func WebCompat(m *catalog.Model, p plan.Plan) []Warning {
	if m == nil || m.Family != catalog.FamilyMouse {
		return nil
	}
	var out []Warning
	warn := func(seq int, r Reason, text string) { out = append(out, Warning{seq, r, text}) }
	slotSeen := map[int]bool{}
	slot := func(seq, k int) {
		if slotSeen[k] {
			return
		}
		slotSeen[k] = true
		i := slices.IndexFunc(m.Buttons, func(b catalog.Button) bool { return b.Slot == k && b.Visible })
		switch {
		case i < 0:
			warn(seq, HiddenSlot, "slot "+strconv.Itoa(k)+" is not shown by the web app on "+m.Key)
		case k >= len(m.Buttons):
			warn(seq, UnscannedSlot, "the web app may not read slot "+strconv.Itoa(k)+
				": it scans only slots 0-"+strconv.Itoa(len(m.Buttons)-1)+" on "+m.Key)
		}
	}
	for _, c := range netChanges(p) {
		if k, ok := slotAt(KeyFnExtent, c.extent); ok {
			slot(c.seq, k)
			if fn, err := DecodeKeyFn(c.new); err == nil {
				keyFnWarnings(m, k, fn, func(r Reason, text string) { warn(c.seq, r, text) })
			}
			continue
		}
		if k, ok := slotAt(ShortcutExtent, c.extent); ok {
			slot(c.seq, k)
			if combo, err := DecodeShortcut(c.new); err == nil {
				comboWarnings(k, combo, func(r Reason, text string) { warn(c.seq, r, text) })
			}
			continue
		}
		if k, ok := slotAt(MacroExtent, c.extent); ok {
			slot(c.seq, k)
			continue
		}
		for _, name := range hiddenNames(m, c.extent) {
			warn(c.seq, HiddenSetting, "the web app does not show "+name)
		}
	}
	slices.SortStableFunc(out, func(a, b Warning) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

func netChanges(p plan.Plan) []netChange {
	var out []netChange
	at := map[flash.Extent]int{}
	for _, op := range p.Ops {
		if i, ok := at[op.Extent]; ok {
			out[i].new, out[i].seq = op.New, op.Seq
			continue
		}
		at[op.Extent] = len(out)
		out = append(out, netChange{op.Extent, op.Old, op.New, op.Seq})
	}
	return slices.DeleteFunc(out, func(c netChange) bool { return bytes.Equal(c.old, c.new) })
}

func slotAt(table func(int) (flash.Extent, bool), e flash.Extent) (int, bool) {
	for k := range Slots {
		if s, _ := table(k); s.Addr == e.Addr && s.Contains(e) {
			return k, true
		}
	}
	return 0, false
}

func hiddenNames(m *catalog.Model, e flash.Extent) []string {
	var out []string
	if !m.UI.RatePanel && pairExtent(AddrReportRate).Overlaps(e) {
		out = append(out, "ReportRate")
	}
	if !m.UI.DPILight && (flash.Extent{Addr: AddrDPIColor, Len: MaxStages * recordSize}).Overlaps(e) {
		out = append(out, "DPIColor")
	}
	for _, h := range hiddenFields {
		if (flash.Extent{Addr: h.addr, Len: h.size}).Overlaps(e) {
			out = append(out, h.name)
		}
	}
	for _, u := range unmappedExtents {
		if u.Overlaps(e) {
			out = append(out, "the unmapped bytes at "+u.String())
		}
	}
	return out
}

func keyFnWarnings(m *catalog.Model, k int, fn KeyFn, warn func(Reason, string)) {
	hidden := false
	switch fn.Type {
	case TypeFire, TypeProfile, TypeDPILock:
		hidden = true
	case TypeRateSwitch:
		hidden = m.UI.Office
	case TypeDragScroll:
		hidden = !m.UI.GameRoller
	case TypeMacro:
		if foreignMacro(k, fn) {
			warn(ForeignMacro, "slot "+strconv.Itoa(k)+" runs macro "+strconv.Itoa(int(fn.Param>>8))+
				"; the web app shows macro "+strconv.Itoa(k)+" for this button")
		}
	}
	if hidden {
		warn(KeyTypeHidden, "the web app cannot assign "+fn.Type.String()+" on "+m.Key+" (slot "+strconv.Itoa(k)+")")
	}
}

func comboWarnings(k int, c keys.Combo, warn func(Reason, string)) {
	name := "shortcut " + strconv.Itoa(k)
	for _, r := range comboReasons(c) {
		switch r {
		case RightModifier:
			warn(r, name+" uses a right-side modifier, which the web app cannot enter")
		case MenuKey:
			warn(r, name+" uses the Menu key (kind 7), which the web app enters only by key capture")
		case MediaCode:
			warn(r, name+": media usage "+hex16(c[0].Value)+" is not in the web app's media list")
		case CustomCombo:
			warn(r, name+" is neither a preset nor left modifiers plus one key, so the web app cannot build it")
		}
	}
}

// comboReasons lists what keeps the web app from building the shortcut body c.
// PlanEdits turns the same reasons into features.
func comboReasons(c keys.Combo) []Reason {
	var out []Reason
	if slices.ContainsFunc(c, func(s keys.Stroke) bool {
		return s.Kind == keys.KindModifier && keys.Modifier(s.Value).Right()
	}) {
		out = append(out, RightModifier)
	}
	if slices.ContainsFunc(c, func(s keys.Stroke) bool { return s.Kind == keys.KindMenu }) {
		out = append(out, MenuKey)
	}
	switch {
	case mediaCombo(c):
		if !slices.ContainsFunc(catalog.MouseMedia(), func(u catalog.Usage) bool { return u.Code == c[0].Value }) {
			out = append(out, MediaCode)
		}
	case !webCombo(c):
		out = append(out, CustomCombo)
	}
	return out
}

func mediaCombo(c keys.Combo) bool { return len(c) == 1 && c[0].Kind == keys.KindConsumer }

func foreignMacro(slot int, fn KeyFn) bool { return fn.Type == TypeMacro && int(fn.Param>>8) != slot }

func webCombo(c keys.Combo) bool {
	for _, os := range []keys.OS{keys.Win, keys.Mac} {
		for _, p := range keys.Presets(os) {
			if slices.Equal(p.Combo(), c) {
				return true
			}
		}
	}
	n := len(c)
	if n == 0 || c[n-1].Kind != keys.KindKey && c[n-1].Kind != keys.KindMenu {
		return false
	}
	var seen keys.Modifier
	for _, s := range c[:n-1] {
		m := keys.Modifier(s.Value)
		if s.Kind != keys.KindModifier || s.Value > 0xFF || bits.OnesCount8(uint8(m)) != 1 || seen&m != 0 {
			return false
		}
		seen |= m
	}
	return true
}
