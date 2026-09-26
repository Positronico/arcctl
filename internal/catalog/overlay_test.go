package catalog

import (
	"slices"
	"testing"
	"unicode/utf8"
)

func TestOverlayMatchesModels(t *testing.T) {
	var keys []string
	for _, m := range models {
		keys = append(keys, m.Key)
		o, ok := overlays[m.Key]
		if !ok {
			t.Errorf("%s has no overlay entry", m.Key)
			continue
		}
		if o.pairCID == 0 {
			t.Errorf("%s has no pairing cid", m.Key)
		}
		if m.Family == FamilyMouse && len(m.MIDs) != 1 {
			t.Errorf("%s: mouse UI flags assume one mid, have %v", m.Key, m.MIDs)
		}
		if o.osSwitchLocked && m.Family != FamilyKeyboard {
			t.Errorf("%s: OS switch lock on a mouse", m.Key)
		}
	}
	for k := range overlays {
		if !slices.Contains(keys, k) {
			t.Errorf("overlay entry %s matches no model", k)
		}
	}
}

func TestLabelLengths(t *testing.T) {
	for k, v := range labels {
		if n := utf8.RuneCountInString(v); n < 1 || n > 40 {
			t.Errorf("label %s has %d characters", k, n)
		}
	}
	for _, u := range usages {
		if n := utf8.RuneCountInString(u.Label); n < 1 || n > 40 {
			t.Errorf("media label %q has %d characters", u.Label, n)
		}
	}
	for _, m := range models {
		for _, b := range m.Buttons {
			if b.Label == "" {
				t.Errorf("%s slot %d has no label", m.Key, b.Slot)
			}
		}
	}
}
