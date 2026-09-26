package flash

import "strconv"

type Extent struct {
	Addr int `json:"addr"`
	Len  int `json:"len"`
}

func (e Extent) End() int { return e.Addr + e.Len }

// Overlaps and Contains compare distances as uint, which holds the exact difference of
// two ordered ints, so they stay correct when End would overflow.
func (e Extent) Overlaps(o Extent) bool {
	switch {
	case e.Len <= 0 || o.Len <= 0:
		return false
	case e.Addr <= o.Addr:
		return uint(o.Addr-e.Addr) < uint(e.Len)
	}
	return uint(e.Addr-o.Addr) < uint(o.Len)
}

func (e Extent) Contains(o Extent) bool {
	return o.Len >= 0 && o.Len <= e.Len && e.Addr <= o.Addr && uint(o.Addr-e.Addr) <= uint(e.Len-o.Len)
}

func (e Extent) String() string {
	return strconv.Itoa(e.Addr) + "+" + strconv.Itoa(e.Len)
}

func (e Extent) inImage() bool {
	return e.Addr >= 0 && e.Len >= 0 && e.Addr <= Size && e.Len <= Size-e.Addr
}
