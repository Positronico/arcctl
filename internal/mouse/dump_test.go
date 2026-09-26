package mouse_test

import (
	"bytes"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
)

func dump(t *testing.T) []byte {
	t.Helper()
	b := readFile(t, testdata(t, "flash-dump.bin"))
	if len(b) != 256 {
		t.Fatalf("dump has %d bytes", len(b))
	}
	return b
}

func pairAt(b []byte, addr int) flash.Pair { return flash.Pair(b[addr : addr+2]) }

func TestDumpSettings(t *testing.T) {
	b := dump(t)
	tests := []struct {
		name   string
		decode func(flash.Pair) (int, error)
		encode func(int) (flash.Pair, error)
		addr   int
		want   int
	}{
		{"rate", mouse.DecodeRate, mouse.EncodeRate, mouse.AddrReportRate, 250},
		{"stage count", mouse.DecodeStageCount, mouse.EncodeStageCount, mouse.AddrMaxDpiStage, 6},
		{"current stage", mouse.DecodeCurrentStage, mouse.EncodeCurrentStage, mouse.AddrCurrentDPI, 3},
		{"DPI light", mouse.DecodeDPILight, mouse.EncodeDPILight, mouse.AddrDPIEffectBrightness, 5},
	}
	for _, tt := range tests {
		p := pairAt(b, tt.addr)
		if got, err := tt.decode(p); err != nil || got != tt.want {
			t.Errorf("%s at %d = %d, %v; want %d", tt.name, tt.addr, got, err, tt.want)
		}
		if got, err := tt.encode(tt.want); err != nil || got != p {
			t.Errorf("%s re-encodes to % x, %v; dump has % x", tt.name, got, err, p)
		}
	}
}

func TestDumpStages(t *testing.T) {
	b := dump(t)
	s := sensor(t, "3104")
	dpis := []mouse.DPI{{800, 800}, {1200, 1200}, {1600, 1600}, {2400, 2400}, {3200, 3200}, {4800, 2400}, {4000, 4000}, {4000, 4000}}
	colors := [][3]byte{{0xFF, 0, 0}, {0, 0, 0xFF}, {0, 0xFF, 0}, {0xFF, 0xFF, 0}, {0xFF, 0x80, 0}, {0xFF, 0, 0xFF}, {0xFF, 0x7D, 0x7D}, {0x7D, 0x7D, 0xFF}}
	for i := range mouse.MaxStages {
		e := at(t, mouse.DPIExtent, i)
		raw := b[e.Addr:e.End()]
		d, err := mouse.DecodeDPI(s, raw)
		if err != nil || d != dpis[i] {
			t.Errorf("stage %d = %+v, %v; want %+v", i, d, err, dpis[i])
		}
		r, err := mouse.EncodeDPI(s, dpis[i].X)
		if symmetric := dpis[i].X == dpis[i].Y; err != nil || bytes.Equal(r, raw) != symmetric {
			t.Errorf("stage %d re-encodes to % x, %v; dump has % x", i, r, err, raw)
		}
		e = at(t, mouse.ColorExtent, i)
		raw = b[e.Addr:e.End()]
		if c, err := mouse.DecodeColor(raw); err != nil || c != colors[i] || !bytes.Equal(mouse.EncodeColor(c), raw) {
			t.Errorf("colour %d = % x, %v; want % x", i, c, err, colors[i])
		}
	}
}

func TestDumpKeyFunctions(t *testing.T) {
	b := dump(t)
	shortcut := mouse.KeyFn{Type: mouse.TypeShortcut}
	want := []mouse.KeyFn{
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
	for slot := range mouse.Slots {
		e := at(t, mouse.KeyFnExtent, slot)
		raw := b[e.Addr:e.End()]
		k, err := mouse.DecodeKeyFn(raw)
		if err != nil || k != want[slot] {
			t.Errorf("slot %d = %+v, %v; want %+v", slot, k, err, want[slot])
		}
		if r, err := mouse.EncodeKeyFn(k); err != nil || !bytes.Equal(r, raw) {
			t.Errorf("slot %d re-encodes to % x, %v; dump has % x", slot, r, err, raw)
		}
	}
}
