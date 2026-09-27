package platform

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
)

// hidrawNodes lists the hidraw nodes of known devices from sysfs (the contents
// of /sys/class/hidraw) and checks read/write access to each /dev node.
func hidrawNodes(sysfs fs.FS, devDir string, access func(string) error) ([]Node, error) {
	entries, err := fs.ReadDir(sysfs, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var nodes []Node
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "hidraw") {
			continue
		}
		uevent, err := fs.ReadFile(sysfs, path.Join(name, "device", "uevent"))
		if err != nil {
			continue
		}
		vid, pid, iface, ok := parseUevent(uevent)
		if !ok || catalog.Classify(vid, pid) == catalog.ClassUnknown {
			continue
		}
		n := Node{Path: path.Join(devDir, name), VID: vid, PID: pid, Interface: iface, Access: AccessGranted}
		if err := access(n.Path); err != nil {
			n.Access, n.Err = AccessUnknown, err
			if errors.Is(err, fs.ErrPermission) {
				n.Access = AccessDenied
			}
		}
		nodes = append(nodes, n)
	}
	slices.SortFunc(nodes, func(a, b Node) int {
		return cmp.Or(cmp.Compare(len(a.Path), len(b.Path)), strings.Compare(a.Path, b.Path))
	})
	return nodes, nil
}

var physInterface = regexp.MustCompile(`/input([0-9]+)$`)

// parseUevent reads HID_ID ("0003:0000260D:00001282": bus, vendor, product) and
// the interface number at the end of HID_PHYS ("usb-0000:00:14.0-2/input1").
func parseUevent(b []byte) (vid, pid uint16, iface int, ok bool) {
	iface = -1
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), "=")
		switch key {
		case "HID_ID":
			f := strings.Split(val, ":")
			if len(f) != 3 {
				return 0, 0, -1, false
			}
			v, err1 := strconv.ParseUint(f[1], 16, 16)
			p, err2 := strconv.ParseUint(f[2], 16, 16)
			if err1 != nil || err2 != nil {
				return 0, 0, -1, false
			}
			vid, pid, ok = uint16(v), uint16(p), true
		case "HID_PHYS":
			if m := physInterface.FindStringSubmatch(val); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					iface = n
				}
			}
		}
	}
	return vid, pid, iface, ok
}

// nodesAccess is Denied when any node is refused, Granted when every node is
// open to this user, and Unknown otherwise (no node, or an unexpected error).
func nodesAccess(nodes []Node) Access {
	if len(nodes) == 0 {
		return AccessUnknown
	}
	all := AccessGranted
	for _, n := range nodes {
		switch n.Access {
		case AccessDenied:
			return AccessDenied
		case AccessGranted:
		default:
			all = AccessUnknown
		}
	}
	return all
}
