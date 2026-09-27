package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// App is the shell: the Bubble Tea model that hosts the tabs.
type App struct {
	opt    Options
	api    session.API
	ctx    context.Context
	cancel context.CancelFunc
	watch  watcher
	keys   shellKeys
	styles Styles
	glyphs Glyphs

	sn       *session.Snapshot
	config   *mouse.Config
	decoded  decoded
	identity string // the device the pending edits were staged for
	pending  *Pending
	tabs     []Tab
	active   int
	os       keys.OS // the key labels and preset tables the screens use
	// dialog is the dialog on top; under are the ones it covers, which come
	// back as it closes.
	dialog   Dialog
	under    []Dialog
	notice   noticeMsg
	host     hostMsg
	pick     int
	prompted map[string]bool // unfinished runs whose prompt was shown

	width, height int

	write *writing
	// writer is the dialog that approved the running write; it follows the
	// write even while it is hidden, and a brings it back.
	writer         Dialog
	quitPrompt     *Confirm
	quitAfterWrite bool
	init           tea.Cmd
}

// decoded is what config was decoded from; snapshots share an image until
// a job finishes.
type decoded struct {
	m  *catalog.Model
	im *flash.Image
}

// New builds the shell over o.Session; Close stops what it started.
func New(ctx context.Context, o Options) *App {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Verified == nil {
		o.Verified = catalog.VerifiedStages()
	}
	if o.Tabs == nil {
		o.Tabs = defaultTabs(o.Session)
	}
	if o.Review == nil {
		o.Review = NewReview(o.Session)
	}
	ctx, cancel := context.WithCancel(ctx)
	a := &App{
		opt:      o,
		api:      o.Session,
		ctx:      ctx,
		cancel:   cancel,
		watch:    watcher{ctx: ctx, api: o.Session, every: snapshotEvery},
		keys:     newShellKeys(),
		styles:   defaultStyles(),
		glyphs:   unicodeGlyphs,
		pending:  NewPending(),
		tabs:     o.Tabs,
		os:       o.OS,
		prompted: map[string]bool{},
		width:    80,
		height:   24,
	}
	if o.ASCII {
		a.glyphs = asciiGlyphs
	}
	if len(o.Notes) > 0 {
		a.notice = noticeMsg{text: strings.Join(o.Notes, " ")}
	}
	a.init = a.setSnapshot(o.Session.Snapshot())
	return a
}

func (a *App) Close() { a.cancel() }

// Pending is the store of staged edits the tabs share.
func (a *App) Pending() *Pending { return a.pending }

// Init hands the tabs the first snapshot, which New already shows.
func (a *App) Init() tea.Cmd {
	return tea.Batch(a.init, a.broadcast(SnapshotMsg{a.sn}), a.watch.next())
}

func (a *App) context() *Context {
	return &Context{
		Snapshot: a.sn,
		Pending:  a.pending,
		Mode:     a.opt.Mode,
		Gates:    a.opt.Gates,
		OS:       a.os,
		Glyphs:   a.glyphs,
		Styles:   a.styles,
		Now:      a.opt.Now(),
		Width:    a.width,
		Height:   a.height,
		verified: a.opt.Verified,
		config:   a.config,
		writing:  a.write != nil,
		dataDir:  a.opt.DataDir,
		notes:    a.opt.Notes,
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil
	case tea.KeyPressMsg:
		return a, a.key(msg)
	case watchMsg:
		return a, tea.Batch(a.watch.next(), a.take(msg.sn))
	case SnapshotMsg:
		return a, a.take(msg.Snapshot)
	case writeRequest:
		return a, a.startWrite(msg)
	case eventsMsg:
		if a.write == nil || msg.q != a.write.q {
			return a, nil
		}
		var cmds []tea.Cmd
		for _, e := range msg.events {
			a.write.add(e)
			cmds = append(cmds, a.broadcast(OpEventMsg{e}))
		}
		if msg.end != nil {
			cmds = append(cmds, a.endWrite(*msg.end))
		} else {
			cmds = append(cmds, msg.q.listen())
		}
		return a, tea.Batch(cmds...)
	case openDialogMsg:
		a.push(msg.d)
		return a, nil
	case closeDialogMsg:
		a.closeDialog(msg.d)
		return a, nil
	case quitMsg:
		return a, a.quit(false)
	case reviewRevertMsg:
		return a, a.reviewRevert(a.context())
	case toggleLabelsMsg:
		a.os = flipOS(a.os)
		return a, nil
	case tea.PasteMsg:
		if a.dialog != nil {
			return a, a.dialog.Update(a.context(), msg)
		}
		if t := a.activeTab(a.context()); t != nil {
			return a, t.Update(a.context(), msg)
		}
		return a, nil
	case noticeMsg:
		a.notice = msg
		return a, nil
	case actionMsg:
		if msg.err != nil {
			a.notice = noticeMsg{text: plain(msg.err), bad: true}
		} else if msg.done != "" {
			a.notice = noticeMsg{text: msg.done}
		}
		return a, nil
	case backupMsg:
		a.notice = msg.notice()
		return a, nil
	case hostMsg:
		a.host = msg
		return a, nil
	}
	return a, a.broadcast(msg)
}

// broadcast sends msg to every tab, every open dialog and the dialog that
// approved the running write.
func (a *App) broadcast(msg tea.Msg) tea.Cmd {
	c := a.context()
	var cmds []tea.Cmd
	for _, t := range a.tabs {
		cmds = append(cmds, t.Update(c, msg))
	}
	ds := a.dialogs()
	if a.writer != nil && !slices.Contains(ds, a.writer) {
		ds = append(ds, a.writer)
	}
	for _, d := range ds {
		cmds = append(cmds, d.Update(c, msg))
	}
	return tea.Batch(cmds...)
}

// dialogs are the open dialogs, the one on top last.
func (a *App) dialogs() []Dialog {
	if a.dialog == nil {
		return nil
	}
	return append(slices.Clone(a.under), a.dialog)
}

func (a *App) push(d Dialog) {
	if a.dialog != nil {
		a.under = append(a.under, a.dialog)
	}
	a.dialog = d
}

// closeDialog closes d, or the dialog on top when d is nil; the one under
// it comes back.
func (a *App) closeDialog(d Dialog) {
	if d != nil && d != a.dialog {
		if i := slices.Index(a.under, d); i >= 0 {
			a.under = slices.Delete(a.under, i, i+1)
		}
		return
	}
	a.dialog = nil
	if n := len(a.under); n > 0 {
		a.dialog, a.under = a.under[n-1], a.under[:n-1]
	}
}

func (a *App) isOpen(d Dialog) bool { return d != nil && slices.Contains(a.dialogs(), d) }

// take shows sn unless it is the one already shown.
func (a *App) take(sn *session.Snapshot) tea.Cmd {
	if sn == nil || sn.Seq == a.sn.Seq {
		return nil
	}
	return tea.Batch(a.setSnapshot(sn), a.broadcast(SnapshotMsg{sn}))
}

// setSnapshot takes a newer snapshot: it decodes the configuration, drops
// edits staged for another mouse, and asks the OS what the new state's
// banner needs.
func (a *App) setSnapshot(sn *session.Snapshot) tea.Cmd {
	prev := a.sn
	a.sn = sn
	c := a.context()
	switch m, im := c.Model(), c.Image(); {
	case m == nil || m.Family != catalog.FamilyMouse || im == nil:
		a.config, a.decoded = nil, decoded{}
	case a.decoded != (decoded{m, im}):
		cfg := mouse.Decode(m, im)
		a.config, a.decoded = &cfg, decoded{m, im}
		c.config = a.config
	}
	var cmds []tea.Cmd
	if sn.Handshake != nil {
		id := sn.Identity.Key()
		if a.identity != "" && id != a.identity && a.pending.Len() > 0 {
			a.pending.Clear()
			cmds = append(cmds, Notice("A different mouse answered; the pending edits were dropped."))
		}
		a.identity = id
	}
	if prev == nil || prev.State != sn.State {
		a.host = hostMsg{}
		cmds = append(cmds, a.askHost(sn.State))
	}
	if n := len(sn.Answers); a.pick >= n {
		a.pick = max(0, n-1)
	}
	if run := openRun(sn); run != nil && !a.prompted[run.Run.ID] && a.dialog == nil && a.write == nil {
		a.prompted[run.Run.ID] = true
		a.push(newRecoveryDialog(c, *run))
	}
	return tea.Batch(cmds...)
}

func openRun(sn *session.Snapshot) *session.OpenRun {
	if sn.State != session.Recovering || sn.Journal == nil || len(sn.Journal.Open) == 0 {
		return nil
	}
	return &sn.Journal.Open[0]
}

// visible lists the indexes of the tabs shown for c.
func (a *App) visible(c *Context) []int {
	var out []int
	for i, t := range a.tabs {
		if c.Shows(t.Needs()) {
			out = append(out, i)
		}
	}
	return out
}

// shown is the index of the tab on screen: the active one while it is
// visible, else the first visible one, so a tab hidden until the model is
// known comes back once it is; -1 when no tab is visible.
func (a *App) shown(c *Context) int {
	vis := a.visible(c)
	switch {
	case len(vis) == 0:
		return -1
	case slices.Contains(vis, a.active):
		return a.active
	}
	return vis[0]
}

// activeTab is the tab on screen; nil when none is visible.
func (a *App) activeTab(c *Context) Tab {
	if i := a.shown(c); i >= 0 {
		return a.tabs[i]
	}
	return nil
}

func (a *App) switchTab(c *Context, delta int, to int) {
	vis := a.visible(c)
	if len(vis) == 0 {
		return
	}
	pos := slices.Index(vis, a.shown(c))
	switch {
	case to >= 0:
		if to >= len(vis) {
			return
		}
		pos = to
	default:
		pos = (pos + delta + len(vis)) % len(vis)
	}
	a.active = vis[pos]
}

func (a *App) key(msg tea.KeyPressMsg) tea.Cmd {
	c := a.context()
	a.notice = noticeMsg{}
	k := a.keys
	if msg.String() == "ctrl+c" {
		return a.quit(true)
	}
	if a.dialog != nil {
		return a.dialog.Update(c, msg)
	}
	if a.sn.State == session.Choosing {
		if cmd, ok := a.pickKey(msg); ok {
			return cmd
		}
	}
	t := a.activeTab(c)
	if t != nil && t.Capturing() {
		return t.Update(c, msg)
	}
	s := msg.String()
	switch {
	case key.Matches(msg, k.Quit):
		return a.quit(false)
	case key.Matches(msg, k.Help):
		a.push(newHelpDialog(a.hints(c, t, false)))
		return nil
	case key.Matches(msg, k.Next):
		a.switchTab(c, 1, -1)
		return nil
	case key.Matches(msg, k.Prev):
		a.switchTab(c, -1, -1)
		return nil
	case len(s) == 1 && s[0] >= '1' && s[0] <= '9':
		a.switchTab(c, 0, int(s[0]-'1'))
		return nil
	case key.Matches(msg, k.Review):
		return a.review(c)
	case key.Matches(msg, k.Discard):
		return a.discard(c)
	case key.Matches(msg, k.Revert):
		return a.reviewRevert(c)
	case key.Matches(msg, k.Reload):
		return a.reload(c)
	case key.Matches(msg, k.Backup):
		return a.backup(c)
	case key.Matches(msg, k.Settle) && openRun(a.sn) != nil:
		a.push(newRecoveryDialog(c, *openRun(a.sn)))
		return nil
	case key.Matches(msg, k.Clear) && a.sn.State == session.Conflict:
		return a.clearConflict()
	case key.Matches(msg, k.Settings) && a.sn.State == session.NeedsPermission && a.opt.Host.OpenSettings != nil:
		open := a.opt.Host.OpenSettings
		return func() tea.Msg { return actionMsg{err: open()} }
	}
	if t != nil {
		return t.Update(c, msg)
	}
	return nil
}

func (a *App) pickKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	n := len(a.sn.Answers)
	switch {
	case key.Matches(msg, a.keys.Up):
		a.pick = max(0, a.pick-1)
	case key.Matches(msg, a.keys.Down):
		a.pick = min(max(0, n-1), a.pick+1)
	case key.Matches(msg, a.keys.Choose):
		if a.pick >= n {
			return nil, true
		}
		path := a.sn.Answers[a.pick].Candidate.Path
		return a.call("", func(ctx context.Context) error { return a.api.Choose(ctx, path) }), true
	default:
		return nil, false
	}
	return nil, true
}

// hiddenWriter is the review that started the running write while esc hid
// it; nil otherwise.
func (a *App) hiddenWriter() Dialog {
	if rv, ok := a.writer.(*reviewDialog); ok && a.write != nil && !a.isOpen(rv) {
		return rv
	}
	return nil
}

// quit ends arcctl, after asking while a write runs or edits are pending.
// ctrl+c while that question is open, or once the write was asked to stop,
// quits at once.
func (a *App) quit(ctrlC bool) tea.Cmd {
	switch {
	case ctrlC && (a.isOpen(a.quitPrompt) || a.quitAfterWrite):
		return tea.Quit
	case a.write != nil && a.quitAfterWrite:
		return Notice("The write stops after its current record; arcctl quits then. ctrl+c quits at once.")
	case a.write == nil && a.pending.Len() == 0:
		return tea.Quit
	case a.isOpen(a.quitPrompt):
		return nil
	}
	a.quitPrompt = a.quitConfirm()
	a.push(a.quitPrompt)
	return nil
}

// quitConfirm asks before quitting: while a write runs, whether to stop it
// and quit when it ends; else whether to drop the pending edits.
func (a *App) quitConfirm() *Confirm {
	now := func(string) tea.Cmd { return tea.Quit }
	switch n := a.pending.Len(); {
	case a.write != nil:
		return &Confirm{
			Title: "A write is running",
			Body: []string{"Stop it after the current record and quit when it ends? The journal keeps track of what was written. " +
				"ctrl+c again quits at once."},
			Yes: func(string) tea.Cmd {
				if a.write == nil {
					return tea.Quit
				}
				a.api.Abort()
				a.quitAfterWrite = true
				a.write.abort = true
				return nil
			},
		}
	case n > 0:
		return &Confirm{Title: "Quit", Body: []string{fmt.Sprintf("Drop %s and quit? ctrl+c again quits at once.",
			plural(n, "pending edit", "pending edits"))}, Yes: now}
	}
	return &Confirm{Title: "Quit", Body: []string{"The write has ended. Quit arcctl?"}, Yes: now}
}

func (a *App) review(c *Context) tea.Cmd {
	if w := a.hiddenWriter(); w != nil {
		a.push(w)
		return nil
	}
	switch {
	case c.Mode == ModeReadOnly:
		return Problem(ErrReadOnlyMode)
	case c.Writing():
		return Problem(ErrWriting)
	case a.pending.Len() == 0:
		return Problem(ErrNothingPending)
	}
	a.push(a.opt.Review(c))
	return nil
}

func (a *App) discard(c *Context) tea.Cmd {
	n := a.pending.Len()
	switch {
	case c.Writing():
		return Problem(ErrWriting)
	case n == 0:
		return Problem(ErrNothingPending)
	}
	a.push(&Confirm{
		Title: "Discard",
		Body:  []string{fmt.Sprintf("Discard %s? Nothing was written to the mouse.", plural(n, "pending edit", "pending edits"))},
		Yes: func(string) tea.Cmd {
			a.pending.Clear()
			return Notice(plural(n, "pending edit", "pending edits") + " discarded.")
		},
	})
	return nil
}

func (a *App) reload(c *Context) tea.Cmd {
	if c.Writing() {
		return Problem(ErrWriting)
	}
	return tea.Batch(Notice("Reloading."), a.call("Reloaded.", a.api.Reload))
}

func (a *App) clearConflict() tea.Cmd {
	a.push(&Confirm{
		Title: "Clear the conflict",
		Body: []string{"Did you close every other configurator, such as a ProtoArc HUB tab? " +
			"arcctl clears the conflict only after the receiver has been quiet for a while."},
		Yes: func(string) tea.Cmd {
			return a.call("Conflict cleared.", func(ctx context.Context) error {
				err := a.api.ClearConflict(ctx)
				if errors.Is(err, session.ErrConflict) {
					return errNotQuiet
				}
				return err
			})
		},
	})
	return nil
}

var (
	errNotQuiet   = errors.New("the receiver has not been quiet long enough yet; close every other configurator, wait a few seconds and press c again")
	errUnapproved = errors.New("a write starts only from the review screen or the recovery prompt")
)

// startWrite runs r when the open dialog it came from approves it (D2).
func (a *App) startWrite(r writeRequest) tea.Cmd {
	switch {
	case a.write != nil:
		return Problem(ErrWriting)
	case a.opt.Mode == ModeReadOnly:
		return Problem(ErrReadOnlyMode)
	case r.from == nil || !a.isOpen(r.from) || !r.from.approves(r):
		return Problem(errUnapproved)
	}
	if a.opt.Mode == ModeDryRun {
		r.gates.DryRun = true
	}
	q := newEvents()
	a.write, a.writer, a.quitAfterWrite = newWriting(r, q), r.from, false
	return tea.Batch(runWrite(a.ctx, a.api, r, q), q.listen())
}

func (a *App) endWrite(d WriteDoneMsg) tea.Cmd {
	w := a.write
	a.write = nil
	if d.Kind == safety.KindApply && d.Err == nil {
		a.pending.Clear()
	}
	if rv, ok := a.writer.(*reviewDialog); ok && a.isOpen(rv) && d.Err != nil && d.Outcome.Run != "" {
		// The review shows the recovery prompt of the run it stopped.
		a.prompted[d.Outcome.Run] = true
	}
	a.notice = writeNotice(w, d)
	cmds := []tea.Cmd{a.broadcast(d)}
	a.writer = nil
	switch {
	case a.quitAfterWrite:
		cmds = append(cmds, tea.Quit)
	case a.isOpen(a.quitPrompt):
		a.closeDialog(a.quitPrompt)
		a.quitPrompt = a.quitConfirm()
		a.push(a.quitPrompt)
	}
	return tea.Batch(cmds...)
}

func writeNotice(w *writing, d WriteDoneMsg) noticeMsg {
	what := map[safety.RunKind]string{safety.KindApply: "Apply", safety.KindRevert: "Revert", safety.KindRecover: "Recovery"}[d.Kind]
	if w.dryRun {
		what = "Dry run of the " + strings.ToLower(what)
	}
	out := d.Outcome
	switch writeEnd(d) {
	case reviewEndVerified:
	case reviewEndStopped:
		return noticeMsg{text: fmt.Sprintf("%s stopped: %d of %d records verified. %s", what, out.Verified, out.Ops, plain(d.Err)), bad: true}
	case reviewEndAborted:
		return noticeMsg{text: what + " stopped at your request; nothing was written."}
	default:
		return noticeMsg{text: what + " refused: " + plain(d.Err), bad: true}
	}
	text := fmt.Sprintf("%s done: %d of %d records verified", what, out.Verified, out.Ops)
	if out.Ops == 0 {
		text = what + " done: nothing to write"
	}
	if w.dryRun {
		text += fmt.Sprintf(", %s to the overlay", plural(len(out.Packets), "packet", "packets"))
	}
	text += "."
	if n := len(out.Backups); n > 0 {
		return noticeMsg{text: text + " Backup saved:", path: out.Backups[n-1]}
	}
	return noticeMsg{text: text}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

var (
	cmdPrefix = regexp.MustCompile(`cmd \d+ \([^)]*\): `)
	opRef     = regexp.MustCompile(`op \d+ \((neutralise|body|bind|record) (\d+\+\d+)\)`)
	opWhat    = map[string]string{"neutralise": "binding", "body": "body", "bind": "binding", "record": "record"}
)

// plain is an error's text for the screen: without the package prefixes
// and the wire command, and with a planned op named by its record.
func plain(err error) string {
	s := err.Error()
	for _, p := range []string{"safety: ", "session: ", "plan: ", "mouse: ", "wire: ", "hidio: "} {
		s = strings.ReplaceAll(s, p, "")
	}
	s = cmdPrefix.ReplaceAllString(s, "")
	s = opRef.ReplaceAllStringFunc(s, func(m string) string {
		g := opRef.FindStringSubmatch(m)
		return "the " + opWhat[g[1]] + " at " + g[2]
	})
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}
