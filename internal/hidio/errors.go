package hidio

import (
	"errors"
	"io/fs"
	"regexp"
	"strconv"
	"syscall"

	"github.com/positronico/arcctl/internal/third_party/usbhid"
)

var (
	ErrForbidden = errors.New("hidio: refused by the guard")
	ErrClosed    = errors.New("hidio: closed")
	ErrStalled   = errors.New("hidio: write stalled")
	ErrNotFound  = errors.New("hidio: device not found")
	ErrBackend   = errors.New("hidio: unknown backend")
	ErrDiverged  = errors.New("hidio: replay diverged from the transcript")
)

// Class groups errors by what the session does about them.
type Class uint8

const (
	ClassNone       Class = iota // no error
	ClassOther                   // anything not listed below
	ClassRefused                 // the guard refused the packet; nothing was sent
	ClassRetry                   // kIOReturnError, which the backends retry on their own
	ClassTimeout                 // kIOReturnTimeout: the device did not take the report
	ClassLocked                  // kIOReturnNotPermitted: screen locked or Secure Input on
	ClassPermission              // kIOReturnNotPrivileged, or EACCES/EPERM on Linux
	ClassSeized                  // another process opened the device exclusively
	ClassGone                    // the device was closed or went away
	ClassStalled                 // the write watchdog fired
)

var classNames = [...]string{"none", "other", "refused", "retry", "timeout", "locked", "permission", "seized", "gone", "stalled"}

func (c Class) String() string {
	if int(c) < len(classNames) {
		return classNames[c]
	}
	return "class " + strconv.Itoa(int(c))
}

const (
	ioReturnError         = 0xE00002BC
	ioReturnNoDevice      = 0xE00002C0
	ioReturnNotPrivileged = 0xE00002C1
	ioReturnExclusive     = 0xE00002C5
	ioReturnNotOpen       = 0xE00002CD
	ioReturnTimeout       = 0xE00002D6
	ioReturnOffline       = 0xE00002D7
	ioReturnNotAttached   = 0xE00002D9
	ioReturnNotPermitted  = 0xE00002E2
	ioReturnAborted       = 0xE00002EB
	ioReturnNotResponding = 0xE00002ED
	ioReturnNotFound      = 0xE00002F0
)

// ioReturnText matches the code both HID libraries print: usbhid ends its error
// text with "(0xe00002e2)", hidapi puts "(0xE00002BC)" after the call name.
var ioReturnText = regexp.MustCompile(`\(0x([0-9A-Fa-f]{8})\)`)

// IOReturn extracts the IOKit return code from an error's text, which is the only
// place either HID library exposes it.
func IOReturn(err error) (uint32, bool) {
	if err == nil {
		return 0, false
	}
	m := ioReturnText.FindAllStringSubmatch(err.Error(), -1)
	if len(m) == 0 {
		return 0, false
	}
	v, perr := strconv.ParseUint(m[len(m)-1][1], 16, 32)
	if perr != nil || v&0xFC000000 != 0xE0000000 {
		return 0, false
	}
	return uint32(v), true
}

// Classify maps an error from this package, a backend or the OS to a Class.
func Classify(err error) Class {
	switch {
	case err == nil:
		return ClassNone
	case errors.Is(err, ErrForbidden):
		return ClassRefused
	case errors.Is(err, ErrStalled):
		return ClassStalled
	case errors.Is(err, usbhid.ErrDeviceLocked):
		return ClassSeized
	case errors.Is(err, ErrClosed), errors.Is(err, ErrNotFound),
		errors.Is(err, usbhid.ErrDeviceIsClosed), errors.Is(err, usbhid.ErrNoDeviceFound),
		errors.Is(err, syscall.ENODEV), errors.Is(err, syscall.ENXIO):
		return ClassGone
	case errors.Is(err, fs.ErrPermission):
		return ClassPermission
	}
	code, ok := IOReturn(err)
	if !ok {
		return ClassOther
	}
	switch code {
	case ioReturnError:
		return ClassRetry
	case ioReturnTimeout:
		return ClassTimeout
	case ioReturnNotPermitted:
		return ClassLocked
	case ioReturnNotPrivileged:
		return ClassPermission
	case ioReturnExclusive:
		return ClassSeized
	case ioReturnNoDevice, ioReturnNotOpen, ioReturnOffline, ioReturnNotAttached,
		ioReturnAborted, ioReturnNotResponding, ioReturnNotFound:
		return ClassGone
	}
	return ClassOther
}
