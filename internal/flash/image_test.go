package flash

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestNewImageKnowsNothing(t *testing.T) {
	for _, im := range []*Image{New(), {}} {
		if got := im.KnownExtents(); got != nil {
			t.Errorf("KnownExtents = %v", got)
		}
		if b := im.Bytes(); len(b) != Size || !all(b, 0xFF) {
			t.Error("unknown bytes do not read as 0xFF")
		}
		if b, ok := im.Byte(0); ok || b != 0xFF {
			t.Errorf("Byte(0) = %#x, %v", b, ok)
		}
		if p, s := im.Pair(0); s != Unknown || p != (Pair{0xFF, 0xFF}) {
			t.Errorf("Pair(0) = % x %v", p[:], s)
		}
	}
}

func TestSetGet(t *testing.T) {
	im := New()
	if err := im.Set(10, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := im.Set(Size-2, []byte{0x00, 0x55}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		e     Extent
		want  []byte
		known bool
	}{
		{Extent{10, 3}, []byte{1, 2, 3}, true},
		{Extent{11, 1}, []byte{2}, true},
		{Extent{9, 5}, []byte{0xFF, 1, 2, 3, 0xFF}, false},
		{Extent{13, 2}, []byte{0xFF, 0xFF}, false},
		{Extent{10, 0}, []byte{}, true},
		{Extent{Size - 2, 2}, []byte{0x00, 0x55}, true},
		{Extent{Size - 2, 3}, nil, false},
		{Extent{-1, 2}, nil, false},
		{Extent{10, -1}, nil, false},
	}
	for _, tt := range tests {
		got, ok := im.Get(tt.e)
		if ok != tt.known || !bytes.Equal(got, tt.want) || (got == nil) != (tt.want == nil) {
			t.Errorf("Get(%v) = % x, %v; want % x, %v", tt.e, got, ok, tt.want, tt.known)
		}
		if k := im.Known(tt.e); k != tt.known {
			t.Errorf("Known(%v) = %v, want %v", tt.e, k, tt.known)
		}
	}
	if b, ok := im.Byte(12); !ok || b != 3 {
		t.Errorf("Byte(12) = %#x, %v", b, ok)
	}
	for _, a := range []int{-1, 13, Size} {
		if _, ok := im.Byte(a); ok {
			t.Errorf("Byte(%d) known", a)
		}
	}
	want := []Extent{{10, 3}, {Size - 2, 2}}
	if got := im.KnownExtents(); !slices.Equal(got, want) {
		t.Errorf("KnownExtents = %v, want %v", got, want)
	}
	if p, s := im.Pair(Size - 2); s != OK || p.Value() != 0 {
		t.Errorf("Pair = % x %v", p[:], s)
	}
	if p, s := im.Pair(12); s != Unknown || p != (Pair{3, 0xFF}) {
		t.Errorf("half-known Pair = % x %v", p[:], s)
	}
	if p, s := im.Pair(Size - 1); s != Unknown || p != (Pair{0xFF, 0xFF}) {
		t.Errorf("out-of-range Pair = % x %v", p[:], s)
	}
}

func TestSetRange(t *testing.T) {
	im := New()
	for _, tt := range []struct {
		addr, n int
		ok      bool
	}{
		{0, 0, true},
		{0, Size, true},
		{Size, 0, true},
		{Size - 1, 2, false},
		{-1, 1, false},
		{-1, 0, false},
		{Size + 1, 0, false},
	} {
		err := im.Set(tt.addr, make([]byte, tt.n))
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrRange)) {
			t.Errorf("Set(%d, %d bytes) = %v", tt.addr, tt.n, err)
		}
	}
	_, err := FromDump(Size-1, []byte{1, 2})
	if !errors.Is(err, ErrRange) || err.Error() != "flash: extent 16383+2 outside the image" {
		t.Errorf("FromDump past the end: %v", err)
	}
}

func TestSetOverwritesAndClone(t *testing.T) {
	im, err := FromDump(96, []byte{0x01, 0x01, 0x00, 0x53})
	if err != nil {
		t.Fatal(err)
	}
	c := im.Clone()
	if err := c.Set(96, []byte{0x00, 0x00, 0x00, 0x55}); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(256, []byte{0x02}); err != nil {
		t.Fatal(err)
	}
	if r, s := im.Record(Extent{96, 4}); s != OK || r.Body()[0] != 1 {
		t.Errorf("original changed: % x %v", []byte(r), s)
	}
	if im.Known(Extent{256, 1}) {
		t.Error("original learned a byte set on the clone")
	}
	if r, s := c.Record(Extent{96, 4}); s != OK || r.Body()[0] != 0 {
		t.Errorf("clone: % x %v", []byte(r), s)
	}
	if r, s := c.Record(Extent{255, 2}); s != Unknown || !bytes.Equal(r, []byte{0xFF, 0x02}) {
		t.Errorf("half-known record: % x %v", []byte(r), s)
	}
	if r, s := c.Record(Extent{-4, 4}); s != Unknown || r != nil {
		t.Errorf("out-of-range record: % x %v", []byte(r), s)
	}
}

func TestDiff(t *testing.T) {
	base := func() *Image {
		im := New()
		for _, w := range []struct {
			addr int
			b    []byte
		}{
			{0, []byte{0x04, 0x51}},
			{6, []byte{0x00, 0x55}},
			{12, []byte{0x15, 0x15, 0x00, 0x2b}},
			{16, []byte{0x1f, 0x1f, 0x00, 0x17}},
			{185, []byte{0x03, 0x53}},
		} {
			if err := im.Set(w.addr, w.b); err != nil {
				t.Fatal(err)
			}
		}
		return im
	}
	set := func(im *Image, addr int, b ...byte) *Image {
		if err := im.Set(addr, b); err != nil {
			t.Fatal(err)
		}
		return im
	}
	mapped := []Extent{{0, 2}, {12, 4}, {16, 4}, {185, 2}, {256, 14}}
	tests := []struct {
		name            string
		from, to        *Image
		extents         []Extent
		changed, unread []Extent
	}{
		{"identical", base(), base(), mapped, nil, nil},
		{"one byte of a record", base(), set(base(), 14, 0x01), mapped, []Extent{{12, 4}}, nil},
		{"pair and record keep extent order", base(), set(set(base(), 16, 0x2a, 0x2a, 0x00, 0x01), 0, 0x01, 0x54), mapped, []Extent{{0, 2}, {16, 4}}, nil},
		{"rewrite with the same bytes", base(), set(base(), 12, 0x15, 0x15, 0x00, 0x2b), mapped, nil, nil},
		{"unmapped byte changed", base(), set(base(), 6, 0x01, 0x54), mapped, nil, nil},
		{"invalid pair rewritten unchanged", base(), set(base(), 185, 0x03, 0x53), mapped, nil, nil},
		{"invalid pair edited", base(), set(base(), 185, 0x01, 0x54), mapped, []Extent{{185, 2}}, nil},
		{"target never captured", base(), New(), mapped, nil, nil},
		{"target partly captured", base(), set(New(), 12, 0x2a, 0x2a), mapped, nil, nil},
		{"source never read", New(), base(), mapped, nil, []Extent{{0, 2}, {12, 4}, {16, 4}, {185, 2}}},
		{"source partly read", set(New(), 256, make([]byte, 4)...), set(base(), 256, NewRecord(0x02, 0x82, 0xcd, 0x00, 0x42, 0xcd, 0x00)...), []Extent{{256, 8}}, nil, []Extent{{256, 8}}},
		{"no extents", base(), set(base(), 14, 0x01), nil, nil, nil},
		{"bad extents", base(), base(), []Extent{{-2, 2}, {Size - 1, 2}, {0, -1}, {0, 0}}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, unread := Diff(tt.from, tt.to, tt.extents)
			if !slices.Equal(changed, tt.changed) || !slices.Equal(unread, tt.unread) {
				t.Errorf("Diff = %v, %v; want %v, %v", changed, unread, tt.changed, tt.unread)
			}
		})
	}
}
