package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
)

// macroAction is what the key picker adds: a tap is a press and its
// release.
type macroAction uint8

const (
	actTap macroAction = iota
	actPress
	actRelease
	macroActions
)

var macroActionNames = [macroActions]string{"Tap", "Press", "Release"}

// macroStroke is one entry of the event key list.
type macroStroke struct {
	stroke keys.Stroke
	search []string
	group  string
}

// macroStrokes are what an event presses or releases (D3): the modifiers,
// the composer's keys and the mouse buttons.
var macroStrokes = func() []macroStroke {
	var out []macroStroke
	for _, m := range modifierBits {
		s := macroStroke{stroke: m.Stroke(), search: []string{m.String()}, group: "modifier"}
		if k, ok := keys.ByValue(keys.KindModifier, uint8(m)); ok {
			s.search = append(s.search, k.Code)
		}
		out = append(out, s)
	}
	for _, k := range composerKeys {
		out = append(out, macroStroke{stroke: k.Stroke(), search: []string{k.Code, k.Win, k.Mac, keyChars[k.Code]}, group: "key"})
	}
	for v := uint16(1); v <= 0x10; v <<= 1 {
		out = append(out, macroStroke{stroke: keys.Stroke{Kind: keys.KindMouse, Value: v}, search: []string{"mouse", "click"}, group: "mouse button"})
	}
	return out
}()

// macroKeyPicker picks the key or mouse button of an event: it inserts a
// tap, a press or a release after the selected event, or changes the key
// of one event.
type macroKeyPicker struct {
	ed      *macroEditor
	replace int // the event whose key changes; -1 inserts
	act     macroAction
	list    pickList
	err     string
}

func newMacroKeyPicker(ed *macroEditor, replace int, clicks bool) *macroKeyPicker {
	p := &macroKeyPicker{ed: ed, replace: replace}
	var cur keys.Stroke
	switch {
	case replace >= 0:
		e := ed.events[replace]
		cur = e.Stroke
		p.act = actRelease
		if e.Press {
			p.act = actPress
		}
	case clicks:
		cur = keys.Stroke{Kind: keys.KindMouse, Value: 1}
	case len(ed.events) > 0:
		cur = ed.events[max(0, ed.sel())].Stroke
	}
	if i := slices.IndexFunc(macroStrokes, func(s macroStroke) bool { return s.stroke == cur }); i >= 0 {
		p.list.cursor = i
	}
	return p
}

func (p *macroKeyPicker) entries(c *Context) []macroStroke {
	var out []macroStroke
	for _, s := range macroStrokes {
		if p.list.matches(append([]string{s.stroke.Name(c.OS), s.group}, s.search...)...) {
			out = append(out, s)
		}
	}
	p.list.cursor = min(p.list.cursor, max(0, len(out)-1))
	return out
}

// events are what choosing s adds.
func (p *macroKeyPicker) events(s keys.Stroke) []mouse.Event {
	switch p.act {
	case actPress:
		return []mouse.Event{{Press: true, Stroke: s, Delay: library.MinDelay}}
	case actRelease:
		return []mouse.Event{{Stroke: s, Delay: library.MinDelay}}
	}
	return []mouse.Event{{Press: true, Stroke: s, Delay: library.MinDelay}, {Stroke: s, Delay: library.MinDelay}}
}

func (p *macroKeyPicker) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	p.err = ""
	es := p.entries(c)
	if p.list.key(k, len(es)) {
		return nil
	}
	switch k.String() {
	case "tab", "right", "l":
		if p.replace < 0 {
			p.act = (p.act + 1) % macroActions
		}
	case "shift+tab", "left", "h":
		if p.replace < 0 {
			p.act = (p.act + macroActions - 1) % macroActions
		}
	case "o":
		return ToggleLabels
	case "enter":
		if len(es) == 0 {
			return nil
		}
		s := es[p.list.cursor].stroke
		if p.replace >= 0 {
			p.ed.events[p.replace].Stroke = s
			return CloseDialog
		}
		if err := p.ed.insert(p.events(s)...); err != nil {
			p.err = err.Error()
			return nil
		}
		return CloseDialog
	case "esc", "q":
		return CloseDialog
	}
	return nil
}

func (p *macroKeyPicker) Hints(c *Context) []key.Binding {
	if p.list.filtering {
		return []key.Binding{hint("type", "filter"), hint("↑/↓", "move"), hint("enter", "done"), hint("esc", "clear")}
	}
	out := []key.Binding{hint("enter", "choose"), hint("/", "filter")}
	if p.replace < 0 {
		out = append(out, hint("tab", "tap/press/release"))
	}
	return append(out, hint("↑/↓", "move"), hint("o", flipOS(c.OS).String()+" labels"), hint("?", "all keys"), hint("esc", "back"))
}

// Helps reports that ? lists the picker's keys, except while it filters.
func (p *macroKeyPicker) Helps() bool { return !p.list.filtering }

func (p *macroKeyPicker) View(c *Context, w, h int) string {
	st := c.Styles
	name := strconv.Quote(shownName(p.ed.name))
	if p.ed.name == "" {
		name = "the new macro"
	}
	var title string
	switch i := p.ed.sel(); {
	case p.replace >= 0:
		title = fmt.Sprintf("Change the key of event %d of %s", p.replace+1, name)
	case i >= 0:
		title = fmt.Sprintf("Insert into %s after event %d", name, i+1)
	default:
		title = "Insert at the end of " + name
	}
	lines := []string{fit(" "+st.Bold.Render(title), w-14, c.Glyphs.Ellipsis) + st.Faint.Render("  labels: "+c.OS.String())}
	var bar []string
	for a := range macroActions {
		switch {
		case a == p.act:
			bar = append(bar, st.TabOn.Render("["+macroActionNames[a]+"]"))
		case p.replace < 0:
			bar = append(bar, " "+macroActionNames[a]+" ")
		}
	}
	lines = append(lines, "  "+strings.Join(bar, " "), "  "+p.list.filterLine(c))
	es := p.entries(c)
	var foot []string
	switch {
	case p.err != "":
		foot = []string{"  " + st.Bad.Render(p.err)}
	case len(es) == 0:
		foot = []string{"  Nothing matches the filter."}
	case p.replace >= 0:
		foot = []string{"  Becomes  " + eventsText(c, []mouse.Event{{Press: p.act == actPress, Stroke: es[p.list.cursor].stroke,
			Delay: p.ed.events[p.replace].Delay}})}
	default:
		foot = []string{"  Adds  " + eventsText(c, p.events(es[p.list.cursor].stroke))}
	}
	room := max(1, h-len(lines)-len(foot))
	more := len(es) > room
	if more {
		room--
	}
	p.list.offset = listOffset(p.list.cursor, p.list.offset, room, len(es))
	end := min(len(es), p.list.offset+room)
	for i := p.list.offset; i < end; i++ {
		s := es[i]
		line := buttonCells(w, "   "+cursorMark(c, i == p.list.cursor), []int{18, 14, 0}, c.Glyphs.Ellipsis,
			s.stroke.Name(c.OS), s.group, s.search[0])
		if i == p.list.cursor {
			line = st.Selected.Render(line)
		}
		lines = append(lines, line)
	}
	if more {
		lines = append(lines, st.Faint.Render(fmt.Sprintf("     %d-%d of %d", p.list.offset+1, end, len(es))))
	}
	for len(lines) < h-len(foot) {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, foot...), "\n")
}

// macroBinder picks the button a macro is bound to, and stages it there.
type macroBinder struct {
	tab    *Macros
	ed     *macroEditor // nil when the list opened it
	entry  library.Entry
	cursor int
	err    string
}

// newMacroBinder opens on slot's button or, without one, on the first
// button other than the left and right ones that is not guarded.
func newMacroBinder(t *Macros, c *Context, ed *macroEditor, e library.Entry, slot int) *macroBinder {
	d := &macroBinder{tab: t, ed: ed, entry: e, cursor: -1}
	rows := buttonRows(c.Model(), false)
	for i, r := range rows {
		if r.slot == slot {
			d.cursor = i
		}
	}
	for _, primary := range []bool{false, true} {
		for i, r := range rows {
			if d.cursor < 0 && (primary || r.slot > 1) && !lastLeftClick(c, r.slot) {
				d.cursor = i
			}
		}
	}
	d.cursor = max(0, d.cursor)
	return d
}

func (d *macroBinder) edit(slot int) mouse.SetMacro {
	return mouse.SetMacro{Slot: slot, Macro: d.entry.Macro, Cycle: d.entry.Cycle}
}

func (d *macroBinder) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	d.err = ""
	rows := buttonRows(c.Model(), false)
	switch k.String() {
	case "up", "k":
		d.cursor = max(0, d.cursor-1)
	case "down", "j":
		d.cursor = max(0, min(len(rows)-1, d.cursor+1))
	case "o":
		return ToggleLabels
	case "enter":
		if d.cursor >= len(rows) {
			return nil
		}
		fail := func(err error) tea.Cmd { d.err = err.Error(); return nil }
		return d.tab.bind(c, d.edit(rows[d.cursor].slot), fail, d.staged)
	case "esc", "q":
		return CloseDialog
	}
	return nil
}

// staged closes the binder, and the editor it came from, once the macro is
// staged.
func (d *macroBinder) staged() tea.Cmd {
	if d.ed == nil {
		return closeDialog(d)
	}
	d.ed.clean = d.entry
	return tea.Sequence(closeDialog(d), closeDialog(d.ed))
}

// bind stages e, first asking whether the other buttons whose macros carry
// e's name take its events too (T175). fail says why e cannot be staged;
// staged runs once it is.
func (t *Macros) bind(c *Context, e mouse.SetMacro, fail func(error) tea.Cmd, staged func() tea.Cmd) tea.Cmd {
	join, blocked := namesakes(c, e)
	if len(blocked) > 0 {
		return fail(errNamesakes(c, e, blocked))
	}
	stage := func(with ...mouse.SetMacro) tea.Cmd {
		cmd, err := t.stage(c, e, with...)
		if err != nil {
			return fail(err)
		}
		return tea.Sequence(staged(), cmd)
	}
	if len(join) > 0 {
		return OpenDialog(joinConfirm(c, e, join, func() tea.Cmd { return stage(join...) }))
	}
	return stage()
}

// joinConfirm offers to give e's events to the other buttons whose macro
// has e's name, as the web app does when a macro several buttons run
// changes; yes stages them.
func joinConfirm(c *Context, e mouse.SetMacro, join []mouse.SetMacro, yes func() tea.Cmd) *Confirm {
	names := make([]string, len(join))
	for i, j := range join {
		names[i] = buttonWhere(c.Model(), j.Slot)
	}
	who := strings.Join(names, ", ")
	return &Confirm{
		Title: "Same name on other buttons",
		Body: []string{
			fmt.Sprintf("%s runs another macro named %q. The web app keeps one macro per name, and so does arcctl.", who, shownName(e.Macro.Name)),
			fmt.Sprintf("Stage these events for %s too, each keeping its repeat mode? The review writes them all. To keep them apart, "+
				"answer n and rename this macro in the editor.", who),
		},
		Yes: func(string) tea.Cmd { return yes() },
	}
}

func (d *macroBinder) Hints(c *Context) []key.Binding {
	return []key.Binding{hint("enter", "stage"), hint("↑/↓", "button"), hint("o", flipOS(c.OS).String()+" labels"), hint("?", "all keys"), hint("esc", "back")}
}

func (d *macroBinder) Helps() bool { return true }

func (d *macroBinder) View(c *Context, w, h int) string {
	st := c.Styles
	title := fmt.Sprintf("Bind %q %s to a button", shownName(d.entry.Macro.Name), cycleText(d.entry.Cycle))
	lines := []string{" " + st.Bold.Render(title),
		st.Faint.Render(buttonCells(w, "   ", []int{12, 4, 0}, c.Glyphs.Ellipsis, "Button", "Slot", "Now"))}
	rows := buttonRows(c.Model(), false)
	for i, r := range rows {
		now, _ := slotFunction(c.Model(), c.Config(), r.slot, c.OS)
		if s, ok := c.Pending.Get(SlotKey(r.slot)); ok {
			now = "pending: " + editText(c.Model(), s.Edit, c.OS)
		}
		if lastLeftClick(c, r.slot) {
			now += " (guarded: the last Left Click)"
		}
		line := buttonCells(w, " "+cursorMark(c, i == d.cursor), []int{12, 4, 0}, c.Glyphs.Ellipsis, r.name, strconv.Itoa(r.slot), now)
		if i == d.cursor {
			line = st.Selected.Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	if d.cursor < len(rows) {
		lines = append(lines, d.preview(c, rows[d.cursor], w)...)
	}
	return strings.Join(lines, "\n")
}

func (d *macroBinder) preview(c *Context, r buttonRow, w int) []string {
	line := func(s string) []string { return wrap(s, w-1, "  ", "    ") }
	if d.err != "" {
		return line(c.Styles.Bad.Render("refused: " + d.err))
	}
	return bindPreview(c, d.edit(r.slot), r, line)
}

// bindPreview says what staging e on r would do: its tier, the macro slot
// it reads first and the web-app warnings, or why it is refused.
func bindPreview(c *Context, e mouse.SetMacro, r buttonRow, line func(string) []string) []string {
	if fn, ok := editFn(e); ok && losesLeftClick(c, r.slot, fn) {
		return line("refused: " + errLeftClick.Error())
	}
	if sanitizeName(e.Macro.Name) != e.Macro.Name {
		return line("refused: " + errOddName(e.Macro.Name).Error())
	}
	join, blocked := namesakes(c, e)
	if len(blocked) > 0 {
		return line("refused: " + errNamesakes(c, e, blocked).Error())
	}
	pv := macroPreview(c, e, join...)
	switch {
	case pv.err != nil:
		return line("refused: " + macroRefusal(pv.err))
	case pv.ops == 0 && len(join) == 0:
		return line("no change: " + r.where() + " already runs this macro")
	}
	ext, _ := mouse.MacroExtent(r.slot)
	out := line(fmt.Sprintf("writes the macro into slot %d (@%d), then binds %s", r.slot, ext.Addr, r.where()))
	for _, j := range join {
		out = append(out, line(fmt.Sprintf("%s runs a macro of the same name with other events; enter offers to give it these events too",
			buttonWhere(c.Model(), j.Slot)))...)
	}
	if users := macroSlotUsers(c, r.slot); len(users) > 0 {
		names := make([]string, len(users))
		for i, k := range users {
			names[i] = buttonWhere(c.Model(), k)
		}
		out = append(out, line(strings.Join(names, ", ")+" may run that slot now, so it is disabled while the body is rewritten, then bound again")...)
	}
	out = append(out, line("tier "+gateNote(c, pv.tier))...)
	if pv.unread != nil {
		out = append(out, line("the macro slot was never read in full; staging reads it")...)
	}
	for _, wn := range pv.warns {
		out = append(out, line("web app: "+wn.Text)...)
	}
	return out
}

// macroSlotUsers are the slots whose bindings on the mouse may run macro
// slot k: a macro binding counts as running its own slot and the one it
// names.
func macroSlotUsers(c *Context, k int) []int {
	cfg := c.Config()
	if cfg == nil {
		return nil
	}
	var out []int
	for j, b := range cfg.Keys {
		if b.Field.State == flash.OK && b.Fn.Type == mouse.TypeMacro && (j == k || int(b.Fn.Param>>8) == k) {
			out = append(out, j)
		}
	}
	return out
}
