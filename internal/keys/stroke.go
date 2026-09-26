package keys

import (
	"strconv"
	"strings"
)

type Stroke struct {
	Kind  Kind
	Value uint16
}

type Combo []Stroke

func (k Key) Stroke() Stroke { return Stroke{k.Kind, uint16(k.Value)} }

func (k Key) Name(os OS) string { return k.Stroke().Name(os) }

func (s Stroke) Key() (Key, bool) {
	if s.Value > 0xFF {
		return Key{}, false
	}
	return ByValue(s.Kind, uint8(s.Value))
}

func (s Stroke) Known() bool {
	switch s.Kind {
	case KindModifier:
		return s.Value != 0 && s.Value <= 0xFF
	case KindMouse:
		return s.Value != 0 && s.Value < 1<<len(mouseButtons)
	case KindConsumer:
		_, ok := ConsumerByUsage(s.Value)
		return ok
	}
	_, ok := s.Key()
	return ok
}

func (s Stroke) Name(os OS) string {
	if !s.Known() {
		return s.raw()
	}
	switch s.Kind {
	case KindModifier:
		return Modifier(s.Value).Name(os)
	case KindMouse:
		var parts []string
		for i, name := range mouseButtons {
			if s.Value&(1<<i) != 0 {
				parts = append(parts, name)
			}
		}
		return strings.Join(parts, "+")
	case KindConsumer:
		c, _ := ConsumerByUsage(s.Value)
		return c.Label
	}
	k, _ := s.Key()
	name := k.Win
	if os == Mac {
		name = k.Mac
	}
	return strings.Join(strings.Fields(name), " ")
}

func (s Stroke) raw() string {
	digits := 2
	if s.Kind == KindConsumer || s.Value > 0xFF {
		digits = 4
	}
	h := strings.ToUpper(strconv.FormatUint(uint64(s.Value), 16))
	return "<" + s.Kind.String() + " 0x" + strings.Repeat("0", digits-len(h)) + h + ">"
}

func (c Combo) Format(os OS) string {
	names := make([]string, len(c))
	for i, s := range c {
		names[i] = s.Name(os)
	}
	return strings.Join(names, "+")
}

var mouseButtons = [...]string{"Left Button", "Right Button", "Middle Button", "Back Button", "Forward Button"}
