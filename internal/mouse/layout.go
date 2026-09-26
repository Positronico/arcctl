package mouse

import "github.com/positronico/arcctl/internal/flash"

const (
	MaxStages    = 8
	Slots        = 16
	ShortcutSize = 32
	MacroSize    = 384
	recordSize   = 4
)

func DPIExtent(stage int) (flash.Extent, bool) {
	return tableExtent(AddrDPIValue, recordSize, MaxStages, stage)
}

func ColorExtent(stage int) (flash.Extent, bool) {
	return tableExtent(AddrDPIColor, recordSize, MaxStages, stage)
}

func KeyFnExtent(slot int) (flash.Extent, bool) {
	return tableExtent(AddrKeyFunction, recordSize, Slots, slot)
}

func ShortcutExtent(slot int) (flash.Extent, bool) {
	return tableExtent(AddrShortcutKey, ShortcutSize, Slots, slot)
}

func MacroExtent(slot int) (flash.Extent, bool) {
	return tableExtent(AddrMacro, MacroSize, Slots, slot)
}

func tableExtent(base, stride, count, i int) (flash.Extent, bool) {
	if i < 0 || i >= count {
		return flash.Extent{}, false
	}
	return flash.Extent{Addr: base + i*stride, Len: stride}, true
}
