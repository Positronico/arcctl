package backup_test

import (
	"bytes"
	"errors"
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
	b, err := backup.ExportPartialBin(em11(t), im)
	must(t, err)
	bin, err := backup.ParseBin(b)
	must(t, err)
	if bin.Type != "mouse" || bin.Sensor != "3104" {
		t.Errorf("trailer %q %q", bin.Type, bin.Sensor)
	}
	macro, _ := mouse.MacroExtent(3)
	want := []flash.Extent{
		{Addr: 0, Len: 256},
		{Addr: 256 + 2*32, Len: 14}, // Cmd+C
		{Addr: 256 + 3*32, Len: 20}, // Cmd+Shift+T
		{Addr: 256 + 4*32, Len: 14}, // Cmd+C
		{Addr: 256 + 5*32, Len: 8},  // Play/Pause
		{Addr: macro.Addr, Len: 32 + 5*4 + 1},
		{Addr: 6912, Len: 75},
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

func TestParseBinSkipsErasedBodies(t *testing.T) {
	b, err := backup.ExportPartialBin(em11(t), dumpImage(t))
	must(t, err)
	bin, err := backup.ParseBin(b)
	must(t, err)
	want := []flash.Extent{{Addr: 0, Len: 256}, {Addr: 6912, Len: 75}}
	if got := bin.Image.KnownExtents(); !equalExtents(got, want) {
		t.Errorf("known %v, want %v: bound but erased bodies count as never read", got, want)
	}
}

func TestParseBinRejects(t *testing.T) {
	good, err := backup.ExportPartialBin(em11(t), dumpImage(t))
	must(t, err)
	bad := bytes.Clone(good)
	copy(bad[flash.Size:], "Other Inc")
	for name, b := range map[string][]byte{"short": good[:100], "long": append(bytes.Clone(good), 0), "trailer": bad} {
		if _, err := backup.ParseBin(b); !errors.Is(err, backup.ErrNotBin) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
