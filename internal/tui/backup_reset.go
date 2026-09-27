package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

var (
	errNoReset    = errors.New("factory reset is not available in this session")
	errResetMoved = errors.New("another mouse answers now; close the factory reset and open it again")
)

// reset opens the factory reset (§7.8, D4): the explanation of the gate
// while hardware test H7 has not recorded the reset for this model and
// firmware, and otherwise its review.
func (t *BackupTab) reset(c *Context) tea.Cmd {
	if err := safety.ResetGate(c.Model(), c.Snapshot.Versions.Mouse, c.verified); err != nil {
		return OpenDialog(&resetLocked{why: err})
	}
	rs, ok := t.api.(session.Resetter)
	switch {
	case !ok:
		return Problem(errNoReset)
	case t.busy != "":
		return Problem(errBusy)
	}
	if err := c.CanWrite(); err != nil {
		return Problem(err)
	}
	return OpenDialog(&resetDialog{api: rs, abort: t.api.Abort, dev: c.Snapshot.Identity, profile: c.MouseOptions().Profile})
}

// resetLocked says why the factory reset cannot be sent yet.
type resetLocked struct{ why error }

func (d *resetLocked) Update(c *Context, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc", "enter", "q":
			return CloseDialog
		}
	}
	return nil
}

func (d *resetLocked) View(c *Context, w, h int) string {
	tw := max(20, w-2)
	out := []string{c.Styles.Bold.Render("Factory reset: not available yet"), ""}
	for _, p := range []string{
		plain(d.why) + ".",
		"Hardware test H7 sends the reset once to a mouse with this firmware, after a verified full backup, and records what it " +
			"clears and whether the mouse stays paired. Until that record exists for this model and firmware, arcctl does not send it.",
		"To put settings back now, select a backup and press w. The vendor web app can reset the mouse too: take a full backup here " +
			"first (f), and see \"Re-pairing without arcctl\" in docs/safety.md in case the mouse needs pairing again.",
	} {
		out = append(out, wrap(p, tw, "", "")...)
		out = append(out, "")
	}
	return indentLines(out, " ")
}

func (d *resetLocked) Hints(c *Context) []key.Binding {
	return []key.Binding{hint("enter/esc", "close")}
}

// resetDialog is the review of a factory reset: what it does and what gates
// it, the session's preflight, the typed phrase, then the reset, which the
// dialog starts once, through the session's Reset (its preflight, its full
// backup, the journal and the reload), and its outcome.
type resetDialog struct {
	api     session.Resetter
	abort   func()
	dev     plan.Identity
	profile *byte
	step    reviewStep

	gen       int
	checked   []error
	checkedOK bool
	input     reviewInput
	pager

	gates    safety.Gates
	stopping bool
	out      session.ResetOutcome
	err      error
}

type (
	resetCheckMsg struct {
		d   *resetDialog
		gen int
		err error
	}
	// resetDoneMsg ends the reset a resetDialog started; the Backup tab
	// lists the backup it saved.
	resetDoneMsg struct {
		d   *resetDialog
		out session.ResetOutcome
		err error
	}
)

func (d *resetDialog) Update(c *Context, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case resetCheckMsg:
		if msg.d == d && msg.gen == d.gen && d.step == reviewChecking {
			d.checked = reviewFailures(msg.err)
			d.checkedOK = len(d.checked) == 0
			if d.checkedOK {
				d.step = reviewConfirm
			} else {
				d.back()
			}
		}
	case resetDoneMsg:
		if msg.d == d {
			d.step, d.out, d.err, d.scroll = reviewDone, msg.out, msg.err, 0
		}
	case tea.PasteMsg:
		if d.step == reviewConfirm {
			d.input.pasted = true
		}
	case tea.KeyPressMsg:
		return d.key(c, msg)
	}
	return nil
}

// Busy names the reset while it runs, so quitting asks first.
func (d *resetDialog) Busy() string {
	if d.step == reviewRunning && !d.gates.DryRun {
		return "the factory reset"
	}
	return ""
}

func (d *resetDialog) back() {
	d.step, d.input, d.scroll = reviewPlan, reviewInput{}, 0
}

func (d *resetDialog) dryRun(c *Context) bool { return c.Mode == ModeDryRun }

// blocker says why no reset can start now, whatever the preflight finds.
func (d *resetDialog) blocker(c *Context) error {
	if err := safety.ResetGate(c.Model(), c.Snapshot.Versions.Mouse, c.verified); err != nil {
		return err
	}
	if c.Snapshot.Identity.Key() != d.dev.Key() {
		return errResetMoved
	}
	return c.CanWrite()
}

func (d *resetDialog) gatesFor(c *Context, typed string) safety.Gates {
	g := c.Gates
	g.Confirm = typed
	if d.dryRun(c) {
		g.DryRun = true
	}
	return g
}

func (d *resetDialog) key(c *Context, k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	switch d.step {
	case reviewPlan:
		if d.pager.key(s) {
			return nil
		}
		switch s {
		case "esc", "q":
			return CloseDialog
		case "enter":
			return d.check(c)
		}
	case reviewChecking:
		if s == "esc" {
			d.gen++
			d.back()
		}
	case reviewConfirm:
		switch {
		case s == "esc":
			d.back()
		case !d.dryRun(c):
			if d.input.key(k, safety.ResetPhrase) {
				return d.send(c, safety.ResetPhrase)
			}
		case s == "y" || s == "Y":
			return d.send(c, "")
		case s == "n" || s == "N":
			d.back()
		}
	case reviewRunning:
		switch s {
		case "s":
			if !d.stopping {
				d.stopping = true
				d.abort()
			}
		case "q", "esc":
			return Notice("The factory reset is running; wait for it to end. s stops it where that is still possible.")
		}
	case reviewDone:
		if d.pager.key(s) {
			return nil
		}
		switch s {
		case "enter":
			if !d.out.Sent && !d.out.DryRun {
				d.back()
				return nil
			}
			return CloseDialog
		case "esc", "q":
			return CloseDialog
		}
	}
	return nil
}

// check runs the session's preflight of the reset; the phrase is asked for
// after it.
func (d *resetDialog) check(c *Context) tea.Cmd {
	if err := d.blocker(c); err != nil {
		return Problem(err)
	}
	d.step, d.checked, d.checkedOK = reviewChecking, nil, false
	d.gen++
	api, dev, profile, g, gen := d.api, d.dev, d.profile, d.gatesFor(c, ""), d.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewCheckWait)
		defer cancel()
		return resetCheckMsg{d: d, gen: gen, err: api.PreflightReset(ctx, dev, profile, g)}
	}
}

// send starts the reset the user just confirmed. Only the confirmation
// leads here, and the dialog never comes back to it once the reset may
// have gone out.
func (d *resetDialog) send(c *Context, typed string) tea.Cmd {
	if err := d.blocker(c); err != nil {
		d.back()
		return Problem(err)
	}
	d.step, d.scroll, d.stopping = reviewRunning, 0, false
	d.gates = d.gatesFor(c, typed)
	api, dev, profile, g := d.api, d.dev, d.profile, d.gates
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), backupWait)
		defer cancel()
		out, err := api.Reset(ctx, dev, profile, g)
		return resetDoneMsg{d: d, out: out, err: err}
	}
}

func (d *resetDialog) View(c *Context, w, h int) string {
	tw := max(20, w-2)
	var body, pinned []string
	switch d.step {
	case reviewPlan, reviewChecking:
		body = d.planLines(c, tw)
	case reviewConfirm:
		body, pinned = d.confirmLines(c, tw)
	case reviewRunning:
		body = d.runningLines(c, tw)
	default:
		body = d.doneLines(c, tw)
	}
	return indentLines(d.frame(c, body, pinned, -1, h), " ")
}

func resetItem(c *Context, w int, m reviewMark, text string) []string {
	st, g := c.Styles, c.Glyphs
	mark := [...]string{st.Good.Render(g.Check), g.Bullet, st.Warn.Render("!"), st.Bad.Render(g.Cross)}[m]
	return wrap(text, w, "  "+mark+" ", "    ")
}

func (d *resetDialog) deviceLine(c *Context) string {
	parts := []string{reviewDevice(c), "profile " + reviewProfile(d.profile)}
	if v := c.Snapshot.Versions.Mouse; v != "" {
		parts = append(parts, "firmware "+v)
	}
	if d.dryRun(c) {
		parts = append(parts, "dry run")
	}
	return strings.Join(parts, " "+c.Glyphs.Bullet+" ")
}

func (d *resetDialog) planLines(c *Context, w int) []string {
	st, sn := c.Styles, c.Snapshot
	var out []string
	add := func(m reviewMark, text string) { out = append(out, resetItem(c, w, m, text)...) }
	out = append(out, st.Bold.Render("Review the factory reset"))
	out = append(out, wrap(d.deviceLine(c), w, "", "  ")...)
	switch err := d.blocker(c); {
	case d.step == reviewChecking:
		out = append(out, st.Info.Render("Checking the mouse and the receiver"+c.Glyphs.Ellipsis))
	case err != nil:
		out = append(out, wrap("Blocked. "+plain(err)+".", w, "", "")...)
	case len(d.checked) > 0:
		out = append(out, st.Bad.Render("Blocked by the checks below."))
	default:
		out = append(out, st.Good.Render("Ready: enter runs the checks, then asks you to confirm."))
	}

	out = append(out, "", st.Bold.Render("What it does"))
	add(markInfo, "First a full backup: arcctl reads the whole configuration afresh and saves it, labelled \""+
		session.LabelBeforeReset+"\". The reset stops there if any range cannot be read.")
	add(markInfo, "Then one factory reset (cmd 9), sent once and never again; arcctl waits up to 3 s for its reply.")
	add(markInfo, "With or without a reply, arcctl reads the whole configuration again and compares it with the backup: "+
		"the comparison says what the reset changed.")
	add(markInfo, "Nothing else is sent: no long-range or receiver-light command follows it.")
	if v, ok := safety.ResetVerification(c.Model(), sn.Versions.Mouse, c.verified); ok {
		add(markOK, fmt.Sprintf("Hardware test %s recorded the reset on this model with firmware %s on %s.", v.Stage, v.Firmware, v.Date))
	}
	add(markWarn, "If the mouse stops answering afterwards, it may need pairing again: see \"Re-pairing without arcctl\" in "+
		"docs/safety.md. Keep another pointing device at hand; the mouse's Bluetooth channel keeps working.")
	add(markInfo, "The backup puts back what a restore writes: select it on this tab and press w.")
	add(markWarn, resetLeftOut(c))

	out = append(out, "", st.Bold.Render("Before sending"))
	if err := c.CanWrite(); err != nil {
		add(markBad, plain(err)+".")
	} else {
		add(markOK, "The mouse is online and ready.")
	}
	switch js := sn.Journal; {
	case js == nil:
		add(markInfo, "The journal is checked before the reset.")
	case js.Err != nil:
		add(markBad, "The journal could not be read: "+reviewLower(js.Err)+".")
	case len(js.Open) == 0:
		add(markOK, "The journal holds no unfinished write.")
	}
	if !d.dryRun(c) {
		switch cl := clientList(sn.Clients); {
		case cl == "":
			add(markOK, "No other program had the receiver open at the last scan.")
		case c.Gates.AllowForeignClient:
			add(markWarn, cl+" had the receiver open at the last scan; --allow-foreign-client lets the reset go on.")
		default:
			add(markBad, cl+" had the receiver open at the last scan: quit it, or restart arcctl with --allow-foreign-client.")
		}
	}
	switch {
	case d.checkedOK:
		add(markOK, "The preflight passed just now.")
	case len(d.checked) > 0:
		for _, f := range d.checked {
			add(markBad, plain(f)+".")
		}
	default:
		add(markInfo, "Before the packet arcctl checks again, before and after the backup: the same mouse, profile and firmware, "+
			"other programs on the receiver, the screen lock and Secure Input.")
	}
	if d.dryRun(c) {
		add(markInfo, "Dry run: arcctl runs the checks and shows the packet; nothing is sent, saved or journaled.")
	} else {
		add(markInfo, fmt.Sprintf("You type %q to send it.", safety.ResetPhrase))
	}
	return out
}

func (d *resetDialog) confirmLines(c *Context, w int) ([]string, []string) {
	st := c.Styles
	var out []string
	add := func(m reviewMark, text string) { out = append(out, resetItem(c, w, m, text)...) }
	if d.dryRun(c) {
		out = append(out, st.Bold.Render("Dry run of the factory reset?"), "")
		add(markInfo, "Nothing is sent: arcctl runs the checks and shows the packet it would send.")
		return append(out, ""), []string{"y runs it; n or esc goes back."}
	}
	out = append(out, st.Bold.Render("Reset "+reviewDevice(c)+" to its factory settings?"), "")
	add(markWarn, "Any setting may go back to its factory value. The full backup taken first is the way back.")
	add(markWarn, resetLeftOut(c))
	add(markOK, "The preflight passed just now; the reset runs it again.")
	pinned := wrap(fmt.Sprintf("Type %q and press enter to reset the mouse; esc goes back.", safety.ResetPhrase), w, "", "")
	return append(out, ""), append(pinned, d.input.view(c))
}

// resetLeftOut says what a restore cannot put back after a reset.
func resetLeftOut(c *Context) string {
	m := c.Model()
	if m == nil {
		return "A restore writes back only what arcctl writes."
	}
	return "A restore never writes back " + andList(backup.RestoreLeavesOut(m, c.MouseOptions())) +
		". If the reset changes them, they stay as it leaves them; hardware test H7 records which it changes."
}

func andList(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

func (d *resetDialog) runningLines(c *Context, w int) []string {
	st, p := c.Styles, c.Snapshot.Progress
	head := "Resetting " + reviewDevice(c)
	if d.gates.DryRun {
		head = "Dry run of the factory reset"
	}
	out := []string{st.Bold.Render(head)}
	progress := func(text string) {
		out = append(out, wrap(text, w, "", "")...)
		if p.Total > 0 {
			out = append(out, bar(c, p.Done, p.Total, 20)+fmt.Sprintf("  %d of %d", p.Done, p.Total))
		}
	}
	switch p.Job {
	case "backup":
		progress("1 of 3: backing up the whole configuration.")
	case "reset":
		out = append(out, "2 of 3: the factory reset is going out; arcctl waits up to 3 s for its reply.")
	case "reset check":
		progress("3 of 3: reading the whole configuration again, to compare it with the backup.")
	default:
		out = append(out, "Checking the mouse and the receiver"+c.Glyphs.Ellipsis)
	}
	if p.Paused {
		out = append(out, wrap("Waiting: the mouse does not answer. Move it or press a button. If it stays silent after the reset, "+
			"it may need pairing again (docs/safety.md).", w, st.Warn.Render("!")+" ", "  ")...)
	}
	if d.stopping {
		out = append(out, wrap("Stopping: before the reset goes out, nothing is sent; after it, arcctl stops waiting for the mouse.", w, "", "")...)
	}
	return out
}

func (d *resetDialog) doneLines(c *Context, w int) []string {
	st, o := c.Styles, d.out
	var out []string
	para := func(text string) { out = append(out, wrap(text, w, "", "")...) }
	add := func(m reviewMark, text string) { out = append(out, resetItem(c, w, m, text)...) }
	backupLine := func() {
		if o.Backup != "" {
			out = append(out, "Backup from before the reset:", "  "+midCut(c, o.Backup, w-2))
		}
	}
	switch {
	case o.DryRun && d.err == nil:
		out = append(out, st.Good.Render("Dry run done: the checks passed."))
		para("Nothing was sent, saved or journaled. The reset would send one packet:")
		for _, pk := range o.Packets {
			out = append(out, "  "+fmt.Sprintf("% x", pk[:]))
		}
		return out
	case !o.Sent && errors.Is(d.err, safety.ErrAborted):
		out = append(out, st.Warn.Render("Stopped: the factory reset was not sent."))
		backupLine()
		return append(out, "", "enter goes back to the review.")
	case !o.Sent:
		out = append(out, st.Bad.Render("Refused: the factory reset was not sent."))
		for _, f := range reviewFailures(d.err) {
			add(markBad, plain(f)+".")
		}
		if len(reviewFailures(d.err)) == 0 {
			add(markBad, plain(d.err)+".")
		}
		backupLine()
		return append(out, "", "enter goes back to the review.")
	}
	switch o.Verdict {
	case safety.VerdictChanged:
		out = append(out, st.Good.Render("The factory reset took effect."))
		para(fmt.Sprintf("%s of the configuration now %s from the backup: %s.", plural(len(o.Changed), "range", "ranges"),
			map[bool]string{true: "differs", false: "differ"}[len(o.Changed) == 1], resetRanges(o.Changed, 6)))
	case safety.VerdictUnchanged:
		out = append(out, st.Warn.Render("The factory reset changed nothing."))
		para("Every byte read again equals the backup: the mouse ignored the reset, or held its factory settings already.")
	default:
		out = append(out, st.Bad.Render("The factory reset went out, but the mouse was not read again."))
		if d.err != nil {
			para(plain(d.err) + ".")
		}
		para("arcctl loads the mouse when it answers. If it stays silent, pair it again: see \"Re-pairing without arcctl\" in docs/safety.md.")
	}
	if len(o.Unread) > 0 {
		add(markWarn, fmt.Sprintf("%s could not be read again: %s.", plural(len(o.Unread), "range", "ranges"), resetRanges(o.Unread, 6)))
	}
	add(markInfo, "The mouse's reply: "+resetReplyText(o.Reply)+".")
	if o.Run != "" {
		add(markInfo, "Journal run "+o.Run+".")
	}
	out = append(out, "")
	backupLine()
	para("It is the newest backup on this tab: d compares it with the mouse record by record, and w restores it.")
	if d.err != nil && o.Verdict != safety.VerdictUnchecked {
		out = append(out, "")
		para(plain(d.err) + ".")
	}
	return out
}

func resetReplyText(r safety.Reply) string {
	switch r {
	case safety.ReplyAck:
		return "acknowledged"
	case safety.ReplyNAK:
		return "refused (NAK)"
	case safety.ReplyNone:
		return "none within 3 s"
	case safety.ReplyError:
		return "none; the OS reported a write error, so the packet may not have gone out"
	}
	return "not known"
}

// resetRanges lists up to n byte ranges, and how many more there are.
func resetRanges(es []flash.Extent, n int) string {
	var parts []string
	for i, e := range es {
		if i == n {
			parts = append(parts, fmt.Sprintf("and %d more", len(es)-n))
			break
		}
		parts = append(parts, e.String())
	}
	return strings.Join(parts, ", ")
}

func (d *resetDialog) Hints(c *Context) []key.Binding {
	switch d.step {
	case reviewPlan:
		return append(d.pager.hints(), hint("enter", "check"), hint("esc", "close"))
	case reviewChecking:
		return []key.Binding{hint("esc", "back")}
	case reviewConfirm:
		if d.dryRun(c) {
			return []key.Binding{hint("y", "run"), hint("n/esc", "back")}
		}
		return []key.Binding{hint("enter", "confirm"), hint("esc", "back")}
	case reviewRunning:
		return []key.Binding{hint("s", "stop")}
	}
	if !d.out.Sent && !d.out.DryRun {
		return append(d.pager.hints(), hint("enter", "back to the review"), hint("esc", "close"))
	}
	return append(d.pager.hints(), hint("enter/esc", "close"))
}
