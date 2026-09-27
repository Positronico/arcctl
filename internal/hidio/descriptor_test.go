package hidio

import (
	"errors"
	"testing"
)

func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	var out []byte
	for i := 0; i < len(s); {
		if s[i] == ' ' {
			i++
			continue
		}
		var b byte
		for _, c := range s[i : i+2] {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= byte(c - '0')
			case c >= 'A' && c <= 'F':
				b |= byte(c - 'A' + 10)
			default:
				t.Fatalf("bad hex %q", s[i:i+2])
			}
		}
		out = append(out, b)
		i += 2
	}
	return out
}

// Synthetic descriptors: a keyboard (ID 2), consumer keys (ID 5), a mouse
// (ID 7) and, where named, a vendor collection.
const (
	keyboard = "05 01 09 06 A1 01 85 02 05 07 19 E0 29 E7 15 00 25 01 75 01 95 08 81 02 95 06 75 08 26 FF 00 19 00 29 FF 81 00 C0"
	consumer = "05 0C 09 01 A1 01 85 05 15 00 26 FF 03 19 00 2A FF 03 75 10 95 01 81 00 C0"
	mouseCol = "05 01 09 02 A1 01 85 07 09 01 A1 00 05 09 19 01 29 05 15 00 25 01 95 05 75 01 81 02 95 01 75 03 81 01 05 01 09 30 09 31 16 01 80 26 FF 7F 75 10 95 02 81 06 C0 C0"
	vendor   = "06 02 FF 09 01 A1 01 85 08 09 01 15 00 26 FF 00 75 08 95 10 81 02 09 02 75 08 95 10 91 02 C0"
)

func TestVendorChannel(t *testing.T) {
	tests := []struct {
		name string
		desc string
		goos string
		ok   bool
	}{
		{"receiver", keyboard + " " + consumer + " " + mouseCol + " " + vendor, "darwin", true},
		{"vendor alone", vendor, "linux", true},
		{"generic mouse", keyboard + " " + mouseCol, "darwin", false},
		{"report 8 of 64 bytes", "06 02 FF 09 01 A1 01 85 08 75 08 95 40 91 02 C0", "darwin", false},
		{"report 8 split over two items", "06 02 FF 09 01 A1 01 85 08 75 08 95 08 91 02 95 08 91 02 C0", "darwin", true},
		{"report 8 on another page", "06 00 FF 09 01 A1 01 85 08 75 08 95 10 91 02 C0", "darwin", false},
		{"report 8 input only", "06 02 FF 09 01 A1 01 85 08 75 08 95 10 81 02 C0", "darwin", false},
		{"page pushed and popped", "06 02 FF A4 05 01 B4 09 01 A1 01 85 08 75 08 95 10 91 02 C0", "darwin", true},
		{"long item skipped", "FE 02 10 AA BB " + vendor, "darwin", true},
		{"truncated", "06 02 FF 09 01 A1 01 85", "darwin", false},
		{"no descriptor", "", "darwin", false},
		{"no descriptor on windows", "", "windows", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := vendorChannel(hexBytes(t, tt.desc), tt.goos)
			if (err == nil) != tt.ok {
				t.Fatalf("vendorChannel = %v, want ok %v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrNotVendor) {
				t.Fatalf("vendorChannel = %v, want ErrNotVendor", err)
			}
		})
	}
}
