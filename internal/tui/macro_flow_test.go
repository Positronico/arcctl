package tui

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// macEmulate is the M4 acceptance setup, an emulated EM11 Pro behind a real
// session with the default tabs, on the Macros tab.
func macEmulate(t *testing.T) (*accEmulated, *Macros) {
	t.Helper()
	e := accEmulate(t)
	h := e.h
	var m *Macros
	for _, tab := range h.app.tabs {
		if x, ok := tab.(*Macros); ok {
			m = x
		}
	}
	if m == nil || m.lib == nil || m.lib.Path != filepath.Join(e.dir, "macros.json") {
		t.Fatalf("the default tabs lack the Macros tab on the session's library: %v", m)
	}
	h.flush(func() bool { return m.loaded })
	e.tab(t, m)
	return e, m
}

// tab switches to tab by its number in the bar, which follows the tabs
// shown.
func (e *accEmulated) tab(t *testing.T, tab Tab) {
	t.Helper()
	h := e.h
	c := h.app.context()
	i := slices.Index(h.app.visible(c), slices.Index(h.app.tabs, tab))
	if i < 0 {
		t.Fatalf("%T is not shown", tab)
	}
	h.keys(strconv.Itoa(i + 1))
}

// macSlotRead waits until the image holds the whole of macro slot k.
func macSlotRead(h *harness, k int) {
	ext, _ := mouse.MacroExtent(k)
	h.flush(func() bool { im := h.app.context().Image(); return im != nil && im.Known(ext) })
}

func macPhases(run *safety.Run) []plan.Phase {
	var out []plan.Phase
	for _, op := range run.Ops {
		out = append(out, op.Op.Phase)
	}
	return out
}

// macHolds checks that the emulated mouse holds m in macro slot k and a
// binding of slot k to it with cycle.
func macHolds(t *testing.T, e *accEmulated, k int, m mouse.Macro, cycle int) {
	t.Helper()
	im := e.dev.Image()
	cfg := mouse.Decode(em11(), im)
	if got := cfg.Macros[k]; cfg.MacroClass[k] != flash.SlotValid || got.Name != m.Name || !slices.Equal(got.Events, m.Events) {
		t.Errorf("macro slot %d holds %+v (%s), want %+v", k, got, cfg.MacroClass[k], m)
	}
	if want, _ := mouse.MacroBinding(k, cycle); cfg.Keys[k].Fn != want {
		t.Errorf("slot %d is bound to %+v, want %+v", k, cfg.Keys[k].Fn, want)
	}
}

// TestMacrosEmulated creates a macro in the editor, binds it to Forward
// (slot 4) and applies it; then edits it and binds it again, which the
// planner turns into a two-phase rebind. Both runs go through the review,
// the preflight, the journal and the read-back, keys only at 80x24.
func TestMacrosEmulated(t *testing.T) {
	e, _ := macEmulate(t)
	h := e.h
	e.see(t, "[3 Macros]", "no button runs a macro")

	h.keys("n", "enter")
	h.typeText("Hello")
	h.keys("enter", "i", "/")
	h.typeText("KeyH")
	h.keys("enter", "enter", "c", "enter")
	e.see(t, "Name    Hello", "4/70 events · 53/384 bytes", "↓ press    Left Button")
	h.keys("b", "down")
	e.see(t, "writes the macro into slot 4 (@2304), then binds Forward (slot 4)")
	h.keys("enter")
	e.see(t, "1 pending", `Pending: Forward (slot 4) → Macro "Hello" ×1`)
	macSlotRead(h, 4)
	e.see(t, "Hello    4      53    ×1     Forward (slot 4), pending")

	h1, click := macTap(t, "KeyH", 10), keys.Stroke{Kind: keys.KindMouse, Value: 1}
	first := mouse.Macro{Name: "Hello", Events: append(h1,
		mouse.Event{Press: true, Stroke: click, Delay: 10}, mouse.Event{Stroke: click, Delay: 10})}
	run := e.apply(t)
	if got := macPhases(run); !slices.Equal(got, []plan.Phase{plan.Body, plan.Bind}) {
		t.Errorf("first bind: phases %v, want body then bind", got)
	}
	macHolds(t, e, 4, first, 1)
	h.flush(func() bool { return h.app.config != nil && h.app.config.Keys[4].Fn.Type == mouse.TypeMacro })
	e.see(t, "Hello    4      53    ×1     Forward (slot 4)")

	h.keys("enter", "end", "i", "/")
	h.typeText("KeyI")
	h.keys("enter", "enter", "home", "down", "right")
	e.see(t, "Macro in slot 4", "6/70 events · 63/384 bytes", "while held (254)")
	h.keys("b")
	e.see(t, "Forward (slot 4) may run that slot now, so it is disabled")
	h.keys("enter")
	e.see(t, "1 pending", `Macro "Hello" while held`)
	macSlotRead(h, 4)

	second := first
	second.Events = append(slices.Clone(first.Events), macTap(t, "KeyI", 10)...)
	rebind := e.apply(t)
	if got := macPhases(rebind); !slices.Equal(got, []plan.Phase{plan.Neutralise, plan.Body, plan.Bind}) {
		t.Errorf("rebind: phases %v, want neutralise, body, bind", got)
	}
	macHolds(t, e, 4, second, 254)

	st := e.journal(t)
	if len(st.Runs) != 2 || st.Runs[0].ID != run.ID || st.Runs[1].ID != rebind.ID || !st.Clean() {
		t.Fatalf("the journal holds %d runs, open %d", len(st.Runs), len(st.Open))
	}
	h.flush(func() bool {
		cfg := h.app.config
		return cfg != nil && cfg.Keys[4].Fn.Param&0xFF == 254 && cfg.Macros[4] != nil && len(cfg.Macros[4].Events) == 6
	})
	e.see(t, "Hello    6      63    while held Forward (slot 4)")
}
