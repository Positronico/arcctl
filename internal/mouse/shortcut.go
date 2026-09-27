package mouse

import (
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
)

const (
	MaxShortcutKeys = 5
	eventPress      = 0x80
	eventRelease    = 0x40
)

var (
	shortcutKinds = []keys.Kind{keys.KindModifier, keys.KindKey, keys.KindConsumer, keys.KindMenu}
	macroKinds    = []keys.Kind{keys.KindModifier, keys.KindKey, keys.KindMouse, keys.KindMenu}
)

func EncodeShortcut(c keys.Combo) (flash.Record, error) {
	n := len(c)
	if n < 1 || n > MaxShortcutKeys {
		return nil, newError(ErrValue, "shortcut with "+strconv.Itoa(n)+" keys")
	}
	b := make([]byte, 1, 6*n+2)
	b[0] = byte(2 * n)
	for i, s := range c {
		if p := strokeProblem(s, shortcutKinds); p != "" {
			return nil, newError(ErrValue, "key "+strconv.Itoa(i)+": "+p)
		}
		b = append(b, eventPress|byte(s.Kind), byte(s.Value), byte(s.Value>>8))
	}
	for _, s := range slices.Backward(c) {
		b = append(b, eventRelease|byte(s.Kind), byte(s.Value), byte(s.Value>>8))
	}
	return flash.NewRecord(b...), nil
}

func EncodeMedia(usage uint16) (flash.Record, error) {
	return EncodeShortcut(keys.Combo{{Kind: keys.KindConsumer, Value: usage}})
}

func DecodeShortcut(b []byte) (keys.Combo, error) {
	if err := body(b); err != nil {
		return nil, err
	}
	count := int(b[0])
	if count == 0 || count%2 != 0 || count > 2*MaxShortcutKeys {
		return nil, newError(ErrInvalid, "shortcut event count "+strconv.Itoa(count))
	}
	r, err := record(b, 3*count+2)
	if err != nil {
		return nil, err
	}
	n := count / 2
	c := make(keys.Combo, n)
	for i := range c {
		p, q := r[1+3*i:], r[1+3*(2*n-1-i):]
		if p[0]&0xF0 != eventPress {
			return nil, newError(ErrInvalid, "shortcut event "+strconv.Itoa(i+1)+" is not a press")
		}
		s := keys.Stroke{Kind: keys.Kind(p[0] & 0x0F), Value: uint16(p[1]) | uint16(p[2])<<8}
		if q[0] != eventRelease|byte(s.Kind) || q[1] != p[1] || q[2] != p[2] {
			return nil, newError(ErrInvalid, "shortcut release "+strconv.Itoa(n-1-i)+" does not mirror press "+strconv.Itoa(i))
		}
		if p := strokeProblem(s, shortcutKinds); p != "" {
			return nil, newError(ErrInvalid, "shortcut key "+strconv.Itoa(i)+": "+p)
		}
		c[i] = s
	}
	return c, nil
}

func strokeProblem(s keys.Stroke, kinds []keys.Kind) string {
	if !slices.Contains(kinds, s.Kind) {
		return "kind " + strconv.Itoa(int(s.Kind)) + " not allowed here"
	}
	limit := uint16(0xFF)
	if s.Kind == keys.KindConsumer {
		limit = 0xFFFF
	}
	if s.Value == 0 || s.Value > limit {
		return s.Kind.String() + " value " + hex16(s.Value) + " out of range"
	}
	return ""
}
