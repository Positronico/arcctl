package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
)

// pickSection is one group of the function picker (§7.4).
type pickSection uint8

const (
	secSystem pickSection = iota
	secSpecial
	secMedia
	secCombo
	secMacro
	pickSections
)

var pickLabels = [pickSections]string{"button.group.system", "button.group.special", "kbd_key.media", "button.group.combo", "button.group.macro"}

// pickEntry is one function a list section offers.
type pickEntry struct {
	name   string
	info   string
	search string
	edit   mouse.Edit
	// extra are features the entry needs beyond its section's; a tier
	// below Verified tags the entry.
	extra []mouse.Feature
	// macHidden is set, on the mac tables, for an entry the web app leaves
	// out on macOS.
	macHidden bool
	// where says where a macro comes from; newMacro is the entry that opens
	// the editor on a new one.
	where    string
	newMacro bool
}

// macHiddenPresets and macHiddenMedia are what the web app leaves out of
// its lists on macOS (§7.4): three presets and nine media keys.
var (
	macHiddenPresets = []string{"diy1", "diy2", "diy4"}
	macHiddenMedia   = []uint16{0x0183, 0x018A, 0x0192, 0x0194, 0x0223, 0x0224, 0x0225, 0x0226, 0x0227}
)

// pickList is the cursor and filter of one list.
type pickList struct {
	cursor, offset int
	filter         []rune
	filtering      bool
}

// key moves the cursor over n entries or edits the filter; it reports
// whether it used k.
func (l *pickList) key(k tea.KeyPressMsg, n int) bool {
	s := k.String()
	if l.filtering {
		switch s {
		case "enter":
			l.filtering = false
		case "esc":
			l.filter, l.filtering = nil, false
		case "backspace":
			if len(l.filter) > 0 {
				l.filter = l.filter[:len(l.filter)-1]
			}
		case "ctrl+u":
			l.filter = nil
		case "up":
			l.cursor = max(0, l.cursor-1)
		case "down":
			l.cursor = max(0, min(n-1, l.cursor+1))
		default:
			if k.Text == "" {
				return false
			}
			l.filter = append(l.filter, []rune(k.Text)...)
			l.cursor, l.offset = 0, 0
		}
		return true
	}
	switch s {
	case "up", "k":
		l.cursor = max(0, l.cursor-1)
	case "down", "j":
		l.cursor = max(0, min(n-1, l.cursor+1))
	case "pgup":
		l.cursor = max(0, l.cursor-10)
	case "pgdown":
		l.cursor = max(0, min(n-1, l.cursor+10))
	case "home", "g":
		l.cursor = 0
	case "end", "G":
		l.cursor = max(0, n-1)
	case "/":
		l.filtering = true
	case "esc":
		if len(l.filter) == 0 {
			return false
		}
		l.filter, l.cursor, l.offset = nil, 0, 0
	default:
		return false
	}
	return true
}

func (l *pickList) matches(texts ...string) bool {
	if len(l.filter) == 0 {
		return true
	}
	f := strings.ToLower(string(l.filter))
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), f) {
			return true
		}
	}
	return false
}

func (l *pickList) filterLine(c *Context) string {
	switch {
	case l.filtering:
		return "filter: " + string(l.filter) + "_"
	case len(l.filter) > 0:
		return "filter: " + string(l.filter) + c.Styles.Faint.Render("  (esc clears)")
	}
	return c.Styles.Faint.Render("/ filters")
}

// buttonPicker is the function picker of one button: it stages the chosen
// function as the button's pending edit.
type buttonPicker struct {
	tab   *Buttons
	row   buttonRow
	sec   pickSection
	lists [pickSections]pickList
	comp  composer
}

// newButtonPicker opens on the button's function, pending or on the mouse:
// the entry that sets it, or else the composer when it can build it.
func newButtonPicker(b *Buttons, c *Context, r buttonRow) *buttonPicker {
	p := &buttonPicker{tab: b, row: r}
	composed := p.comp.prefill(c, r.slot)
	secs := p.sections(c)
	if len(secs) == 0 {
		return p
	}
	p.sec = secs[0]
	if cur, ok := slotEdit(c, r.slot); ok {
		for _, s := range secs {
			if i := slices.IndexFunc(p.entries(c, s), func(e pickEntry) bool { return sameEdit(e.edit, cur) }); i >= 0 {
				p.sec, p.lists[s].cursor = s, i
				return p
			}
		}
	}
	if composed && slices.Contains(secs, secCombo) {
		p.sec = secCombo
	}
	return p
}

// slotEdit is the edit that would give slot what it does now, pending or on
// the mouse.
func slotEdit(c *Context, slot int) (mouse.Edit, bool) {
	if s, ok := c.Pending.Get(SlotKey(slot)); ok {
		return s.Edit, true
	}
	return deviceEdit(c, slot)
}

// deviceEdit is the edit that would give slot what it does on the mouse.
func deviceEdit(c *Context, slot int) (mouse.Edit, bool) {
	cfg := c.Config()
	fn, ok := slotDeviceFn(c, slot)
	if !ok {
		return nil, false
	}
	if e, ok := deviceMacro(cfg, slot, fn); ok {
		return e, true
	}
	switch {
	case fn.Type != mouse.TypeShortcut:
		return mouse.SetKey{Slot: slot, Fn: fn}, true
	case cfg.Slots[slot] != flash.SlotValid:
		return nil, false
	}
	combo := cfg.Shortcuts[slot]
	if len(combo) == 1 && combo[0].Kind == keys.KindConsumer {
		return mouse.SetMedia{Slot: slot, Usage: combo[0].Value}, true
	}
	return mouse.SetShortcut{Slot: slot, Combo: combo}, true
}

// deviceMacro is the edit that binds slot to the macro in its own slot, as
// the mouse does now; false for any other binding.
func deviceMacro(cfg *mouse.Config, slot int, fn mouse.KeyFn) (mouse.SetMacro, bool) {
	m := cfg.Macros[slot]
	if fn.Type != mouse.TypeMacro || int(fn.Param>>8) != slot || cfg.MacroClass[slot] != flash.SlotValid || m == nil {
		return mouse.SetMacro{}, false
	}
	return mouse.SetMacro{Slot: slot, Macro: *m, Cycle: int(fn.Param & 0xFF)}, true
}

func sameEdit(a, b mouse.Edit) bool {
	switch x := a.(type) {
	case mouse.SetShortcut:
		y, ok := b.(mouse.SetShortcut)
		return ok && x.Slot == y.Slot && slices.Equal(x.Combo, y.Combo)
	case mouse.SetMacro:
		y, ok := b.(mouse.SetMacro)
		return ok && x.Slot == y.Slot && x.Cycle == y.Cycle && x.Macro.Name == y.Macro.Name && slices.Equal(x.Macro.Events, y.Macro.Events)
	case mouse.SetKey, mouse.SetMedia:
		return a == b
	}
	return false
}

// sections are the groups this model and the run's flags show.
func (p *buttonPicker) sections(c *Context) []pickSection {
	var out []pickSection
	for s := range pickSections {
		if len(p.entries(c, s)) > 0 || s == secCombo && c.Visible(mouse.FeatureShortcut) {
			out = append(out, s)
		}
	}
	return out
}

func (p *buttonPicker) entries(c *Context, s pickSection) []pickEntry {
	slot, os := p.row.slot, p.tab.osFor(c)
	var out []pickEntry
	switch s {
	case secSystem:
		for _, e := range systemFns {
			if c.Visible(e.feature) {
				out = append(out, pickEntry{name: buttonLabel(e.label), info: buttonLabel(e.group), edit: mouse.SetKey{Slot: slot, Fn: e.fn}})
			}
		}
	case secSpecial:
		if c.Visible(mouse.FeatureShortcut) {
			for _, pr := range keys.Presets(os) {
				combo := pr.Combo()
				out = append(out, pickEntry{name: pr.Label, info: combo.Format(os), edit: mouse.SetShortcut{Slot: slot, Combo: combo},
					macHidden: os == keys.Mac && slices.Contains(macHiddenPresets, pr.ID)})
			}
		}
		for _, e := range specialFns {
			if c.Visible(e.feature) {
				out = append(out, pickEntry{name: buttonLabel(e.label), edit: mouse.SetKey{Slot: slot, Fn: e.fn}, extra: []mouse.Feature{e.feature}})
			}
		}
	case secMedia:
		if c.Visible(mouse.FeatureMedia) {
			for _, u := range catalog.MouseMedia() {
				out = append(out, pickEntry{name: u.Label, info: fmt.Sprintf("0x%04X", u.Code), search: u.Name,
					edit: mouse.SetMedia{Slot: slot, Usage: u.Code}, macHidden: os == keys.Mac && slices.Contains(macHiddenMedia, u.Code)})
			}
		}
	case secMacro:
		if t := p.tab.macros; t != nil && c.Visible(mouse.FeatureMacro) {
			out = append(out, pickEntry{name: "New macro" + c.Glyphs.Ellipsis, info: "opens the editor", newMacro: true})
			out = append(out, macroEntries(c, t, slot)...)
		}
	}
	return out
}

// macroEntries are the macros the Macro group offers for slot, each once:
// those on the mouse, pending ones included, the valid bodies no binding
// runs, then the library's.
func macroEntries(c *Context, t *Macros, slot int) []pickEntry {
	onMouse, inFlash, inLib := t.macroRows(c)
	var seen []library.Entry
	var out []pickEntry
	for _, r := range slices.Concat(onMouse, inFlash, inLib) {
		if r.macro == nil {
			continue
		}
		e := r.entry()
		if slices.ContainsFunc(seen, e.Equal) {
			continue
		}
		seen = append(seen, e)
		where := t.where(c, r)
		if r.src == srcLibrary {
			where = "library"
		}
		out = append(out, pickEntry{name: shownName(e.Macro.Name), info: cycleText(e.Cycle), search: where, where: where,
			edit: mouse.SetMacro{Slot: slot, Macro: e.Macro, Cycle: e.Cycle}})
	}
	return out
}

func (p *buttonPicker) filtered(c *Context, s pickSection) []pickEntry {
	l := &p.lists[s]
	all := p.entries(c, s)
	out := all[:0]
	for _, e := range all {
		if l.matches(e.name, e.info, e.search) {
			out = append(out, e)
		}
	}
	l.cursor = min(l.cursor, max(0, len(out)-1))
	return out
}

func (p *buttonPicker) current(c *Context) pickSection {
	secs := p.sections(c)
	if len(secs) > 0 && !slices.Contains(secs, p.sec) {
		p.sec = secs[0]
	}
	return p.sec
}

func (p *buttonPicker) move(c *Context, delta int) {
	secs := p.sections(c)
	if len(secs) == 0 {
		return
	}
	i := max(0, slices.Index(secs, p.current(c)))
	p.sec = secs[(i+delta+len(secs))%len(secs)]
}

func (p *buttonPicker) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if len(p.sections(c)) == 0 {
		return CloseDialog
	}
	switch s := p.current(c); s {
	case secCombo:
		if cmd, used := p.comp.key(c, p, k); used {
			return cmd
		}
	default:
		entries := p.filtered(c, s)
		if p.lists[s].key(k, len(entries)) {
			return nil
		}
		if k.String() == "enter" && len(entries) > 0 {
			return p.choose(c, entries[p.lists[s].cursor])
		}
	}
	switch k.String() {
	case "tab", "right", "l":
		p.move(c, 1)
	case "shift+tab", "left", "h":
		p.move(c, -1)
	case "o":
		return ToggleLabels
	case "esc", "q":
		return CloseDialog
	}
	return nil
}

// choose stages the function of e, or opens the macro editor for a new
// macro. A macro goes through the Macros tab, which asks about the other
// buttons whose macros carry its name and reads its slot.
func (p *buttonPicker) choose(c *Context, e pickEntry) tea.Cmd {
	if e.newMacro {
		return p.newMacro(c)
	}
	if m, ok := e.edit.(mouse.SetMacro); ok {
		return p.tab.macros.bind(c, m, Problem, func() tea.Cmd { return closeDialog(p) })
	}
	return p.tab.stage(c, p.row, e.edit)
}

// newMacro puts the Macros tab's editor on a new macro in place of the
// picker; b there binds it, starting on this button.
func (p *buttonPicker) newMacro(c *Context) tea.Cmd {
	ed := newMacroEditor(p.tab.macros, c, macroRow{src: srcNew, slot: -1})
	ed.bindTo, ed.title = p.row.slot, "New macro for "+p.row.where()
	return tea.Sequence(closeDialog(p), OpenDialog(ed))
}

func (p *buttonPicker) Hints(c *Context) []key.Binding {
	s := p.current(c)
	if s == secCombo {
		return p.comp.hints()
	}
	if p.lists[s].filtering {
		return []key.Binding{hint("type", "filter"), hint("↑/↓", "move"), hint("enter", "done"), hint("esc", "clear")}
	}
	return []key.Binding{hint("/", "filter"), hint("tab", "group"), hint("↑/↓", "move"),
		hint("o", flipOS(p.tab.osFor(c)).String()+" labels"), hint("enter", "choose"), hint("esc", "back")}
}

func (p *buttonPicker) View(c *Context, w, h int) string {
	st := c.Styles
	os := p.tab.osFor(c)
	now, _ := slotFunction(c.Model(), c.Config(), p.row.slot, os)
	head := fmt.Sprintf(" %s  now %s", st.Bold.Render(buttonTitle(c, p.row)), now)
	if s, ok := c.Pending.Get(SlotKey(p.row.slot)); ok {
		head += "  pending " + editText(c.Model(), s.Edit, os)
	}
	lines := []string{fit(head, w-14, c.Glyphs.Ellipsis) + st.Faint.Render("  labels: "+os.String()), p.sectionBar(c)}
	var body, foot []string
	switch s := p.current(c); s {
	case secCombo:
		ks := p.comp.keysFor(os)
		foot = p.comp.foot(c, p, ks, w)
		body = p.comp.body(c, p, ks, w, h-len(lines)-len(foot)-1)
	default:
		entries := p.filtered(c, s)
		l := &p.lists[s]
		if len(entries) > 0 {
			foot = p.previewLines(c, entries[l.cursor], w)
		} else {
			foot = []string{"  Nothing matches the filter."}
		}
		body = []string{"  " + l.filterLine(c)}
		if os == keys.Mac && (s == secSpecial || s == secMedia) {
			body = append(body, st.Faint.Render("  On macOS the web app does not offer the dimmed entries."))
		}
		body = append(body, p.listLines(c, entries, l, w, h-len(lines)-len(body)-len(foot)-1)...)
	}
	lines = append(lines, body...)
	for len(lines) < h-len(foot) {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, foot...), "\n")
}

func (p *buttonPicker) sectionBar(c *Context) string {
	cur := p.current(c)
	var parts []string
	for _, s := range p.sections(c) {
		name := buttonLabel(pickLabels[s])
		if s == cur {
			parts = append(parts, c.Styles.TabOn.Render("["+name+"]"))
		} else {
			parts = append(parts, " "+name+" ")
		}
	}
	return " " + strings.Join(parts, " ")
}

func (p *buttonPicker) listLines(c *Context, entries []pickEntry, l *pickList, w, room int) []string {
	room = max(1, room)
	nameW := 10
	for _, e := range entries {
		nameW = max(nameW, ansi.StringWidth(e.name))
	}
	nameW = min(nameW, 24)
	more := len(entries) > room
	if more {
		room--
	}
	l.offset = listOffset(l.cursor, l.offset, room, len(entries))
	end := min(len(entries), l.offset+room)
	var out []string
	for i := l.offset; i < end; i++ {
		e := entries[i]
		mark := "  "
		if i == l.cursor {
			mark = c.Glyphs.Cursor + " "
		}
		tag := tierTag(c, e.extra)
		switch {
		case e.macHidden:
			tag = "not on macOS web app"
		case tag == "":
			tag = e.where
		}
		line := buttonCells(w, " "+mark, []int{nameW, 22, 12}, c.Glyphs.Ellipsis, e.name, e.info, tag)
		switch {
		case i == l.cursor:
			line = c.Styles.Selected.Render(line)
		case e.macHidden:
			line = c.Styles.Faint.Render(line)
		}
		out = append(out, line)
	}
	if more {
		out = append(out, c.Styles.Faint.Render(fmt.Sprintf("   %d-%d of %d", l.offset+1, end, len(entries))))
	}
	return out
}

// tierTag is the tier of the features an entry needs beyond its section's,
// when that is below Verified.
func tierTag(c *Context, fs []mouse.Feature) string {
	if len(fs) == 0 {
		return ""
	}
	t := catalog.Verified
	for _, f := range fs {
		ft, _ := c.Tier(f)
		t = min(t, ft)
	}
	if t == catalog.Verified {
		return ""
	}
	return t.String()
}

// previewLines say what staging entry would do: its tier and web-app
// warnings, or why the planner refuses it.
func (p *buttonPicker) previewLines(c *Context, entry pickEntry, w int) []string {
	e := entry.edit
	line := func(s string) []string { return wrap(s, w-1, "  ", "    ") }
	var out []string
	if m, ok := e.(mouse.SetMacro); ok || entry.newMacro {
		if entry.newMacro {
			out = line("opens the macro editor on a new macro; b there binds it to " + p.row.where())
		} else {
			out = bindPreview(c, m, p.row, line)
		}
		if len(out) > pickPreviewRows {
			out = append(out[:pickPreviewRows-1], "  "+c.Glyphs.Ellipsis)
		}
		return out
	}
	pv := previewSlot(c, p.row.slot, e)
	switch {
	case pv.ops == 0 && (pv.err == nil || doesNow(c, p.row.slot, e)):
		out = line("no change: the button already does " + editText(c.Model(), e, p.tab.osFor(c)))
	case pv.err != nil:
		out = line("refused: " + editRefusal(pv.err))
	default:
		out = line("tier " + gateNote(c, pv.tier))
		if pv.unread != nil {
			out = append(out, line(fmt.Sprintf("its body at %v was never read; choosing this reads it", *pv.unread))...)
		}
		switch {
		case entry.macHidden:
			out = append(out, line("web app: macOS does not offer this; the web app shows it only on Windows")...)
		case len(pv.warns) == 0:
			out = append(out, line("web app: shows and edits this")...)
		}
		for _, wn := range pv.warns {
			out = append(out, line("web app: "+wn.Text)...)
		}
	}
	if len(out) > pickPreviewRows {
		out = append(out[:pickPreviewRows-1], "  "+c.Glyphs.Ellipsis)
	}
	return out
}

const pickPreviewRows = 4

// modifierBits are the composer's toggles 1 to 8 (§7.5).
var modifierBits = [8]keys.Modifier{keys.LCtrl, keys.LShift, keys.LAlt, keys.LMeta, keys.RCtrl, keys.RShift, keys.RAlt, keys.RMeta}

const maxModifiers = mouse.MaxShortcutKeys - 1

// composerKeys are the keys a combo can end with: the key table's kind-1
// usages and the Menu key, in the table's order.
var composerKeys = func() []keys.Key {
	var out []keys.Key
	for _, k := range keys.All() {
		if k.Kind == keys.KindKey || k.Kind == keys.KindMenu {
			out = append(out, k)
		}
	}
	return out
}()

// composer builds a combo of up to 4 modifiers, pressed in the order they
// were toggled, and one key from a searchable list (D3: no live capture).
type composer struct {
	mods []keys.Modifier
	list pickList
	err  string
}

// prefill starts from the combo slot holds, pending or on the mouse, and
// reports whether it is one the composer can build.
func (m *composer) prefill(c *Context, slot int) bool {
	var combo keys.Combo
	if s, ok := c.Pending.Get(SlotKey(slot)); ok {
		if e, ok := s.Edit.(mouse.SetShortcut); ok {
			combo = e.Combo
		}
	} else if cfg := c.Config(); cfg != nil && cfg.Keys[slot].Fn.Type == mouse.TypeShortcut {
		combo = cfg.Shortcuts[slot]
	}
	n := len(combo)
	if n == 0 || n > maxModifiers+1 {
		return false
	}
	last := combo[n-1]
	i := slices.IndexFunc(composerKeys, func(k keys.Key) bool { return k.Stroke() == last })
	if i < 0 {
		return false
	}
	var mods []keys.Modifier
	for _, s := range combo[:n-1] {
		mod := keys.Modifier(s.Value)
		if s.Kind != keys.KindModifier || !slices.Contains(modifierBits[:], mod) || slices.Contains(mods, mod) {
			return false
		}
		mods = append(mods, mod)
	}
	m.mods, m.list.cursor = mods, i
	return true
}

func (m *composer) toggle(mod keys.Modifier) {
	if i := slices.Index(m.mods, mod); i >= 0 {
		m.mods = slices.Delete(m.mods, i, i+1)
		return
	}
	if len(m.mods) == maxModifiers {
		m.err = fmt.Sprintf("Up to %d modifiers and 1 key.", maxModifiers)
		return
	}
	m.mods = append(m.mods, mod)
}

// keyChars are the unshifted characters of the keys whose labels show the
// shifted one, so that a search finds them by either.
var keyChars = map[string]string{"Equal": "=", "Backslash": `\`, "Semicolon": ";", "IntlBackslash": `\`}

func (m *composer) keysFor(os keys.OS) []keys.Key {
	var out []keys.Key
	for _, k := range composerKeys {
		if m.list.matches(k.Name(os), k.Win, k.Mac, k.Code, keyChars[k.Code]) {
			out = append(out, k)
		}
	}
	m.list.cursor = min(m.list.cursor, max(0, len(out)-1))
	return out
}

// combo is the composed combo with the highlighted key; nil without one.
func (m *composer) combo(ks []keys.Key) keys.Combo {
	if len(ks) == 0 {
		return nil
	}
	out := make(keys.Combo, 0, len(m.mods)+1)
	for _, mod := range m.mods {
		out = append(out, mod.Stroke())
	}
	return append(out, ks[m.list.cursor].Stroke())
}

func (m *composer) key(c *Context, p *buttonPicker, k tea.KeyPressMsg) (tea.Cmd, bool) {
	m.err = ""
	ks := m.keysFor(p.tab.osFor(c))
	if m.list.key(k, len(ks)) {
		return nil, true
	}
	s := k.String()
	switch {
	case len(s) == 1 && s[0] >= '1' && s[0] <= '8':
		m.toggle(modifierBits[s[0]-'1'])
	case s == "0":
		m.mods = nil
	case s == "enter":
		combo := m.combo(ks)
		if combo == nil {
			m.err = "Pick a key."
			return nil, true
		}
		return p.tab.stage(c, p.row, mouse.SetShortcut{Slot: p.row.slot, Combo: combo}), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *composer) hints() []key.Binding {
	if m.list.filtering {
		return []key.Binding{hint("type", "filter keys"), hint("↑/↓", "move"), hint("enter", "done"), hint("esc", "clear")}
	}
	return []key.Binding{hint("1-8", "modifier"), hint("/", "filter keys"), hint("tab", "group"), hint("↑/↓", "key"),
		hint("0", "no modifiers"), hint("o", "labels"), hint("enter", "stage"), hint("esc", "back")}
}

func (m *composer) body(c *Context, p *buttonPicker, ks []keys.Key, w, h int) []string {
	os := p.tab.osFor(c)
	out := []string{fmt.Sprintf("  Modifiers, pressed in the order toggled (up to %d):", maxModifiers)}
	for row := range 2 {
		line := " "
		for i := row * 4; i < row*4+4; i++ {
			box := "[ ]"
			if slices.Contains(m.mods, modifierBits[i]) {
				box = "[x]"
			}
			line += fit(fmt.Sprintf(" %s %d %s", box, i+1, modifierBits[i].Name(os)), 14, c.Glyphs.Ellipsis)
		}
		if row == 1 {
			if tag := tierTag(c, []mouse.Feature{mouse.FeatureShortcutRightModifier}); tag != "" {
				line += "  " + c.Styles.Warn.Render("right side: "+tag)
			}
		}
		out = append(out, line)
	}
	out = append(out, "  Key   "+m.list.filterLine(c))
	return append(out, p.keyLines(c, ks, w, h-len(out))...)
}

func (m *composer) foot(c *Context, p *buttonPicker, ks []keys.Key, w int) []string {
	combo := m.combo(ks)
	switch {
	case m.err != "":
		return []string{"  " + c.Styles.Bad.Render(m.err)}
	case combo == nil:
		return []string{"  Combo  (no key matches the filter)"}
	}
	out := []string{"  Combo  " + c.Styles.Bold.Render(comboText(combo, p.tab.osFor(c)))}
	return append(out, p.previewLines(c, pickEntry{edit: mouse.SetShortcut{Slot: p.row.slot, Combo: combo}}, w)...)
}

func (p *buttonPicker) keyLines(c *Context, ks []keys.Key, w, room int) []string {
	room = max(1, room)
	l := &p.comp.list
	os := p.tab.osFor(c)
	more := len(ks) > room
	if more {
		room--
	}
	l.offset = listOffset(l.cursor, l.offset, room, len(ks))
	end := min(len(ks), l.offset+room)
	menu := tierTag(c, []mouse.Feature{mouse.FeatureShortcutMenu})
	var out []string
	for i := l.offset; i < end; i++ {
		k := ks[i]
		mark := "  "
		if i == l.cursor {
			mark = c.Glyphs.Cursor + " "
		}
		tag := ""
		if k.Kind == keys.KindMenu {
			tag = menu
		}
		line := buttonCells(w, "   "+mark, []int{16, 16, 12}, c.Glyphs.Ellipsis, k.Name(os), k.Code, tag)
		if i == l.cursor {
			line = c.Styles.Selected.Render(line)
		}
		out = append(out, line)
	}
	if more {
		out = append(out, c.Styles.Faint.Render(fmt.Sprintf("     %d-%d of %d", l.offset+1, end, len(ks))))
	}
	return out
}
