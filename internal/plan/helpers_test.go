package plan_test

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/vectors"
)

const (
	bindBase     = 96
	shortcutBase = 256
	shortcutSize = 32
	macroBase    = 768
	macroSize    = 384
	slots        = 16
	knownEnd     = macroBase + slots*macroSize
)

func em11Layout() plan.Layout {
	return plan.Layout{
		Bindings:  plan.Table{Base: bindBase, Stride: 4, Count: slots},
		Shortcuts: plan.Table{Base: shortcutBase, Stride: shortcutSize, Count: slots},
		Macros:    plan.Table{Base: macroBase, Stride: macroSize, Count: slots},
		Buttons:   []int{0, 1, 2, 3, 4, 5},
		Frozen:    []flash.Extent{{Addr: 8, Len: 2}},
		Records:   em11Records(),
	}
}

func em11Records() []flash.Extent {
	var out []flash.Extent
	for _, a := range []int{0, 2, 4, 10, 76, 78, 80, 82, 169, 171, 173, 175, 177, 179, 181, 183, 185} {
		out = append(out, flash.Extent{Addr: a, Len: 2})
	}
	for i := range 16 {
		out = append(out, flash.Extent{Addr: 12 + 4*i, Len: 4})
	}
	return append(out, flash.Extent{Addr: 160, Len: 7})
}

var em11 = plan.Identity{CID: 0x7B, MID: 4, VID: 0x260D, PID: 0x1282}

func bindAddr(k int) int     { return bindBase + 4*k }
func shortcutAddr(k int) int { return shortcutBase + shortcutSize*k }
func macroAddr(k int) int    { return macroBase + macroSize*k }

func h(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

func fill(v byte, n int) []byte { return bytes.Repeat([]byte{v}, n) }

func cks(body ...byte) []byte {
	var s byte
	for _, x := range body {
		s += x
	}
	return append(body, 0x55-s)
}

var (
	disable   = h("00 00 00 55")
	leftClick = h("01 01 00 53")
	right     = h("01 02 00 52")
	shortcut  = h("05 00 00 50")
)

func macroBinding(slot, cycles byte) []byte { return cks(6, slot, cycles) }

func loadVector(t testing.TB, name string) []byte {
	t.Helper()
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Vectors {
		if v.Name == name {
			b, err := v.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("vector %q not found", name)
	return nil
}

// dumpImage is the user's settings page plus the shortcut bodies its bindings 2-5 use;
// every other body slot is erased.
func dumpImage(t testing.TB) *flash.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "flash-dump.bin"))
	if err != nil {
		t.Fatal(err)
	}
	im, err := flash.FromDump(0, b)
	if err != nil {
		t.Fatal(err)
	}
	set(t, im, shortcutBase, fill(0xFF, knownEnd-shortcutBase))
	for k, name := range map[int]string{2: "Cmd+V shortcut", 3: "Ctrl+Tab shortcut", 4: "Cmd+Tab shortcut", 5: "Cmd+C shortcut"} {
		set(t, im, shortcutAddr(k), loadVector(t, name))
	}
	return im
}

func set(t testing.TB, im *flash.Image, addr int, b []byte) {
	t.Helper()
	if err := im.Set(addr, b); err != nil {
		t.Fatal(err)
	}
}

func get(t testing.TB, im *flash.Image, addr, n int) []byte {
	t.Helper()
	b, ok := im.Get(flash.Extent{Addr: addr, Len: n})
	if !ok {
		t.Fatalf("%d+%d not known", addr, n)
	}
	return b
}

func op(phase plan.Phase, addr int, old, new []byte) plan.Op {
	return plan.Op{Extent: flash.Extent{Addr: addr, Len: len(new)}, Old: old, New: new, Phase: phase, Tier: catalog.Untested}
}

func numbered(ops ...plan.Op) []plan.Op {
	for i := range ops {
		ops[i].Seq = i + 1
	}
	return ops
}

func apply(t testing.TB, im *flash.Image, p plan.Plan) *flash.Image {
	t.Helper()
	out := im.Clone()
	for _, o := range p.Ops {
		set(t, out, o.Extent.Addr, o.New)
	}
	return out
}
