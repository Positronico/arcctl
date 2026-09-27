package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

// Reader loads flash extents that were never read, as session.API.Read
// does.
type Reader func(ctx context.Context, extents ...flash.Extent) (session.Capture, error)

// Buttons is the Buttons tab (§7.4): each button of the model with its flash
// slot, what it does now, its tier and web-app warnings. enter opens the
// function picker, whose choice becomes a pending edit; nothing is written
// from here.
type Buttons struct {
	read    Reader
	macros  *Macros
	reading bool
	ready   bool
	all     bool
	cursor  int
	offset  int
}

// NewButtons builds the tab. read loads a body that was never read before
// an edit that rewrites it can be planned; nil leaves that to the user.
// macros is the Macros tab: the picker's Macro group offers its macros, its
// library and its editor, and it reads the macro slots of pending macros;
// nil leaves the group out.
func NewButtons(read Reader, macros *Macros) *Buttons { return &Buttons{read: read, macros: macros} }

const bodyReadTimeout = 30 * time.Second

var (
	errButtonsUnloaded = errors.New("nothing is loaded from the mouse yet")
	errLeftClick       = errors.New("this is the last button on Left Click; give another button Left Click first, so the mouse can always click")
	errHiddenSlot      = errors.New("this slot is not a button the web app shows; editing it is experimental: restart arcctl with --experimental")
)

type buttonsReadMsg struct {
	extents []flash.Extent
	err     error
}

func (b *Buttons) Title() string { return "Buttons" }

func (b *Buttons) Needs() []mouse.Feature {
	return []mouse.Feature{mouse.FeatureSystem, mouse.FeatureMedia, mouse.FeatureShortcut, mouse.FeatureMacro}
}

func (b *Buttons) Capturing() bool { return false }

// osFor is the OS whose labels and preset tables the screens use; o
// switches it for every screen (ToggleLabels).
func (b *Buttons) osFor(c *Context) keys.OS { return c.OS }

func flipOS(os keys.OS) keys.OS {
	if os == keys.Mac {
		return keys.Win
	}
	return keys.Mac
}

// buttonRow is one line of the list: a flash slot and the name of the
// button wired to it.
type buttonRow struct {
	slot  int
	name  string
	shown bool // the web app shows this button for the model
}

// buttonRows lists the model's buttons in the catalog's UI order, or with
// all every slot in slot order.
func buttonRows(m *catalog.Model, all bool) []buttonRow {
	if m == nil {
		return nil
	}
	var out []buttonRow
	if !all {
		for _, bt := range m.Buttons {
			if bt.Visible {
				out = append(out, buttonRow{bt.Slot, bt.Label, true})
			}
		}
		return out
	}
	for k := range mouse.Slots {
		r := buttonRow{slot: k, name: "Slot " + strconv.Itoa(k)}
		for _, bt := range m.Buttons {
			if bt.Slot == k && (bt.Visible || !r.shown) {
				r.name, r.shown = bt.Label, bt.Visible
			}
		}
		if !r.shown {
			r.name = "Slot " + strconv.Itoa(k)
		}
		out = append(out, r)
	}
	return out
}

// where names the row in a sentence.
func (r buttonRow) where() string {
	if !r.shown {
		return r.name
	}
	return fmt.Sprintf("%s (slot %d)", r.name, r.slot)
}

func (b *Buttons) rows(c *Context) []buttonRow {
	rows := buttonRows(c.Model(), b.all)
	b.cursor = min(b.cursor, max(0, len(rows)-1))
	return rows
}

// slotPreview is what planning an edit says about its slot.
type slotPreview struct {
	tier  catalog.Tier
	ops   int
	warns []mouse.Warning
	// unread is the body the edit rewrites that was never read; the preview
	// takes it as erased, and staging the edit reads it.
	unread *flash.Extent
	err    error
}

// previewSlot plans e for slot with the other pending edits, as the review
// will, and keeps what concerns the slot. When another edit is what fails,
// e is planned alone.
func previewSlot(c *Context, slot int, e mouse.Edit) slotPreview {
	m, im := c.Model(), c.Image()
	if m == nil || im == nil {
		return slotPreview{err: errButtonsUnloaded}
	}
	var pv slotPreview
	if ext, ok := editBody(e); ok {
		if _, known := im.Get(ext); !known {
			pv.unread = &ext
			im = im.Clone()
			for a := ext.Addr; a < ext.End(); a++ {
				if _, ok := im.Byte(a); !ok {
					_ = im.Set(a, []byte{0xFF})
				}
			}
		}
	}
	opt := c.MouseOptions()
	var others []mouse.Edit
	for _, s := range c.Pending.List() {
		if s.Key != SlotKey(slot) {
			others = append(others, s.Edit)
		}
	}
	p, err := mouse.PlanEdits(m, im, append(others, e), opt)
	if err != nil && len(others) > 0 {
		if alone, aerr := mouse.PlanEdits(m, im, []mouse.Edit{e}, opt); aerr == nil {
			p, err = alone, nil
		}
	}
	if err != nil {
		pv.err = err
		return pv
	}
	mine := map[int]bool{}
	pv.tier = catalog.Verified
	for _, op := range p.Ops {
		if slotOwns(slot, op.Extent) {
			mine[op.Seq] = true
			pv.ops++
			pv.tier = min(pv.tier, op.Tier)
		}
	}
	for _, w := range mouse.WebCompat(m, p) {
		if mine[w.Seq] {
			pv.warns = append(pv.warns, w)
		}
	}
	return pv
}

// slotOwns reports whether e is slot's binding or one of its bodies.
func slotOwns(slot int, e flash.Extent) bool {
	for _, table := range []func(int) (flash.Extent, bool){mouse.KeyFnExtent, mouse.ShortcutExtent, mouse.MacroExtent} {
		if s, ok := table(slot); ok && s.Contains(e) {
			return true
		}
	}
	return false
}

// editBody is the body e rewrites, which must be read before the review can
// plan e: a shortcut body, or a whole macro slot.
func editBody(e mouse.Edit) (flash.Extent, bool) {
	switch e := e.(type) {
	case mouse.SetShortcut:
		return mouse.ShortcutExtent(e.Slot)
	case mouse.SetMedia:
		return mouse.ShortcutExtent(e.Slot)
	case mouse.SetMacro:
		return mouse.MacroExtent(e.Slot)
	}
	return flash.Extent{}, false
}

var editIndexPrefix = regexp.MustCompile(`^Edit \d+: (edit refused: )?`)

// editRefusal is why the planner refused an edit, without its index.
func editRefusal(err error) string { return editIndexPrefix.ReplaceAllString(plain(err), "") }

// slotWarnings are the web-app warnings about what slot k holds now: the
// lint of a plan that would write its binding and body as they are.
func slotWarnings(c *Context, k int) []mouse.Warning {
	m, cfg := c.Model(), c.Config()
	if m == nil || cfg == nil || cfg.Keys[k].Field.State != flash.OK {
		return nil
	}
	bind, _ := mouse.KeyFnExtent(k)
	ops := []plan.Op{{Seq: 1, Extent: bind, New: cfg.Keys[k].Field.Raw}}
	if cfg.Keys[k].Fn.Type == mouse.TypeShortcut && cfg.Shortcuts[k] != nil {
		if body, err := mouse.EncodeShortcut(cfg.Shortcuts[k]); err == nil {
			ext, _ := mouse.ShortcutExtent(k)
			ops = append(ops, plan.Op{Seq: 2, Extent: flash.Extent{Addr: ext.Addr, Len: len(body)}, New: body})
		}
	}
	return mouse.WebCompat(m, plan.Plan{Ops: ops})
}

var leftClickFn = mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft}

func slotDeviceFn(c *Context, k int) (mouse.KeyFn, bool) {
	cfg := c.Config()
	if cfg == nil || cfg.Keys[k].Field.State != flash.OK {
		return mouse.KeyFn{}, false
	}
	return cfg.Keys[k].Fn, true
}

// slotPendingFn is slot k's key function once the pending edits are written.
func slotPendingFn(c *Context, k int) (mouse.KeyFn, bool) {
	if s, ok := c.Pending.Get(SlotKey(k)); ok {
		return editFn(s.Edit)
	}
	return slotDeviceFn(c, k)
}

// losesLeftClick reports whether giving slot k the function fn leaves no
// physical button on Left Click while the mouse has one now (I9), counting
// the pending edits as the planner will.
func losesLeftClick(c *Context, k int, fn mouse.KeyFn) bool {
	m := c.Model()
	if m == nil {
		return false
	}
	had, after := false, false
	for _, bt := range m.Buttons {
		if !bt.Visible {
			continue
		}
		if f, ok := slotDeviceFn(c, bt.Slot); ok && f == leftClickFn {
			had = true
		}
		f, ok := slotPendingFn(c, bt.Slot)
		if bt.Slot == k {
			f, ok = fn, true
		}
		if ok && f == leftClickFn {
			after = true
		}
	}
	return had && !after
}

// lastLeftClick reports that slot k holds the last Left Click.
func lastLeftClick(c *Context, k int) bool {
	f, ok := slotPendingFn(c, k)
	return ok && f == leftClickFn && losesLeftClick(c, k, mouse.KeyFn{})
}

// stage makes e the pending edit of slot k, or drops the slot's pending
// edit when e changes nothing. A body that was never read is read now so
// that the review can plan it.
func (b *Buttons) stage(c *Context, r buttonRow, e mouse.Edit) tea.Cmd {
	switch {
	case c.Mode == ModeReadOnly:
		return Problem(ErrReadOnlyMode)
	case c.Writing():
		return Problem(ErrWriting)
	}
	if fn, ok := editFn(e); ok && losesLeftClick(c, r.slot, fn) {
		return Problem(errLeftClick)
	}
	pv := previewSlot(c, r.slot, e)
	if pv.err != nil && !doesNow(c, r.slot, e) {
		return Problem(errors.New(editRefusal(pv.err)))
	}
	text := editText(c.Model(), e, b.osFor(c))
	where := r.where()
	if pv.ops == 0 {
		c.Pending.Drop(SlotKey(r.slot))
		return tea.Batch(CloseDialog, Notice(where+" already does "+text+"; nothing is pending for it."))
	}
	c.Pending.Stage(Staged{Key: SlotKey(r.slot), Desc: where + ": " + text, Edit: e})
	notice := "Pending: " + where + " " + c.Glyphs.Arrow + " " + text + ". a reviews and applies."
	if pv.unread != nil && c.Snapshot.State != session.Ready {
		notice += " Its body is read once the mouse is ready."
	}
	return tea.Batch(CloseDialog, Notice(notice), b.readBodies(c))
}

// readBodies reads the bodies that pending edits rewrite and that were
// never read, while the session is ready and no such read runs. The Macros
// tab, when there is one, reads the macro slots.
func (b *Buttons) readBodies(c *Context) tea.Cmd {
	im := c.Image()
	if b.reading || im == nil || c.Snapshot.State != session.Ready {
		return nil
	}
	var want []flash.Extent
	for _, s := range c.Pending.List() {
		if _, ok := s.Edit.(mouse.SetMacro); ok && b.macros != nil {
			continue
		}
		if ext, ok := editBody(s.Edit); ok {
			if _, known := im.Get(ext); !known {
				want = append(want, ext)
			}
		}
	}
	switch {
	case len(want) == 0:
		return nil
	case b.read == nil:
		return Problem(fmt.Errorf("the body at %v was never read; the review cannot plan its edit until it is", want[0]))
	}
	b.reading = true
	read := b.read
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bodyReadTimeout)
		defer cancel()
		_, err := read(ctx, want...)
		return buttonsReadMsg{want, err}
	}
}

// doesNow reports that e gives slot what it does on the mouse already.
func doesNow(c *Context, slot int, e mouse.Edit) bool {
	cur, ok := deviceEdit(c, slot)
	return ok && sameEdit(e, cur)
}

func (b *Buttons) drop(c *Context, r buttonRow) tea.Cmd {
	s, ok := c.Pending.Get(SlotKey(r.slot))
	if !ok {
		return nil
	}
	if err := guardedDrop(c, s); err != nil {
		return Problem(err)
	}
	c.Pending.Drop(SlotKey(r.slot))
	return Notice("Dropped the pending edit of " + r.where() + ".")
}

// guardedDrop refuses to drop s when the button would then leave the mouse
// with no Left Click (I9), counting the other pending edits.
func guardedDrop(c *Context, s Staged) error {
	slot, ok := editSlot(s.Edit)
	if !ok {
		return nil
	}
	if fn, ok := slotDeviceFn(c, slot); ok && losesLeftClick(c, slot, fn) {
		return errLeftClick
	}
	return nil
}

// editSlot is the slot a button edit changes.
func editSlot(e mouse.Edit) (int, bool) {
	switch e := e.(type) {
	case mouse.SetKey:
		return e.Slot, true
	case mouse.SetShortcut:
		return e.Slot, true
	case mouse.SetMedia:
		return e.Slot, true
	case mouse.SetMacro:
		return e.Slot, true
	}
	return 0, false
}

// buttonWhere names slot k in a sentence, by its button when the model
// shows one.
func buttonWhere(m *catalog.Model, k int) string {
	for _, r := range buttonRows(m, true) {
		if r.slot == k {
			return r.where()
		}
	}
	return "Slot " + strconv.Itoa(k)
}

func (b *Buttons) open(c *Context, r buttonRow) tea.Cmd {
	switch {
	case c.Config() == nil:
		return Problem(errButtonsUnloaded)
	case c.Writing():
		return Problem(ErrWriting)
	case lastLeftClick(c, r.slot):
		return Problem(errLeftClick)
	case !r.shown && !c.Visible(mouse.FeatureHiddenSlot):
		return Problem(errHiddenSlot)
	}
	return OpenDialog(newButtonPicker(b, c, r))
}

func (b *Buttons) Update(c *Context, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case buttonsReadMsg:
		b.reading = false
		if msg.err != nil {
			return Problem(fmt.Errorf("could not read the body at %v, so the review cannot plan its edit yet: %w", msg.extents[0], msg.err))
		}
		return nil
	case SnapshotMsg:
		was := b.ready
		b.ready = msg.Snapshot.State == session.Ready
		if b.ready && !was {
			return b.readBodies(c)
		}
		return nil
	case tea.KeyPressMsg:
		rows := b.rows(c)
		switch msg.String() {
		case "up", "k":
			b.cursor = max(0, b.cursor-1)
		case "down", "j":
			b.cursor = min(len(rows)-1, b.cursor+1)
		case "home", "g":
			b.cursor = 0
		case "end", "G":
			b.cursor = max(0, len(rows)-1)
		case "s":
			slot := -1
			if b.cursor < len(rows) {
				slot = rows[b.cursor].slot
			}
			b.all = !b.all
			b.cursor, b.offset = 0, 0
			for i, r := range buttonRows(c.Model(), b.all) {
				if r.slot == slot {
					b.cursor = i
				}
			}
		case "o":
			return ToggleLabels
		case "enter":
			if b.cursor < len(rows) {
				return b.open(c, rows[b.cursor])
			}
		case "d", "delete":
			if b.cursor < len(rows) {
				return b.drop(c, rows[b.cursor])
			}
		}
	}
	return nil
}

func (b *Buttons) Hints(c *Context) []key.Binding {
	out := []key.Binding{hint("enter", "change")}
	rows := b.rows(c)
	if b.cursor < len(rows) {
		if _, ok := c.Pending.Get(SlotKey(rows[b.cursor].slot)); ok {
			out = append(out, hint("d", "drop edit"))
		}
	}
	all := "all 16 slots"
	if b.all {
		all = "buttons only"
	}
	return append(out, hint("s", all), hint("o", flipOS(b.osFor(c)).String()+" labels"))
}

// buttonInfo is everything a row and its detail show.
type buttonInfo struct {
	buttonRow
	now     string
	state   slotState
	next    string // the pending function, when one is staged
	tier    catalog.Tier
	refused string
	warns   []mouse.Warning
	guarded bool
	pending bool
	// unread is the body the pending edit rewrites that was never read, a
	// macro slot when macro is set; reading says a read of it runs.
	unread   *flash.Extent
	macro    bool
	reading  bool
	bindRaw  []byte
	bodyRaw  []byte
	bodyAddr flash.Extent
}

func (b *Buttons) info(c *Context, r buttonRow) buttonInfo {
	os := b.osFor(c)
	in := buttonInfo{buttonRow: r}
	in.now, in.state = slotFunction(c.Model(), c.Config(), r.slot, os)
	in.tier = slotTier(c, r)
	in.warns = slotWarnings(c, r.slot)
	if s, ok := c.Pending.Get(SlotKey(r.slot)); ok {
		in.pending, in.state = true, slotPending
		in.next = editText(c.Model(), s.Edit, os)
		pv := previewSlot(c, r.slot, s.Edit)
		switch {
		case pv.err != nil:
			in.refused = editRefusal(pv.err)
		case pv.ops > 0:
			in.tier, in.warns = pv.tier, pv.warns
		}
		_, in.macro = s.Edit.(mouse.SetMacro)
		in.unread, in.reading = pv.unread, b.readingBody(s.Edit)
	}
	in.guarded = lastLeftClick(c, r.slot)
	if cfg := c.Config(); cfg != nil {
		in.bindRaw = cfg.Keys[r.slot].Field.Raw
		if cfg.Keys[r.slot].Fn.Type == mouse.TypeShortcut && cfg.Shortcuts[r.slot] != nil {
			in.bodyRaw, _ = mouse.EncodeShortcut(cfg.Shortcuts[r.slot])
			in.bodyAddr, _ = mouse.ShortcutExtent(r.slot)
		}
	}
	return in
}

// readingBody reports that a read of the bodies pending edits like e
// rewrite runs: the Macros tab's, when there is one, for a macro slot.
func (b *Buttons) readingBody(e mouse.Edit) bool {
	if _, ok := e.(mouse.SetMacro); ok && b.macros != nil {
		return b.macros.reading
	}
	return b.reading
}

// slotTier is the tier of giving slot r a function the web app offers.
func slotTier(c *Context, r buttonRow) catalog.Tier {
	t, _ := c.Tier(mouse.FeatureSystem)
	for _, f := range mouse.SlotFeatures(c.Model(), r.slot) {
		ft, _ := c.Tier(f)
		t = min(t, ft)
	}
	return t
}

func (in buttonInfo) function() string {
	if in.pending {
		return in.next
	}
	return in.now
}

func (in buttonInfo) stateText() string {
	if in.guarded && !in.pending {
		return "guarded"
	}
	return in.state.String()
}

func (in buttonInfo) webText() string {
	for _, w := range in.warns {
		if w.Reason == mouse.HiddenSlot {
			return "hidden"
		}
	}
	if len(in.warns) > 0 {
		return "warns"
	}
	return ""
}

func (in buttonInfo) tierText(c *Context) string {
	switch {
	case in.refused != "":
		return "refused"
	case in.unread != nil && in.reading:
		return "reading" + c.Glyphs.Ellipsis
	case in.unread != nil:
		return "not read"
	}
	return in.tier.String()
}

// unreadText says why the pending edit's body is not read yet and when it
// will be.
func (in buttonInfo) unreadText(c *Context) string {
	what := "body"
	if in.macro {
		what = "macro slot"
	}
	switch {
	case in.reading:
		return fmt.Sprintf("reading the %s at %v from the mouse", what, *in.unread)
	case c.Snapshot.State != session.Ready:
		return fmt.Sprintf("the %s at %v was never read; it is read once the mouse is ready", what, *in.unread)
	}
	return fmt.Sprintf("the %s at %v was never read; r reloads and reads it", what, *in.unread)
}

// buttonsSideBySide is the width from which list and detail share the row (§7.1).
const buttonsSideBySide = 100

func (b *Buttons) View(c *Context, w, h int) string {
	if c.Model() == nil {
		return ""
	}
	rows := b.rows(c)
	infos := make([]buttonInfo, len(rows))
	for i, r := range rows {
		infos[i] = b.info(c, r)
	}
	var detail []string
	switch {
	case w >= buttonsSideBySide:
		detailW := min(46, w*2/5)
		listW := w - detailW - 1
		if b.cursor < len(infos) {
			detail = b.detail(c, infos[b.cursor], detailW)
		}
		list := b.list(c, infos, listW, h)
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
	case w >= 80 && b.cursor < len(infos):
		detail = b.detail(c, infos[b.cursor], w)
		list := b.list(c, infos, w, max(4, h-len(detail)-1))
		return strings.Join(append(append(list, ""), detail...), "\n")
	}
	return strings.Join(b.list(c, infos, w, h), "\n")
}

var buttonHeads = []string{"Button", "Slot", "Function", "Tier", "State", "Web app"}

func (b *Buttons) list(c *Context, infos []buttonInfo, w, h int) []string {
	st := c.Styles
	what := plural(len(infos), "button", "buttons")
	if b.all {
		what = "All 16 slots"
	}
	title := " " + st.Bold.Render(what) + st.Faint.Render("  labels: "+b.osFor(c).String())
	nameW := 6
	for _, in := range infos {
		nameW = max(nameW, ansi.StringWidth(in.name))
	}
	widths := []int{nameW, 4, 0, 12, 8, 7}
	fixed := len(widths) + 1
	for _, cw := range widths {
		fixed += cw
	}
	widths[2] = max(10, w-fixed)
	out := []string{title, st.Faint.Render(buttonCells(w, "  ", widths, c.Glyphs.Ellipsis, buttonHeads...))}
	room := max(1, h-len(out))
	more := len(infos) > room
	if more {
		room--
	}
	b.offset = listOffset(b.cursor, b.offset, room, len(infos))
	end := min(len(infos), b.offset+room)
	for i := b.offset; i < end; i++ {
		in := infos[i]
		mark := "  "
		if i == b.cursor {
			mark = c.Glyphs.Cursor + " "
		}
		line := buttonCells(w, mark, widths, c.Glyphs.Ellipsis,
			in.name, strconv.Itoa(in.slot), in.function(), in.tierText(c), in.stateText(), in.webText())
		switch {
		case i == b.cursor:
			line = st.Selected.Render(line)
		case !in.shown:
			line = st.Faint.Render(line)
		case in.pending:
			line = st.Warn.Render(line)
		}
		out = append(out, line)
	}
	if more {
		out = append(out, st.Faint.Render(fmt.Sprintf("  %d-%d of %d", b.offset+1, end, len(infos))))
	}
	return out
}

func (b *Buttons) detail(c *Context, in buttonInfo, w int) []string {
	st := c.Styles
	row := func(k, v string) []string { return wrap(v, w-1, fmt.Sprintf(" %-8s ", k), strings.Repeat(" ", 10)) }
	out := []string{" " + st.Bold.Render(buttonTitle(c, in.buttonRow))}
	out = append(out, row("now", in.now)...)
	if in.pending {
		out = append(out, row("pending", in.next+" (d drops it)")...)
	}
	if in.unread != nil {
		out = append(out, row("read", in.unreadText(c))...)
	}
	if in.refused != "" {
		out = append(out, row("refused", in.refused)...)
	} else {
		out = append(out, row("tier", gateNote(c, in.tier))...)
	}
	if in.guarded {
		out = append(out, row("guard", "the last Left Click; it stays, so the mouse can always click")...)
	}
	if len(in.warns) == 0 {
		out = append(out, row("web app", "shows and edits this")...)
	}
	for i, wn := range in.warns {
		k := ""
		if i == 0 {
			k = "web app"
		}
		out = append(out, row(k, wn.Text)...)
	}
	if len(in.bindRaw) > 0 {
		raw := fmt.Sprintf("% x", in.bindRaw)
		if len(in.bodyRaw) > 0 {
			raw += fmt.Sprintf(" %s body @%d: % x", c.Glyphs.Bullet, in.bodyAddr.Addr, in.bodyRaw)
		}
		out = append(out, row("bytes", raw)...)
	}
	return out
}

func buttonTitle(c *Context, r buttonRow) string {
	if !r.shown {
		return fmt.Sprintf("%s %s not shown by the vendor app", r.name, c.Glyphs.Bullet)
	}
	return fmt.Sprintf("%s %s slot %d", r.name, c.Glyphs.Bullet, r.slot)
}

// buttonCells lays cells out in fixed widths after a prefix, one space apart,
// the last one taking what is left of w.
func buttonCells(w int, prefix string, widths []int, tail string, cells ...string) string {
	var sb strings.Builder
	sb.WriteString(prefix)
	for i, cell := range cells {
		if i > 0 {
			sb.WriteByte(' ')
		}
		cw := widths[i]
		if i == len(cells)-1 {
			cw = max(0, w-ansi.StringWidth(sb.String()))
		}
		sb.WriteString(fit(cell, cw, tail))
	}
	return sb.String()
}

// listOffset moves offset so that cursor is one of the room rows shown out of n.
func listOffset(cursor, offset, room, n int) int {
	if n <= room {
		return 0
	}
	offset = max(min(offset, cursor), cursor-room+1)
	return max(0, min(offset, n-room))
}
