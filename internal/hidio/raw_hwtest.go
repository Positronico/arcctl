//go:build hwtest

package hidio

import (
	"fmt"

	"github.com/positronico/arcctl/internal/catalog"
)

// OpenRaw opens c like Open but hands out its Raw without a guard. It is the
// device end of the hardware tests' raw path (D10), which sends what no policy
// allows, such as a write with a wrong checksum. Only builds with the hwtest
// tag have it.
func OpenRaw(c Candidate) (Raw, error) {
	b, err := lookup(c.Backend)
	if err != nil {
		return nil, err
	}
	if catalog.Classify(c.VID, c.PID) == catalog.ClassUnknown {
		return nil, fmt.Errorf("%w: %04x:%04x is not a device arcctl knows", ErrNotFound, c.VID, c.PID)
	}
	return b.open(c)
}
