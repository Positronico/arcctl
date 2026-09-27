package session

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

func extents(ops []op) []flash.Extent {
	var out []flash.Extent
	for _, o := range ops {
		out = append(out, o.e)
	}
	return out
}

func TestReadsSplitIntoChunks(t *testing.T) {
	tests := []struct {
		in   []flash.Extent
		want []flash.Extent
	}{
		{nil, nil},
		{[]flash.Extent{{Addr: 0, Len: 256}}, nil},
		{[]flash.Extent{{Addr: 6912, Len: 75}}, nil},
		{[]flash.Extent{{Addr: 4, Len: 2}}, []flash.Extent{{Addr: 4, Len: 2}}},
		{[]flash.Extent{{Addr: 256, Len: 32}}, []flash.Extent{{Addr: 256, Len: 10}, {Addr: 266, Len: 10}, {Addr: 276, Len: 10}, {Addr: 286, Len: 2}}},
	}
	for _, tt := range tests {
		got := extents(reads(tt.in...))
		if tt.want == nil && len(tt.in) > 0 {
			e := tt.in[0]
			if n := (e.Len + 9) / 10; len(got) != n || got[0].Addr != e.Addr || got[n-1].End() != e.End() {
				t.Errorf("reads(%v) = %d chunks ending at %d", e, len(got), got[len(got)-1].End())
			}
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("reads(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
	if n := len(reads(loadSettings, loadExtended)); n != 34 {
		t.Errorf("the settings load is %d reads, want 26 + 8", n)
	}
	if n := len(reads(fullSettings, fullCRC)); n != 725 {
		t.Errorf("a full backup is %d reads, want 725", n)
	}
}

func TestUnknown(t *testing.T) {
	im := flash.New()
	must(t, im.Set(10, make([]byte, 5)))
	must(t, im.Set(20, make([]byte, 5)))
	got := unknown(im, []flash.Extent{{Addr: 0, Len: 30}, {Addr: 12, Len: 2}, {Addr: 22, Len: 5}})
	want := []flash.Extent{{Addr: 0, Len: 10}, {Addr: 15, Len: 5}, {Addr: 25, Len: 5}, {Addr: 25, Len: 2}}
	if !slices.Equal(got, want) {
		t.Errorf("unknown = %v, want %v", got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestBodiesAndMacroEvents(t *testing.T) {
	im := flash.New()
	bind := func(slot int, fn mouse.KeyFn) {
		r, err := mouse.EncodeKeyFn(fn)
		must(t, err)
		e, _ := mouse.KeyFnExtent(slot)
		must(t, im.Set(e.Addr, r))
	}
	bind(0, mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft})
	bind(2, mouse.KeyFn{Type: mouse.TypeShortcut})
	fn, _ := mouse.MacroBinding(9, 1)
	bind(4, fn)
	fn, _ = mouse.MacroBinding(4, 1)
	bind(9, fn)
	sc, _ := mouse.ShortcutExtent(2)
	m4, _ := mouse.MacroExtent(4)
	m9, _ := mouse.MacroExtent(9)
	want := []flash.Extent{sc, {Addr: m4.Addr, Len: 32}, {Addr: m9.Addr, Len: 32}}
	if got := bodies(im); !slices.Equal(got, want) {
		t.Errorf("bodies = %v, want %v", got, want)
	}

	header := func(e flash.Extent, count byte) {
		h := make([]byte, 32)
		h[0], h[31] = 1, count
		must(t, im.Set(e.Addr, h))
	}
	header(m4, 3)
	header(m9, 0xFF)
	m1, _ := mouse.MacroExtent(1)
	header(m1, 70)
	want = []flash.Extent{{Addr: m1.Addr + 32, Len: 351}, {Addr: m4.Addr + 32, Len: 16}}
	if got := macroEvents(im); !slices.Equal(got, want) {
		t.Errorf("macroEvents = %v, want %v", got, want)
	}
	if end := m1.Addr + 32 + 351; end > m1.End() {
		t.Errorf("a 70-event macro ends at %d, past its slot at %d", end, m1.End())
	}
}

func TestRereads(t *testing.T) {
	if got := rereads(0xFF, 0xFF); len(got) != 9 {
		t.Errorf("every flag re-reads %v", got)
	}
	if got := rereads(pushProfile|pushBattery|0x10|0x80, 0xE0); len(got) != 0 {
		t.Errorf("flags without a range re-read %v", got)
	}
}

func TestTimingDefaults(t *testing.T) {
	got := Timing{Try: time.Millisecond, Tries: 2}.withDefaults()
	want := DefaultTiming()
	want.Try, want.Tries = time.Millisecond, 2
	if got != want {
		t.Errorf("withDefaults = %+v, want %+v", got, want)
	}
}

func TestStateNames(t *testing.T) {
	for st := NoReceiver; st <= Unknown; st++ {
		if st.String() == "" || st.String()[0] == 's' && st != Seized && st != Stalled && st != SuspectedConflict {
			t.Errorf("state %d has name %q", st, st)
		}
	}
	if State(99).String() != "state 99" {
		t.Error(State(99).String())
	}
}

func TestBackoff(t *testing.T) {
	var b time.Duration
	var got []time.Duration
	for range 5 {
		later(&b, time.Second, 5*time.Second)
		got = append(got, b)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("backoff %v, want %v", got, want)
	}
}

func TestHandshakeRequest(t *testing.T) {
	p := handshake(wire.Mouse)
	if err := wire.ReadOnly.Check(p, wire.Mouse); err != nil {
		t.Fatal(err)
	}
	if p.Len() != 8 || !slices.Equal(p[9:13], []byte{0, 0, 0, 0}) {
		t.Errorf("handshake %v", p)
	}
}

// FuzzLoadPlan checks that whatever the bindings and macro headers hold, a load
// reads only inside the body slots, in chunks of at most 10 bytes.
func FuzzLoadPlan(f *testing.F) {
	f.Add(make([]byte, 64), make([]byte, 16), make([]byte, 16))
	f.Add([]byte{5, 0, 0, 0x50, 6, 3, 1, 0x4b}, []byte{1, 70, 255}, []byte{2, 1})
	f.Fuzz(func(t *testing.T, bindings, counts, names []byte) {
		im := flash.New()
		must(t, im.Set(96, bindings[:min(len(bindings), 64)]))
		for i := range min(len(counts), mouse.Slots) {
			e, _ := mouse.MacroExtent(i)
			h := make([]byte, macroHeader)
			if i < len(names) {
				h[0] = names[i]
			}
			h[macroHeader-1] = counts[i]
			must(t, im.Set(e.Addr, h))
		}
		shortcuts := flash.Extent{Addr: mouse.AddrShortcutKey, Len: mouse.Slots * mouse.ShortcutSize}
		macros := flash.Extent{Addr: mouse.AddrMacro, Len: mouse.Slots * mouse.MacroSize}
		for _, e := range append(bodies(im), macroEvents(im)...) {
			area, slot := shortcuts, mouse.ShortcutSize
			if macros.Contains(e) {
				area, slot = macros, mouse.MacroSize
			} else if !shortcuts.Contains(e) {
				t.Fatalf("%v is outside the body slots", e)
			}
			base := e.Addr - (e.Addr-area.Addr)%slot
			if e.Len <= 0 || e.End() > base+slot {
				t.Fatalf("%v crosses its slot at %d", e, base)
			}
			total := 0
			for _, o := range reads(e) {
				if o.e.Len < 1 || o.e.Len > wire.MaxData || !e.Contains(o.e) {
					t.Fatalf("chunk %v of %v", o.e, e)
				}
				total += o.e.Len
			}
			if total != e.Len {
				t.Fatalf("chunks of %v cover %d bytes", e, total)
			}
		}
	})
}

// PLAN §4.3 backs the Locked retry off from Retry to RetryMax, also when the
// lock hits the probe of a device that is not attached yet.
func TestBlockedProbeBacksOff(t *testing.T) {
	b := emu.New(emu.Options{})
	defer b.Close()
	d, err := b.Add(emu.Config{})
	if err != nil {
		t.Fatal(err)
	}
	d.SetLocked(true)
	s := New(Options{Devices: b, Timing: Timing{Retry: 20 * time.Millisecond, RetryMax: 160 * time.Millisecond, ProbeTry: time.Millisecond}})
	if err := s.guard.Set(wire.ReadOnly, "test"); err != nil {
		t.Fatal(err)
	}
	var got []time.Duration
	for range 5 {
		s.connect(context.Background())
		if s.base != Locked {
			t.Fatalf("state %v, want locked", s.base)
		}
		got = append(got, s.retryBackoff)
	}
	want := []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond, 160 * time.Millisecond}
	if !slices.Equal(got, want) {
		t.Errorf("retry backoff %v, want %v", got, want)
	}
	if s.prev == Probing {
		t.Error("the state to return to is Probing")
	}
	d.SetLocked(false)
	s.connect(context.Background())
	if s.base != Offline || s.retryBackoff != 0 {
		t.Errorf("after the unlock: state %v, backoff %v", s.base, s.retryBackoff)
	}
	s.shutdown()
}
