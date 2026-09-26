package mouse

import (
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

var rateCodes = [...]struct {
	hz   int
	code byte
}{{125, 8}, {250, 4}, {500, 2}, {1000, 1}, {2000, 16}, {4000, 32}, {8000, 64}}

const lowRateSensor = "3212"

func Rates() []int {
	out := make([]int, len(rateCodes))
	for i, r := range rateCodes {
		out[i] = r.hz
	}
	return out
}

func EncodeRate(hz int) (flash.Pair, error) {
	for _, r := range rateCodes {
		if r.hz == hz {
			return flash.NewPair(r.code), nil
		}
	}
	return flash.Pair{}, newError(ErrValue, "report rate "+strconv.Itoa(hz)+" Hz")
}

func DecodeRate(p flash.Pair) (int, error) {
	v, err := pairValue(p)
	if err != nil {
		return 0, err
	}
	for _, r := range rateCodes {
		if r.code == v {
			return r.hz, nil
		}
	}
	return 0, newError(ErrInvalid, "report rate code "+hexString([]byte{v}))
}

func MaxRate(conn byte) (int, bool) {
	switch conn {
	case 0, 2:
		return 1000, true
	case 1:
		return 4000, true
	case 3, 5:
		return 8000, true
	case 4:
		return 2000, true
	}
	return 0, false
}

func RateOptions(conn byte, s *catalog.Sensor) []int {
	limit, ok := MaxRate(conn)
	if !ok {
		return nil
	}
	if s != nil && s.ID == lowRateSensor {
		return []int{rateCodes[0].hz}
	}
	var out []int
	for _, r := range rateCodes {
		if r.hz <= limit {
			out = append(out, r.hz)
		}
	}
	return out
}
