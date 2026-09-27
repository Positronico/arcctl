package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// alert is one banner: a badge, then paragraphs. A paragraph that starts
// with "- " is a list item.
type alert struct {
	tone  tone
	badge string
	text  []string
}

func (n alert) render(c *Context, w int) []string {
	if len(n.text) == 0 {
		return nil
	}
	first := " " + c.Styles.tone(n.tone).Render(n.badge) + "  "
	out := wrap(n.text[0], w, first, "   ")
	for _, p := range n.text[1:] {
		switch {
		case strings.HasPrefix(p, "- "):
			out = append(out, wrap(p[2:], w, "   "+c.Glyphs.Bullet+" ", "     ")...)
		case strings.HasPrefix(p, "  "):
			out = append(out, "   "+p)
		default:
			out = append(out, wrap(p, w, "   ", "   ")...)
		}
	}
	return out
}

// banner is what the session state asks the user to know or do, plus the
// warnings that hold in Ready.
func (a *App) banner(c *Context, w int) []string {
	var out []string
	for _, n := range a.notices(c) {
		out = append(out, n.render(c, w)...)
	}
	return out
}

func (a *App) notices(c *Context) []alert {
	sn := a.sn
	if a.write != nil && sn.State == session.Ready {
		return append([]alert{a.applying(c)}, a.readyNotices(c)...)
	}
	switch sn.State {
	case session.NoReceiver:
		return []alert{noReceiver(sn)}
	case session.NeedsPermission:
		return []alert{a.needsPermission()}
	case session.Probing:
		return []alert{{toneInfo, "PROBING", []string{"Asking each receiver interface which one answers."}}}
	case session.Choosing:
		return []alert{a.choosing(c)}
	case session.Locked:
		return []alert{a.locked()}
	case session.Seized:
		return []alert{seized(sn)}
	case session.Stalled:
		return []alert{stalled(sn)}
	case session.Offline:
		return []alert{offline(c)}
	case session.Handshaking:
		return []alert{{toneInfo, "HANDSHAKE", []string{"The mouse is online; asking which model it is."}}}
	case session.Loading:
		return []alert{loading(c, toneInfo, "LOADING")}
	case session.Conflict:
		return []alert{conflict(c)}
	case session.SuspectedConflict:
		return []alert{suspected(sn)}
	case session.Applying:
		return []alert{a.applying(c)}
	case session.Recovering:
		return append([]alert{recovering(sn)}, a.readyNotices(c)...)
	case session.Unknown:
		return []alert{unknown(sn)}
	}
	return a.readyNotices(c)
}

func noReceiver(sn *session.Snapshot) alert {
	head := "No ProtoArc receiver found. arcctl keeps looking."
	if errors.Is(sn.Err, session.ErrNoAnswer) {
		head = "Receivers were found, but none answered. arcctl keeps trying."
	}
	return alert{toneBad, "NO RECEIVER", []string{head,
		"- Plug the receiver in.",
		"- Give the terminal access to input devices (Input Monitoring on macOS); 'arcctl doctor' checks it.",
		"- Close any ProtoArc HUB tab in a browser.",
	}}
}

func (a *App) needsPermission() alert {
	text := []string{"The OS refused access to the receiver."}
	if h := a.host.permission; h != "" {
		text = append(text, h)
	} else if err := a.sn.Err; err != nil {
		text = append(text, plain(err)+".")
	}
	tail := "'arcctl doctor' shows what is missing; arcctl retries on its own."
	if a.opt.Host.OpenSettings != nil {
		tail = "o opens the settings. " + tail
	}
	return alert{toneBad, "NO ACCESS", append(text, tail)}
}

func (a *App) choosing(c *Context) alert {
	text := []string{"Several ProtoArc devices answer. Pick one with the arrows and enter."}
	type row struct{ what, conn, ids, path string }
	rows := make([]row, len(a.sn.Answers))
	whatW, connW := 0, 0
	for i, ans := range a.sn.Answers {
		r := row{what: "mouse offline", ids: fmt.Sprintf("%04x:%04x", ans.Candidate.VID, ans.Candidate.PID), path: ans.Candidate.Path}
		switch {
		case ans.Model != nil:
			r.what = modelName(ans.Model) + " " + modelKey(ans.Model)
		case ans.Handshake != nil:
			r.what = fmt.Sprintf("unknown device %02X/%02X", ans.Handshake.CID, ans.Handshake.MID)
		}
		if ans.Handshake != nil {
			r.conn = ans.Handshake.ConnString()
		}
		whatW, connW = max(whatW, ansi.StringWidth(r.what)), max(connW, ansi.StringWidth(r.conn))
		rows[i] = r
	}
	for i, r := range rows {
		cur := "  "
		if i == a.pick {
			cur = c.Glyphs.Cursor + " "
		}
		line := fmt.Sprintf("%s%-*s  %-*s  %s  ", cur, whatW, r.what, connW, r.conn, r.ids)
		// The paths of two receivers differ near their end, so a long one
		// loses its middle.
		line += midCut(c, r.path, max(12, c.Width-5-ansi.StringWidth(line)))
		if i == a.pick {
			line = c.Styles.Selected.Render(line)
		}
		text = append(text, "  "+line)
	}
	return alert{toneWarn, "CHOOSE", text}
}

func (a *App) locked() alert {
	text := "The receiver is locked (0xE00002E2). Waiting."
	con := a.host.console
	switch {
	case con == nil:
	case con.ScreenLocked:
		text = "Screen locked. Waiting."
	case con.SecureInput != "":
		text = "Secure Input is on, held by " + con.SecureInput + ", so the receiver is locked (0xE00002E2). Waiting."
		if strings.Contains(strings.ToLower(con.SecureInput), "ghostty") {
			text += " Ghostty turns it on at password prompts when macos-auto-secure-input is set."
		}
	}
	return alert{toneWarn, "LOCKED", []string{text}}
}

func seized(sn *session.Snapshot) alert {
	text := []string{"Another app holds the receiver exclusively (for example Karabiner 'Modify events'). Waiting."}
	if s := clientList(sn.Clients); s != "" {
		text = append(text, "Holding it: "+s+".")
	}
	return alert{toneBad, "SEIZED", text}
}

func stalled(sn *session.Snapshot) alert {
	text := "The receiver stopped responding to writes. Replug it."
	if sn.Stalls > 1 {
		text += fmt.Sprintf(" It stalled %d times this session.", sn.Stalls)
	}
	return alert{toneBad, "STALLED", []string{text}}
}

func offline(c *Context) alert {
	sn := c.Snapshot
	text := []string{"The mouse is asleep or out of range. Move it, or switch it to its 2.4 GHz channel."}
	if sn.Image != nil {
		text = append(text, "Showing the last loaded configuration. Edits can be staged; applying waits for the mouse.")
	}
	if p := sn.Progress; p.Job != "" && p.Paused {
		text = append(text, fmt.Sprintf("%s paused at %d of %d; it resumes when the mouse wakes.", jobName(p.Job), p.Done, p.Total))
	}
	return alert{toneWarn, "OFFLINE", text}
}

var jobNames = map[string]string{
	"load":    "Loading the configuration",
	"reread":  "Reading changed records again",
	"backup":  "Backing up",
	"read":    "Reading",
	"journal": "Checking the journal",
	"apply":   "Writing",
	"revert":  "Reverting",
	"recover": "Settling the unfinished write",
}

func jobName(job string) string {
	if s, ok := jobNames[job]; ok {
		return s
	}
	return job
}

func loading(c *Context, t tone, badge string) alert {
	p := c.Snapshot.Progress
	job := "load"
	if p.Job != "" {
		job = p.Job
	}
	line := jobName(job)
	if p.Total > 0 {
		line += "  " + bar(c, p.Done, p.Total, 20) + fmt.Sprintf("  %d of %d", p.Done, p.Total)
	}
	return alert{t, badge, []string{line}}
}

func bar(c *Context, done, total, w int) string {
	n := 0
	if total > 0 {
		n = min(w, done*w/total)
	}
	return strings.Repeat(c.Glyphs.BarFull, n) + strings.Repeat(c.Glyphs.BarEmpty, w-n)
}

func clientList(cs []session.Client) string {
	var parts []string
	for _, cl := range cs {
		s := fmt.Sprintf("%s (pid %d)", cl.Name, cl.PID)
		if cl.Seized {
			s += " exclusively"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func conflict(c *Context) alert {
	sn := c.Snapshot
	text := []string{"Another configurator is talking to the receiver. Close the ProtoArc HUB tab. Writes are disabled."}
	if s := clientList(sn.Clients); s != "" {
		text = append(text, "Other programs on the receiver: "+s+".")
	}
	tail := "c clears the conflict once the receiver has been quiet for 10 s"
	if !sn.LastForeign.IsZero() {
		tail += fmt.Sprintf("; the last foreign reply came %s ago", ago(c.Now.Sub(sn.LastForeign)))
	}
	return alert{toneBad, "CONFLICT", append(text, tail+".")}
}

func ago(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", max(0, int(d/time.Second)))
	}
	return fmt.Sprintf("%d min", int(d/time.Minute))
}

func suspected(sn *session.Snapshot) alert {
	text := []string{"Replies are going missing. Another program may be reading the receiver (a HUB tab?). Writes are disabled until replies come back."}
	if s := clientList(sn.Clients); s != "" {
		text = append(text, "Other programs on the receiver: "+s+".")
	}
	return alert{toneBad, "REPLIES MISSING", text}
}

func recovering(sn *session.Snapshot) alert {
	text := "An earlier write did not finish."
	if run := openRun(sn); run != nil {
		text = fmt.Sprintf("An earlier write did not finish: %s %s.", run.Run.Kind, run.Run.ID)
	}
	return alert{toneBad, "RECOVERY", []string{text + " Writes stay blocked until it is settled; R opens the prompt."}}
}

func unknown(sn *session.Snapshot) alert {
	if errors.Is(sn.Err, session.ErrChargingBase) {
		return alert{toneWarn, "UNSUPPORTED", []string{"A charging base answered; arcctl does not support it."}}
	}
	text := "Unknown device: read-only."
	if h := sn.Handshake; h != nil {
		text = fmt.Sprintf("Unknown device 0x%02X/0x%02X: read-only.", h.CID, h.MID)
	}
	return alert{toneWarn, "UNKNOWN", []string{text}}
}

var stepNames = map[safety.EventKind]string{
	safety.EventStart:    "sending",
	safety.EventWritten:  "reading back",
	safety.EventVerified: "verified",
	safety.EventFailed:   "failed",
	safety.EventRestart:  "restarting",
}

func (a *App) applying(c *Context) alert {
	w := a.write
	if w == nil {
		return loading(c, toneWarn, "WRITING")
	}
	badge := "WRITING"
	if w.dryRun {
		badge = "DRY RUN"
	}
	what := map[safety.RunKind]string{safety.KindApply: "Applying", safety.KindRevert: "Reverting", safety.KindRecover: "Settling the unfinished write"}[w.kind]
	head := fmt.Sprintf("%s: %s.", what, w.progress())
	if p := c.Snapshot.Progress; p.Job == "backup" && w.cur < 0 {
		badge, head = "BACKING UP", "Backing up the whole configuration before the first write."
		if p.Total > 0 {
			head = fmt.Sprintf("Backing up the whole configuration before the first write: %d of %d.", p.Done, p.Total)
		}
	}
	switch {
	case w.abort || a.quitAfterWrite:
		head += " Stopping after the current record."
	case a.hiddenWriter() != nil:
		head += " a shows the write; q stops it and quits."
	default:
		head += " q stops it after the current record and quits."
	}
	text := []string{head}
	if a.dialog != nil {
		return alert{toneWarn, badge, text}
	}
	from := max(0, min(w.cur-2, len(w.steps)-5))
	for i := from; i < min(len(w.steps), from+5); i++ {
		s := w.steps[i]
		mark := " "
		switch {
		case s.kind == safety.EventVerified:
			mark = c.Glyphs.Check
		case s.kind == safety.EventFailed:
			mark = c.Glyphs.Cross
		case i == w.cur:
			mark = c.Glyphs.Cursor
		}
		state := "planned"
		switch {
		case s.kind == safety.EventChunk:
			state = fmt.Sprintf("writing %d/%d", s.chunk, s.chunks)
		case s.kind != 0:
			state = stepNames[s.kind]
		}
		text = append(text, fmt.Sprintf("  %s %-9s %-10s %-34s %s", mark, s.op.Extent, s.op.Phase, s.op.Desc, state))
	}
	if w.paused != nil {
		text = append(text, "Paused: "+plain(w.paused)+". It goes on when that clears.")
	}
	return alert{toneWarn, badge, text}
}

// readyNotices are the warnings that hold while the mouse is loaded.
func (a *App) readyNotices(c *Context) []alert {
	sn := a.sn
	var out []alert
	if p := sn.Progress; p.Job != "" && sn.State == session.Ready && a.write == nil {
		out = append(out, loading(c, toneInfo, "BUSY"))
	}
	if len(sn.Clients) > 0 {
		text := clientList(sn.Clients) + " has the receiver open. Close it: writes are blocked."
		if c.Gates.AllowForeignClient {
			text = clientList(sn.Clients) + " has the receiver open; --allow-foreign-client lets writes go on."
		}
		out = append(out, alert{toneBad, "OTHER CLIENT", []string{text}})
	}
	if m := c.Model(); m != nil && m.Family != catalog.FamilyMouse {
		out = append(out, alert{toneInfo, "READ-ONLY", []string{"Keyboards are shown read-only in this version."}})
	} else if m != nil && c.Mode == ModeEdit && !c.Gates.AllowUntested && a.untested(c) {
		out = append(out, alert{toneWarn, "UNTESTED", []string{"Writes need --allow-untested until hardware tests verify them."}})
	}
	if n := len(sn.Unread); n > 0 {
		out = append(out, alert{toneWarn, "UNREAD", []string{fmt.Sprintf("%s could not be read; those fields show as unread.", plural(n, "chunk", "chunks"))}})
	}
	if js := sn.Journal; js != nil && js.Err != nil {
		out = append(out, alert{toneBad, "JOURNAL", []string{"The journal could not be read, so writes are blocked: " + plain(js.Err) + "."}})
	}
	return out
}

func (a *App) untested(c *Context) bool {
	for _, f := range webFeatures {
		if t, _ := c.Tier(f); t == catalog.Untested {
			return true
		}
	}
	return false
}
