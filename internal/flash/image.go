package flash

import (
	"bytes"
	"errors"
)

const Size = 16384

var ErrRange = errors.New("flash: extent outside the image")

type rangeError Extent

func (e rangeError) Error() string {
	return "flash: extent " + Extent(e).String() + " outside the image"
}

func (e rangeError) Is(target error) bool { return target == ErrRange }

type Image struct {
	b     [Size]byte
	known [Size / 8]byte
}

func New() *Image { return new(Image) }

func FromDump(addr int, b []byte) (*Image, error) {
	im := New()
	if err := im.Set(addr, b); err != nil {
		return nil, err
	}
	return im, nil
}

func (im *Image) Clone() *Image {
	c := *im
	return &c
}

func (im *Image) Set(addr int, b []byte) error {
	e := Extent{addr, len(b)}
	if !e.inImage() {
		return rangeError(e)
	}
	copy(im.b[addr:], b)
	for i := addr; i < e.End(); i++ {
		im.known[i>>3] |= 1 << (i & 7)
	}
	return nil
}

func (im *Image) Byte(addr int) (byte, bool) {
	if addr < 0 || addr >= Size || !im.isKnown(addr) {
		return 0xFF, false
	}
	return im.b[addr], true
}

func (im *Image) Get(e Extent) ([]byte, bool) {
	if !e.inImage() {
		return nil, false
	}
	out := make([]byte, e.Len)
	ok := true
	for i := range out {
		if a := e.Addr + i; im.isKnown(a) {
			out[i] = im.b[a]
		} else {
			out[i] = 0xFF
			ok = false
		}
	}
	return out, ok
}

func (im *Image) Known(e Extent) bool {
	if !e.inImage() {
		return false
	}
	for a := e.Addr; a < e.End(); a++ {
		if !im.isKnown(a) {
			return false
		}
	}
	return true
}

func (im *Image) KnownExtents() []Extent {
	var out []Extent
	start := -1
	for a := 0; a <= Size; a++ {
		k := a < Size && im.isKnown(a)
		switch {
		case k && start < 0:
			start = a
		case !k && start >= 0:
			out = append(out, Extent{start, a - start})
			start = -1
		}
	}
	return out
}

func (im *Image) Bytes() []byte {
	b, _ := im.Get(Extent{0, Size})
	return b
}

func (im *Image) Pair(addr int) (Pair, FieldState) {
	b, ok := im.Get(Extent{addr, 2})
	if !ok {
		p := Pair{0xFF, 0xFF}
		copy(p[:], b)
		return p, Unknown
	}
	p := Pair(b)
	return p, p.State()
}

func (im *Image) Record(e Extent) (Record, FieldState) {
	b, ok := im.Get(e)
	if !ok {
		return Record(b), Unknown
	}
	r := Record(b)
	return r, r.State()
}

func Diff(from, to *Image, extents []Extent) (changed, unread []Extent) {
	for _, e := range extents {
		nb, ok := to.Get(e)
		if !ok {
			continue
		}
		ob, ok := from.Get(e)
		switch {
		case !ok:
			unread = append(unread, e)
		case !bytes.Equal(ob, nb):
			changed = append(changed, e)
		}
	}
	return changed, unread
}

func (im *Image) isKnown(a int) bool { return im.known[a>>3]&(1<<(a&7)) != 0 }
