package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

// Macros is the Macros tab (§7.7): the macros bound on the mouse, the valid
// bodies no binding runs, and the macro library. The editor builds a macro
// from the composer's key list and the mouse buttons (D3: no recorder).
// Binding a macro stages its body and binding as one pending edit for the
// review; library macros stay in the library whether a button runs them or
// not.
type Macros struct {
	read    Reader
	lib     *library.Store
	entries []library.Entry
	libErr  error
	loaded  bool
	asked   bool
	saves   *libSaves
	reading bool
	ready   bool
	// image is the loaded image of the last snapshot; a new load may lack
	// macro slots read for pending macros.
	image  *flash.Image
	cursor int
	offset int
}

// NewMacros builds the tab. read loads a macro slot that was never read
// before a macro bound to it can be planned; nil leaves that to the user.
// lib is the library file; nil leaves the tab without a library.
func NewMacros(read Reader, lib *library.Store) *Macros {
	t := &Macros{read: read, lib: lib, saves: &libSaves{}}
	if lib != nil {
		t.saves.store = *lib
	}
	return t
}

func (t *Macros) Title() string { return "Macros" }

func (t *Macros) Needs() []mouse.Feature { return []mouse.Feature{mouse.FeatureMacro} }

func (t *Macros) Capturing() bool { return false }

// macroSource is where a row of the list comes from.
type macroSource uint8

const (
	srcNew macroSource = iota
	srcMouse
	srcPending
	srcFlash
	srcLibrary
)

// macroRow is one macro of the list.
type macroRow struct {
	src  macroSource
	slot int // the macro slot, for the mouse, pending and flash rows
	lib  int // the library index, for library rows
	// macro is nil when the body was never read or is invalid.
	macro *mouse.Macro
	class flash.SlotClass
	cycle int
	// users are the slots whose bindings may run it.
	users []int
}

func (r macroRow) entry() library.Entry {
	cycle := r.cycle
	if cycle == 0 {
		cycle = 1
	}
	return library.Entry{Macro: *r.macro, Cycle: cycle}
}

// macroRows lists the mouse's macros by slot (pending ones in place of what
// they replace), then the valid bodies no binding runs, then the library.
func (t *Macros) macroRows(c *Context) (onMouse, inFlash, inLib []macroRow) {
	cfg := c.Config()
	pending := map[int]mouse.SetMacro{}
	for _, s := range c.Pending.List() {
		if e, ok := s.Edit.(mouse.SetMacro); ok {
			pending[e.Slot] = e
		}
	}
	for k := range mouse.Slots {
		if e, ok := pending[k]; ok {
			m := e.Macro
			onMouse = append(onMouse, macroRow{src: srcPending, slot: k, macro: &m, class: flash.SlotValid, cycle: e.Cycle, users: []int{k}})
			continue
		}
		if cfg == nil {
			continue
		}
		r := macroRow{slot: k, macro: cfg.Macros[k], class: cfg.MacroClass[k], users: macroSlotUsers(c, k)}
		if r.class != flash.SlotValid {
			r.macro = nil
		}
		if len(r.users) > 0 {
			j := r.users[0]
			if slices.Contains(r.users, k) {
				j = k
			}
			r.cycle = int(cfg.Keys[j].Fn.Param & 0xFF)
		}
		switch {
		case len(r.users) > 0:
			r.src = srcMouse
			onMouse = append(onMouse, r)
		case r.macro != nil:
			r.src = srcFlash
			inFlash = append(inFlash, r)
		}
	}
	for i, e := range t.entries {
		m := e.Macro
		inLib = append(inLib, macroRow{src: srcLibrary, lib: i, macro: &m, class: flash.SlotValid, cycle: e.Cycle})
	}
	return onMouse, inFlash, inLib
}

func (t *Macros) rows(c *Context) []macroRow {
	a, b, l := t.macroRows(c)
	rows := slices.Concat(a, b, l)
	t.cursor = min(t.cursor, max(0, len(rows)-1))
	return rows
}

func (t *Macros) selected(c *Context) (macroRow, bool) {
	rows := t.rows(c)
	if t.cursor < len(rows) {
		return rows[t.cursor], true
	}
	return macroRow{}, false
}

// holds reports whether one of rows holds e's macro.
func holds(rows []macroRow, e library.Entry) bool {
	return slices.ContainsFunc(rows, func(r macroRow) bool {
		return r.macro != nil && r.macro.Name == e.Macro.Name && slices.Equal(r.macro.Events, e.Macro.Events)
	})
}

func (t *Macros) Update(c *Context, msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	if !t.asked {
		t.asked = true
		cmds = append(cmds, t.loadLibrary())
	}
	switch msg := msg.(type) {
	case macroLibMsg:
		cmds = append(cmds, t.libraryLoaded(msg))
	case macroSavedMsg:
		if msg.err != nil {
			cmds = append(cmds, Problem(fmt.Errorf("the library was not saved: %w", msg.err)))
		}
	case macroFileMsg:
		cmds = append(cmds, t.fileDone(msg))
	case macroReadMsg:
		t.reading = false
		if msg.err != nil {
			cmds = append(cmds, Problem(fmt.Errorf("could not read the macro slot at %v, so the review cannot plan its macro yet: %w", msg.extents[0], msg.err)))
		}
	case SnapshotMsg:
		was, loaded := t.ready, t.image
		t.ready, t.image = msg.Snapshot.State == session.Ready, msg.Snapshot.Image
		if t.ready && (!was || t.image != loaded) {
			cmds = append(cmds, t.readBodies(c))
		}
	case tea.KeyPressMsg:
		cmds = append(cmds, t.key(c, msg))
	}
	return tea.Batch(cmds...)
}

func (t *Macros) key(c *Context, k tea.KeyPressMsg) tea.Cmd {
	rows := t.rows(c)
	r, ok := t.selected(c)
	switch k.String() {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = max(0, min(len(rows)-1, t.cursor+1))
	case "home", "g":
		t.cursor = 0
	case "end", "G":
		t.cursor = max(0, len(rows)-1)
	case "o":
		return ToggleLabels
	case "n":
		return OpenDialog(newMacroEditor(t, c, macroRow{src: srcNew, slot: -1}))
	case "enter":
		if ok {
			return t.edit(c, r)
		}
	case "B":
		if ok {
			if err := usable(r); err != nil {
				return Problem(err)
			}
			return OpenDialog(newMacroBinder(t, c, nil, r.entry(), bindSlot(r)))
		}
	case "s":
		if ok && r.src != srcLibrary {
			if err := usable(r); err != nil {
				return Problem(err)
			}
			cmd, _ := t.keep(r.entry(), "")
			return cmd
		}
	case "x", "delete":
		if ok && r.src == srcLibrary {
			return t.remove(r.lib)
		}
	case "d":
		if ok && r.src == srcPending {
			return t.drop(c, r.slot)
		}
	case "i":
		return t.importPrompt()
	case "e":
		return t.exportPrompt(c)
	}
	return nil
}

func usable(r macroRow) error {
	if r.macro != nil {
		return nil
	}
	if r.class == flash.SlotUnknown {
		return fmt.Errorf("the macro in slot %d was never read; r reloads", r.slot)
	}
	return fmt.Errorf("the macro in slot %d is %s; n starts a new one", r.slot, r.class)
}

// bindSlot is the slot a macro is bound to first when it is bound again:
// its own button when that button runs it.
func bindSlot(r macroRow) int {
	switch r.src {
	case srcPending:
		return r.slot
	case srcMouse:
		if slices.Contains(r.users, r.slot) {
			return r.slot
		}
		if len(r.users) > 0 {
			return r.users[0]
		}
	}
	return -1
}

func (t *Macros) edit(c *Context, r macroRow) tea.Cmd {
	if err := usable(r); err != nil {
		return Problem(err)
	}
	return OpenDialog(newMacroEditor(t, c, r))
}

func (t *Macros) drop(c *Context, slot int) tea.Cmd {
	s, ok := c.Pending.Get(SlotKey(slot))
	if !ok {
		return nil
	}
	if err := guardedDrop(c, s); err != nil {
		return Problem(err)
	}
	c.Pending.Drop(SlotKey(slot))
	return Notice("Dropped the pending edit of " + buttonWhere(c.Model(), slot) + ".")
}

func (t *Macros) Hints(c *Context) []key.Binding {
	out := []key.Binding{hint("enter", "edit"), hint("n", "new"), hint("B", "bind to button")}
	if r, ok := t.selected(c); ok {
		switch r.src {
		case srcPending:
			out = append(out, hint("d", "drop edit"))
		case srcLibrary:
			out = append(out, hint("x", "delete"))
		}
		if r.src != srcLibrary && t.lib != nil {
			out = append(out, hint("s", "save to library"))
		}
	}
	if t.lib != nil {
		out = append(out, hint("i", "import"), hint("e", "export"))
	}
	return append(out, hint("o", flipOS(c.OS).String()+" labels"))
}

// macroSideBySide is the width from which the list and the detail share
// the row, as on the Buttons tab.
const macroSideBySide = 100

func (t *Macros) View(c *Context, w, h int) string {
	if c.Model() == nil {
		return ""
	}
	r, ok := t.selected(c)
	switch {
	case w >= macroSideBySide:
		detailW := min(46, w*2/5)
		listW := w - detailW - 1
		var detail []string
		if ok {
			detail = t.detail(c, r, detailW)
		}
		list := t.list(c, listW, h)
		lines := make([]string, max(len(list), len(detail)))
		for i := range lines {
			var l, d string
			if i < len(list) {
				l = list[i]
			}
			if i < len(detail) {
				d = detail[i]
			}
			lines[i] = fit(l, listW, c.Glyphs.Ellipsis) + " " + d
		}
		return strings.Join(lines, "\n")
	case ok:
		detail := t.detail(c, r, w)
		list := t.list(c, w, max(5, h-len(detail)-1))
		return strings.Join(append(append(list, ""), detail...), "\n")
	}
	return strings.Join(t.list(c, w, h), "\n")
}

var macroHeads = []string{"Name", "Events", "Bytes", "Repeat", "Runs on"}

func (t *Macros) list(c *Context, w, h int) []string {
	st := c.Styles
	onMouse, inFlash, inLib := t.macroRows(c)
	title := fmt.Sprintf(" %s  %d on the mouse, %d unbound, %s", st.Bold.Render("Macros"), len(onMouse), len(inFlash), t.libCount())
	var cells [][]string
	nameW, repeatW := 8, 6
	for _, r := range slices.Concat(onMouse, inFlash, inLib) {
		cs := t.cells(c, r, onMouse)
		cells = append(cells, cs)
		nameW, repeatW = max(nameW, ansi.StringWidth(cs[0])), max(repeatW, ansi.StringWidth(cs[3]))
	}
	widths := []int{min(nameW, 20), 6, 5, min(repeatW, 19), 0}
	head := []string{fit(title, w-14, c.Glyphs.Ellipsis) + st.Faint.Render("  labels: "+c.OS.String()),
		st.Faint.Render(buttonCells(w, "  ", widths, c.Glyphs.Ellipsis, macroHeads...))}
	var body []string
	focus, n := -1, 0
	section := func(name string, rows []macroRow, none string) {
		body = append(body, " "+st.Bold.Render(name))
		if len(rows) == 0 {
			body = append(body, st.Faint.Render("  "+none))
		}
		for _, r := range rows {
			mark := "  "
			if n == t.cursor {
				mark, focus = c.Glyphs.Cursor+" ", len(body)
			}
			line := buttonCells(w, mark, widths, c.Glyphs.Ellipsis, cells[n]...)
			switch {
			case n == t.cursor:
				line = st.Selected.Render(line)
			case r.src == srcPending:
				line = st.Warn.Render(line)
			case r.macro == nil:
				line = st.Faint.Render(line)
			}
			body = append(body, line)
			n++
		}
	}
	section("On the mouse", onMouse, "no button runs a macro")
	section("Unbound in flash", inFlash, "none among the slots read")
	libNone := "empty: s saves a macro here, i imports a file"
	switch {
	case t.lib == nil:
		libNone = "not available in this session"
	case t.libErr != nil:
		libNone = "could not be read: " + plain(t.libErr)
	case !t.loaded:
		libNone = "loading"
	}
	section("Library", inLib, libNone)
	room := max(1, h-len(head))
	if len(body) <= room {
		t.offset = 0
		return append(head, body...)
	}
	room--
	t.offset = listOffset(max(0, focus), t.offset, room, len(body))
	end := min(len(body), t.offset+room)
	out := append(head, body[t.offset:end]...)
	return append(out, st.Faint.Render(fmt.Sprintf("  lines %d-%d of %d", t.offset+1, end, len(body))))
}

func (t *Macros) libCount() string {
	switch {
	case t.lib == nil:
		return "no library"
	case !t.loaded:
		return "library loading"
	}
	return fmt.Sprintf("%d in the library", len(t.entries))
}

// cells are the columns of r's line; onMouse are the rows of the macros on
// the mouse, which a library row is compared with.
func (t *Macros) cells(c *Context, r macroRow, onMouse []macroRow) []string {
	where := t.where(c, r)
	if r.src == srcLibrary && holds(onMouse, r.entry()) {
		where = "also on the mouse"
	}
	if r.macro == nil {
		name := "(" + r.class.String() + " body)"
		if r.class == flash.SlotUnknown {
			name = "(not read)"
		}
		return []string{name, "", "", cycleCell(r.cycle), where}
	}
	n := len(r.macro.Events)
	return []string{shownName(r.macro.Name), strconv.Itoa(n), strconv.Itoa(macroBytes(n)), cycleCell(r.cycle), where}
}

func cycleCell(cycle int) string {
	if cycle == 0 {
		return ""
	}
	return cycleText(cycle)
}

// macroBytes is the size of a body of n events.
func macroBytes(n int) int { return 33 + 5*n }

func (t *Macros) where(c *Context, r macroRow) string {
	m := c.Model()
	switch r.src {
	case srcPending:
		return buttonWhere(m, r.slot) + ", pending"
	case srcMouse:
		var names []string
		for _, k := range r.users {
			names = append(names, buttonWhere(m, k))
		}
		return strings.Join(names, ", ")
	case srcFlash:
		return "slot " + strconv.Itoa(r.slot) + ", no button"
	}
	return ""
}

func (t *Macros) detail(c *Context, r macroRow, w int) []string {
	st := c.Styles
	row := func(k, v string) []string { return wrap(v, w-1, fmt.Sprintf(" %-8s ", k), strings.Repeat(" ", 10)) }
	var title string
	switch {
	case r.macro != nil:
		title = strconv.Quote(shownName(r.macro.Name))
	default:
		title = "Slot " + strconv.Itoa(r.slot)
	}
	switch r.src {
	case srcMouse, srcPending:
		title += " " + c.Glyphs.Bullet + " macro slot " + strconv.Itoa(r.slot)
	case srcFlash:
		title += " " + c.Glyphs.Bullet + " slot " + strconv.Itoa(r.slot) + ", unbound"
	case srcLibrary:
		title += " " + c.Glyphs.Bullet + " library"
	}
	out := []string{" " + st.Bold.Render(title)}
	if r.macro == nil {
		return append(out, row("state", usable(r).Error())...)
	}
	ev := row("events", eventsText(c, r.macro.Events))
	if len(ev) > 3 {
		ev = append(ev[:2], ansi.Truncate(ev[2]+" "+c.Glyphs.Ellipsis, w-1, c.Glyphs.Ellipsis))
	}
	out = append(out, ev...)
	n := len(r.macro.Events)
	out = append(out, row("size", fmt.Sprintf("%d of %d events, %d of %d bytes", n, mouse.MaxMacroEvents, macroBytes(n), mouse.MacroSize))...)
	switch r.src {
	case srcPending:
		pv := macroPreview(c, mouse.SetMacro{Slot: r.slot, Macro: *r.macro, Cycle: r.cycle})
		if pv.err != nil {
			out = append(out, row("refused", macroRefusal(pv.err))...)
		} else {
			out = append(out, row("pending", "bound to "+buttonWhere(c.Model(), r.slot)+" (d drops it)")...)
			out = append(out, row("tier", gateNote(c, pv.tier))...)
		}
	case srcMouse:
		tier, _ := c.Tier(mouse.FeatureMacro)
		out = append(out, row("tier", gateNote(c, tier))...)
	case srcLibrary:
		if t.lib != nil {
			out = append(out, row("file", t.lib.Path)...)
		}
	}
	return out
}

// eventsText lists events as arrows, key names and delays: ↓ for a press,
// ↑ for a release.
func eventsText(c *Context, evs []mouse.Event) string {
	parts := make([]string, len(evs))
	for i, e := range evs {
		parts[i] = eventArrow(c, e.Press) + e.Stroke.Name(c.OS) + " " + strconv.Itoa(int(e.Delay)) + "ms"
	}
	return strings.Join(parts, ", ")
}

func eventArrow(c *Context, press bool) string {
	if press {
		return dilArrows(c, "↓", "v")
	}
	return dilArrows(c, "↑", "^")
}
