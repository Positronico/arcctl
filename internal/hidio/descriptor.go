package hidio

import (
	"errors"
	"fmt"

	"github.com/positronico/arcctl/internal/wire"
)

// ErrNotVendor marks an interface whose report descriptor does not declare the
// vendor channel. Such an interface never gets a packet.
var ErrNotVendor = errors.New("hidio: not the vendor channel")

// vendorChannel checks that a report descriptor declares what arcctl writes
// to: output report 8 of 16 bytes inside an application collection on usage
// page 0xFF02. Other devices that happen to share a VID and PID with the
// catalog (0x062A is a shared vendor ID) fail it. Windows backends may not
// expose the descriptor; there Enumerate's usage-page filter stands in for it.
func vendorChannel(desc []byte, goos string) error {
	if len(desc) == 0 {
		if goos == "windows" {
			return nil
		}
		return fmt.Errorf("%w: no report descriptor", ErrNotVendor)
	}
	type globals struct{ page, size, count, id uint32 }
	var (
		g     globals
		stack []globals
		depth int
		app   uint32
		bits  = map[uint32]uint32{}
		pages = map[uint32][]uint32{}
	)
	for i := 0; i < len(desc); {
		b := desc[i]
		if b == 0xFE {
			if i+1 >= len(desc) {
				return fmt.Errorf("%w: truncated report descriptor", ErrNotVendor)
			}
			i += 3 + int(desc[i+1])
			continue
		}
		size := int(b & 3)
		if size == 3 {
			size = 4
		}
		if i+1+size > len(desc) {
			return fmt.Errorf("%w: truncated report descriptor", ErrNotVendor)
		}
		var v uint32
		for k := size; k > 0; k-- {
			v = v<<8 | uint32(desc[i+k])
		}
		switch b & 0xFC {
		case 0x04:
			g.page = v
		case 0x74:
			g.size = v
		case 0x84:
			g.id = v
		case 0x94:
			g.count = v
		case 0xA4:
			stack = append(stack, g)
		case 0xB4:
			if n := len(stack); n > 0 {
				g, stack = stack[n-1], stack[:n-1]
			}
		case 0xA0:
			if depth == 0 {
				app = g.page
			}
			depth++
		case 0xC0:
			depth = max(depth-1, 0)
		case 0x90:
			bits[g.id] += g.size * g.count
			pages[g.id] = append(pages[g.id], app)
		}
		i += 1 + size
	}
	if n := bits[wire.ReportID]; n != 8*wire.Size {
		return fmt.Errorf("%w: output report %d has %d bits, want %d", ErrNotVendor, wire.ReportID, n, 8*wire.Size)
	}
	for _, p := range pages[wire.ReportID] {
		if p != vendorUsagePage {
			return fmt.Errorf("%w: output report %d is on usage page %#04x", ErrNotVendor, wire.ReportID, p)
		}
	}
	return nil
}
