package flash

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"testing"
)

func FuzzPair(f *testing.F) {
	for _, p := range []Pair{{0x04, 0x51}, {0x03, 0x53}, {0xFF, 0xFF}, {0xFF, 0x56}, {0x00, 0xFF}} {
		f.Add(p[0], p[1])
	}
	f.Fuzz(func(t *testing.T, a, b byte) {
		p := Pair{a, b}
		s := p.State()
		if s.Valid() != (a+b == 0x55) || (s == Erased) != (p == Pair{0xFF, 0xFF}) {
			t.Fatalf("% x: %v", p[:], s)
		}
		if n := NewPair(a); n.State() == Invalid || n.Value() != a {
			t.Fatalf("NewPair(%#x) = % x", a, n[:])
		}
	})
}

func FuzzRecord(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x15, 0x15, 0x00, 0x2b})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{0x04, 0x80, 0x08, 0x00, 0x81, 0x06, 0x00, 0x41, 0x06, 0x00, 0x40, 0x08, 0x00, 0xb3})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := Record(b)
		s := r.State()
		if (s == OK) != (r.Verify() && !all(b, 0xFF)) {
			t.Fatalf("% x: %v", b, s)
		}
		_ = r.Body()
		_ = ClassifySlot(b, r.Verify())
		n := NewRecord(b...)
		if !n.Verify() || !bytes.Equal(n.Body(), b) {
			t.Fatalf("NewRecord(% x) = % x", b, []byte(n))
		}
	})
}

func bigEnd(e Extent) *big.Int {
	return new(big.Int).Add(big.NewInt(int64(e.Addr)), big.NewInt(int64(e.Len)))
}

func bigOverlaps(a, b Extent) bool {
	return a.Len > 0 && b.Len > 0 && big.NewInt(int64(a.Addr)).Cmp(bigEnd(b)) < 0 && big.NewInt(int64(b.Addr)).Cmp(bigEnd(a)) < 0
}

func bigContains(a, b Extent) bool {
	return b.Len >= 0 && a.Addr <= b.Addr && bigEnd(b).Cmp(bigEnd(a)) <= 0
}

func FuzzImage(f *testing.F) {
	f.Add(0, 2, []byte{0x04, 0x51})
	f.Add(Size-1, 4, []byte{0x00})
	f.Add(-1, -1, []byte{})
	f.Add(185, 1<<62, []byte{0x03, 0x53})
	f.Add(5, math.MaxInt, bytes.Repeat([]byte{0}, 10))
	f.Add(math.MinInt, math.MaxInt, []byte{})
	f.Fuzz(func(t *testing.T, addr, n int, data []byte) {
		im := New()
		w := Extent{addr, len(data)}
		err := im.Set(addr, data)
		if err != nil {
			if !errors.Is(err, ErrRange) {
				t.Fatal(err)
			}
		} else if got, ok := im.Get(w); !ok || !bytes.Equal(got, data) || !im.Known(w) {
			t.Fatalf("Get(%v) = % x, %v after Set", w, got, ok)
		}
		q := Extent{addr, n}
		_, _ = im.Get(q)
		_ = im.Known(q)
		_, _ = im.Record(q)
		_, _ = im.Pair(addr)
		_, _ = im.Pair(n)
		_, _ = im.Byte(addr)
		_, _ = im.Byte(n)
		for _, pair := range [][2]Extent{{q, w}, {w, q}, {q, Extent{n, addr}}} {
			a, b := pair[0], pair[1]
			if got, want := a.Overlaps(b), bigOverlaps(a, b); got != want {
				t.Fatalf("%v.Overlaps(%v) = %v, want %v", a, b, got, want)
			}
			if got, want := a.Contains(b), bigContains(a, b); got != want {
				t.Fatalf("%v.Contains(%v) = %v, want %v", a, b, got, want)
			}
		}
		_ = q.String()
		for _, e := range im.KnownExtents() {
			if !im.Known(e) || e.Len <= 0 {
				t.Fatalf("known extent %v", e)
			}
		}
		changed, unread := Diff(New(), im, []Extent{w, q})
		if len(changed) != 0 {
			t.Fatalf("changed %v against an empty source", changed)
		}
		for _, e := range unread {
			if !im.Known(e) {
				t.Fatalf("unread %v is not known in the target", e)
			}
		}
	})
}
