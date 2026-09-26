package flash

import "strconv"

type FieldState uint8

const (
	Unknown FieldState = iota
	Erased
	Invalid
	OK
	Unset
)

var fieldStateNames = [...]string{"unknown", "erased", "invalid", "ok", "unset"}

func (s FieldState) String() string {
	if int(s) < len(fieldStateNames) {
		return fieldStateNames[s]
	}
	return "FieldState(" + strconv.Itoa(int(s)) + ")"
}

func (s FieldState) Valid() bool { return s == OK || s == Unset }

type Pair [2]byte

func NewPair(v byte) Pair { return Pair{v, 0x55 - v} }

func (p Pair) Value() byte { return p[0] }

func (p Pair) State() FieldState {
	switch {
	case p == Pair{0xFF, 0xFF}:
		return Erased
	case p[0]+p[1] != 0x55:
		return Invalid
	case p[0] == 0xFF:
		return Unset
	}
	return OK
}

type Record []byte

func NewRecord(body ...byte) Record {
	r := make(Record, len(body)+1)
	copy(r, body)
	r[len(body)] = 0x55 - sum(body)
	return r
}

func (r Record) Body() []byte {
	if len(r) == 0 {
		return nil
	}
	return r[:len(r)-1]
}

func (r Record) Verify() bool { return len(r) > 0 && sum(r) == 0x55 }

func (r Record) State() FieldState {
	switch {
	case len(r) == 0:
		return Unknown
	case all(r, 0xFF):
		return Erased
	case sum(r) != 0x55:
		return Invalid
	}
	return OK
}

type SlotClass uint8

const (
	SlotUnknown SlotClass = iota
	SlotEmpty
	SlotValid
	SlotInvalid
)

var slotClassNames = [...]string{"unknown", "empty", "valid", "invalid"}

func (c SlotClass) String() string {
	if int(c) < len(slotClassNames) {
		return slotClassNames[c]
	}
	return "SlotClass(" + strconv.Itoa(int(c)) + ")"
}

func ClassifySlot(b []byte, valid bool) SlotClass {
	switch {
	case len(b) == 0:
		return SlotUnknown
	case all(b, 0xFF), all(b, 0x00):
		return SlotEmpty
	case valid:
		return SlotValid
	}
	return SlotInvalid
}

func sum(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return s
}

func all(b []byte, v byte) bool {
	for _, x := range b {
		if x != v {
			return false
		}
	}
	return true
}
