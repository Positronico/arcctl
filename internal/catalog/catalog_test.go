package catalog_test

import (
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		cid, mid byte
		key      string
		name     string
		alias    string
		group    string
		family   catalog.Family
	}{
		{0x7B, 1, "7B01", "ProtoArc EM25", "", catalog.GroupMouse, catalog.FamilyMouse},
		{0x7B, 2, "7B02", "", "EM06 (inferred)", catalog.GroupMouse, catalog.FamilyMouse},
		{0x7B, 3, "7B03", "", "Vertical mouse", catalog.GroupMouse, catalog.FamilyMouse},
		{0x7B, 4, "7B04", "ProtoArc EM11 Pro", "", catalog.GroupOfficeMouse, catalog.FamilyMouse},
		{0x7B, 5, "7B05", "ProtoArc EM16 Pro", "", catalog.GroupOfficeMouse, catalog.FamilyMouse},
		{0x7B, 6, "7B06", "ProtoArc EM11 Pro", "", catalog.GroupOfficeMouse, catalog.FamilyMouse},
		{0x03, 1, "0301", "", "Full-size keyboard", catalog.GroupKeyboard, catalog.FamilyKeyboard},
		{0x03, 3, "0301", "", "Full-size keyboard", catalog.GroupKeyboard, catalog.FamilyKeyboard},
		{0x03, 4, "0304", "", "Compact keyboard with knob", catalog.GroupKeyboard, catalog.FamilyKeyboard},
	}
	for _, tc := range cases {
		m, ok := catalog.Resolve(tc.cid, tc.mid)
		if !ok {
			t.Errorf("Resolve(%#x, %d) found nothing, want %s", tc.cid, tc.mid, tc.key)
			continue
		}
		if m.Key != tc.key || m.Name != tc.name || m.Alias != tc.alias || m.Group != tc.group || m.Family != tc.family {
			t.Errorf("Resolve(%#x, %d) = %s %q %q %s %v, want %s %q %q %s %v", tc.cid, tc.mid,
				m.Key, m.Name, m.Alias, m.Group, m.Family, tc.key, tc.name, tc.alias, tc.group, tc.family)
		}
		if m.CID != tc.cid || !slices.Contains(m.MIDs, tc.mid) {
			t.Errorf("%s: cid %#x mids %v do not cover (%#x, %d)", m.Key, m.CID, m.MIDs, tc.cid, tc.mid)
		}
		if byKey, ok := catalog.ByKey(tc.key); !ok || byKey != m {
			t.Errorf("ByKey(%s) does not return the resolved model", tc.key)
		}
	}
	unknown := [][2]byte{{0x7B, 0}, {0x7B, 7}, {0x7B, 0xFF}, {0, 0}, {0x03, 0}, {0x03, 2}, {0x03, 5}, {0x74, 1}, {0x04, 4}}
	for _, id := range unknown {
		if m, ok := catalog.Resolve(id[0], id[1]); ok || m != nil {
			t.Errorf("Resolve(%#x, %d) = %v, %v, want nil, false", id[0], id[1], m, ok)
		}
	}
	if _, ok := catalog.ByKey("7B07"); ok {
		t.Error("ByKey(7B07) found a model")
	}
}

func TestModelsCoverEveryCfgEntry(t *testing.T) {
	var keys []string
	for _, m := range catalog.Models() {
		keys = append(keys, m.Key)
		if m.Name == "" && m.Alias == "" {
			t.Errorf("%s has neither a name nor an alias", m.Key)
		}
	}
	want := []string{"7B01", "7B02", "7B03", "7B04", "7B05", "7B06", "0301", "0304"}
	if !slices.Equal(keys, want) {
		t.Errorf("models %v, want %v", keys, want)
	}
	ms := catalog.Models()
	ms[0] = nil
	if catalog.Models()[0] == nil {
		t.Error("Models returns the package slice")
	}
}

type button struct {
	slot    int
	typ     byte
	param   uint16
	media   uint16
	visible bool
	label   string
}

var (
	left     = button{0, 0x01, 0x0100, 0, true, "Left Click"}
	right    = button{1, 0x01, 0x0200, 0, true, "Right Click"}
	middle   = button{2, 0x01, 0x0400, 0, true, "Wheel Click"}
	forward  = button{4, 0x01, 0x1000, 0, true, "Forward"}
	backward = button{3, 0x01, 0x0800, 0, true, "Backward"}
	dpiCycle = button{5, 0x02, 0x0100, 0, true, "DPI Cycle"}
	common   = []button{left, right, middle, forward, backward, dpiCycle}
)

func TestMouseModels(t *testing.T) {
	cases := []struct {
		key       string
		sensor    string
		maxDPI    int
		stages    int
		current   int
		longRange bool
		buttons   []button
	}{
		{"7B01", "3104", 8000, 7, 1, false, common},
		{"7B02", "3212", 1600, 5, 1, false, append(slices.Clone(common),
			button{11, 0x00, 0x0000, 0, true, "Do nothing"},
			button{8, 0x08, 0x0500, 0, true, "Drag Scroll"})},
		{"7B03", "3104", 8000, 7, 1, true, common},
		{"7B04", "3104", 7200, 6, 2, true, common},
		{"7B05", "3212", 2400, 3, 1, false, append(slices.Clone(common),
			button{15, 0x0B, 0x0200, 0, false, "Scroll Down"},
			button{14, 0x05, 0x0000, 0x0183, false, "Media Player"})},
		{"7B06", "3104", 7200, 6, 2, true, append(slices.Clone(common),
			button{12, 0x03, 0x0100, 0, true, "Scroll Left"},
			button{13, 0x03, 0x0200, 0, true, "Scroll Right"})},
	}
	for _, tc := range cases {
		m, ok := catalog.ByKey(tc.key)
		if !ok {
			t.Fatalf("no model %s", tc.key)
		}
		if m.Sensor == nil || m.Sensor.ID != tc.sensor || m.MaxDPI != tc.maxDPI || m.Stages != tc.stages {
			t.Errorf("%s: sensor %v, max %d, stages %d, want %s, %d, %d", tc.key, m.Sensor, m.MaxDPI, m.Stages, tc.sensor, tc.maxDPI, tc.stages)
		}
		if m.Defaults == nil || len(m.Defaults.DPIs) != tc.stages || m.Defaults.CurrentDPI != tc.current {
			t.Errorf("%s: defaults %+v, want %d stages, current %d", tc.key, m.Defaults, tc.stages, tc.current)
		}
		if m.LongRangeDefault == nil || *m.LongRangeDefault != tc.longRange {
			t.Errorf("%s: long range default %v, want %v", tc.key, m.LongRangeDefault, tc.longRange)
		}
		if m.Keyboard != nil || m.PairCID != 0x7B {
			t.Errorf("%s: keyboard %v, pairing cid %#x", tc.key, m.Keyboard, m.PairCID)
		}
		got := make([]button, len(m.Buttons))
		for i, b := range m.Buttons {
			got[i] = button{b.Slot, b.Type, b.Param, b.Media, b.Visible, b.Label}
		}
		if !slices.Equal(got, tc.buttons) {
			t.Errorf("%s: buttons\n%v\nwant\n%v", tc.key, got, tc.buttons)
		}
	}
}

func TestEM11Defaults(t *testing.T) {
	m, _ := catalog.Resolve(0x7B, 4)
	d := m.Defaults
	var dpis []int
	for _, s := range d.DPIs {
		dpis = append(dpis, s.DPI)
	}
	if !slices.Equal(dpis, []int{800, 1200, 1600, 2400, 3200, 4800}) {
		t.Errorf("EM11 default DPIs %v", dpis)
	}
	if d.DPIs[0].Color != [3]byte{0xFF, 0, 0} || d.ReportRate != 250 || d.LOD == nil || *d.LOD != 1 || d.Firmware != nil {
		t.Errorf("EM11 defaults %+v", d)
	}
	if em25, _ := catalog.ByKey("7B01"); em25.Defaults.LOD != nil || em25.Defaults.Firmware["device"] != "v2.18" {
		t.Errorf("EM25 LOD %v, firmware %v", em25.Defaults.LOD, em25.Defaults.Firmware)
	}
}

func TestUIFlags(t *testing.T) {
	want := map[string]catalog.UIFlags{
		"7B01": {ProfilesUI: true, DPILight: true, RatePanel: true},
		"7B02": {Office: true, GameRoller: true, ProfilesUI: true},
		"7B03": {ProfilesUI: true, DPILight: true, RatePanel: true},
		"7B04": {Office: true},
		"7B05": {Office: true, Flywheel: true},
		"7B06": {Office: true},
		"0301": {},
		"0304": {OSSwitchLocked: true},
	}
	for _, m := range catalog.Models() {
		if m.UI != want[m.Key] {
			t.Errorf("%s: UI %+v, want %+v", m.Key, m.UI, want[m.Key])
		}
	}
}

func TestKeyboards(t *testing.T) {
	for _, key := range []string{"0301", "0304"} {
		m, _ := catalog.ByKey(key)
		k := m.Keyboard
		if k == nil || m.Sensor != nil || m.Buttons != nil || m.Defaults != nil || m.LongRangeDefault != nil || m.PairCID != 0x03 {
			t.Fatalf("%s: %+v", key, m)
		}
		if !slices.Equal(k.Systems, []string{"win", "mac"}) || !slices.Equal(k.Layouts, []string{"normal", "fn"}) {
			t.Errorf("%s: systems %v, layouts %v", key, k.Systems, k.Layouts)
		}
		if len(k.Rects) != 128 || len(k.Maps) != 4 {
			t.Errorf("%s: %d rects, %d layers", key, len(k.Rects), len(k.Maps))
		}
		for _, l := range k.Maps {
			if len(l.Slots) != len(k.Rects) {
				t.Errorf("%s %s/%s: %d slots", key, l.System, l.Layout, len(l.Slots))
			}
		}
		if _, ok := k.Layer("iOS", "normal"); ok {
			t.Errorf("%s has an iOS layer", key)
		}
	}
	full, _ := catalog.ByKey("0301")
	win, _ := full.Keyboard.Layer("win", "normal")
	if s := win.Slots[1]; s.Type != 0x82 || s.Value != 0x3A {
		t.Errorf("0301 win/normal slot 1 (F1) = %+v, want flagged basic key 0x3A", s)
	}
}

func TestKeyboardOffImageSlots(t *testing.T) {
	cases := []struct {
		key      string
		onImage  int
		occupied []int
	}{
		{"0301", 108, nil},
		{"0304", 97, []int{61, 83}},
	}
	for _, tc := range cases {
		m, _ := catalog.ByKey(tc.key)
		k := m.Keyboard
		var on int
		var occupied []int
		for slot := range k.Rects {
			if k.OnImage(slot) {
				on++
				continue
			}
			for _, l := range k.Maps {
				if s := l.Slots[slot]; s.Type != 0 || s.Value != 0 {
					occupied = append(occupied, slot)
					break
				}
			}
		}
		if on != tc.onImage || !slices.Equal(occupied, tc.occupied) {
			t.Errorf("%s: %d slots on the picture, off-picture slots with defaults %v; want %d, %v", tc.key, on, occupied, tc.onImage, tc.occupied)
		}
		if k.OnImage(-1) || k.OnImage(len(k.Rects)) {
			t.Errorf("%s: OnImage accepts a slot outside the table", tc.key)
		}
	}
}

func TestKeyboardLocks(t *testing.T) {
	cases := []struct {
		key, system, layout string
		locked              []int
	}{
		{"0301", "win", "normal", []int{61, 76, 77, 88}},
		{"0301", "win", "fn", []int{0, 3, 4, 29, 61, 75, 76, 77, 78, 88, 93, 94, 95}},
		{"0301", "mac", "normal", []int{5, 6, 61, 76, 77, 88}},
		{"0301", "mac", "fn", []int{0, 4, 29, 61, 75, 76, 77, 78, 88, 93, 94, 95}},
		{"0304", "win", "normal", []int{88}},
		{"0304", "win", "fn", []int{0, 3, 4, 17, 18, 19, 29, 33, 75, 78, 88, 90, 93, 94, 95}},
		{"0304", "mac", "normal", []int{5, 6, 88}},
		{"0304", "mac", "fn", []int{0, 4, 17, 18, 19, 29, 33, 75, 78, 88, 90, 93, 94, 95}},
	}
	for _, tc := range cases {
		m, _ := catalog.ByKey(tc.key)
		var locked []int
		for slot := range m.Keyboard.Rects {
			if m.Keyboard.Locked(tc.system, tc.layout, slot) {
				locked = append(locked, slot)
			}
		}
		if !slices.Equal(locked, tc.locked) {
			t.Errorf("%s %s/%s locked %v, want %v", tc.key, tc.system, tc.layout, locked, tc.locked)
		}
	}
	m, _ := catalog.ByKey("0301")
	if !m.Keyboard.Locked("iOS", "normal", 0) || !m.Keyboard.Locked("win", "normal", 128) || !m.Keyboard.Locked("win", "normal", -1) {
		t.Error("a layer or slot the catalog does not know is not locked")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		vid, pid uint16
		want     catalog.PIDClass
	}{
		{0x260D, 0x1282, catalog.ClassComposite},
		{0x062A, 0x1282, catalog.ClassComposite},
		{0x3554, 0xF819, catalog.ClassComposite},
		{0x260D, 0x1291, catalog.ClassComposite},
		{0x260D, 0x1213, catalog.ClassMouse},
		{0x062A, 0x1213, catalog.ClassMouse},
		{0x3554, 0xF511, catalog.ClassMouse},
		{0x260D, 0x1326, catalog.ClassMouse},
		{0x260D, 0xFA0A, catalog.ClassKeyboard},
		{0x3554, 0xF809, catalog.ClassKeyboard},
		{0x260D, 0x0000, catalog.ClassUnknown},
		{0x3554, 0xF808, catalog.ClassUnknown},
		{0x62A0, 0x1282, catalog.ClassUnknown},
		{0x046D, 0x1282, catalog.ClassUnknown},
		{0x0000, 0x0000, catalog.ClassUnknown},
	}
	for _, tc := range cases {
		if got := catalog.Classify(tc.vid, tc.pid); got != tc.want {
			t.Errorf("Classify(%04x, %04x) = %v, want %v", tc.vid, tc.pid, got, tc.want)
		}
	}
	groups := map[catalog.PIDClass]string{
		catalog.ClassMouse:     catalog.GroupMouse,
		catalog.ClassKeyboard:  catalog.GroupKeyboard,
		catalog.ClassComposite: catalog.GroupOfficeMouse,
		catalog.ClassUnknown:   "",
	}
	for c, g := range groups {
		if c.Group() != g {
			t.Errorf("%v.Group() = %q, want %q", c, c.Group(), g)
		}
	}
}

func TestUSBIDs(t *testing.T) {
	ids := catalog.USBIDs()
	if len(ids) != 48 {
		t.Errorf("%d USB IDs, want 3 VIDs x 16 PIDs", len(ids))
	}
	seen := map[[2]uint16]bool{}
	for _, id := range ids {
		k := [2]uint16{id.VID, id.PID}
		if seen[k] {
			t.Errorf("duplicate %04x:%04x", id.VID, id.PID)
		}
		seen[k] = true
		if got := catalog.Classify(id.VID, id.PID); got != id.Class || got == catalog.ClassUnknown {
			t.Errorf("%04x:%04x has class %v, Classify says %v", id.VID, id.PID, id.Class, got)
		}
	}
}

func TestSensors(t *testing.T) {
	if n := len(catalog.Sensors()); n != 10 {
		t.Errorf("%d sensors, want 10", n)
	}
	s, ok := catalog.SensorByID("3104")
	if !ok {
		t.Fatal("no sensor 3104")
	}
	want := []catalog.Range{{Min: 200, Max: 4000, Step: 100, DPIEx: 0x00}, {Min: 4200, Max: 8000, Step: 200, DPIEx: 0x11}}
	if !slices.Equal(s.Ranges, want) || len(s.Values) != 39 || s.Values[6] != 21 {
		t.Errorf("3104: ranges %v, %d values", s.Ranges, len(s.Values))
	}
	if s.HasGate("LOD") || s.HasGate("MotionSync") {
		t.Errorf("3104 is in gates %v", s.Gates)
	}
	if s, _ := catalog.SensorByID("3335"); !s.HasGate("LOD") || s.HasGate("MotionSync") {
		t.Errorf("3335 gates %v", s.Gates)
	}
	if s, _ := catalog.SensorByID("3950"); s.Values != nil || !s.HasGate("MotionSync") {
		t.Errorf("3950: values %v, gates %v", s.Values, s.Gates)
	}
	if _, ok := catalog.SensorByID("9999"); ok {
		t.Error("SensorByID(9999) found a sensor")
	}
}

func TestMedia(t *testing.T) {
	mouse := catalog.MouseMedia()
	if len(mouse) != 17 || mouse[0].Code != 0x0183 || mouse[0].Label != "Media Player" {
		t.Errorf("mouse media %v", mouse)
	}
	if kb := catalog.KeyboardMedia(); len(kb) != 19 || kb[0].Code != 0x006F {
		t.Errorf("keyboard media %v", kb)
	}
	cases := []struct {
		code  uint16
		name  string
		label string
	}{
		{0x00CD, "Play/Pause", "Play/Pause"},
		{0x0224, "AC Back", "Browser Back"},
		{0x0225, "AC Forward", "Browser Forward"},
	}
	for _, tc := range cases {
		u, ok := catalog.Media(tc.code)
		if !ok || u.Name != tc.name || u.Label != tc.label {
			t.Errorf("Media(%#04x) = %+v, %v", tc.code, u, ok)
		}
	}
	if _, ok := catalog.Media(0); ok {
		t.Error("Media(0) found a usage")
	}
}

func TestLabels(t *testing.T) {
	if s, ok := catalog.Label("button.left"); !ok || s != "Left Click" {
		t.Errorf("Left Click label = %q, %v", s, ok)
	}
	if s, ok := catalog.Label("tab.buttons"); !ok || s != "Button Mapping" {
		t.Errorf("tab label = %q, %v", s, ok)
	}
	for _, k := range []string{"menu.Options[1].items[0x0100]", "light.off_time"} {
		if _, ok := catalog.Label(k); ok {
			t.Errorf("%q resolved", k)
		}
	}
}

func TestSources(t *testing.T) {
	if catalog.FactsSchema != 1 || catalog.CfgVersion == "" || catalog.Bundle == "" {
		t.Errorf("schema %d, cfg %q, bundle %q", catalog.FactsSchema, catalog.CfgVersion, catalog.Bundle)
	}
	inputs := catalog.Inputs()
	if len(inputs) == 0 || len(catalog.Tools()) == 0 {
		t.Fatal("no inputs or tools recorded")
	}
	for _, in := range inputs {
		if len(in.SHA256) != 64 {
			t.Errorf("%s: hash %q", in.Path, in.SHA256)
		}
	}
	inputs[0].Path = "changed"
	if catalog.Inputs()[0].Path == "changed" {
		t.Error("Inputs returns the package slice")
	}
}

func TestStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{catalog.Off.String(), "off"},
		{catalog.ReadOnly.String(), "read-only"},
		{catalog.Experimental.String(), "experimental"},
		{catalog.Untested.String(), "untested"},
		{catalog.Verified.String(), "verified"},
		{catalog.Tier(9).String(), "Tier(9)"},
		{catalog.FamilyMouse.String(), "mouse"},
		{catalog.FamilyKeyboard.String(), "keyboard"},
		{catalog.Family(0).String(), "Family(0)"},
		{catalog.ClassComposite.String(), "composite"},
		{catalog.PIDClass(7).String(), "PIDClass(7)"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
	var zero catalog.Tier
	if zero != catalog.Off {
		t.Error("the zero Tier is not Off")
	}
}
