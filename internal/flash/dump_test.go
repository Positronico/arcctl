package flash

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func loadDump(t testing.TB) *Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "flash-dump.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 256 {
		t.Fatalf("flash-dump.bin has %d bytes, want 256", len(b))
	}
	im, err := FromDump(0, b)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var dumpPairs = []struct {
	name  string
	addr  int
	state FieldState
	value byte
}{
	{"ReportRate", 0, OK, 4},
	{"maxDpiStage", 2, OK, 6},
	{"CurrentDPI", 4, OK, 3},
	{"unmapped 6", 6, OK, 0},
	{"KeyOperation", 8, OK, 0},
	{"LOD", 10, OK, 0},
	{"DPIEffectMode", 76, OK, 0},
	{"DPIEffectBrightness", 78, OK, 0x80},
	{"DPIEffectSpeed", 80, OK, 3},
	{"DPIEffectState", 82, OK, 0},
	{"unmapped 88", 88, OK, 0},
	{"unmapped 90", 90, OK, 0x80},
	{"unmapped 92", 92, OK, 3},
	{"LightPowerSave", 94, OK, 0},
	{"LightState", 167, OK, 1},
	{"DebounceTime", 169, OK, 2},
	{"MotionSync", 171, OK, 0},
	{"SleepTime", 173, OK, 1},
	{"Angle", 175, OK, 0},
	{"Ripple", 177, OK, 0},
	{"MovingOffLight", 179, OK, 0},
	{"PerformanceState", 181, OK, 4},
	{"Performance", 183, OK, 6},
	{"SensorMode", 185, Invalid, 3},
	{"unmapped 187", 187, OK, 0},
	{"AngleTune", 189, Invalid, 0},
	{"AngleTuneState", 191, Erased, 0xFF},
	{"SensorFPS20K", 225, Erased, 0xFF},
	{"WheelDebounceTime", 227, Erased, 0xFF},
	{"DebounceReleaseTime", 229, Erased, 0xFF},
	{"FlywheelState", 233, Erased, 0xFF},
	{"LeftTrigger", 239, Erased, 0xFF},
	{"LeftFastTrigger", 241, Erased, 0xFF},
	{"LeftTactileFeedback", 243, Erased, 0xFF},
	{"RightTrigger", 245, Erased, 0xFF},
	{"RightFastTrigger", 247, Erased, 0xFF},
	{"RightTactileFeedback", 249, Erased, 0xFF},
}

var dumpRecords = []struct {
	name  string
	ext   Extent
	state FieldState
	hex   string
}{
	{"DPIValue 0", Extent{12, 4}, OK, "15 15 00 2b"},
	{"DPIValue 1", Extent{16, 4}, OK, "1f 1f 00 17"},
	{"DPIValue 2", Extent{20, 4}, OK, "2a 2a 00 01"},
	{"DPIValue 3", Extent{24, 4}, OK, "3f 3f 00 d7"},
	{"DPIValue 4", Extent{28, 4}, OK, "54 54 00 ad"},
	{"DPIValue 5", Extent{32, 4}, OK, "3f 3f 01 d6"},
	{"DPIValue 6", Extent{36, 4}, OK, "69 69 00 83"},
	{"DPIValue 7", Extent{40, 4}, OK, "69 69 00 83"},
	{"DPIColor 0", Extent{44, 4}, OK, "ff 00 00 56"},
	{"DPIColor 1", Extent{48, 4}, OK, "00 00 ff 56"},
	{"DPIColor 2", Extent{52, 4}, OK, "00 ff 00 56"},
	{"DPIColor 3", Extent{56, 4}, OK, "ff ff 00 57"},
	{"DPIColor 4", Extent{60, 4}, OK, "ff 80 00 d6"},
	{"DPIColor 5", Extent{64, 4}, OK, "ff 00 ff 57"},
	{"DPIColor 6", Extent{68, 4}, OK, "ff 7d 7d 5c"},
	{"DPIColor 7", Extent{72, 4}, OK, "7d 7d ff 5c"},
	{"unmapped colour 84", Extent{84, 4}, OK, "ff 00 ff 57"},
	{"KeyFunction 0 Left", Extent{96, 4}, OK, "01 01 00 53"},
	{"KeyFunction 1 Right", Extent{100, 4}, OK, "01 02 00 52"},
	{"KeyFunction 2 shortcut", Extent{104, 4}, OK, "05 00 00 50"},
	{"KeyFunction 3 shortcut", Extent{108, 4}, OK, "05 00 00 50"},
	{"KeyFunction 4 shortcut", Extent{112, 4}, OK, "05 00 00 50"},
	{"KeyFunction 5 shortcut", Extent{116, 4}, OK, "05 00 00 50"},
	{"KeyFunction 6 ProfileSwitch", Extent{120, 4}, OK, "09 00 00 4c"},
	{"KeyFunction 7 DPI cycle", Extent{124, 4}, OK, "02 01 00 52"},
	{"KeyFunction 8 Scroll Left", Extent{128, 4}, OK, "03 01 00 51"},
	{"KeyFunction 9 Scroll Right", Extent{132, 4}, OK, "03 02 00 50"},
	{"KeyFunction 10 DPI+", Extent{136, 4}, OK, "02 02 00 51"},
	{"KeyFunction 11 DPI-", Extent{140, 4}, OK, "02 03 00 50"},
	{"KeyFunction 12 Disable", Extent{144, 4}, OK, "00 00 00 55"},
	{"KeyFunction 13 Disable", Extent{148, 4}, OK, "00 00 00 55"},
	{"KeyFunction 14 Disable", Extent{152, 4}, OK, "00 00 00 55"},
	{"KeyFunction 15 Disable", Extent{156, 4}, OK, "00 00 00 55"},
	{"Light", Extent{160, 7}, OK, "01 ff 00 00 07 02 4c"},
	{"Flywheel", Extent{235, 4}, Erased, "ff ff ff ff"},
	{"ShortcutKey 2 header", Extent{256 + 32*2, 1}, Unknown, "ff"},
	{"Macro 0 name block", Extent{768, 10}, Unknown, "ff ff ff ff ff ff ff ff ff ff"},
	{"Sensor3955DPI 0", Extent{6912, 6}, Unknown, "ff ff ff ff ff ff"},
}

func TestDumpPairs(t *testing.T) {
	im := loadDump(t)
	for _, tt := range dumpPairs {
		p, s := im.Pair(tt.addr)
		if s != tt.state || p.Value() != tt.value {
			t.Errorf("%s @%d: % x = %v %#x, want %v %#x", tt.name, tt.addr, p[:], s, p.Value(), tt.state, tt.value)
		}
	}
}

func TestDumpRecords(t *testing.T) {
	im := loadDump(t)
	for _, tt := range dumpRecords {
		r, s := im.Record(tt.ext)
		if want := unhex(t, tt.hex); s != tt.state || !bytes.Equal(r, want) {
			t.Errorf("%s @%v: % x %v, want % x %v", tt.name, tt.ext, []byte(r), s, want, tt.state)
		}
		if s == OK && !bytes.Equal(NewRecord(r.Body()...), r) {
			t.Errorf("%s: re-encoding changed the record", tt.name)
		}
	}
}

func TestDumpKnown(t *testing.T) {
	im := loadDump(t)
	if got, want := im.KnownExtents(), []Extent{{0, 256}}; !slices.Equal(got, want) {
		t.Errorf("KnownExtents = %v, want %v", got, want)
	}
	if b := im.Bytes(); !all(b[256:], 0xFF) {
		t.Error("bytes past the dump are not 0xFF")
	}
	if b, _ := im.Get(Extent{192, 64}); !all(b, 0xFF) {
		t.Error("192..255 are not erased")
	}
}

func dumpExtents() []Extent {
	var out []Extent
	for _, p := range dumpPairs {
		if !strings.HasPrefix(p.name, "unmapped") {
			out = append(out, Extent{p.addr, 2})
		}
	}
	for _, r := range dumpRecords {
		if !strings.HasPrefix(r.name, "unmapped") {
			out = append(out, r.ext)
		}
	}
	return out
}

func TestDumpDiff(t *testing.T) {
	from := loadDump(t)
	to := from.Clone()
	writes := []struct {
		addr int
		b    []byte
	}{
		{4, unhex(t, "03 52")},
		{20, NewRecord(0x2f, 0x2f, 0x00)},
		{108, NewRecord(0x05, 0x00, 0x00)},
		{167, unhex(t, "00 55")},
		{256 + 32*3, NewRecord(0x02, 0x82, 0xcd, 0x00, 0x42, 0xcd, 0x00)},
	}
	for _, w := range writes {
		if err := to.Set(w.addr, w.b); err != nil {
			t.Fatal(err)
		}
	}
	extents := append(dumpExtents(), Extent{256 + 32*3, 8})
	changed, unread := Diff(from, to, extents)
	if want := []Extent{{167, 2}, {20, 4}}; !slices.Equal(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}
	if want := []Extent{{352, 8}}; !slices.Equal(unread, want) {
		t.Errorf("unread = %v, want %v", unread, want)
	}
	preserved := []Extent{{6, 2}, {84, 12}, {181, 2}, {185, 2}, {187, 2}}
	for _, e := range preserved {
		a, _ := from.Get(e)
		b, _ := to.Get(e)
		if !bytes.Equal(a, b) {
			t.Errorf("%v changed", e)
		}
		for _, c := range changed {
			if c.Overlaps(e) {
				t.Errorf("changed extent %v overlaps preserved %v", c, e)
			}
		}
	}
	if changed, unread := Diff(from, from.Clone(), extents); changed != nil || unread != nil {
		t.Errorf("self diff = %v, %v", changed, unread)
	}
}
