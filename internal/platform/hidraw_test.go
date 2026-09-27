package platform

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"syscall"
	"testing"
	"testing/fstest"
)

func uevent(id, phys string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("DRIVER=hid-generic\nHID_ID=" + id + "\nHID_NAME=USB Receiver\nHID_PHYS=" + phys + "\nHID_UNIQ=\n")}
}

func TestHidrawNodes(t *testing.T) {
	sysfs := fstest.MapFS{
		"hidraw0/device/uevent":  uevent("0003:0000260D:00001282", "usb-0000:00:14.0-2/input0"),
		"hidraw1/device/uevent":  uevent("0003:0000260D:00001282", "usb-0000:00:14.0-2/input1"),
		"hidraw2/device/uevent":  uevent("0003:0000046D:0000C52B", "usb-0000:00:14.0-3/input2"),
		"hidraw10/device/uevent": uevent("0005:00003554:0000F819", "aa:bb:cc:dd:ee:ff"),
		"hidraw3/device/uevent":  {Data: []byte("HID_NAME=broken\n")},
		"hidraw4/other":          {},
		"README":                 {},
	}
	denied := map[string]error{
		"/dev/hidraw1":  &fs.PathError{Op: "access", Path: "/dev/hidraw1", Err: syscall.EACCES},
		"/dev/hidraw10": &fs.PathError{Op: "access", Path: "/dev/hidraw10", Err: syscall.ENOENT},
	}
	nodes, err := hidrawNodes(sysfs, "/dev", func(p string) error { return denied[p] })
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		path     string
		vid, pid uint16
		iface    int
		access   Access
	}
	var got []row
	for _, n := range nodes {
		got = append(got, row{n.Path, n.VID, n.PID, n.Interface, n.Access})
		if (n.Err != nil) != (n.Access != AccessGranted) {
			t.Errorf("%s: Err %v with access %v", n.Path, n.Err, n.Access)
		}
	}
	want := []row{
		{"/dev/hidraw0", 0x260D, 0x1282, 0, AccessGranted},
		{"/dev/hidraw1", 0x260D, 0x1282, 1, AccessDenied},
		{"/dev/hidraw10", 0x3554, 0xF819, -1, AccessUnknown},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("hidrawNodes =\n%v\nwant\n%v", got, want)
	}
}

func TestHidrawNodesMissing(t *testing.T) {
	nodes, err := hidrawNodes(os.DirFS(t.TempDir()+"/absent"), "/dev", func(string) error { return nil })
	if err != nil || nodes != nil {
		t.Fatalf("hidrawNodes = %v, %v; want nothing", nodes, err)
	}
	_, err = hidrawNodes(errFS{}, "/dev", func(string) error { return nil })
	if err == nil {
		t.Fatal("a sysfs read error was ignored")
	}
}

type errFS struct{}

func (errFS) Open(string) (fs.File, error) { return nil, errors.New("io error") }

func TestParseUevent(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		vid, pid uint16
		iface    int
		ok       bool
	}{
		{"usb interface 1", "HID_ID=0003:0000260D:00001282\nHID_PHYS=usb-0000:00:14.0-2/input1\n", 0x260D, 0x1282, 1, true},
		{"phys first", "HID_PHYS=usb-1-1/input12\nHID_ID=0003:0000062A:00001213", 0x062A, 0x1213, 12, true},
		{"bluetooth", "HID_ID=0005:00003554:0000F819\nHID_PHYS=aa:bb:cc:dd:ee:ff\n", 0x3554, 0xF819, -1, true},
		{"no phys", "HID_ID=0003:0000260D:00001282\n", 0x260D, 0x1282, -1, true},
		{"vendor too wide", "HID_ID=0003:0001260D:00001282\n", 0, 0, -1, false},
		{"bad hex", "HID_ID=0003:0000XYZW:00001282\n", 0, 0, -1, false},
		{"short id", "HID_ID=0003:0000260D\n", 0, 0, -1, false},
		{"empty", "", 0, 0, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vid, pid, iface, ok := parseUevent([]byte(tt.in))
			if vid != tt.vid || pid != tt.pid || iface != tt.iface || ok != tt.ok {
				t.Fatalf("parseUevent = %04x, %04x, %d, %v; want %04x, %04x, %d, %v", vid, pid, iface, ok, tt.vid, tt.pid, tt.iface, tt.ok)
			}
		})
	}
}

func TestNodesAccess(t *testing.T) {
	g, d, u := Node{Access: AccessGranted}, Node{Access: AccessDenied}, Node{Access: AccessUnknown}
	tests := []struct {
		nodes []Node
		want  Access
	}{
		{nil, AccessUnknown},
		{[]Node{g, g}, AccessGranted},
		{[]Node{g, d}, AccessDenied},
		{[]Node{u, d}, AccessDenied},
		{[]Node{g, u}, AccessUnknown},
	}
	for _, tt := range tests {
		if got := nodesAccess(tt.nodes); got != tt.want {
			t.Errorf("nodesAccess(%v) = %v, want %v", tt.nodes, got, tt.want)
		}
	}
}
