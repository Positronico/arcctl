package platform

import (
	"errors"
	"os"
	"syscall"
)

const accessRW = 0x4 | 0x2 // R_OK | W_OK

// CheckPermission checks read/write access to the hidraw node of every known
// device. It is Denied when any node is refused (install the udev rules) and
// Unknown when no known device is plugged in.
func CheckPermission() (Permission, error) {
	nodes, err := hidrawNodes(os.DirFS("/sys/class/hidraw"), "/dev", func(path string) error {
		return syscall.Access(path, accessRW)
	})
	if err != nil {
		return Permission{}, err
	}
	return Permission{Access: nodesAccess(nodes), Nodes: nodes}, nil
}

// RequestPermission cannot prompt on Linux; it reports CheckPermission's answer.
func RequestPermission() (Access, error) {
	p, err := CheckPermission()
	return p.Access, err
}

// ConsoleState is macOS only.
func ConsoleState() (Console, error) { return Console{}, errors.ErrUnsupported }

// HIDClients is macOS only.
func HIDClients() ([]DeviceClients, error) { return nil, errors.ErrUnsupported }
