package mouse

import (
	"strconv"
	"unicode/utf8"

	"github.com/positronico/arcctl/internal/keys"
)

const (
	MaxMacroEvents = 70
	MaxNameLen     = 30
	macroCount     = 1 + MaxNameLen
	macroEvents    = macroCount + 1
	eventSize      = 5
)

type Event struct {
	Press  bool
	Stroke keys.Stroke
	Delay  uint16
}

type Macro struct {
	Name   string
	Events []Event
}

func EncodeMacro(m Macro) ([]byte, error) {
	if l := len(m.Name); l < 1 || l > MaxNameLen || !utf8.ValidString(m.Name) {
		return nil, newError(ErrValue, "macro name must be 1-30 bytes of UTF-8")
	}
	n := len(m.Events)
	if n < 1 || n > MaxMacroEvents {
		return nil, newError(ErrValue, "macro with "+strconv.Itoa(n)+" events")
	}
	b := make([]byte, macroEvents, macroEvents+eventSize*n+1)
	b[0] = byte(len(m.Name))
	copy(b[1:], m.Name)
	for i := 1 + len(m.Name); i < macroCount; i++ {
		b[i] = 0xFF
	}
	b[macroCount] = byte(n)
	for i, e := range m.Events {
		if p := strokeProblem(e.Stroke, macroKinds); p != "" {
			return nil, newError(ErrValue, "macro event "+strconv.Itoa(i)+": "+p)
		}
		h := byte(eventRelease)
		if e.Press {
			h = eventPress
		}
		v := e.Stroke.Value
		b = append(b, h|byte(e.Stroke.Kind), byte(v), byte(v>>8), byte(e.Delay>>8), byte(e.Delay))
	}
	return append(b, 0x55-sum(b[macroCount:])), nil
}

func DecodeMacro(b []byte) (Macro, error) {
	if err := body(b); err != nil {
		return Macro{}, err
	}
	nl := int(b[0])
	if nl < 1 || nl > MaxNameLen {
		return Macro{}, newError(ErrInvalid, "macro name length "+strconv.Itoa(nl))
	}
	if len(b) < macroEvents {
		return Macro{}, truncated(macroEvents, len(b))
	}
	name := b[1 : 1+nl]
	switch {
	case !utf8.Valid(name):
		return Macro{}, newError(ErrInvalid, "macro name is not UTF-8")
	case !all(b[1+nl:macroCount], 0xFF):
		return Macro{}, newError(ErrInvalid, "macro name padding is not 0xFF")
	}
	n := int(b[macroCount])
	if n < 1 || n > MaxMacroEvents {
		return Macro{}, newError(ErrInvalid, "macro event count "+strconv.Itoa(n))
	}
	size := macroEvents + eventSize*n + 1
	if len(b) < size {
		return Macro{}, truncated(size, len(b))
	}
	if sum(b[macroCount:size]) != 0x55 {
		return Macro{}, newError(ErrInvalid, "macro events do not sum to 0x55")
	}
	m := Macro{Name: string(name), Events: make([]Event, n)}
	for i := range m.Events {
		e := b[macroEvents+eventSize*i:]
		h := e[0] & 0xF0
		if h != eventPress && h != eventRelease {
			return Macro{}, newError(ErrInvalid, "macro event "+strconv.Itoa(i)+" header "+hexString(e[:1]))
		}
		s := keys.Stroke{Kind: keys.Kind(e[0] & 0x0F), Value: uint16(e[1]) | uint16(e[2])<<8}
		if p := strokeProblem(s, macroKinds); p != "" {
			return Macro{}, newError(ErrInvalid, "macro event "+strconv.Itoa(i)+": "+p)
		}
		m.Events[i] = Event{Press: h == eventPress, Stroke: s, Delay: uint16(e[3])<<8 | uint16(e[4])}
	}
	return m, nil
}
