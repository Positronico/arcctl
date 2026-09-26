package mouse

import (
	"errors"
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

const maxRaw = 0x3FF

type DPI struct{ X, Y int }

func DPIs(s *catalog.Sensor) []int {
	if s == nil {
		return nil
	}
	var out []int
	for _, r := range s.Ranges {
		for d := r.Min; r.Step > 0 && d <= r.Max; d += r.Step {
			if _, _, err := encodeAxis(s, d); err == nil {
				out = append(out, d)
			}
		}
	}
	return out
}

func EncodeDPI(s *catalog.Sensor, dpi int) (flash.Record, error) {
	raw, flags, err := encodeAxis(s, dpi)
	if err != nil {
		return nil, err
	}
	hi := byte(raw >> 8)
	return flash.NewRecord(byte(raw), byte(raw), hi<<2|hi<<6|flags), nil
}

func DecodeDPI(s *catalog.Sensor, b []byte) (DPI, error) {
	if s == nil || len(s.Ranges) == 0 {
		return DPI{}, newError(ErrValue, "no sensor")
	}
	r, err := record(b, recordSize)
	if err != nil {
		return DPI{}, err
	}
	f := r[2]
	x, okX := decodeAxis(s, uint16(r[0])|uint16(f>>2&3)<<8, f&3)
	y, okY := decodeAxis(s, uint16(r[1])|uint16(f>>6&3)<<8, f>>4&3)
	switch {
	case !okX:
		return DPI{}, newError(ErrInvalid, "X of "+hexString(r)+" is not a DPI of sensor "+s.ID)
	case !okY:
		return DPI{}, newError(ErrInvalid, "Y of "+hexString(r)+" is not a DPI of sensor "+s.ID)
	}
	return DPI{X: x, Y: y}, nil
}

func encodeAxis(s *catalog.Sensor, dpi int) (raw uint16, flags byte, err error) {
	bad := func(why string) (uint16, byte, error) {
		return 0, 0, newError(ErrValue, strconv.Itoa(dpi)+" DPI: "+why)
	}
	if s == nil || len(s.Ranges) == 0 {
		return bad("no sensor")
	}
	rg, ok := legalRange(s, dpi)
	if !ok {
		return bad("not a DPI of sensor " + s.ID)
	}
	flags = rg.DPIEx | rg.DPIEx<<4
	if flags&3 != flags>>4&3 {
		return bad("sensor " + s.ID + " range flags differ for X and Y")
	}
	m := multiplier(flags & 3)
	if dpi%m != 0 {
		return bad("not a multiple of the range multiplier")
	}
	t, r0 := dpi/m, s.Ranges[0]
	var v int
	if len(s.Values) > 0 {
		if t < r0.Min || (t-r0.Min)%r0.Step != 0 || (t-r0.Min)/r0.Step >= len(s.Values) {
			return bad("outside the value table of sensor " + s.ID)
		}
		v = int(s.Values[(t-r0.Min)/r0.Step])
	} else {
		if t%r0.Step != 0 || t < r0.Step {
			return bad("not on the base step of sensor " + s.ID)
		}
		v = t/r0.Step - 1
	}
	if v > maxRaw {
		return bad("raw code does not fit 10 bits")
	}
	return uint16(v), flags, nil
}

func decodeAxis(s *catalog.Sensor, raw uint16, f byte) (int, bool) {
	r0 := s.Ranges[0]
	var d int
	if len(s.Values) > 0 {
		i := slices.Index(s.Values, raw)
		if i < 0 {
			return 0, false
		}
		d = i*r0.Step + r0.Min
	} else {
		d = (int(raw) + 1) * r0.Step
	}
	d *= multiplier(f)
	_, ok := legalRange(s, d)
	return d, ok
}

func legalRange(s *catalog.Sensor, dpi int) (catalog.Range, bool) {
	for i := len(s.Ranges) - 1; i >= 0; i-- {
		r := s.Ranges[i]
		if dpi >= r.Min {
			return r, r.Step > 0 && dpi <= r.Max && (dpi-r.Min)%r.Step == 0
		}
	}
	return catalog.Range{}, false
}

func multiplier(f byte) int {
	m := 1
	if f&1 != 0 {
		m *= 2
	}
	if f&2 != 0 {
		m *= 2
	}
	return m
}

func EncodeStageCount(n int) (flash.Pair, error) {
	if n < 1 || n > MaxStages {
		return flash.Pair{}, newError(ErrValue, "stage count "+strconv.Itoa(n))
	}
	return flash.NewPair(byte(n)), nil
}

func DecodeStageCount(p flash.Pair) (int, error) {
	v, err := pairValue(p)
	if err != nil {
		return 0, err
	}
	if v < 1 || v > MaxStages {
		return 0, newError(ErrInvalid, "stage count "+strconv.Itoa(int(v)))
	}
	return int(v), nil
}

func EncodeCurrentStage(i int) (flash.Pair, error) {
	if i < 0 || i >= MaxStages {
		return flash.Pair{}, newError(ErrValue, "current stage "+strconv.Itoa(i))
	}
	return flash.NewPair(byte(i)), nil
}

func DecodeCurrentStage(p flash.Pair) (int, error) {
	v, err := pairValue(p)
	if err != nil {
		return 0, err
	}
	if v >= MaxStages {
		return 0, newError(ErrInvalid, "current stage "+strconv.Itoa(int(v)))
	}
	return int(v), nil
}

func EncodeColor(c [3]byte) flash.Record { return flash.NewRecord(c[:]...) }

func DecodeColor(b []byte) ([3]byte, error) {
	r, err := record(b, recordSize)
	if err != nil {
		return [3]byte{}, err
	}
	return [3]byte(r), nil
}

func EncodeDPILight(level int) (flash.Pair, error) {
	b := DPILightBrightness(level)
	if l, ok := DPILightLevel(b); !ok || l != level {
		return flash.Pair{}, newError(ErrValue, "DPI light level "+strconv.Itoa(level))
	}
	return flash.NewPair(b), nil
}

func DecodeDPILight(p flash.Pair) (int, error) {
	v, err := pairValue(p)
	// Level 10 is brightness 0xFF, which a pair holds as "ff 56", the unset marker.
	if errors.Is(err, ErrUnset) {
		v, err = p.Value(), nil
	}
	if err != nil {
		return 0, err
	}
	l, ok := DPILightLevel(v)
	if !ok {
		return 0, newError(ErrInvalid, "DPI light brightness "+strconv.Itoa(int(v)))
	}
	return l, nil
}
