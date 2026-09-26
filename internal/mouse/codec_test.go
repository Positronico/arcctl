package mouse_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

func withChecksum(body string) string {
	return body + " " + hex.EncodeToString([]byte{0x55 - checksum(unhexString(body))})
}

func unhexString(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

func TestDecodeShortcutErrors(t *testing.T) {
	cmdC := "04 80 08 00 81 06 00 41 06 00 40 08 00 b3"
	tests := []struct {
		name string
		hex  string
		want error
	}{
		{"valid", cmdC, nil},
		{"valid in a full slot", cmdC + strings.Repeat(" ff", 18), nil},
		{"no bytes", "", mouse.ErrTruncated},
		{"erased", strings.Repeat("ff ", 31) + "ff", mouse.ErrEmpty},
		{"zeroed", strings.Repeat("00 ", 31) + "00", mouse.ErrEmpty},
		{"count zero", "00 80 08 00", mouse.ErrInvalid},
		{"odd count", "03 80 08 00", mouse.ErrInvalid},
		{"count over five keys", "0c 80 08 00", mouse.ErrInvalid},
		{"count 0xff", "ff 80 08 00", mouse.ErrInvalid},
		{"truncated", cmdC[:20], mouse.ErrTruncated},
		{"bad checksum", cmdC[:len(cmdC)-2] + "b4", mouse.ErrInvalid},
		{"press marked release", withChecksum("04 40 08 00 81 06 00 41 06 00 40 08 00"), mouse.ErrInvalid},
		{"press with reserved bits", withChecksum("04 90 08 00 81 06 00 41 06 00 40 08 00"), mouse.ErrInvalid},
		{"releases not reversed", withChecksum("04 80 08 00 81 06 00 40 08 00 41 06 00"), mouse.ErrInvalid},
		{"release of another key", withChecksum("04 80 08 00 81 06 00 41 07 00 40 08 00"), mouse.ErrInvalid},
		{"mouse kind", withChecksum("02 84 01 00 44 01 00"), mouse.ErrInvalid},
		{"zero key", withChecksum("02 81 00 00 41 00 00"), mouse.ErrInvalid},
		{"key above 0xff", withChecksum("02 81 04 01 41 04 01"), mouse.ErrInvalid},
		{"menu key", withChecksum("02 87 01 00 47 01 00"), nil},
		{"right modifiers", withChecksum("04 80 80 00 81 04 00 41 04 00 40 80 00"), nil},
		{"wide consumer usage", withChecksum("02 82 34 12 42 34 12"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := unhexString(tt.hex)
			c, err := mouse.DecodeShortcut(b)
			if !sameErr(err, tt.want) || (err != nil) != (c == nil) {
				t.Fatalf("DecodeShortcut(%s) = %+v, %v; want %v", tt.hex, c, err, tt.want)
			}
			if err == nil {
				r, err := mouse.EncodeShortcut(c)
				if err != nil || !bytes.HasPrefix(b, r) {
					t.Errorf("re-encode = % x, %v", r, err)
				}
			}
		})
	}
}

func TestDecodeMacroErrors(t *testing.T) {
	name := "02 61 62" + strings.Repeat(" ff", 28)
	valid := name + " 02 81 04 00 00 32 41 04 00 00 0a 4d"
	tests := []struct {
		name string
		hex  string
		want error
	}{
		{"valid", valid, nil},
		{"valid in a full slot", valid + strings.Repeat(" ff", mouse.MacroSize-43), nil},
		{"no bytes", "", mouse.ErrTruncated},
		{"erased", strings.Repeat("ff ", 383) + "ff", mouse.ErrEmpty},
		{"zeroed", strings.Repeat("00 ", 383) + "00", mouse.ErrEmpty},
		{"name length zero", "00 61", mouse.ErrInvalid},
		{"name length 31", "1f 61", mouse.ErrInvalid},
		{"name block only", "02 61 62 ff ff ff ff ff ff ff", mouse.ErrTruncated},
		{"name not UTF-8", "02 c3 28" + strings.Repeat(" ff", 28) + " 02 81 04 00 00 32 41 04 00 00 0a 4d", mouse.ErrInvalid},
		{"padding not 0xff", "02 61 62 00" + strings.Repeat(" ff", 27) + " 02 81 04 00 00 32 41 04 00 00 0a 4d", mouse.ErrInvalid},
		{"no events", name + " 00 55", mouse.ErrInvalid},
		{"71 events", name + " 47", mouse.ErrInvalid},
		{"events truncated", valid[:len(valid)-6], mouse.ErrTruncated},
		{"bad checksum", valid[:len(valid)-2] + "4e", mouse.ErrInvalid},
		{"header 0xc1", name + " " + withChecksum("01 c1 04 00 00 32"), mouse.ErrInvalid},
		{"header 0x01", name + " " + withChecksum("01 01 04 00 00 32"), mouse.ErrInvalid},
		{"consumer kind", name + " " + withChecksum("01 82 cd 00 00 32"), mouse.ErrInvalid},
		{"zero key", name + " " + withChecksum("01 81 00 00 00 32"), mouse.ErrInvalid},
		{"value above 0xff", name + " " + withChecksum("01 81 04 01 00 32"), mouse.ErrInvalid},
		{"zero delay", name + " " + withChecksum("01 81 04 00 00 00"), nil},
		{"mouse button", name + " " + withChecksum("02 84 01 00 00 32 44 01 00 00 0a"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := unhexString(tt.hex)
			m, err := mouse.DecodeMacro(b)
			if !sameErr(err, tt.want) || (err != nil && (m.Name != "" || m.Events != nil)) {
				t.Fatalf("DecodeMacro = %+v, %v; want %v", m, err, tt.want)
			}
			if err == nil {
				r, err := mouse.EncodeMacro(m)
				if err != nil || !bytes.HasPrefix(b, r) {
					t.Errorf("re-encode = % x, %v", r, err)
				}
			}
		})
	}
}

func TestDecodeRecordErrors(t *testing.T) {
	s := sensor(t, "3104")
	dpi := func(b []byte) error { _, err := mouse.DecodeDPI(s, b); return err }
	color := func(b []byte) error { _, err := mouse.DecodeColor(b); return err }
	keyFn := func(b []byte) error { _, err := mouse.DecodeKeyFn(b); return err }
	tests := []struct {
		name   string
		decode func([]byte) error
		hex    string
		want   error
	}{
		{"dpi", dpi, "15 15 00 2b", nil},
		{"dpi prefix of longer bytes", dpi, "15 15 00 2b 99", nil},
		{"dpi short", dpi, "15 15 00", mouse.ErrTruncated},
		{"dpi erased", dpi, "ff ff ff ff", mouse.ErrEmpty},
		{"dpi checksum", dpi, "15 15 00 2c", mouse.ErrInvalid},
		{"dpi unknown raw code", dpi, withChecksum("06 06 00"), mouse.ErrInvalid},
		{"dpi Y unknown", dpi, withChecksum("15 06 00"), mouse.ErrInvalid},
		{"dpi above the sensor", dpi, withChecksum("69 69 33"), mouse.ErrInvalid},
		{"dpi factory stage 6", dpi, "3f 3f 01 d6", nil},
		{"colour", color, "ff 00 00 56", nil},
		{"colour zeroed", color, "00 00 00 00", mouse.ErrInvalid},
		{"keyfn disable", keyFn, "00 00 00 55", nil},
		{"keyfn erased", keyFn, "ff ff ff ff", mouse.ErrEmpty},
		{"keyfn unknown type", keyFn, withChecksum("0c 00 00"), mouse.ErrInvalid},
		{"keyfn two buttons", keyFn, withChecksum("01 03 00"), mouse.ErrInvalid},
		{"keyfn cycle 251", keyFn, withChecksum("06 04 fb"), mouse.ErrInvalid},
		{"keyfn cycle 252", keyFn, withChecksum("06 04 fc"), mouse.ErrInvalid},
		{"keyfn macro slot 16", keyFn, withChecksum("06 10 01"), mouse.ErrInvalid},
		{"keyfn fire interval 9", keyFn, withChecksum("04 09 00"), mouse.ErrInvalid},
		{"keyfn fire times 4", keyFn, withChecksum("04 0a 04"), mouse.ErrInvalid},
		{"keyfn dpi lock 10 bits", keyFn, withChecksum("0a ff 03"), nil},
		{"keyfn dpi lock 11 bits", keyFn, withChecksum("0a 00 04"), mouse.ErrInvalid},
		{"keyfn short", keyFn, "01 01", mouse.ErrTruncated},
	}
	for _, tt := range tests {
		if err := tt.decode(unhexString(tt.hex)); !sameErr(err, tt.want) {
			t.Errorf("%s: %s gives %v; want %v", tt.name, tt.hex, err, tt.want)
		}
	}
}

func sameErr(err, want error) bool {
	if want == nil {
		return err == nil
	}
	return errors.Is(err, want)
}

func TestEncoderDomains(t *testing.T) {
	s := sensor(t, "3104")
	stroke := func(k keys.Kind, v uint16) keys.Stroke { return keys.Stroke{Kind: k, Value: v} }
	key := stroke(keys.KindKey, 4)
	name30 := strings.Repeat("a", 30)
	tests := []struct {
		name string
		err  error
	}{
		{"shortcut no keys", err2(mouse.EncodeShortcut(nil))},
		{"shortcut six keys", err2(mouse.EncodeShortcut(keys.Combo{key, key, key, key, key, key}))},
		{"shortcut mouse kind", err2(mouse.EncodeShortcut(keys.Combo{stroke(keys.KindMouse, 1)}))},
		{"shortcut kind 3", err2(mouse.EncodeShortcut(keys.Combo{stroke(3, 1)}))},
		{"shortcut zero key", err2(mouse.EncodeShortcut(keys.Combo{stroke(keys.KindKey, 0)}))},
		{"shortcut modifier 0x100", err2(mouse.EncodeShortcut(keys.Combo{stroke(keys.KindModifier, 0x100)}))},
		{"media zero", err2(mouse.EncodeMedia(0))},
		{"macro empty name", err2(mouse.EncodeMacro(mouse.Macro{Events: []mouse.Event{{Stroke: key}}}))},
		{"macro 31-byte name", err2(mouse.EncodeMacro(mouse.Macro{Name: name30 + "a", Events: []mouse.Event{{Stroke: key}}}))},
		{"macro 31-byte UTF-8 name", err2(mouse.EncodeMacro(mouse.Macro{Name: strings.Repeat("a", 29) + "é", Events: []mouse.Event{{Stroke: key}}}))},
		{"macro invalid UTF-8", err2(mouse.EncodeMacro(mouse.Macro{Name: "a\xff", Events: []mouse.Event{{Stroke: key}}}))},
		{"macro no events", err2(mouse.EncodeMacro(mouse.Macro{Name: "a"}))},
		{"macro 71 events", err2(mouse.EncodeMacro(mouse.Macro{Name: "a", Events: make([]mouse.Event, 71)}))},
		{"macro consumer event", err2(mouse.EncodeMacro(mouse.Macro{Name: "a", Events: []mouse.Event{{Stroke: stroke(keys.KindConsumer, 0xCD)}}}))},
		{"keyfn unknown type", err2(mouse.EncodeKeyFn(mouse.KeyFn{Type: 12}))},
		{"keyfn disable with param", err2(mouse.EncodeKeyFn(mouse.KeyFn{Param: 1}))},
		{"fire interval 9", err2(mouse.FireKey(9, 0))},
		{"fire interval 256", err2(mouse.FireKey(256, 0))},
		{"fire times 4", err2(mouse.FireKey(10, 4))},
		{"fire times -1", err2(mouse.FireKey(10, -1))},
		{"binding slot 16", err2(mouse.MacroBinding(16, 1))},
		{"binding slot -1", err2(mouse.MacroBinding(-1, 1))},
		{"binding cycle 0", err2(mouse.MacroBinding(0, 0))},
		{"binding cycle 251", err2(mouse.MacroBinding(0, 251))},
		{"binding cycle 252", err2(mouse.MacroBinding(0, 252))},
		{"binding cycle 256", err2(mouse.MacroBinding(0, 256))},
		{"dpi lock above the first range", err2(mouse.DPILock(s, 4200))},
		{"dpi lock off step", err2(mouse.DPILock(s, 250))},
		{"dpi 4100", err2(mouse.EncodeDPI(s, 4100))},
		{"dpi 4300", err2(mouse.EncodeDPI(s, 4300))},
		{"dpi 100", err2(mouse.EncodeDPI(s, 100))},
		{"dpi 8200", err2(mouse.EncodeDPI(s, 8200))},
		{"dpi no sensor", err2(mouse.EncodeDPI(nil, 800))},
		{"rate 333", err2(mouse.EncodeRate(333))},
	}
	for _, tt := range tests {
		if !errors.Is(tt.err, mouse.ErrValue) {
			t.Errorf("%s: %v; want ErrValue", tt.name, tt.err)
		}
	}
	m, err := mouse.EncodeMacro(mouse.Macro{Name: name30, Events: slices.Repeat([]mouse.Event{{Press: true, Stroke: key, Delay: 10}}, mouse.MaxMacroEvents)})
	if err != nil || len(m) != 383 {
		t.Errorf("largest macro = %d bytes, %v; want 383", len(m), err)
	}
	if _, err := mouse.DecodeDPI(nil, []byte{0x15, 0x15, 0, 0x2b}); !errors.Is(err, mouse.ErrValue) {
		t.Errorf("DecodeDPI without a sensor: %v", err)
	}
}

func err2[T any](_ T, err error) error { return err }

func TestErrorClasses(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), mouse.ErrTruncated)
	tests := []struct {
		err   error
		state flash.FieldState
		class flash.SlotClass
	}{
		{nil, flash.OK, flash.SlotValid},
		{mouse.ErrEmpty, flash.Erased, flash.SlotEmpty},
		{mouse.ErrUnset, flash.Unset, flash.SlotInvalid},
		{wrapped, flash.Unknown, flash.SlotUnknown},
		{mouse.ErrInvalid, flash.Invalid, flash.SlotInvalid},
		{mouse.ErrValue, flash.Invalid, flash.SlotInvalid},
	}
	for _, tt := range tests {
		if s, c := mouse.State(tt.err), mouse.Class(tt.err); s != tt.state || c != tt.class {
			t.Errorf("%v: state %v class %v; want %v %v", tt.err, s, c, tt.state, tt.class)
		}
	}
	_, err := mouse.DecodeShortcut([]byte{0x04, 0x80})
	if !strings.HasPrefix(err.Error(), "mouse: truncated: need 14 bytes, have 2") {
		t.Errorf("error text %q", err)
	}
}

func TestExtents(t *testing.T) {
	tables := []struct {
		name   string
		extent func(int) (flash.Extent, bool)
		count  int
		first  int
		size   int
		next   int
	}{
		{"dpi", mouse.DPIExtent, mouse.MaxStages, mouse.AddrDPIValue, 4, mouse.AddrDPIColor},
		{"colour", mouse.ColorExtent, mouse.MaxStages, mouse.AddrDPIColor, 4, mouse.AddrDPIEffectMode},
		{"keyfn", mouse.KeyFnExtent, mouse.Slots, mouse.AddrKeyFunction, 4, mouse.AddrLight},
		{"shortcut", mouse.ShortcutExtent, mouse.Slots, mouse.AddrShortcutKey, mouse.ShortcutSize, mouse.AddrMacro},
		{"macro", mouse.MacroExtent, mouse.Slots, mouse.AddrMacro, mouse.MacroSize, mouse.AddrSensor3955DPI},
	}
	for _, tt := range tables {
		for _, i := range []int{-1, tt.count} {
			if e, ok := tt.extent(i); ok || e != (flash.Extent{}) {
				t.Errorf("%s(%d) = %v, %v", tt.name, i, e, ok)
			}
		}
		for i := range tt.count {
			e, ok := tt.extent(i)
			if want := (flash.Extent{Addr: tt.first + i*tt.size, Len: tt.size}); !ok || e != want {
				t.Errorf("%s(%d) = %v, %v; want %v", tt.name, i, e, ok, want)
			}
		}
		if last, _ := tt.extent(tt.count - 1); last.End() != tt.next {
			t.Errorf("%s table ends at %d; next field starts at %d", tt.name, last.End(), tt.next)
		}
	}
}

func TestRates(t *testing.T) {
	for _, hz := range mouse.Rates() {
		want := hz / 2000 * 16
		if hz <= 1000 {
			want = 1000 / hz
		}
		if p, err := mouse.EncodeRate(hz); err != nil || int(p.Value()) != want {
			t.Errorf("EncodeRate(%d) = % x, %v; web app writes %d", hz, p, err, want)
		}
	}
	all := mouse.Rates()
	s3104, s3212 := sensor(t, "3104"), sensor(t, "3212")
	tests := []struct {
		conn byte
		max  int
	}{{0, 1000}, {1, 4000}, {2, 1000}, {3, 8000}, {4, 2000}, {5, 8000}, {6, 0}, {7, 0}, {0xFF, 0}}
	for _, tt := range tests {
		limit, ok := mouse.MaxRate(tt.conn)
		if limit != tt.max || ok != (tt.max > 0) {
			t.Errorf("MaxRate(%d) = %d, %v; want %d", tt.conn, limit, ok, tt.max)
		}
		want := all[:slices.Index(all, tt.max)+1]
		if tt.max == 0 {
			want = nil
		}
		if got := mouse.RateOptions(tt.conn, s3104); !slices.Equal(got, want) {
			t.Errorf("RateOptions(%d, 3104) = %v; want %v", tt.conn, got, want)
		}
		if got := mouse.RateOptions(tt.conn, nil); !slices.Equal(got, want) {
			t.Errorf("RateOptions(%d, nil) = %v; want %v", tt.conn, got, want)
		}
		if want != nil {
			want = []int{125}
		}
		if got := mouse.RateOptions(tt.conn, s3212); !slices.Equal(got, want) {
			t.Errorf("RateOptions(%d, 3212) = %v; want %v", tt.conn, got, want)
		}
	}
}

func TestKeyTypeNames(t *testing.T) {
	tests := map[mouse.KeyType]string{
		mouse.TypeDisable: "disable", mouse.TypeMacro: "macro", mouse.TypeWheel: "scroll up/down", 12: "KeyType(12)",
	}
	for k, want := range tests {
		if got := k.String(); got != want {
			t.Errorf("KeyType(%d) = %q; want %q", uint8(k), got, want)
		}
	}
}

func TestCatalogButtonDefaults(t *testing.T) {
	for _, m := range catalog.Models() {
		if m.Family != catalog.FamilyMouse {
			continue
		}
		for _, b := range m.Buttons {
			k := mouse.KeyFn{Type: mouse.KeyType(b.Type), Param: b.Param}
			if err := k.Check(); err != nil {
				t.Errorf("%s slot %d default %+v: %v", m.Key, b.Slot, k, err)
			}
			if _, ok := mouse.KeyFnExtent(b.Slot); !ok {
				t.Errorf("%s slot %d outside the table", m.Key, b.Slot)
			}
			if b.Media != 0 {
				if _, err := mouse.EncodeMedia(b.Media); err != nil || k.Type != mouse.TypeShortcut {
					t.Errorf("%s slot %d media %#04x on %v: %v", m.Key, b.Slot, b.Media, k.Type, err)
				}
			}
		}
	}
	for _, u := range append(catalog.MouseMedia(), catalog.KeyboardMedia()...) {
		if _, err := mouse.EncodeMedia(u.Code); err != nil {
			t.Errorf("media %#04x: %v", u.Code, err)
		}
	}
}

func TestRepeatOptionsAreCycles(t *testing.T) {
	for _, o := range keys.RepeatOptions() {
		if _, err := mouse.MacroBinding(0, int(o.Value)); err != nil {
			t.Errorf("repeat option %d: %v", o.Value, err)
		}
	}
}

func TestLockedDPI(t *testing.T) {
	s := sensor(t, "3104")
	for _, d := range mouse.DPIs(s) {
		k, err := mouse.DPILock(s, d)
		if d > 4000 {
			if !errors.Is(err, mouse.ErrValue) {
				t.Errorf("DPILock(%d): %v", d, err)
			}
			continue
		}
		if got, ok := k.LockedDPI(s); err != nil || !ok || got != d {
			t.Errorf("DPILock(%d) = %+v, %v; LockedDPI %d, %v", d, k, err, got, ok)
		}
	}
	for _, k := range []mouse.KeyFn{{Type: mouse.TypeDPI, Param: 5}, {Type: mouse.TypeDPILock, Param: 6}, {Type: mouse.TypeDPILock, Param: 0x400}} {
		if d, ok := k.LockedDPI(s); ok {
			t.Errorf("LockedDPI(%+v) = %d", k, d)
		}
	}
	if _, ok := (mouse.KeyFn{Type: mouse.TypeDPILock, Param: 5}).LockedDPI(nil); ok {
		t.Error("LockedDPI without a sensor")
	}
}

func TestDPIRefusesInconsistentSensors(t *testing.T) {
	r := func(lo, hi, step int, ex byte) catalog.Range {
		return catalog.Range{Min: lo, Max: hi, Step: step, DPIEx: ex}
	}
	tests := []struct {
		name string
		s    catalog.Sensor
		dpi  int
	}{
		{"X and Y flags differ", catalog.Sensor{ID: "x", Ranges: []catalog.Range{r(100, 200, 100, 0), r(400, 800, 200, 0x10)}}, 400},
		{"odd DPI in a doubled range", catalog.Sensor{ID: "x", Ranges: []catalog.Range{r(100, 200, 100, 0), r(301, 305, 2, 0x11)}}, 301},
		{"value table too short", catalog.Sensor{ID: "x", Ranges: []catalog.Range{r(100, 500, 100, 0)}, Values: []uint16{1, 2}}, 300},
		{"doubled range below the table", catalog.Sensor{ID: "x", Ranges: []catalog.Range{r(100, 200, 100, 0), r(120, 120, 10, 0x11)}, Values: []uint16{1, 2}}, 120},
		{"off the base step", catalog.Sensor{ID: "x", Ranges: []catalog.Range{r(30, 90, 20, 0)}}, 30},
		{"raw code over 10 bits", catalog.Sensor{ID: "x", Ranges: []catalog.Range{r(50, 60000, 50, 0)}}, 51250},
		{"no ranges", catalog.Sensor{ID: "x"}, 100},
	}
	for _, tt := range tests {
		if _, err := mouse.EncodeDPI(&tt.s, tt.dpi); !errors.Is(err, mouse.ErrValue) {
			t.Errorf("%s: EncodeDPI(%d) = %v", tt.name, tt.dpi, err)
		}
		if slices.Contains(mouse.DPIs(&tt.s), tt.dpi) {
			t.Errorf("%s: DPIs lists %d", tt.name, tt.dpi)
		}
	}
	if mouse.DPIs(nil) != nil {
		t.Error("DPIs(nil) is not nil")
	}
}
