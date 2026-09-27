package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

func demoImage(t *testing.T) *flash.Image {
	t.Helper()
	src, err := backup.Open(filepath.Join("..", "..", "testdata", "demo-em11.json"))
	if err != nil {
		t.Fatal(err)
	}
	return src.Image
}

// backupSnap is the mouse the Backup tab tests run on: the demo image, with
// its shortcut bodies loaded.
func backupSnap(t *testing.T) *session.Snapshot {
	sn := ready(t)
	sn.Image = demoImage(t)
	return sn
}

// seedBackups saves three backups of backupSnap's mouse: one of the demo
// image, labelled; one the write engine saved, where DPI stage 2 was 1600
// and slot 3 ran Back; and a full backup that could not read two ranges.
func seedBackups(t *testing.T, sn *session.Snapshot) (backup.Store, string) {
	t.Helper()
	dir := t.TempDir()
	save := func(at time.Time, label string, im *flash.Image, full bool, missing ...flash.Extent) {
		t.Helper()
		st := backup.Store{Root: dir, Tool: "arcctl test", OS: keys.Mac, Now: func() time.Time { return at }}
		_, err := st.Save(session.Capture{
			Device: sn.Identity, Model: sn.Model, Profile: sn.Profile, Versions: session.Versions{Mouse: "v1.00", Receiver: "v1.0"},
			Image: im, Full: full, Missing: missing, Started: at, Finished: at,
		}, label)
		if err != nil {
			t.Fatal(err)
		}
	}
	save(now.Add(-26*time.Hour), "before H1", demoImage(t), false)
	older := demoImage(t)
	dpi, _ := mouse.DPIExtent(1)
	r, err := mouse.EncodeDPI(em11().Sensor, 1600)
	if err != nil {
		t.Fatal(err)
	}
	noErr(t, older.Set(dpi.Addr, r))
	back, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack})
	if err != nil {
		t.Fatal(err)
	}
	key3, _ := mouse.KeyFnExtent(3)
	noErr(t, older.Set(key3.Addr, back))
	save(now.Add(-2*time.Hour), session.LabelBeforeWrite, older, false)
	full := demoImage(t)
	noErr(t, full.Set(mouse.AddrSensor3955DPI, make([]byte, 75)))
	save(now.Add(-time.Hour), "", full, true, flash.Extent{Addr: 9504, Len: 20}, flash.Extent{Addr: 9600, Len: 10})
	return backup.Store{Root: dir, Tool: "arcctl test", OS: keys.Mac, Now: func() time.Time { return now }}, dir
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// deviceStore saves the captures of the fake session, which carry no
// device, as the snapshot's mouse.
type deviceStore struct {
	backup.Store
	sn *session.Snapshot
}

func (s deviceStore) Save(c session.Capture, label string) (string, error) {
	c.Device, c.Model, c.Profile = s.sn.Identity, s.sn.Model, s.sn.Profile
	return s.Store.Save(c, label)
}

func backupHarness(t *testing.T, sn *session.Snapshot, store session.Backups) *harness {
	t.Helper()
	return newHarness(t, sn, func(o *Options) {
		o.Tabs = []Tab{NewBackupTab(o.Session, store, o.Source)}
		o.Backups = store
		o.Gates.AllowUntested = true
		o.OS = keys.Mac
	})
}

func backupTab(h *harness) *BackupTab { return h.app.tabs[0].(*BackupTab) }

func TestBackupTabList(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h := backupHarness(t, sn, store)
	if n := len(backupTab(h).list); n != 3 {
		t.Fatalf("%d backups listed", n)
	}
	h.golden("backup-list")
	h.keys("down")
	h.golden("backup-list-auto")
	h.screen(79, 24)
}

func TestBackupTabEmpty(t *testing.T) {
	h := backupHarness(t, backupSnap(t), backup.Store{Root: t.TempDir()})
	h.golden("backup-empty")
	h = backupHarness(t, backupSnap(t), nil)
	if s := h.screen(80, 24); !strings.Contains(s, "Backups are not available in this session") {
		t.Errorf("without a store:\n%s", s)
	}
}

func TestBackupTabShow(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h := backupHarness(t, sn, store)
	h.keys("down", "down", "enter")
	if _, ok := h.app.dialog.(*backupShow); !ok {
		t.Fatalf("enter opened %T", h.app.dialog)
	}
	h.golden("backup-show")
	h.keys("esc")
	if h.app.dialog != nil {
		t.Error("esc left the dialog open")
	}
}

func TestBackupTabDiff(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h := backupHarness(t, sn, store)
	h.keys("down", "d")
	h.flush(func() bool { _, ok := h.app.dialog.(*restoreDiff); return ok })
	h.golden("backup-diff")
}

// The restore goes through the review: the same checks, the typed phrase,
// then the session's Apply with the plan the review showed. Staged edits
// survive it.
func TestBackupTabRestore(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h := backupHarness(t, sn, store)
	h.app.Pending().Stage(Staged{Key: "dpi 1", Edit: mouse.SetDPI{Stage: 0, DPI: 900}, Desc: "DPI stage 1: 900"})
	var applied plan.Plan
	h.fake.write = func(ctx context.Context, on func(safety.OpEvent)) (session.Outcome, error) {
		for _, op := range applied.Ops {
			on(safety.OpEvent{Kind: safety.EventVerified, Op: op, Run: "run-1"})
		}
		n := len(applied.Ops)
		return session.Outcome{Result: safety.Result{Run: "run-1", Ops: n, Verified: n}}, nil
	}
	h.keys("down", "w")
	h.flush(func() bool { return restoring(h.app.dialog) })
	d := h.app.dialog.(*reviewDialog)
	applied = d.plan
	h.golden("backup-restore-review")
	if got := len(d.plan.Ops); got != 2 {
		t.Fatalf("the restore plans %d ops, want the DPI stage and the binding", got)
	}
	h.keys("enter")
	if d.step != reviewConfirm {
		t.Fatalf("step %d after enter\n%s", d.step, h.screen(80, 24))
	}
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	calls := h.fake.Calls()
	if !contains(calls, "preflight") || !contains(calls, "apply 2 ops") {
		t.Errorf("calls %v", calls)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "Restored: 2 of 2 records verified.") {
		t.Errorf("done screen:\n%s", s)
	}
	if h.app.Pending().Len() != 1 {
		t.Error("the restore dropped the staged edits")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestBackupTabRestoreRefusesAnotherProfile(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	p := byte(2)
	sn.Profile = session.Probe{Asked: true, Supported: true, Value: p}
	h := backupHarness(t, sn, store)
	h.keys("w")
	if h.app.dialog != nil {
		t.Fatalf("opened %T", h.app.dialog)
	}
	if !strings.Contains(h.app.notice.text, "onboard profile") {
		t.Errorf("notice %q", h.app.notice.text)
	}
}

func TestBackupTabFullBackup(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h := backupHarness(t, sn, deviceStore{store, sn})
	h.keys("f")
	h.flush(func() bool { return len(backupTab(h).list) == 4 })
	if !contains(h.fake.Calls(), "backup full=true") {
		t.Errorf("calls %v", h.fake.Calls())
	}
	if !strings.Contains(h.app.notice.text, "Backup saved") {
		t.Errorf("notice %q", h.app.notice.text)
	}
}

// TestBackupRestoreEmulated takes a full backup from the Backup tab of an
// emulated mouse, changes the mouse behind arcctl's back, and restores the
// backup through the review: the run ends verified in the journal and the
// mouse holds the backup again.
func TestBackupRestoreEmulated(t *testing.T) {
	e := accEmulate(t)
	h := e.h
	store := backup.Store{Root: filepath.Join(t.TempDir(), "backups"), Tool: "arcctl test", Source: backup.SourceEmulator}
	var tab *BackupTab
	for i, x := range h.app.tabs {
		if b, ok := x.(*BackupTab); ok {
			tab, h.app.active = b, i
		}
	}
	if tab == nil {
		t.Fatal("the default tabs have no Backup tab")
	}
	tab.store, tab.lister = store, store
	h.keys("down")
	e.see(t, "No backup of this mouse yet")

	h.keys("f")
	h.flush(func() bool { return len(tab.list) == 1 })
	src, err := backup.Load(tab.list[0].Path)
	noErr(t, err)
	if !src.Full {
		t.Fatal("f did not take a full backup")
	}

	dpi, _ := mouse.DPIExtent(1)
	r, err := mouse.EncodeDPI(em11().Sensor, 2000)
	noErr(t, err)
	noErr(t, e.dev.Store(dpi.Addr, r))
	h.keys("r")
	h.flush(func() bool {
		b, _ := h.app.sn.Image.Get(dpi)
		return string(b) == string(r) && h.app.sn.Progress.Job == ""
	})

	h.keys("w")
	h.flush(func() bool { return restoring(h.app.dialog) })
	e.see(t, "Review the restore", "1200", "restore DPI stage 2: 1200")
	h.keys("enter")
	h.flush(func() bool { return h.app.dialog.(*reviewDialog).step == reviewConfirm })
	h.typeText("write untested")
	h.keys("enter")
	d := h.app.dialog.(*reviewDialog)
	h.flush(func() bool { return d.step == reviewDone })
	if d.run.end() != reviewEndVerified {
		t.Fatalf("the restore ended %v\n%s", d.run.done.Err, e.screen(t))
	}
	st := e.journal(t)
	last := st.Runs[len(st.Runs)-1]
	if !st.Clean() || !last.Complete || last.Kind != safety.KindApply {
		t.Fatalf("journal: last run %+v, open %d", last, len(st.Open))
	}
	want, _ := src.Image().Get(dpi)
	if got, _ := e.dev.Image().Get(dpi); string(got) != string(want) {
		t.Errorf("DPI stage 2 holds % x, the backup % x", got, want)
	}
}

// The summary names each button with what it does, in the Buttons tab's
// words and order.
func TestBackupSummaryNamesButtons(t *testing.T) {
	sn := backupSnap(t)
	store, _ := seedBackups(t, sn)
	h := backupHarness(t, sn, store)
	s := strings.Join(strings.Fields(h.screen(120, 40)), " ")
	if !strings.Contains(s, "Buttons Left Click: Left Click; Right Click: Right Click; Wheel Click: Cmd+V (Paste); Forward: Cmd+Tab; Backward: Ctrl+Tab;") {
		t.Errorf("summary:\n%s", h.screen(120, 40))
	}
}

// q closes a dialog of the Backup tab rather than asking to quit, and the
// restore review shows its plan before the records it leaves alone.
func TestBackupDialogsCloseOnQ(t *testing.T) {
	sn := backupSnap(t)
	store, dir := seedBackups(t, sn)
	h := backupHarness(t, sn, store)
	h.keys("enter")
	if _, ok := h.app.dialog.(*backupShow); !ok {
		t.Fatalf("enter opened %T", h.app.dialog)
	}
	h.keys("q")
	if h.app.dialog != nil || h.quit {
		t.Fatalf("q: dialog %T, quit %v", h.app.dialog, h.quit)
	}

	im := demoImage(t)
	col, _ := mouse.ColorExtent(0)
	noErr(t, im.Set(col.Addr, mouse.EncodeColor([3]byte{1, 2, 3})))
	dpi, _ := mouse.DPIExtent(1)
	r, err := mouse.EncodeDPI(em11().Sensor, 1600)
	noErr(t, err)
	noErr(t, im.Set(dpi.Addr, r))
	st := backup.Store{Root: dir, Tool: "arcctl test", OS: keys.Mac, Now: func() time.Time { return now.Add(time.Minute) }}
	_, err = st.Save(session.Capture{Device: sn.Identity, Model: sn.Model, Profile: sn.Profile, Image: im, Started: now, Finished: now}, "colour")
	noErr(t, err)
	h = backupHarness(t, sn, store)
	h.keys("w")
	h.flush(func() bool { return restoring(h.app.dialog) })
	d := h.app.dialog.(*reviewDialog)
	lines, _ := d.planLines(h.app.context(), 76)
	plan, left := -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(ansi.Strip(l)) {
		case "Plan":
			plan = i
		case "Left alone":
			left = i
		}
	}
	if plan < 0 || left < plan {
		t.Errorf("the plan at line %d, the records left alone at %d:\n%s", plan, left, strings.Join(lines, "\n"))
	}
	h.keys("q")
	if h.app.dialog != nil || h.quit {
		t.Errorf("q in the restore review: dialog %T, quit %v", h.app.dialog, h.quit)
	}
}

// With u in the diff, the restore also writes back the bytes the backup
// captured where arcctl knows no valid value: the review names them, needs
// --experimental and a phrase that names every extent, and the run ends
// verified with the mouse holding the backup's bytes.
func TestBackupRestoreUnknownEmulated(t *testing.T) {
	e := accEmulate(t)
	h := e.h
	store := backup.Store{Root: filepath.Join(t.TempDir(), "backups"), Tool: "arcctl test", Source: backup.SourceEmulator}
	var tab *BackupTab
	for i, x := range h.app.tabs {
		if b, ok := x.(*BackupTab); ok {
			tab, h.app.active = b, i
		}
	}
	if tab == nil {
		t.Fatal("the default tabs have no Backup tab")
	}
	tab.store, tab.lister = store, store
	h.keys("f")
	h.flush(func() bool { return len(tab.list) == 1 })
	src, err := backup.Load(tab.list[0].Path)
	noErr(t, err)

	unknown := []flash.Extent{{Addr: 6, Len: 2}, {Addr: 84, Len: 12}, {Addr: 187, Len: 2}}
	for _, x := range unknown {
		b, _ := src.Image().Get(x)
		noErr(t, e.dev.Store(x.Addr, []byte{b[0] ^ 1, b[1] ^ 1}))
	}
	h.keys("r")
	h.flush(func() bool {
		b, _ := h.app.sn.Image.Get(unknown[0])
		w, _ := src.Image().Get(unknown[0])
		return b != nil && b[0] != w[0] && h.app.sn.Progress.Job == ""
	})

	h.keys("d")
	h.flush(func() bool { _, ok := h.app.dialog.(*restoreDiff); return ok })
	diff := h.app.dialog.(*restoreDiff)
	if len(diff.rs.Plan.Ops) != 0 {
		t.Fatalf("without u the restore writes %v", diff.rs.Plan.Ops)
	}
	e.see(t, "u include unknown")
	h.keys("u")
	if !diff.unknown || len(diff.rs.Captured()) != len(unknown) {
		t.Fatalf("after u: unknown %v, records %+v", diff.unknown, diff.rs.Records)
	}
	h.keys("w")
	h.flush(func() bool { return restoring(h.app.dialog) })
	d := h.app.dialog.(*reviewDialog)
	if want := "write experimental and captured 6+2 84+12 187+2"; d.phrase != want {
		t.Fatalf("phrase %q, want %q", d.phrase, want)
	}
	e.see(t, "Review the restore", "With unknown records included", "captured")

	h.keys("enter")
	if d.step != reviewPlan || !strings.Contains(h.app.notice.text, "--experimental") {
		t.Fatalf("without --experimental: step %d, notice %q", d.step, h.app.notice.text)
	}
	h.app.opt.Gates.Experimental = true
	h.keys("enter")
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewConfirm {
		t.Fatalf("the preflight did not pass: %v\n%s", d.checked, e.screen(t))
	}
	h.typeText("write experimental")
	h.keys("enter")
	if d.step != reviewConfirm {
		t.Fatalf("a phrase without the extents went on to step %d", d.step)
	}
	h.keys("ctrl+u")
	h.typeText(d.phrase)
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	if d.run.end() != reviewEndVerified {
		t.Fatalf("the restore ended %v\n%s", d.run.done.Err, e.screen(t))
	}
	st := e.journal(t)
	last := st.Runs[len(st.Runs)-1]
	var captured int
	for _, op := range last.Ops {
		if op.Phase == plan.Captured && op.State == safety.StateVerified {
			captured++
		}
	}
	if !st.Clean() || !last.Complete || captured != len(unknown) {
		t.Fatalf("journal: last run %+v, %d captured ops verified", last, captured)
	}
	for _, x := range unknown {
		want, _ := src.Image().Get(x)
		if got, _ := e.dev.Image().Get(x); string(got) != string(want) {
			t.Errorf("%v holds % x, the backup % x", x, got, want)
		}
	}

	h.keys("enter")
	e.settle(last)
	h.keys("U")
	rv, ok := h.app.dialog.(*reviewDialog)
	if !ok || rv.kind != safety.KindRevert {
		t.Fatalf("U opened %T\n%s", h.app.dialog, e.screen(t))
	}
	if rv.phrase != d.phrase {
		t.Fatalf("the revert asks for %q, the restore asked for %q", rv.phrase, d.phrase)
	}
	h.keys("enter")
	h.flush(func() bool { return rv.step != reviewChecking })
	h.typeText(rv.phrase)
	h.keys("enter")
	h.flush(func() bool { return rv.step == reviewDone })
	if rv.run.end() != reviewEndVerified {
		t.Fatalf("the revert ended %v\n%s", rv.run.done.Err, e.screen(t))
	}
	for _, x := range unknown {
		want, _ := src.Image().Get(x)
		if got, _ := e.dev.Image().Get(x); got[0] != want[0]^1 || got[1] != want[1]^1 {
			t.Errorf("%v holds % x after the revert, % x before the restore", x, got, []byte{want[0] ^ 1, want[1] ^ 1})
		}
	}
}
