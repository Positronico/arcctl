package safety_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/vectors"
)

var identity = plan.Identity{CID: 0x7B, MID: 4, VID: 0x260D, PID: 0x1282}

type setup struct {
	seed     func(t testing.TB, im *flash.Image)
	behavior emu.Behavior
	watchdog time.Duration
	noSync   bool // the journal skips fsync; for tests of the executor, not the disk
}

// fixture is an EM11 Pro on an emulated receiver, a journal in a temp dir
// and an executor over a link to the device.
type fixture struct {
	t       testing.TB
	bus     *emu.Bus
	dev     *emu.Device
	model   *catalog.Model
	root    string
	j       *safety.Journal
	link    *link
	x       *safety.Executor
	profile byte
	start   *flash.Image // the device before the test wrote anything
	noSync  bool
}

func newFixture(t testing.TB, s setup) *fixture {
	t.Helper()
	m, ok := catalog.ByKey("7B04")
	if !ok {
		t.Fatal("no EM11 Pro in the catalog")
	}
	im := dumpImage(t)
	if s.seed != nil {
		s.seed(t, im)
	}
	b := emu.New(emu.Options{Watchdog: s.watchdog})
	t.Cleanup(b.Close)
	d, err := b.Add(emu.Config{
		RxVersion: &emu.Version{Major: 1, Minor: 2},
		Behavior:  s.behavior,
		Mouse: &emu.Mouse{
			Model: m, Image: im, Firmware: emu.Version{Major: 1, Minor: 5},
			Battery: emu.Battery{Level: 80, MilliVolts: 3900}, Profile: ptr(byte(0)), LongRange: ptr(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, bus: b, dev: d, model: m, root: t.TempDir(), noSync: s.noSync}
	f.start = f.image()
	f.restart()
	return f
}

// restart is what a new arcctl process does: a fresh journal file, link and
// executor. The old journal is left as it was.
func (f *fixture) restart() {
	f.t.Helper()
	j, err := safety.OpenJournal(f.root, identity)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { j.Close() })
	if f.noSync {
		safety.SkipSync(j)
	}
	f.j = j
	f.reconnect()
}

// reconnect opens the device again, as the session does after a stall or an
// unplug.
func (f *fixture) reconnect() {
	f.t.Helper()
	if f.link != nil {
		f.link.tr.Close()
	}
	f.link = openLink(f.t, f.bus, f.dev)
	f.x = safety.NewExecutor(f.link, f.j, fastOptions())
}

func fastOptions() safety.Options {
	return safety.Options{OfflineWait: 2 * time.Second, LockWait: 2 * time.Second, Poll: 2 * time.Millisecond, Resends: 2, Restarts: 3}
}

// image is the device's flash; bytes it never held read 0xFF, as a read of
// them would.
func (f *fixture) image() *flash.Image {
	f.t.Helper()
	im, err := flash.FromDump(0, f.dev.Image().Bytes())
	if err != nil {
		f.t.Fatal(err)
	}
	return im
}

func (f *fixture) device() safety.Device {
	return safety.Device{Identity: identity, Profile: ptr(f.profile), Image: f.image(), Layout: mouse.Layout(f.model)}
}

func (f *fixture) plan(changes []plan.Change) plan.Plan {
	f.t.Helper()
	p, err := plan.New(identity, ptr(f.profile), f.image(), mouse.Layout(f.model), changes)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(p.Ops) == 0 {
		f.t.Fatal("the plan writes nothing")
	}
	return p
}

func (f *fixture) apply(p plan.Plan, on func(safety.OpEvent)) (safety.Result, error) {
	return f.x.Apply(context.Background(), p, f.device(), on)
}

func (f *fixture) status() *safety.Status {
	f.t.Helper()
	st, err := f.j.Status()
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// after is the image p leaves when every op is written.
func after(t testing.TB, im *flash.Image, p plan.Plan) *flash.Image {
	t.Helper()
	out := im.Clone()
	for _, op := range p.Ops {
		if err := out.Set(op.Extent.Addr, op.New); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// same reports the extents of p where got and want differ.
func differ(got, want *flash.Image, p plan.Plan) []flash.Extent {
	var out []flash.Extent
	for _, op := range p.Ops {
		a, _ := got.Get(op.Extent)
		b, _ := want.Get(op.Extent)
		if !bytes.Equal(a, b) && !slices.Contains(out, op.Extent) {
			out = append(out, op.Extent)
		}
	}
	return out
}

// checkOutside fails when a byte outside p's extents differs from the start
// image, or a cmd 7 reached the device outside them.
func (f *fixture) checkOutside(p plan.Plan, skip ...flash.Extent) {
	f.t.Helper()
	exts := slices.Concat(skip, extents(p))
	now := f.image()
	a, b := now.Bytes(), f.start.Bytes()
	for i := range a {
		if a[i] != b[i] && !slices.ContainsFunc(exts, func(e flash.Extent) bool { return e.Contains(flash.Extent{Addr: i, Len: 1}) }) {
			f.t.Fatalf("byte %d changed from %02x to %02x outside the plan", i, b[i], a[i])
		}
	}
	for _, w := range f.dev.Writes() {
		if w.Packet.Cmd() != 7 {
			continue
		}
		e := flash.Extent{Addr: int(w.Packet.Addr()), Len: w.Packet.Len()}
		if !slices.ContainsFunc(exts, func(x flash.Extent) bool { return x.Contains(e) }) {
			f.t.Fatalf("cmd 7 to %s is outside the plan", e)
		}
	}
}

func extents(p plan.Plan) []flash.Extent {
	var out []flash.Extent
	for _, op := range p.Ops {
		out = append(out, op.Extent)
	}
	return out
}

// checkBindings fails when a binding points at a body that does not decode.
func checkBindings(t testing.TB, im *flash.Image) {
	t.Helper()
	for k := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(k)
		b, _ := im.Get(e)
		fn, err := mouse.DecodeKeyFn(b)
		if err != nil {
			t.Fatalf("binding %d: % x: %v", k, b, err)
		}
		body := func(table func(int) (flash.Extent, bool), slot int, decode func([]byte) error) {
			be, _ := table(slot)
			bb, _ := im.Get(be)
			if err := decode(bb); err != nil {
				t.Fatalf("binding %d (% x) points at slot %d, which does not decode: %v", k, b, slot, err)
			}
		}
		switch fn.Type {
		case mouse.TypeShortcut:
			body(mouse.ShortcutExtent, k, func(b []byte) error { _, err := mouse.DecodeShortcut(b); return err })
		case mouse.TypeMacro:
			for _, s := range []int{k, int(fn.Param >> 8)} {
				body(mouse.MacroExtent, s, func(b []byte) error { _, err := mouse.DecodeMacro(b); return err })
			}
		}
	}
}

func dumpImage(t testing.TB) *flash.Image {
	t.Helper()
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "testdata", "flash-dump.bin"))
	if err != nil {
		t.Fatal(err)
	}
	im, err := flash.FromDump(0, b)
	if err != nil {
		t.Fatal(err)
	}
	if im, err = emu.WithBodies(im); err != nil {
		t.Fatal(err)
	}
	return im
}

func ptr[T any](v T) *T { return &v }

func must[T any](t testing.TB) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func extentOf(t testing.TB, table func(int) (flash.Extent, bool), i int) flash.Extent {
	t.Helper()
	e, ok := table(i)
	if !ok {
		t.Fatalf("no slot %d", i)
	}
	return e
}

func key(v uint16) keys.Stroke { return keys.Stroke{Kind: keys.KindKey, Value: v} }

// macro builds a macro of n events: key presses and releases, 10 ms apart.
func macro(name string, n int) mouse.Macro {
	m := mouse.Macro{Name: name}
	for i := range n {
		m.Events = append(m.Events, mouse.Event{Press: i%2 == 0, Stroke: key(uint16(0x04 + i/2%26)), Delay: 10})
	}
	return m
}

// Changes of each op kind. Slot 4 holds Cmd+Tab in the dump, slot 3 Ctrl+Tab.
func pairChange(t testing.TB) []plan.Change {
	p := must[flash.Pair](t)(mouse.EncodeCurrentStage(1))
	return []plan.Change{{Addr: mouse.AddrCurrentDPI, New: p[:], Tier: catalog.Untested, Desc: "current stage 2"}}
}

func dpiChange(t testing.TB, m *catalog.Model) []plan.Change {
	r := must[flash.Record](t)(mouse.EncodeDPI(m.Sensor, 900))
	return []plan.Change{{Addr: extentOf(t, mouse.DPIExtent, 0).Addr, New: r, Tier: catalog.Untested, Desc: "DPI stage 1: 900"}}
}

func shortcutChange(t testing.TB) []plan.Change {
	body := must[flash.Record](t)(mouse.EncodeShortcut(keys.Combo{
		keys.LCtrl.Stroke(), keys.LShift.Stroke(), keys.LAlt.Stroke(), keys.LMeta.Stroke(), key(0x2B)}))
	return []plan.Change{{Addr: extentOf(t, mouse.ShortcutExtent, 4).Addr, New: body, Tier: catalog.Untested, Desc: "shortcut 4"}}
}

func macroChanges(t testing.TB, slot int, name string, events int) []plan.Change {
	body := must[[]byte](t)(mouse.EncodeMacro(macro(name, events)))
	fn := must[mouse.KeyFn](t)(mouse.MacroBinding(slot, 1))
	bind := must[flash.Record](t)(mouse.EncodeKeyFn(fn))
	return []plan.Change{
		{Addr: extentOf(t, mouse.MacroExtent, slot).Addr, New: body, Tier: catalog.Untested, Desc: "macro " + name},
		{Addr: extentOf(t, mouse.KeyFnExtent, slot).Addr, New: bind, Tier: catalog.Untested, Desc: "bind macro " + name},
	}
}

// boundMacro seeds slot 5 with a short macro bound to it, so rewriting that
// macro takes the two-phase path.
func boundMacro(t testing.TB, im *flash.Image) {
	for _, c := range macroChanges(t, 5, "old", 4) {
		if err := im.Set(c.Addr, c.New); err != nil {
			t.Fatal(err)
		}
	}
}

func chunksOf(p plan.Plan) int {
	n := 0
	for _, op := range p.Ops {
		n += (op.Extent.Len + 9) / 10
	}
	return n
}

// opAt is the index of the op whose chunks include the k-th cmd 7 (from 1).
func opAt(p plan.Plan, k int) int {
	for i, op := range p.Ops {
		k -= (op.Extent.Len + 9) / 10
		if k <= 0 {
			return i
		}
	}
	return len(p.Ops)
}

func stopError(t testing.TB, err error) *safety.StopError {
	t.Helper()
	var st *safety.StopError
	if !errors.As(err, &st) {
		t.Fatalf("err = %v, want a *safety.StopError", err)
	}
	return st
}
