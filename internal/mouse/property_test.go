package mouse_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

func legalDPIs(s *catalog.Sensor) []int {
	var out []int
	for _, r := range s.Ranges {
		for d := r.Min; d <= r.Max; d += r.Step {
			out = append(out, d)
		}
	}
	return out
}

// webEncodeDPI follows the web app: the range is the last one whose minimum is at most dpi, and
// its index (not its flags) picks the divisor.
func webEncodeDPI(s *catalog.Sensor, dpi int) ([]byte, bool) {
	r := len(s.Ranges) - 1
	for r >= 0 && dpi < s.Ranges[r].Min {
		r--
	}
	if r < 0 {
		return nil, false
	}
	a := 1
	switch r {
	case 1, 2:
		a = 2
	case 3:
		a = 4
	}
	r0 := s.Ranges[0]
	var v int
	if len(s.Values) > 0 {
		num, den := dpi-a*r0.Min, a*r0.Step
		if num < 0 || num%den != 0 || num/den >= len(s.Values) {
			return nil, false
		}
		v = int(s.Values[num/den])
	} else {
		den := a * r0.Step
		if dpi%den != 0 {
			return nil, false
		}
		v = dpi/den - 1
	}
	ex, hi := int(s.Ranges[r].DPIEx), v>>8
	return flash.NewRecord(byte(v), byte(v), byte(hi<<2|hi<<6|ex|ex<<4)), true
}

func webDecodeAxis(s *catalog.Sensor, raw uint16, f byte) int {
	r0 := s.Ranges[0]
	var n int
	if len(s.Values) > 0 {
		o := 0
		for o < len(s.Values) && s.Values[o] != raw {
			o++
		}
		n = o*r0.Step + r0.Min
	} else {
		n = (int(raw) + 1) * r0.Step
	}
	if f&1 != 0 {
		n *= 2
	}
	if f&2 != 0 {
		n *= 2
	}
	return n
}

func TestDPIEveryLegalValue(t *testing.T) {
	for _, s := range catalog.Sensors() {
		t.Run(s.ID, func(t *testing.T) {
			legal := legalDPIs(s)
			if got := mouse.DPIs(s); !slices.Equal(got, legal) {
				t.Fatalf("DPIs = %v; want %v", got, legal)
			}
			for _, d := range legal {
				r, err := mouse.EncodeDPI(s, d)
				if err != nil || !r.Verify() {
					t.Fatalf("EncodeDPI(%d) = % x, %v", d, r, err)
				}
				if web, ok := webEncodeDPI(s, d); !ok || !bytes.Equal(r, web) {
					t.Errorf("EncodeDPI(%d) = % x; web app writes % x", d, r, web)
				}
				if got, err := mouse.DecodeDPI(s, r); err != nil || got != (mouse.DPI{X: d, Y: d}) {
					t.Errorf("DecodeDPI(% x) = %+v, %v; want %d", r, got, err, d)
				}
				x := webDecodeAxis(s, uint16(r[0])|uint16(r[2]>>2&3)<<8, r[2]&3)
				y := webDecodeAxis(s, uint16(r[1])|uint16(r[2]>>6&3)<<8, r[2]>>4&3)
				if x != d || y != d {
					t.Errorf("web app decodes % x as %d/%d; want %d", r, x, y, d)
				}
			}
		})
	}
}

func TestDPIDecodeAgreesWithWeb(t *testing.T) {
	for _, s := range catalog.Sensors() {
		legal := legalDPIs(s)
		for raw := uint16(0); raw <= 0x3FF; raw++ {
			for f := range byte(4) {
				hi := byte(raw >> 8)
				r := flash.NewRecord(byte(raw), byte(raw), hi<<2|hi<<6|f|f<<4)
				got, err := mouse.DecodeDPI(s, r)
				web := webDecodeAxis(s, raw, f)
				known := len(s.Values) == 0 || slices.Contains(s.Values, raw)
				wantOK := known && slices.Contains(legal, web)
				if (err == nil) != wantOK || (err != nil && !errors.Is(err, mouse.ErrInvalid)) {
					t.Fatalf("sensor %s DecodeDPI(% x) = %+v, %v; want ok %v", s.ID, r, got, err, wantOK)
				}
				if err == nil && (got.X != web || got.Y != web) {
					t.Fatalf("sensor %s DecodeDPI(% x) = %+v; web app %d", s.ID, r, got, web)
				}
			}
		}
	}
}

func TestDPIAxesIndependent(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, s := range catalog.Sensors() {
		legal := mouse.DPIs(s)
		for range 2000 {
			x, y := legal[rng.IntN(len(legal))], legal[rng.IntN(len(legal))]
			rx, _ := mouse.EncodeDPI(s, x)
			ry, _ := mouse.EncodeDPI(s, y)
			r := flash.NewRecord(rx[0], ry[1], rx[2]&0x0F|ry[2]&0xF0)
			if got, err := mouse.DecodeDPI(s, r); err != nil || got != (mouse.DPI{X: x, Y: y}) {
				t.Fatalf("sensor %s DecodeDPI(% x) = %+v, %v; want %d/%d", s.ID, r, got, err, x, y)
			}
		}
	}
}

func TestKeyFnExhaustive(t *testing.T) {
	var accepted [256]int
	params := map[mouse.KeyType][]uint16{}
	for typ := range 256 {
		for param := range 1 << 16 {
			if typ > 15 && param&0x0F0F != param {
				continue
			}
			hi, lo := byte(param>>8), byte(param)
			r := flash.NewRecord(byte(typ), hi, lo)
			if typ == int(mouse.TypeDPILock) {
				r = flash.NewRecord(byte(typ), lo, hi)
			}
			want := mouse.KeyFn{Type: mouse.KeyType(typ), Param: uint16(param)}
			k, err := mouse.DecodeKeyFn(r)
			checkErr := want.Check()
			if (err == nil) != (checkErr == nil) {
				t.Fatalf("DecodeKeyFn(% x) err %v but Check %v", r, err, checkErr)
			}
			if err != nil {
				if !errors.Is(err, mouse.ErrInvalid) || !errors.Is(checkErr, mouse.ErrValue) || k != (mouse.KeyFn{}) {
					t.Fatalf("DecodeKeyFn(% x) = %+v, %v; Check %v", r, k, err, checkErr)
				}
				continue
			}
			if enc, err := mouse.EncodeKeyFn(k); k != want || err != nil || !bytes.Equal(enc, r) || !enc.Verify() {
				t.Fatalf("round trip of % x = %+v, % x, %v", r, k, enc, err)
			}
			accepted[typ]++
			if typ != int(mouse.TypeFire) && typ != int(mouse.TypeMacro) && typ != int(mouse.TypeDPILock) {
				params[k.Type] = append(params[k.Type], k.Param)
			}
		}
	}
	want := [256]int{0: 1, 1: 5, 2: 3, 3: 2, 4: 246 * 4, 5: 1, 6: 16 * 253, 7: 1, 8: 1, 9: 1, 10: 1024, 11: 2}
	if accepted != want {
		t.Errorf("accepted params per type %v, want %v", accepted, want)
	}
	small := map[mouse.KeyType][]uint16{
		mouse.TypeDisable:    {0},
		mouse.TypeMouse:      {0x0100, 0x0200, 0x0400, 0x0800, 0x1000},
		mouse.TypeDPI:        {0x0100, 0x0200, 0x0300},
		mouse.TypeScroll:     {0x0100, 0x0200},
		mouse.TypeShortcut:   {0},
		mouse.TypeRateSwitch: {0},
		mouse.TypeDragScroll: {0x0500},
		mouse.TypeProfile:    {0},
		mouse.TypeWheel:      {0x0100, 0x0200},
	}
	for typ, want := range small {
		if !slices.Equal(params[typ], want) {
			t.Errorf("type %v takes % x, want % x", typ, params[typ], want)
		}
	}
}

func randomStroke(rng *rand.Rand, kinds []keys.Kind) keys.Stroke {
	k := kinds[rng.IntN(len(kinds))]
	if k == keys.KindConsumer {
		return keys.Stroke{Kind: k, Value: uint16(1 + rng.IntN(0xFFFF))}
	}
	return keys.Stroke{Kind: k, Value: uint16(1 + rng.IntN(0xFF))}
}

func TestShortcutRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	kinds := []keys.Kind{keys.KindModifier, keys.KindKey, keys.KindConsumer, keys.KindMenu}
	for range 5000 {
		c := make(keys.Combo, 1+rng.IntN(mouse.MaxShortcutKeys))
		for i := range c {
			c[i] = randomStroke(rng, kinds)
		}
		r, err := mouse.EncodeShortcut(c)
		if err != nil || !r.Verify() || len(r) != 6*len(c)+2 || len(r) > mouse.ShortcutSize {
			t.Fatalf("EncodeShortcut(%+v) = % x, %v", c, r, err)
		}
		got, err := mouse.DecodeShortcut(pad(r, mouse.ShortcutSize))
		if err != nil || !slices.Equal(got, c) {
			t.Fatalf("DecodeShortcut(% x) = %+v, %v; want %+v", r, got, err, c)
		}
	}
}

var nameRunes = []rune("abcXYZ019_éüß中文マク한😀🎮")

func randomName(rng *rand.Rand) string {
	var b []byte
	for n := 1 + rng.IntN(30); len(b) < n; {
		next := utf8.AppendRune(b, nameRunes[rng.IntN(len(nameRunes))])
		if len(next) > mouse.MaxNameLen {
			break
		}
		b = next
	}
	if len(b) == 0 {
		b = []byte{'m'}
	}
	return string(b)
}

func TestMacroRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	kinds := []keys.Kind{keys.KindModifier, keys.KindKey, keys.KindMouse, keys.KindMenu}
	for range 2000 {
		m := mouse.Macro{Name: randomName(rng), Events: make([]mouse.Event, 1+rng.IntN(mouse.MaxMacroEvents))}
		for i := range m.Events {
			m.Events[i] = mouse.Event{Press: rng.IntN(2) == 0, Stroke: randomStroke(rng, kinds), Delay: uint16(rng.Uint32())}
		}
		b, err := mouse.EncodeMacro(m)
		switch {
		case err != nil:
			t.Fatalf("EncodeMacro(%q, %d events): %v", m.Name, len(m.Events), err)
		case len(b) != 33+5*len(m.Events) || len(b) > mouse.MacroSize:
			t.Fatalf("EncodeMacro wrote %d bytes for %d events", len(b), len(m.Events))
		case !flash.Record(b[31:]).Verify():
			t.Fatalf("macro events do not sum to 0x55")
		}
		got, err := mouse.DecodeMacro(pad(b, mouse.MacroSize))
		if err != nil || got.Name != m.Name || !slices.Equal(got.Events, m.Events) {
			t.Fatalf("DecodeMacro = %+v, %v; want %+v", got, err, m)
		}
	}
}

func TestPairCodecsExhaustive(t *testing.T) {
	codecs := []struct {
		name   string
		decode func(flash.Pair) (int, error)
		encode func(int) (flash.Pair, error)
		domain []int
	}{
		{"rate", mouse.DecodeRate, mouse.EncodeRate, mouse.Rates()},
		{"stage count", mouse.DecodeStageCount, mouse.EncodeStageCount, []int{1, 2, 3, 4, 5, 6, 7, 8}},
		{"current stage", mouse.DecodeCurrentStage, mouse.EncodeCurrentStage, []int{0, 1, 2, 3, 4, 5, 6, 7}},
		{"DPI light", mouse.DecodeDPILight, mouse.EncodeDPILight, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
	}
	for _, c := range codecs {
		valid := 0
		for v := range 1 << 16 {
			p := flash.Pair{byte(v), byte(v >> 8)}
			got, err := c.decode(p)
			var ok bool
			switch p.State() {
			case flash.Erased:
				ok = errors.Is(err, mouse.ErrEmpty)
			case flash.Invalid:
				ok = errors.Is(err, mouse.ErrInvalid)
			case flash.Unset:
				ok = err == nil || errors.Is(err, mouse.ErrUnset)
			case flash.OK:
				ok = err == nil || errors.Is(err, mouse.ErrInvalid)
			}
			if !ok {
				t.Fatalf("%s: decode(% x) = %d, %v for a %v pair", c.name, p, got, err, p.State())
			}
			if err != nil {
				continue
			}
			valid++
			if enc, err := c.encode(got); err != nil || enc != p || !slices.Contains(c.domain, got) {
				t.Fatalf("%s: decode(% x) = %d re-encodes to % x, %v", c.name, p, got, enc, err)
			}
		}
		if valid != len(c.domain) {
			t.Errorf("%s: %d valid pairs; want %d", c.name, valid, len(c.domain))
		}
		for v := -300; v <= 9000; v++ {
			p, err := c.encode(v)
			if in := slices.Contains(c.domain, v); in != (err == nil) || (err != nil && !errors.Is(err, mouse.ErrValue)) {
				t.Fatalf("%s: encode(%d) = % x, %v", c.name, v, p, err)
			}
		}
	}
}

func TestColorRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	colors := [][3]byte{{}, {0xFF, 0xFF, 0xFF}, {0xFF, 0, 0}}
	for range 2000 {
		colors = append(colors, [3]byte{byte(rng.Uint32()), byte(rng.Uint32()), byte(rng.Uint32())})
	}
	for _, c := range colors {
		r := mouse.EncodeColor(c)
		got, err := mouse.DecodeColor(r)
		if !r.Verify() || err != nil || got != c {
			t.Fatalf("colour %x: % x -> %x, %v", c, r, got, err)
		}
	}
}
