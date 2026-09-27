package emu

import "fmt"

// IOError is an IOKit return code. Its text ends in the "(0x…)" form both HID
// libraries print, so hidio.Classify reads it like a real one.
type IOError uint32

const (
	ErrGeneral IOError = 0xE00002BC // transient; hidio retries it
	ErrGone    IOError = 0xE00002C0 // the device went away
	ErrDenied  IOError = 0xE00002C1 // not privileged: no Input Monitoring grant
	ErrSeized  IOError = 0xE00002C5 // another process holds the device exclusively
	ErrLocked  IOError = 0xE00002E2 // screen locked or Secure Input on
)

var ioErrorText = map[IOError]string{
	ErrGeneral: "general error",
	ErrGone:    "no such device",
	ErrDenied:  "privilege violation",
	ErrSeized:  "exclusive access and device already open",
	ErrLocked:  "not permitted",
}

func (e IOError) Error() string {
	s, ok := ioErrorText[e]
	if !ok {
		s = "error"
	}
	return fmt.Sprintf("emu: (iokit/common) %s (0x%08x)", s, uint32(e))
}
