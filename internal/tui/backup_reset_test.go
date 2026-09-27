package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// resetRecord is a test-only record of hardware test H7 for the snapshots'
// EM11 Pro on firmware v1.00; verified.json holds none.
var resetRecord = catalog.Verification{Model: "7B04", Feature: hidio.ResetFeature, Firmware: "v1.00", Stage: "H7", Date: "2026-10-01"}

// resetFake is a fake session that also resets.
type resetFake struct {
	*fakeSession
	mu        sync.Mutex
	preflight error
	reset     func(ctx context.Context) (session.ResetOutcome, error)
	checks    []safety.Gates
	resets    []safety.Gates
	devs      []plan.Identity
}

func (f *resetFake) PreflightReset(_ context.Context, dev plan.Identity, _ *byte, g safety.Gates) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks, f.devs = append(f.checks, g), append(f.devs, dev)
	return f.preflight
}

func (f *resetFake) Reset(ctx context.Context, dev plan.Identity, _ *byte, g safety.Gates) (session.ResetOutcome, error) {
	f.mu.Lock()
	f.resets, f.devs = append(f.resets, g), append(f.devs, dev)
	reset := f.reset
	f.mu.Unlock()
	if reset == nil {
		return session.ResetOutcome{}, errors.New("no reset in this test")
	}
	return reset(ctx)
}

func (f *resetFake) calls() (checks, resets []safety.Gates, devs []plan.Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.checks), slices.Clone(f.resets), slices.Clone(f.devs)
}

func resetHarness(t *testing.T, sn *session.Snapshot, store session.Backups, vs catalog.Verifications, mode Mode) (*harness, *resetFake) {
	t.Helper()
	rf := &resetFake{fakeSession: newFake(sn)}
	h := newHarness(t, sn, func(o *Options) {
		o.Session = rf
		o.Tabs = []Tab{NewBackupTab(rf, store, o.Source)}
		o.Backups = store
		o.OS = keys.Mac
		o.Verified = vs
		o.Mode = mode
		o.Gates.AllowUntested = true
	})
	return h, rf
}

// Until H7 has recorded the reset for the model and firmware, X explains
// the gate and asks the session nothing.
func TestBackupTabResetLocked(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	other := resetRecord
	other.Firmware = "v1.05"
	for _, vs := range []catalog.Verifications{{}, {other}, catalog.VerifiedStages()} {
		h, rf := resetHarness(t, sn, store, vs, ModeEdit)
		h.keys("X")
		if _, ok := h.app.dialog.(*resetLocked); !ok {
			t.Fatalf("X opened %T", h.app.dialog)
		}
		if s := h.screen(80, 24); !strings.Contains(s, "Factory reset is available after hardware test H7") {
			t.Errorf("locked screen:\n%s", s)
		}
		if checks, resets, _ := rf.calls(); len(checks)+len(resets) != 0 {
			t.Error("the locked reset asked the session")
		}
		h.keys("esc")
		if h.app.dialog != nil {
			t.Error("esc left the dialog open")
		}
	}
	h, _ := resetHarness(t, sn, store, catalog.Verifications{}, ModeEdit)
	h.keys("X")
	h.golden("backup-reset-locked")
}

// With the gate open the reset goes through its review: the session's
// preflight, the typed phrase "reset", then one call to Reset for the mouse
// the review showed, and the outcome.
func TestBackupTabResetReview(t *testing.T) {
	sn := backupSnap(t)
	store, dir := seedBackups(t, sn)
	h, rf := resetHarness(t, sn, store, catalog.Verifications{resetRecord}, ModeEdit)
	rf.reset = func(context.Context) (session.ResetOutcome, error) {
		st := backup.Store{Root: dir, Tool: "arcctl test", OS: keys.Mac, Now: func() time.Time { return now }}
		if _, err := st.Save(session.Capture{Device: sn.Identity, Model: sn.Model, Profile: sn.Profile, Image: demoImage(t), Full: true,
			Started: now, Finished: now}, session.LabelBeforeReset); err != nil {
			return session.ResetOutcome{}, err
		}
		return session.ResetOutcome{
			Backup: "/data/backups/em11-pro-260d-1282-7b04/20260926T120000Z-auto-before-reset.json", Run: "20260926T120000.000000000Z-42/1",
			Sent: true, Reply: safety.ReplyAck, Verdict: safety.VerdictChanged,
			Changed: []flash.Extent{{Addr: 4, Len: 2}, {Addr: 12, Len: 4}, {Addr: 96, Len: 16}},
		}, nil
	}
	h.keys("X")
	d, ok := h.app.dialog.(*resetDialog)
	if !ok {
		t.Fatalf("X opened %T", h.app.dialog)
	}
	h.golden("backup-reset-review")
	h.keys("enter")
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewConfirm {
		t.Fatalf("step %d after the preflight\n%s", d.step, h.screen(80, 24))
	}
	h.golden("backup-reset-confirm")
	h.send(tea.PasteMsg{Content: "reset"})
	if s := h.screen(80, 24); !strings.Contains(s, "paste is ignored") {
		t.Errorf("a paste into the phrase:\n%s", s)
	}
	h.typeText("rest")
	h.keys("enter")
	if _, resets, _ := rf.calls(); len(resets) != 0 || d.step != reviewConfirm {
		t.Fatal("a wrong phrase sent the reset")
	}
	h.keys("backspace", "backspace", "backspace", "backspace")
	h.typeText("reset")
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	h.golden("backup-reset-done")
	checks, resets, devs := rf.calls()
	if len(checks) != 1 || checks[0].Confirm != "" || len(resets) != 1 || resets[0].Confirm != safety.ResetPhrase || resets[0].DryRun {
		t.Fatalf("checks %+v, resets %+v", checks, resets)
	}
	for _, dev := range devs {
		if dev != sn.Identity {
			t.Errorf("asked for %v, the review showed %v", dev, sn.Identity)
		}
	}
	h.flush(func() bool { return len(backupTab(h).list) == 4 })
	h.keys("enter")
	if h.app.dialog != nil {
		t.Error("enter left the outcome open")
	}
	if _, resets, _ := rf.calls(); len(resets) != 1 {
		t.Error("the reset was asked for twice")
	}
}

// A refused preflight shows what blocks the reset, without the phrase the
// review asks for itself; a refused reset goes back to the review.
func TestBackupTabResetRefused(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h, rf := resetHarness(t, sn, store, catalog.Verifications{resetRecord}, ModeEdit)
	rf.preflight = &safety.PreflightError{Failures: []error{
		fmt.Errorf("%w: Google Chrome Helper (pid 7); quit it or pass --allow-foreign-client", safety.ErrForeignClient),
		fmt.Errorf("%w: type %q to reset the mouse to its factory settings", safety.ErrConfirm, safety.ResetPhrase),
	}}
	h.keys("X", "enter")
	d := h.app.dialog.(*resetDialog)
	h.flush(func() bool { return d.step != reviewChecking })
	s := h.screen(120, 40)
	if d.step != reviewPlan || len(d.checked) != 1 || !strings.Contains(s, "Google Chrome Helper") || strings.Contains(s, "to reset the mouse to its") {
		t.Fatalf("step %d, checked %v\n%s", d.step, d.checked, s)
	}

	rf.preflight = nil
	rf.reset = func(context.Context) (session.ResetOutcome, error) {
		return session.ResetOutcome{}, &safety.PreflightError{Failures: []error{fmt.Errorf("%w: the mouse is asleep", safety.ErrOffline)}}
	}
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewConfirm })
	h.typeText("reset")
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	if s := h.screen(80, 24); !strings.Contains(s, "Refused: the factory reset was not sent.") || !strings.Contains(s, "the mouse is asleep") {
		t.Fatalf("refused screen:\n%s", s)
	}
	h.keys("enter")
	if d.step != reviewPlan || h.app.dialog != d {
		t.Fatal("enter did not go back to the review")
	}
}

// A dry run confirms with y and shows the packet.
func TestBackupTabResetDryRun(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h, rf := resetHarness(t, sn, store, catalog.Verifications{resetRecord}, ModeDryRun)
	rf.reset = func(context.Context) (session.ResetOutcome, error) {
		return session.ResetOutcome{DryRun: true, Packets: []wire.Packet{wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil)}}, nil
	}
	h.keys("X", "enter")
	d := h.app.dialog.(*resetDialog)
	h.flush(func() bool { return d.step == reviewConfirm })
	h.keys("y")
	h.flush(func() bool { return d.step == reviewDone })
	_, resets, _ := rf.calls()
	if len(resets) != 1 || !resets[0].DryRun || resets[0].Confirm != "" {
		t.Fatalf("resets %+v", resets)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "09 00 00 00 00 00 00 00 00 00 00 00 00 00 00 44") {
		t.Fatalf("dry-run screen:\n%s", s)
	}
}

// TestBackupResetEmulatedLocked runs the shell over a real session on an
// emulated EM11 Pro, keys only at 80x24, and the session holds the hardware
// records compiled from verified.json, as in a release build: X explains the
// gate and sends nothing. When only the shell holds a record, the review
// opens, but the session's own gate refuses the reset, and still no packet
// goes out.
func TestBackupResetEmulatedLocked(t *testing.T) {
	e := accEmulate(t)
	h := e.h
	for i, x := range h.app.tabs {
		if _, ok := x.(*BackupTab); ok {
			h.app.active = i
		}
	}
	n := len(e.dev.Writes())
	h.keys("X")
	if _, ok := h.app.dialog.(*resetLocked); !ok {
		t.Fatalf("X opened %T\n%s", h.app.dialog, e.screen(t))
	}
	e.see(t, "Factory reset is available after hardware test H7")
	h.keys("esc")

	record := resetRecord
	record.Firmware = h.app.sn.Versions.Mouse
	h.app.opt.Verified = catalog.Verifications{record}
	h.keys("X")
	d, ok := h.app.dialog.(*resetDialog)
	if !ok {
		t.Fatalf("X opened %T\n%s", h.app.dialog, e.screen(t))
	}
	h.keys("enter")
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewPlan || len(d.checked) != 1 || !errors.Is(d.checked[0], safety.ErrResetLocked) {
		t.Fatalf("step %d, checked %v\n%s", d.step, d.checked, e.screen(t))
	}
	if got := len(e.dev.Writes()); got != n {
		t.Fatalf("%d packets reached the mouse", got-n)
	}
	for _, w := range e.dev.Writes() {
		if w.Packet.Cmd() == wire.CmdClear {
			t.Fatal("cmd 9 reached the mouse")
		}
	}
}

// Quitting while the factory reset runs asks first, and says the next
// session checks it; ctrl+c again quits at once.
func TestQuitDuringAResetAsks(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h, rf := resetHarness(t, sn, store, catalog.Verifications{resetRecord}, ModeEdit)
	release := make(chan struct{})
	rf.reset = func(context.Context) (session.ResetOutcome, error) {
		<-release
		return session.ResetOutcome{}, errors.New("stopped")
	}
	defer close(release)
	h.keys("X", "enter")
	d := h.app.dialog.(*resetDialog)
	h.flush(func() bool { return d.step == reviewConfirm })
	h.typeText("reset")
	h.keys("enter")
	if d.step != reviewRunning {
		t.Fatalf("step %d", d.step)
	}
	h.keys("ctrl+c")
	c, ok := h.app.dialog.(*Confirm)
	if !ok || h.quit || !strings.Contains(strings.Join(c.Body, " "), "checks what it did the next time it loads the mouse") {
		t.Fatalf("ctrl+c during the reset: dialog %T, quit %v", h.app.dialog, h.quit)
	}
	h.keys("n")
	if h.app.dialog != d || h.quit {
		t.Fatalf("n: dialog %T, quit %v", h.app.dialog, h.quit)
	}
	h.keys("ctrl+c", "ctrl+c")
	if !h.quit {
		t.Error("ctrl+c twice did not quit")
	}
}

// A factory reset another process never checked shows at startup, with the
// backup taken before it.
func TestUncheckedResetNotice(t *testing.T) {
	sn := backupSnap(t)
	run := &safety.Run{ID: "20260926T115900.000000000Z-42/1", Kind: safety.KindReset, Device: sn.Identity,
		Reset: &safety.ResetRecord{Backup: "/data/backups/em11-pro-260d-1282-7b04/20260926T115800Z-auto-before-reset.json", Sending: true}}
	sn.Journal = &session.JournalState{Resets: []*safety.Run{run}}
	h := backupHarness(t, sn, nil)
	n := h.app.notice
	if !n.bad || !strings.Contains(n.text, "ended before arcctl checked what it did") || n.path != run.Reset.Backup {
		t.Fatalf("notice %+v", n)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "auto-before-reset.json") {
		t.Errorf("screen:\n%s", s)
	}
}
