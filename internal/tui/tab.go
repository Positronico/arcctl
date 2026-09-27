package tui

import (
	"errors"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// Tab is one screen under the tab bar. The shell calls it only from its
// Update goroutine.
type Tab interface {
	Title() string
	// Needs lists the features the tab shows (§4.7). The tab is shown only
	// when one of them is present at a visible tier; an empty list means
	// always.
	Needs() []mouse.Feature
	// Update gets the active tab's keys, and every other message (snapshots,
	// write progress, the results of the tab's own commands) whether the
	// tab is active or not.
	Update(c *Context, msg tea.Msg) tea.Cmd
	// View renders the body in w columns and h rows.
	View(c *Context, w, h int) string
	// Hints are the tab's own keys, shown in the footer before the shell's.
	Hints(c *Context) []key.Binding
	// Capturing reports that the tab takes every key, such as while a text
	// field has the focus; only ctrl+c still reaches the shell.
	Capturing() bool
}

// Dialog is a modal screen over the active tab: it takes every key until it
// closes itself with CloseDialog.
type Dialog interface {
	Update(c *Context, msg tea.Msg) tea.Cmd
	View(c *Context, w, h int) string
	Hints(c *Context) []key.Binding
}

// Context is what the shell shares with the tabs and dialogs on every call;
// it is built for that call only and must not be kept.
type Context struct {
	// Snapshot is the latest session snapshot; never nil.
	Snapshot *session.Snapshot
	Pending  *Pending
	Mode     Mode
	Gates    safety.Gates
	OS       keys.OS
	Glyphs   Glyphs
	Styles   Styles
	Now      time.Time
	// Width and Height are the terminal's size.
	Width, Height int

	verified catalog.Verifications
	config   *mouse.Config
	writing  bool
	dataDir  string
	notes    []string
}

// Model is the resolved model, nil before the handshake or when unknown.
func (c *Context) Model() *catalog.Model { return c.Snapshot.Model }

// Image is the loaded image with the dry-run writes of this session on top;
// nil before the first load.
func (c *Context) Image() *flash.Image {
	if c.Snapshot.DryRun != nil {
		return c.Snapshot.DryRun
	}
	return c.Snapshot.Image
}

// Config is Image decoded for a mouse, nil when there is none.
func (c *Context) Config() *mouse.Config { return c.config }

// MouseOptions are what the planner and the tiers need about this device.
func (c *Context) MouseOptions() mouse.Options {
	sn := c.Snapshot
	o := mouse.Options{Device: sn.Identity, Firmware: sn.Versions.Mouse, Verified: c.verified}
	if sn.Profile.Supported {
		v := sn.Profile.Value
		o.Profile = &v
	}
	return o
}

// Plan turns the staged edits into a plan against the image the tabs show.
func (c *Context) Plan() (plan.Plan, error) {
	return c.Pending.Plan(c.Model(), c.Image(), c.MouseOptions())
}

// Tier is f's tier on this device, and whether the planner writes it.
func (c *Context) Tier(f mouse.Feature) (catalog.Tier, bool) {
	return f.Tier(c.Model(), c.MouseOptions())
}

// Visible reports whether f is shown: only on a mouse, Experimental
// features only with --experimental (D5), Off never.
func (c *Context) Visible(f mouse.Feature) bool {
	if m := c.Model(); m == nil || m.Family != catalog.FamilyMouse {
		return false
	}
	t, _ := c.Tier(f)
	switch t {
	case catalog.Off:
		return false
	case catalog.Experimental:
		return c.Gates.Experimental
	}
	return true
}

// Shows reports whether a tab with these needs is shown.
func (c *Context) Shows(needs []mouse.Feature) bool {
	if len(needs) == 0 {
		return true
	}
	for _, f := range needs {
		if c.Visible(f) {
			return true
		}
	}
	return false
}

// Writing reports that a write runs.
func (c *Context) Writing() bool { return c.writing }

var (
	ErrReadOnlyMode   = errors.New("this session is read-only")
	ErrWriting        = errors.New("a write is running")
	ErrNothingPending = errors.New("nothing is pending")
	ErrNotReady       = errors.New("the mouse is not ready for writes")
	ErrUnsettled      = errors.New("an earlier write is unfinished; settle it first")
	ErrAsleep         = errors.New("the mouse is asleep; wake it to apply")
	ErrWritesOff      = errors.New("writes are disabled while another client talks to the receiver")
)

// CanApply says why the staged edits cannot be applied now; nil when they
// can. The session's preflight still checks everything again.
func (c *Context) CanApply() error {
	if c.Mode != ModeReadOnly && !c.writing && c.Pending.Len() == 0 {
		return ErrNothingPending
	}
	return c.CanWrite()
}

// CanWrite says why no write can start now; nil when one can.
func (c *Context) CanWrite() error {
	switch {
	case c.Mode == ModeReadOnly:
		return ErrReadOnlyMode
	case c.writing:
		return ErrWriting
	}
	switch c.Snapshot.State {
	case session.Ready:
		return nil
	case session.Recovering:
		return ErrUnsettled
	case session.Offline:
		return ErrAsleep
	case session.Conflict, session.SuspectedConflict:
		return ErrWritesOff
	}
	return ErrNotReady
}

// Messages the shell sends to every tab and to the open dialog.
type (
	// SnapshotMsg carries a newer session snapshot.
	SnapshotMsg struct{ Snapshot *session.Snapshot }
	// OpEventMsg is one step of the running write.
	OpEventMsg struct{ Event safety.OpEvent }
	// WriteDoneMsg ends a write started with Apply, Revert or Recover.
	WriteDoneMsg struct {
		Kind    safety.RunKind
		Outcome session.Outcome
		Err     error
	}
)

type (
	openDialogMsg  struct{ d Dialog }
	closeDialogMsg struct{ d Dialog } // nil closes the one on top
	noticeMsg      struct {
		text string
		bad  bool
		path string // shown after text, cut in the middle when it is too long
	}
	quitMsg         struct{}
	toggleLabelsMsg struct{}
)

// OpenDialog shows d over the active tab, or over the open dialog, which
// comes back when d closes.
func OpenDialog(d Dialog) tea.Cmd { return func() tea.Msg { return openDialogMsg{d} } }

// CloseDialog closes the dialog on top.
func CloseDialog() tea.Msg { return closeDialogMsg{} }

// closeDialog closes d wherever it is in the stack; nothing when it is not
// open.
func closeDialog(d Dialog) tea.Cmd { return func() tea.Msg { return closeDialogMsg{d} } }

// askQuit hands quitting to the shell, which asks first when a write runs
// or edits are pending.
func askQuit() tea.Msg { return quitMsg{} }

// ToggleLabels switches the key labels and preset tables between mac and
// win, for every tab and dialog.
func ToggleLabels() tea.Msg { return toggleLabelsMsg{} }

// Notice shows text above the footer until the next key.
func Notice(text string) tea.Cmd { return func() tea.Msg { return noticeMsg{text: text} } }

// Problem is a Notice that reports a failure.
func Problem(err error) tea.Cmd {
	return func() tea.Msg { return noticeMsg{text: plain(err), bad: true} }
}
