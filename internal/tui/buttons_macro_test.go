package tui

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

// heldReads is a Reader whose reads wait until the test lets them end.
type heldReads struct {
	mu      sync.Mutex
	asked   []flash.Extent
	release chan struct{}
}

func (r *heldReads) read(ctx context.Context, e ...flash.Extent) (session.Capture, error) {
	r.mu.Lock()
	r.asked = append(r.asked, e...)
	r.mu.Unlock()
	<-r.release
	return session.Capture{}, nil
}

func (r *heldReads) extents() []flash.Extent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

// btnMacroHarness is the Buttons tab with the Macros tab behind its Macro
// group, over lib; the Macros tab reads through reads.
func btnMacroHarness(t *testing.T, sn *session.Snapshot, lib *library.Store, read Reader, opts ...func(*Options)) (*harness, *Buttons, *Macros) {
	t.Helper()
	m := NewMacros(read, lib)
	b := NewButtons(read, m)
	set := func(o *Options) {
		o.Tabs = []Tab{b, m}
		o.OS = keys.Mac
		o.Gates.AllowUntested = true
	}
	h := newHarness(t, sn, append([]func(*Options){set}, opts...)...)
	h.flush(func() bool { return m.loaded })
	return h, b, m
}

// btnInfo is what the Buttons tab shows about slot.
func btnInfo(t *testing.T, h *harness, b *Buttons, slot int) buttonInfo {
	t.Helper()
	for _, r := range b.rows(h.app.context()) {
		if r.slot == slot {
			return b.info(h.app.context(), r)
		}
	}
	t.Fatalf("no row for slot %d", slot)
	return buttonInfo{}
}

// pickMacro puts the picker's cursor on the Macro group's entry named name.
func pickMacro(t *testing.T, h *harness, p *buttonPicker, name string) {
	t.Helper()
	c := h.app.context()
	p.sec = secMacro
	i := slices.IndexFunc(p.filtered(c, secMacro), func(e pickEntry) bool { return e.name == name })
	if i < 0 {
		t.Fatalf("the Macro group has no %q: %+v", name, p.filtered(c, secMacro))
	}
	p.lists[secMacro].cursor = i
}

// The picker's Macro group lists the macros on the mouse, the unbound ones
// and the library's, each once, after "New macro…"; it opens on the macro
// the button runs. Choosing one stages it through the Macros tab, which
// reads the macro slot; while that read runs the button shows the macro by
// its name and "reading…", never "refused".
func TestButtonsMacroGroup(t *testing.T) {
	reads := &heldReads{release: make(chan struct{})}
	lib := macLibrary(t, macGreet(t), library.Entry{Macro: macCopy(t), Cycle: 1})
	sn := macSnapshot(t)
	h, b, m := btnMacroHarness(t, sn, lib, reads.read)

	btnRow(t, h, b, 4)
	h.keys("enter")
	p := btnPicker(t, h)
	c := h.app.context()
	var names []string
	for _, e := range p.filtered(c, secMacro) {
		names = append(names, e.name+" | "+e.info+" | "+e.where)
	}
	want := []string{"New macro… | opens the editor | ", "Hello | until any key | Forward (slot 4)", "copy | ×1 | slot 9, no button",
		"greet | ×3 | library"}
	if !slices.Equal(names, want) {
		t.Fatalf("the Macro group lists\n%q\nwant\n%q", names, want)
	}
	if p.current(c) != secMacro || p.lists[secMacro].cursor != 1 {
		t.Errorf("Forward runs Hello; the picker opened on group %d, entry %d", p.sec, p.lists[secMacro].cursor)
	}
	h.keys("esc")

	btnRow(t, h, b, 3)
	h.keys("enter")
	p = btnPicker(t, h)
	h.keys("tab")
	if p.current(h.app.context()) != secMacro {
		t.Fatalf("tab from the composer went to group %d", p.sec)
	}
	pickMacro(t, h, p, "greet")
	h.golden("buttons-picker-macro")
	h.keys("enter")
	if h.app.dialog != nil {
		t.Fatalf("the picker stays open over %T (notice %q)", h.app.dialog, h.app.notice.text)
	}
	e, ok := btnStaged(t, h, 3).(mouse.SetMacro)
	if !ok || e.Macro.Name != "greet" || e.Cycle != 3 {
		t.Fatalf("staged %#v", btnStaged(t, h, 3))
	}
	if !strings.Contains(h.app.notice.text, `Pending: Backward (slot 3) → Macro "greet" ×3`) {
		t.Errorf("notice %q", h.app.notice.text)
	}
	ext, _ := mouse.MacroExtent(3)
	h.flush(func() bool { return len(reads.extents()) > 0 })
	if !m.reading || !slices.Equal(reads.extents(), []flash.Extent{ext}) {
		t.Fatalf("the macro slot read: running %v, extents %v", m.reading, reads.extents())
	}
	h.golden("buttons-macro-reading")
	c = h.app.context()
	in := btnInfo(t, h, b, 3)
	if in.function() != `Macro "greet" ×3` || in.tierText(c) != "reading…" || in.refused != "" ||
		in.unreadText(c) != "reading the macro slot at 1920+384 from the mouse" {
		t.Errorf("while the slot is read: %q, %q, refused %q, %q", in.function(), in.tierText(c), in.refused, in.unreadText(c))
	}

	close(reads.release)
	h.flush(func() bool { return !m.reading })
	c = h.app.context()
	if in := btnInfo(t, h, b, 3); in.tierText(c) != "not read" || in.unreadText(c) != "the macro slot at 1920+384 was never read; r reloads and reads it" {
		t.Errorf("after a read that loaded nothing: %q, %q", in.tierText(c), in.unreadText(c))
	}
	loaded := withState(sn, session.Ready)
	loaded.Image = sn.Image.Clone()
	if err := loaded.Image.Set(ext.Addr, bytes.Repeat([]byte{0xFF}, ext.Len)); err != nil {
		t.Fatal(err)
	}
	h.snap(loaded)
	c = h.app.context()
	if in := btnInfo(t, h, b, 3); in.unread != nil || in.tierText(c) != "untested" || in.function() != `Macro "greet" ×3` {
		t.Errorf("once the slot is read: %q, %q", in.function(), in.tierText(c))
	}
	if _, err := h.app.context().Plan(); err != nil {
		t.Errorf("the review cannot plan the staged macro: %v", err)
	}
}

// A pending macro whose slot was never read is read while the mouse is
// ready; until then the button says when it will be.
func TestButtonsPendingMacroReadWhenReady(t *testing.T) {
	reads := &btnReads{}
	sn := withState(macSnapshot(t), session.Offline)
	h, b, m := btnMacroHarness(t, sn, macLibrary(t, macGreet(t)), reads.read)
	btnRow(t, h, b, 3)
	h.keys("enter")
	pickMacro(t, h, btnPicker(t, h), "greet")
	h.keys("enter")
	btnStaged(t, h, 3)
	if len(reads.extents) != 0 || !strings.Contains(h.app.notice.text, "once the mouse is ready") {
		t.Fatalf("read %v while offline (notice %q)", reads.extents, h.app.notice.text)
	}
	c := h.app.context()
	if in := btnInfo(t, h, b, 3); in.tierText(c) != "not read" || !strings.HasSuffix(in.unreadText(c), "was never read; it is read once the mouse is ready") {
		t.Errorf("offline: %q, %q", in.tierText(c), in.unreadText(c))
	}
	h.snap(macSnapshot(t))
	ext, _ := mouse.MacroExtent(3)
	if !slices.Equal(reads.extents, []flash.Extent{ext}) || m.reading {
		t.Errorf("once ready the tabs read %v, want the macro slot %v once", reads.extents, ext)
	}
}

// "New macro…" puts the Macros tab's editor in place of the picker; its
// binder starts on the button, and staging closes it all.
func TestButtonsNewMacro(t *testing.T) {
	h, b, _ := btnMacroHarness(t, macSnapshot(t), macLibrary(t), (&btnReads{}).read)
	btnRow(t, h, b, 3)
	h.keys("enter")
	pickMacro(t, h, btnPicker(t, h), "New macro…")
	if s := h.screen(80, 24); !strings.Contains(s, "opens the macro editor on a new macro; b there binds it to Backward (slot 3)") {
		t.Errorf("the preview of New macro:\n%s", s)
	}
	h.keys("enter")
	ed := macEditor(t, h)
	if ed.bindTo != 3 || len(h.app.under) != 0 || !strings.Contains(h.screen(80, 24), "New macro for Backward (slot 3)") {
		t.Fatalf("the editor binds to %d over %d dialogs:\n%s", ed.bindTo, len(h.app.under), h.screen(80, 24))
	}
	h.keys("enter")
	h.typeText("yo")
	h.keys("enter", "i", "/")
	h.typeText("KeyY")
	h.keys("enter", "enter", "b")
	d, ok := h.app.dialog.(*macroBinder)
	if !ok {
		t.Fatalf("b opened %T", h.app.dialog)
	}
	if rows := buttonRows(h.app.context().Model(), false); rows[d.cursor].slot != 3 {
		t.Errorf("the binder starts on slot %d, want 3", rows[d.cursor].slot)
	}
	h.keys("enter")
	if h.app.dialog != nil {
		t.Fatalf("still open: %T", h.app.dialog)
	}
	if e, ok := btnStaged(t, h, 3).(mouse.SetMacro); !ok || e.Macro.Name != "yo" || len(e.Macro.Events) != 2 {
		t.Fatalf("staged %#v", btnStaged(t, h, 3))
	}
}

// Choosing a macro whose name another button's macro carries asks, as the
// binder does, whether that button takes the same events.
func TestButtonsMacroNamesakes(t *testing.T) {
	other := library.Entry{Macro: mouse.Macro{Name: "Hello", Events: macTap(t, "KeyZ", 10)}, Cycle: 1}
	h, b, _ := btnMacroHarness(t, macSnapshot(t), macLibrary(t, other), (&btnReads{}).read)
	btnRow(t, h, b, 3)
	h.keys("enter")
	p := btnPicker(t, h)
	c := h.app.context()
	i := slices.IndexFunc(p.filtered(c, secMacro), func(e pickEntry) bool { return e.where == "library" })
	p.sec, p.lists[secMacro].cursor = secMacro, i
	h.keys("enter")
	q, ok := h.app.dialog.(*Confirm)
	if !ok || !strings.Contains(strings.Join(q.Body, " "), `Forward (slot 4) runs another macro named "Hello"`) {
		t.Fatalf("enter opened %T", h.app.dialog)
	}
	h.keys("y")
	if h.app.dialog != nil {
		t.Fatalf("still open: %T", h.app.dialog)
	}
	three, _ := btnStaged(t, h, 3).(mouse.SetMacro)
	four, _ := btnStaged(t, h, 4).(mouse.SetMacro)
	if !slices.Equal(three.Macro.Events, other.Macro.Events) || !slices.Equal(four.Macro.Events, other.Macro.Events) || four.Cycle != 255 {
		t.Errorf("staged %#v and %#v", three, four)
	}
}

// TestButtonsMacroEmulated makes a macro from the Buttons tab of the
// default tabs over a real session and an emulated EM11 Pro, keys only at
// 80x24: "New macro…" in Backward's picker, the editor, the binder. The
// macro slot, which the load never read, is read through the session, and
// the apply writes the body, then the binding.
func TestButtonsMacroEmulated(t *testing.T) {
	e := accEmulate(t)
	h := e.h
	e.cursorTo(t, "Backward")
	h.keys("enter", "tab")
	e.see(t, "[Macro]", "New macro…")
	h.keys("enter", "enter")
	h.typeText("Hey")
	h.keys("enter", "i", "/")
	h.typeText("KeyH")
	h.keys("enter", "enter", "b")
	e.see(t, "writes the macro into slot 3 (@1920), then binds Backward (slot 3)")
	h.keys("enter")
	e.see(t, "1 pending", `Pending: Backward (slot 3) → Macro "Hey" ×1`)
	macSlotRead(h, 3)
	if l := btnLine(e.screen(t), "Backward    3"); !strings.Contains(l, `Macro "Hey" ×1`) || !strings.Contains(l, "untested") {
		t.Errorf("the pending macro once its slot is read: %q", l)
	}
	run := e.apply(t)
	if got := macPhases(run); !slices.Equal(got, []plan.Phase{plan.Body, plan.Bind}) {
		t.Errorf("phases %v, want body then bind", got)
	}
	macHolds(t, e, 3, mouse.Macro{Name: "Hey", Events: macTap(t, "KeyH", 10)}, 1)
}
