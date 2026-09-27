package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

const rvPlayPause = 0xCD

// rvOpts builds the shell with the real review over the fake session.
func rvOpts(extra ...func(*Options)) []func(*Options) {
	return append([]func(*Options){func(o *Options) { o.Review = NewReview(o.Session) }}, extra...)
}

func rvUntested(o *Options) { o.Gates.AllowUntested = true }

// rvReady is a loaded EM11 Pro whose image also holds the shortcut bodies its
// bindings run, as a load reads them.
func rvReady(t *testing.T) *session.Snapshot {
	t.Helper()
	sn := ready(t)
	im, err := emu.WithBodies(sn.Image)
	if err != nil {
		t.Fatal(err)
	}
	sn.Image = im
	return sn
}

// rvStageMedia stages slot 3 to Play/Pause and DPI stage 2 to 1600: four
// ops, one of them a two-phase rebind of a shortcut slot.
func rvStageMedia(h *harness) {
	h.app.Pending().Stage(Staged{Key: SlotKey(3), Desc: "Back (side): Play/Pause", Edit: mouse.SetMedia{Slot: 3, Usage: rvPlayPause}})
	h.app.Pending().Stage(Staged{Key: DPIKey(1), Desc: "DPI stage 2: 1600", Edit: mouse.SetDPI{Stage: 1, DPI: 1600}})
}

func rvDialog(t *testing.T, h *harness) *reviewDialog {
	t.Helper()
	d, ok := h.app.dialog.(*reviewDialog)
	if !ok {
		t.Fatalf("the open dialog is %T, not the review", h.app.dialog)
	}
	return d
}

func rvScreen(h *harness) string { return h.screen(120, 40) }

// rvWant checks that the 120x40 screen says each of wants, whatever the
// line breaks.
func rvWant(t *testing.T, h *harness, wants ...string) {
	t.Helper()
	s := rvScreen(h)
	flat := strings.Join(strings.Fields(s), " ")
	for _, w := range wants {
		if !strings.Contains(flat, strings.Join(strings.Fields(w), " ")) {
			t.Errorf("the screen lacks %q:\n%s", w, s)
		}
	}
}

// rvConfirm opens the review and goes on to the confirmation.
func rvConfirm(t *testing.T, h *harness) *reviewDialog {
	t.Helper()
	h.keys("a", "enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewConfirm {
		t.Fatalf("step %d after enter, want the confirmation", d.step)
	}
	return d
}

func TestReviewPlan(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	rvStageMedia(h)
	h.keys("a")
	d := rvDialog(t, h)
	if d.planErr != nil || len(d.plan.Ops) != 4 {
		t.Fatalf("plan %+v, %v", d.plan.Ops, d.planErr)
	}
	phases := []plan.Phase{plan.Neutralise, plan.Body, plan.Bind, plan.Record}
	for i, r := range d.rows {
		if r.op.Phase != phases[i] {
			t.Errorf("op %d is %v, want %v", i+1, r.op.Phase, phases[i])
		}
	}
	rows := d.rows
	if rows[0].old != "Ctrl+Tab" || rows[1].old != "Ctrl+Tab" || rows[1].new != "Play/Pause" {
		t.Errorf("the neutralise and body rows decode %q -> %q and %q -> %q", rows[0].old, rows[0].new, rows[1].old, rows[1].new)
	}
	if rows[2].old != rows[0].new || rows[2].new != "Play/Pause" {
		t.Errorf("the bind row decodes %q -> %q after %q", rows[2].old, rows[2].new, rows[0].new)
	}
	if rows[3].old != "1200" || rows[3].new != "1600" {
		t.Errorf("the DPI row decodes %q -> %q", rows[3].old, rows[3].new)
	}
	rvWant(t, h, "4 records, 4 packets", "Ctrl+Tab → Play/Pause", "write untested", "full backup")
	h.golden("review-plan")
	if calls := h.fake.Calls(); len(calls) > 0 {
		t.Errorf("opening the review called %v", calls)
	}
}

func TestReviewScrolls(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	rvStageMedia(h)
	h.keys("a")
	h.screen(80, 24)
	h.keys("pgdown")
	d := rvDialog(t, h)
	if d.scroll == 0 {
		t.Fatal("pgdown did not scroll at 80x24")
	}
	h.golden("review-scrolled")
	h.keys("down")
	h.screen(80, 24)
	if d.cursor != 1 || d.scroll != 0 {
		t.Errorf("cursor %d, scroll %d: moving the cursor must bring it into view", d.cursor, d.scroll)
	}
}

func TestReviewNeedsAllowUntested(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts()...)
	rvStageMedia(h)
	h.keys("a")
	rvWant(t, h, "Blocked. The plan writes untested features (4 records): restart arcctl with --allow-untested.")
	h.golden("review-blocked-untested")
	h.keys("enter")
	d := rvDialog(t, h)
	if d.step != reviewPlan || !h.app.notice.bad || len(h.fake.Calls()) > 0 {
		t.Errorf("step %d, notice %+v, calls %v", d.step, h.app.notice, h.fake.Calls())
	}
}

// rvHiddenSlot records a hardware test for hidden slots on this firmware, so
// the planner writes them at the Experimental tier (D5).
func rvHiddenSlot(o *Options) {
	o.Verified = catalog.Verifications{{Model: "7B04", Feature: string(mouse.FeatureHiddenSlot), Firmware: "v1.00", Stage: "H3b"}}
}

func TestReviewExperimentalAndWebCompat(t *testing.T) {
	combo := keys.Combo{keys.RCtrl.Stroke(), {Kind: keys.KindKey, Value: 0x06}}
	stageBoth := func(h *harness) {
		h.app.Pending().Stage(Staged{Key: SlotKey(7), Desc: "Slot 7: Wheel Click",
			Edit: mouse.SetKey{Slot: 7, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamMiddle}}})
		h.app.Pending().Stage(Staged{Key: SlotKey(4), Desc: "Forward (side): Right Ctrl+C", Edit: mouse.SetShortcut{Slot: 4, Combo: combo}})
	}
	t.Run("without --experimental", func(t *testing.T) {
		h := newHarness(t, rvReady(t), rvOpts(rvUntested, rvHiddenSlot)...)
		stageBoth(h)
		h.keys("a")
		rvWant(t, h, "restart arcctl with --experimental", "slot 7 is not shown by the web app", "right-side modifier")
		h.golden("review-experimental-blocked")
	})
	t.Run("with --experimental", func(t *testing.T) {
		h := newHarness(t, rvReady(t), rvOpts(rvUntested, rvHiddenSlot, func(o *Options) { o.Gates.Experimental = true })...)
		stageBoth(h)
		d := rvConfirm(t, h)
		if d.phrase != "write experimental" {
			t.Fatalf("phrase %q", d.phrase)
		}
		h.typeText("write untested")
		h.keys("enter")
		rvWant(t, h, `Type "write experimental"`, "does not match")
		h.golden("review-confirm-experimental")
		if d.step != reviewConfirm || slices.Contains(h.fake.Calls(), "apply 5 ops") {
			t.Fatalf("the wrong phrase went on: step %d, calls %v", d.step, h.fake.Calls())
		}
	})
}

// D5: an Experimental feature without a recorded hardware test, and
// KeyOperation @8, are refused by the planner whatever the flags.
func TestReviewRefusesWhatD5Keeps(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested, func(o *Options) { o.Gates.Experimental = true })...)
	h.app.Pending().Stage(Staged{Key: SlotKey(7), Desc: "Slot 7: Wheel Click",
		Edit: mouse.SetKey{Slot: 7, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamMiddle}}})
	h.app.Pending().Stage(Staged{Key: SettingKey(mouse.AddrKeyOperation), Desc: "KeyOperation: 1",
		Edit: mouse.SetSetting{Addr: mouse.AddrKeyOperation, Value: 1}})
	h.keys("a")
	d := rvDialog(t, h)
	if !errors.Is(d.planErr, mouse.ErrRefused) || len(d.refused) != 2 {
		t.Fatalf("plan error %v, refused %v", d.planErr, d.refused)
	}
	if why := d.refused[SlotKey(7)]; !strings.Contains(why, "no recorded hardware test") {
		t.Errorf("the hidden slot is refused with %q", why)
	}
	if why := d.refused[SettingKey(mouse.AddrKeyOperation)]; !strings.Contains(why, "read-only") {
		t.Errorf("KeyOperation is refused with %q", why)
	}
	h.keys("enter")
	if d.step != reviewPlan || len(h.fake.Calls()) > 0 {
		t.Errorf("step %d, calls %v", d.step, h.fake.Calls())
	}
}

func TestReviewRefusedEdit(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	h.app.Pending().Stage(Staged{Key: SlotKey(0), Desc: "Left button: Disable", Edit: mouse.SetKey{Slot: 0, Fn: mouse.KeyFn{Type: mouse.TypeDisable}}})
	stage(h, 1, 1600)
	h.keys("a")
	d := rvDialog(t, h)
	if !errors.Is(d.planErr, plan.ErrLeftClick) || d.refused[SlotKey(0)] == "" || d.refused[DPIKey(1)] != "" {
		t.Fatalf("plan error %v, refused %v", d.planErr, d.refused)
	}
	rvWant(t, h, "Cannot plan these edits", "refused: ")
	h.golden("review-refused-edit")
	h.keys("d")
	if d.planErr != nil || len(d.plan.Ops) != 1 || h.app.Pending().Len() != 1 {
		t.Errorf("after d: plan %v, %d ops, %d pending", d.planErr, len(d.plan.Ops), h.app.Pending().Len())
	}
}

func TestReviewBlockers(t *testing.T) {
	sn := withState(rvReady(t), session.Offline)
	sn.Online = false
	sn.Clients = []session.Client{chrome}
	h := newHarness(t, sn, rvOpts(rvUntested)...)
	rvStageMedia(h)
	h.keys("a")
	rvWant(t, h, "Blocked. The mouse is asleep", "Google Chrome Helper (pid 4242) had the receiver open")
	h.golden("review-blockers")
}

func TestReviewPreflightFails(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	rvStageMedia(h)
	h.fake.err = &safety.PreflightError{Failures: []error{
		fmt.Errorf("%w: Ghostty (pid 812) holds it; close its password field or quit it, then retry", safety.ErrSecureInput),
		fmt.Errorf("%w: type %q to write features no hardware test has verified", safety.ErrConfirm, "write untested"),
		fmt.Errorf("%w: 12+4 holds 3f 3f 01 d6, the plan expects 3f 3f 02 d5; reload and plan again", safety.ErrStale),
	}}
	h.keys("a", "enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewPlan || len(d.checked) != 2 {
		t.Fatalf("step %d, checked %v", d.step, d.checked)
	}
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"preflight"}) {
		t.Errorf("calls %v", calls)
	}
	rvWant(t, h, "Blocked: 2 checks failed just now", "Secure Input is on", "reload and plan")
	h.golden("review-preflight-failed")
}

func TestReviewConfirmAndApply(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	rvStageMedia(h)
	d := rvConfirm(t, h)
	h.typeText("write unt")
	h.keys("enter")
	h.golden("review-confirm-typed")
	p := d.plan
	release := holdWrite(h, p)
	gate := make(chan struct{})
	write := h.fake.write
	h.fake.write = func(ctx context.Context, on func(safety.OpEvent)) (session.Outcome, error) {
		<-gate
		return write(ctx, on)
	}
	h.typeText("ested")
	h.keys("enter")
	if d.step != reviewRunning {
		t.Fatalf("step %d after the phrase", d.step)
	}
	h.flush(func() bool { return h.app.write != nil })
	backingUp := rvReady(t)
	backingUp.Progress = session.Progress{Job: "backup", Done: 120, Total: 725}
	backingUp.Journal = &session.JournalState{}
	h.snap(backingUp)
	rvWant(t, h, "Backing up the whole configuration before the first write", "120 of 725")
	h.golden("review-running-backup")
	close(gate)
	h.flush(func() bool { return d.run.w.cur == 1 })
	h.snap(withState(rvReady(t), session.Applying))
	rvWant(t, h, "1 of 4 records verified", "verified", "sending")
	h.golden("review-running")
	release()
	h.flush(func() bool { return d.step == reviewDone })
	if g := h.fake.gates; len(g) != 1 || g[0].Confirm != "write untested" || !g[0].AllowUntested || g[0].DryRun {
		t.Errorf("gates %+v", g)
	}
	if n := h.app.Pending().Len(); n != 0 {
		t.Errorf("%d edits pending after a verified apply", n)
	}
	h.snap(rvReady(t))
	rvWant(t, h, "Applied: 4 of 4 records verified", "auto-before-write")
	h.golden("review-done")
	h.keys("enter")
	if h.app.dialog != nil {
		t.Error("enter did not close the result")
	}
}

func TestReviewOverlap(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{Result: safety.Result{Run: "20260926T120000Z-4242", Ops: 1, Verified: 1, Overlap: true}}, nil
	}
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewDone })
	rvWant(t, h, "A push re-read the last record")
}

func TestReviewDryRun(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(mode(ModeDryRun))...)
	rvStageMedia(h)
	d := rvConfirm(t, h)
	if d.needsPhrase(h.app.context()) {
		t.Fatal("a dry run asks for the phrase")
	}
	h.golden("review-confirm-dry-run")
	var packets []wire.Packet
	for _, op := range d.plan.Ops {
		for i := 0; i < op.Extent.Len; i += wire.MaxData {
			b := op.New[i:min(len(op.New), i+wire.MaxData)]
			packets = append(packets, wire.MustBuild(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr+i), b))
		}
	}
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{Result: safety.Result{Run: "20260926T120000Z-4242", Ops: 4, Verified: 4}, DryRun: true, Packets: packets}, nil
	}
	h.keys("y")
	h.flush(func() bool { return d.step == reviewDone })
	if g := h.fake.gates; len(g) != 1 || !g[0].DryRun || g[0].Confirm != "" {
		t.Errorf("gates %+v", g)
	}
	rvWant(t, h, "Dry run done: 4 of 4 records verified against the overlay", "4 packets sent to the overlay", packets[2].String())
	h.golden("review-done-dry-run")
}

func TestReviewPartialBackup(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	partial := &safety.PartialError{Path: "backups/em11-pro-260d-1282-7b04/20260926T120000Z-auto-first-write.json",
		Missing: []flash.Extent{{Addr: 9504, Len: 10}, {Addr: 9514, Len: 10}}}
	calls := 0
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		calls++
		if calls == 1 {
			return session.Outcome{Backups: []string{partial.Path}}, &safety.PreflightError{Failures: []error{partial}}
		}
		return session.Outcome{Result: safety.Result{Run: "20260926T120000Z-4242", Ops: 1, Verified: 1}}, nil
	}
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewPartial })
	rvWant(t, h, "The full backup is partial", "9504+10, 9514+10")
	h.golden("review-partial")
	h.keys("y")
	h.flush(func() bool { return d.step == reviewDone })
	g := h.fake.gates
	if len(g) != 2 || g[0].AcceptPartial || !g[1].AcceptPartial || g[1].Confirm != "write untested" {
		t.Errorf("gates %+v", g)
	}
	rvWant(t, h, "Applied: 1 of 1 records verified")
}

func TestReviewPartialDeclined(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{}, &safety.PreflightError{Failures: []error{&safety.PartialError{Path: "b.json", Missing: []flash.Extent{{Addr: 9504, Len: 10}}}}}
	}
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewPartial })
	h.keys("n")
	if d.step != reviewPlan || !strings.Contains(d.note, "b.json") || len(h.fake.gates) != 1 {
		t.Errorf("step %d, note %q, %d writes", d.step, d.note, len(h.fake.gates))
	}
}

// rvStopped is the snapshot after an apply of p that stopped at its second op:
// the journal holds the run open, its second record torn.
func rvStopped(t *testing.T, p plan.Plan, id string) *session.Snapshot {
	sn := withState(rvReady(t), session.Recovering)
	run := &safety.Run{ID: id, Kind: safety.KindApply, Device: sn.Identity, Started: now.Add(-time.Minute),
		Ended: true, Err: "stopped at op 2"}
	in := &safety.Inspection{Run: run, Image: sn.Image.Clone()}
	for i, op := range p.Ops {
		state, class, found := safety.StateVerified, safety.ClassNew, op.New
		if i == 1 {
			state, class, found = safety.StateFailed, safety.ClassTorn, []byte{0x12, 0x34, 0x56, 0x78}
		} else if i > 1 {
			state, class, found = safety.StatePlanned, safety.ClassOld, op.Old
		}
		run.Ops = append(run.Ops, safety.OpRecord{Op: op, State: state})
		if err := in.Image.Set(op.Extent.Addr, found); err != nil {
			t.Fatal(err)
		}
		in.Extents = append(in.Extents, safety.ExtentState{Extent: op.Extent, Before: op.Old, After: op.New, Found: found,
			Class: class, Tier: op.Tier, Desc: op.Desc})
	}
	sn.Journal = &session.JournalState{Open: []session.OpenRun{{Run: run, Inspection: in}}}
	return sn
}

func TestReviewStoppedThenRecovery(t *testing.T) {
	const id = "20260926T120000Z-4242"
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	stage(h, 2, 2000)
	p := stagedPlan(t, h)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{Result: safety.Result{Run: id, Ops: 2, Verified: 1}},
			&safety.StopError{Op: p.Ops[1], Written: true, Chunks: 0, Class: safety.ClassTorn, Err: wire.ErrNAK}
	}
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewDone })
	busy := rvReady(t)
	busy.Progress = session.Progress{Job: "journal"}
	h.snap(busy)
	rvWant(t, h, "Stopped: 1 of 2 records verified", "rejected by the device", "torn", "checking the journal")
	if n := h.app.Pending().Len(); n != 2 {
		t.Errorf("%d edits pending after a stopped apply, want 2", n)
	}
	h.snap(rvStopped(t, p, id))
	if d.run.rec == nil {
		t.Fatal("no recovery prompt after the journal listed the run")
	}
	if _, ok := h.app.dialog.(*reviewDialog); !ok {
		t.Fatalf("the shell replaced the review with %T", h.app.dialog)
	}
	rvWant(t, h, "Unfinished write", "f  finish", "l  leave: refused while a record is torn")
	h.golden("review-stopped")
	h.keys("l")
	if !h.app.notice.bad || rvDialog(t, h).step != reviewDone {
		t.Errorf("leave on a torn record: notice %+v", h.app.notice)
	}
	h.keys("b")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("b opened %T", h.app.dialog)
	}
	h.keys("n")
	if h.app.dialog != d {
		t.Fatalf("n left %T open, not the review", h.app.dialog)
	}
	h.keys("b")
	h.fake.write = nil
	h.keys("y")
	h.flush(func() bool { return slices.Contains(h.fake.Calls(), "recover "+id+" back") && h.app.write == nil })
	if h.app.dialog != d {
		t.Errorf("after the recovery the open dialog is %T, not the review", h.app.dialog)
	}
}

func TestReviewStoppedSettled(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	p := stagedPlan(t, h)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{Result: safety.Result{Run: "20260926T120000Z-4242", Ops: 1}},
			&safety.StopError{Op: p.Ops[0], Written: true, Class: safety.ClassOld, Err: wire.ErrNAK}
	}
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewDone })
	h.snap(rvReady(t))
	rvWant(t, h, "It still holds its", "The journal holds nothing unfinished")
}

func TestReviewRefused(t *testing.T) {
	sn := rvReady(t)
	h := newHarness(t, sn, rvOpts(rvUntested)...)
	rvStageMedia(h)
	d := rvConfirm(t, h)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{}, &safety.PreflightError{Failures: []error{
			fmt.Errorf("%w: Google Chrome Helper (pid 4242); quit it or pass --allow-foreign-client", safety.ErrForeignClient),
			fmt.Errorf("%w: unlock the Mac, then retry", safety.ErrScreenLocked),
		}}
	}
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	rvWant(t, h, "Refused: nothing was sent to the mouse.", "Another program has the receiver open", "The screen is locked")
	h.golden("review-refused")
	h.keys("enter")
	if d.step != reviewPlan || len(d.checked) != 2 || h.app.Pending().Len() != 2 {
		t.Fatalf("after enter: step %d, checked %v, %d pending", d.step, d.checked, h.app.Pending().Len())
	}
	rvWant(t, h, "Blocked: 2 checks failed just now")
}

func TestReviewNothingToWrite(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1200)
	h.keys("a")
	rvWant(t, h, "Nothing to write")
	h.keys("enter")
	if h.app.dialog != nil || h.app.Pending().Len() != 0 || len(h.fake.Calls()) > 0 {
		t.Errorf("dialog %T, %d pending, calls %v", h.app.dialog, h.app.Pending().Len(), h.fake.Calls())
	}
}

func TestReviewDiscard(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	rvStageMedia(h)
	h.keys("a", "u")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("u opened %T", h.app.dialog)
	}
	h.keys("y")
	if h.app.dialog != nil || h.app.Pending().Len() != 0 {
		t.Errorf("dialog %T, %d pending", h.app.dialog, h.app.Pending().Len())
	}
}

func TestReviewPlanChangesWhileConfirming(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	d := rvConfirm(t, h)
	h.typeText("write")
	h.snap(rvReady(t))
	if d.step != reviewConfirm {
		t.Fatal("an unchanged plan left the confirmation")
	}
	stage(h, 2, 2000)
	h.snap(rvReady(t))
	if d.step != reviewPlan || !strings.Contains(d.note, "The plan changed") || len(d.plan.Ops) != 2 {
		t.Errorf("step %d, note %q, %d ops", d.step, d.note, len(d.plan.Ops))
	}
}

func TestReviewRunningKeys(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	stage(h, 2, 2000)
	release := holdWrite(h, stagedPlan(t, h))
	defer release()
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.run.w.cur == 1 })
	h.keys("s")
	if !slices.Contains(h.fake.Calls(), "abort") || !d.run.stopping {
		t.Errorf("s did not stop the write: %v", h.fake.Calls())
	}
	h.keys("q")
	if _, ok := h.app.dialog.(*Confirm); !ok || h.quit {
		t.Errorf("q during a write: dialog %T, quit %v", h.app.dialog, h.quit)
	}
}

func TestReviewNoSession(t *testing.T) {
	h := newHarness(t, rvReady(t), func(o *Options) {
		o.Gates.AllowUntested = true
		o.Review = NewReview(nil)
	})
	stage(h, 1, 1600)
	h.keys("a", "enter")
	if d := rvDialog(t, h); d.step != reviewConfirm || len(h.fake.Calls()) > 0 {
		t.Errorf("step %d, calls %v", d.step, h.fake.Calls())
	}
}

func TestReviewASCII(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested, func(o *Options) { o.ASCII = true })...)
	rvStageMedia(h)
	h.keys("a")
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		s := h.screen(size[0], size[1])
		for _, r := range s {
			if r > 0x7E && r != '\n' {
				t.Fatalf("%dx%d shows %q with --ascii:\n%s", size[0], size[1], r, s)
			}
		}
	}
	rvWant(t, h, "Ctrl+Tab -> Play/Pause")
}

// Without a loaded mouse the review says so and keeps the edits, instead of
// blaming them.
func TestReviewWithoutAMouse(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1300)
	h.snap(attached(session.NoReceiver))
	h.keys("a")
	d := rvDialog(t, h)
	if len(d.refused) > 0 {
		t.Errorf("the review blames the edits: %v", d.refused)
	}
	rvWant(t, h, "Blocked. No mouse is loaded to plan against: plug in the receiver and wake the mouse; the edits stay staged.")
	if s := rvScreen(h); strings.Contains(s, "refused") || strings.Contains(s, "d drops") {
		t.Errorf("the review blames the edits:\n%s", s)
	}
	h.keys("enter")
	if calls := h.fake.Calls(); len(calls) > 0 || h.app.Pending().Len() != 1 {
		t.Errorf("calls %v, %d pending", calls, h.app.Pending().Len())
	}
}

// d in the review keeps the last Left Click, as the Buttons tab does.
func TestReviewDropKeepsTheLastLeftClick(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	left, right := mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft}, mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamRight}
	h.app.Pending().Stage(Staged{Key: SlotKey(1), Desc: "slot 1", Edit: mouse.SetKey{Slot: 1, Fn: left}})
	h.app.Pending().Stage(Staged{Key: SlotKey(0), Desc: "slot 0", Edit: mouse.SetKey{Slot: 0, Fn: right}})
	h.keys("a")
	d := rvDialog(t, h)
	if d.edits[0].Key != SlotKey(1) {
		t.Fatalf("the first edit is %s", d.edits[0].Key)
	}
	h.keys("d")
	if h.app.Pending().Len() != 2 || !h.app.notice.bad || !strings.Contains(h.app.notice.text, "last button on Left Click") {
		t.Errorf("%d pending, notice %+v", h.app.Pending().Len(), h.app.notice)
	}
}

// At 80 columns a body's bytes, lengths and packet count all show, with
// --ascii too.
func TestReviewBytesFit80(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		h := newHarness(t, rvReady(t), rvOpts(rvUntested, func(o *Options) { o.ASCII = ascii })...)
		cmdC := keys.Combo{keys.LMeta.Stroke(), keys.Stroke{Kind: keys.KindKey, Value: 0x06}}
		h.app.Pending().Stage(Staged{Key: SlotKey(2), Desc: "slot 2", Edit: mouse.SetShortcut{Slot: 2, Combo: cmdC}})
		h.keys("a")
		s := strings.Join(strings.Fields(h.screen(80, 60)), " ")
		for _, want := range []string{"04 80 08 00 81 06 00 41", "(14 bytes)", "2 packets"} {
			if !strings.Contains(s, want) {
				t.Errorf("ascii %v: the review lacks %q:\n%s", ascii, want, h.screen(80, 60))
			}
		}
		if strings.Contains(s, "(14 by…") || strings.Contains(s, "......") {
			t.Errorf("ascii %v: a bytes line is cut:\n%s", ascii, h.screen(80, 60))
		}
	}
}

// s during the first full backup stops the write; that is the user's stop,
// not a refusal.
func TestReviewStopBeforeTheFirstPacket(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	gate := make(chan struct{})
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		<-gate
		return session.Outcome{}, fmt.Errorf("%w before anything was written", safety.ErrAborted)
	}
	d := rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter", "s")
	close(gate)
	h.flush(func() bool { return d.step == reviewDone && h.app.write == nil })
	rvWant(t, h, "Stopped at your request: nothing was written to the mouse.")
	if s := rvScreen(h); strings.Contains(s, "Refused") || strings.Contains(h.app.notice.text, "refused") {
		t.Errorf("a stop reads as a refusal: notice %q\n%s", h.app.notice.text, s)
	}
	if !strings.Contains(h.app.notice.text, "stopped at your request") {
		t.Errorf("notice %q", h.app.notice.text)
	}
	h.keys("enter")
	if d.step != reviewPlan || h.app.dialog != d {
		t.Errorf("enter after the stop: step %d, dialog %T", d.step, h.app.dialog)
	}
}

// A pasted phrase is not taken, and the prompt says why.
func TestReviewIgnoresAPastedPhrase(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	rvConfirm(t, h)
	h.send(tea.PasteMsg{Content: "write untested"})
	h.keys("enter")
	rvWant(t, h, "does not match")
	h.send(tea.PasteMsg{Content: "write untested"})
	rvWant(t, h, "paste is ignored: type it out")
	if calls := h.fake.Calls(); slices.Contains(calls, "apply 1 ops") {
		t.Errorf("a paste went on: %v", calls)
	}
}
