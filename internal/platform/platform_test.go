package platform

import (
	"slices"
	"testing"
)

func TestForeign(t *testing.T) {
	const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	const helper = "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/1/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper"
	clients := []Client{
		{Process: Process{PID: 1, Name: "arcctl"}, Self: true},
		{Process: Process{PID: 2, Name: "karabiner_observer", Path: "/Library/Application Support/org.pqrs/Karabiner-Elements/bin/karabiner_observer"}},
		{Process: Process{PID: 3, Name: "karabiner_observ"}},
		{Process: Process{PID: 4, Name: "Google Chrome", Path: chrome}},
		{Process: Process{PID: 5, Name: "Google Chrome Helper", Path: helper}},
		{Process: Process{PID: 6, Name: "hidtool"}},
		{Process: Process{PID: 7, Name: "karabiner_grabber"}},
		{Process: Process{PID: 8, Name: "Google Chrome He"}},
		{Process: Process{PID: 9, Name: "arcctl"}},
		{Process: Process{PID: 10, Name: "karabiner_observer"}, Seized: true},
	}
	tests := []struct {
		name   string
		benign []string
		want   []int
	}{
		{"known benign only", nil, []int{4, 5, 6, 7, 8, 9, 10}},
		{"user marks a tool", []string{"hidtool"}, []int{4, 5, 7, 8, 9, 10}},
		{"browsers are never benign", []string{"Google Chrome", "Google Chrome Helper"}, []int{4, 5, 6, 7, 8, 9, 10}},
		{"another arcctl is foreign unless marked", []string{"arcctl"}, []int{4, 5, 6, 7, 8, 10}},
		{"empty names match nothing", []string{""}, []int{4, 5, 6, 7, 8, 9, 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			for _, c := range Foreign(clients, tt.benign) {
				got = append(got, c.PID)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Foreign = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBrowser(t *testing.T) {
	tests := []struct {
		p    Process
		want bool
	}{
		{Process{Name: "Google Chrome", Path: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}, true},
		{Process{Name: "Google Chrome Helper (Renderer)"}, true},
		{Process{Name: "Google Chrome He"}, true},
		{Process{Name: "Brave Browser Helper"}, true},
		{Process{Name: "Microsoft Edge"}, true},
		{Process{Name: "chrome", Path: "/opt/google/chrome/chrome"}, true},
		{Process{Name: "msedge.exe"}, true},
		{Process{Name: "Arc", Path: "/Applications/Arc.app/Contents/MacOS/Arc"}, true},
		{Process{Name: "Vivaldi"}, true},
		{Process{Name: "arcctl", Path: "/opt/homebrew/bin/arcctl"}, false},
		{Process{Name: "Search", Path: "/Applications/Search.app/Contents/MacOS/Search"}, false},
		{Process{Name: "cooperative"}, false},
		{Process{Name: "karabiner_observer"}, false},
		{Process{}, false},
	}
	for _, tt := range tests {
		if got := isBrowser(tt.p); got != tt.want {
			t.Errorf("isBrowser(%+v) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestBundleOf(t *testing.T) {
	tests := []struct{ path, bundle, name string }{
		{"/Applications/Ghostty.app/Contents/MacOS/ghostty", "/Applications/Ghostty.app", "Ghostty"},
		{"/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", "/Applications/Utilities/Terminal.app", "Terminal"},
		{"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper", "/Applications/Visual Studio Code.app", "Visual Studio Code"},
		{"/opt/homebrew/bin/tmux", "", ""},
		{"/Applications/Ghostty.app", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		if b, n := bundleOf(tt.path); b != tt.bundle || n != tt.name {
			t.Errorf("bundleOf(%q) = %q, %q; want %q, %q", tt.path, b, n, tt.bundle, tt.name)
		}
	}
}

func TestDeviceClientsMatches(t *testing.T) {
	d := DeviceClients{Path: "IOService:/AppleARMPE/arm-io/usb/IOUSBHostInterface@1/AppleUserUSBHostHIDDevice", RegistryID: 4294967301}
	tests := []struct {
		path string
		want bool
	}{
		{d.Path, true},
		{"DevSrvsID:4294967301", true},
		{"DevSrvsID:4294967302", false},
		{"IOService:/AppleARMPE/arm-io/usb/IOUSBHostInterface@0/AppleUserUSBHostHIDDevice", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := d.Matches(tt.path); got != tt.want {
			t.Errorf("Matches(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if (DeviceClients{}).Matches("DevSrvsID:0") {
		t.Error("a zero registry ID matched")
	}
}

func TestProcessString(t *testing.T) {
	tests := []struct {
		p    Process
		want string
	}{
		{Process{}, "none"},
		{Process{PID: 200, Name: "loginwindow"}, "loginwindow (pid 200)"},
		{Process{PID: 7}, "unknown (pid 7)"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}
