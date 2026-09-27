package plan_test

import (
	"testing"

	"github.com/positronico/arcctl/internal/flash"
)

// Unread names what an image lacks before a binding can be checked: the
// count of each body it points at, then the whole record that count
// declares. A body that is invalid whatever its unread bytes needs nothing.
func TestUnread(t *testing.T) {
	l := em11Layout()
	sc := h("04 80 08 00 81 06 00 41 06 00 40 08 00 b3")
	macro := h("02 61 62" + " ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff" + " 02 81 04 00 00 32 41 04 00 00 0a 4d")
	set := func(im *flash.Image, addr int, b []byte) {
		t.Helper()
		if err := im.Set(addr, b); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		slot  int
		b     []byte
		known func(im *flash.Image)
		want  flash.Extent
		ok    bool
	}{
		{"not a body", 3, h("0b 01 00 49"), nil, flash.Extent{}, false},
		{"shortcut count", 3, h("05 00 00 50"), nil, flash.Extent{Addr: shortcutAddr(3), Len: 1}, true},
		{"shortcut record", 3, h("05 00 00 50"), func(im *flash.Image) { set(im, shortcutAddr(3), sc[:1]) },
			flash.Extent{Addr: shortcutAddr(3), Len: len(sc)}, true},
		{"shortcut known", 3, h("05 00 00 50"), func(im *flash.Image) { set(im, shortcutAddr(3), sc) }, flash.Extent{}, false},
		{"shortcut empty", 3, h("05 00 00 50"), func(im *flash.Image) { set(im, shortcutAddr(3), []byte{0}) }, flash.Extent{}, false},
		{"shortcut too long", 3, h("05 00 00 50"), func(im *flash.Image) { set(im, shortcutAddr(3), []byte{0xff}) }, flash.Extent{}, false},
		{"macro header", 5, h("06 05 01 4a"), nil, flash.Extent{Addr: macroAddr(5), Len: 32}, true},
		{"macro record", 5, h("06 05 01 4a"), func(im *flash.Image) { set(im, macroAddr(5), macro[:32]) },
			flash.Extent{Addr: macroAddr(5), Len: len(macro)}, true},
		{"other slot's macro", 5, h("06 07 01 48"), func(im *flash.Image) { set(im, macroAddr(5), macro) },
			flash.Extent{Addr: macroAddr(7), Len: 32}, true},
		{"slot out of range", 16, h("05 00 00 50"), nil, flash.Extent{}, false},
		{"short binding", 3, h("05 00 00"), nil, flash.Extent{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := flash.New()
			if tt.known != nil {
				tt.known(im)
			}
			got, ok := l.Unread(im, tt.slot, tt.b)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("Unread = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
