package platform

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ebitengine/purego"
)

// These tests call the read-only macOS APIs. None of them opens a device.

func TestCheckPermissionHost(t *testing.T) {
	p, err := CheckPermission()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains([]Access{AccessGranted, AccessDenied, AccessUnknown}, p.Access) {
		t.Fatalf("Access = %v", p.Access)
	}
	t.Logf("input monitoring %v; app %q via %v (%v, bundle %q, tmux %v)", p.Access, p.App.Name, p.App.Via, p.App.Proc, p.App.Bundle, p.App.Tmux)
	if p.App.Via == ViaResponsibility || p.App.Via == ViaSelf {
		if p.App.Proc.PID <= 0 || p.App.Proc.Path == "" {
			t.Fatalf("responsible process %+v", p.App.Proc)
		}
	}
}

func TestParentHost(t *testing.T) {
	if got := (darwinProcs{}).parent(os.Getpid()); got != os.Getppid() {
		t.Fatalf("parent = %d, want %d", got, os.Getppid())
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatal(err)
	}
	if got := (darwinProcs{}).path(os.Getpid()); got != exe {
		t.Fatalf("path = %q, want %q", got, exe)
	}
	if got := (darwinProcs{}).path(-1); got != "" {
		t.Fatalf("path(-1) = %q", got)
	}
}

func TestConsoleStateHost(t *testing.T) {
	c, err := ConsoleState()
	if errors.Is(err, ErrNoSession) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("screen locked %v; secure input %v %q", c.ScreenLocked, c.SecureInput, c.SecureInput.Path)

	cg, okCG := sessionFromCG()
	reg, okReg := sessionFromRegistry(os.Getuid())
	if okCG && okReg && parseConsole(cg) != parseConsole(reg) {
		t.Fatalf("CoreGraphics says %+v, IOConsoleUsers says %+v", parseConsole(cg), parseConsole(reg))
	}
	t.Logf("sources: CoreGraphics %v, IOConsoleUsers %v", okCG, okReg)
}

func TestHIDClientsHost(t *testing.T) {
	devs, err := HIDClients()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		if d.Path == "" || d.RegistryID == 0 {
			t.Errorf("incomplete %+v", d)
		}
		for _, c := range d.Clients {
			if c.PID <= 0 || c.Name == "" {
				t.Errorf("incomplete client %+v", c)
			}
			if c.Self {
				t.Errorf("this test opened nothing, yet %+v is marked self", c)
			}
		}
		t.Logf("%04x:%04x interface %d registry %d: %d clients %v", d.VID, d.PID, d.Interface, d.RegistryID, len(d.Clients), d.Clients)
	}
}

func TestGoValue(t *testing.T) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	cf, err := purego.Dlopen(cfPath, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	var createData func(alloc uintptr, b []byte, n int) uintptr
	var createPlist func(alloc uintptr, data uintptr, options uint, format *int, errOut *uintptr) uintptr
	purego.RegisterLibFunc(&createData, cf, "CFDataCreate")
	purego.RegisterLibFunc(&createPlist, cf, "CFPropertyListCreateWithData")

	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>IOUserClientCreator</key><string>pid 409, karabiner_observ</string>
<key>DebugState</key><dict><key>ClientSeized</key><false/><key>ClientOptions</key><integer>0</integer>
<key>EventQueueMap</key><array><dict><key>QueueSize</key><integer>16384</integer></dict></array></dict>
<key>kCGSSessionSecureInputPID</key><integer>200</integer>
<key>CGSSessionScreenIsLocked</key><true/>
<key>Name</key><string>Caf&#xe9;</string>
<key>Empty</key><string></string>
<key>Big</key><integer>-5000000000</integer>
<key>Blob</key><data>AAEC</data>
</dict></plist>`
	data := createData(0, []byte(plist), len(plist))
	if data == 0 {
		t.Fatal("CFDataCreate failed")
	}
	defer cfRelease(data)
	var errRef uintptr
	ref := createPlist(0, data, 0, nil, &errRef)
	if ref == 0 {
		t.Fatal("CFPropertyListCreateWithData failed")
	}
	defer cfRelease(ref)

	got := goValue(ref, 0)
	want := map[string]any{
		"IOUserClientCreator": "pid 409, karabiner_observ",
		"DebugState": map[string]any{
			"ClientSeized":  false,
			"ClientOptions": int64(0),
			"EventQueueMap": []any{map[string]any{"QueueSize": int64(16384)}},
		},
		"kCGSSessionSecureInputPID": int64(200),
		"CGSSessionScreenIsLocked":  true,
		"Name":                      "Café",
		"Empty":                     "",
		"Big":                       int64(-5000000000),
		"Blob":                      nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("goValue =\n%#v\nwant\n%#v", got, want)
	}
	d := got.(map[string]any)
	if c := parseConsole(d); !c.ScreenLocked || c.SecureInput.PID != 200 {
		t.Fatalf("parseConsole = %+v", c)
	}
	if c, ok := parseClient(d["IOUserClientCreator"], d["DebugState"]); !ok || c.PID != 409 || c.Seized {
		t.Fatalf("parseClient = %+v, %v", c, ok)
	}
}

func TestDiagnoseHost(t *testing.T) {
	for _, code := range []uint32{0xE00002E2, 0xE00002C1, 0xE00002C5} {
		d := Diagnose(iokitErr(code))
		if d.Code != code || d.Summary == "" {
			t.Fatalf("Diagnose(%#x) = %+v", code, d)
		}
		t.Logf("%#x %v: %s | %s | holder %v", code, d.Class, d.Summary, d.Hint, d.Holder)
	}
}
