//go:build hwtest

package emu

import "github.com/positronico/arcctl/internal/hidio"

// OpenRaw opens c like Open but hands out its Raw without a guard, for the
// hardware tests' raw path. Only builds with the hwtest tag have it.
func (b *Bus) OpenRaw(c hidio.Candidate) (hidio.Raw, error) {
	h, err := b.open(c)
	if err != nil {
		return nil, err
	}
	return h, nil
}
