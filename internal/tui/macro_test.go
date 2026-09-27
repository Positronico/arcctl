package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

func macTap(t *testing.T, code string, delay uint16) []mouse.Event {
	t.Helper()
	s := btnStroke(t, code)
	return []mouse.Event{{Press: true, Stroke: s, Delay: delay}, {Stroke: s, Delay: delay}}
}

func macHello(t *testing.T) mouse.Macro {
	return mouse.Macro{Name: "Hello", Events: slices.Concat(macTap(t, "KeyH", 30), macTap(t, "KeyI", 10))}
}

func macCopy(t *testing.T) mouse.Macro {
	cmd := keys.LMeta.Stroke()
	return mouse.Macro{Name: "copy", Events: slices.Concat(
		[]mouse.Event{{Press: true, Stroke: cmd, Delay: 10}}, macTap(t, "KeyC", 10), []mouse.Event{{Stroke: cmd, Delay: 10}})}
}

// macPut writes m into macro slot k, padded with erased flash.
func macPut(t *testing.T, im *flash.Image, k int, m mouse.Macro) {
	t.Helper()
	body, err := mouse.EncodeMacro(m)
	if err != nil {
		t.Fatal(err)
	}
	ext, _ := mouse.MacroExtent(k)
	if err := im.Set(ext.Addr, append(body, bytes.Repeat([]byte{0xFF}, ext.Len-len(body))...)); err != nil {
		t.Fatal(err)
	}
}

func macBind(t *testing.T, im *flash.Image, slot, src, cycle int) {
	t.Helper()
	fn := mouse.KeyFn{Type: mouse.TypeMacro, Param: uint16(src)<<8 | uint16(cycle)}
	r, err := mouse.EncodeKeyFn(fn)
	if err != nil {
		t.Fatal(err)
	}
	ext, _ := mouse.KeyFnExtent(slot)
	if err := im.Set(ext.Addr, r); err != nil {
		t.Fatal(err)
	}
}

// macSnapshot is the maintainer's mouse with "Hello" on Forward (slot 4),
// repeated until any key, and "copy" left unbound in slot 9.
func macSnapshot(t *testing.T) *session.Snapshot {
	sn := btnSnapshot(t)
	im := sn.Image.Clone()
	macPut(t, im, 4, macHello(t))
	macBind(t, im, 4, 4, 255)
	macPut(t, im, 9, macCopy(t))
	sn.Image = im
	return sn
}

func macLibrary(t *testing.T, entries ...library.Entry) *library.Store {
	t.Helper()
	s := &library.Store{Path: filepath.Join(t.TempDir(), "macros.json")}
	if len(entries) > 0 {
		if err := s.Save(entries); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func macGreet(t *testing.T) library.Entry {
	evs := slices.Concat(macTap(t, "KeyG", 20), macTap(t, "KeyO", 20))
	evs = append(evs, mouse.Event{Press: true, Stroke: keys.Stroke{Kind: keys.KindMouse, Value: 1}, Delay: 50},
		mouse.Event{Stroke: keys.Stroke{Kind: keys.KindMouse, Value: 1}, Delay: 10})
	return library.Entry{Macro: mouse.Macro{Name: "greet", Events: evs}, Cycle: 3}
}

func macHarness(t *testing.T, sn *session.Snapshot, lib *library.Store, opts ...func(*Options)) (*harness, *Macros, *btnReads) {
	t.Helper()
	reads := &btnReads{}
	m := NewMacros(reads.read, lib)
	set := func(o *Options) {
		o.Tabs = []Tab{m}
		o.OS = keys.Mac
	}
	h := newHarness(t, sn, append([]func(*Options){set}, opts...)...)
	h.flush(func() bool { return m.loaded || lib == nil })
	return h, m, reads
}

func macEditor(t *testing.T, h *harness) *macroEditor {
	t.Helper()
	ed, ok := h.app.dialog.(*macroEditor)
	if !ok {
		t.Fatalf("the editor is not open (dialog %T, notice %q)", h.app.dialog, h.app.notice.text)
	}
	return ed
}

// macCursor puts the list cursor on the row named name.
func macCursor(t *testing.T, h *harness, m *Macros, src macroSource, name string) {
	t.Helper()
	for i, r := range m.rows(h.app.context()) {
		if r.src == src && r.macro != nil && r.macro.Name == name {
			m.cursor = i
			return
		}
	}
	t.Fatalf("no row %q", name)
}

func TestMacroNames(t *testing.T) {
	cases := map[string]string{
		"Hello":                     "Hello",
		"a b.c-d_e!":                "abcde",
		"tab\there\nnew":            "tabherenew",
		"复制，粘贴。":                    "复制粘贴",
		"《书名》【注】":                   "书名注",
		"esc\x1b[31mred":            "esc31mred",
		"bom\ufeffzero\u200bwidth":  "bomzerowidth",
		"nbsp\u00a0ideo\u3000space": "nbspideospace",
		strings.Repeat("x", 40):     strings.Repeat("x", 30),
		strings.Repeat("字", 11):     strings.Repeat("字", 10),
		"bad\xffbyte":               "badbyte",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	shown := map[string]string{
		"plain":          "plain",
		"esc\x1b[31mred": `esc\x1b[31mred`,
		`back\slash`:     `back\\slash`,
		"rtl\u202eabc":   `rtl\u202eabc`,
		"bad\xffbyte":    `bad\xffbyte`,
		"line\u2028sep":  `line\u2028sep`,
		"复制":             "复制",
	}
	for in, want := range shown {
		if got := shownName(in); got != want {
			t.Errorf("shownName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMacroTidyScan(t *testing.T) {
	a, b := btnStroke(t, "KeyA"), btnStroke(t, "KeyB")
	cmd := keys.LMeta.Stroke()
	ev := func(press bool, s keys.Stroke) mouse.Event { return mouse.Event{Press: press, Stroke: s, Delay: 10} }
	r := tidyScan([]mouse.Event{ev(true, cmd), ev(true, a), ev(true, a), ev(false, b), ev(true, b), ev(false, a)})
	want := map[int]string{0: "never released", 2: "pressed again before its release", 3: "released without a press", 4: "never released"}
	if len(r.flags) != len(want) {
		t.Errorf("flags %v, want %v", r.flags, want)
	}
	for i, w := range want {
		if r.flags[i] != w {
			t.Errorf("event %d: %q, want %q", i, r.flags[i], w)
		}
	}
	if !slices.Equal(r.missing, []keys.Stroke{b, cmd}) {
		t.Errorf("missing %v, want B then Cmd", r.missing)
	}
	if r := tidyScan(macCopy(t).Events); len(r.flags) != 0 || len(r.missing) != 0 {
		t.Errorf("a paired macro: %+v", r)
	}
}

func TestMacrosScreens(t *testing.T) {
	lib := macLibrary(t, macGreet(t), library.Entry{Macro: macHello(t), Cycle: 255})
	h, m, _ := macHarness(t, macSnapshot(t), lib)
	macCursor(t, h, m, srcMouse, "Hello")
	h.golden("macros-list")

	h.keys("enter")
	ed := macEditor(t, h)
	h.keys("down", "x")
	if len(ed.events) != 3 {
		t.Fatalf("%d events after a delete", len(ed.events))
	}
	h.golden("macros-editor")

	h.keys("t")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("t opened %T", h.app.dialog)
	}
	h.golden("macros-tidy")
	h.keys("y")
	if len(ed.events) != 4 || ed.events[3].Press || ed.events[3].Stroke != btnStroke(t, "KeyH") {
		t.Fatalf("after the tidy: %+v", ed.events)
	}

	h.keys("i", "/")
	h.typeText("KeyZ")
	h.keys("enter")
	h.golden("macros-keys")
	h.keys("enter")
	if len(ed.events) != 6 || h.app.dialog != ed {
		t.Fatalf("after the insert: %d events, dialog %T", len(ed.events), h.app.dialog)
	}

	h.keys("b")
	h.golden("macros-bind")
	h.keys("down", "down")
	if s := h.screen(80, 24); !strings.Contains(s, "Forward (slot 4) runs a macro of the same name with other events") {
		t.Errorf("a second macro named Hello:\n%s", s)
	}
}

func TestMacrosEmptyLibrary(t *testing.T) {
	h, _, _ := macHarness(t, btnSnapshot(t), macLibrary(t))
	h.golden("macros-empty")
	if s := h.screen(80, 24); !strings.Contains(s, "no button runs a macro") || !strings.Contains(s, "empty: s saves a macro here") {
		t.Errorf("empty sections:\n%s", s)
	}
}

func TestMacrosEditorKeys(t *testing.T) {
	h, m, _ := macHarness(t, btnSnapshot(t), macLibrary(t))
	h.keys("n")
	ed := macEditor(t, h)
	h.keys("b")
	if ed.note == "" || h.app.dialog != ed {
		t.Fatal("b bound a macro without a name")
	}
	h.keys("home", "enter")
	h.typeText("my macro, v2!")
	h.keys("enter")
	if ed.name != "mymacrov2" {
		t.Errorf("name %q", ed.name)
	}
	h.keys("enter", "ctrl+u")
	h.typeText(strings.Repeat("n", 35))
	h.keys("enter")
	if len(ed.name) != mouse.MaxNameLen {
		t.Errorf("a long name became %d bytes", len(ed.name))
	}

	h.keys("down")
	var cycles []int
	for range 4 {
		h.keys("right")
		cycles = append(cycles, ed.cycle)
	}
	if !slices.Equal(cycles, []int{254, 255, 253, 1}) {
		t.Errorf("repeat modes %v", cycles)
	}
	h.keys("enter", "backspace")
	h.typeText("300")
	h.keys("enter")
	if ed.cycle != 250 || !ed.bad {
		t.Errorf("count 300 became %d (%q)", ed.cycle, ed.note)
	}
	h.keys("left", "right")
	if ed.cycle != 250 {
		t.Errorf("the count was not kept across modes: %d", ed.cycle)
	}

	h.keys("c", "enter")
	if len(ed.events) != 2 || ed.events[0].Stroke.Kind != keys.KindMouse || !ed.events[0].Press || ed.events[1].Press {
		t.Fatalf("click pair: %+v", ed.events)
	}
	h.keys("i", "tab", "/")
	h.typeText("ShiftLeft")
	h.keys("enter", "enter")
	if len(ed.events) != 3 || ed.events[2].Stroke != keys.LShift.Stroke() || !ed.events[2].Press {
		t.Fatalf("press: %+v", ed.events)
	}
	h.keys("K")
	if ed.events[1].Stroke != keys.LShift.Stroke() || ed.sel() != 1 {
		t.Fatalf("move up: %+v, selected %d", ed.events, ed.sel())
	}
	h.keys("enter", "ctrl+u")
	h.typeText("5")
	h.keys("enter")
	if ed.events[1].Delay != library.MinDelay {
		t.Errorf("delay 5 became %d", ed.events[1].Delay)
	}
	h.keys("right", "right")
	if ed.events[1].Delay != 30 {
		t.Errorf("delay after two steps: %d", ed.events[1].Delay)
	}
	h.keys("space")
	if ed.events[1].Press {
		t.Error("space did not make the press a release")
	}
	h.keys("e", "/")
	h.typeText("ShiftRight")
	h.keys("enter", "enter")
	if ed.events[1].Stroke != keys.RShift.Stroke() || ed.events[1].Press || len(ed.events) != 3 {
		t.Errorf("change key: %+v", ed.events)
	}
	h.keys("x")
	if len(ed.events) != 2 {
		t.Errorf("%d events after a delete", len(ed.events))
	}

	for len(ed.events) < mouse.MaxMacroEvents-1 {
		if err := ed.insert(macTap(t, "KeyA", 10)[0]); err != nil {
			t.Fatal(err)
		}
	}
	h.keys("i", "enter")
	if len(ed.events) != mouse.MaxMacroEvents-1 {
		t.Errorf("a tap past the limit made %d events", len(ed.events))
	}
	h.keys("esc")
	if s := h.screen(80, 24); !strings.Contains(s, "69/70 events · 378/384 bytes") {
		t.Errorf("counters:\n%s", s)
	}

	h.keys("esc")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("esc on a changed macro opened %T", h.app.dialog)
	}
	h.keys("y")
	if h.app.dialog != nil || m.loaded && len(m.entries) != 0 {
		t.Errorf("after dropping the changes: dialog %T, %d library macros", h.app.dialog, len(m.entries))
	}
}

func TestMacrosBindStages(t *testing.T) {
	h, m, reads := macHarness(t, macSnapshot(t), macLibrary(t, macGreet(t)))
	macCursor(t, h, m, srcLibrary, "greet")
	h.keys("B")
	d, ok := h.app.dialog.(*macroBinder)
	if !ok {
		t.Fatalf("B opened %T", h.app.dialog)
	}
	if rows := buttonRows(h.app.context().Model(), false); rows[d.cursor].slot != 2 {
		t.Errorf("a new binding starts on slot %d, want 2, the first button that is neither guarded nor a main click", rows[d.cursor].slot)
	}
	h.keys("up", "up")
	if s := h.screen(80, 24); !strings.Contains(s, "refused: this is the last button on Left Click") || strings.Contains(s, "96+4") {
		t.Errorf("the guard on Left Click is not in plain words:\n%s", s)
	}
	h.keys("enter")
	if d.err == "" || h.app.dialog != d {
		t.Fatalf("the last Left Click took a macro (notice %q)", h.app.notice.text)
	}
	for range 4 {
		h.keys("down")
	}
	h.keys("enter")
	if h.app.dialog != nil {
		t.Fatalf("still open: %T (%s)", h.app.dialog, d.err)
	}
	e, ok := btnStaged(t, h, 3).(mouse.SetMacro)
	if !ok || e.Macro.Name != "greet" || e.Cycle != 3 {
		t.Fatalf("staged %#v", btnStaged(t, h, 3))
	}
	ext, _ := mouse.MacroExtent(3)
	if !slices.Contains(reads.extents, ext) {
		t.Errorf("the tab read %v, want the macro slot %v", reads.extents, ext)
	}
	if !strings.Contains(h.app.notice.text, `Pending: Backward (slot 3) → Macro "greet" ×3`) {
		t.Errorf("notice %q", h.app.notice.text)
	}
	rows, _, _ := m.macroRows(h.app.context())
	if len(rows) != 2 || rows[0].src != srcPending || rows[0].slot != 3 {
		t.Fatalf("rows on the mouse: %+v", rows)
	}

	macCursor(t, h, m, srcMouse, "Hello")
	h.keys("B")
	d = h.app.dialog.(*macroBinder)
	if rows := buttonRows(h.app.context().Model(), false); rows[d.cursor].slot != 4 {
		t.Errorf("the chooser starts on slot %d, want 4", rows[d.cursor].slot)
	}
	h.keys("enter")
	if h.app.dialog != nil || !strings.Contains(h.app.notice.text, "already runs") {
		t.Errorf("binding a macro where it is: dialog %T, notice %q", h.app.dialog, h.app.notice.text)
	}

	macCursor(t, h, m, srcPending, "greet")
	h.keys("d")
	if h.app.Pending().Len() != 0 {
		t.Error("d kept the pending macro")
	}

	h2, m2, _ := macHarness(t, macSnapshot(t), macLibrary(t, macGreet(t)), func(o *Options) { o.Mode = ModeReadOnly })
	macCursor(t, h2, m2, srcLibrary, "greet")
	h2.keys("B", "down", "down", "enter")
	if d := h2.app.dialog.(*macroBinder); !strings.Contains(d.err, "read-only") || h2.app.Pending().Len() != 0 {
		t.Errorf("read-only: %q, %d pending", d.err, h2.app.Pending().Len())
	}
}

func TestMacrosLibraryFiles(t *testing.T) {
	lib := macLibrary(t)
	h, m, _ := macHarness(t, macSnapshot(t), lib)
	macCursor(t, h, m, srcMouse, "Hello")
	h.keys("s")
	h.flush(func() bool { got, _ := lib.Load(); return len(got) == 1 })
	macCursor(t, h, m, srcFlash, "copy")
	h.keys("s")
	h.flush(func() bool { got, _ := lib.Load(); return len(got) == 2 })
	got, _ := lib.Load()
	if !got[0].Equal(library.Entry{Macro: macHello(t), Cycle: 255}) || !got[1].Equal(library.Entry{Macro: macCopy(t), Cycle: 1}) {
		t.Fatalf("library %+v", got)
	}

	out := filepath.Join(t.TempDir(), "export.json")
	h.keys("e", "ctrl+u")
	h.typeText(out)
	h.keys("enter")
	h.flush(func() bool {
		_, err := os.Stat(out)
		return err == nil && strings.HasPrefix(h.app.notice.text, "Exported")
	})
	h.keys("e", "ctrl+u")
	h.typeText(out)
	h.keys("enter")
	h.flush(func() bool { return h.app.notice.bad })
	if n := h.app.notice; n.text != "Export failed: the file exists; exports never replace one. Type another name:" || n.path != out {
		t.Errorf("a second export over the file: %q %q", n.text, n.path)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "Type another name:") || !strings.Contains(s, "export.json") {
		t.Errorf("the notice cuts the cause or the file name:\n%s", s)
	}
	missing := filepath.Join(t.TempDir(), "none.json")
	h.keys("i")
	h.typeText(missing)
	h.keys("enter")
	h.flush(func() bool { return h.app.notice.bad })
	if n := h.app.notice; n.text != "Import failed: there is no such file:" || n.path != missing {
		t.Errorf("an import of a missing file: %q %q", n.text, n.path)
	}

	other := filepath.Join(t.TempDir(), "in.json")
	changed := library.Entry{Macro: macHello(t), Cycle: 2}
	if err := library.WriteNew(other, []library.Entry{macGreet(t), changed, got[1]}); err != nil {
		t.Fatal(err)
	}
	h.keys("i")
	h.typeText(other)
	h.keys("enter")
	h.flush(func() bool { return strings.HasPrefix(h.app.notice.text, "Imported") })
	if want := `Imported 1 macro, 1 already in the library; left out, as the library has other macros by these names: "Hello".`; h.app.notice.text != want {
		t.Errorf("notice %q\nwant   %q", h.app.notice.text, want)
	}
	h.flush(func() bool { got, _ := lib.Load(); return len(got) == 3 })

	macCursor(t, h, m, srcLibrary, "copy")
	h.keys("x", "y")
	h.flush(func() bool { got, _ := lib.Load(); return len(got) == 2 })
	if len(m.entries) != 2 || library.Index(m.entries, "copy") >= 0 {
		t.Errorf("after the delete: %d entries", len(m.entries))
	}

	macCursor(t, h, m, srcLibrary, "greet")
	h.keys("enter", "home", "enter", "ctrl+u")
	h.typeText("Hello")
	h.keys("enter", "s")
	if !h.app.notice.bad || !strings.Contains(h.app.notice.text, "another macro named") {
		t.Errorf("a rename onto another library macro: %q", h.app.notice.text)
	}
	h.keys("enter", "ctrl+u")
	h.typeText("greet2")
	h.keys("enter", "s")
	h.flush(func() bool { got, _ := lib.Load(); return library.Index(got, "greet2") >= 0 })
	if got, _ := lib.Load(); len(got) != 2 || library.Index(got, "greet") >= 0 {
		t.Errorf("a renamed library macro: %+v", got)
	}
	if ed := macEditor(t, h); ed.dirty() {
		t.Error("the editor is still changed after a save")
	}
}

func TestMacrosWithoutLibrary(t *testing.T) {
	h, m, _ := macHarness(t, macSnapshot(t), nil)
	macCursor(t, h, m, srcMouse, "Hello")
	h.keys("s")
	if !h.app.notice.bad || !strings.Contains(h.app.notice.text, "no macro library") {
		t.Errorf("notice %q", h.app.notice.text)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "not available in this session") {
		t.Errorf("screen:\n%s", s)
	}
}

func TestMacrosReadSlotsWhenReady(t *testing.T) {
	sn := withState(macSnapshot(t), session.Offline)
	h, m, reads := macHarness(t, sn, macLibrary(t, macGreet(t)))
	macCursor(t, h, m, srcLibrary, "greet")
	h.keys("B", "down", "down", "enter")
	if !strings.Contains(h.app.notice.text, "Its macro slot is read once the mouse is ready.") || len(reads.extents) != 0 {
		t.Fatalf("offline: notice %q, reads %v", h.app.notice.text, reads.extents)
	}
	ext, _ := mouse.MacroExtent(3)
	h.snap(macSnapshot(t))
	if !slices.Equal(reads.extents, []flash.Extent{ext}) {
		t.Fatalf("once ready the tab read %v, want %v", reads.extents, ext)
	}
	h.snap(macSnapshot(t))
	if len(reads.extents) != 2 {
		t.Errorf("a new load without the slot: reads %v", reads.extents)
	}
	loaded := macSnapshot(t)
	loaded.Image = loaded.Image.Clone()
	if err := loaded.Image.Set(ext.Addr, bytes.Repeat([]byte{0xFF}, ext.Len)); err != nil {
		t.Fatal(err)
	}
	h.snap(loaded)
	h.snap(loaded)
	if len(reads.extents) != 2 {
		t.Errorf("the slot is known, yet the tab read %v", reads.extents)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "greet    6      63    ×3            Backward (slot 3), pending") {
		t.Errorf("pending row:\n%s", s)
	}
}

// §7.7: a name the web app's sanitiser would change never reaches the
// planner; the user renames it in the editor first. An imported one takes
// the sanitised name.
func TestMacroOddNamesStayOffTheMouse(t *testing.T) {
	e := macGreet(t)
	e.Macro.Name = "a.b c\x1b[2J"
	if err := e.Check(); err != nil {
		t.Fatalf("setup: the library refuses the name: %v", err)
	}
	h, m, _ := macHarness(t, macSnapshot(t), macLibrary(t, e))
	macCursor(t, h, m, srcLibrary, e.Macro.Name)
	h.keys("B", "down", "enter")
	d, ok := h.app.dialog.(*macroBinder)
	if !ok || !strings.Contains(d.err, "rename it in the editor") || h.app.Pending().Len() != 0 {
		t.Fatalf("an odd name was staged: dialog %T, pending %d", h.app.dialog, h.app.Pending().Len())
	}
	h.keys("esc", "enter", "home", "enter")
	h.keys("enter", "b", "down", "enter")
	got, ok := btnStaged(t, h, 4).(mouse.SetMacro)
	if !ok || got.Macro.Name != "abc2J" {
		t.Fatalf("after the rename: %#v (notice %q)", btnStaged(t, h, 4), h.app.notice.text)
	}

	lib := macLibrary(t)
	h, m, _ = macHarness(t, macSnapshot(t), lib)
	in := filepath.Join(t.TempDir(), "in.json")
	odd := macGreet(t)
	odd.Macro.Name = "my macro!"
	blank := macGreet(t)
	blank.Macro.Name = "!!"
	if err := library.WriteNew(in, []library.Entry{odd, blank}); err != nil {
		t.Fatal(err)
	}
	h.keys("i")
	h.typeText(in)
	h.keys("enter")
	h.flush(func() bool { got, _ := lib.Load(); return len(got) == 1 })
	if got, _ := lib.Load(); got[0].Macro.Name != "mymacro" {
		t.Errorf("imported %q", got[0].Macro.Name)
	}
	if n := h.app.notice.text; !strings.Contains(n, `renamed, as the web app leaves their characters out of names: "my macro!" as "mymacro"`) ||
		!strings.Contains(n, `nothing of their names is left for the web app: "!!"`) {
		t.Errorf("notice %q", n)
	}
	_ = m
}

// The web app keeps one macro per name: a macro several buttons run is
// edited for all of them in one step, after the user agrees, and a
// namesake no button runs from its own slot needs a new name.
func TestMacroEditedForEveryButtonThatRunsIt(t *testing.T) {
	sn := macSnapshot(t)
	im := sn.Image.Clone()
	macPut(t, im, 3, macHello(t))
	macBind(t, im, 3, 3, 1)
	sn.Image = im
	h, m, _ := macHarness(t, sn, macLibrary(t))
	var row macroRow
	for _, r := range m.rows(h.app.context()) {
		if r.src == srcMouse && r.slot == 3 {
			row = r
		}
	}
	m.cursor = slices.IndexFunc(m.rows(h.app.context()), func(r macroRow) bool { return r.src == srcMouse && r.slot == row.slot })
	h.keys("enter", "c", "enter", "b")
	if s := h.screen(80, 24); !strings.Contains(s, "Forward (slot 4) runs a macro of the same name with other events") {
		t.Fatalf("the binder does not say another button runs the macro:\n%s", s)
	}
	h.keys("enter")
	c, ok := h.app.dialog.(*Confirm)
	if !ok {
		t.Fatalf("enter opened %T, want the question", h.app.dialog)
	}
	if body := strings.Join(c.Body, " "); !strings.Contains(body, `Forward (slot 4) runs another macro named "Hello"`) {
		t.Errorf("question %q", body)
	}
	h.keys("y")
	three, ok3 := btnStaged(t, h, 3).(mouse.SetMacro)
	four, ok4 := btnStaged(t, h, 4).(mouse.SetMacro)
	if !ok3 || !ok4 || len(three.Macro.Events) != 6 || !slices.Equal(three.Macro.Events, four.Macro.Events) || three.Cycle != 1 || four.Cycle != 255 {
		t.Fatalf("staged %#v and %#v (notice %q)", btnStaged(t, h, 3), btnStaged(t, h, 4), h.app.notice.text)
	}
	if !strings.Contains(h.app.notice.text, "and the same events for Forward (slot 4)") {
		t.Errorf("notice %q", h.app.notice.text)
	}
	if _, err := h.app.pending.Plan(sn.Model, h.app.context().Image(), h.app.context().MouseOptions()); err != nil {
		t.Errorf("the review cannot plan the staged macros: %v", err)
	}

	sn = macSnapshot(t)
	im = sn.Image.Clone()
	macPut(t, im, 3, macHello(t))
	sn.Image = im
	h, m, _ = macHarness(t, sn, macLibrary(t))
	macCursor(t, h, m, srcMouse, "Hello")
	h.keys("enter", "c", "enter", "b", "enter")
	if d, ok := h.app.dialog.(*macroBinder); !ok || !strings.Contains(d.err, "macro slot 3 holds another macro named") {
		t.Fatalf("an unbound namesake: dialog %T", h.app.dialog)
	}
}

// Delays under 10 ms, as the web app's recorder leaves them, are flagged in
// the editor, and tidy raises them; refusals number events from 1.
func TestMacroShortDelays(t *testing.T) {
	fast := mouse.Macro{Name: "Fast", Events: slices.Concat(macTap(t, "KeyF", 4), macTap(t, "KeyA", 10))}
	fast.Events[1].Delay = 6
	sn := btnSnapshot(t)
	im := sn.Image.Clone()
	macPut(t, im, 2, fast)
	macBind(t, im, 2, 2, 1)
	sn.Image = im
	h, m, _ := macHarness(t, sn, macLibrary(t))
	macCursor(t, h, m, srcMouse, "Fast")
	h.keys("s")
	if n := h.app.notice.text; !strings.Contains(n, "Event 1: a delay of 4 ms is under 10 ms") && !strings.Contains(n, "event 1") {
		t.Errorf("save: %q", n)
	}
	h.keys("enter")
	ed := macEditor(t, h)
	s := h.screen(80, 24)
	if !strings.Contains(s, "! a delay under 10 ms") || !strings.Contains(s, "2 delays are under 10 ms, the least arcctl writes: t tidies.") {
		t.Errorf("the editor does not flag the short delays:\n%s", s)
	}
	h.keys("home", "down", "right", "b", "enter")
	if d, ok := h.app.dialog.(*macroBinder); !ok || d.err != "Event 1: a delay of 4 ms is under 10 ms; t in the editor raises every such delay to 10 ms" {
		t.Errorf("binding with short delays after a repeat change: dialog %T", h.app.dialog)
	}
	h.keys("esc", "t")
	c, ok := h.app.dialog.(*Confirm)
	if !ok || !strings.Contains(strings.Join(c.Body, "\n"), "Raise 2 delays under 10 ms to 10 ms?") {
		t.Fatalf("tidy: %T %v", h.app.dialog, c)
	}
	h.keys("y")
	for i, e := range ed.events {
		if e.Delay < library.MinDelay {
			t.Errorf("event %d keeps %d ms", i+1, e.Delay)
		}
	}
	if !strings.Contains(ed.note, "Raised 2 delays to 10 ms") {
		t.Errorf("note %q", ed.note)
	}
}

// Tidy says what each kind of flag needs when it has nothing to add.
func TestMacroTidyAdvice(t *testing.T) {
	h, _, _ := macHarness(t, btnSnapshot(t), macLibrary(t))
	h.keys("n")
	ed := macEditor(t, h)
	hk := btnStroke(t, "KeyH")
	ed.events = []mouse.Event{{Press: true, Stroke: hk, Delay: 10}, {Press: true, Stroke: hk, Delay: 10}, {Stroke: hk, Delay: 10}}
	h.keys("t")
	if want := "1 event is pressed again before its release: x deletes one of the presses, or i adds a release between them."; ed.note != want {
		t.Errorf("pressed again: %q", ed.note)
	}
	ed.events = []mouse.Event{{Stroke: hk, Delay: 10}, {Stroke: hk, Delay: 10}}
	h.keys("t")
	if want := "2 releases have no press before them: x deletes the selected event."; ed.note != want {
		t.Errorf("releases: %q", ed.note)
	}
}

// ? lists every key of the editor, the key picker and the binder, but is
// text while a name is typed.
func TestMacroDialogHelp(t *testing.T) {
	h, _, _ := macHarness(t, btnSnapshot(t), macLibrary(t))
	h.keys("n", "?")
	hd, ok := h.app.dialog.(*helpDialog)
	if !ok {
		t.Fatalf("? in the editor opened %T", h.app.dialog)
	}
	var keys []string
	for _, b := range hd.keys {
		keys = append(keys, b.Help().Key)
	}
	for _, k := range []string{"t", "s", "J/K", "space", "←/→", "e", "o"} {
		if !slices.Contains(keys, k) {
			t.Errorf("the help lacks %s: %v", k, keys)
		}
	}
	h.keys("esc")
	ed := macEditor(t, h)
	h.keys("home", "enter", "?")
	if h.app.dialog != ed || ed.field != fieldName {
		t.Errorf("? while naming: dialog %T", h.app.dialog)
	}
	h.keys("esc", "i", "?")
	if _, ok := h.app.dialog.(*helpDialog); !ok {
		t.Errorf("? in the key picker opened %T", h.app.dialog)
	}
}

// The export prompt starts on a name no file has yet.
func TestMacroExportName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if got := exportName(now); got != "~/arcctl-macros-2026-09-27.json" {
		t.Errorf("first name %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, "arcctl-macros-2026-09-27.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := exportName(now); got != "~/arcctl-macros-2026-09-27-2.json" {
		t.Errorf("second name %q", got)
	}
}

// A library that would pass what arcctl reads back is refused before it is
// saved, and the tab keeps the one it has.
func TestMacroLibraryStaysLoadable(t *testing.T) {
	lib := macLibrary(t)
	h, m, _ := macHarness(t, macSnapshot(t), lib)
	var big []library.Entry
	var evs []mouse.Event
	for len(evs) < mouse.MaxMacroEvents {
		evs = append(evs, macTap(t, "KeyA", 65535)...)
	}
	for i := range 800 {
		big = append(big, library.Entry{Macro: mouse.Macro{Name: "m" + strconv.Itoa(i), Events: evs}, Cycle: 250})
	}
	h.deliver(h.run(m.fileDone(macroFileMsg{path: "/in.json", entries: big})))
	if !h.app.notice.bad || !strings.Contains(h.app.notice.text, "Nothing imported: too large") || len(m.entries) != 0 {
		t.Errorf("notice %q, %d entries", h.app.notice.text, len(m.entries))
	}
}
