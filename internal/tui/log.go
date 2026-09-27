package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// LogTab is the session's event log (§7.8): what the snapshots, the writes,
// the factory reset and the journal reported, newest last; the last write or
// reset of this session and the journal's last change; and a review of the
// revert of that change.
type LogTab struct {
	prev    *session.Snapshot
	entries []logEntry
	back    int // lines scrolled back from the newest
	page    int
	last    *logWrite
}

type logEntry struct {
	at   time.Time
	tone tone
	text string
}

// logWrite is the end of the last write or factory reset this session
// started: its line, the backup it saved, and the run of a reset that went
// out, which no revert goes past.
type logWrite struct {
	at     time.Time
	entry  logEntry
	backup string
	reset  string
}

// logLimit is how many events the tab keeps; older ones are dropped.
const logLimit = 2000

func NewLogTab() *LogTab { return &LogTab{} }

func (t *LogTab) Title() string          { return "Log" }
func (t *LogTab) Needs() []mouse.Feature { return nil }
func (t *LogTab) Capturing() bool        { return false }

func (t *LogTab) Update(c *Context, msg tea.Msg) tea.Cmd {
	t.sync(c)
	switch msg := msg.(type) {
	case SnapshotMsg:
		t.observe(c, msg.Snapshot)
	case OpEventMsg:
		if e, ok := logOp(msg.Event); ok {
			t.add(c, e)
		}
	case WriteDoneMsg:
		e := logDone(msg)
		t.add(c, e)
		t.last = &logWrite{at: c.Now, entry: e}
		if b := msg.Outcome.Backups; len(b) > 0 {
			t.last.backup = b[len(b)-1]
		}
	case resetDoneMsg:
		e := logReset(msg.out, msg.err)
		t.add(c, e)
		t.last = &logWrite{at: c.Now, entry: e, backup: msg.out.Backup}
		if msg.out.Sent {
			t.last.reset = msg.out.Run
		}
	case tea.KeyPressMsg:
		return t.key(c, msg.String())
	}
	return nil
}

func (t *LogTab) key(c *Context, k string) tea.Cmd {
	page := max(1, t.page-1)
	switch k {
	case "up", "k":
		t.back++
	case "down", "j":
		t.back--
	case "pgup":
		t.back += page
	case "pgdown", "space":
		t.back -= page
	case "home", "g":
		t.back = len(t.entries)
	case "end", "G":
		t.back = 0
	case "v":
		return ReviewRevert
	}
	t.back = max(0, min(t.back, len(t.entries)))
	return nil
}

func (t *LogTab) Hints(c *Context) []key.Binding {
	out := []key.Binding{hint(dilArrows(c, "↑/↓", "up/down"), "scroll"), hint("g/G", "oldest/newest")}
	if c.Mode != ModeReadOnly && c.Snapshot.Journal != nil && c.Snapshot.Journal.Last != nil {
		out = append([]key.Binding{hint("v", "review revert")}, out...)
	}
	return out
}

// sync starts the log from the snapshot the shell had before the tab saw
// any message.
func (t *LogTab) sync(c *Context) {
	if t.prev == nil {
		t.observe(c, c.Snapshot)
	}
}

func (t *LogTab) observe(c *Context, sn *session.Snapshot) {
	if sn == nil || sn == t.prev {
		return
	}
	first := t.prev == nil
	prev := t.prev
	if first {
		prev = &session.Snapshot{}
	}
	for _, e := range logDiff(prev, sn, first) {
		t.add(c, e)
	}
	t.prev = sn
}

func (t *LogTab) add(c *Context, e logEntry) {
	e.at = c.Now
	t.entries = append(t.entries, e)
	if n := len(t.entries) - logLimit; n > 0 {
		t.entries = slices.Delete(t.entries, 0, n)
	}
	if t.back > 0 {
		t.back++
	}
}

// logDiff lists what changed from prev to sn; first logs everything sn
// holds.
func logDiff(prev, sn *session.Snapshot, first bool) []logEntry {
	var out []logEntry
	add := func(t tone, format string, args ...any) {
		out = append(out, logEntry{tone: t, text: fmt.Sprintf(format, args...)})
	}
	if first || prev.State != sn.State {
		text := "State: " + sn.State.String()
		if sn.Err != nil {
			text += ": " + plain(sn.Err)
		}
		add(stateBadges[sn.State].tone, "%s", text)
	}
	if devicePath(prev) != devicePath(sn) {
		if d := sn.Device; d != nil {
			add(toneInfo, "Receiver %04x:%04x, interface %d: %s", d.VID, d.PID, d.Interface, d.Product)
		} else {
			add(toneWarn, "Receiver gone")
		}
	}
	if hs := sn.Handshake; hs != nil && (prev.Handshake == nil || *prev.Handshake != *hs) {
		name := "unknown model"
		if sn.Model != nil {
			name = modelName(sn.Model)
		}
		add(toneInfo, "Handshake: %02X/%02X %s, %s", hs.CID, hs.MID, name, hs.ConnString())
	}
	if v := sn.Versions; v != prev.Versions && (v.Mouse != "" || v.Receiver != "") {
		add(toneInfo, "Firmware: mouse %s, receiver %s", infoOr(v.Mouse), infoOr(v.Receiver))
	}
	if b := sn.Battery; b != nil && (prev.Battery == nil || prev.Battery.Level != b.Level || prev.Battery.Charging != b.Charging) {
		text := fmt.Sprintf("Battery %d%% raw", b.Level)
		if b.Charging {
			text += ", charging"
		}
		add(toneInfo, "%s", text)
	}
	if p := sn.Profile; p.Asked && p != prev.Profile {
		add(toneInfo, "Profiles: %s", infoProbe(p, "cmd 14", func(v byte) string { return fmt.Sprintf("onboard profile %d is active", v) }, "refused"))
	}
	if p := sn.LongRange; p.Asked && p != prev.LongRange {
		add(toneInfo, "Long range: %s", infoProbe(p, "cmd 23", func(v byte) string { return map[bool]string{true: "on", false: "off"}[v == 1] }, "refused"))
	}
	out = append(out, logProgress(prev.Progress, sn.Progress, sn.State)...)
	if n := len(sn.Unread); n > 0 && n != len(prev.Unread) {
		add(toneWarn, "%s could not be read", plural(n, "chunk", "chunks"))
	}
	out = append(out, logStats(prev.Stats, sn.Stats)...)
	out = append(out, logJournal(prev.Journal, sn.Journal)...)
	if a, b := clientList(prev.Clients), clientList(sn.Clients); a != b {
		if b == "" {
			add(toneGood, "No other program on the receiver")
		} else {
			add(toneBad, "Other programs on the receiver: %s", b)
		}
	}
	if sn.Stalls > prev.Stalls {
		add(toneBad, "The receiver stalled (%d in this session)", sn.Stalls)
	}
	if !first && sn.Policy != prev.Policy {
		t := toneInfo
		if sn.Policy.String() != "read-only" {
			t = toneWarn
		}
		add(t, "Guard: %s", sn.Policy)
	}
	if prev.DryRun == nil && sn.DryRun != nil {
		add(toneInfo, "The dry-run overlay holds this session's dry-run writes")
	}
	return out
}

func devicePath(sn *session.Snapshot) string {
	if sn.Device == nil {
		return ""
	}
	return sn.Device.Path
}

// logProgress logs the jobs that started and ended between two snapshots.
// Snapshots skip steps, so a job that ends short of its total finished
// unless the session left for a state that cut it short. A write's end is
// logged from its result instead.
func logProgress(a, b session.Progress, st session.State) []logEntry {
	var out []logEntry
	if a.Job != b.Job {
		switch {
		case a.Job == "" || writeJobs[a.Job]:
		case a.Done < a.Total && !working[st]:
			out = append(out, logEntry{tone: toneWarn, text: fmt.Sprintf("%s stopped at %d of %d: %s", logJob(a.Job), a.Done, a.Total, st)})
		default:
			out = append(out, logEntry{tone: toneInfo, text: logJob(a.Job) + " finished"})
		}
		if b.Job != "" {
			out = append(out, logEntry{tone: toneInfo, text: logJob(b.Job) + " started"})
		}
	}
	if b.Job != "" && a.Job == b.Job && a.Paused != b.Paused {
		if b.Paused {
			out = append(out, logEntry{tone: toneWarn, text: logJob(b.Job) + " paused: waiting for the mouse"})
		} else {
			out = append(out, logEntry{tone: toneInfo, text: logJob(b.Job) + " resumed"})
		}
	}
	return out
}

// logJob names a job in the log, the steps of a factory reset included.
func logJob(job string) string {
	switch job {
	case "reset":
		return "Factory reset"
	case "reset check":
		return "Reading the mouse again after the factory reset"
	}
	return jobName(job)
}

var (
	writeJobs = map[string]bool{"apply": true, "revert": true, "recover": true, "reset": true}
	working   = map[session.State]bool{session.Ready: true, session.Loading: true, session.Handshaking: true,
		session.Applying: true, session.Recovering: true}
)

func logStats(a, b session.Stats) []logEntry {
	var out []logEntry
	for _, s := range []struct {
		one, many string
		old, now  int
		tone      tone
	}{
		{"foreign reply", "foreign replies", a.Foreign, b.Foreign, toneBad},
		{"duplicate reply", "duplicate replies", a.Duplicates, b.Duplicates, toneInfo},
		{"late reply", "late replies", a.Late, b.Late, toneWarn},
		{"NAK", "NAKs", a.NAKs, b.NAKs, toneWarn},
		{"push", "pushes", a.Pushes, b.Pushes, toneInfo},
		{"unasked cmd-3 report", "unasked cmd-3 reports", a.PossiblePushes, b.PossiblePushes, toneInfo},
		{"unanswered try", "unanswered tries", a.FailedTries, b.FailedTries, toneWarn},
		{"refused write", "refused writes", a.WriteErrors, b.WriteErrors, toneBad},
		{"bad checksum", "bad checksums", a.BadChecksums, b.BadChecksums, toneWarn},
		{"odd status byte", "odd status bytes", a.OddStatus, b.OddStatus, toneWarn},
		{"dropped report", "dropped reports", int(a.Dropped), int(b.Dropped), toneWarn},
	} {
		if d := s.now - s.old; d > 0 {
			out = append(out, logEntry{tone: s.tone, text: fmt.Sprintf("%s (%d in all)", plural(d, s.one, s.many), s.now)})
		}
	}
	return out
}

func logJournal(a, b *session.JournalState) []logEntry {
	if b == nil {
		return nil
	}
	var out []logEntry
	for _, o := range b.Open {
		if a == nil || !slices.ContainsFunc(a.Open, func(p session.OpenRun) bool { return p.Run.ID == o.Run.ID }) {
			out = append(out, logEntry{tone: toneBad, text: fmt.Sprintf("Journal: %s %s is unfinished (%s)",
				o.Run.Kind, o.Run.ID, plural(len(o.Run.Ops), "record", "records"))})
		}
	}
	for _, r := range b.Resets {
		if a == nil || !slices.ContainsFunc(a.Resets, func(p *safety.Run) bool { return p.ID == r.ID }) {
			out = append(out, logEntry{tone: toneBad, text: fmt.Sprintf("Journal: factory reset %s is unchecked; arcctl compares the mouse "+
				"with the backup from before it", r.ID)})
		}
	}
	if l := b.Last; l != nil && (a == nil || a.Last == nil || !sameRun(a.Last, l)) {
		out = append(out, logEntry{tone: toneInfo, text: "Journal: last change " + runLine(l)})
	}
	if b.Err != nil && (a == nil || a.Err == nil) {
		out = append(out, logEntry{tone: toneBad, text: "Journal unreadable: " + plain(b.Err)})
	}
	return out
}

func sameRun(a, b *safety.Run) bool {
	return a.ID == b.ID && a.Complete == b.Complete && a.Resolved == b.Resolved && len(a.Ops) == len(b.Ops)
}

// runLine is a run's kind, ID, start and how it ended.
func runLine(r *safety.Run) string {
	text := fmt.Sprintf("%s %s, %s, %s", r.Kind, r.ID, r.Started.UTC().Format("2006-01-02 15:04 UTC"),
		plural(len(r.Ops), "record", "records"))
	switch {
	case r.Complete:
		text += ", complete"
	case r.Resolved != 0:
		text += ", settled " + r.Resolved.String()
	case r.Err != "":
		text += ", stopped: " + r.Err
	}
	return text
}

func logOp(e safety.OpEvent) (logEntry, bool) {
	if e.Kind == safety.EventChunk {
		return logEntry{}, false
	}
	le := logEntry{tone: toneInfo, text: fmt.Sprintf("  op %d %s %s: %s", e.Op.Seq, e.Op.Extent, e.Op.Desc, e.Kind)}
	switch e.Kind {
	case safety.EventVerified:
		le.tone = toneGood
	case safety.EventFailed:
		le.tone = toneBad
		le.text += " (" + e.Class.String() + ")"
		if e.Err != nil {
			le.text += ": " + plain(e.Err)
		}
	case safety.EventPaused:
		le.tone = toneWarn
		if e.Err != nil {
			le.text += ": " + plain(e.Err)
		}
	}
	return le, true
}

func logDone(d WriteDoneMsg) logEntry {
	what := d.Kind.String()
	if d.Outcome.DryRun {
		what = "dry run of the " + what
	}
	what = strings.ToUpper(what[:1]) + what[1:]
	out := d.Outcome
	switch writeEnd(d) {
	case reviewEndVerified:
	case reviewEndStopped:
		return logEntry{tone: toneBad, text: fmt.Sprintf("%s stopped: %d of %d records verified. %s", what, out.Verified, out.Ops, plain(d.Err))}
	case reviewEndAborted:
		return logEntry{tone: toneWarn, text: what + " stopped at your request; nothing was written"}
	default:
		return logEntry{tone: toneBad, text: what + " refused: " + plain(d.Err)}
	}
	if out.Ops == 0 {
		return logEntry{tone: toneGood, text: what + " done: nothing to write"}
	}
	return logEntry{tone: toneGood, text: fmt.Sprintf("%s %s done: %d of %d records verified", what, out.Run, out.Verified, out.Ops)}
}

// logReset is how a factory reset ended: refused or stopped before its
// packet, or what the full read after it found.
func logReset(o session.ResetOutcome, err error) logEntry {
	switch {
	case o.DryRun && err == nil:
		return logEntry{tone: toneGood, text: "Dry run of the factory reset done: the checks passed; nothing was sent"}
	case !o.Sent && errors.Is(err, safety.ErrAborted):
		return logEntry{tone: toneWarn, text: "Factory reset stopped at your request; nothing was sent"}
	case !o.Sent:
		what := "Factory reset"
		if o.DryRun {
			what = "Dry run of the factory reset"
		}
		if err == nil {
			return logEntry{tone: toneBad, text: what + " ended; nothing was sent"}
		}
		return logEntry{tone: toneBad, text: what + " refused: " + plain(err)}
	}
	what := "Factory reset"
	if o.Run != "" {
		what += " " + o.Run
	}
	var e logEntry
	switch n := len(o.Changed); o.Verdict {
	case safety.VerdictChanged:
		verb := "differ"
		if n == 1 {
			verb = "differs"
		}
		e = logEntry{tone: toneGood, text: fmt.Sprintf("%s done: %s of the configuration %s from the backup", what, plural(n, "range", "ranges"), verb)}
	case safety.VerdictUnchanged:
		e = logEntry{tone: toneWarn, text: what + " done: it changed nothing"}
	default:
		e = logEntry{tone: toneBad, text: what + " went out, but the mouse was not read again"}
		if err != nil {
			e.text += strings.TrimPrefix(": "+plain(err), ": "+plain(safety.ErrResetUnchecked))
			err = nil
		}
	}
	e.text += " (reply: " + resetReplyText(o.Reply) + ")"
	if err != nil {
		e.text += ". " + plain(err)
	}
	return e
}

func (t *LogTab) View(c *Context, w, h int) string {
	t.sync(c)
	top := t.summary(c, w, h)
	top = append(top, "", c.Styles.Bold.Render(" Events")+c.Styles.Faint.Render(" newest last"))
	room := h - len(top)
	t.page = room
	lines := make([]string, len(t.entries))
	for i, e := range t.entries {
		text := e.text
		if e.tone != toneInfo {
			text = c.Styles.tone(e.tone).Render(text)
		}
		lines[i] = " " + e.at.Format("15:04:05") + "  " + text
	}
	if room < 1 {
		return strings.Join(top, "\n")
	}
	t.back = max(0, min(t.back, len(lines)-room))
	end := len(lines) - t.back
	start := max(0, end-room)
	shown := lines[start:end]
	if t.back > 0 && len(shown) > 0 {
		shown = append(slices.Clone(shown[:len(shown)-1]), c.Styles.Faint.Render(fmt.Sprintf(" %s %d newer (G shows the newest)", c.Glyphs.Ellipsis, t.back+1)))
	}
	return strings.Join(append(top, shown...), "\n")
}

// summary is the last write of this session and the journal's last change,
// with its records.
func (t *LogTab) summary(c *Context, w, h int) []string {
	const label = 13
	pad := strings.Repeat(" ", label+1)
	line := func(name, text string) []string { return wrap(text, w-1, fmt.Sprintf(" %-*s", label, name), pad) }
	var out []string
	l := t.last
	if l != nil {
		out = append(out, line("This session", l.at.Format("15:04:05")+"  "+l.entry.text)...)
		if l.backup != "" {
			out = append(out, fmt.Sprintf(" %-*s%s", label, "Backup", midCut(c, l.backup, w-label-2)))
		}
	}
	js := c.Snapshot.Journal
	switch {
	case js == nil:
		return append(out, line("Last change", "the journal is not read in this session")...)
	case js.Last == nil && l != nil && l.reset != "":
		return append(out, line("Last change", "none since the factory reset "+l.reset+"; no revert goes past it")...)
	case js.Last == nil:
		return append(out, line("Last change", "none: the journal holds no write to this mouse")...)
	}
	run := js.Last
	out = append(out, line("Last change", runLine(run))...)
	limit := max(1, h/5)
	for i, o := range run.Ops {
		if i == limit {
			out = append(out, fmt.Sprintf("   %s and %d more", c.Glyphs.Ellipsis, len(run.Ops)-limit))
			break
		}
		out = append(out, fmt.Sprintf("   %-9s %-10s %s %s %s  %-9s %s", o.Extent, o.Phase, shortHex(c, o.Old, 4),
			c.Glyphs.Arrow, shortHex(c, o.New, 4), o.State, o.Desc))
	}
	if c.Mode != ModeReadOnly {
		out = append(out, "   v reviews a revert of it")
	}
	return out
}
