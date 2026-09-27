// Package platform answers the operating-system questions around device access:
// permission (macOS Input Monitoring, Linux hidraw nodes), the macOS console state
// (screen lock, Secure Input), the HID clients sharing a receiver, the
// single-instance lock and the data paths. It never opens a device.
package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNoSession means there is no login session to ask about, such as an SSH
// login with nobody at the console.
var ErrNoSession = errors.New("platform: no login session")

// Access is what the OS says about opening the devices.
type Access uint8

const (
	AccessUnknown   Access = iota // not decided yet, not checkable, or no device to check
	AccessGranted                 // allowed
	AccessDenied                  // refused: NeedsPermission
	AccessNotNeeded               // the OS has no such check
)

var accessNames = [...]string{"unknown", "granted", "denied", "not needed"}

func (a Access) String() string {
	if int(a) < len(accessNames) {
		return accessNames[a]
	}
	return "access " + strconv.Itoa(int(a))
}

// Process identifies a running process. Name is the executable's base name, or
// the kernel's short name when the path cannot be read.
type Process struct {
	PID  int
	Name string
	Path string
}

func (p Process) String() string {
	if p.PID == 0 {
		return "none"
	}
	name := p.Name
	if name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("%s (pid %d)", name, p.PID)
}

// Via says how the responsible app was found.
type Via uint8

const (
	ViaNone           Via = iota // not found
	ViaResponsibility            // the kernel's responsible-process record
	ViaSelf                      // arcctl itself is responsible: it was not started from an app
	ViaProcessTree               // the nearest ancestor inside an app bundle
	ViaTermProgram               // only the TERM_PROGRAM variable
)

var viaNames = [...]string{"none", "responsibility", "self", "process tree", "TERM_PROGRAM"}

func (v Via) String() string {
	if int(v) < len(viaNames) {
		return viaNames[v]
	}
	return "via " + strconv.Itoa(int(v))
}

// App is the process macOS charges the Input Monitoring grant to: normally the
// terminal app arcctl runs in.
type App struct {
	Name   string  // bundle name ("Ghostty"), else executable name, else TERM_PROGRAM
	Bundle string  // the .app directory when the executable is inside one
	Proc   Process // PID 0 when only TERM_PROGRAM named it
	Via    Via
	Tmux   bool // inside tmux the grant belongs to the app that started the tmux server
}

// Permission is the OS answer before any device is opened.
type Permission struct {
	Access Access
	App    App    // macOS
	Nodes  []Node // Linux: the hidraw nodes of known devices
}

// Node is a Linux hidraw node that belongs to a device the catalog knows.
type Node struct {
	Path      string
	VID, PID  uint16
	Interface int // USB interface number, -1 when unknown
	Access    Access
	Err       error // why access is not granted
}

// Console is the macOS login session state that blocks HID writes.
type Console struct {
	ScreenLocked bool
	SecureInput  Process // PID 0 when Secure Input is off
}

// Client is a process holding an IOHIDLibUserClient on a device.
type Client struct {
	Process
	Seized bool // opened exclusively; every other client's writes fail
	Self   bool // this process
}

// DeviceClients lists the clients of one HID interface the catalog knows.
type DeviceClients struct {
	VID, PID   uint16
	Interface  int    // USB interface number, -1 when unknown
	Path       string // IOService path, as the usbhid backend names the device
	RegistryID uint64 // the hidapi backend names the device "DevSrvsID:<RegistryID>"
	Clients    []Client
}

// Matches reports whether a backend's device path names this interface.
func (d DeviceClients) Matches(path string) bool {
	if path == "" {
		return false
	}
	return path == d.Path || d.RegistryID != 0 && path == "DevSrvsID:"+strconv.FormatUint(d.RegistryID, 10)
}

var knownBenign = []string{"karabiner_observer"}

var browsers = []string{"google chrome", "chrome", "chromium", "microsoft edge", "msedge", "brave browser", "brave", "opera", "vivaldi", "arc", "firefox", "safari"}

// Foreign returns the clients that are neither this process nor benign. Benign
// clients are the known ones (karabiner_observer) and the names in benign; a
// browser or a client that seized the device is never benign.
func Foreign(clients []Client, benign []string) []Client {
	var out []Client
	for _, c := range clients {
		if c.Self {
			continue
		}
		if !c.Seized && !isBrowser(c.Process) && (nameIn(c.Name, knownBenign) || nameIn(c.Name, benign)) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// isBrowser matches the executable and bundle names, so helper processes
// ("Google Chrome Helper (Renderer)") count as their browser.
func isBrowser(p Process) bool {
	_, bundle := bundleOf(p.Path)
	for _, n := range []string{p.Name, bundle} {
		n = strings.TrimSuffix(strings.ToLower(n), ".exe")
		for _, b := range browsers {
			if n == b || strings.HasPrefix(n, b+" ") || strings.HasPrefix(n, b+"-") {
				return true
			}
		}
	}
	return false
}

// maxComLen is the length the kernel truncates process names to in the
// registry's IOUserClientCreator.
const maxComLen = 16

func nameIn(name string, list []string) bool {
	if name == "" {
		return false
	}
	for _, n := range list {
		if name == n || len(name) == maxComLen && strings.HasPrefix(n, name) {
			return true
		}
	}
	return false
}

// displayName prefers the app bundle's name ("Ghostty") to the executable's.
func displayName(p Process) string {
	if _, name := bundleOf(p.Path); name != "" {
		return name
	}
	return p.Name
}

// bundleOf returns the outermost .app directory in path and its name, since
// macOS charges helpers inside an app to the app itself.
func bundleOf(path string) (bundle, name string) {
	i := strings.Index(path, ".app/")
	if i < 0 {
		return "", ""
	}
	bundle = path[:i+len(".app")]
	return bundle, strings.TrimSuffix(filepath.Base(bundle), ".app")
}
