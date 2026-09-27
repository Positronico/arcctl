package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// reviewRun is the write the review started: its gates, its progress from
// the executor's events, and how it ended.
type reviewRun struct {
	gates    safety.Gates
	w        *writing
	done     *WriteDoneMsg
	partial  *safety.PartialError
	stopping bool
	// rec is the prompt for the run the journal holds unfinished after the
	// write stopped; nil until the journal check lists it.
	rec   *recoveryDialog
	recIn *safety.Inspection
}

func (r *reviewRun) begin(w writeRequest) {
	r.w = newWriting(w, nil)
	r.done, r.partial, r.stopping, r.rec, r.recIn = nil, nil, false, nil, nil
}

func (d *reviewDialog) event(e safety.OpEvent) {
	if d.step == reviewRunning && d.run.w != nil {
		d.run.w.add(e)
	}
}

// reviewEnd is how a write ended, as the review tells it.
type reviewEnd uint8

const (
	reviewEndVerified reviewEnd = iota
	reviewEndStopped            // the executor stopped at an op; the journal may hold the run open
	reviewEndAborted            // the user stopped it before its first packet
	reviewEndRefused            // nothing was sent: the preflight or a gate refused it
	reviewEndFailed             // anything else
)

func (r *reviewRun) end() reviewEnd {
	return writeEnd(*r.done)
}

func writeEnd(d WriteDoneMsg) reviewEnd {
	var stop *safety.StopError
	var pre *safety.PreflightError
	switch err := d.Err; {
	case err == nil:
		return reviewEndVerified
	case errors.As(err, &stop):
		return reviewEndStopped
	case errors.Is(err, safety.ErrAborted) && d.Outcome.Run == "":
		return reviewEndAborted
	case errors.As(err, &pre):
		return reviewEndRefused
	}
	return reviewEndFailed
}

// again reports whether the write can be reviewed again: nothing was sent,
// and there is still something to write.
func (d *reviewDialog) again(c *Context) bool {
	switch d.run.end() {
	case reviewEndRefused, reviewEndAborted:
		return d.kind == safety.KindRevert || d.restore != nil || c.Pending.Len() > 0
	}
	return false
}

// finish takes the end of the write. A first full backup that came out
// partial asks whether to write anyway (I1); anything else is the result.
func (d *reviewDialog) finish(c *Context, m WriteDoneMsg) {
	d.run.done = &m
	var pe *safety.PartialError
	if errors.As(m.Err, &pe) && len(reviewFailures(m.Err)) == 1 {
		d.run.partial = pe
		d.step = reviewPartial
		return
	}
	d.step, d.scroll = reviewDone, 0
	if d.run.end() == reviewEndRefused {
		d.checked, d.checkedOK = reviewFailures(m.Err), false
	}
	d.run.syncRecovery(c)
}

// syncRecovery follows the journal after a write that did not end verified:
// once the reload lists the run as unfinished, the review offers to settle
// it (§6.5).
func (r *reviewRun) syncRecovery(c *Context) {
	if r.done == nil || r.done.Err == nil || r.done.Outcome.Run == "" || r.gates.DryRun {
		return
	}
	var open *session.OpenRun
	if js := c.Snapshot.Journal; js != nil {
		i := slices.IndexFunc(js.Open, func(o session.OpenRun) bool { return o.Run.ID == r.done.Outcome.Run })
		if i >= 0 {
			open = &js.Open[i]
		}
	}
	switch {
	case open == nil:
		r.rec, r.recIn = nil, nil
	case r.rec == nil || open.Inspection != r.recIn:
		r.rec, r.recIn = newRecoveryDialog(c, *open), open.Inspection
	}
}

func (d *reviewDialog) runningKey(c *Context, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		return tea.Batch(CloseDialog, Notice("The write goes on; its progress shows above the tabs."))
	case "q":
		return askQuit
	case "s":
		if d.api != nil && !d.run.stopping {
			d.api.Abort()
			d.run.stopping = true
		}
	default:
		d.pager.key(k.String())
	}
	return nil
}

func (d *reviewDialog) partialKey(c *Context, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y", "Y":
		d.run.gates.AcceptPartial = true
		return d.start()
	case "n", "N", "esc":
		d.back("Nothing was written. The partial backup stays at " + d.run.partial.Path + ".")
	}
	return nil
}

func (d *reviewDialog) doneKey(c *Context, k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if d.pager.key(s) {
		return nil
	}
	switch s {
	case "esc", "q":
		return CloseDialog
	case "enter":
		if d.again(c) {
			checked, gen := d.checked, d.gen
			d.back("")
			d.sync(c)
			if d.gen == gen {
				d.checked = checked
			}
			return nil
		}
		return CloseDialog
	case "f", "b", "l":
		if d.run.rec != nil {
			return d.run.rec.Update(c, k)
		}
	}
	return nil
}

func (r *reviewRun) hints() []key.Binding {
	switch {
	case r.done == nil:
	case r.end() == reviewEndRefused || r.end() == reviewEndAborted:
		return []key.Binding{hint("enter", "back to the review"), hint("esc", "close")}
	case r.rec != nil:
		return []key.Binding{hint("f", "finish"), hint("b", "roll back"), hint("l", "leave"), hint("esc", "later")}
	}
	return []key.Binding{hint("enter/esc", "close")}
}

// runningLines show the write as it goes: the first full backup when I1
// asks for one, then each op's step.
func (d *reviewDialog) runningLines(c *Context, w int) []string {
	r, st, sn := &d.run, c.Styles, c.Snapshot
	n := plural(len(d.plan.Ops), "record", "records")
	what, prep := "Applying", "to"
	switch {
	case d.restore != nil:
		what = "Restoring"
	case d.kind == safety.KindRevert:
		what, prep = "Reverting", "on"
	}
	head := fmt.Sprintf("%s %s %s %s", what, n, prep, reviewDevice(c))
	if r.gates.DryRun {
		head = fmt.Sprintf("Dry run: %s %s; nothing reaches the mouse", strings.ToLower(what), n)
	}
	out := []string{st.Bold.Render(head)}
	started := r.w != nil && r.w.cur >= 0
	switch p := sn.Progress; {
	case started:
		out = append(out, r.w.progress()+".")
	case p.Job == "backup":
		out = append(out, "Backing up the whole configuration before the first write.")
		if p.Total > 0 {
			out = append(out, bar(c, p.Done, p.Total, 20)+fmt.Sprintf("  %d of %d", p.Done, p.Total))
		}
		if p.Paused {
			out = append(out, st.Warn.Render("Paused: the mouse is asleep. Move it; the backup resumes where it stopped."))
		}
	default:
		out = append(out, "Checking the mouse and the receiver before the first packet"+c.Glyphs.Ellipsis)
	}
	if r.stopping {
		out = append(out, st.Warn.Render("Stopping after the current record."))
	}
	if r.w != nil && r.w.paused != nil {
		out = append(out, wrap("Paused: "+plain(r.w.paused)+". It goes on when that clears.", w, "", "  ")...)
	}
	out = append(out, "")
	return append(out, d.stepLines(c, w)...)
}

// stepLines list the ops with where each got to.
func (d *reviewDialog) stepLines(c *Context, w int) []string {
	r, st, g := &d.run, c.Styles, c.Glyphs
	if r.w == nil {
		return nil
	}
	out := []string{st.Faint.Render(fmt.Sprintf("     %3s  %-8s %-10s %-13s %s", "#", "Record", "Phase", "State", "What"))}
	for i, s := range r.w.steps {
		mark, style := " ", st.Faint
		state := "planned"
		switch {
		case s.kind == safety.EventVerified:
			mark, style = g.Check, st.Good
		case s.kind == safety.EventFailed:
			mark, style = g.Cross, st.Bad
		case i == r.w.cur:
			mark, style = g.Cursor, st.Warn
		}
		switch {
		case s.kind == safety.EventChunk:
			state = fmt.Sprintf("writing %d/%d", s.chunk, s.chunks)
		case s.kind != 0:
			state = stepNames[s.kind]
		}
		lead := fmt.Sprintf("  %s  %3d  %-8s %-10s %s ", style.Render(mark), s.op.Seq, s.op.Extent, s.op.Phase, style.Render(fmt.Sprintf("%-13s", state)))
		out = append(out, wrap(reviewDesc(s.op), w, lead, strings.Repeat(" ", ansi.StringWidth(lead)))...)
	}
	return out
}

func (d *reviewDialog) partialLines(c *Context, w int) []string {
	pe, st := d.run.partial, c.Styles
	ranges := make([]string, len(pe.Missing))
	for i, m := range pe.Missing {
		ranges[i] = m.String()
	}
	out := []string{st.Warn.Render("The full backup is partial"), ""}
	for _, p := range []string{
		"No write to this mouse is on record, so arcctl read its whole configuration and saved it before the first write.",
		fmt.Sprintf("The backup at %s lacks %s that could not be read: %s. A restore from it cannot bring those bytes back.",
			pe.Path, plural(len(pe.Missing), "range", "ranges"), strings.Join(ranges, ", ")),
		"Nothing was written yet.",
	} {
		out = append(out, wrap(p, w, "", "")...)
		out = append(out, "")
	}
	return append(out, "y writes anyway with this backup; n or esc stops here.")
}

// doneLines tell how the write ended and, after a stop, what the journal
// holds and how to settle it.
func (d *reviewDialog) doneLines(c *Context, w int) []string {
	r, st := &d.run, c.Styles
	out := d.outcomeLines(c, w)
	if r.done.Outcome.Overlap {
		out = append(out, "")
		out = append(out, wrap("A push re-read the last record while it was written (a button press on the mouse?). "+
			"arcctl reloads it; check that it holds what you wanted.", w, st.Warn.Render("!")+" ", "  ")...)
	}
	switch r.end() {
	case reviewEndVerified:
		if r.gates.DryRun {
			out = append(out, "")
			out = append(out, reviewPacketLines(r.done.Outcome.Packets, d.plan)...)
		}
	case reviewEndRefused, reviewEndAborted:
		if d.again(c) {
			out = append(out, "")
			out = append(out, wrap("enter goes back to the review, which plans again against what the mouse holds now.", w, "", "")...)
		}
		return out
	default:
		out = append(out, "")
		out = append(out, r.journalLines(c, w)...)
	}
	out = append(out, "")
	return append(out, d.stepLines(c, w)...)
}

func (d *reviewDialog) outcomeLines(c *Context, w int) []string {
	r, p := &d.run, d.plan
	st, out := c.Styles, r.done.Outcome
	para := func(text string) []string { return wrap(text, w, "", "") }
	switch r.end() {
	case reviewEndVerified:
		if r.gates.DryRun {
			lines := []string{st.Good.Render(fmt.Sprintf("Dry run done: %d of %d records verified against the overlay.", out.Verified, out.Ops))}
			return append(lines, para("Nothing reached the mouse. The tabs now show what the write would leave.")...)
		}
		done, next := "Applied", "U reverts it."
		switch {
		case d.restore != nil:
			done = "Restored"
		case d.kind == safety.KindRevert:
			done, next = "Reverted", "U undoes this revert."
		}
		lines := []string{st.Good.Render(fmt.Sprintf("%s: %d of %d records verified.", done, out.Verified, out.Ops))}
		if out.Run != "" {
			lines = append(lines, para("Run "+out.Run+" is in the journal; "+next)...)
		}
		if len(out.Backups) > 0 {
			lines = append(lines, "Saved before writing:")
			for _, b := range out.Backups {
				lines = append(lines, "  "+midCut(c, b, w-2))
			}
		}
		return lines
	case reviewEndAborted:
		return []string{st.Warn.Render("Stopped at your request: nothing was written to the mouse.")}
	case reviewEndStopped:
		var stop *safety.StopError
		errors.As(r.done.Err, &stop)
		n := max(out.Ops, len(p.Ops))
		lines := []string{st.Bad.Render(fmt.Sprintf("Stopped: %d of %d records verified.", out.Verified, n))}
		return append(lines, para(reviewStopText(stop, n))...)
	case reviewEndRefused:
		lines := []string{st.Bad.Render("Refused: nothing was sent to the mouse.")}
		for _, f := range reviewFailures(r.done.Err) {
			lines = append(lines, wrap(plain(f)+".", w, st.Bad.Render(c.Glyphs.Cross)+" ", "  ")...)
		}
		return lines
	}
	lines := []string{st.Bad.Render("The write failed.")}
	return append(lines, para(plain(r.done.Err)+".")...)
}

func reviewStopText(e *safety.StopError, ops int) string {
	s := fmt.Sprintf("Record %d of %d, %s (%s, %s phase), ", e.Op.Seq, ops, reviewDesc(e.Op), e.Op.Extent, e.Op.Phase)
	if e.Written {
		s += fmt.Sprintf("stopped with %d of %s acknowledged", e.Chunks, plural(reviewPackets(e.Op), "packet", "packets"))
	} else {
		s += "stopped before any of its packets went out"
	}
	s += ": " + reviewLower(e.Err) + "."
	switch e.Class {
	case safety.ClassOld:
		s += " It still holds its old bytes."
	case safety.ClassNew:
		s += " It holds its new bytes."
	case safety.ClassMid:
		s += " It holds a step in between: a binding disabled while its body is rewritten."
	case safety.ClassTorn:
		s += " It holds neither its old nor its new bytes: it is torn."
	default:
		if e.Written {
			s += " It could not be read again."
		}
	}
	return s
}

// journalLines say what the journal holds after a write that stopped.
func (r *reviewRun) journalLines(c *Context, w int) []string {
	sn := c.Snapshot
	switch {
	case r.gates.DryRun:
		return wrap("A dry run keeps no journal, and nothing reached the mouse.", w, "", "")
	case r.done.Outcome.Run == "":
		return wrap("The journal holds nothing of this write.", w, "", "")
	case r.rec != nil:
		lines := strings.Split(r.rec.View(c, w, 0), "\n")
		for i, l := range lines {
			lines[i] = strings.TrimPrefix(l, " ")
		}
		return lines
	case sn.Progress.Job != "" || sn.State == session.Applying:
		return []string{"Reading the records again and checking the journal" + c.Glyphs.Ellipsis}
	}
	return wrap("The journal holds nothing unfinished: every record the write touched holds its old or its new bytes.", w, "", "")
}

// reviewPacketLines list the packets a dry run sent to the overlay, each
// under the op whose record it writes. They come in op order, a record's
// chunks in a row.
func reviewPacketLines(packets []wire.Packet, p plan.Plan) []string {
	out := []string{fmt.Sprintf("%s sent to the overlay instead of the mouse:", plural(len(packets), "packet", "packets"))}
	i := 0
	line := func(op *plan.Op, pk wire.Packet) string {
		if op == nil {
			return fmt.Sprintf("  %3s  %-8s %s", "", "", pk)
		}
		return fmt.Sprintf("  %3d  %-8s %s", op.Seq, op.Extent, pk)
	}
	for _, op := range p.Ops {
		for range reviewPackets(op) {
			if i < len(packets) && op.Extent.Contains(flash.Extent{Addr: int(packets[i].Addr()), Len: packets[i].Len()}) {
				out = append(out, line(&op, packets[i]))
				i++
			}
		}
	}
	for _, pk := range packets[i:] {
		out = append(out, line(nil, pk))
	}
	return out
}

func reviewDevice(c *Context) string {
	sn := c.Snapshot
	var parts []string
	if m := c.Model(); m != nil {
		parts = append(parts, modelName(m)+" "+modelKey(m))
	}
	if sn.Handshake != nil {
		parts = append(parts, "("+sn.Identity.Key()+")")
	}
	if len(parts) == 0 {
		return "the mouse"
	}
	return strings.Join(parts, " ")
}

// reviewLower is err in plain words, starting lower case to follow a colon.
func reviewLower(err error) string {
	s := plain(err)
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
