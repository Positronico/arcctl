package backup

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
)

// A web .bin is the 16 KiB flash image, unread bytes as 0xFF, then a 64-byte
// trailer of NUL-padded strings: the vendor tag at 0, the device type at 32
// and, for mice, the sensor at 48.
const (
	BinSize     = flash.Size + trailerSize
	trailerSize = 64
	binVendor   = "Compx Inc"
	binMouse    = "mouse"
	typeOffset  = 32
	sensorOff   = 48
)

var (
	ErrNotBin      = errors.New("backup: not a web .bin file")
	ErrUnsupported = errors.New("backup: only mice have a web .bin export")
	ErrPartial     = errors.New("backup: the image lacks bytes the web app's .bin import writes")
)

// PartialError lists the bytes a web import of the .bin would overwrite with
// 0xFF, because the image does not know them.
type PartialError struct {
	Missing []flash.Extent
}

func (e *PartialError) Error() string {
	parts := make([]string, len(e.Missing))
	for i, x := range e.Missing {
		parts[i] = x.String()
	}
	return ErrPartial.Error() + ": " + strings.Join(parts, ", ")
}

func (e *PartialError) Unwrap() error { return ErrPartial }

// ImportExtents lists what the web app reads before it exports a .bin, which
// is also what its import writes back: the settings page, the extended block
// (the import always rewrites 6984..6987) and, for each key bound to a
// shortcut or a macro, the record in that key's own slot as far as its header
// declares, or the whole header while the count in it is unknown. The import
// rewrites a whole slot whenever it differs from what the web app read.
func ImportExtents(im *flash.Image) []flash.Extent {
	out := []flash.Extent{{Addr: 0, Len: mouse.AddrShortcutKey}}
	data := im.Bytes()
	for k := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(k)
		r, ok := im.Get(e)
		if !ok {
			continue
		}
		fn, err := mouse.DecodeKeyFn(r)
		if err != nil {
			continue
		}
		switch fn.Type {
		case mouse.TypeShortcut:
			out = append(out, shortcutExtent(data, k))
		case mouse.TypeMacro:
			out = append(out, macroExtent(data, k))
		}
	}
	out = append(out, flash.Extent{Addr: mouse.AddrSensor3955DPI, Len: mouse.AddrEndEeprom - mouse.AddrSensor3955DPI})
	slices.SortFunc(out, func(a, b flash.Extent) int { return cmp.Compare(a.Addr, b.Addr) })
	return out
}

// BinMissing lists the parts of ImportExtents that im does not know.
func BinMissing(im *flash.Image) []flash.Extent {
	var out []flash.Extent
	for _, e := range ImportExtents(im) {
		start := -1
		for a := e.Addr; a <= e.End(); a++ {
			_, known := im.Byte(a)
			switch gap := a < e.End() && !known; {
			case gap && start < 0:
				start = a
			case !gap && start >= 0:
				out = append(out, flash.Extent{Addr: start, Len: a - start})
				start = -1
			}
		}
	}
	return out
}

// ExportBin renders im the way the web app exports it, so the web app can
// import it again. It refuses an image that lacks bytes of ImportExtents with
// a *PartialError: the import would write those bytes to the mouse as 0xFF.
func ExportBin(m *catalog.Model, im *flash.Image) ([]byte, error) {
	if missing := BinMissing(im); len(missing) > 0 && supported(m) {
		return nil, &PartialError{Missing: missing}
	}
	return ExportPartialBin(m, im)
}

// ExportPartialBin is ExportBin without the check: unknown bytes go into the
// file as 0xFF, whatever the import then does with them.
func ExportPartialBin(m *catalog.Model, im *flash.Image) ([]byte, error) {
	if !supported(m) {
		return nil, ErrUnsupported
	}
	out := make([]byte, BinSize)
	copy(out, im.Bytes())
	t := out[flash.Size:]
	copy(t, binVendor)
	copy(t[typeOffset:sensorOff], binMouse)
	copy(t[sensorOff:], m.Sensor.ID)
	return out, nil
}

func supported(m *catalog.Model) bool {
	return m != nil && m.Family == catalog.FamilyMouse && m.Sensor != nil
}

// Bin is a web .bin file. Image holds only what the web app reads before it
// exports: the settings page, the extended block and the bodies the buttons
// are bound to, as far as their headers reach. A body of all 0xFF was never
// read and stays unknown.
type Bin struct {
	Type   string
	Sensor string
	Image  *flash.Image
}

func ParseBin(b []byte) (*Bin, error) {
	if len(b) != BinSize {
		return nil, fmt.Errorf("%w: %d bytes, want %d", ErrNotBin, len(b), BinSize)
	}
	t := b[flash.Size:]
	if field(t[:typeOffset]) != binVendor {
		return nil, fmt.Errorf("%w: no %q trailer", ErrNotBin, binVendor)
	}
	bin := &Bin{Type: field(t[typeOffset:sensorOff]), Sensor: field(t[sensorOff:]), Image: flash.New()}
	data := b[:flash.Size]
	known := func(e flash.Extent) {
		if e.Len > 0 && !allFF(data[e.Addr:e.End()]) {
			_ = bin.Image.Set(e.Addr, data[e.Addr:e.End()])
		}
	}
	_ = bin.Image.Set(0, data[:mouse.AddrShortcutKey])
	_ = bin.Image.Set(mouse.AddrSensor3955DPI, data[mouse.AddrSensor3955DPI:mouse.AddrEndEeprom])
	if bin.Type != binMouse {
		return bin, nil
	}
	for k := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(k)
		fn, err := mouse.DecodeKeyFn(data[e.Addr:e.End()])
		if err != nil {
			continue
		}
		switch fn.Type {
		case mouse.TypeShortcut:
			known(shortcutExtent(data, k))
		case mouse.TypeMacro:
			known(macroExtent(data, k))
			known(macroExtent(data, int(fn.Param>>8)))
		}
	}
	return bin, nil
}

// shortcutExtent is the record a shortcut header declares, or the whole slot
// when the header is not a valid count.
func shortcutExtent(data []byte, k int) flash.Extent {
	e, _ := mouse.ShortcutExtent(k)
	if n := int(data[e.Addr]); n > 0 && n%2 == 0 && n <= 2*mouse.MaxShortcutKeys {
		e.Len = 3*n + 2
	}
	return e
}

// macroExtent is the 32-byte header and the events its count declares, or the
// header alone when the count is out of range.
func macroExtent(data []byte, k int) flash.Extent {
	const header = 1 + mouse.MaxNameLen + 1
	e, _ := mouse.MacroExtent(k)
	n := int(data[e.Addr+header-1])
	if n < 1 || n > mouse.MaxMacroEvents {
		return flash.Extent{Addr: e.Addr, Len: header}
	}
	e.Len = header + 5*n + 1
	return e
}

func field(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func allFF(b []byte) bool {
	for _, x := range b {
		if x != 0xFF {
			return false
		}
	}
	return true
}
