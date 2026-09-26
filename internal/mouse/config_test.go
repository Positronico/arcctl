package mouse_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/vectors"
)

func model(t testing.TB, key string) *catalog.Model {
	t.Helper()
	m, ok := catalog.ByKey(key)
	if !ok {
		t.Fatalf("model %s not in the catalog", key)
	}
	return m
}

func vector(t testing.TB, name string) []byte {
	t.Helper()
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Vectors {
		if v.Name == name {
			b, err := v.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("vector %q not found", name)
	return nil
}

func put(t testing.TB, im *flash.Image, addr int, b []byte) {
	t.Helper()
	if err := im.Set(addr, b); err != nil {
		t.Fatal(err)
	}
}

func fill(v byte, n int) []byte { return bytes.Repeat([]byte{v}, n) }

func bodyAddr(t testing.TB, table func(int) (flash.Extent, bool), k int) int {
	t.Helper()
	return at(t, table, k).Addr
}

func record(t testing.TB, fn mouse.KeyFn) []byte {
	t.Helper()
	r, err := mouse.EncodeKeyFn(fn)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var maintainerShortcuts = map[int]string{
	2: "Cmd+V shortcut",
	3: "Ctrl+Tab shortcut",
	4: "Cmd+Tab shortcut",
	5: "Cmd+C shortcut",
}

// maintainerImage is flash-dump.bin plus the shortcut bodies its bindings 2-5 use,
// with every other shortcut and macro slot read back erased.
func maintainerImage(t testing.TB) *flash.Image {
	t.Helper()
	im, err := flash.FromDump(0, readFile(t, testdata(t, "flash-dump.bin")))
	if err != nil {
		t.Fatal(err)
	}
	put(t, im, mouse.AddrShortcutKey, fill(0xFF, mouse.Slots*mouse.ShortcutSize))
	put(t, im, mouse.AddrMacro, fill(0xFF, mouse.Slots*mouse.MacroSize))
	for k, name := range maintainerShortcuts {
		put(t, im, bodyAddr(t, mouse.ShortcutExtent, k), vector(t, name))
	}
	return im
}

func hiddenAt(t *testing.T, c mouse.Config, addr int) mouse.Field {
	t.Helper()
	for _, f := range c.Hidden {
		if f.Extent.Addr == addr {
			return f
		}
	}
	t.Fatalf("no hidden field at %d", addr)
	return mouse.Field{}
}

func TestDecodeDump(t *testing.T) {
	b := dump(t)
	c := mouse.Decode(model(t, "7B04"), maintainerImage(t))

	pairs := []struct {
		f    mouse.Field
		addr int
		want int
	}{
		{c.Rate, mouse.AddrReportRate, 250},
		{c.Stages, mouse.AddrMaxDpiStage, 6},
		{c.Current, mouse.AddrCurrentDPI, 3},
	}
	for _, p := range pairs {
		if p.f.State != flash.OK || p.f.Value != p.want || p.f.Extent != (flash.Extent{Addr: p.addr, Len: 2}) ||
			!bytes.Equal(p.f.Raw, b[p.addr:p.addr+2]) {
			t.Errorf("%s = %+v, want %d", p.f.Name, p.f, p.want)
		}
	}

	dpis := []mouse.DPI{{800, 800}, {1200, 1200}, {1600, 1600}, {2400, 2400}, {3200, 3200}, {4800, 2400}, {4000, 4000}, {4000, 4000}}
	for i, st := range c.DPI {
		e := at(t, mouse.DPIExtent, i)
		if st.DPIField.State != flash.OK || st.DPI != dpis[i] || !bytes.Equal(st.DPIField.Raw, b[e.Addr:e.End()]) {
			t.Errorf("stage %d = %+v %+v, want %+v", i+1, st.DPI, st.DPIField, dpis[i])
		}
		if st.ColorField.State != flash.OK || st.ColorField.Extent != at(t, mouse.ColorExtent, i) {
			t.Errorf("stage %d colour field %+v", i+1, st.ColorField)
		}
	}
	if flag := c.DPI[5].DPIField.Raw[2]; flag != 0x01 {
		t.Errorf("stage 6 flag byte = %#02x, want 0x01", flag)
	}
	if c.DPI[0].Color != [3]byte{0xFF, 0, 0} {
		t.Errorf("stage 1 colour = % x", c.DPI[0].Color)
	}

	shortcut := mouse.KeyFn{Type: mouse.TypeShortcut}
	keyTable := []mouse.KeyFn{
		{mouse.TypeMouse, mouse.ParamLeft},
		{mouse.TypeMouse, mouse.ParamRight},
		shortcut, shortcut, shortcut, shortcut,
		{mouse.TypeProfile, 0},
		{mouse.TypeDPI, mouse.ParamDPICycle},
		{mouse.TypeScroll, mouse.ParamScrollLeft},
		{mouse.TypeScroll, mouse.ParamScrollRight},
		{mouse.TypeDPI, mouse.ParamDPIUp},
		{mouse.TypeDPI, mouse.ParamDPIDown},
		{}, {}, {}, {},
	}
	combos := map[int]string{2: "Cmd+V", 3: "Ctrl+Tab", 4: "Cmd+Tab", 5: "Cmd+C"}
	for k, bnd := range c.Keys {
		if bnd.Field.State != flash.OK || bnd.Fn != keyTable[k] {
			t.Errorf("slot %d = %+v %v, want %+v", k, bnd.Fn, bnd.Field.State, keyTable[k])
		}
		wantClass := flash.SlotUnknown
		if name, ok := combos[k]; ok {
			wantClass = flash.SlotValid
			if got := c.Shortcuts[k].Format(keys.Mac); got != name {
				t.Errorf("shortcut %d = %q, want %q", k, got, name)
			}
		} else if c.Shortcuts[k] != nil {
			t.Errorf("shortcut %d = %v, want none", k, c.Shortcuts[k])
		}
		if c.Slots[k] != wantClass {
			t.Errorf("slot %d class = %v, want %v", k, c.Slots[k], wantClass)
		}
		if c.Macros[k] != nil {
			t.Errorf("macro %d = %+v, want none", k, c.Macros[k])
		}
		wantShortcut := flash.SlotEmpty
		if wantClass == flash.SlotValid {
			wantShortcut = flash.SlotValid
		}
		if c.ShortcutClass[k] != wantShortcut || c.MacroClass[k] != flash.SlotEmpty {
			t.Errorf("slot %d body classes = %v %v, want %v empty", k, c.ShortcutClass[k], c.MacroClass[k], wantShortcut)
		}
	}

	hidden := []struct {
		addr  int
		name  string
		size  int
		state flash.FieldState
		value int
	}{
		{6, "unmapped", 2, flash.OK, 0},
		{mouse.AddrKeyOperation, "KeyOperation", 2, flash.OK, 0},
		{84, "unmapped", 12, flash.Unknown, 0},
		{mouse.AddrLight, "Light", 7, flash.OK, 0},
		{167, "unmapped", 2, flash.OK, 1},
		{mouse.AddrSleepTime, "SleepTime", 2, flash.OK, 1},
		{mouse.AddrPerformanceState, "PerformanceState", 2, flash.OK, 4},
		{mouse.AddrSensorMode, "SensorMode", 2, flash.Invalid, 0},
		{187, "unmapped", 2, flash.OK, 0},
		{mouse.AddrAngleTune, "AngleTune", 2, flash.Invalid, 0},
		{mouse.AddrAngleTuneState, "AngleTuneState", 2, flash.Erased, 0},
		{193, "unmapped", 32, flash.Erased, 0},
		{mouse.AddrFlywheelMaxSpeed, "Flywheel", 4, flash.Erased, 0},
	}
	for _, h := range hidden {
		f := hiddenAt(t, c, h.addr)
		if f.Name != h.name || f.Extent.Len != h.size || f.State != h.state || f.Value != h.value ||
			!bytes.Equal(f.Raw, b[h.addr:h.addr+h.size]) {
			t.Errorf("hidden %d = %+v, want %s %d bytes %v value %d", h.addr, f, h.name, h.size, h.state, h.value)
		}
	}
	if f := hiddenAt(t, c, mouse.AddrSensorMode); !bytes.Equal(f.Raw, []byte{0x03, 0x53}) || f.Err == nil {
		t.Errorf("SensorMode = %+v, want raw 03 53 with an error", f)
	}
	if f := hiddenAt(t, c, mouse.AddrVirtualCenter); f.State != flash.Unknown || f.Raw != nil {
		t.Errorf("VirtualCenter = %+v, want never read", f)
	}
	if !slices.IsSortedFunc(c.Hidden, func(a, b mouse.Field) int { return a.Extent.Addr - b.Extent.Addr }) {
		t.Error("hidden fields are not in address order")
	}
}

func TestDecodeHiddenStates(t *testing.T) {
	cases := []struct {
		raw   []byte
		state flash.FieldState
		value int
	}{
		{[]byte{0x05, 0x50}, flash.OK, 5},
		{[]byte{0xFF, 0x56}, flash.Unset, 0xFF},
		{[]byte{0xFF, 0xFF}, flash.Erased, 0},
		{[]byte{0x03, 0x53}, flash.Invalid, 0},
		{nil, flash.Unknown, 0},
	}
	for _, tc := range cases {
		im := flash.New()
		if tc.raw != nil {
			put(t, im, mouse.AddrSleepTime, tc.raw)
		}
		f := hiddenAt(t, mouse.Decode(model(t, "7B04"), im), mouse.AddrSleepTime)
		if f.State != tc.state || f.Value != tc.value || !bytes.Equal(f.Raw, tc.raw) || (f.Err == nil) != (tc.state == flash.OK || tc.state == flash.Unknown) {
			t.Errorf("% x: %+v, want %v %d", tc.raw, f, tc.state, tc.value)
		}
	}
}

func TestDecodeCoversSettingsPage(t *testing.T) {
	b := dump(t)
	c := mouse.Decode(model(t, "7B04"), maintainerImage(t))
	fields := append([]mouse.Field{c.Rate, c.Stages, c.Current}, c.Hidden...)
	for _, st := range c.DPI {
		fields = append(fields, st.DPIField, st.ColorField)
	}
	for _, k := range c.Keys {
		fields = append(fields, k.Field)
	}
	var owner [256]string
	for _, f := range fields {
		if f.Extent.Addr >= len(owner) {
			continue
		}
		for a := f.Extent.Addr; a < f.Extent.End(); a++ {
			if owner[a] != "" {
				t.Errorf("byte %d is in %s and %s", a, owner[a], f.Name)
			}
			owner[a] = f.Name
		}
		if !bytes.Equal(f.Raw, b[f.Extent.Addr:f.Extent.End()]) {
			t.Errorf("%s at %v raw % x, dump has % x", f.Name, f.Extent, f.Raw, b[f.Extent.Addr:f.Extent.End()])
		}
	}
	for a, o := range owner {
		if o == "" {
			t.Errorf("byte %d is in no field", a)
		}
	}
}

func TestDecodeEmptyImage(t *testing.T) {
	for _, im := range []*flash.Image{nil, flash.New()} {
		c := mouse.Decode(model(t, "7B04"), im)
		fields := append([]mouse.Field{c.Rate, c.Stages, c.Current}, c.Hidden...)
		for _, st := range c.DPI {
			fields = append(fields, st.DPIField, st.ColorField)
		}
		for _, k := range c.Keys {
			fields = append(fields, k.Field)
		}
		for _, f := range fields {
			if f.State != flash.Unknown || f.Raw != nil || f.Err != nil || f.Value != 0 {
				t.Errorf("%s at %v = %+v, want unknown", f.Name, f.Extent, f)
			}
		}
		for k := range mouse.Slots {
			if c.Slots[k] != flash.SlotUnknown || c.Shortcuts[k] != nil || c.Macros[k] != nil || c.Keys[k].Fn != (mouse.KeyFn{}) {
				t.Errorf("slot %d decoded from nothing: %+v %v %v %v", k, c.Keys[k], c.Slots[k], c.Shortcuts[k], c.Macros[k])
			}
		}
	}
}

func TestDecodeBodies(t *testing.T) {
	ab := vector(t, `Macro "ab"`)
	macroAB := pad(ab, mouse.MacroSize)
	cases := []struct {
		name  string
		setup func(t *testing.T, im *flash.Image)
		slot  int
		class flash.SlotClass
		combo string
		macro string

		shortcut, macroClass flash.SlotClass
	}{
		{"media body", func(t *testing.T, im *flash.Image) {
			put(t, im, bodyAddr(t, mouse.KeyFnExtent, 12), record(t, mouse.KeyFn{Type: mouse.TypeShortcut}))
			put(t, im, bodyAddr(t, mouse.ShortcutExtent, 12), vector(t, "Media Play/Pause"))
		}, 12, flash.SlotValid, "Play/Pause", "", flash.SlotValid, flash.SlotEmpty},
		{"erased body", func(t *testing.T, im *flash.Image) {
			put(t, im, bodyAddr(t, mouse.KeyFnExtent, 12), record(t, mouse.KeyFn{Type: mouse.TypeShortcut}))
		}, 12, flash.SlotEmpty, "", "", flash.SlotEmpty, flash.SlotEmpty},
		{"zero body", func(t *testing.T, im *flash.Image) {
			put(t, im, bodyAddr(t, mouse.KeyFnExtent, 12), record(t, mouse.KeyFn{Type: mouse.TypeShortcut}))
			put(t, im, bodyAddr(t, mouse.ShortcutExtent, 12), fill(0, mouse.ShortcutSize))
		}, 12, flash.SlotEmpty, "", "", flash.SlotEmpty, flash.SlotEmpty},
		{"bad checksum", func(t *testing.T, im *flash.Image) {
			bad := vector(t, "Ctrl+Tab shortcut")
			bad[len(bad)-1]++
			put(t, im, bodyAddr(t, mouse.ShortcutExtent, 3), bad)
		}, 3, flash.SlotInvalid, "", "", flash.SlotInvalid, flash.SlotEmpty},
		{"body read in part", func(t *testing.T, im *flash.Image) {
			*im = flash.Image{}
			put(t, im, 0, dump(t))
			put(t, im, bodyAddr(t, mouse.ShortcutExtent, 3), vector(t, "Ctrl+Tab shortcut")[:10])
		}, 3, flash.SlotUnknown, "", "", flash.SlotUnknown, flash.SlotUnknown},
		{"macro bound", func(t *testing.T, im *flash.Image) {
			put(t, im, bodyAddr(t, mouse.KeyFnExtent, 4), vector(t, "Macro binding"))
			put(t, im, bodyAddr(t, mouse.MacroExtent, 4), macroAB)
		}, 4, flash.SlotValid, "Cmd+Tab", "ab", flash.SlotValid, flash.SlotValid},
		{"macro padding never read", func(t *testing.T, im *flash.Image) {
			*im = flash.Image{}
			put(t, im, 0, dump(t))
			put(t, im, bodyAddr(t, mouse.KeyFnExtent, 4), vector(t, "Macro binding"))
			put(t, im, bodyAddr(t, mouse.MacroExtent, 4), ab[:3])
			put(t, im, bodyAddr(t, mouse.MacroExtent, 4)+31, ab[31:])
		}, 4, flash.SlotUnknown, "", "", flash.SlotUnknown, flash.SlotUnknown},
		{"macro unbound", func(t *testing.T, im *flash.Image) {
			put(t, im, bodyAddr(t, mouse.MacroExtent, 9), macroAB)
		}, 9, flash.SlotUnknown, "", "ab", flash.SlotEmpty, flash.SlotValid},
		{"binding invalid", func(t *testing.T, im *flash.Image) {
			put(t, im, bodyAddr(t, mouse.KeyFnExtent, 3), []byte{0x05, 0x00, 0x00, 0x51})
		}, 3, flash.SlotUnknown, "Ctrl+Tab", "", flash.SlotValid, flash.SlotEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := maintainerImage(t)
			tc.setup(t, im)
			c := mouse.Decode(model(t, "7B04"), im)
			if c.Slots[tc.slot] != tc.class {
				t.Errorf("class = %v, want %v", c.Slots[tc.slot], tc.class)
			}
			if got := c.Shortcuts[tc.slot].Format(keys.Mac); got != tc.combo {
				t.Errorf("shortcut = %q, want %q", got, tc.combo)
			}
			switch m := c.Macros[tc.slot]; {
			case tc.macro == "" && m != nil, tc.macro != "" && (m == nil || m.Name != tc.macro):
				t.Errorf("macro = %+v, want %q", m, tc.macro)
			}
			if tc.shortcut != c.ShortcutClass[tc.slot] || tc.macroClass != c.MacroClass[tc.slot] {
				t.Errorf("body classes = %v %v, want %v %v", c.ShortcutClass[tc.slot], c.MacroClass[tc.slot], tc.shortcut, tc.macroClass)
			}
		})
	}
}

// loadAsRead builds the image a working load reads (docs/protocol.md, "Reads"): the
// settings page, then for each bound slot the whole shortcut slot, or the macro header
// and its events.
func loadAsRead(t *testing.T, full *flash.Image) *flash.Image {
	t.Helper()
	im := flash.New()
	copyKnown := func(e flash.Extent) {
		b, ok := full.Get(e)
		if !ok {
			t.Fatalf("%v not in the source image", e)
		}
		put(t, im, e.Addr, b)
	}
	copyKnown(flash.Extent{Addr: 0, Len: 256})
	for k := range mouse.Slots {
		fn, err := mouse.DecodeKeyFn(im.Bytes()[bodyAddr(t, mouse.KeyFnExtent, k):][:4])
		if err != nil {
			continue
		}
		switch fn.Type {
		case mouse.TypeShortcut:
			copyKnown(at(t, mouse.ShortcutExtent, k))
		case mouse.TypeMacro:
			base := bodyAddr(t, mouse.MacroExtent, k)
			copyKnown(flash.Extent{Addr: base, Len: 32})
			n, _ := im.Byte(base + 31)
			copyKnown(flash.Extent{Addr: base + 31, Len: 5*int(n) + 2})
		}
	}
	return im
}

func TestWorkingLoadDecodesAndPlans(t *testing.T) {
	full := maintainerImage(t)
	put(t, full, bodyAddr(t, mouse.KeyFnExtent, 1), macroBinding(t, 1, 1))
	put(t, full, bodyAddr(t, mouse.MacroExtent, 1), vector(t, `Macro "ab"`))
	im := loadAsRead(t, full)
	c := mouse.Decode(model(t, "7B04"), im)
	for k, want := range map[int]flash.SlotClass{1: flash.SlotValid, 2: flash.SlotValid, 3: flash.SlotValid, 4: flash.SlotValid, 5: flash.SlotValid} {
		if c.Slots[k] != want {
			t.Errorf("slot %d class %v, want %v", k, c.Slots[k], want)
		}
	}
	if c.Macros[1] == nil || c.Macros[1].Name != "ab" {
		t.Fatalf("macro 1 = %+v", c.Macros[1])
	}
	edits := []mouse.Edit{
		mouse.SetMacro{Slot: 1, Macro: macroOf("ab", 70), Cycle: 1},
		mouse.SetShortcut{Slot: 4, Combo: keys.Combo{lCtrl, keys.LShift.Stroke(), keyA}},
	}
	if _, err := mouse.PlanEdits(model(t, "7B04"), im, edits, allow("7B04")); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeWithoutModel(t *testing.T) {
	c := mouse.Decode(nil, maintainerImage(t))
	for i, st := range c.DPI {
		if st.DPIField.State != flash.OK || st.DPI != (mouse.DPI{}) {
			t.Errorf("stage %d = %+v %+v, want a checked record with no DPI", i+1, st.DPI, st.DPIField)
		}
	}
	if c.Keys[0].Fn != (mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft}) || c.Rate.Value != 250 {
		t.Errorf("model-free fields not decoded: %+v %+v", c.Keys[0], c.Rate)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(uint16(0), readFile(f, testdata(f, "flash-dump.bin")))
	f.Add(uint16(mouse.AddrShortcutKey), vector(f, "Cmd+C shortcut"))
	f.Add(uint16(mouse.AddrMacro), vector(f, `Macro "ab"`))
	f.Add(uint16(90), []byte{0x05, 0x00, 0x00, 0x50, 0x02, 0x82})
	m := model(f, "7B04")
	f.Fuzz(func(t *testing.T, addr uint16, data []byte) {
		im := flash.New()
		if im.Set(int(addr), data) != nil {
			return
		}
		c := mouse.Decode(m, im)
		fields := append([]mouse.Field{c.Rate, c.Stages, c.Current}, c.Hidden...)
		for _, st := range c.DPI {
			fields = append(fields, st.DPIField, st.ColorField)
		}
		for _, k := range c.Keys {
			fields = append(fields, k.Field)
		}
		for _, fd := range fields {
			known := im.Known(fd.Extent)
			if (fd.Raw != nil) != known || (!known && fd.State != flash.Unknown) {
				t.Fatalf("%s at %v: raw % x state %v, known %v", fd.Name, fd.Extent, fd.Raw, fd.State, known)
			}
		}
		for k := range mouse.Slots {
			if c.Shortcuts[k] != nil {
				r, err := mouse.EncodeShortcut(c.Shortcuts[k])
				e := at(t, mouse.ShortcutExtent, k)
				got, ok := im.Get(flash.Extent{Addr: e.Addr, Len: len(r)})
				if err != nil || !ok || !bytes.Equal(got, r) {
					t.Fatalf("shortcut %d decoded from bytes it does not re-encode to", k)
				}
			}
			if c.Macros[k] != nil {
				r, err := mouse.EncodeMacro(*c.Macros[k])
				e := at(t, mouse.MacroExtent, k)
				got, ok := im.Get(flash.Extent{Addr: e.Addr, Len: len(r)})
				if err != nil || !ok || !bytes.Equal(got, r) {
					t.Fatalf("macro %d decoded from bytes it does not re-encode to", k)
				}
			}
		}
	})
}
