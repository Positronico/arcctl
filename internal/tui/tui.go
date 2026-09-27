// Package tui is arcctl's terminal UI. The shell follows the session through
// its snapshots, shows a banner for every session state, hosts the tabs and
// their dialogs, and keeps the staged edits every tab shares. Writes go only
// through the session's staged apply: its preflight, backups, journal and
// read-back.
package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// Mode is what the session may do to the mouse; the header shows it.
type Mode uint8

const (
	ModeReadOnly Mode = iota
	ModeDryRun
	ModeEdit
)

var modeNames = [...]string{"read-only", "dry-run", "edit"}

func (m Mode) String() string {
	if int(m) < len(modeNames) {
		return modeNames[m]
	}
	return "mode?"
}

// Host answers what the banners ask the operating system. Any field may be
// nil.
type Host struct {
	// Permission says which app holds the device-access grant and how to
	// give it.
	Permission func() string
	// Console reports the screen lock and the Secure Input holder.
	Console func() (safety.Console, error)
	// OpenSettings opens the OS pane where device access is granted.
	OpenSettings func() error
}

type Options struct {
	Session session.API // required
	Mode    Mode
	// Gates are what the global flags allow a write; the review adds the
	// typed confirmation.
	Gates safety.Gates
	// Source is where the device comes from: "device", "emulator" or
	// "replay".
	Source  string
	OS      keys.OS
	ASCII   bool
	NoColor bool
	Host    Host
	// Backups saves the captures of the backup key; nil disables it.
	Backups session.Backups
	// DataDir is where the journal and the backups go; Info shows it.
	DataDir string
	// Library is the macro library file of the Macros tab; empty means
	// none.
	Library string
	// Notes are what arcctl said before the TUI started: the first notice,
	// and kept on the Info tab.
	Notes []string
	// Verified is the list of passed hardware stages; nil means the
	// catalog's.
	Verified catalog.Verifications
	Now      func() time.Time
	// Tabs are the tabs in their bar order; nil means the default set.
	Tabs []Tab
	// Review builds the Review & Apply dialog for the staged edits; nil
	// means the default one.
	Review func(c *Context) Dialog
}

// saveWait is how long quitting waits for the tabs' last background saves,
// such as the macro library's.
const saveWait = 5 * time.Second

// Run shows the TUI until the user quits or ctx ends, then waits up to
// saveWait for the saves still running; a save that failed or did not finish
// is in the error.
func Run(ctx context.Context, o Options) error {
	var opts []tea.ProgramOption
	if o.NoColor {
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	return run(ctx, o, saveWait, opts...)
}

// run is Run with the program's options and the wait for the saves given.
func run(ctx context.Context, o Options, wait time.Duration, opts ...tea.ProgramOption) error {
	if o.Session == nil {
		return errors.New("tui: no session")
	}
	app := New(ctx, o)
	defer app.Close()
	_, err := tea.NewProgram(app, append([]tea.ProgramOption{tea.WithContext(ctx)}, opts...)...).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		err = ctx.Err()
	}
	fctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	switch ferr := app.flush(fctx); {
	case ferr == nil:
		return err
	case err == nil || ctx.Err() != nil:
		return ferr
	default:
		return errors.Join(err, ferr)
	}
}
