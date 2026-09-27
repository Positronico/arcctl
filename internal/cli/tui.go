package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// TUI is what the terminal UI gets from the command line: a running session
// and what the global flags allow.
type TUI struct {
	Session session.API
	// ReadOnly means the session cannot write (a replay); DryRun that its
	// writes go to the overlay.
	ReadOnly bool
	DryRun   bool
	Gates    safety.Gates
	Source   string // backup.SourceDevice, SourceEmulator or SourceReplay
	OS       keys.OS
	ASCII    bool
	NoColor  bool
	Backups  session.Backups
	// DataDir is where the journal and the backups go; empty for a replay.
	DataDir string
	// Notes are what arcctl said on stderr before the TUI started, for the
	// TUI to show again.
	Notes []string
	// Permission, Console and OpenSettings answer the banners' questions to
	// the OS; nil where the OS or the source has no answer.
	Permission   func() string
	Console      func() (safety.Console, error)
	OpenSettings func() error
}

// runTUI starts the session the global flags describe and hands it to the
// TUI until the user quits.
func (r *runner) runTUI() error {
	r.cmd = "tui"
	if r.env.TUI == nil || !r.env.Terminal {
		fmt.Fprintln(r.out, "arcctl: the TUI needs a terminal. Commands:")
		r.commandList(r.out)
		return nil
	}
	if err := r.checkGlobals(); err != nil {
		return err
	}
	w, err := r.world()
	if err != nil {
		return err
	}
	defer w.close()
	var notes []string
	note := func(format string, args ...any) {
		n := fmt.Sprintf(format, args...)
		fmt.Fprintln(r.errw, "arcctl: "+n)
		notes = append(notes, strings.ToUpper(n[:1])+n[1:]+".")
	}
	if n := w.bodiesMissing; len(n) > 0 {
		note("%s holds no body for the shortcuts or macros bound to %s; the emulated mouse reads them "+
			"as erased flash (testdata/demo-em11.json in the source tree is flash-dump.bin with its shortcut bodies)", r.g.emulate, slotList(n))
	}
	t := TUI{
		DryRun:  r.g.dryRun,
		Gates:   r.g.gates(),
		Source:  w.source,
		OS:      r.keyOS(),
		ASCII:   r.g.ascii,
		NoColor: r.g.noColor,
		Console: func() (safety.Console, error) { return consoleOf(w.host) },
	}
	if w.source == backup.SourceReplay {
		t.ReadOnly = true
	} else {
		data, journal, backups, err := r.writeFolders(w)
		if err != nil {
			return err
		}
		if w.source == backup.SourceEmulator {
			note("the emulated mouse's journal and backups go to %s", data)
		}
		t.DataDir = data
		store := backup.Store{Root: backups, Tool: "arcctl " + r.env.Version, Source: w.source, OS: r.keyOS(), Now: r.env.Now}
		r.writes = &session.Writes{
			Journal: journal,
			Backups: store,
			// start takes the lock for a real device before the session runs.
			Lock:     func() error { return nil },
			Executor: r.env.Executor,
		}
		t.Backups = store
	}
	if w.source == backup.SourceDevice {
		t.Permission = func() string {
			p, err := w.host.Permission()
			if err != nil {
				return ""
			}
			return p.Hint()
		}
		if runtime.GOOS == "darwin" {
			t.OpenSettings = openInputMonitoring
		}
	}
	c, _, err := r.start(w)
	if err != nil {
		return err
	}
	defer c.stop()
	t.Session, t.Notes = c.s, notes
	return r.env.TUI(r.ctx, t)
}

// writeFolders are the data, journal and backups folders of a TUI session.
// The emulator's go to a new temporary folder, so its writes never reach
// the real device's journal (T120).
func (r *runner) writeFolders(w *world) (data, journal, backups string, err error) {
	if w.source == backup.SourceEmulator {
		tmp, err := os.MkdirTemp("", "arcctl-emulated-")
		if err != nil {
			return "", "", "", err
		}
		return tmp, filepath.Join(tmp, "journal"), filepath.Join(tmp, "backups"), nil
	}
	paths, err := r.env.Paths()
	if err != nil {
		return "", "", "", err
	}
	return paths.Data, paths.Journal, paths.Backups, nil
}

func openInputMonitoring() error {
	return exec.CommandContext(context.Background(), "open",
		"x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent").Run()
}

// slotList names slots as "slot 3" or "slots 2, 3 and 5".
func slotList(slots []int) string {
	if len(slots) == 1 {
		return "slot " + strconv.Itoa(slots[0])
	}
	parts := make([]string, len(slots))
	for i, k := range slots {
		parts[i] = strconv.Itoa(k)
	}
	return "slots " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
