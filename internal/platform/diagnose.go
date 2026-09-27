package platform

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/positronico/arcctl/internal/hidio"
)

// Diagnosis explains a device error in terms of what the OS is doing.
type Diagnosis struct {
	Class   hidio.Class
	Code    uint32 // IOKit return code, 0 when the error carries none
	Summary string
	Hint    string  // what the user can do; "" when nothing
	Holder  Process // the Secure Input holder or the process that seized the device
}

type system interface {
	console() (Console, error)
	permission() (Permission, error)
	clients() ([]DeviceClients, error)
}

type host struct{}

func (host) console() (Console, error)         { return ConsoleState() }
func (host) permission() (Permission, error)   { return CheckPermission() }
func (host) clients() ([]DeviceClients, error) { return HIDClients() }

// Diagnose classifies err with hidio.Classify and, for the classes the OS
// explains (Locked, Permission, Seized), asks the OS who or what is responsible.
// It never opens a device.
func Diagnose(err error) Diagnosis { return diagnose(err, host{}, runtime.GOOS) }

func diagnose(err error, sys system, goos string) Diagnosis {
	d := Diagnosis{Class: hidio.Classify(err)}
	d.Code, _ = hidio.IOReturn(err)
	switch d.Class {
	case hidio.ClassNone:
	case hidio.ClassRefused:
		d.Summary = "the guard refused the packet; nothing was sent"
	case hidio.ClassRetry:
		d.Summary = "transient IOKit error; the write is retried"
	case hidio.ClassTimeout:
		d.Summary = "the device did not take the report in time"
	case hidio.ClassLocked:
		diagnoseLocked(&d, sys)
	case hidio.ClassPermission:
		diagnosePermission(&d, sys, goos)
	case hidio.ClassSeized:
		d.Summary = "another process has the device open exclusively"
		d.Hint = "Quit the process that holds it, then retry."
		if h, ok := seizer(sys); ok {
			seizedBy(&d, h)
		}
	case hidio.ClassGone:
		d.Summary = "the device was unplugged or closed"
	case hidio.ClassStalled:
		d.Summary = "a write never completed"
		d.Hint = "Replug the receiver; if it happens again, restart arcctl."
	default:
		d.Summary = err.Error()
	}
	return d
}

func diagnoseLocked(d *Diagnosis, sys system) {
	c, err := sys.console()
	switch {
	case err == nil && c.ScreenLocked:
		d.Summary = "the screen is locked"
		d.Hint = "Unlock the Mac, then retry."
	case err == nil && c.SecureInput.PID != 0:
		d.Holder = c.SecureInput
		d.Summary = "Secure Input is on, held by " + c.SecureInput.String()
		d.Hint = fmt.Sprintf("Close the password field or turn off Secure Keyboard Entry in %s, then retry.", orUnknown(displayName(c.SecureInput)))
	default:
		d.Summary = "macOS refused the report (screen lock or Secure Input)"
		d.Hint = "Unlock the Mac and close any password prompt, then retry."
	}
}

func diagnosePermission(d *Diagnosis, sys system, goos string) {
	p, err := sys.permission()
	switch {
	case goos == "darwin" && err == nil && p.Access != AccessGranted:
		app := p.App.Name
		if app == "" {
			app = "the terminal app"
		}
		d.Summary = "Input Monitoring is not granted to " + app
		d.Hint = inputMonitoringHint(p.App)
	case goos == "darwin":
		d.Summary = "macOS refused access to the device"
		d.Hint = "Another process may hold the device exclusively; quit it and retry."
		if h, ok := seizer(sys); ok {
			seizedBy(d, h)
		}
	case goos == "linux":
		d.Summary = "no read/write access to the hidraw node"
		for _, n := range p.Nodes {
			if n.Access == AccessDenied {
				d.Summary = "no read/write access to " + n.Path
				break
			}
		}
		d.Hint = "Install the udev rules, then replug the receiver:\n" + UdevHint()
	default:
		d.Summary = "the OS refused access to the device"
	}
}

func seizedBy(d *Diagnosis, h Process) {
	d.Holder = h
	d.Summary = "the device is held exclusively by " + h.String()
	d.Hint = fmt.Sprintf("Quit %s, then retry.", orUnknown(displayName(h)))
	if strings.HasPrefix(strings.ToLower(h.Name), "karabiner") {
		d.Hint = "Turn off \"Modify events\" for the receiver in Karabiner-Elements > Devices, then retry."
	}
}

func seizer(sys system) (Process, bool) {
	devs, err := sys.clients()
	if err != nil {
		return Process{}, false
	}
	for _, dev := range devs {
		for _, c := range dev.Clients {
			if c.Seized && !c.Self {
				return c.Process, true
			}
		}
	}
	return Process{}, false
}

func orUnknown(s string) string {
	if s == "" {
		return "the app that holds it"
	}
	return s
}
