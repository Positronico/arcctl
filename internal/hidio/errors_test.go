package hidio_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/third_party/usbhid"
	"github.com/positronico/arcctl/internal/wire"
)

func TestIOReturn(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code uint32
		ok   bool
	}{
		{"usbhid write", fmt.Errorf("%w [rid=8; vid=0x260d; pid=0x1282]: %w", usbhid.ErrSetOutputReportFailed, errors.New("(iokit/common) not permitted (0xe00002e2)")), 0xE00002E2, true},
		{"hidapi write", errors.New("IOHIDDeviceSetReport failed: (0xE00002BC) (iokit/common) general error"), 0xE00002BC, true},
		{"usb family code", errors.New("(iokit/usb) pipe is stalled (0xe0004051)"), 0xE0004051, true},
		{"not an IOReturn", errors.New("code (0x12345678)"), 0, false},
		{"no code", errors.New("usb hid device is closed"), 0, false},
		{"nil", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := hidio.IOReturn(tt.err)
			if code != tt.code || ok != tt.ok {
				t.Fatalf("IOReturn = %#x, %v; want %#x, %v", code, ok, tt.code, tt.ok)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	iokit := func(code uint32) error {
		return fmt.Errorf("%w [rid=8]: %w", usbhid.ErrSetOutputReportFailed, fmt.Errorf("(iokit/common) error (0x%08x)", code))
	}
	tests := []struct {
		name string
		err  error
		want hidio.Class
	}{
		{"nil", nil, hidio.ClassNone},
		{"guard", fmt.Errorf("%w: %w", hidio.ErrForbidden, wire.ErrTarget), hidio.ClassRefused},
		{"general error", iokit(0xE00002BC), hidio.ClassRetry},
		{"timeout", iokit(0xE00002D6), hidio.ClassTimeout},
		{"screen locked", iokit(0xE00002E2), hidio.ClassLocked},
		{"not privileged", iokit(0xE00002C1), hidio.ClassPermission},
		{"exclusive access", iokit(0xE00002C5), hidio.ClassSeized},
		{"usbhid seized", fmt.Errorf("%w [vid=0x260d]", usbhid.ErrDeviceLocked), hidio.ClassSeized},
		{"no device", iokit(0xE00002C0), hidio.ClassGone},
		{"aborted", iokit(0xE00002EB), hidio.ClassGone},
		{"not responding", iokit(0xE00002ED), hidio.ClassGone},
		{"usbhid closed", fmt.Errorf("%w [x]: %w", usbhid.ErrGetInputReportFailed, usbhid.ErrDeviceIsClosed), hidio.ClassGone},
		{"closed", hidio.ErrClosed, hidio.ClassGone},
		{"stalled", hidio.ErrStalled, hidio.ClassStalled},
		{"linux EACCES", &os.PathError{Op: "open", Path: "/dev/hidraw0", Err: syscall.EACCES}, hidio.ClassPermission},
		{"permission", fs.ErrPermission, hidio.ClassPermission},
		{"linux ENODEV", &os.PathError{Op: "read", Path: "/dev/hidraw0", Err: syscall.ENODEV}, hidio.ClassGone},
		{"unknown IOReturn", iokit(0xE00002C7), hidio.ClassOther},
		{"other", errors.New("boom"), hidio.ClassOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hidio.Classify(tt.err); got != tt.want {
				t.Fatalf("Classify(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}
