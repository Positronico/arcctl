package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

func (d *reviewDialog) View(c *Context, w, h int) string {
	tw := max(20, w-2)
	var body, pinned []string
	focus := -1
	switch d.step {
	case reviewPlan, reviewChecking:
		body, focus = d.planLines(c, tw)
	case reviewConfirm:
		body, pinned = d.confirmLines(c, tw)
	case reviewRunning:
		body = d.runningLines(c, tw)
	case reviewPartial:
		body = d.partialLines(c, tw)
	default:
		body = d.doneLines(c, tw)
	}
	return indentLines(d.frame(c, body, pinned, focus, h), " ")
}

// planLines is the review: where the plan goes, whether it can go, the
// staged edits, the ops, the web-app warnings and the checks before a write.
func (d *reviewDialog) planLines(c *Context, w int) ([]string, int) {
	st := c.Styles
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	title := "Review & Apply"
	switch {
	case d.restore != nil:
		title = "Review the restore"
	case d.kind == safety.KindRevert && d.target != nil && d.target.Kind == safety.KindRevert:
		title = "Review: undo the last revert"
	case d.kind == safety.KindRevert:
		title = "Review the revert"
	}
	title = st.Bold.Render(title)
	if d.planErr == nil && len(d.plan.Ops) > 0 {
		pk := 0
		for _, r := range d.rows {
			pk += r.packets
		}
		title += fmt.Sprintf("  %s, %s", plural(len(d.plan.Ops), "record", "records"), plural(pk, "packet", "packets"))
	}
	add(title)
	add(wrap(d.deviceLine(c), w, "", "  ")...)
	add(d.statusLines(c, w)...)
	focus := -1
	switch {
	case d.restore != nil:
		add("")
		add(d.restoreLines(c, w)...)
	case d.kind == safety.KindRevert:
		add("")
		add(d.revertLines(c, w)...)
	default:
		add("", st.Bold.Render(fmt.Sprintf("Staged edits (%d)", len(d.edits))))
		for i, e := range d.edits {
			line := "  " + reviewEditName(c, e)
			if i == d.cursor {
				focus = len(out)
				line = st.Selected.Render(c.Glyphs.Cursor + " " + reviewEditName(c, e))
			}
			add(line)
			if why, ok := d.refused[e.Key]; ok {
				add(wrap("refused: "+why, w, "    ", "    ")...)
			}
		}
		if len(d.edits) == 0 {
			add(st.Faint.Render("  nothing is staged"))
		}
	}
	if len(d.rows) > 0 {
		add("", st.Bold.Render("Plan"))
		add(d.opLines(c, w)...)
		if len(d.warnings) > 0 {
			add("", st.Warn.Render("Web app")+" "+st.Faint.Render("(the last-resort fallback) warnings:"))
			for _, wn := range d.warnings {
				add(wrap(wn.Text, w, "  "+st.Warn.Render("!")+" ", "    ")...)
			}
		}
		add("", st.Bold.Render("Before writing"))
		add(d.checkLines(c, w)...)
	}
	if d.restore != nil {
		add(d.restoreLeftLines(c, w)...)
	}
	return out, focus
}

func (d *reviewDialog) deviceLine(c *Context) string {
	sn := c.Snapshot
	parts := []string{reviewDevice(c), "profile " + reviewProfile(c.MouseOptions().Profile)}
	if v := sn.Versions.Mouse; v != "" {
		parts = append(parts, "firmware "+v)
	}
	if d.dryRun(c) {
		parts = append(parts, "dry run")
	}
	return strings.Join(parts, " "+c.Glyphs.Bullet+" ")
}

// statusLines say in one line whether the plan can go on, and why not.
func (d *reviewDialog) statusLines(c *Context, w int) []string {
	st := c.Styles
	style, text := st.Good, ""
	switch {
	case d.step == reviewChecking:
		style, text = st.Info, "Checking the mouse and the receiver"+c.Glyphs.Ellipsis
	case d.kind == safety.KindApply && d.restore == nil && len(d.edits) == 0:
		style, text = st.Faint, "Nothing is staged."
	case errors.Is(d.planErr, errNoMouse):
		style, text = st.Bad, "Blocked. "+plain(d.planErr)+"."
	case d.planErr != nil && d.kind == safety.KindRevert && !d.preview:
		style, text = st.Bad, "Cannot revert: "+reviewErrText(d.planErr)+"."
	case d.planErr != nil && d.restore != nil:
		style, text = st.Bad, "Cannot plan the restore: "+reviewErrText(d.planErr)+"."
	case d.planErr != nil && d.kind == safety.KindApply:
		style, text = st.Bad, "Cannot plan these edits: "+reviewErrText(d.planErr)+". d drops the selected edit."
	case d.planErr == nil && len(d.plan.Ops) == 0 && d.restore != nil:
		style, text = st.Info, "Nothing to write: the mouse already holds every record the backup restores."
	case d.planErr == nil && len(d.plan.Ops) == 0 && d.kind == safety.KindRevert:
		style, text = st.Info, "Nothing to write: the mouse already holds what the revert would write."
	case d.planErr == nil && len(d.plan.Ops) == 0:
		style, text = st.Info, "Nothing to write: the mouse already holds what the staged edits ask for. enter drops them."
	default:
		switch err := d.blocker(c); {
		case err != nil:
			style, text = st.Bad, "Blocked. "+plain(err)+"."
		case len(d.checked) > 0:
			style, text = st.Bad, fmt.Sprintf("Blocked: %s failed just now (below). enter checks again.", plural(len(d.checked), "check", "checks"))
		case d.dryRun(c):
			text = "Ready for a dry run: enter goes on; nothing reaches the mouse."
		case d.api != nil:
			text = "Ready: enter checks the mouse again, then asks you to confirm."
		default:
			text = "Ready: enter asks you to confirm."
		}
	}
	var out []string
	if d.note != "" {
		for _, l := range wrap(d.note, w, "", "") {
			out = append(out, st.Warn.Render(l))
		}
	}
	for _, l := range wrap(text, w, "", "") {
		out = append(out, style.Render(l))
	}
	return out
}

// opLines is the ops table: each op's record, phase and tier and what it
// does, then the record in words and in bytes, before and after.
func (d *reviewDialog) opLines(c *Context, w int) []string {
	st, arrow := c.Styles, " "+c.Glyphs.Arrow+" "
	out := []string{st.Faint.Render(fmt.Sprintf("  %3s  %-8s %-10s %-12s %s", "#", "Record", "Phase", "Tier", "What"))}
	pad := strings.Repeat(" ", 7)
	for _, r := range d.rows {
		op := r.op
		lead := fmt.Sprintf("  %3d  %-8s %-10s %s ", op.Seq, op.Extent, op.Phase, reviewTierStyle(st, op.Tier).Render(fmt.Sprintf("%-12s", op.Tier)))
		out = append(out, wrap(reviewDesc(op), w, lead, strings.Repeat(" ", ansi.StringWidth(lead)))...)
		words := ""
		if r.old != "" || r.new != "" {
			words = r.old + arrow + r.new
		}
		oldB, newB, pk := reviewBytes(c, op.Old, 8), reviewBytes(c, op.New, 8), plural(r.packets, "packet", "packets")
		raw := oldB + arrow + newB + "  " + pk
		if len(pad)+ansi.StringWidth(words)+3+ansi.StringWidth(raw) <= w && words != "" {
			out = append(out, pad+words+"   "+st.Faint.Render(raw))
			continue
		}
		if words != "" {
			out = append(out, wrap(words, w, pad, pad+"  ")...)
		}
		rawLines := []string{raw}
		switch {
		case len(pad)+ansi.StringWidth(raw) <= w:
		case len(pad)+2+ansi.StringWidth(newB+"  "+pk) <= w:
			rawLines = []string{oldB + arrow, "  " + newB + "  " + pk}
		default:
			rawLines = []string{oldB + arrow, "  " + newB, "  " + pk}
		}
		for _, l := range rawLines {
			out = append(out, pad+st.Faint.Render(l))
		}
	}
	return out
}

func reviewTierStyle(st Styles, t catalog.Tier) lipgloss.Style {
	switch t {
	case catalog.Verified:
		return st.Good
	case catalog.Untested:
		return st.Warn
	}
	return st.Bad
}

type reviewMark uint8

const (
	markOK reviewMark = iota
	markInfo
	markWarn
	markBad
)

func (d *reviewDialog) item(c *Context, w int, m reviewMark, text string) []string {
	st, g := c.Styles, c.Glyphs
	mark := [...]string{st.Good.Render(g.Check), g.Bullet, st.Warn.Render("!"), st.Bad.Render(g.Cross)}[m]
	return wrap(text, w, "  "+mark+" ", "    ")
}

// checkLines are what a write of this plan needs besides the confirmation:
// the session's state, the journal, other programs on the receiver, the
// preflight, the I1 backup and the tier gates.
func (d *reviewDialog) checkLines(c *Context, w int) []string {
	sn, dry := c.Snapshot, d.dryRun(c)
	var out []string
	add := func(m reviewMark, text string) { out = append(out, d.item(c, w, m, text)...) }
	if err := d.canWrite(c); err != nil && !errors.Is(err, ErrNothingPending) {
		add(markBad, plain(err)+".")
	} else {
		add(markOK, "The mouse is online and ready.")
	}
	switch js := sn.Journal; {
	case js == nil:
		add(markInfo, "The journal is checked when you apply.")
	case js.Err != nil:
		add(markBad, "The journal could not be read: "+reviewLower(js.Err)+".")
	case len(js.Open) == 0:
		add(markOK, "The journal holds no unfinished write.")
	}
	if !dry {
		switch cl := clientList(sn.Clients); {
		case cl == "":
			add(markOK, "No other program had the receiver open at the last scan.")
		case c.Gates.AllowForeignClient:
			add(markWarn, cl+" had the receiver open at the last scan; --allow-foreign-client lets the write go on.")
		default:
			add(markBad, cl+" had the receiver open at the last scan: quit it, or restart arcctl with --allow-foreign-client.")
		}
	}
	switch {
	case d.checkedOK:
		add(markOK, "The preflight passed just now: the same mouse and profile, every record as planned, the receiver free, no screen lock or Secure Input.")
	case len(d.checked) > 0:
		for _, f := range d.checked {
			add(markBad, plain(f)+".")
		}
	case d.kind == safety.KindRevert:
		add(markInfo, "Before the first packet arcctl checks again: the same mouse and profile, other programs on the receiver, the screen lock and Secure Input; then it reads each record and stops if one no longer holds what that write left.")
	default:
		add(markInfo, "Before the first packet arcctl checks again: the same mouse and profile, every record as planned, other programs on the receiver, the screen lock and Secure Input.")
	}
	if dry {
		add(markInfo, "Dry run: the packets go to an overlay and nothing reaches the mouse, so no backup is taken and the tier gates do not apply.")
		return out
	}
	add(markInfo, "A backup of the loaded configuration is saved before this session's first write. Every record is journaled before it is sent, then read back and compared.")
	if reviewFirstWrite(sn) {
		add(markWarn, "No write to this mouse is on record: arcctl first reads its whole configuration and saves a full backup. A partial one needs your consent.")
	}
	return append(out, d.gateLines(c, w, true)...)
}

// gateLines are the tier gates (§6.3 item 8) as this run's flags meet them,
// and with phrase what the confirmation asks for.
func (d *reviewDialog) gateLines(c *Context, w int, phrase bool) []string {
	fw := "this firmware"
	if v := c.Snapshot.Versions.Mouse; v != "" {
		fw = "firmware " + v
	}
	var out []string
	add := func(m reviewMark, text string) { out = append(out, d.item(c, w, m, text)...) }
	gate := func(t catalog.Tier, allowed bool, flag string) {
		n := d.count(t)
		switch {
		case n == 0:
		case allowed:
			add(markWarn, fmt.Sprintf("%s %s on %s; %s is set.", plural(n, "record is", "records are"), t, fw, flag))
		default:
			add(markBad, fmt.Sprintf("%s %s on %s: restart arcctl with %s to write them.", plural(n, "record is", "records are"), t, fw, flag))
		}
	}
	gate(catalog.Untested, c.Gates.AllowUntested, "--allow-untested")
	gate(catalog.Experimental, c.Gates.Experimental, "--experimental")
	switch {
	case !phrase:
	case d.phrase == "":
		add(markOK, "Every record is verified on "+fw+": y goes on.")
	default:
		add(markInfo, fmt.Sprintf("You type %q to write features no hardware test has verified.", d.phrase))
	}
	return out
}

// reviewFirstWrite reports whether the journal holds no write to this
// mouse, so the session takes a full backup before the first one (I1).
func reviewFirstWrite(sn *session.Snapshot) bool {
	js := sn.Journal
	return js != nil && js.Err == nil && js.Last == nil && len(js.Open) == 0
}

// confirmLines ask for y or the typed phrase; the prompt stays at the bottom.
func (d *reviewDialog) confirmLines(c *Context, w int) ([]string, []string) {
	st, sn := c.Styles, c.Snapshot
	n := plural(len(d.plan.Ops), "record", "records")
	var out []string
	add := func(m reviewMark, text string) { out = append(out, d.item(c, w, m, text)...) }
	if d.dryRun(c) {
		out = append(out, st.Bold.Render("Dry run of "+n+"?"), "")
		add(markInfo, "The packets go to an overlay; nothing reaches the mouse. The tabs then show what the write would leave.")
	} else {
		q := fmt.Sprintf("Write %s to %s?", n, reviewDevice(c))
		if d.kind == safety.KindRevert {
			q = fmt.Sprintf("Revert %s on %s?", n, reviewDevice(c))
		}
		out = append(out, st.Bold.Render(q), "")
		out = append(out, d.gateLines(c, w, false)...)
		add(markInfo, "A backup is saved first; every record is journaled, then read back and compared.")
		if reviewFirstWrite(sn) {
			add(markWarn, "No write to this mouse is on record: a full backup runs first. Its progress shows here.")
		}
	}
	if n := len(d.warnings); n > 0 {
		add(markWarn, fmt.Sprintf("%s: the web app cannot show or recreate what this plan writes (see the review).", plural(n, "web-app warning", "web-app warnings")))
	}
	if d.checkedOK {
		add(markOK, "The preflight passed just now; the write runs it again.")
	}
	var pinned []string
	switch {
	case d.needsPhrase(c):
		pinned = wrap(fmt.Sprintf("Type %q and press enter to write; esc goes back.", d.phrase), w, "", "")
		pinned = append(pinned, d.input.view(c))
	case d.dryRun(c):
		pinned = []string{"y runs it; n or esc goes back."}
	default:
		pinned = []string{"y writes; n or esc goes back."}
	}
	return append(out, ""), pinned
}
