package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// Confirm asks before an action. With a Phrase the user types it out and
// presses enter; the typing is read as key events and echoed (§7.8), and a
// paste is ignored. Without one, y goes on. n or esc closes it. Going on
// runs the command Yes returns, then closes the dialog.
type Confirm struct {
	Title  string
	Body   []string
	Phrase string
	Yes    func(typed string) tea.Cmd
	typed  []rune
	wrong  bool
	pasted bool
	// write is the write this confirmation approves once the user goes on.
	write     *writeRequest
	confirmed bool
}

func (d *Confirm) approves(r writeRequest) bool {
	if !d.confirmed || d.write == nil || !sameRequest(r, *d.write) {
		return false
	}
	d.write = nil
	return true
}

func (d *Confirm) Update(c *Context, msg tea.Msg) tea.Cmd {
	if _, ok := msg.(tea.PasteMsg); ok && d.Phrase != "" {
		d.pasted = true
		return nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	d.pasted = false
	s := k.String()
	if s == "esc" {
		return CloseDialog
	}
	if d.Phrase == "" {
		switch s {
		case "y", "Y":
			return d.yes("")
		case "n", "N":
			return CloseDialog
		}
		return nil
	}
	switch s {
	case "enter":
		if typed := strings.TrimSpace(string(d.typed)); typed == d.Phrase {
			return d.yes(typed)
		}
		d.wrong = true
	case "backspace":
		if n := len(d.typed); n > 0 {
			d.typed = d.typed[:n-1]
		}
		d.wrong = false
	case "ctrl+u":
		d.typed, d.wrong = nil, false
	default:
		if k.Text != "" {
			d.typed = append(d.typed, []rune(k.Text)...)
			d.wrong = false
		}
	}
	return nil
}

func (d *Confirm) yes(typed string) tea.Cmd {
	d.confirmed = true
	var cmd tea.Cmd
	if d.Yes != nil {
		cmd = d.Yes(typed)
	}
	return tea.Sequence(cmd, closeDialog(d))
}

func (d *Confirm) View(c *Context, w, h int) string {
	lines := []string{c.Styles.Bold.Render(d.Title), ""}
	for _, b := range d.Body {
		if strings.HasPrefix(b, "  ") {
			lines = append(lines, b)
			continue
		}
		lines = append(lines, wrap(b, w-2, "", "  ")...)
	}
	lines = append(lines, "")
	if d.Phrase == "" {
		lines = append(lines, "y goes on, n or esc cancels.")
	} else {
		lines = append(lines, wrap(fmt.Sprintf("Type %q and press enter to go on; esc cancels.", d.Phrase), w-2, "", "")...)
		lines = append(lines, phraseLine(c, d.typed, d.wrong, d.pasted))
	}
	return indentLines(lines, " ")
}

// phraseLine echoes a typed confirmation, and says why it is not accepted.
func phraseLine(c *Context, typed []rune, wrong, pasted bool) string {
	line := "> " + string(typed) + "_"
	switch {
	case pasted:
		line += "   " + c.Styles.Warn.Render("paste is ignored: type it out")
	case wrong:
		line += "   " + c.Styles.Bad.Render("does not match")
	}
	return line
}

func (d *Confirm) Hints(c *Context) []key.Binding {
	if d.Phrase != "" {
		return []key.Binding{hint("enter", "confirm"), hint("esc", "cancel")}
	}
	return []key.Binding{hint("y", "go on"), hint("n/esc", "cancel")}
}

func hint(k, help string) key.Binding {
	return key.NewBinding(key.WithKeys(k), key.WithHelp(k, help))
}

func indentLines(lines []string, prefix string) string {
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// helpDialog lists every key that works on the current screen, in two
// columns when they do not fit in one, and scrolls when they still do not.
type helpDialog struct {
	keys []key.Binding
	page pager
}

func newHelpDialog(keys []key.Binding) *helpDialog { return &helpDialog{keys: keys} }

func (d *helpDialog) Update(c *Context, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch s := k.String(); s {
		case "esc", "?", "q", "enter":
			return CloseDialog
		default:
			d.page.key(s)
		}
	}
	return nil
}

const helpColumn = 38

func (d *helpDialog) View(c *Context, w, h int) string {
	var items []string
	for _, b := range d.keys {
		hl := b.Help()
		items = append(items, fmt.Sprintf("%-12s %s", hl.Key, hl.Desc))
	}
	note := wrap("No key needs the mouse, and none uses Cmd or Ctrl+Tab.", w-2, "", "")
	rows := items
	if room := h - 3 - len(note); len(items) > room && w >= 2*helpColumn+3 {
		half := (len(items) + 1) / 2
		rows = besideCols(items[:half], items[half:], helpColumn, 2)
	}
	body := append([]string{c.Styles.Bold.Render("Keys"), ""}, rows...)
	body = append(body, "")
	body = append(body, note...)
	return indentLines(d.page.frame(c, body, nil, -1, h), " ")
}

func (d *helpDialog) Hints(c *Context) []key.Binding {
	return append([]key.Binding{hint("esc", "close")}, d.page.hints()...)
}

// recoveryDialog is the prompt for a write the journal holds unfinished:
// what its records hold now and what each way of settling it writes. The
// ways stay at the bottom while the records scroll above them.
type recoveryDialog struct {
	open    session.OpenRun
	forward string
	back    string
	ops     map[safety.Strategy]int
	page    pager
}

func newRecoveryDialog(c *Context, o session.OpenRun) *recoveryDialog {
	d := &recoveryDialog{open: o, forward: "unknown until its records are read", back: "unknown until its records are read",
		ops: map[safety.Strategy]int{}}
	if o.Inspection == nil {
		return d
	}
	dev := safety.Device{Identity: o.Run.Device, Profile: o.Run.Profile, Image: o.Inspection.Image}
	if m := c.Model(); m != nil {
		dev.Layout = mouse.Layout(m)
	}
	count := func(how safety.Strategy) string {
		p, err := safety.RecoveryPlan(o.Inspection, how, dev)
		switch {
		case err != nil:
			return "not possible: " + plain(err)
		case len(p.Ops) == 0:
			return "nothing to write"
		}
		d.ops[how] = len(p.Ops)
		return plural(len(p.Ops), "write", "writes")
	}
	d.forward, d.back = count(safety.Forward), count(safety.Back)
	return d
}

func (d *recoveryDialog) torn() bool { return d.open.Inspection != nil && d.open.Inspection.Torn() }

func (d *recoveryDialog) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch s := k.String(); s {
	case "esc":
		return tea.Batch(CloseDialog, Notice("Writes stay blocked until the unfinished write is settled; R opens the prompt again."))
	case "f":
		return d.settle(c, safety.Forward, "Finish")
	case "b":
		return d.settle(c, safety.Back, "Roll back")
	case "l":
		if d.torn() {
			return Problem(safety.ErrTorn)
		}
		return d.settle(c, safety.Leave, "Leave")
	default:
		d.page.key(s)
	}
	return nil
}

// settle asks before it settles the run with how; going on closes this
// prompt too when it is open.
func (d *recoveryDialog) settle(c *Context, how safety.Strategy, what string) tea.Cmd {
	run := d.open.Run
	r := writeRequest{kind: safety.KindRecover, run: run.ID, how: how, gates: c.Gates, ops: d.ops[how]}
	conf := &Confirm{
		Title: "Settle the unfinished write",
		Body:  []string{fmt.Sprintf("%s %s %s: %s.", what, run.Kind, run.ID, d.detail(how))},
	}
	r.from = conf
	conf.write = &r
	conf.Yes = func(string) tea.Cmd { return tea.Sequence(r.cmd(), closeDialog(d)) }
	return OpenDialog(conf)
}

func (d *recoveryDialog) detail(how safety.Strategy) string {
	switch how {
	case safety.Forward:
		return d.forward
	case safety.Back:
		return d.back
	}
	return "nothing is written"
}

// View shows the run and its records, with the ways to settle it pinned
// below them; h of 0 or less is no limit.
func (d *recoveryDialog) View(c *Context, w, h int) string {
	run := d.open.Run
	body := []string{c.Styles.Bold.Render("Unfinished write"), ""}
	head := fmt.Sprintf("%s %s, started %s, %s.", run.Kind, run.ID, run.Started.UTC().Format("2006-01-02 15:04 UTC"),
		plural(len(run.Ops), "record", "records"))
	if run.Err != "" {
		head += " It stopped: " + reviewLower(errors.New(run.Err)) + "."
	}
	body = append(body, wrap(head, w-2, "", "  ")...)
	in := d.open.Inspection
	if in == nil {
		msg := "Its records could not be read again"
		if d.open.Err != nil {
			msg += ": " + plain(d.open.Err)
		}
		body = append(body, "", msg+". Reload once the mouse answers.")
	} else {
		body = append(body, "", fmt.Sprintf("%-9s  %-4s  %s", "Record", "Now", "What"))
		for _, e := range in.Extents {
			body = append(body, fmt.Sprintf("%-9s  %-4s  %s", e.Extent, e.Class, e.Desc))
		}
		body = append(body, "")
		body = append(body, wrap("old: as before the write; new: as it meant to leave; mid: a step in between; torn: neither, the write was cut short.", w-2, "", "")...)
	}
	pinned := []string{"",
		fmt.Sprintf("f  finish: write what the run meant to leave (%s)", d.forward),
		fmt.Sprintf("b  roll back: write what the records held before it (%s)", d.back)}
	if d.torn() {
		pinned = append(pinned, "l  leave: refused while a record is torn")
	} else {
		pinned = append(pinned, "l  leave: mark it settled; nothing is written")
	}
	pinned = append(pinned, "esc  later; writes stay blocked")
	if h <= 0 {
		return indentLines(append(body, pinned...), " ")
	}
	return indentLines(d.page.frame(c, body, pinned, -1, h), " ")
}

func (d *recoveryDialog) Hints(c *Context) []key.Binding {
	out := []key.Binding{hint("f", "finish"), hint("b", "roll back"), hint("l", "leave"), hint("esc", "later")}
	return append(out, d.page.hints()...)
}
