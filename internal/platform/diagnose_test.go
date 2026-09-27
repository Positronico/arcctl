package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

type fakeSystem struct {
	cons    Console
	consErr error
	perm    Permission
	permErr error
	devs    []DeviceClients
	devsErr error
}

func (f fakeSystem) console() (Console, error)         { return f.cons, f.consErr }
func (f fakeSystem) permission() (Permission, error)   { return f.perm, f.permErr }
func (f fakeSystem) clients() ([]DeviceClients, error) { return f.devs, f.devsErr }

func iokitErr(code uint32) error {
	return fmt.Errorf("set usb hid output report failed [rid=8]: (iokit/common) error (0x%08x)", code)
}

func TestDiagnose(t *testing.T) {
	ghostty := App{Name: "Ghostty", Bundle: "/Applications/Ghostty.app", Via: ViaResponsibility, Proc: Process{PID: 2686, Name: "ghostty"}}
	loginwindow := Process{PID: 200, Name: "loginwindow"}
	grabber := Process{PID: 408, Name: "karabiner_grabber"}
	seized := []DeviceClients{{Clients: []Client{
		{Process: Process{PID: 409, Name: "karabiner_observer"}},
		{Process: grabber, Seized: true},
	}}}
	eacces := &fs.PathError{Op: "open", Path: "/dev/hidraw1", Err: syscall.EACCES}

	tests := []struct {
		name    string
		err     error
		sys     fakeSystem
		goos    string
		class   hidio.Class
		code    uint32
		summary string
		hint    string // substring
		holder  Process
	}{
		{name: "nil", class: hidio.ClassNone},
		{name: "transient", err: iokitErr(0xE00002BC), class: hidio.ClassRetry, code: 0xE00002BC, summary: "transient IOKit error; the write is retried"},
		{name: "timeout", err: iokitErr(0xE00002D6), class: hidio.ClassTimeout, code: 0xE00002D6, summary: "the device did not take the report in time"},
		{
			name: "screen locked", err: iokitErr(0xE00002E2), goos: "darwin",
			sys:   fakeSystem{cons: Console{ScreenLocked: true, SecureInput: loginwindow}},
			class: hidio.ClassLocked, code: 0xE00002E2, summary: "the screen is locked", hint: "Unlock the Mac, then retry.",
		},
		{
			name: "secure input", err: iokitErr(0xE00002E2), goos: "darwin",
			sys:   fakeSystem{cons: Console{SecureInput: Process{PID: 2686, Name: "ghostty", Path: "/Applications/Ghostty.app/Contents/MacOS/ghostty"}}},
			class: hidio.ClassLocked, code: 0xE00002E2, summary: "Secure Input is on, held by ghostty (pid 2686)",
			hint: "Secure Keyboard Entry in Ghostty, then retry.", holder: Process{PID: 2686, Name: "ghostty", Path: "/Applications/Ghostty.app/Contents/MacOS/ghostty"},
		},
		{
			name: "not permitted, console unknown", err: iokitErr(0xE00002E2), goos: "darwin",
			sys:   fakeSystem{consErr: ErrNoSession},
			class: hidio.ClassLocked, code: 0xE00002E2, summary: "macOS refused the report (screen lock or Secure Input)", hint: "close any password prompt, then retry.",
		},
		{
			name: "not permitted, nothing visible", err: iokitErr(0xE00002E2), goos: "darwin",
			class: hidio.ClassLocked, code: 0xE00002E2, summary: "macOS refused the report (screen lock or Secure Input)",
		},
		{
			name: "input monitoring denied", err: iokitErr(0xE00002C1), goos: "darwin",
			sys:   fakeSystem{perm: Permission{Access: AccessDenied, App: ghostty}},
			class: hidio.ClassPermission, code: 0xE00002C1, summary: "Input Monitoring is not granted to Ghostty",
			hint: "Allow Input Monitoring for Ghostty in System Settings > Privacy & Security > Input Monitoring, then quit and reopen Ghostty.",
		},
		{
			name: "input monitoring undecided, app unknown", err: iokitErr(0xE00002C1), goos: "darwin",
			sys:   fakeSystem{perm: Permission{Access: AccessUnknown}},
			class: hidio.ClassPermission, code: 0xE00002C1, summary: "Input Monitoring is not granted to the terminal app", hint: "the terminal app that runs arcctl",
		},
		{
			name: "not privileged although granted: seized", err: iokitErr(0xE00002C1), goos: "darwin",
			sys:   fakeSystem{perm: Permission{Access: AccessGranted, App: ghostty}, devs: seized},
			class: hidio.ClassPermission, code: 0xE00002C1, summary: "the device is held exclusively by karabiner_grabber (pid 408)",
			hint: "Modify events", holder: grabber,
		},
		{
			name: "not privileged although granted", err: iokitErr(0xE00002C1), goos: "darwin",
			sys:   fakeSystem{perm: Permission{Access: AccessGranted}},
			class: hidio.ClassPermission, code: 0xE00002C1, summary: "macOS refused access to the device", hint: "exclusively",
		},
		{
			name: "exclusive access", err: iokitErr(0xE00002C5), goos: "darwin",
			sys:   fakeSystem{devs: seized},
			class: hidio.ClassSeized, code: 0xE00002C5, summary: "the device is held exclusively by karabiner_grabber (pid 408)",
			hint: "Karabiner-Elements > Devices", holder: grabber,
		},
		{
			name: "exclusive access by another tool", err: iokitErr(0xE00002C5), goos: "darwin",
			sys:   fakeSystem{devs: []DeviceClients{{Clients: []Client{{Process: Process{PID: 9, Name: "hidtool"}, Seized: true}}}}},
			class: hidio.ClassSeized, code: 0xE00002C5, summary: "the device is held exclusively by hidtool (pid 9)",
			hint: "Quit hidtool, then retry.", holder: Process{PID: 9, Name: "hidtool"},
		},
		{
			name: "exclusive access, holder not found", err: iokitErr(0xE00002C5), goos: "darwin",
			sys:   fakeSystem{devsErr: errors.New("registry")},
			class: hidio.ClassSeized, code: 0xE00002C5, summary: "another process has the device open exclusively",
		},
		{
			name: "our own seized client is not the seizer", err: iokitErr(0xE00002C5), goos: "darwin",
			sys:   fakeSystem{devs: []DeviceClients{{Clients: []Client{{Process: Process{PID: 1, Name: "arcctl"}, Seized: true, Self: true}}}}},
			class: hidio.ClassSeized, code: 0xE00002C5, summary: "another process has the device open exclusively",
		},
		{
			name: "linux EACCES", err: eacces, goos: "linux",
			sys:   fakeSystem{perm: Permission{Access: AccessDenied, Nodes: []Node{{Path: "/dev/hidraw0", Access: AccessGranted}, {Path: "/dev/hidraw1", Access: AccessDenied}}}},
			class: hidio.ClassPermission, summary: "no read/write access to /dev/hidraw1", hint: "sudo udevadm trigger",
		},
		{
			name: "linux EACCES, nodes unknown", err: eacces, goos: "linux",
			sys:   fakeSystem{permErr: errors.New("sysfs")},
			class: hidio.ClassPermission, summary: "no read/write access to the hidraw node", hint: UdevRulesPath,
		},
		{name: "windows permission", err: fs.ErrPermission, goos: "windows", class: hidio.ClassPermission, summary: "the OS refused access to the device"},
		{name: "gone", err: iokitErr(0xE00002C0), class: hidio.ClassGone, code: 0xE00002C0, summary: "the device was unplugged or closed"},
		{name: "closed", err: hidio.ErrClosed, class: hidio.ClassGone, summary: "the device was unplugged or closed"},
		{name: "stalled", err: hidio.ErrStalled, class: hidio.ClassStalled, summary: "a write never completed", hint: "Replug the receiver"},
		{name: "refused", err: fmt.Errorf("%w: %w", hidio.ErrForbidden, wire.ErrTarget), class: hidio.ClassRefused, summary: "the guard refused the packet; nothing was sent"},
		{name: "other", err: errors.New("boom"), class: hidio.ClassOther, summary: "boom"},
		{name: "other IOReturn", err: iokitErr(0xE00002C7), class: hidio.ClassOther, code: 0xE00002C7, summary: iokitErr(0xE00002C7).Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := diagnose(tt.err, tt.sys, tt.goos)
			if d.Class != tt.class || d.Code != tt.code || d.Summary != tt.summary || d.Holder != tt.holder {
				t.Fatalf("diagnose = %+v\nwant class %v code %#x summary %q holder %+v", d, tt.class, tt.code, tt.summary, tt.holder)
			}
			if !strings.Contains(d.Hint, tt.hint) {
				t.Fatalf("hint %q lacks %q", d.Hint, tt.hint)
			}
		})
	}
}

func TestPermissionHint(t *testing.T) {
	tests := []struct {
		name string
		goos string
		p    Permission
		want string // "" means no hint at all
	}{
		{"granted", "darwin", Permission{Access: AccessGranted}, ""},
		{"not needed", "windows", Permission{Access: AccessNotNeeded}, ""},
		{"darwin denied", "darwin", Permission{Access: AccessDenied, App: App{Name: "Ghostty"}},
			"Allow Input Monitoring for Ghostty in System Settings > Privacy & Security > Input Monitoring, then quit and reopen Ghostty."},
		{"darwin tmux", "darwin", Permission{Access: AccessUnknown, App: App{Name: "Ghostty", Tmux: true}},
			"Allow Input Monitoring for Ghostty in System Settings > Privacy & Security > Input Monitoring, then quit and reopen Ghostty. " +
				"Inside tmux the grant belongs to the app that started the tmux server (Ghostty), which may not be the terminal in front of you."},
		{"darwin tmux, app unknown", "darwin", Permission{Access: AccessDenied, App: App{Tmux: true}},
			"Allow Input Monitoring for the terminal app that runs arcctl in System Settings > Privacy & Security > Input Monitoring, then quit and reopen that app. " +
				"Inside tmux the grant belongs to the app that started the tmux server, which may not be the terminal in front of you."},
		{"darwin self", "darwin", Permission{Access: AccessDenied, App: App{Name: "arcctl", Via: ViaSelf, Proc: Process{PID: 5, Path: "/opt/homebrew/bin/arcctl"}}},
			"Allow Input Monitoring for /opt/homebrew/bin/arcctl in System Settings > Privacy & Security > Input Monitoring."},
		{"linux denied", "linux", Permission{Access: AccessDenied}, "Install the udev rules, then replug the receiver:\n" + UdevHint()},
		{"linux no device", "linux", Permission{Access: AccessUnknown}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permissionHint(tt.goos, tt.p); got != tt.want {
				t.Fatalf("permissionHint =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestAccessString(t *testing.T) {
	for a, want := range map[Access]string{AccessUnknown: "unknown", AccessGranted: "granted", AccessDenied: "denied", AccessNotNeeded: "not needed", 9: "access 9"} {
		if got := a.String(); got != want {
			t.Errorf("Access(%d).String() = %q, want %q", a, got, want)
		}
	}
	for v, want := range map[Via]string{ViaNone: "none", ViaResponsibility: "responsibility", ViaSelf: "self", ViaProcessTree: "process tree", ViaTermProgram: "TERM_PROGRAM", 9: "via 9"} {
		if got := v.String(); got != want {
			t.Errorf("Via(%d).String() = %q, want %q", v, got, want)
		}
	}
}
