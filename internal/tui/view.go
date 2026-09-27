package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.KeyboardEnhancements.ReportAlternateKeys = true
	v.WindowTitle = "arcctl"
	return v
}

// render lays the screen out: header, tab bar, rule, banner, body, rule or
// notice, footer; every line exactly the terminal's width.
func (a *App) render() string {
	w, h := a.width, a.height
	c := a.context()
	t := a.activeTab(c)
	rule := c.Styles.Faint.Render(strings.Repeat(c.Glyphs.Rule, w))
	top := []string{a.header(c, w), a.tabBar(c, w), rule}
	bottom := a.noticeLines(c, w)
	if len(bottom) == 0 {
		bottom = []string{rule}
	}
	room := h - len(top) - len(bottom) - 1
	ban := a.banner(c, w)
	if limit := max(1, room/2); len(ban) > limit && !a.bannerOnly(c) {
		ban = append(ban[:limit-1], c.Styles.Faint.Render("  "+c.Glyphs.Ellipsis))
	}
	bodyH := room - len(ban)
	var body string
	switch {
	case a.dialog != nil:
		body = a.dialog.View(c, w, bodyH)
	case t != nil && !a.bannerOnly(c):
		body = t.View(c, w, bodyH)
	}
	lines := append(top, ban...)
	lines = append(lines, block(body, bodyH)...)
	lines = append(lines, bottom...)
	lines = append(lines, a.footer(c, t, w))
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if a.opt.ASCII {
			l = asciiLine(l)
		}
		lines[i] = fit(l, w, c.Glyphs.Ellipsis)
	}
	return strings.Join(lines, "\n")
}

// noticeLines are the notice in up to two lines, a path in it cut in the
// middle so that its file name shows; nil without a notice.
func (a *App) noticeLines(c *Context, w int) []string {
	n := a.notice
	if n.text == "" && n.path == "" {
		return nil
	}
	st := c.Styles.Info
	if n.bad {
		st = c.Styles.Bad
	}
	text := n.text
	if n.path != "" {
		text += " " + midCut(c, n.path, w-4)
	}
	lines := wrap(text, w, " ", "   ")
	if len(lines) > 2 {
		lines = []string{lines[0], ansi.Truncate(lines[1]+" "+strings.TrimSpace(strings.Join(lines[2:], " ")), w, c.Glyphs.Ellipsis)}
	}
	for i, l := range lines {
		lines[i] = st.Render(l)
	}
	return lines
}

// bannerOnly reports that there is nothing for the tabs to show yet, so the
// banner has the screen to itself.
func (a *App) bannerOnly(c *Context) bool {
	return a.sn.State == session.Choosing || (a.sn.Image == nil && a.sn.Model == nil && len(a.visible(c)) == 0)
}

// block cuts or pads s to exactly h lines.
func block(s string, h int) []string {
	if h <= 0 {
		return nil
	}
	var lines []string
	if s != "" {
		lines = strings.Split(s, "\n")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

type segment struct {
	text string
	drop int // segments with the highest drop go first when the line is too long
}

func (a *App) header(c *Context, w int) string {
	sn := a.sn
	st := c.Styles
	left := []segment{{st.App.Render("arcctl"), 0}}
	switch m := sn.Model; {
	case m != nil:
		left = append(left, segment{st.Bold.Render(modelName(m)) + " " + modelKey(m), 0})
	case sn.Handshake != nil:
		left = append(left, segment{fmt.Sprintf("device %02X/%02X", sn.Handshake.CID, sn.Handshake.MID), 0})
	}
	if hs := sn.Handshake; hs != nil {
		left = append(left, segment{hs.ConnString(), 4})
	}
	if v := sn.Versions.Mouse; v != "" {
		left = append(left, segment{"mouse " + v, 5})
	}
	if v := sn.Versions.Receiver; v != "" {
		left = append(left, segment{"rx " + v, 6})
	}
	if b := sn.Battery; b != nil {
		s := fmt.Sprintf("battery %d%%", b.Level)
		if b.Charging {
			s += " charging"
		}
		left = append(left, segment{s, 3})
	}
	right := []segment{a.stateBadge(c), {a.modeBadge(c), 0}}
	if s := a.tierBadge(c); s != "" {
		right = append(right, segment{s, 1})
	}
	switch a.opt.Source {
	case "emulator":
		right = append(right, segment{st.Info.Render("[EMULATED]"), 2})
	case "replay":
		right = append(right, segment{st.Info.Render("[REPLAY]"), 2})
	}
	for {
		line := " " + joinSegments(left) + "  " + joinSegments(right)
		if ansi.StringWidth(line) <= w {
			return line
		}
		if !dropOne(&left, &right) {
			return line
		}
	}
}

func joinSegments(ss []segment) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = s.text
	}
	return strings.Join(parts, "  ")
}

func dropOne(lists ...*[]segment) bool {
	bl, bi, best := -1, -1, 0
	for l, list := range lists {
		for i, s := range *list {
			if s.drop > best {
				bl, bi, best = l, i, s.drop
			}
		}
	}
	if bl < 0 {
		return false
	}
	l := lists[bl]
	*l = append((*l)[:bi], (*l)[bi+1:]...)
	return true
}

func modelName(m *catalog.Model) string {
	if m.Alias != "" {
		return m.Alias
	}
	return m.Name
}

func modelKey(m *catalog.Model) string {
	if len(m.Key) == 4 {
		return m.Key[:2] + "/" + m.Key[2:]
	}
	return m.Key
}

var stateBadges = map[session.State]struct {
	text string
	tone tone
}{
	session.NoReceiver:        {"NO RECEIVER", toneBad},
	session.NeedsPermission:   {"NO ACCESS", toneBad},
	session.Probing:           {"PROBING", toneInfo},
	session.Choosing:          {"CHOOSE", toneWarn},
	session.Locked:            {"LOCKED", toneWarn},
	session.Seized:            {"SEIZED", toneBad},
	session.Stalled:           {"STALLED", toneBad},
	session.Offline:           {"OFFLINE", toneWarn},
	session.Handshaking:       {"HANDSHAKE", toneInfo},
	session.Loading:           {"LOADING", toneInfo},
	session.Ready:             {"ONLINE", toneGood},
	session.Conflict:          {"CONFLICT", toneBad},
	session.SuspectedConflict: {"REPLIES MISSING", toneBad},
	session.Applying:          {"WRITING", toneWarn},
	session.Recovering:        {"RECOVERY", toneBad},
	session.Unknown:           {"UNKNOWN DEVICE", toneWarn},
}

func (a *App) stateBadge(c *Context) segment {
	b, ok := stateBadges[a.sn.State]
	switch {
	case a.write != nil && a.sn.State == session.Ready && a.sn.Progress.Job == "backup":
		b.text, b.tone = "BACKING UP", toneWarn
	case a.write != nil && a.sn.State == session.Ready:
		b = stateBadges[session.Applying]
	case !ok:
		b.text = strings.ToUpper(a.sn.State.String())
	}
	return segment{c.Styles.tone(b.tone).Render(b.text), 0}
}

func (a *App) modeBadge(c *Context) string {
	switch c.Mode {
	case ModeEdit:
		return c.Styles.Warn.Render("[EDIT]")
	case ModeDryRun:
		return c.Styles.Info.Render("[DRY-RUN]")
	}
	return c.Styles.Faint.Render("[READ-ONLY]")
}

// webFeatures are what the web app edits on every mouse; their lowest tier
// is the header's tier badge.
var webFeatures = []mouse.Feature{mouse.FeatureStages, mouse.FeatureCurrent, mouse.FeatureDPI,
	mouse.FeatureSystem, mouse.FeatureMedia, mouse.FeatureShortcut, mouse.FeatureMacro}

func (a *App) tierBadge(c *Context) string {
	m := c.Model()
	switch {
	case m == nil:
		return ""
	case m.Family != catalog.FamilyMouse:
		return c.Styles.Faint.Render("[READ-ONLY MODEL]")
	}
	low := catalog.Verified
	elsewhere := false
	for _, f := range webFeatures {
		t, _ := c.Tier(f)
		low = min(low, t)
		if t < catalog.Verified && len(c.verified.Find(m.Key, string(f))) > 0 {
			elsewhere = true
		}
	}
	switch {
	case low == catalog.Verified:
		return c.Styles.Good.Render("[VERIFIED]")
	case elsewhere:
		return c.Styles.Warn.Render("[UNTESTED ON THIS FIRMWARE]")
	}
	return c.Styles.Warn.Render("[UNTESTED]")
}

func (a *App) tabBar(c *Context, w int) string {
	var parts []string
	on := a.shown(c)
	for n, i := range a.visible(c) {
		label := fmt.Sprintf("%d %s", n+1, a.tabs[i].Title())
		if i == on {
			parts = append(parts, c.Styles.TabOn.Render("["+label+"]"))
		} else {
			parts = append(parts, c.Styles.TabOff.Render(" "+label+" "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func (a *App) footer(c *Context, t Tab, w int) string {
	pending := "0 pending"
	if n := a.pending.Len(); n > 0 {
		pending = c.Styles.Warn.Render(fmt.Sprintf("%d pending", n))
	}
	part := func(b key.Binding) string {
		h := b.Help()
		p := c.Styles.Key.Render(h.Key) + " " + h.Desc
		if a.opt.ASCII {
			p = asciiLine(p)
		}
		return p
	}
	// The last two always stay (help and quit, or a dialog's go on and
	// back); the others show in order where they fit.
	keys := a.hints(c, t, true)
	n := max(0, len(keys)-2)
	tail := ""
	for _, b := range keys[n:] {
		tail += "   " + part(b)
	}
	line := " " + pending
	for _, b := range keys[:n] {
		if p := part(b); ansi.StringWidth(line)+3+ansi.StringWidth(p)+ansi.StringWidth(tail) <= w {
			line += "   " + p
		}
	}
	return line + tail
}

// hints are the keys that work now: the dialog's, or the picker's, or those
// of a tab that takes every key, or else the review's when edits are
// pending, the tab's and then the shell's. short leaves out the ones the
// footer has no room for.
func (a *App) hints(c *Context, t Tab, short bool) []key.Binding {
	k := a.keys
	if a.dialog != nil {
		return a.dialog.Hints(c)
	}
	var out []key.Binding
	switch a.sn.State {
	case session.Choosing:
		return []key.Binding{k.Up, k.Down, k.Choose, k.Quit}
	case session.Recovering:
		out = append(out, k.Settle)
	case session.Conflict:
		out = append(out, k.Clear)
	case session.NeedsPermission:
		if a.opt.Host.OpenSettings != nil {
			out = append(out, k.Settings)
		}
	}
	tab := t != nil && !a.bannerOnly(c)
	if tab && t.Capturing() {
		return t.Hints(c)
	}
	idle := !c.Writing()
	switch {
	case a.hiddenWriter() != nil:
		out = append(out, hint("a", "show the write"))
	case idle && c.Mode != ModeReadOnly && a.pending.Len() > 0:
		out = append(out, k.Review, k.Discard)
	}
	if tab {
		out = append(out, t.Hints(c)...)
	}
	if !short || idle && c.Mode != ModeReadOnly && a.sn.Journal != nil && a.sn.Journal.Last != nil {
		rv := k.Revert
		rv.SetHelp("U", revertName(c))
		out = append(out, rv)
	}
	if idle && a.sn.Image != nil {
		out = append(out, k.Reload)
		if a.opt.Backups != nil && !short {
			out = append(out, k.Backup)
		}
	}
	if !short {
		out = append(out, k.Next, k.Prev, hint("1-9", "tab by number"))
	}
	return append(out, k.Help, k.Quit)
}
