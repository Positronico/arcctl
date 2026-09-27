package backup_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/vectors"
)

var created = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func em11(t testing.TB) *catalog.Model {
	t.Helper()
	m, ok := catalog.ByKey("7B04")
	if !ok {
		t.Fatal("no EM11 Pro in the catalog")
	}
	return m
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func dumpBytes(t testing.TB) []byte {
	t.Helper()
	root, err := vectors.ModuleRoot(".")
	must(t, err)
	b, err := os.ReadFile(filepath.Join(root, "testdata", "flash-dump.bin"))
	must(t, err)
	return b
}

func dumpImage(t testing.TB) *flash.Image {
	t.Helper()
	im, err := flash.FromDump(0, dumpBytes(t))
	must(t, err)
	return im
}

var (
	cmdC      = keys.Combo{keys.LMeta.Stroke(), {Kind: keys.KindKey, Value: 0x06}}
	cmdShiftT = keys.Combo{keys.LMeta.Stroke(), keys.LShift.Stroke(), {Kind: keys.KindKey, Value: 0x17}}
	playPause = keys.Combo{{Kind: keys.KindConsumer, Value: 0xCD}}
	macroAB   = mouse.Macro{Name: "ab", Events: []mouse.Event{
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 50},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 10},
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x05}, Delay: 50},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x05}, Delay: 10},
	}}
)

// richImage is the dump with bodies in the slots it binds (2 to 5), a media
// key on slot 5, and slot 8 bound to the macro in slot 3.
func richImage(t testing.TB) *flash.Image {
	t.Helper()
	im := dumpImage(t)
	set := func(e flash.Extent, ok bool, b []byte, err error) {
		t.Helper()
		if !ok {
			t.Fatal("no such slot")
		}
		must(t, err)
		must(t, im.Set(e.Addr, b))
	}
	body := func(c keys.Combo) ([]byte, error) { return mouse.EncodeShortcut(c) }
	for slot, c := range map[int]keys.Combo{2: cmdC, 3: cmdShiftT, 4: cmdC, 5: playPause} {
		e, ok := mouse.ShortcutExtent(slot)
		b, err := body(c)
		set(e, ok, b, err)
	}
	e, ok := mouse.MacroExtent(3)
	mb, err := mouse.EncodeMacro(macroAB)
	set(e, ok, mb, err)
	fn, err := mouse.MacroBinding(3, 1)
	must(t, err)
	e, ok = mouse.KeyFnExtent(8)
	r, err := mouse.EncodeKeyFn(fn)
	set(e, ok, r, err)
	return im
}

func capture(t testing.TB, im *flash.Image) session.Capture {
	t.Helper()
	return session.Capture{
		Device:   plan.Identity{CID: 0x7B, MID: 4, Addr: [3]byte{0x11, 0x22, 0x33}, VID: 0x260D, PID: 0x1282},
		Model:    em11(t),
		Profile:  session.Probe{Asked: true, Supported: true, Value: 0},
		Versions: session.Versions{Receiver: "v1.02", Mouse: "v1.05"},
		Image:    im,
		Started:  created.Add(-2 * time.Second),
		Finished: created.Add(-time.Second),
	}
}
