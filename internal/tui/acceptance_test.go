package tui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// accEmulated is the shell with its default tabs and review over a real
// session and an emulated EM11 Pro seeded from the demo backup.
type accEmulated struct {
	h   *harness
	dev *emu.Device
	dir string
}

func accEmulate(t *testing.T) *accEmulated {
	t.Helper()
	src, err := backup.Open(filepath.Join("..", "..", "testdata", "demo-em11.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := src.File.CatalogModel()
	if !ok {
		t.Fatal("the demo backup's model is not in the catalog")
	}
	bus := emu.New(emu.Options{})
	t.Cleanup(bus.Close)
	dev, err := bus.Add(emu.Config{Mouse: &emu.Mouse{Model: m, Image: src.Image.Clone(), Firmware: emu.Version{Major: 1},
		Battery: emu.Battery{Level: 80, MilliVolts: 3900}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := session.New(session.Options{
		Devices: bus,
		Clients: rvClientsOf(bus),
		Timing:  session.Timing{Try: 25 * time.Millisecond, ProbeTry: 25 * time.Millisecond, Online: time.Hour, Battery: time.Hour},
		Writes: &session.Writes{
			Journal:  filepath.Join(dir, "journal"),
			Backups:  backup.Store{Root: filepath.Join(dir, "backups"), Tool: "arcctl test", Source: backup.SourceEmulator},
			Lock:     func() error { return nil },
			Executor: safety.Options{OfflineWait: 300 * time.Millisecond, Poll: 5 * time.Millisecond},
		},
	})
	h := newHarness(t, s.Snapshot(), func(o *Options) {
		o.Session = s
		o.Source = "emulator"
		o.Gates.AllowUntested = true
		o.OS = keys.Mac
		o.Tabs = nil
	})
	h.screen(80, 24)
	run := make(chan struct{})
	go func() {
		defer close(run)
		_ = s.Run(h.app.ctx)
	}()
	t.Cleanup(func() {
		h.app.Close()
		<-run
	})
	h.flush(func() bool { return h.app.sn.State == session.Ready && h.app.sn.Journal != nil && h.app.config != nil })
	return &accEmulated{h: h, dev: dev, dir: dir}
}

// TestAcceptance is the M4 exit script (PLAN §13), at 80x24 and through
// keys only: slot 3 becomes Play/Pause from the Buttons tab, DPI stage 2
// changes on the DPI tab, and U reverts the last apply. Each goes through
// the review, the session's preflight, journal and read-back, and each run
// ends verified in the journal.
func TestAcceptance(t *testing.T) {
	e := accEmulate(t)
	h := e.h
	e.see(t, "[1 Buttons]", "Backward    3    Ctrl+Tab", "Wheel Click 2    Cmd+V (Paste)", "Forward     4    Cmd+Tab", "DPI Cycle   5    Cmd+C (Copy)")

	e.cursorTo(t, "Backward")
	h.keys("enter", "shift+tab", "/")
	h.typeText("play/pause")
	h.keys("enter", "enter")
	e.see(t, "1 pending", "Backward    3    Play/Pause")
	media := e.apply(t)

	h.keys("2")
	e.see(t, "[2 DPI]")
	h.keys("down", "enter")
	h.typeText("1600")
	h.keys("enter")
	e.see(t, "1 pending", "2  1200 → 1600", "Pending    DPI stage 2: 1600")
	dpi := e.apply(t)
	e.see(t, "Stage 2  X 1600, Y 1600")

	revert := e.write(t, "U", "Review the revert", "Revert", "1600 → 1200")
	if revert.Kind != safety.KindRevert || revert.Of != dpi.ID {
		t.Errorf("the revert run %s undoes %q, want the DPI apply %s", revert.ID, revert.Of, dpi.ID)
	}

	st := e.journal(t)
	if len(st.Runs) != 3 || st.Runs[0].ID != media.ID || st.Runs[1].ID != dpi.ID || st.Runs[2].ID != revert.ID || !st.Clean() {
		t.Fatalf("the journal holds %d runs, open %d", len(st.Runs), len(st.Open))
	}
	e.holds(t, st)
	h.flush(func() bool {
		cfg := h.app.config
		return cfg != nil && cfg.DPI[1].DPI.X == 1200 && cfg.Shortcuts[3].Format(keys.Mac) == "Play/Pause"
	})
	h.keys("1")
	e.see(t, "Backward    3    Play/Pause")
	h.keys("2")
	e.see(t, "Stage 2  X 1200, Y 1200")
}

// apply reviews and applies the pending edits with the typed phrase, and
// returns the run once the journal holds it verified.
func (e *accEmulated) apply(t *testing.T) *safety.Run {
	t.Helper()
	return e.write(t, "a", "Review & Apply", "Apply")
}

// write opens a review with k, checks its title and diff, goes on with the
// typed phrase and returns the run once the journal holds it verified.
func (e *accEmulated) write(t *testing.T, k, title, what string, diff ...string) *safety.Run {
	t.Helper()
	h := e.h
	h.keys(k)
	d, ok := h.app.dialog.(*reviewDialog)
	if !ok {
		t.Fatalf("%s opened %T\n%s", k, h.app.dialog, e.screen(t))
	}
	e.see(t, append([]string{title}, diff...)...)
	h.keys("enter")
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewConfirm {
		t.Fatalf("the preflight did not pass: %v\n%s", d.checked, e.screen(t))
	}
	e.see(t, "write untested")
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return h.app.write == nil && d.step == reviewDone })
	e.see(t, "verified")
	run := e.verified(t, what)
	h.keys("enter")
	if h.app.dialog != nil || h.app.Pending().Len() != 0 {
		t.Fatalf("after the apply: dialog %T, %d pending\n%s", h.app.dialog, h.app.Pending().Len(), e.screen(t))
	}
	e.settle(run)
	return run
}

// settle waits for the session to report run as the journal's last change,
// as the screen shows it once the write ends.
func (e *accEmulated) settle(run *safety.Run) {
	e.h.flush(func() bool {
		sn := e.h.app.sn
		return sn.State == session.Ready && sn.Journal != nil && sn.Journal.Last != nil && sn.Journal.Last.ID == run.ID
	})
}

// verified checks the notice of the write that just ended and returns its
// run from the journal, every op verified.
func (e *accEmulated) verified(t *testing.T, what string) *safety.Run {
	t.Helper()
	n := e.h.app.notice
	if n.bad || !strings.HasPrefix(n.text, what+" done: ") {
		t.Fatalf("notice %q\n%s", n.text, e.screen(t))
	}
	st := e.journal(t)
	run := st.Runs[len(st.Runs)-1]
	if !run.Ended || !run.Complete || len(run.Ops) == 0 {
		t.Fatalf("run %s: ended %v, complete %v, %d ops", run.ID, run.Ended, run.Complete, len(run.Ops))
	}
	for _, op := range run.Ops {
		if op.State != safety.StateVerified {
			t.Errorf("run %s op %d (%s): %v, want verified", run.ID, op.Op.Seq, op.Op.Desc, op.State)
		}
	}
	return run
}

func (e *accEmulated) journal(t *testing.T) *safety.Status {
	t.Helper()
	st, err := safety.Load(filepath.Join(e.dir, "journal", e.h.app.sn.Identity.Key()))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// holds checks that the emulated flash holds, at every extent the runs of
// st wrote, what the last of them left there.
func (e *accEmulated) holds(t *testing.T, st *safety.Status) {
	t.Helper()
	want := map[flash.Extent][]byte{}
	for _, r := range st.Runs {
		for _, op := range r.Ops {
			want[op.Op.Extent] = op.Op.New
		}
	}
	for ext, b := range want {
		if got, _ := e.dev.Image().Get(ext); !bytes.Equal(got, b) {
			t.Errorf("the mouse holds % x at %v, want % x", got, ext, b)
		}
	}
}

func (e *accEmulated) screen(t *testing.T) string {
	t.Helper()
	return e.h.screen(80, 24)
}

// see checks that the 80x24 screen shows each text.
func (e *accEmulated) see(t *testing.T, texts ...string) {
	t.Helper()
	s := e.screen(t)
	for _, text := range texts {
		if !strings.Contains(s, text) {
			t.Fatalf("the screen does not show %q:\n%s", text, s)
		}
	}
}

// cursorTo moves the Buttons cursor down to the row that shows label.
func (e *accEmulated) cursorTo(t *testing.T, label string) {
	t.Helper()
	for range 16 {
		for _, l := range strings.Split(e.screen(t), "\n") {
			if strings.HasPrefix(l, e.h.app.glyphs.Cursor+" ") && strings.Contains(l, label) {
				return
			}
		}
		e.h.keys("j")
	}
	t.Fatalf("no row shows %q:\n%s", label, e.screen(t))
}
