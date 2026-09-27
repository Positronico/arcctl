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
	"github.com/positronico/arcctl/internal/wire"
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

// Bin is a web .bin file, read only as a restore source. Image holds what
// the web app had read before it exported: the records of the settings page
// and of the extended block, and the shortcut and macro bodies its buttons
// are bound to, as far as their headers declare. The export fills everything
// else with 0xFF, so a record of all 0xFF counts as not captured and stays
// unknown.
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
	vendor, err := trailerField(t[:typeOffset])
	if err != nil || vendor != binVendor {
		return nil, fmt.Errorf("%w: no %q trailer", ErrNotBin, binVendor)
	}
	typ, err := trailerField(t[typeOffset:sensorOff])
	if err != nil {
		return nil, fmt.Errorf("%w: device type: %w", ErrNotBin, err)
	}
	sensor, err := trailerField(t[sensorOff:])
	if err != nil {
		return nil, fmt.Errorf("%w: sensor: %w", ErrNotBin, err)
	}
	if typ != binMouse {
		return nil, fmt.Errorf("%w: the .bin is for a %q", ErrUnsupported, typ)
	}
	if _, ok := catalog.SensorByID(sensor); !ok {
		return nil, fmt.Errorf("%w: sensor %q is not in this build's catalog", ErrUnsupported, sensor)
	}
	return &Bin{Type: typ, Sensor: sensor, Image: captured(b[:flash.Size])}, nil
}

// trailerField is a NUL-padded field of the trailer: printable ASCII, then
// only NULs.
func trailerField(b []byte) (string, error) {
	n := bytes.IndexByte(b, 0)
	if n < 0 {
		n = len(b)
	}
	if n == 0 {
		return "", errors.New("empty")
	}
	for _, c := range b[:n] {
		if c < 0x20 || c > 0x7E {
			return "", fmt.Errorf("byte %#02x is not printable", c)
		}
	}
	for _, c := range b[n:] {
		if c != 0 {
			return "", errors.New("text after the NUL padding")
		}
	}
	return string(b[:n]), nil
}

// captured rebuilds what the web app read from the image part of a .bin:
// each record of the settings page and the extended block, and the body in
// the own slot of each key bound to a shortcut or a macro, unless the
// record is all 0xFF. Of a macro the web app reads the name and the events
// but not the name's padding; see macroReads.
func captured(data []byte) *flash.Image {
	im := flash.New()
	keep := func(e flash.Extent) {
		if e.Len > 0 && !allFF(data[e.Addr:e.End()]) {
			_ = im.Set(e.Addr, data[e.Addr:e.End()])
		}
	}
	for _, e := range SettingsRecords() {
		keep(e)
	}
	for k := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(k)
		if !im.Known(e) {
			continue
		}
		fn, err := mouse.DecodeKeyFn(data[e.Addr:e.End()])
		if err != nil {
			continue
		}
		switch fn.Type {
		case mouse.TypeShortcut:
			keep(readShortcutExtent(data, k))
		case mouse.TypeMacro:
			name, events, pad := macroReads(data, k)
			keep(name)
			keep(events)
			if im.Known(name) && im.Known(events) && pad.Len > 0 {
				fill := bytes.Repeat([]byte{0xFF}, pad.Len)
				rec := slices.Concat(data[name.Addr:name.End()], fill, data[events.Addr:events.End()])
				if _, err := mouse.DecodeMacro(rec); err == nil {
					_ = im.Set(pad.Addr, fill)
				}
			}
		}
	}
	return im
}

// macroReads are the two reads the web app makes of macro slot k: the name
// block, 10 bytes or the name's length byte and name if longer, and from the
// event count on, 10 bytes or the count, the events and the checksum if
// longer. pad is the rest of the name block, which it never reads. A macro
// the codec decodes from the two reads with pad as 0xFF, the padding every
// valid macro holds, gets pad filled in, so that it can be restored whole.
func macroReads(data []byte, k int) (name, events, pad flash.Extent) {
	const count = 1 + mouse.MaxNameLen
	slot, _ := mouse.MacroExtent(k)
	nl := int(data[slot.Addr])
	name = flash.Extent{Addr: slot.Addr, Len: min(max(wire.MaxData, nl+1), count)}
	pad = flash.Extent{Addr: name.End(), Len: count - name.Len}
	n := int(data[slot.Addr+count])
	events = flash.Extent{Addr: slot.Addr + count, Len: min(max(wire.MaxData, 5*n+2), slot.Len-count)}
	return name, events, pad
}

// SettingsRecords are the records of the settings page and of the extended
// block the web app reads: every field the decoder knows, and each run of
// bytes between them, in address order.
func SettingsRecords() []flash.Extent {
	c := mouse.Decode(nil, flash.New())
	out := []flash.Extent{c.Rate.Extent, c.Stages.Extent, c.Current.Extent}
	for _, s := range c.DPI {
		out = append(out, s.DPIField.Extent, s.ColorField.Extent)
	}
	for _, k := range c.Keys {
		out = append(out, k.Field.Extent)
	}
	for _, f := range c.Hidden {
		out = append(out, f.Extent)
	}
	slices.SortFunc(out, func(a, b flash.Extent) int { return cmp.Compare(a.Addr, b.Addr) })
	block := flash.Extent{Addr: mouse.AddrSensor3955DPI, Len: mouse.AddrEndEeprom - mouse.AddrSensor3955DPI}
	var gaps []flash.Extent
	at := block.Addr
	for _, e := range out {
		if !block.Contains(e) {
			continue
		}
		if e.Addr > at {
			gaps = append(gaps, flash.Extent{Addr: at, Len: e.Addr - at})
		}
		at = max(at, e.End())
	}
	if at < block.End() {
		gaps = append(gaps, flash.Extent{Addr: at, Len: block.End() - at})
	}
	out = append(out, gaps...)
	slices.SortFunc(out, func(a, b flash.Extent) int { return cmp.Compare(a.Addr, b.Addr) })
	return out
}

// readShortcutExtent is what the web app read of shortcut slot k: the record
// its header declares, or its first read of 10 bytes when the header is not
// a valid count.
func readShortcutExtent(data []byte, k int) flash.Extent {
	e := shortcutExtent(data, k)
	if n := int(data[e.Addr]); n == 0 || n%2 != 0 || n > 2*mouse.MaxShortcutKeys {
		e.Len = wire.MaxData
	}
	return e
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
	e, ok := mouse.MacroExtent(k)
	if !ok {
		return flash.Extent{}
	}
	n := int(data[e.Addr+header-1])
	if n < 1 || n > mouse.MaxMacroEvents {
		return flash.Extent{Addr: e.Addr, Len: header}
	}
	e.Len = header + 5*n + 1
	return e
}

func allFF(b []byte) bool {
	for _, x := range b {
		if x != 0xFF {
			return false
		}
	}
	return true
}
