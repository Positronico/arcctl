package flash

import (
	"bytes"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/vectors"
)

func TestPairState(t *testing.T) {
	tests := []struct {
		p     Pair
		state FieldState
	}{
		{Pair{0x04, 0x51}, OK},
		{Pair{0x00, 0x55}, OK},
		{Pair{0x55, 0x00}, OK},
		{Pair{0xFE, 0x57}, OK},
		{Pair{0xFF, 0x56}, Unset},
		{Pair{0xFF, 0xFF}, Erased},
		{Pair{0x03, 0x53}, Invalid},
		{Pair{0x00, 0xFF}, Invalid},
		{Pair{0xFF, 0x00}, Invalid},
		{Pair{0x00, 0x00}, Invalid},
		{Pair{0x56, 0xFF}, OK},
	}
	for _, tt := range tests {
		if got := tt.p.State(); got != tt.state {
			t.Errorf("% x: state %v, want %v", tt.p[:], got, tt.state)
		}
	}
}

func TestPairRoundTrip(t *testing.T) {
	for v := range 256 {
		p := NewPair(byte(v))
		if p.Value() != byte(v) || sum(p[:]) != 0x55 {
			t.Fatalf("NewPair(%#x) = % x", v, p[:])
		}
		want := OK
		if v == 0xFF {
			want = Unset
		}
		if s := p.State(); s != want || !s.Valid() {
			t.Fatalf("NewPair(%#x).State() = %v, want %v", v, s, want)
		}
		im := New()
		if err := im.Set(173, p[:]); err != nil {
			t.Fatal(err)
		}
		if got, s := im.Pair(173); got != p || s != want {
			t.Fatalf("image pair %#x: % x %v", v, got[:], s)
		}
	}
}

func TestPairStateExhaustive(t *testing.T) {
	counts := map[FieldState]int{}
	for i := range 1 << 16 {
		p := Pair{byte(i >> 8), byte(i)}
		s := p.State()
		counts[s]++
		checksum := p[0]+p[1] == 0x55
		switch s {
		case Erased:
			if p != (Pair{0xFF, 0xFF}) {
				t.Fatalf("% x erased", p[:])
			}
		case OK, Unset:
			if !checksum || (s == Unset) != (p[0] == 0xFF) || p != NewPair(p.Value()) {
				t.Fatalf("% x %v", p[:], s)
			}
		case Invalid:
			if checksum || p == (Pair{0xFF, 0xFF}) {
				t.Fatalf("% x invalid", p[:])
			}
		default:
			t.Fatalf("% x: state %v", p[:], s)
		}
	}
	want := map[FieldState]int{Erased: 1, OK: 255, Unset: 1, Invalid: 1<<16 - 257}
	for s, n := range want {
		if counts[s] != n {
			t.Errorf("%v: %d pairs, want %d", s, counts[s], n)
		}
	}
}

func TestRecordState(t *testing.T) {
	tests := []struct {
		r     Record
		state FieldState
	}{
		{nil, Unknown},
		{Record{}, Unknown},
		{Record{0x55}, OK},
		{Record{0xFF}, Erased},
		{Record{0x15, 0x15, 0x00, 0x2b}, OK},
		{Record{0x15, 0x15, 0x00, 0x2c}, Invalid},
		{Record{0xFF, 0xFF, 0xFF, 0xFF}, Erased},
		{Record{0xFF, 0xFF, 0xFF, 0x58}, OK},
		{Record{0x00, 0x00, 0x00, 0x00}, Invalid},
		{Record{0x01, 0xFF, 0x00, 0x00, 0x07, 0x02, 0x4c}, OK},
		{Record(bytes.Repeat([]byte{0xFF}, 171)), Erased},
	}
	for _, tt := range tests {
		if got := tt.r.State(); got != tt.state {
			t.Errorf("% x: state %v, want %v", []byte(tt.r), got, tt.state)
		}
		if got := tt.r.Verify(); got != (sum(tt.r) == 0x55 && len(tt.r) > 0) {
			t.Errorf("% x: Verify %v", []byte(tt.r), got)
		}
	}
}

func TestRecordChecksumProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(0xA2C, 0x55))
	for range 5000 {
		body := make([]byte, rng.IntN(400))
		for i := range body {
			body[i] = byte(rng.Uint32())
		}
		r := NewRecord(body...)
		if len(r) != len(body)+1 || !r.Verify() || sum(r) != 0x55 || !bytes.Equal(r.Body(), body) {
			t.Fatalf("NewRecord(% x) = % x", body, []byte(r))
		}
		if s := r.State(); s != OK && !(s == Erased && all(r, 0xFF)) {
			t.Fatalf("NewRecord(% x).State() = %v", body, s)
		}
		i := rng.IntN(len(r))
		bad := bytes.Clone(r)
		bad[i] += byte(1 + rng.IntN(255))
		if Record(bad).Verify() {
			t.Fatalf("% x verifies after changing byte %d", bad, i)
		}
		if s := Record(bad).State(); s != Invalid && !(s == Erased && all(bad, 0xFF)) {
			t.Fatalf("% x: state %v after changing byte %d", bad, s, i)
		}
	}
}

func TestRecordBody(t *testing.T) {
	if Record(nil).Body() != nil {
		t.Error("nil record has a body")
	}
	if b := NewRecord().Body(); len(b) != 0 {
		t.Errorf("empty body = % x", b)
	}
	if r := NewRecord(0x02, 0x01, 0x00); !bytes.Equal(r, []byte{0x02, 0x01, 0x00, 0x52}) {
		t.Errorf("DPI cycle = % x", []byte(r))
	}
}

func TestClassifySlot(t *testing.T) {
	ff := bytes.Repeat([]byte{0xFF}, 384)
	zero := make([]byte, 384)
	tests := []struct {
		b     []byte
		valid bool
		want  SlotClass
	}{
		{nil, false, SlotUnknown},
		{nil, true, SlotUnknown},
		{ff, false, SlotEmpty},
		{ff[:10], false, SlotEmpty},
		{zero, false, SlotEmpty},
		{zero[:32], true, SlotEmpty},
		{[]byte{0x04, 0x80, 0x08, 0x00, 0x81, 0x06, 0x00, 0x41, 0x06, 0x00, 0x40, 0x08, 0x00, 0xb3}, true, SlotValid},
		{[]byte{0x04, 0x80, 0x08, 0x00, 0x81, 0x06, 0x00, 0x41, 0x06, 0x00, 0x40, 0x08, 0x00, 0xb4}, false, SlotInvalid},
		{[]byte{0xFF, 0x00}, false, SlotInvalid},
	}
	for _, tt := range tests {
		if got := ClassifySlot(tt.b, tt.valid); got != tt.want {
			t.Errorf("ClassifySlot(% x, %v) = %v, want %v", tt.b, tt.valid, got, tt.want)
		}
	}
}

func TestStrings(t *testing.T) {
	var names []string
	for s := range FieldState(6) {
		names = append(names, s.String())
	}
	for c := range SlotClass(5) {
		names = append(names, c.String())
	}
	want := "unknown erased invalid ok unset FieldState(5) unknown empty valid invalid SlotClass(4)"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGoldenVectors(t *testing.T) {
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var pairs, records int
	for _, v := range f.Vectors {
		b, err := v.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		from := 0
		arg, isFrom := strings.CutPrefix(v.Check, "sum55_from:")
		if isFrom {
			if from, err = strconv.Atoi(arg); err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
		}
		switch {
		case v.Check == "sum55" || isFrom:
			r := Record(b[from:])
			if r.State() != OK || !bytes.Equal(NewRecord(r.Body()...), r) {
				t.Errorf("%s: % x state %v", v.Name, b[from:], r.State())
			}
			records++
		case v.Group == "packet" && b[0] == 7 && b[2] == 0 && b[4]&0x0F == 2:
			p := Pair(b[5:7])
			if p.State() != OK || NewPair(p.Value()) != p {
				t.Errorf("%s: pair % x state %v", v.Name, p[:], p.State())
			}
			pairs++
		}
	}
	if pairs < 4 || records < 20 {
		t.Errorf("checked %d pairs and %d records", pairs, records)
	}
}
