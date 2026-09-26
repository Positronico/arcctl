package keys

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestConsumerTable(t *testing.T) {
	for i, c := range consumers {
		if i > 0 && consumers[i-1].Usage >= c.Usage {
			t.Errorf("%#04x: not sorted after %#04x", c.Usage, consumers[i-1].Usage)
		}
		for _, s := range []string{c.Name, c.Label} {
			if s == "" || s != strings.TrimSpace(s) || utf8.RuneCountInString(s) > 40 || strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
				t.Errorf("%#04x: bad text %q", c.Usage, s)
			}
		}
		if got, ok := ConsumerByUsage(c.Usage); !ok || got != c {
			t.Errorf("ConsumerByUsage(%#04x) = %+v, %v", c.Usage, got, ok)
		}
	}
	for _, u := range []uint16{0, 0x0001, 0x00CE, 0x0222, 0x0300, 0xFFFF} {
		if c, ok := ConsumerByUsage(u); ok {
			t.Errorf("ConsumerByUsage(%#04x) = %+v", u, c)
		}
	}
}
