package platform

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
)

const hidRequestListenEvent = 1

func accessOf(v uint32) Access {
	switch v {
	case 0:
		return AccessGranted
	case 1:
		return AccessDenied
	}
	return AccessUnknown
}

// CheckPermission reads the Input Monitoring status (IOHIDCheckAccess) and names
// the app macOS charges the grant to.
func CheckPermission() (Permission, error) {
	if err := load(); err != nil {
		return Permission{}, err
	}
	return Permission{
		Access: accessOf(ioHIDCheckAccess(hidRequestListenEvent)),
		App:    resolveApp(os.Getpid(), darwinProcs{}, os.Getenv),
	}, nil
}

// RequestPermission asks macOS to prompt for Input Monitoring. The prompt goes
// to the responsible app and appears only while the status is unknown.
func RequestPermission() (Access, error) {
	if err := load(); err != nil {
		return AccessUnknown, err
	}
	if ioHIDRequestAccess(hidRequestListenEvent) {
		return AccessGranted, nil
	}
	return accessOf(ioHIDCheckAccess(hidRequestListenEvent)), nil
}

// ConsoleState reads the screen lock and the Secure Input holder from the login
// session: CGSessionCopyCurrentDictionary, or the registry root's IOConsoleUsers
// when there is no window-server session.
func ConsoleState() (Console, error) {
	if err := load(); err != nil {
		return Console{}, err
	}
	d, ok := sessionFromCG()
	if !ok {
		d, ok = sessionFromRegistry(os.Getuid())
	}
	if !ok {
		return Console{}, ErrNoSession
	}
	c := parseConsole(d)
	if c.SecureInput.PID != 0 {
		c.SecureInput = process(c.SecureInput.PID, "")
	}
	return c, nil
}

func sessionFromCG() (map[string]any, bool) {
	if cgSessionCopyCurrentDictionary == nil {
		return nil, false
	}
	ref := cgSessionCopyCurrentDictionary()
	if ref == 0 {
		return nil, false
	}
	defer cfRelease(ref)
	d, ok := goValue(ref, 0).(map[string]any)
	return d, ok
}

func sessionFromRegistry(uid int) (map[string]any, bool) {
	root := ioRegistryGetRootEntry(0)
	if root == 0 {
		return nil, false
	}
	defer ioObjectRelease(root)
	users, _ := property(root, "IOConsoleUsers").([]any)
	return consoleSession(users, uid)
}

// HIDClients lists the IOHIDLibUserClients (the handles IOHIDDeviceOpen
// creates) on every HID interface whose VID and PID the catalog knows. It reads
// the registry only.
func HIDClients() ([]DeviceClients, error) {
	if err := load(); err != nil {
		return nil, err
	}
	matching := ioServiceMatching("IOHIDDevice")
	if matching == 0 {
		return nil, errors.New("platform: IOServiceMatching failed")
	}
	var it uint32
	if kr := ioServiceGetMatchingServices(0, matching, &it); kr != 0 {
		return nil, fmt.Errorf("platform: IOServiceGetMatchingServices: %#x", uint32(kr))
	}
	defer ioObjectRelease(it)
	self := os.Getpid()
	var out []DeviceClients
	for svc := ioIteratorNext(it); svc != 0; svc = ioIteratorNext(it) {
		if d, ok := deviceClients(svc, self); ok {
			out = append(out, d)
		}
		ioObjectRelease(svc)
	}
	slices.SortFunc(out, func(a, b DeviceClients) int {
		return cmp.Or(cmp.Compare(a.VID, b.VID), cmp.Compare(a.PID, b.PID),
			cmp.Compare(a.Interface, b.Interface), strings.Compare(a.Path, b.Path))
	})
	return out, nil
}

func deviceClients(svc uint32, self int) (DeviceClients, bool) {
	vid, ok1 := asInt(property(svc, "VendorID"))
	pid, ok2 := asInt(property(svc, "ProductID"))
	if !ok1 || !ok2 || vid < 0 || vid > 0xFFFF || pid < 0 || pid > 0xFFFF ||
		catalog.Classify(uint16(vid), uint16(pid)) == catalog.ClassUnknown {
		return DeviceClients{}, false
	}
	d := DeviceClients{VID: uint16(vid), PID: uint16(pid), Interface: -1}
	if n, ok := asInt(inheritedProperty(svc, "bInterfaceNumber")); ok {
		d.Interface = int(n)
	}
	path := make([]byte, 512)
	if ioRegistryEntryGetPath(svc, "IOService", path) == 0 {
		d.Path = cString(path)
	}
	ioRegistryEntryGetRegistryID(svc, &d.RegistryID)

	var children uint32
	if ioRegistryEntryGetChildIterator(svc, "IOService", &children) != 0 {
		return d, true
	}
	defer ioObjectRelease(children)
	for c := ioIteratorNext(children); c != 0; c = ioIteratorNext(children) {
		if ioObjectConformsTo(c, "IOHIDLibUserClient") != 0 {
			if cl, ok := parseClient(property(c, "IOUserClientCreator"), property(c, "DebugState")); ok {
				cl.Process = process(cl.PID, cl.Name)
				cl.Self = cl.PID == self
				d.Clients = append(d.Clients, cl)
			}
		}
		ioObjectRelease(c)
	}
	return d, true
}
