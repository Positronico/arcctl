package flash

import (
	"math"
	"testing"
)

func TestExtent(t *testing.T) {
	tests := []struct {
		a, b      Extent
		overlaps  bool
		aContainB bool
	}{
		{Extent{0, 2}, Extent{0, 2}, true, true},
		{Extent{0, 2}, Extent{2, 2}, false, false},
		{Extent{2, 2}, Extent{0, 2}, false, false},
		{Extent{0, 4}, Extent{3, 4}, true, false},
		{Extent{12, 32}, Extent{20, 4}, true, true},
		{Extent{20, 4}, Extent{12, 32}, true, false},
		{Extent{0, 256}, Extent{255, 1}, true, true},
		{Extent{0, 256}, Extent{255, 2}, true, false},
		{Extent{0, 0}, Extent{0, 0}, false, true},
		{Extent{0, 4}, Extent{4, 0}, false, true},
		{Extent{0, 4}, Extent{2, 0}, false, true},
		{Extent{5, 0}, Extent{0, 10}, false, false},
		{Extent{0, 4}, Extent{1, -1}, false, false},
		{Extent{0, -4}, Extent{-2, 1}, false, false},
		{Extent{0, 10}, Extent{5, math.MaxInt}, true, false},
		{Extent{math.MaxInt - 1, 1}, Extent{math.MaxInt - 5, 10}, true, false},
		{Extent{math.MaxInt - 5, 10}, Extent{math.MaxInt - 1, 1}, true, true},
		{Extent{math.MinInt, math.MaxInt}, Extent{math.MaxInt - 1, 1}, false, false},
		{Extent{math.MinInt, math.MaxInt}, Extent{-2, 1}, true, true},
		{Extent{-1, math.MaxInt}, Extent{math.MinInt, math.MaxInt}, false, false},
	}
	for _, tt := range tests {
		if got := tt.a.Overlaps(tt.b); got != tt.overlaps {
			t.Errorf("%v.Overlaps(%v) = %v, want %v", tt.a, tt.b, got, tt.overlaps)
		}
		if got := tt.b.Overlaps(tt.a); got != tt.overlaps {
			t.Errorf("%v.Overlaps(%v) = %v, want %v", tt.b, tt.a, got, tt.overlaps)
		}
		if got := tt.a.Contains(tt.b); got != tt.aContainB {
			t.Errorf("%v.Contains(%v) = %v, want %v", tt.a, tt.b, got, tt.aContainB)
		}
	}
}

func TestExtentEndString(t *testing.T) {
	e := Extent{Addr: 768, Len: 384}
	if e.End() != 1152 {
		t.Errorf("End = %d, want 1152", e.End())
	}
	if e.String() != "768+384" {
		t.Errorf("String = %q", e.String())
	}
}

func TestExtentInImage(t *testing.T) {
	tests := []struct {
		e    Extent
		want bool
	}{
		{Extent{0, Size}, true},
		{Extent{Size, 0}, true},
		{Extent{Size - 1, 1}, true},
		{Extent{Size - 1, 2}, false},
		{Extent{-1, 1}, false},
		{Extent{0, -1}, false},
		{Extent{Size + 1, 0}, false},
		{Extent{1<<62 + 1<<61, 1 << 62}, false},
		{Extent{1, 1<<63 - 1}, false},
	}
	for _, tt := range tests {
		if got := tt.e.inImage(); got != tt.want {
			t.Errorf("%v.inImage() = %v, want %v", tt.e, got, tt.want)
		}
	}
}
