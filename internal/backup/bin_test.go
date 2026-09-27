package backup_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
)

func TestExportBinLayout(t *testing.T) {
	im := richImage(t)
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	if len(b) != 16448 || backup.BinSize != 16448 {
		t.Fatalf("%d bytes, want 16448", len(b))
	}
	if !bytes.Equal(b[:flash.Size], im.Bytes()) {
		t.Error("the image part is not the flash with 0xFF for unread bytes")
	}
	if b[300] != 0xFF || b[9504] != 0xFF {
		t.Error("an unread byte is not 0xFF")
	}
	want := make([]byte, 64)
	copy(want, "Compx Inc")
	copy(want[32:], "mouse")
	copy(want[48:], "3104")
	if got := b[flash.Size:]; !bytes.Equal(got, want) {
		t.Errorf("trailer\n% x\nwant\n% x", got, want)
	}
}

func TestExportBinRefuses(t *testing.T) {
	kb, ok := catalog.ByKey("0301")
	if !ok {
		t.Fatal("no keyboard 0301")
	}
	for _, m := range []*catalog.Model{nil, kb} {
		if _, err := backup.ExportBin(m, flash.New()); !errors.Is(err, backup.ErrUnsupported) {
			t.Errorf("ExportBin(%v) = %v", m, err)
		}
		if _, err := backup.ExportPartialBin(m, flash.New()); !errors.Is(err, backup.ErrUnsupported) {
			t.Errorf("ExportPartialBin(%v) = %v", m, err)
		}
	}
}

// fullImage knows every byte the web app reads: the whole settings area.
func fullImage(t testing.TB) *flash.Image {
	t.Helper()
	im, err := flash.FromDump(0, richImage(t).Bytes()[:mouse.AddrEndEeprom])
	must(t, err)
	return im
}

func TestExportBinRefusesWhatTheImportWouldErase(t *testing.T) {
	macro8, _ := mouse.MacroExtent(8)
	tests := []struct {
		name    string
		im      *flash.Image
		missing []flash.Extent
	}{
		{"settings page only", dumpImage(t), []flash.Extent{
			{Addr: 256 + 2*32, Len: 32}, {Addr: 256 + 3*32, Len: 32}, {Addr: 256 + 4*32, Len: 32}, {Addr: 256 + 5*32, Len: 32},
			{Addr: 6912, Len: 75},
		}},
		{"bodies but no extended block or macro slot 8", richImage(t), []flash.Extent{
			{Addr: macro8.Addr, Len: 32}, {Addr: 6912, Len: 75},
		}},
		{"everything the web app reads", fullImage(t), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backup.BinMissing(tt.im); !equalExtents(got, tt.missing) {
				t.Errorf("BinMissing = %v, want %v", got, tt.missing)
			}
			b, err := backup.ExportBin(em11(t), tt.im)
			var pe *backup.PartialError
			switch {
			case tt.missing == nil && err != nil:
				t.Fatalf("ExportBin = %v", err)
			case tt.missing == nil:
				if !bytes.Equal(b[:flash.Size], tt.im.Bytes()) {
					t.Error("the export is not the image")
				}
			case !errors.As(err, &pe) || !errors.Is(err, backup.ErrPartial) || !equalExtents(pe.Missing, tt.missing):
				t.Fatalf("ExportBin = %v, want a PartialError listing %v", err, tt.missing)
			}
			if b, err := backup.ExportPartialBin(em11(t), tt.im); err != nil || !bytes.Equal(b[:flash.Size], tt.im.Bytes()) {
				t.Errorf("ExportPartialBin = %d bytes, %v", len(b), err)
			}
		})
	}
}

func TestParseBinKnowsWhatTheWebAppReads(t *testing.T) {
	im := richImage(t)
	e, _ := mouse.ShortcutExtent(9)
	must(t, im.Set(e.Addr, []byte{0x02, 0x81, 0x04, 0x00}))
	must(t, im.Set(mouse.AddrDynamicSensitivity, []byte{0x01, 0x54}))
	must(t, im.Set(mouse.AddrVirtualCenter, []byte{0x00, 0x55}))
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	bin, err := backup.ParseBin(b)
	must(t, err)
	if bin.Type != "mouse" || bin.Sensor != "3104" {
		t.Errorf("trailer %q %q", bin.Type, bin.Sensor)
	}
	// Slot 8 runs macro 3, which the web app never reads: it reads the
	// macro in slot 8's own place, erased here.
	want := []flash.Extent{
		{Addr: 0, Len: 191},         // 191..255 is erased on this unit
		{Addr: 256 + 2*32, Len: 14}, // Cmd+C
		{Addr: 256 + 3*32, Len: 20}, // Cmd+Shift+T
		{Addr: 256 + 4*32, Len: 14}, // Cmd+C
		{Addr: 256 + 5*32, Len: 8},  // Play/Pause
		{Addr: mouse.AddrDynamicSensitivity, Len: 2},
		{Addr: mouse.AddrVirtualCenter, Len: 2},
	}
	if got := bin.Image.KnownExtents(); !equalExtents(got, want) {
		t.Errorf("known %v\nwant  %v", got, want)
	}
	for _, x := range want {
		g, _ := bin.Image.Get(x)
		w, _ := im.Get(x)
		if !bytes.Equal(g, w) {
			t.Errorf("%v differs", x)
		}
	}
}

// Of a macro the web app reads the name and the events, not the name's
// padding; the padding is filled in as 0xFF only when that makes the macro
// valid, so a valid macro restores whole and an invalid one never does.
func TestParseBinMacroReads(t *testing.T) {
	im := dumpImage(t)
	bind := func(k int, m mouse.Macro, corrupt bool) flash.Extent {
		t.Helper()
		e, _ := mouse.MacroExtent(k)
		b, err := mouse.EncodeMacro(m)
		must(t, err)
		if corrupt {
			b[len(b)-1]++
		}
		must(t, im.Set(e.Addr, b))
		fn, err := mouse.MacroBinding(k, 1)
		must(t, err)
		r, err := mouse.EncodeKeyFn(fn)
		must(t, err)
		kf, _ := mouse.KeyFnExtent(k)
		must(t, im.Set(kf.Addr, r))
		return flash.Extent{Addr: e.Addr, Len: len(b)}
	}
	long := macroAB
	long.Name = strings.Repeat("n", mouse.MaxNameLen)
	valid := bind(3, macroAB, false)
	invalid := bind(4, macroAB, true)
	full := bind(5, long, false)
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	bin, err := backup.ParseBin(b)
	must(t, err)
	for _, tc := range []struct {
		name  string
		e     flash.Extent
		known bool
	}{
		{"valid macro", valid, true},
		{"valid macro with a 30-byte name", full, true},
		{"invalid macro: name", flash.Extent{Addr: invalid.Addr, Len: 10}, true},
		{"invalid macro: events", flash.Extent{Addr: invalid.Addr + 31, Len: invalid.Len - 31}, true},
		{"invalid macro: padding", flash.Extent{Addr: invalid.Addr + 10, Len: 1}, false},
		{"invalid macro: padding end", flash.Extent{Addr: invalid.Addr + 30, Len: 1}, false},
	} {
		if bin.Image.Known(tc.e) != tc.known {
			t.Errorf("%s: %v known %v, want %v", tc.name, tc.e, !tc.known, tc.known)
			continue
		}
		g, _ := bin.Image.Get(tc.e)
		if w, _ := im.Get(tc.e); tc.known && !bytes.Equal(g, w) {
			t.Errorf("%s differs", tc.name)
		}
	}
}

func TestParseBinSkipsErasedRecords(t *testing.T) {
	im := dumpImage(t)
	dpi, _ := mouse.DPIExtent(1)
	must(t, im.Set(dpi.Addr, []byte{0xFF, 0xFF, 0xFF, 0xFF}))
	key, _ := mouse.KeyFnExtent(9)
	must(t, im.Set(key.Addr, []byte{0xFF, 0xFF, 0xFF, 0xFF}))
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	bin, err := backup.ParseBin(b)
	must(t, err)
	want := []flash.Extent{{Addr: 0, Len: dpi.Addr}, {Addr: dpi.End(), Len: key.Addr - dpi.End()}, {Addr: key.End(), Len: 191 - key.End()}}
	if got := bin.Image.KnownExtents(); !equalExtents(got, want) {
		t.Errorf("known %v, want %v: erased records and bound but erased bodies count as never read", got, want)
	}
}

func TestParseBinShortcutWithoutCount(t *testing.T) {
	im := dumpImage(t)
	e, _ := mouse.ShortcutExtent(2)
	must(t, im.Set(e.Addr, bytes.Repeat([]byte{0x03}, 32)))
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	bin, err := backup.ParseBin(b)
	must(t, err)
	if !bin.Image.Known(flash.Extent{Addr: e.Addr, Len: 10}) || bin.Image.Known(flash.Extent{Addr: e.Addr + 10, Len: 1}) {
		t.Errorf("a body whose count is not valid should be known for the 10 bytes the web app read: %v", bin.Image.KnownExtents())
	}
}

func TestParseBinRejects(t *testing.T) {
	good, err := backup.ExportPartialBin(em11(t), dumpImage(t))
	must(t, err)
	trailer := func(f func(tr []byte)) []byte {
		b := bytes.Clone(good)
		f(b[flash.Size:])
		return b
	}
	for name, tt := range map[string]struct {
		b    []byte
		want error
	}{
		"short":            {good[:100], backup.ErrNotBin},
		"long":             {append(bytes.Clone(good), 0), backup.ErrNotBin},
		"vendor":           {trailer(func(tr []byte) { copy(tr, "Other Inc") }), backup.ErrNotBin},
		"vendor padding":   {trailer(func(tr []byte) { tr[20] = 'x' }), backup.ErrNotBin},
		"no vendor NUL":    {trailer(func(tr []byte) { copy(tr, bytes.Repeat([]byte("C"), 32)) }), backup.ErrNotBin},
		"type unprintable": {trailer(func(tr []byte) { tr[33] = 0x01 }), backup.ErrNotBin},
		"type padding":     {trailer(func(tr []byte) { tr[40] = 'y' }), backup.ErrNotBin},
		"no sensor":        {trailer(func(tr []byte) { clear(tr[48:]) }), backup.ErrNotBin},
		"sensor padding":   {trailer(func(tr []byte) { tr[63] = '1' }), backup.ErrNotBin},
		"keyboard":         {trailer(func(tr []byte) { clear(tr[32:48]); copy(tr[32:], "keyboard") }), backup.ErrUnsupported},
		"unknown sensor":   {trailer(func(tr []byte) { copy(tr[48:], "9999") }), backup.ErrUnsupported},
	} {
		if _, err := backup.ParseBin(tt.b); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", name, err, tt.want)
		}
	}
}
