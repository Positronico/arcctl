package catalog

import "slices"

type overlay struct {
	alias          string
	pairCID        byte
	osSwitchLocked bool
}

var overlays = map[string]overlay{
	"7B01": {pairCID: 0x7B},
	"7B02": {alias: "EM06 (inferred)", pairCID: 0x7B},
	"7B03": {alias: "Vertical mouse", pairCID: 0x7B},
	"7B04": {pairCID: 0x7B},
	"7B05": {pairCID: 0x7B},
	"7B06": {pairCID: 0x7B},
	"0301": {alias: "Full-size keyboard", pairCID: 0x03},
	"0304": {alias: "Compact keyboard with knob", pairCID: 0x03, osSwitchLocked: true},
}

var officeMIDs = []byte{2, 4, 5, 6}

const (
	gameRollerMID = 2
	flywheelMID   = 5
)

func applyOverlay(ms []*Model) []*Model {
	for _, m := range ms {
		o := overlays[m.Key]
		m.Alias = o.alias
		m.PairCID = o.pairCID
		m.UI = uiFlags(m, o)
	}
	return ms
}

func uiFlags(m *Model, o overlay) UIFlags {
	if m.Family != FamilyMouse {
		return UIFlags{OSSwitchLocked: o.osSwitchLocked}
	}
	mid := m.MIDs[0]
	office := slices.Contains(officeMIDs, mid)
	return UIFlags{
		Office:     office,
		GameRoller: mid == gameRollerMID,
		Flywheel:   office && mid == flywheelMID,
		ProfilesUI: m.Group == GroupMouse,
		DPILight:   m.Defaults.DPIEffect.Show && !office,
		RatePanel:  !office,
	}
}

const (
	kbKindBasic    = 2
	kbKindSpecial  = 5
	kbKindFunction = 6
	kbKindQueue    = 9

	usageEscape    = 0x29
	usageBackspace = 0x2A
	usageF5        = 0x3E
	usageF6        = 0x3F

	fnSlotDelete = 46
)

func (k *KeyboardSpec) Locked(system, layout string, slot int) bool {
	l, ok := k.Layer(system, layout)
	if !ok || slot < 0 || slot >= len(l.Slots) {
		return true
	}
	kind, v := l.Slots[slot].Type&0x0F, l.Slots[slot].Value
	f5f6 := kind == kbKindBasic && (v == usageF5 || v == usageF6)
	if layout == "fn" {
		switch {
		case kind == kbKindBasic && (v == usageEscape || v == usageBackspace),
			kind == 0 && v == 0 && slot == fnSlotDelete:
			return true
		case kind == kbKindQueue && (v == 3 || v == 4), f5f6:
			return false
		}
	}
	return (system == "mac" && f5f6) || kind == kbKindSpecial || kind == kbKindFunction || kind == kbKindQueue
}
