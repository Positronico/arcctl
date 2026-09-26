package catalog_test

import (
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/keyboard"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/vectors"
)

func TestKeyTable(t *testing.T) {
	all := keys.All()
	if len(all) != 115 {
		t.Errorf("%d keys, want 115", len(all))
	}
	cases := []struct {
		code  string
		kind  keys.Kind
		value uint8
		win   string
		mac   string
	}{
		{"Escape", 1, 0x29, "Esc", "Esc"},
		{"Equal", 1, 0x2E, "+", "+"},
		{"MetaLeft", 0, 0x08, "LWin", "Command"},
		{"AltRight", 0, 0x40, "RAlt", "Option"},
		{"ContextMenu", 7, 0x01, "Menu", "Menu"},
	}
	for _, tc := range cases {
		k, ok := keys.ByCode(tc.code)
		if !ok || k.Kind != tc.kind || k.Value != tc.value || k.Win != tc.win || k.Mac != tc.mac {
			t.Errorf("ByCode(%s) = %+v, %v", tc.code, k, ok)
		}
		if byValue, ok := keys.ByValue(tc.kind, tc.value); !ok || byValue != k {
			t.Errorf("ByValue(%d, %#x) = %+v, want %+v", tc.kind, tc.value, byValue, k)
		}
	}
	if _, ok := keys.ByCode("Hyper"); ok {
		t.Error("ByCode(Hyper) found a key")
	}
	all[0].Code = "changed"
	if keys.All()[0].Code == "changed" {
		t.Error("All returns the package table")
	}
}

func presetStrokes(p keys.Preset) [][2]byte {
	var s [][2]byte
	for _, k := range p.Keys {
		s = append(s, [2]byte{byte(k.Kind), k.Value})
	}
	return s
}

func shortcutPresses(t *testing.T, body []byte) [][2]byte {
	t.Helper()
	n := int(body[0]) / 2
	var s [][2]byte
	for i := range n {
		ev := body[1+3*i : 4+3*i]
		if ev[0]&0xC0 != 0x80 || ev[2] != 0 {
			t.Fatalf("event %d % x is not a key press", i, ev)
		}
		s = append(s, [2]byte{ev[0] & 0x0F, ev[1]})
	}
	return s
}

func TestPresetsMatchGoldenVectors(t *testing.T) {
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	win, mac := keys.WinPresets(), keys.MacPresets()
	if len(win) != 16 || len(mac) != 15 {
		t.Errorf("%d win and %d mac presets, want 16 and 15", len(win), len(mac))
	}
	find := func(ps []keys.Preset, id string) keys.Preset {
		i := slices.IndexFunc(ps, func(p keys.Preset) bool { return p.ID == id })
		if i < 0 {
			t.Fatalf("no preset %s", id)
		}
		return ps[i]
	}
	cases := []struct {
		vector string
		preset keys.Preset
		label  string
	}{
		{"P0 win diy5 (LWin, LShift, S)", find(win, "diy5"), "Screenshot"},
		{"P0 mac diy5 (LShift, LWin, 3)", find(mac, "diy5"), "Screenshot"},
		{"P0 mac diy17 (LCtrl, LWin, Q)", find(mac, "diy17"), "Lock"},
	}
	for _, tc := range cases {
		i := slices.IndexFunc(f.Vectors, func(v vectors.Vector) bool { return v.Name == tc.vector })
		if i < 0 {
			t.Fatalf("vector %q not found", tc.vector)
		}
		body, err := f.Vectors[i].Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := presetStrokes(tc.preset), shortcutPresses(t, body); !slices.Equal(got, want) {
			t.Errorf("%s: preset presses %v, vector presses %v", tc.vector, got, want)
		}
		if tc.preset.Label != tc.label {
			t.Errorf("%s: label %q, want %q", tc.vector, tc.preset.Label, tc.label)
		}
	}
	for _, id := range []string{"diy4", "diy13"} {
		if slices.ContainsFunc(mac, func(p keys.Preset) bool { return p.ID == id }) {
			t.Errorf("mac presets include %s", id)
		}
	}
	if slices.ContainsFunc(win, func(p keys.Preset) bool { return p.ID == "diy13" }) {
		t.Error("win presets include diy13")
	}
	win[0].Keys[0].Code = "changed"
	if keys.WinPresets()[0].Keys[0].Code == "changed" {
		t.Error("WinPresets shares its key slices")
	}
}

func TestRepeatOptions(t *testing.T) {
	want := []struct {
		value uint8
		mode  keys.RepeatMode
	}{
		{254, keys.RepeatUntilReleased},
		{255, keys.RepeatUntilAnyKey},
		{253, keys.RepeatUntilPressedAgain},
		{1, keys.RepeatCount},
	}
	got := keys.RepeatOptions()
	if len(got) != len(want) {
		t.Fatalf("%d repeat options, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Value != w.value || got[i].Mode != w.mode || got[i].Label == "" {
			t.Errorf("repeat option %d = %+v, want value %d mode %d", i, got[i], w.value, w.mode)
		}
	}
}

func TestMouseOffsets(t *testing.T) {
	cases := []struct {
		name      string
		got, want int
	}{
		{"ReportRate", mouse.AddrReportRate, 0},
		{"maxDpiStage", mouse.AddrMaxDpiStage, 2},
		{"CurrentDPI", mouse.AddrCurrentDPI, 4},
		{"KeyOperation", mouse.AddrKeyOperation, 8},
		{"DPIValue", mouse.AddrDPIValue, 12},
		{"DPIColor", mouse.AddrDPIColor, 44},
		{"KeyFunction", mouse.AddrKeyFunction, 96},
		{"SensorMode", mouse.AddrSensorMode, 185},
		{"FlywheelState", mouse.AddrFlywheelState, 233},
		{"FlywheelMaxSpeed", mouse.AddrFlywheelMaxSpeed, 235},
		{"ShortcutKey", mouse.AddrShortcutKey, 256},
		{"Macro", mouse.AddrMacro, 768},
		{"Sensor3955DPI", mouse.AddrSensor3955DPI, 6912},
		{"EndEeprom", mouse.AddrEndEeprom, 6987},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestDPILightBrightness(t *testing.T) {
	levels := []byte{16, 30, 60, 90, 128, 150, 180, 210, 230, 255}
	for i, want := range levels {
		level := i + 1
		if got := mouse.DPILightBrightness(level); got != want {
			t.Errorf("DPILightBrightness(%d) = %d, want %d", level, got, want)
		}
		if got, ok := mouse.DPILightLevel(want); !ok || got != level {
			t.Errorf("DPILightLevel(%d) = %d, %v, want %d", want, got, ok, level)
		}
	}
	for _, level := range []int{-1, 0, 11, 255} {
		if got := mouse.DPILightBrightness(level); got != 128 {
			t.Errorf("DPILightBrightness(%d) = %d, want the default 128", level, got)
		}
	}
	for _, b := range []byte{0, 17, 120, 240} {
		if _, ok := mouse.DPILightLevel(b); ok {
			t.Errorf("DPILightLevel(%d) found a level", b)
		}
	}
}

func TestKeyboardOffsets(t *testing.T) {
	std, office := keyboard.StandardOffsets(), keyboard.OfficeOffsets()
	cases := []struct {
		name      string
		got, want int
	}{
		{"standard CustomLightMaps", std.CustomLightMaps, 6176},
		{"standard CurrentLightMode", std.CurrentLightMode, 8496},
		{"standard EffectPara", std.EffectPara, 8544},
		{"standard ReportRate", std.ReportRate, 9408},
		{"standard CurrentSystem", std.CurrentSystem, 9416},
		{"standard End", std.End, 9430},
		{"standard SyncCRC", std.SyncCRC, 9504},
		{"standard SyncCRCLen", std.SyncCRCLen, 8},
		{"standard Macro", std.Macro, 10064},
		{"standard SystemKeysSize", std.SystemKeysSize, 1536},
		{"standard LayoutKeysSize", std.LayoutKeysSize, 512},
		{"standard OfficeCustomParam", std.OfficeCustomParam, keyboard.NoOffset},
		{"office CustomLightMaps", office.CustomLightMaps, keyboard.NoOffset},
		{"office TapeParam", office.TapeParam, keyboard.NoOffset},
		{"office End", office.End, 5349},
		{"office SystemKeysSize", office.SystemKeysSize, 1152},
		{"office OfficeCustomLights", office.OfficeCustomLights, 11540},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	if std.End <= std.ReportRate {
		t.Error("the standard settings end is not past the settings start")
	}
}
