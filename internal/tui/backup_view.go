package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

// backupSideBySide is the width from which the list and the selected
// backup share the row (§7.1); below backupStacked only the list shows.
const (
	backupSideBySide = 100
	backupStacked    = 80
)

func (t *BackupTab) View(c *Context, w, h int) string {
	lines := t.head(c, w)
	if len(t.list) == 0 {
		return strings.Join(lines, "\n")
	}
	switch {
	case w >= backupSideBySide:
		dw := min(56, w*9/20)
		lw := w - dw - 1
		list := t.rows(c, lw, h-len(lines))
		detail := t.detail(c, dw)
		for i := range max(len(list), len(detail)) {
			var l, d string
			if i < len(list) {
				l = list[i]
			}
			if i < len(detail) {
				d = detail[i]
			}
			lines = append(lines, fit(l, lw, c.Glyphs.Ellipsis)+" "+d)
		}
	case w >= backupStacked:
		detail := t.detail(c, w)
		lines = append(lines, t.rows(c, w, max(3, h-len(lines)-len(detail)-1))...)
		lines = append(lines, "")
		lines = append(lines, detail...)
	default:
		lines = append(lines, t.rows(c, w, h-len(lines))...)
	}
	return strings.Join(lines, "\n")
}

// head is the tab's title and, when there is nothing to list, why.
func (t *BackupTab) head(c *Context, w int) []string {
	st := c.Styles
	var who string
	switch m := c.Model(); {
	case backupKey(c) == "":
	case m != nil:
		who = " of " + modelName(m) + " (" + backupKey(c) + ")"
	default:
		who = " of " + backupKey(c)
	}
	title := " " + st.Bold.Render("Backups"+who)
	if len(t.list) > 0 {
		title += st.Faint.Render(fmt.Sprintf("  %d, newest first", len(t.list)))
	}
	out := []string{title}
	say := func(style func(...string) string, text string) {
		for _, l := range wrap(text, w, " ", " ") {
			out = append(out, style(l))
		}
	}
	if t.busy != "" {
		say(st.Info.Render, t.busy)
	}
	switch {
	case t.lister == nil:
		say(st.Faint.Render, "Backups are not available in this session: it writes nothing, so it keeps none.")
	case backupKey(c) == "":
		say(st.Faint.Render, "No mouse is identified yet; its backups show here once it answers.")
	case t.listErr != nil:
		say(st.Bad.Render, "The backups folder could not be read: "+plain(t.listErr)+".")
	case len(t.list) == 0 && t.listing:
		say(st.Faint.Render, "Reading the backups folder"+c.Glyphs.Ellipsis)
	case len(t.list) == 0:
		say(st.Faint.Render, "No backup of this mouse yet. f takes a full backup; b saves what is loaded. "+
			"arcctl also saves one before its first write to a mouse.")
	}
	return out
}

var backupHeads = []string{"Created (UTC)", "Kind", "Firmware", "Profile", "Label"}

func (t *BackupTab) rows(c *Context, w, h int) []string {
	st := c.Styles
	widths := []int{16, 13, 8, 7, 0}
	out := []string{st.Faint.Render(buttonCells(w, "  ", widths, c.Glyphs.Ellipsis, backupHeads...))}
	room := max(1, h-len(out))
	more := len(t.list) > room
	if more {
		room--
	}
	t.offset = listOffset(t.cursor, t.offset, room, len(t.list))
	end := min(len(t.list), t.offset+room)
	for i := t.offset; i < end; i++ {
		l := t.list[i]
		mark := "  "
		if i == t.cursor {
			mark = c.Glyphs.Cursor + " "
		}
		var cells []string
		if f := l.File; f != nil {
			cells = []string{f.Created.UTC().Format("2006-01-02 15:04"), backupKind(f), orDash(f.Device.FWMouse),
				backupProfile(f.Device.Profile), backupLabel(f)}
		} else {
			cells = []string{"unreadable", "", "", "", filepath.Base(l.Path)}
		}
		line := buttonCells(w, mark, widths, c.Glyphs.Ellipsis, cells...)
		switch {
		case i == t.cursor:
			line = st.Selected.Render(line)
		case l.File == nil:
			line = st.Bad.Render(line)
		case l.File.Automatic():
			line = st.Faint.Render(line)
		}
		out = append(out, line)
	}
	if more {
		out = append(out, st.Faint.Render(fmt.Sprintf("  %d-%d of %d", t.offset+1, end, len(t.list))))
	}
	return out
}

func backupKind(f *backup.File) string {
	kind := "loaded"
	if f.Full {
		kind = "full"
	}
	if len(f.Missing()) > 0 {
		kind += ", partial"
	}
	return kind
}

func backupLabel(f *backup.File) string {
	switch {
	case f.Label != "":
		return f.Label
	case f.Source != backup.SourceDevice:
		return "(" + f.Source + ")"
	}
	return ""
}

func backupProfile(p *backup.Profile) string {
	switch {
	case p == nil:
		return "?"
	case !p.Supported:
		return "none"
	}
	return strconv.Itoa(int(p.Value))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// detail describes the selected backup: where it came from, what it
// captured, and a short decoded summary.
func (t *BackupTab) detail(c *Context, w int) []string {
	l, ok := t.selected()
	if !ok {
		return nil
	}
	st := c.Styles
	out := []string{" " + st.Bold.Render(filepath.Base(l.Path))}
	row := func(k, v string) {
		out = append(out, wrap(v, w-1, fmt.Sprintf(" %-9s ", k), strings.Repeat(" ", 11))...)
	}
	f := l.File
	if f == nil {
		row("error", plain(l.Err))
		return out
	}
	created := f.Created.UTC().Format(time.DateTime) + " UTC"
	if f.Tool != "" {
		created += " by " + f.Tool
	}
	if f.Source != backup.SourceDevice {
		created += ", from the " + f.Source
	}
	row("Created", created)
	if f.Label != "" {
		row("Label", f.Label)
	}
	row("Identity", f.Key())
	row("Firmware", fmt.Sprintf("mouse %s, receiver %s", orDash(f.Device.FWMouse), orDash(f.Device.FWReceiver)))
	row("Profile", backupProfileText(f.Device.Profile))
	captured := fmt.Sprintf("%d bytes, %s", f.Known(), map[bool]string{true: "full backup", false: "the loaded bytes"}[f.Full])
	if miss := f.Missing(); len(miss) > 0 {
		captured += "; could not read " + extentWords(miss)
	}
	row("Captured", captured)
	m, known := f.CatalogModel()
	if !known || m.Family != catalog.FamilyMouse {
		row("Model", "not decoded: the model is not a mouse this build knows")
		return out
	}
	s := backup.Summarize(m, f.Image(), c.OS)
	row("DPI", summaryDPI(s))
	row("Buttons", summaryButtons(c, m, f.Image()))
	if b := summaryBodies(s); b != "" {
		row("Bodies", b)
	}
	if n := len(s.Invalid); n > 0 {
		names := make([]string, n)
		for i, iv := range s.Invalid {
			names[i] = iv.Name
		}
		row("Invalid", strings.Join(names, ", "))
	}
	return out
}

func backupProfileText(p *backup.Profile) string {
	switch {
	case p == nil:
		return "not asked"
	case !p.Supported:
		return "no onboard profiles"
	}
	return "onboard profile " + strconv.Itoa(int(p.Value))
}

func extentWords(es []flash.Extent) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.String()
	}
	return strings.Join(parts, ", ")
}

func summaryDPI(s *backup.Summary) string {
	if s.Stages == nil {
		return "not read"
	}
	var vals []string
	for _, d := range s.DPI {
		if !d.Active {
			continue
		}
		v := strconv.Itoa(d.X)
		switch {
		case d.X == 0:
			v = d.State
		case d.Y != d.X:
			v += "x" + strconv.Itoa(d.Y)
		}
		vals = append(vals, v)
	}
	text := plural(*s.Stages, "stage", "stages")
	if s.Current != nil {
		text += ", current " + strconv.Itoa(*s.Current)
	}
	return text + ": " + strings.Join(vals, " ")
}

// summaryButtons names each button the web app shows and what it does in
// im, in the Buttons tab's order and words.
func summaryButtons(c *Context, m *catalog.Model, im *flash.Image) string {
	cfg := mouse.Decode(m, im)
	var parts []string
	for _, r := range buttonRows(m, false) {
		what, _ := slotFunction(m, &cfg, r.slot, c.OS)
		parts = append(parts, r.name+": "+what)
	}
	return strings.Join(parts, "; ")
}

func summaryBodies(s *backup.Summary) string {
	var parts []string
	if n := len(s.Shortcuts); n > 0 {
		parts = append(parts, plural(n, "shortcut", "shortcuts"))
	}
	var names []string
	for _, m := range s.Macros {
		if m.Name != "" {
			names = append(names, strconv.Quote(m.Name))
		}
	}
	if len(s.Macros) > 0 {
		text := plural(len(s.Macros), "macro", "macros")
		if len(names) > 0 {
			text += " (" + strings.Join(names, ", ") + ")"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", ")
}

// backupShow shows a backup's decoded configuration (§6.6 summary): what a
// person needs to set the mouse up again by hand.
type backupShow struct {
	l backup.Listing
	pager
}

func newBackupShow(l backup.Listing) *backupShow { return &backupShow{l: l} }

func (d *backupShow) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	s := k.String()
	switch {
	case d.pager.key(s):
	case s == "esc", s == "enter", s == "q":
		return CloseDialog
	}
	return nil
}

func (d *backupShow) Hints(c *Context) []key.Binding {
	return append(d.pager.hints(), hint("esc", "close"))
}

func (d *backupShow) View(c *Context, w, h int) string {
	return indentLines(d.frame(c, d.lines(c, max(20, w-2)), nil, -1, h), " ")
}

func (d *backupShow) lines(c *Context, w int) []string {
	st, f := c.Styles, d.l.File
	out := []string{st.Bold.Render("Backup " + filepath.Base(d.l.Path))}
	out = append(out, wrap(fmt.Sprintf("%s UTC %s %s %s %s", f.Created.UTC().Format(time.DateTime), c.Glyphs.Bullet,
		backupKind(f), c.Glyphs.Bullet, f.Key()), w, "", "  ")...)
	m, ok := f.CatalogModel()
	if !ok || m.Family != catalog.FamilyMouse {
		return append(out, "", "The backup's model is not a mouse this build decodes.")
	}
	s := backup.Summarize(m, f.Image(), c.OS)
	sec := func(title string) { out = append(out, "", st.Bold.Render(title)) }
	add := func(first, indent, text string) { out = append(out, wrap(text, w, first, indent)...) }

	sec("Settings")
	add("  Report rate  ", strings.Repeat(" ", 15), intOr(s.Rate, " Hz"))
	add("  DPI stages   ", strings.Repeat(" ", 15), intOr(s.Stages, "")+", current "+intOr(s.Current, ""))
	sec("DPI")
	for _, x := range s.DPI {
		v := x.State
		if x.X > 0 {
			v = strconv.Itoa(x.X)
			if x.Y != x.X {
				v += " x " + strconv.Itoa(x.Y)
			}
		}
		line := fmt.Sprintf("  %d  %-12s %s", x.Stage, v, x.Color)
		switch {
		case x.Current:
			line += "  current"
		case !x.Active:
			line += "  inactive"
		}
		out = append(out, line)
	}
	sec("Buttons")
	for _, b := range s.Buttons {
		name := b.Button
		if name == "" || b.Hidden {
			name = "-"
		}
		lead := fmt.Sprintf("  %2d  %-12s ", b.Slot, name)
		add(lead, strings.Repeat(" ", ansi.StringWidth(lead)), b.Action)
	}
	if len(s.Shortcuts) > 0 {
		sec("Shortcut bodies")
		for _, sc := range s.Shortcuts {
			text := sc.Keys
			switch {
			case sc.State != "valid":
				text = sc.State
			case sc.Preset != "":
				text += " (" + sc.Preset + ")"
			}
			if !sc.Bound {
				text += ", not bound"
			}
			out = append(out, fmt.Sprintf("  %2d  %s", sc.Slot, text))
		}
	}
	if len(s.Macros) > 0 {
		sec("Macros")
		for _, mc := range s.Macros {
			head := fmt.Sprintf("  %2d  %s", mc.Slot, strconv.Quote(mc.Name))
			if mc.State != "valid" {
				head = fmt.Sprintf("  %2d  %s", mc.Slot, mc.State)
			}
			if len(mc.BoundBy) > 0 {
				head += ", bound to slot " + intsText(mc.BoundBy)
			}
			out = append(out, head)
			var evs []string
			for _, e := range mc.Events {
				verb := "release"
				if e.Press {
					verb = "press"
				}
				evs = append(evs, fmt.Sprintf("%s %s %dms,", verb, e.Key, e.Delay))
			}
			if n := len(evs); n > 0 {
				evs[n-1] = strings.TrimSuffix(evs[n-1], ",")
				out = append(out, wrap(strings.Join(evs, " "), w, "      ", "      ")...)
			}
		}
	}
	if len(s.Settings) > 0 {
		sec("Other fields")
		for _, x := range s.Settings {
			v := ""
			if x.Value != nil {
				v = strconv.Itoa(*x.Value)
			}
			out = append(out, fmt.Sprintf("  %-20s %4d  %-24s %s", x.Name, x.Addr, x.Raw, v))
		}
	}
	if len(s.Invalid) > 0 {
		sec("Invalid")
		for _, iv := range s.Invalid {
			add(fmt.Sprintf("  %s @%d+%d: ", iv.Name, iv.Addr, iv.Len), "    ", iv.Raw+": "+iv.Error)
		}
	}
	return out
}

func intOr(p *int, unit string) string {
	if p == nil {
		return "not read"
	}
	return strconv.Itoa(*p) + unit
}

func intsText(v []int) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ", ")
}

// restoreDiff shows how the mouse differs from a backup, record by record,
// with what a restore does with each; u includes or leaves out the records
// arcctl knows no valid value for (--include-unknown), and w goes on to the
// review.
type restoreDiff struct {
	tab     *BackupTab
	src     *backup.Source
	read    *flash.Image
	rs      *backup.Restore
	unknown bool
	pager
}

func newRestoreDiff(t *BackupTab, src *backup.Source, read *flash.Image, rs *backup.Restore) *restoreDiff {
	return &restoreDiff{tab: t, src: src, read: read, rs: rs}
}

func (d *restoreDiff) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	s := k.String()
	switch {
	case d.pager.key(s):
	case s == "esc", s == "q":
		return CloseDialog
	case s == "w":
		return tea.Sequence(CloseDialog, d.tab.openReview(c, d.src, d.read, d.unknown))
	case s == "u" && d.eligible():
		return d.include(c, !d.unknown)
	}
	return nil
}

// eligible reports whether some record differs that only --include-unknown
// writes back.
func (d *restoreDiff) eligible() bool {
	return slices.ContainsFunc(d.rs.Records, func(x backup.RestoreRecord) bool { return x.Eligible })
}

// include lays the restore out again with or without the unknown records.
func (d *restoreDiff) include(c *Context, unknown bool) tea.Cmd {
	tg := restoreTarget(c, d.tab.source)
	tg.Image = withReads(tg.Image, d.read)
	rs, err := backup.PlanRestore(d.src, tg, backup.RestoreOptions{IncludeUnknown: unknown})
	if err != nil {
		return Problem(fmt.Errorf("the restore cannot be laid out: %w", err))
	}
	d.rs, d.unknown = rs, unknown
	if unknown {
		return Notice(fmt.Sprintf("Unknown records included: %s go back as the backup captured them, at the experimental tier.",
			plural(len(rs.Captured()), "record", "records")))
	}
	return Notice("Unknown records left out.")
}

func (d *restoreDiff) Hints(c *Context) []key.Binding {
	out := d.pager.hints()
	if len(d.rs.Plan.Ops) > 0 {
		out = append(out, hint("w", "restore"))
	}
	switch {
	case d.unknown:
		out = append(out, hint("u", "leave out unknown"))
	case d.eligible():
		out = append(out, hint("u", "include unknown"))
	}
	return append(out, hint("esc", "close"))
}

func (d *restoreDiff) View(c *Context, w, h int) string {
	tw := max(20, w-2)
	st := c.Styles
	out := []string{st.Bold.Render("The mouse now " + c.Glyphs.Arrow + " backup " + filepath.Base(d.src.Path))}
	out = append(out, restoreSummary(c, d.rs, tw)...)
	out = append(out, "")
	out = append(out, restoreRecordLines(c, d.src, d.rs, tw, false)...)
	return indentLines(d.frame(c, out, nil, -1, h), " ")
}

// restoreSummary says how many records differ and what happens to them,
// with the notes of the restore.
func restoreSummary(c *Context, rs *backup.Restore, w int) []string {
	var out []string
	for _, n := range rs.Notes {
		out = append(out, wrap(n, w, "", "  ")...)
	}
	for _, cl := range rs.Clamps {
		out = append(out, wrap(fmt.Sprintf("Clamped %s % x to % x: the value is past what this mouse takes.", cl.Name, cl.From, cl.To), w, "", "  ")...)
	}
	writes := rs.Count(backup.FateWrite)
	text := fmt.Sprintf("%s differ: %s to write, %s left alone; %s equal.", plural(len(rs.Records), "record", "records"),
		plural(writes, "record", "records"), plural(len(rs.Records)-writes, "record", "records"), plural(rs.Equal, "record", "records"))
	if len(rs.Records) == 0 {
		text = fmt.Sprintf("The mouse holds what the backup holds: %s equal.", plural(rs.Equal, "record", "records"))
	}
	return append(out, wrap(text, w, "", "")...)
}

// restoreRecordLines list the records that differ: the mouse now and the
// backup in words, and what the restore does; with leftOnly, only those it
// leaves alone.
func restoreRecordLines(c *Context, src *backup.Source, rs *backup.Restore, w int, leftOnly bool) []string {
	st, m := c.Styles, c.Model()
	base := c.Image()
	if base == nil {
		base = flash.New()
	}
	width := 0
	for _, x := range rs.Records {
		width = max(width, len(x.Name))
	}
	pad := strings.Repeat(" ", width+4)
	var out []string
	for _, x := range rs.Records {
		if leftOnly && x.Fate == backup.FateWrite {
			continue
		}
		now := recordWords(m, base, x.Extent, x.Device, c.OS)
		was := recordWords(m, src.Image, x.Extent, x.Source, c.OS)
		lead := fmt.Sprintf("  %-*s  ", width, x.Name)
		out = append(out, wrap(now+" "+c.Glyphs.Arrow+" "+was, w, lead, pad)...)
		fate, style := "write, "+x.Tier.String(), reviewTierStyle(st, x.Tier)
		switch {
		case x.Captured:
			fate += ", as the backup captured it: " + x.Why
		case x.Eligible:
			fate, style = x.Fate.String()+": "+x.Why+"; u in the diff includes it", st.Faint
		case x.Fate != backup.FateWrite:
			fate, style = x.Fate.String()+": "+x.Why, st.Faint
		}
		for _, l := range wrap(fate, w, pad, pad) {
			out = append(out, style.Render(l))
		}
	}
	return out
}

// recordWords says in words what b holds as the record at e over base; b
// is nil when it is not known.
func recordWords(m *catalog.Model, base *flash.Image, e flash.Extent, b []byte, os keys.OS) string {
	if b == nil || m == nil {
		return "not known"
	}
	im := base.Clone()
	if err := im.Set(e.Addr, b); err != nil {
		return "not known"
	}
	return backup.Words(m, im, e, os)
}
