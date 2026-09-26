package keys_test

import (
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/keys"
)

func TestPresets(t *testing.T) {
	want := map[keys.OS][][2]string{
		keys.Win: {
			{"diy1", "Win+H"}, {"diy2", "Win+A"}, {"diy3", "Win+Tab"}, {"diy4", "Win+D"},
			{"diy5", "Win+Shift+S"}, {"diy6", "Alt+F4"}, {"diy7", "Win+↑"}, {"diy8", "Win+↓"},
			{"diy9", "Ctrl+X"}, {"diy10", "Ctrl+C"}, {"diy11", "Ctrl+V"}, {"diy12", "Ctrl+Z"},
			{"diy14", "Ctrl++"}, {"diy15", "Ctrl+-"}, {"diy16", "Ctrl+T"}, {"diy17", "Win+L"},
		},
		keys.Mac: {
			{"diy1", "Option+Esc"}, {"diy2", "Cmd+Space"}, {"diy3", "Ctrl+↑"},
			{"diy5", "Shift+Cmd+3"}, {"diy6", "Cmd+W"}, {"diy7", "Ctrl+Cmd+F"}, {"diy8", "Cmd+M"},
			{"diy9", "Cmd+X"}, {"diy10", "Cmd+C"}, {"diy11", "Cmd+V"}, {"diy12", "Cmd+Z"},
			{"diy14", "Cmd++"}, {"diy15", "Cmd+-"}, {"diy16", "Cmd+T"}, {"diy17", "Ctrl+Cmd+Q"},
		},
	}
	for os, table := range want {
		ps := keys.Presets(os)
		if len(ps) != len(table) {
			t.Fatalf("%s: %d presets, want %d", os, len(ps), len(table))
		}
		for i, p := range ps {
			id, format := table[i][0], table[i][1]
			if p.ID != id || p.Label == "" {
				t.Errorf("%s preset %d: id %q label %q, want id %q", os, i, p.ID, p.Label, id)
			}
			byID, ok := keys.PresetByID(os, id)
			if !ok || byID.ID != id || !slices.Equal(byID.Keys, p.Keys) {
				t.Errorf("%s PresetByID(%q) = %+v, %v", os, id, byID, ok)
			}
			c := p.Combo()
			if got := c.Format(os); got != format {
				t.Errorf("%s %s: %q, want %q", os, id, got, format)
			}
			if n := len(c); n < 2 || n > 5 {
				t.Errorf("%s %s: %d strokes", os, id, n)
			}
			for j, k := range p.Keys {
				if c[j] != k.Stroke() || !c[j].Known() {
					t.Errorf("%s %s stroke %d: %+v from %+v", os, id, j, c[j], k)
				}
				if byCode, ok := keys.ByCode(k.Code); !ok || byCode != k {
					t.Errorf("%s %s: ByCode(%q) = %+v, %v; want %+v", os, id, k.Code, byCode, ok, k)
				}
				if byValue, ok := c[j].Key(); !ok || byValue != k {
					t.Errorf("%s %s: stroke %+v resolves to %+v, %v; want %+v", os, id, c[j], byValue, ok, k)
				}
				if last := j == len(p.Keys)-1; (k.Kind == keys.KindModifier) == last {
					t.Errorf("%s %s: stroke %d kind %s", os, id, j, k.Kind)
				}
			}
			reversed := slices.Clone(c)
			slices.Reverse(reversed)
			if m, ok := keys.MatchPreset(os, reversed); !ok || m.ID != id {
				t.Errorf("%s MatchPreset(%s reversed) = %q, %v", os, id, m.ID, ok)
			}
		}
	}
}

func TestPresetLookupMisses(t *testing.T) {
	if ps := keys.Presets(2); ps != nil {
		t.Errorf("Presets(2) = %d presets", len(ps))
	}
	for _, c := range []struct {
		os keys.OS
		id string
	}{{keys.Mac, "diy4"}, {keys.Win, "diy13"}, {keys.Mac, "diy13"}, {keys.Win, ""}, {2, "diy1"}} {
		if p, ok := keys.PresetByID(c.os, c.id); ok {
			t.Errorf("PresetByID(%s, %q) = %+v", c.os, c.id, p)
		}
	}
}

func TestMatchPreset(t *testing.T) {
	c := st(keys.KindKey, 0x06)
	cases := []struct {
		os    keys.OS
		combo keys.Combo
		id    string
	}{
		{keys.Win, keys.Combo{keys.LCtrl.Stroke(), c}, "diy10"},
		{keys.Win, keys.Combo{c, keys.LCtrl.Stroke()}, "diy10"},
		{keys.Mac, keys.Combo{keys.LMeta.Stroke(), c}, "diy10"},
		{keys.Mac, keys.Combo{keys.LCtrl.Stroke(), c}, ""},
		{keys.Win, keys.Combo{keys.RCtrl.Stroke(), c}, ""},
		{keys.Win, keys.Combo{keys.LCtrl.Stroke(), keys.LShift.Stroke(), c}, ""},
		{keys.Win, keys.Combo{keys.LCtrl.Stroke(), keys.LCtrl.Stroke()}, ""},
		{keys.Win, keys.Combo{keys.LCtrl.Stroke()}, ""},
		{keys.Mac, keys.Combo{keys.LMeta.Stroke(), keys.LShift.Stroke(), st(keys.KindKey, 0x20)}, "diy5"},
		{keys.Win, nil, ""},
		{2, keys.Combo{keys.LCtrl.Stroke(), c}, ""},
	}
	for _, tc := range cases {
		p, ok := keys.MatchPreset(tc.os, tc.combo)
		if ok != (tc.id != "") || p.ID != tc.id {
			t.Errorf("MatchPreset(%s, %s) = %q, %v; want %q", tc.os, tc.combo.Format(tc.os), p.ID, ok, tc.id)
		}
	}
}
