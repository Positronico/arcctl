package keys_test

import (
	"testing"

	"github.com/positronico/arcctl/internal/keys"
)

func TestConsumerNames(t *testing.T) {
	cases := []struct {
		usage       uint16
		name, label string
	}{
		{0x00CD, "Play/Pause", "Play/Pause"},
		{0x00E9, "Volume Increment", "Volume Up"},
		{0x0183, "AL Consumer Control Configuration", "Media Player"},
		{0x0221, "AC Search", "Search"},
		{0x0224, "AC Back", "Browser Back"},
		{0x0225, "AC Forward", "Browser Forward"},
	}
	for _, c := range cases {
		got, ok := keys.ConsumerByUsage(c.usage)
		if !ok || got.Usage != c.usage || got.Name != c.name || got.Label != c.label {
			t.Errorf("ConsumerByUsage(%#04x) = %+v, %v; want %q, %q", c.usage, got, ok, c.name, c.label)
		}
	}
}
