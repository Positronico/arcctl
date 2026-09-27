package tui

import (
	"bytes"
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
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// NewReview returns the builder of the Review & Apply dialog (§7.8) for
// Options.Review. api runs the preflight before the confirmation and stops a
// running write; with a nil api the preflight runs only as part of the
// write.
func NewReview(api session.API) func(c *Context) Dialog {
	return func(c *Context) Dialog { return newReview(c, api, safety.KindApply) }
}

// reviewCheckWait bounds the preflight the review asks for.
const reviewCheckWait = 30 * time.Second

type reviewStep uint8

const (
	reviewPlan     reviewStep = iota // the plan and what gates it
	reviewChecking                   // the session's preflight runs
	reviewConfirm                    // y, or the typed phrase
	reviewRunning                    // the write runs: its backups, then its ops
	reviewPartial                    // the first full backup is partial: accept it or stop
	reviewDone                       // how the write ended
)

var errNoMouse = errors.New("no mouse is loaded to plan against: plug in the receiver and wake the mouse; the edits stay staged")

// reviewDialog shows a write with everything that gates it, takes the
// confirmation the tiers ask for, and follows the write to its end. It
// reviews either the staged edits (an apply) or the undo of the journal's
// last run (a revert). Every write goes through the session: its preflight,
// the I1 backups, the journal and the read-back.
type reviewDialog struct {
	api  session.API
	kind safety.RunKind // safety.KindApply or safety.KindRevert
	step reviewStep

	// What the plan was made from.
	rev   int
	image *flash.Image
	model *catalog.Model
	last  *safety.Run // the journal's last run, as the snapshot had it

	edits  []Staged
	cursor int
	target *safety.Run // the run a revert undoes
	plan   plan.Plan
	// planErr is why there is no plan. For a revert whose records the loaded
	// image lacks, preview is set: the rows come from the journal, and the
	// session reads the records when it reverts.
	planErr  error
	preview  bool
	refused  map[string]string // staged key -> why the planner refuses that edit alone
	rows     []reviewRow
	warnings []mouse.Warning
	gateOps  []plan.Op // the ops whose tiers gate the write: the plan's, or the run's for a revert (T97)
	phrase   string

	gen       int     // bumped whenever the plan changes
	checked   []error // failures of the last preflight of this plan
	checkedOK bool
	note      string

	input reviewInput
	pager
	sent *writeRequest // the write the review asked for and the shell has not started yet

	run reviewRun
}

func newReview(c *Context, api session.API, kind safety.RunKind) *reviewDialog {
	d := &reviewDialog{api: api, kind: kind}
	d.replan(c)
	return d
}

// reviewCheckMsg carries the result of the preflight the review asked for.
type reviewCheckMsg struct {
	gen int
	err error
}

func (d *reviewDialog) Update(c *Context, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case reviewCheckMsg:
		return d.checkDone(msg)
	case OpEventMsg:
		d.event(msg.Event)
		return nil
	case WriteDoneMsg:
		if d.step == reviewRunning && msg.Kind == d.kind {
			d.finish(c, msg)
		}
		return nil
	case tea.PasteMsg:
		if d.step == reviewConfirm {
			d.input.pasted = true
		}
		return nil
	case tea.KeyPressMsg:
		d.sync(c)
		return d.key(c, msg)
	}
	d.sync(c)
	return nil
}

// approves takes the write the review asked for, once.
func (d *reviewDialog) approves(r writeRequest) bool {
	if d.step != reviewRunning || d.sent == nil || !sameRequest(r, *d.sent) {
		return false
	}
	d.sent = nil
	return true
}

// sync plans again when what the plan was made from changed. A plan that
// changed sends a confirmation in progress back to the review.
func (d *reviewDialog) sync(c *Context) {
	switch d.step {
	case reviewDone:
		d.run.syncRecovery(c)
		return
	case reviewRunning, reviewPartial:
		return
	}
	if !d.stale(c) {
		return
	}
	old, oldErr := d.plan, d.planErr
	d.replan(c)
	if reviewSamePlan(old, d.plan) && reviewErrText(oldErr) == reviewErrText(d.planErr) {
		return
	}
	d.gen++
	d.checked, d.checkedOK = nil, false
	if d.step != reviewPlan {
		d.back("The plan changed: the mouse, the journal or the staged edits changed. Review it again.")
	}
}

func (d *reviewDialog) stale(c *Context) bool {
	switch {
	case c.Image() != d.image || c.Model() != d.model:
		return true
	case d.kind == safety.KindRevert:
		return journalLast(c) != d.last
	}
	return c.Pending.Rev() != d.rev
}

func journalLast(c *Context) *safety.Run {
	if js := c.Snapshot.Journal; js != nil {
		return js.Last
	}
	return nil
}

func (d *reviewDialog) replan(c *Context) {
	d.rev, d.image, d.model, d.last = c.Pending.Rev(), c.Image(), c.Model(), journalLast(c)
	d.plan, d.planErr, d.preview = plan.Plan{}, nil, false
	d.refused, d.rows, d.warnings, d.gateOps = nil, nil, nil, nil
	if d.kind == safety.KindRevert {
		d.replanRevert(c)
	} else {
		d.replanEdits(c)
	}
	if d.planErr == nil {
		d.rows = reviewRows(c, d.plan)
		d.warnings = mouse.WebCompat(c.Model(), d.plan)
	}
	if d.gateOps == nil {
		d.gateOps = d.plan.Ops
	}
	d.phrase = safety.ConfirmPhrase(d.gateOps)
}

func (d *reviewDialog) replanEdits(c *Context) {
	d.edits = c.Pending.List()
	d.cursor = max(0, min(d.cursor, len(d.edits)-1))
	if m := c.Model(); m == nil || m.Family != catalog.FamilyMouse || c.Image() == nil {
		d.planErr = errNoMouse
		return
	}
	d.plan, d.planErr = c.Plan()
	if d.planErr != nil {
		d.refused = d.blame(c)
	}
}

// blame plans each staged edit alone and names the ones the planner
// refuses, so the user knows which one to drop.
func (d *reviewDialog) blame(c *Context) map[string]string {
	out := map[string]string{}
	for _, e := range d.edits {
		if _, err := mouse.PlanEdits(c.Model(), c.Image(), []mouse.Edit{e.Edit}, c.MouseOptions()); err != nil {
			out[e.Key] = reviewErrText(err)
		}
	}
	return out
}

func reviewSamePlan(a, b plan.Plan) bool {
	if a.Device != b.Device || (a.Profile == nil) != (b.Profile == nil) || a.Profile != nil && *a.Profile != *b.Profile {
		return false
	}
	return slices.EqualFunc(a.Ops, b.Ops, func(x, y plan.Op) bool {
		return x.Seq == y.Seq && x.Extent == y.Extent && x.Phase == y.Phase && x.Tier == y.Tier && x.Desc == y.Desc &&
			bytes.Equal(x.Old, y.Old) && bytes.Equal(x.New, y.New)
	})
}

var reviewEditPrefix = regexp.MustCompile(`^edit \d+: `)

// reviewErrText is err in plain words, without the index PlanEdits gives the
// edit it refused.
func reviewErrText(err error) string {
	if err == nil {
		return ""
	}
	return plain(errors.New(reviewEditPrefix.ReplaceAllString(err.Error(), "")))
}

// back returns to the review with a note.
func (d *reviewDialog) back(note string) {
	d.step, d.note, d.input, d.scroll = reviewPlan, note, reviewInput{}, 0
}

func (d *reviewDialog) dryRun(c *Context) bool { return c.Mode == ModeDryRun }

// count is the number of gating ops at tier t.
func (d *reviewDialog) count(t catalog.Tier) int {
	n := 0
	for _, op := range d.gateOps {
		if op.Tier == t {
			n++
		}
	}
	return n
}

// canWrite says why no write of this kind can start now.
func (d *reviewDialog) canWrite(c *Context) error {
	if d.kind == safety.KindRevert {
		return c.CanWrite()
	}
	return c.CanApply()
}

// blocker is what keeps the plan from being written at all, whatever the
// preflight finds: a plan that cannot be made, a session that cannot write
// now, or a tier the flags of this run do not allow.
func (d *reviewDialog) blocker(c *Context) error {
	if d.planErr != nil && !d.preview {
		return d.planErr
	}
	if err := d.canWrite(c); err != nil {
		return err
	}
	if js := c.Snapshot.Journal; js != nil && js.Err != nil {
		return fmt.Errorf("%w: %w", safety.ErrJournal, js.Err)
	}
	if d.dryRun(c) {
		return nil
	}
	if n := d.count(catalog.Untested); n > 0 && !c.Gates.AllowUntested {
		return fmt.Errorf("%w (%s): restart arcctl with --allow-untested", safety.ErrUntested, plural(n, "record", "records"))
	}
	if n := d.count(catalog.Experimental); n > 0 && !c.Gates.Experimental {
		return fmt.Errorf("%w (%s): restart arcctl with --experimental", safety.ErrExperimental, plural(n, "record", "records"))
	}
	return nil
}

func (d *reviewDialog) key(c *Context, k tea.KeyPressMsg) tea.Cmd {
	switch d.step {
	case reviewPlan:
		return d.planKey(c, k)
	case reviewChecking:
		if k.String() == "esc" {
			d.gen++
			d.back("")
		}
		return nil
	case reviewConfirm:
		return d.confirmKey(c, k)
	case reviewRunning:
		return d.runningKey(c, k)
	case reviewPartial:
		return d.partialKey(c, k)
	}
	return d.doneKey(c, k)
}

func (d *reviewDialog) planKey(c *Context, k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if d.pager.key(s) {
		return nil
	}
	switch s {
	case "esc":
		return CloseDialog
	case "q":
		return askQuit
	case "enter":
		return d.proceed(c)
	}
	if d.kind != safety.KindApply {
		return nil
	}
	switch s {
	case "up", "k":
		d.cursor = max(0, d.cursor-1)
		d.follow = true
	case "down", "j":
		d.cursor = max(0, min(len(d.edits)-1, d.cursor+1))
		d.follow = true
	case "d", "delete":
		if d.cursor < len(d.edits) {
			e := d.edits[d.cursor]
			if err := guardedDrop(c, e); err != nil {
				return Problem(err)
			}
			c.Pending.Drop(e.Key)
			d.sync(c)
			return Notice("Dropped: " + reviewEditName(c, e) + ".")
		}
	case "u":
		return d.discard(c)
	}
	return nil
}

func (d *reviewDialog) discard(c *Context) tea.Cmd {
	p, n := c.Pending, c.Pending.Len()
	if n == 0 {
		return Problem(ErrNothingPending)
	}
	return OpenDialog(&Confirm{
		Title: "Discard",
		Body:  []string{fmt.Sprintf("Discard %s? Nothing was written to the mouse.", plural(n, "pending edit", "pending edits"))},
		Yes: func(string) tea.Cmd {
			p.Clear()
			return tea.Batch(closeDialog(d), Notice(plural(n, "pending edit", "pending edits")+" discarded."))
		},
	})
}

// proceed leaves the review for the preflight, or for the confirmation when
// there is no session to ask.
func (d *reviewDialog) proceed(c *Context) tea.Cmd {
	if d.planErr == nil && len(d.plan.Ops) == 0 {
		if d.kind == safety.KindRevert {
			return tea.Batch(CloseDialog, Notice("Nothing to write: the mouse already holds what the revert would write."))
		}
		if len(d.edits) > 0 {
			n := c.Pending.Len()
			c.Pending.Clear()
			return tea.Batch(CloseDialog, Notice(fmt.Sprintf("Nothing to write: the mouse already holds what %s asked for; dropped.",
				plural(n, "staged edit", "staged edits"))))
		}
	}
	if err := d.blocker(c); err != nil {
		return Problem(err)
	}
	d.note = ""
	if d.api == nil {
		d.step = reviewConfirm
		return nil
	}
	d.step = reviewChecking
	d.checked, d.checkedOK = nil, false
	api, p, g, gen, revert := d.api, d.plan, d.gates(c, d.phrase), d.gen, d.kind == safety.KindRevert
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviewCheckWait)
		defer cancel()
		if revert {
			return reviewCheckMsg{gen: gen, err: api.PreflightRevert(ctx, g)}
		}
		return reviewCheckMsg{gen: gen, err: api.Preflight(ctx, p, g)}
	}
}

// gates are what this run allows the write, with typed as the confirmation.
func (d *reviewDialog) gates(c *Context, typed string) safety.Gates {
	g := c.Gates
	g.Confirm = typed
	if d.dryRun(c) {
		g.DryRun = true
	}
	return g
}

func (d *reviewDialog) checkDone(m reviewCheckMsg) tea.Cmd {
	if m.gen != d.gen || d.step != reviewChecking {
		return nil
	}
	d.checked = reviewFailures(m.err)
	if len(d.checked) == 0 {
		d.checkedOK = true
		d.step = reviewConfirm
		return nil
	}
	d.back("")
	return nil
}

// reviewFailures lists what a preflight refused, leaving out the tier gates
// and the typed confirmation, which the review asks for itself.
func reviewFailures(err error) []error {
	if err == nil {
		return nil
	}
	var pe *safety.PreflightError
	if !errors.As(err, &pe) {
		return []error{err}
	}
	var out []error
	for _, f := range pe.Failures {
		if errors.Is(f, safety.ErrUntested) || errors.Is(f, safety.ErrExperimental) || errors.Is(f, safety.ErrConfirm) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func (d *reviewDialog) confirmKey(c *Context, k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "esc" {
		d.back("")
		return nil
	}
	if d.needsPhrase(c) {
		if d.input.key(k, d.phrase) {
			return d.write(c, d.phrase)
		}
		return nil
	}
	switch s {
	case "y", "Y":
		return d.write(c, "")
	case "n", "N":
		d.back("")
	}
	return nil
}

// needsPhrase reports whether the write needs the phrase typed out: any op
// below Verified, unless it is a dry run, which writes nothing (T97).
func (d *reviewDialog) needsPhrase(c *Context) bool { return d.phrase != "" && !d.dryRun(c) }

func (d *reviewDialog) write(c *Context, typed string) tea.Cmd {
	if err := d.blocker(c); err != nil {
		d.back("")
		return Problem(err)
	}
	d.run = reviewRun{gates: d.gates(c, typed)}
	return d.start()
}

// start asks the shell for the write; the shell starts it because the
// review approves it.
func (d *reviewDialog) start() tea.Cmd {
	d.step, d.scroll = reviewRunning, 0
	r := writeRequest{kind: d.kind, gates: d.run.gates, from: d}
	if d.planErr == nil {
		r.plan = d.plan
	}
	d.run.begin(r)
	d.sent = &r
	return r.cmd()
}

func (d *reviewDialog) Hints(c *Context) []key.Binding {
	switch d.step {
	case reviewPlan:
		var out []key.Binding
		if d.kind == safety.KindApply {
			out = []key.Binding{hint(dilArrows(c, "↑/↓", "up/down"), "select"), hint("d", "drop edit"), hint("u", "discard all")}
		}
		out = append(out, d.pager.hints()...)
		return append(out, hint("enter", "continue"), hint("esc", "close"))
	case reviewChecking:
		return []key.Binding{hint("esc", "back")}
	case reviewConfirm:
		if d.needsPhrase(c) {
			return []key.Binding{hint("enter", "confirm"), hint("esc", "back")}
		}
		return []key.Binding{hint("y", "go on"), hint("n/esc", "back")}
	case reviewRunning:
		out := []key.Binding{hint("esc", "hide"), hint("q", "quit")}
		if d.api != nil {
			out = append(out, hint("s", "stop after this record"))
		}
		return out
	case reviewPartial:
		return []key.Binding{hint("y", "write anyway"), hint("n/esc", "stop")}
	}
	return append(d.run.hints(), d.pager.hints()...)
}

// reviewEditName names a staged edit: a button's in the labels the screens
// use now, anything else as its tab described it.
func reviewEditName(c *Context, e Staged) string {
	if slot, ok := editSlot(e.Edit); ok {
		if m := c.Model(); m != nil {
			return buttonWhere(m, slot) + ": " + editText(m, e.Edit, c.OS)
		}
	}
	if e.Desc != "" {
		return e.Desc
	}
	return e.Key
}

// reviewInput reads a typed confirmation as key events and echoes it on a
// plain line (§7.8); a paste is ignored.
type reviewInput struct {
	typed  []rune
	wrong  bool
	pasted bool
}

// key takes k and reports whether enter confirmed the phrase.
func (in *reviewInput) key(k tea.KeyPressMsg, phrase string) bool {
	in.pasted = false
	switch k.String() {
	case "enter":
		if strings.TrimSpace(string(in.typed)) == phrase {
			return true
		}
		in.wrong = true
	case "backspace":
		if n := len(in.typed); n > 0 {
			in.typed = in.typed[:n-1]
		}
		in.wrong = false
	case "ctrl+u":
		in.typed, in.wrong = nil, false
	default:
		if k.Text != "" {
			in.typed = append(in.typed, []rune(k.Text)...)
			in.wrong = false
		}
	}
	return false
}

func (in *reviewInput) view(c *Context) string { return phraseLine(c, in.typed, in.wrong, in.pasted) }
