package keys_test

import (
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/keys"
)

func st(k keys.Kind, v uint16) keys.Stroke { return keys.Stroke{Kind: k, Value: v} }

func TestKindValues(t *testing.T) {
	cases := []struct {
		kind  keys.Kind
		value uint8
		name  string
	}{
		{keys.KindModifier, 0, "modifier"},
		{keys.KindKey, 1, "key"},
		{keys.KindConsumer, 2, "consumer"},
		{keys.KindMouse, 4, "mouse"},
		{keys.KindMenu, 7, "menu"},
		{3, 3, "Kind(3)"},
		{0x82, 0x82, "Kind(130)"},
	}
	for _, c := range cases {
		if uint8(c.kind) != c.value || c.kind.String() != c.name {
			t.Errorf("kind %d: value %d, String %q; want %d, %q", c.kind, uint8(c.kind), c.kind.String(), c.value, c.name)
		}
	}
}

func TestOSString(t *testing.T) {
	for os, want := range map[keys.OS]string{keys.Win: "win", keys.Mac: "mac", 2: "OS(2)"} {
		if got := os.String(); got != want {
			t.Errorf("OS(%d).String() = %q, want %q", uint8(os), got, want)
		}
	}
}

func TestModifiers(t *testing.T) {
	cases := []struct {
		mod      keys.Modifier
		bit      uint8
		code     string
		id       string
		win, mac string
		right    bool
	}{
		{keys.LCtrl, 0x01, "ControlLeft", "LCtrl", "Ctrl", "Ctrl", false},
		{keys.LShift, 0x02, "ShiftLeft", "LShift", "Shift", "Shift", false},
		{keys.LAlt, 0x04, "AltLeft", "LAlt", "Alt", "Option", false},
		{keys.LMeta, 0x08, "MetaLeft", "LMeta", "Win", "Cmd", false},
		{keys.RCtrl, 0x10, "ControlRight", "RCtrl", "RCtrl", "RCtrl", true},
		{keys.RShift, 0x20, "ShiftRight", "RShift", "RShift", "RShift", true},
		{keys.RAlt, 0x40, "AltRight", "RAlt", "RAlt", "ROption", true},
		{keys.RMeta, 0x80, "MetaRight", "RMeta", "RWin", "RCmd", true},
	}
	seen := 0
	for _, c := range keys.All() {
		if c.Kind == keys.KindModifier {
			seen++
		}
	}
	if seen != len(cases) {
		t.Errorf("%d modifier keys in the table, want %d", seen, len(cases))
	}
	for _, c := range cases {
		if uint8(c.mod) != c.bit {
			t.Errorf("%s = %#02x, want %#02x", c.id, uint8(c.mod), c.bit)
		}
		if c.mod.String() != c.id || c.mod.Name(keys.Win) != c.win || c.mod.Name(keys.Mac) != c.mac || c.mod.Right() != c.right {
			t.Errorf("%#02x: String %q, win %q, mac %q, right %v; want %q, %q, %q, %v",
				c.bit, c.mod.String(), c.mod.Name(keys.Win), c.mod.Name(keys.Mac), c.mod.Right(), c.id, c.win, c.mac, c.right)
		}
		byValue, ok := keys.ByValue(keys.KindModifier, c.bit)
		if !ok || byValue.Code != c.code {
			t.Errorf("ByValue(modifier, %#02x) = %q, %v; want %q", c.bit, byValue.Code, ok, c.code)
		}
		byCode, ok := keys.ByCode(c.code)
		if !ok || byCode.Stroke() != c.mod.Stroke() {
			t.Errorf("ByCode(%q) = %+v, %v; want stroke %+v", c.code, byCode.Stroke(), ok, c.mod.Stroke())
		}
		if byCode.Name(keys.Win) != c.win || byCode.Name(keys.Mac) != c.mac {
			t.Errorf("%s key names %q/%q, want %q/%q", c.code, byCode.Name(keys.Win), byCode.Name(keys.Mac), c.win, c.mac)
		}
	}
	both := keys.LCtrl | keys.LShift | keys.RMeta
	if got := both.Name(keys.Mac); got != "Ctrl+Shift+RCmd" {
		t.Errorf("multi-bit mac name %q", got)
	}
	if got := both.String(); got != "LCtrl|LShift|RMeta" {
		t.Errorf("multi-bit String %q", got)
	}
	if got := keys.Modifier(0).String(); got != "Modifier(0)" {
		t.Errorf("zero String %q", got)
	}
	if got := keys.LMeta.Name(9); got != "Win" {
		t.Errorf("unknown OS uses %q, want the win name", got)
	}
	if keys.Modifier(0).Name(keys.Mac) != "" || keys.Modifier(0).Right() {
		t.Error("zero modifier has a name or a side")
	}
}

func TestStrokeName(t *testing.T) {
	cases := []struct {
		stroke   keys.Stroke
		known    bool
		win, mac string
	}{
		{st(keys.KindKey, 0x06), true, "C", "C"},
		{st(keys.KindKey, 0x2B), true, "Tab", "Tab"},
		{st(keys.KindKey, 0x52), true, "↑", "↑"},
		{st(keys.KindKey, 0x2E), true, "+", "+"},
		{st(keys.KindKey, 0x89), true, "K14 | ¥", "K14 | ¥"},
		{st(keys.KindMenu, 0x01), true, "Menu", "Menu"},
		{st(keys.KindModifier, 0x08), true, "Win", "Cmd"},
		{st(keys.KindModifier, 0x05), true, "Ctrl+Alt", "Ctrl+Option"},
		{st(keys.KindConsumer, 0x00CD), true, "Play/Pause", "Play/Pause"},
		{st(keys.KindConsumer, 0x0224), true, "Browser Back", "Browser Back"},
		{st(keys.KindConsumer, 0x0225), true, "Browser Forward", "Browser Forward"},
		{st(keys.KindMouse, 0x01), true, "Left Button", "Left Button"},
		{st(keys.KindMouse, 0x18), true, "Back Button+Forward Button", "Back Button+Forward Button"},
		{st(keys.KindKey, 0x00), false, "<key 0x00>", "<key 0x00>"},
		{st(keys.KindKey, 0x187), false, "<key 0x0187>", "<key 0x0187>"},
		{st(keys.KindKey, 0xE3), false, "<key 0xE3>", "<key 0xE3>"},
		{st(keys.KindMenu, 0x02), false, "<menu 0x02>", "<menu 0x02>"},
		{st(keys.KindModifier, 0x00), false, "<modifier 0x00>", "<modifier 0x00>"},
		{st(keys.KindModifier, 0x100), false, "<modifier 0x0100>", "<modifier 0x0100>"},
		{st(keys.KindConsumer, 0x0301), false, "<consumer 0x0301>", "<consumer 0x0301>"},
		{st(keys.KindConsumer, 0x0006), false, "<consumer 0x0006>", "<consumer 0x0006>"},
		{st(keys.KindMouse, 0x00), false, "<mouse 0x00>", "<mouse 0x00>"},
		{st(keys.KindMouse, 0x20), false, "<mouse 0x20>", "<mouse 0x20>"},
		{st(3, 0x01), false, "<Kind(3) 0x01>", "<Kind(3) 0x01>"},
	}
	for _, c := range cases {
		if got := c.stroke.Known(); got != c.known {
			t.Errorf("%+v Known = %v, want %v", c.stroke, got, c.known)
		}
		if win, mac := c.stroke.Name(keys.Win), c.stroke.Name(keys.Mac); win != c.win || mac != c.mac {
			t.Errorf("%+v names %q/%q, want %q/%q", c.stroke, win, mac, c.win, c.mac)
		}
	}
}

func TestStrokeNameTotal(t *testing.T) {
	values := []uint16{0, 1, 2, 4, 8, 0x10, 0x1F, 0x20, 0x65, 0x80, 0xCD, 0xFF, 0x100, 0x224, 0x7FFF, 0xFFFF}
	for kind := range 256 {
		for _, v := range values {
			s := st(keys.Kind(kind), v)
			for _, os := range []keys.OS{keys.Win, keys.Mac, 9} {
				name := s.Name(os)
				if name == "" || strings.HasPrefix(name, "<") == s.Known() {
					t.Fatalf("%+v on %s: name %q, known %v", s, os, name, s.Known())
				}
			}
		}
	}
}

func TestKeyTableNames(t *testing.T) {
	for _, k := range keys.All() {
		s := k.Stroke()
		if !s.Known() {
			t.Errorf("%s: stroke %+v unknown", k.Code, s)
		}
		back, ok := s.Key()
		if !ok || back != k {
			t.Errorf("%s: Stroke().Key() = %+v, %v", k.Code, back, ok)
		}
		for _, os := range []keys.OS{keys.Win, keys.Mac} {
			name := k.Name(os)
			if name == "" || name != strings.Join(strings.Fields(name), " ") {
				t.Errorf("%s on %s: name %q", k.Code, os, name)
			}
		}
		if k.Kind != keys.KindModifier && (k.Name(keys.Win) != strings.Join(strings.Fields(k.Win), " ") || k.Name(keys.Mac) != strings.Join(strings.Fields(k.Mac), " ")) {
			t.Errorf("%s: names %q/%q differ from the table's %q/%q", k.Code, k.Name(keys.Win), k.Name(keys.Mac), k.Win, k.Mac)
		}
	}
	if _, ok := st(keys.KindConsumer, 0x06).Key(); ok {
		t.Error("consumer stroke resolved to a key table entry")
	}
}

func TestComboFormat(t *testing.T) {
	cases := []struct {
		combo    keys.Combo
		win, mac string
	}{
		{nil, "", ""},
		{keys.Combo{keys.LMeta.Stroke(), st(keys.KindKey, 0x06)}, "Win+C", "Cmd+C"},
		{keys.Combo{keys.LCtrl.Stroke(), keys.LAlt.Stroke(), st(keys.KindKey, 0x4C)}, "Ctrl+Alt+Del", "Ctrl+Option+Del"},
		{keys.Combo{keys.RCtrl.Stroke(), st(keys.KindMenu, 1)}, "RCtrl+Menu", "RCtrl+Menu"},
		{keys.Combo{keys.LShift.Stroke(), st(keys.KindKey, 0xE3)}, "Shift+<key 0xE3>", "Shift+<key 0xE3>"},
		{keys.Combo{st(keys.KindConsumer, 0x00E9)}, "Volume Up", "Volume Up"},
	}
	for _, c := range cases {
		if win, mac := c.combo.Format(keys.Win), c.combo.Format(keys.Mac); win != c.win || mac != c.mac {
			t.Errorf("%+v: %q/%q, want %q/%q", c.combo, win, mac, c.win, c.mac)
		}
	}
}
