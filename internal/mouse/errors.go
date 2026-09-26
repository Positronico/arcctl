package mouse

import (
	"errors"
	"strconv"

	"github.com/positronico/arcctl/internal/flash"
)

var (
	ErrValue     = errors.New("mouse: value outside the codec domain")
	ErrEmpty     = errors.New("mouse: empty")
	ErrUnset     = errors.New("mouse: unset")
	ErrTruncated = errors.New("mouse: truncated")
	ErrInvalid   = errors.New("mouse: invalid")
)

type codecError struct {
	kind   error
	detail string
}

func (e *codecError) Error() string { return e.kind.Error() + ": " + e.detail }

func (e *codecError) Unwrap() error { return e.kind }

func newError(kind error, detail string) error {
	return &codecError{kind: kind, detail: detail}
}

func State(err error) flash.FieldState {
	switch {
	case err == nil:
		return flash.OK
	case errors.Is(err, ErrEmpty):
		return flash.Erased
	case errors.Is(err, ErrUnset):
		return flash.Unset
	case errors.Is(err, ErrTruncated):
		return flash.Unknown
	}
	return flash.Invalid
}

func Class(err error) flash.SlotClass {
	switch {
	case err == nil:
		return flash.SlotValid
	case errors.Is(err, ErrEmpty):
		return flash.SlotEmpty
	case errors.Is(err, ErrTruncated):
		return flash.SlotUnknown
	}
	return flash.SlotInvalid
}

func pairValue(p flash.Pair) (byte, error) {
	switch p.State() {
	case flash.Erased:
		return 0, ErrEmpty
	case flash.Unset:
		return 0, ErrUnset
	case flash.Invalid:
		return 0, newError(ErrInvalid, "pair "+hexString(p[:])+" does not sum to 0x55")
	}
	return p.Value(), nil
}

func record(b []byte, n int) (flash.Record, error) {
	if len(b) < n {
		return nil, truncated(n, len(b))
	}
	r := flash.Record(b[:n])
	switch r.State() {
	case flash.Erased:
		return nil, ErrEmpty
	case flash.Invalid:
		return nil, newError(ErrInvalid, "record "+hexString(r)+" does not sum to 0x55")
	}
	return r, nil
}

func body(b []byte) error {
	switch {
	case len(b) == 0:
		return truncated(1, 0)
	case all(b, 0xFF), all(b, 0x00):
		return ErrEmpty
	}
	return nil
}

func truncated(need, have int) error {
	return newError(ErrTruncated, "need "+strconv.Itoa(need)+" bytes, have "+strconv.Itoa(have))
}

func all(b []byte, v byte) bool {
	for _, x := range b {
		if x != v {
			return false
		}
	}
	return true
}

func sum(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return s
}

const hexDigits = "0123456789abcdef"

func hexString(b []byte) string {
	out := make([]byte, 0, 3*len(b))
	for i, x := range b {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, hexDigits[x>>4], hexDigits[x&0x0F])
	}
	return string(out)
}

func hex16(v uint16) string {
	return "0x" + string([]byte{hexDigits[v>>12], hexDigits[v>>8&0x0F], hexDigits[v>>4&0x0F], hexDigits[v&0x0F]})
}
