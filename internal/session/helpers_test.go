package session_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

const waitFor = 5 * time.Second

func fast() session.Timing {
	return session.Timing{
		Try:           25 * time.Millisecond,
		ProbeTry:      25 * time.Millisecond,
		Window:        150 * time.Millisecond,
		Debounce:      5 * time.Millisecond,
		Rescan:        20 * time.Millisecond,
		RescanMax:     50 * time.Millisecond,
		Retry:         20 * time.Millisecond,
		RetryMax:      50 * time.Millisecond,
		Offline:       40 * time.Millisecond,
		Online:        time.Hour,
		Battery:       time.Hour,
		Suspect:       30 * time.Millisecond,
		ConflictQuiet: 100 * time.Millisecond,
		LoadWatchdog:  10 * time.Second,
	}
}

func newBus(t *testing.T, o emu.Options) *emu.Bus {
	t.Helper()
	b := emu.New(o)
	t.Cleanup(b.Close)
	return b
}

// add plugs a device into b and, when the test ends, checks that nothing but
// read-only commands for the mouse ever reached it.
func add(t *testing.T, b *emu.Bus, c emu.Config) *emu.Device {
	t.Helper()
	d, err := b.Add(c)
	if err != nil {
		t.Fatal(err)
	}
	target := wire.Mouse
	if catalog.Classify(d.Candidates()[0].VID, d.Candidates()[0].PID) == catalog.ClassKeyboard {
		target = wire.Keyboard
	}
	t.Cleanup(func() { checkReadOnly(t, d.Writes(), target) })
	return d
}

var neverSent = []wire.Cmd{
	wire.CmdDriverStatus, wire.CmdPair, wire.CmdPairState, wire.CmdWrite, wire.CmdClear, wire.CmdSetProfile,
	wire.CmdSetRxLED4K, wire.CmdGetRxLED4K, wire.CmdSetLongRange, wire.CmdSetRxLEDBar, wire.CmdGetRxLEDBar,
	wire.CmdSetRxLED3, wire.CmdGetRxLED3,
}

// checkReadOnly fails on any packet the read-only policy refuses. A keyboard
// may also get unflagged packets, after its flagged probe went unanswered.
func checkReadOnly(t *testing.T, ws []emu.Write, target wire.Target) {
	t.Helper()
	for _, w := range ws {
		p := w.Packet
		if p.Target() != wire.Mouse && p.Target() != target {
			t.Errorf("packet %v has the wrong target", p)
		}
		if err := wire.ReadOnly.Check(p, p.Target()); err != nil {
			t.Errorf("packet %v reached the device: %v", p, err)
		}
		if slices.Contains(neverSent, p.Cmd()) {
			t.Errorf("%v reached the device", p.Cmd())
		}
	}
}

// start runs a session on b, or on opt.Devices when b is nil, until the test
// ends.
func start(t *testing.T, b *emu.Bus, opt session.Options) *session.Session {
	t.Helper()
	if b != nil {
		opt.Devices, opt.Clients = b, clientsOf(b)
	}
	if opt.Timing == (session.Timing{}) {
		opt.Timing = fast()
	}
	s := session.New(opt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != context.Canceled {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	})
	return s
}

func clientsOf(b *emu.Bus) func(hidio.Candidate) ([]session.Client, error) {
	return func(c hidio.Candidate) ([]session.Client, error) {
		var out []session.Client
		for _, x := range b.Clients() {
			if x.Path == c.Path && x.PID != os.Getpid() {
				out = append(out, session.Client{PID: x.PID, Name: x.Process, Seized: x.Seized})
			}
		}
		return out, nil
	}
}

func await(t *testing.T, s session.API, what string, pred func(*session.Snapshot) bool) *session.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	sn, err := session.Await(ctx, s, pred)
	if err != nil {
		t.Fatalf("waiting for %s: %v (state %v, link %v, err %v, progress %+v)", what, err, sn.State, sn.Link, sn.Err, sn.Progress)
	}
	return sn
}

func in(st session.State) func(*session.Snapshot) bool {
	return func(sn *session.Snapshot) bool { return sn.State == st }
}

// idle is Ready with no job left.
func idle(sn *session.Snapshot) bool { return sn.State == session.Ready && sn.Progress.Job == "" }

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	t.Cleanup(cancel)
	return ctx
}

func model(t testing.TB, key string) *catalog.Model {
	t.Helper()
	m, ok := catalog.ByKey(key)
	if !ok {
		t.Fatalf("no model %s", key)
	}
	return m
}

// dumpImage is the committed settings page with placeholder bodies in the
// slots it binds.
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

// macroSlot and macroBound are where macroImage puts a macro: slot 8 is bound
// to the macro in slot 3, so the load reads both headers.
const (
	macroSlot  = 3
	macroBound = 8
)

var testMacro = mouse.Macro{Name: "ab", Events: []mouse.Event{
	{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 50},
	{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 10},
	{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x05}, Delay: 50},
	{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x05}, Delay: 10},
}}

func macroImage(t testing.TB) *flash.Image {
	t.Helper()
	im := dumpImage(t)
	body, err := mouse.EncodeMacro(testMacro)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := mouse.MacroExtent(macroSlot)
	must(t, im.Set(e.Addr, body))
	fn, err := mouse.MacroBinding(macroSlot, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := mouse.EncodeKeyFn(fn)
	if err != nil {
		t.Fatal(err)
	}
	e, _ = mouse.KeyFnExtent(macroBound)
	must(t, im.Set(e.Addr, r))
	return im
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

// em11 is an EM11 Pro, awake, with the committed dump and a bound macro.
func em11(t testing.TB) *emu.Mouse {
	return &emu.Mouse{
		Model:     model(t, "7B04"),
		Image:     macroImage(t),
		Firmware:  emu.Version{Major: 1, Minor: 5},
		Battery:   emu.Battery{Level: 80, MilliVolts: 3900},
		Profile:   ptr(byte(0)),
		LongRange: ptr(true),
	}
}

func receiver(m *emu.Mouse) emu.Config {
	return emu.Config{RxVersion: &emu.Version{Major: 1, Minor: 2}, Mouse: m}
}

// reads lists the reads that reached the device, in order.
func reads(ws []emu.Write) []flash.Extent {
	var out []flash.Extent
	for _, w := range ws {
		if w.Packet.Cmd() == wire.CmdRead {
			out = append(out, flash.Extent{Addr: int(w.Packet.Addr()), Len: w.Packet.Len()})
		}
	}
	return out
}

func logical(ws []emu.Write) []flash.Extent {
	return slices.Compact(reads(ws))
}

func count(ws []emu.Write, c wire.Cmd) int {
	n := 0
	for _, w := range ws {
		if w.Packet.Cmd() == c {
			n++
		}
	}
	return n
}

// chunks splits extents the way the session reads them.
func chunks(extents ...flash.Extent) []flash.Extent {
	var out []flash.Extent
	for _, e := range extents {
		for a := e.Addr; a < e.End(); a += 10 {
			out = append(out, flash.Extent{Addr: a, Len: min(10, e.End()-a)})
		}
	}
	return out
}

// workingSet is what a load of macroImage reads, in order.
func workingSet() []flash.Extent {
	var out []flash.Extent
	out = append(out, chunks(flash.Extent{Addr: 0, Len: 256}, flash.Extent{Addr: 6912, Len: 75})...)
	var bodies []flash.Extent
	for slot := 2; slot <= 5; slot++ {
		e, _ := mouse.ShortcutExtent(slot)
		bodies = append(bodies, e)
	}
	for _, slot := range []int{macroSlot, macroBound} {
		e, _ := mouse.MacroExtent(slot)
		bodies = append(bodies, flash.Extent{Addr: e.Addr, Len: 32})
	}
	out = append(out, chunks(bodies...)...)
	e, _ := mouse.MacroExtent(macroSlot)
	return append(out, chunks(flash.Extent{Addr: e.Addr + 32, Len: 1 + 5*len(testMacro.Events)})...)
}

// sameBytes fails unless got knows every extent with the bytes of want.
func sameBytes(t *testing.T, got, want *flash.Image, extents ...flash.Extent) {
	t.Helper()
	for _, e := range extents {
		g, ok := got.Get(e)
		if !ok {
			t.Errorf("%v not loaded", e)
			continue
		}
		w, _ := want.Get(e)
		if !slices.Equal(g, w) {
			t.Errorf("%v = % x, device has % x", e, g, w)
		}
	}
}
