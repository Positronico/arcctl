package tui

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
)

// macroField is the field the editor reads typed text into.
type macroField uint8

const (
	fieldNone macroField = iota
	fieldName
	fieldCount
	fieldDelay
)

// The editor's rows: the name, the repeat mode, then one per event.
const (
	rowName = iota
	rowRepeat
	rowEvents
)

// delayStep is what ←/→ change a delay by, in ms.
const delayStep = 10

// macroEditor edits one macro: its name, repeat mode and events. It writes
// nothing: b stages the macro for a button, s keeps it in the library.
type macroEditor struct {
	tab *Macros
	// from names the library entry the macro came from; saving replaces it.
	from string
	// bindTo is the slot the button chooser starts on; -1 for none.
	bindTo int
	title  string
	name   string
	cycle  int
	count  int // the ×N count, kept while another mode is chosen
	events []mouse.Event
	clean  library.Entry
	row    int
	offset int
	field  macroField
	typed  []rune
	note   string
	bad    bool
}

func newMacroEditor(t *Macros, c *Context, r macroRow) *macroEditor {
	ed := &macroEditor{tab: t, bindTo: bindSlot(r), cycle: 1, count: 1, title: "New macro"}
	if r.macro != nil {
		e := r.entry()
		ed.name, ed.cycle, ed.events = e.Macro.Name, e.Cycle, slices.Clone(e.Macro.Events)
		if ed.cycle <= 250 {
			ed.count = ed.cycle
		}
	}
	switch r.src {
	case srcMouse:
		ed.title = "Macro in slot " + strconv.Itoa(r.slot)
	case srcPending:
		ed.title = "Pending macro for " + buttonWhere(c.Model(), r.slot)
	case srcFlash:
		ed.title = "Unbound macro in slot " + strconv.Itoa(r.slot)
	case srcLibrary:
		ed.title, ed.from = "Library macro", ed.name
	}
	ed.clean = ed.entry()
	if len(ed.events) > 0 {
		ed.row = rowEvents
	}
	return ed
}

func (ed *macroEditor) entry() library.Entry {
	return library.Entry{Macro: mouse.Macro{Name: ed.name, Events: slices.Clone(ed.events)}, Cycle: ed.cycle}
}

func (ed *macroEditor) dirty() bool { return !ed.entry().Equal(ed.clean) }

// sel is the selected event; -1 on the name and repeat rows.
func (ed *macroEditor) sel() int {
	if i := ed.row - rowEvents; i >= 0 && i < len(ed.events) {
		return i
	}
	return -1
}

func (ed *macroEditor) say(text string, bad bool) { ed.note, ed.bad = text, bad }

func (ed *macroEditor) Update(c *Context, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if ed.field != fieldNone {
			ed.typeText(msg.Content)
		}
	case tea.KeyPressMsg:
		ed.note = ""
		if ed.field != fieldNone {
			ed.typing(msg)
			return nil
		}
		return ed.key(c, msg)
	}
	return nil
}

func (ed *macroEditor) typing(k tea.KeyPressMsg) {
	switch k.String() {
	case "esc":
		ed.field, ed.typed = fieldNone, nil
	case "enter":
		ed.commit()
	case "backspace":
		if n := len(ed.typed); n > 0 {
			ed.typed = ed.typed[:n-1]
		}
	case "ctrl+u":
		ed.typed = nil
	default:
		ed.typeText(k.Text)
	}
}

// typeText adds text to the field: a name goes through the sanitiser, a
// number keeps its digits.
func (ed *macroEditor) typeText(text string) {
	if text == "" {
		return
	}
	switch ed.field {
	case fieldName:
		joined := string(ed.typed) + text
		kept := stripName(joined)
		switch {
		case len(kept) > mouse.MaxNameLen:
			ed.say(fmt.Sprintf("A name holds at most %d bytes.", mouse.MaxNameLen), true)
		case kept != joined:
			ed.say("Punctuation, spaces and control characters are left out of names, as the web app does.", false)
		}
		ed.typed = []rune(cutName(kept))
	default:
		limit := 3
		if ed.field == fieldDelay {
			limit = 5
		}
		for _, r := range text {
			if r >= '0' && r <= '9' && len(ed.typed) < limit {
				ed.typed = append(ed.typed, r)
			}
		}
	}
}

func (ed *macroEditor) commit() {
	s := string(ed.typed)
	f := ed.field
	ed.field, ed.typed = fieldNone, nil
	switch f {
	case fieldName:
		ed.name = sanitizeName(s)
		if ed.name == "" {
			ed.say("A macro needs a name.", true)
		}
	case fieldCount:
		n, err := strconv.Atoi(s)
		if err != nil {
			return
		}
		clamped := min(max(n, 1), 250)
		if clamped != n {
			ed.say(fmt.Sprintf("A count is 1 to 250; %d became %d.", n, clamped), true)
		}
		ed.count, ed.cycle = clamped, clamped
	case fieldDelay:
		n, err := strconv.Atoi(s)
		if i := ed.sel(); err == nil && i >= 0 {
			ed.events[i].Delay = ed.clampDelay(n)
		}
	}
}

func (ed *macroEditor) clampDelay(n int) uint16 {
	d := min(max(n, library.MinDelay), library.MaxDelay)
	if d != n {
		ed.say(fmt.Sprintf("A delay is %d to %d ms; %d became %d.", library.MinDelay, library.MaxDelay, n, d), true)
	}
	return uint16(d)
}

// repeatModes are the repeat modes in the order of the web app's table,
// with the ×N count in place of the counted mode.
func (ed *macroEditor) repeatModes() []int {
	var out []int
	for _, o := range keys.RepeatOptions() {
		if o.Mode == keys.RepeatCount {
			out = append(out, ed.count)
			continue
		}
		out = append(out, int(o.Value))
	}
	return out
}

func (ed *macroEditor) moveMode(delta int) {
	modes := ed.repeatModes()
	i := slices.Index(modes, ed.cycle)
	if i < 0 {
		i = 0
	}
	ed.cycle = modes[(i+delta+len(modes))%len(modes)]
}

func (ed *macroEditor) key(c *Context, k tea.KeyPressMsg) tea.Cmd {
	last := rowEvents + len(ed.events) - 1
	i := ed.sel()
	switch k.String() {
	case "up", "k":
		ed.row = max(0, ed.row-1)
	case "down", "j":
		ed.row = min(max(last, rowRepeat), ed.row+1)
	case "home", "g":
		ed.row = 0
	case "end", "G":
		ed.row = max(last, rowRepeat)
	case "enter":
		switch {
		case ed.row == rowName:
			ed.field, ed.typed = fieldName, []rune(ed.name)
		case ed.row == rowRepeat && ed.cycle <= 250:
			ed.field, ed.typed = fieldCount, []rune(strconv.Itoa(ed.cycle))
		case ed.row == rowRepeat:
			ed.say("←/→ picks the mode; the count is typed in the ×N mode.", false)
		case i >= 0:
			ed.field, ed.typed = fieldDelay, []rune(strconv.Itoa(int(ed.events[i].Delay)))
		}
	case "left", "h", "right", "l":
		delta := 1
		if s := k.String(); s == "left" || s == "h" {
			delta = -1
		}
		switch {
		case ed.row == rowRepeat:
			ed.moveMode(delta)
		case i >= 0:
			ed.events[i].Delay = ed.clampDelay(int(ed.events[i].Delay) + delta*delayStep)
		}
	case "space":
		if i >= 0 {
			ed.events[i].Press = !ed.events[i].Press
		}
	case "i", "insert":
		return OpenDialog(newMacroKeyPicker(ed, -1, false))
	case "c":
		return OpenDialog(newMacroKeyPicker(ed, -1, true))
	case "e":
		if i >= 0 {
			return OpenDialog(newMacroKeyPicker(ed, i, false))
		}
	case "x", "delete":
		if i >= 0 {
			ed.events = slices.Delete(ed.events, i, i+1)
			ed.row = min(ed.row, max(rowEvents+len(ed.events)-1, rowRepeat))
		}
	case "K", "shift+up":
		if i > 0 {
			ed.events[i-1], ed.events[i] = ed.events[i], ed.events[i-1]
			ed.row--
		}
	case "J", "shift+down":
		if i >= 0 && i < len(ed.events)-1 {
			ed.events[i+1], ed.events[i] = ed.events[i], ed.events[i+1]
			ed.row++
		}
	case "t":
		return ed.tidy(c)
	case "b":
		if err := ed.ready(); err != nil {
			ed.say(err.Error(), true)
			return nil
		}
		return OpenDialog(newMacroBinder(ed.tab, c, ed, ed.entry(), ed.bindTo))
	case "s":
		if err := ed.ready(); err != nil {
			ed.say(err.Error(), true)
			return nil
		}
		e := ed.entry()
		cmd, kept := ed.tab.keep(e, ed.from)
		if kept {
			ed.from, ed.clean = e.Macro.Name, e
		}
		return cmd
	case "o":
		return ToggleLabels
	case "esc", "q":
		if !ed.dirty() {
			return CloseDialog
		}
		return OpenDialog(&Confirm{
			Title: "Close the editor",
			Body:  []string{"The changes to this macro are neither saved in the library nor pending for a button. Drop them?"},
			Yes:   func(string) tea.Cmd { return closeDialog(ed) },
		})
	}
	return nil
}

// ready says why the macro cannot be bound or saved yet.
func (ed *macroEditor) ready() error {
	switch {
	case ed.name == "":
		return errors.New("name the macro first: enter on Name")
	case len(ed.events) == 0:
		return errors.New("add an event first: i inserts a key, c a click")
	}
	return nil
}

// insert adds evs after the selected event, or at the end when none is
// selected, and selects the last one added.
func (ed *macroEditor) insert(evs ...mouse.Event) error {
	if n := len(ed.events) + len(evs); n > mouse.MaxMacroEvents {
		return fmt.Errorf("a macro holds at most %d events; this would make %d", mouse.MaxMacroEvents, n)
	}
	at := len(ed.events)
	if i := ed.sel(); i >= 0 {
		at = i + 1
	}
	ed.events = slices.Insert(ed.events, at, evs...)
	ed.row = rowEvents + at + len(evs) - 1
	return nil
}

// tidyReport is what tidy finds: why an event is unpaired, and the releases
// the presses still held need, the last press first.
type tidyReport struct {
	flags   map[int]string
	missing []keys.Stroke
}

// tidyScan pairs every press with the next release of the same stroke.
func tidyScan(evs []mouse.Event) tidyReport {
	r := tidyReport{flags: map[int]string{}}
	var held []int
	for i, e := range evs {
		at := slices.IndexFunc(held, func(j int) bool { return evs[j].Stroke == e.Stroke })
		switch {
		case e.Press && at >= 0:
			r.flags[i] = flagAgain
		case e.Press:
			held = append(held, i)
		case at < 0:
			r.flags[i] = flagOrphan
		default:
			held = slices.Delete(held, at, at+1)
		}
	}
	for j := len(held) - 1; j >= 0; j-- {
		r.flags[held[j]] = flagHeld
		r.missing = append(r.missing, evs[held[j]].Stroke)
	}
	return r
}

// Why tidyScan flags an event.
const (
	flagAgain  = "pressed again before its release"
	flagOrphan = "released without a press"
	flagHeld   = "never released"
	flagShort  = "a delay under 10 ms"
)

// shortDelays are the events whose delay is under the planner's minimum,
// as the web app's recorder leaves them.
func shortDelays(evs []mouse.Event) []int {
	var out []int
	for i, e := range evs {
		if e.Delay < library.MinDelay {
			out = append(out, i)
		}
	}
	return out
}

// tidyAdvice says what to do about the flags tidy cannot fix itself.
func tidyAdvice(r tidyReport) []string {
	n := map[string]int{}
	for _, f := range r.flags {
		n[f]++
	}
	var out []string
	if k := n[flagAgain]; k > 0 {
		out = append(out, plural(k, "event is", "events are")+" "+flagAgain+": x deletes one of the presses, or i adds a release between them.")
	}
	if k := n[flagOrphan]; k > 0 {
		out = append(out, plural(k, "release has", "releases have")+" no press before "+map[bool]string{true: "it", false: "them"}[k == 1]+
			": x deletes the selected event.")
	}
	return out
}

// tidy offers what it can fix: the releases the held presses need, added
// at the end, and the delays under 10 ms, raised to 10 ms; the other flags
// get advice.
func (ed *macroEditor) tidy(c *Context) tea.Cmd {
	r := tidyScan(ed.events)
	short := shortDelays(ed.events)
	if len(r.flags) == 0 && len(short) == 0 {
		ed.say("Every press has its release, and every delay is at least 10 ms.", false)
		return nil
	}
	advice := tidyAdvice(r)
	var fixes []string
	missing := r.missing
	if n := len(ed.events) + len(missing); len(missing) > 0 && n > mouse.MaxMacroEvents {
		advice = append(advice, fmt.Sprintf("The missing releases would make %d events, over the %d a macro holds.", n, mouse.MaxMacroEvents))
		missing = nil
	}
	if len(missing) > 0 {
		fixes = append(fixes, fmt.Sprintf("add %s at the end, %d ms apart", plural(len(missing), "missing release", "missing releases"), library.MinDelay))
	}
	if len(short) > 0 {
		fixes = append(fixes, fmt.Sprintf("raise %s to %d ms", plural(len(short), "delay under 10 ms", "delays under 10 ms"), library.MinDelay))
	}
	if len(fixes) == 0 {
		ed.say(strings.Join(advice, " "), true)
		return nil
	}
	flags := map[int][]string{}
	for i, f := range r.flags {
		flags[i] = append(flags[i], f)
	}
	for _, i := range short {
		flags[i] = append(flags[i], fmt.Sprintf("a delay of %d ms", ed.events[i].Delay))
	}
	idx := slices.Sorted(maps.Keys(flags))
	var body []string
	for _, i := range idx {
		e := ed.events[i]
		body = append(body, fmt.Sprintf("  event %d, %s %s: %s", i+1, pressWord(e.Press), e.Stroke.Name(c.OS), strings.Join(flags[i], "; ")))
	}
	body = append(body, "")
	body = append(body, advice...)
	q := strings.Join(fixes, ", and ")
	body = append(body, strings.ToUpper(q[:1])+q[1:]+"?")
	return OpenDialog(&Confirm{
		Title: "Tidy the events",
		Body:  body,
		Yes: func(string) tea.Cmd {
			for _, i := range short {
				ed.events[i].Delay = library.MinDelay
			}
			for _, s := range missing {
				ed.events = append(ed.events, mouse.Event{Stroke: s, Delay: library.MinDelay})
			}
			var done []string
			if len(missing) > 0 {
				ed.row = rowEvents + len(ed.events) - 1
				done = append(done, "added "+plural(len(missing), "release", "releases"))
			}
			if len(short) > 0 {
				done = append(done, "raised "+plural(len(short), "delay", "delays")+fmt.Sprintf(" to %d ms", library.MinDelay))
			}
			d := strings.Join(done, " and ")
			ed.say(strings.ToUpper(d[:1])+d[1:]+".", false)
			return nil
		},
	})
}

func pressWord(press bool) string {
	if press {
		return "press"
	}
	return "release"
}

func (ed *macroEditor) Hints(c *Context) []key.Binding {
	switch ed.field {
	case fieldName:
		return []key.Binding{hint("type", "name"), hint("ctrl+u", "clear"), hint("enter", "done"), hint("esc", "cancel")}
	case fieldCount, fieldDelay:
		return []key.Binding{hint("digits", "value"), hint("enter", "done"), hint("esc", "cancel")}
	}
	return []key.Binding{hint("i", "insert key"), hint("c", "click"), hint("b", "bind to button"), hint("t", "tidy"),
		hint("s", "save to library"), hint("x", "delete"), hint("J/K", "move"), hint("space", "press/release"), hint("←/→", "delay"),
		hint("enter", "name, count or delay"), hint("e", "change key"), hint("o", flipOS(c.OS).String()+" labels"),
		hint("?", "all keys"), hint("esc", "close")}
}

// Helps reports that ? lists the editor's keys: always but while it takes
// typed text.
func (ed *macroEditor) Helps() bool { return ed.field == fieldNone }

func (ed *macroEditor) View(c *Context, w, h int) string {
	st := c.Styles
	n := len(ed.events)
	counts := fmt.Sprintf("%d/%d events %s %d/%d bytes", n, mouse.MaxMacroEvents, c.Glyphs.Bullet, macroBytes(n), mouse.MacroSize)
	head := " " + st.Bold.Render(ed.title) + "  " + counts
	if ed.dirty() {
		head += "  " + st.Warn.Render("changed")
	}
	lines := []string{fit(head, w-14, c.Glyphs.Ellipsis) + st.Faint.Render("  labels: "+c.OS.String())}
	mark := func(row int) string { return cursorMark(c, row == ed.row) }
	name := shownName(ed.name)
	switch {
	case ed.field == fieldName:
		name = shownName(string(ed.typed)) + "_"
	case ed.name == "":
		name = st.Faint.Render("(none: enter names it)")
	}
	nameLine := mark(rowName) + "Name    " + name
	if ed.field == fieldName {
		nameLine += st.Faint.Render(fmt.Sprintf("   %d/%d bytes", len(string(ed.typed)), mouse.MaxNameLen))
	}
	repeat := cycleText(ed.cycle)
	switch {
	case ed.field == fieldCount:
		repeat = "×" + string(ed.typed) + "_" + st.Faint.Render("   1-250")
	case ed.cycle > 250:
		repeat += fmt.Sprintf(" (%d)", ed.cycle)
	}
	repeatLine := mark(rowRepeat) + "Repeat  " + repeat
	if ed.row == rowRepeat && ed.field == fieldNone {
		repeatLine += st.Faint.Render("   ←/→ mode, enter count")
	}
	for i, l := range []string{nameLine, repeatLine} {
		if i == ed.row && ed.field == fieldNone {
			l = st.Selected.Render(l)
		}
		lines = append(lines, " "+l)
	}
	report := tidyScan(ed.events)
	status := ed.status(c, report)
	table := ed.table(c, report, w, h-len(lines)-len(status))
	lines = append(lines, table...)
	for len(lines) < h-len(status) {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, status...), "\n")
}

var macroEventHeads = []string{"#", "Action", "Key", "Delay after", ""}

func (ed *macroEditor) table(c *Context, r tidyReport, w, room int) []string {
	st := c.Styles
	widths := []int{3, 10, 18, 12, 0}
	out := []string{st.Faint.Render(buttonCells(w, "   ", widths, c.Glyphs.Ellipsis, macroEventHeads...))}
	if len(ed.events) == 0 {
		return append(out, st.Faint.Render("   No events yet: i inserts a key press, release or tap, c a click."))
	}
	room = max(1, room-1)
	more := len(ed.events) > room
	if more {
		room--
	}
	cur := max(0, ed.sel())
	ed.offset = listOffset(cur, ed.offset, room, len(ed.events))
	end := min(len(ed.events), ed.offset+room)
	for i := ed.offset; i < end; i++ {
		e := ed.events[i]
		delay := strconv.Itoa(int(e.Delay)) + " ms"
		if ed.field == fieldDelay && i == ed.sel() {
			delay = string(ed.typed) + "_ ms"
		}
		var why []string
		if f, ok := r.flags[i]; ok {
			why = append(why, f)
		}
		if e.Delay < library.MinDelay {
			why = append(why, flagShort)
		}
		flag := ""
		if len(why) > 0 {
			flag = "! " + strings.Join(why, "; ")
		}
		line := buttonCells(w, " "+cursorMark(c, ed.row == rowEvents+i), widths, c.Glyphs.Ellipsis,
			strconv.Itoa(i+1), eventArrow(c, e.Press)+" "+pressWord(e.Press), e.Stroke.Name(c.OS), delay, flag)
		switch {
		case ed.row == rowEvents+i && ed.field == fieldNone:
			line = st.Selected.Render(line)
		case flag != "":
			line = st.Warn.Render(line)
		}
		out = append(out, line)
	}
	if more {
		out = append(out, st.Faint.Render(fmt.Sprintf("   %d-%d of %d", ed.offset+1, end, len(ed.events))))
	}
	return out
}

func cursorMark(c *Context, on bool) string {
	if on {
		return c.Glyphs.Cursor + " "
	}
	return "  "
}

// status is the line under the table: the last note, or what keeps the
// macro from being bound, or the unpaired events.
func (ed *macroEditor) status(c *Context, r tidyReport) []string {
	st := c.Styles
	switch {
	case ed.note != "" && ed.bad:
		return []string{" " + st.Bad.Render(ed.note)}
	case ed.note != "":
		return []string{" " + st.Info.Render(ed.note)}
	case len(r.flags) > 0 || len(shortDelays(ed.events)) > 0:
		var parts []string
		if n := len(r.flags); n > 0 {
			parts = append(parts, plural(n, "event is", "events are")+" unpaired")
		}
		if n := len(shortDelays(ed.events)); n > 0 {
			parts = append(parts, plural(n, "delay is", "delays are")+" under 10 ms, the least arcctl writes")
		}
		return []string{" " + st.Warn.Render(strings.Join(parts, "; ")+": t tidies.")}
	}
	if err := ed.ready(); err != nil {
		return []string{" " + st.Faint.Render(strings.ToUpper(err.Error()[:1])+err.Error()[1:]+".")}
	}
	return []string{" " + st.Faint.Render("b binds it to a button (the review writes it); s keeps it in the library.")}
}
