package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func tabTitles(h *harness) []string {
	c := h.app.context()
	var out []string
	for _, i := range h.app.visible(c) {
		out = append(out, h.app.tabs[i].Title())
	}
	return out
}

func activeTitle(h *harness) string { return h.app.activeTab(h.app.context()).Title() }

func TestTabSets(t *testing.T) {
	unknown := attached(session.Unknown)
	unknown.Handshake = &session.Handshake{CID: 0x7B, MID: 9}
	keyboard := ready(t)
	keyboard.Model, _ = catalog.ByKey("0301")
	em06 := ready(t)
	em06.Model, _ = catalog.ByKey("7B02")
	tests := []struct {
		name string
		sn   *session.Snapshot
		opts []func(*Options)
		want []string
	}{
		{"em11", ready(t), nil, []string{"Buttons", "DPI", "Info", "Log"}},
		{"em11 experimental", ready(t), []func(*Options){func(o *Options) { o.Gates.Experimental = true }},
			[]string{"Buttons", "DPI", "Info", "Advanced", "Log"}},
		{"em06", em06, nil, []string{"Buttons", "DPI", "Info", "Log"}},
		{"unknown device", unknown, nil, []string{"Info", "Log"}},
		{"keyboard", keyboard, nil, []string{"Info", "Log"}},
		{"no receiver", bare(session.NoReceiver), nil, []string{"Info", "Log"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.sn, tc.opts...)
			if got := tabTitles(h); !slices.Equal(got, tc.want) {
				t.Errorf("tabs %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTabKeys(t *testing.T) {
	h := newHarness(t, ready(t))
	for _, step := range []struct{ key, want string }{
		{"2", "DPI"}, {"tab", "Info"}, {"tab", "Log"}, {"tab", "Buttons"}, {"shift+tab", "Log"}, {"9", "Log"}, {"1", "Buttons"},
	} {
		h.keys(step.key)
		if got := activeTitle(h); got != step.want {
			t.Fatalf("after %s: tab %s, want %s", step.key, got, step.want)
		}
	}
	h.keys("enter", "x")
	if got := h.app.tabs[0].(*stubTab).keys; !slices.Equal(got, []string{"enter", "x"}) {
		t.Errorf("the active tab got %v", got)
	}
	if got := h.app.tabs[1].(*stubTab).keys; len(got) > 0 {
		t.Errorf("an inactive tab got %v", got)
	}
}

func TestHiddenTabLeavesActive(t *testing.T) {
	h := newHarness(t, ready(t))
	h.keys("2")
	h.snap(attached(session.NoReceiver))
	if got := activeTitle(h); got != "Info" {
		t.Errorf("active tab %s, want Info", got)
	}
	h.snap(ready(t))
	if got := activeTitle(h); got != "DPI" {
		t.Errorf("active tab %s once DPI shows again, want DPI", got)
	}
}

func TestCapturingTabTakesKeys(t *testing.T) {
	h := newHarness(t, ready(t))
	tab := h.app.tabs[0].(*stubTab)
	tab.capture = true
	h.keys("q", "2", "a")
	if h.quit || activeTitle(h) != "Buttons" || !slices.Equal(tab.keys, []string{"q", "2", "a"}) {
		t.Fatalf("quit %v, tab %s, keys %v", h.quit, activeTitle(h), tab.keys)
	}
	h.keys("ctrl+c")
	if !h.quit {
		t.Error("ctrl+c did not quit")
	}
}

func TestSnapshotsReachTabs(t *testing.T) {
	h := newHarness(t, ready(t))
	tab := h.app.tabs[2].(*stubTab)
	h.snap(withState(ready(t), session.Offline))
	h.send(SnapshotMsg{h.fake.Snapshot()})
	n := 0
	for _, m := range tab.msgs {
		if _, ok := m.(SnapshotMsg); ok {
			n++
		}
	}
	if n != 2 {
		t.Errorf("an inactive tab got %d snapshots, want 2: the first and the offline one (a repeated one is dropped)", n)
	}
}

// stage stages a DPI edit through the shared store.
func stage(h *harness, stageNo, dpi int) {
	h.app.Pending().Stage(Staged{Key: DPIKey(stageNo), Desc: "DPI", Edit: mouse.SetDPI{Stage: stageNo, DPI: dpi}})
}

func stagedPlan(t *testing.T, h *harness) plan.Plan {
	t.Helper()
	p, err := h.app.context().Plan()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// holdWrite makes the fake's writes report every op's events up to a
// verified first op, then wait for release before finishing the rest.
func holdWrite(h *harness, p plan.Plan) (release func()) {
	gate := make(chan struct{})
	h.fake.write = func(ctx context.Context, on func(safety.OpEvent)) (session.Outcome, error) {
		ev := func(k safety.EventKind, op plan.Op, chunk, chunks int) {
			on(safety.OpEvent{Run: "20260926T120000Z-4242", Kind: k, Op: op, Chunk: chunk, Chunks: chunks})
		}
		first := p.Ops[0]
		ev(safety.EventStart, first, 0, 1)
		ev(safety.EventChunk, first, 1, 1)
		ev(safety.EventWritten, first, 0, 0)
		ev(safety.EventVerified, first, 0, 0)
		if len(p.Ops) > 1 {
			ev(safety.EventStart, p.Ops[1], 0, 1)
		}
		<-gate
		for _, op := range p.Ops[1:] {
			ev(safety.EventChunk, op, 1, 1)
			ev(safety.EventVerified, op, 0, 0)
		}
		return session.Outcome{Result: safety.Result{Run: "20260926T120000Z-4242", Ops: len(p.Ops), Verified: len(p.Ops)},
			Backups: []string{"backups/em11-pro-260d-1282-7b04/20260926T120000Z-auto-before-write.json"}}, nil
	}
	return func() { close(gate) }
}

func TestApply(t *testing.T) {
	h := newHarness(t, ready(t), func(o *Options) { o.Gates.AllowUntested = true })
	stage(h, 1, 1600)
	stage(h, 2, 2000)
	p := stagedPlan(t, h)
	if len(p.Ops) != 2 {
		t.Fatalf("%d ops", len(p.Ops))
	}
	release := holdWrite(h, p)
	g := h.app.opt.Gates
	g.Confirm = "write untested"
	h.startWrite(applyRequest(p, g))
	h.flush(func() bool { return len(h.app.write.steps) == 2 && h.app.write.cur == 1 })
	h.snap(withState(ready(t), session.Applying))
	if err := h.app.context().CanApply(); !errors.Is(err, ErrWriting) {
		t.Errorf("CanApply during a write: %v", err)
	}
	h.golden("applying")
	release()
	h.flush(func() bool { return h.app.write == nil })
	if n := h.app.Pending().Len(); n != 0 {
		t.Errorf("%d edits still pending after a verified apply", n)
	}
	if got := h.fake.gates; len(got) != 1 || got[0] != g {
		t.Errorf("gates %+v, want %+v", got, g)
	}
	if want := "Apply done: 2 of 2 records verified. Backup saved:"; !strings.HasPrefix(h.app.notice.text, want) {
		t.Errorf("notice %q", h.app.notice.text)
	}
	events, done := 0, 0
	for _, m := range h.app.tabs[2].(*stubTab).msgs {
		switch m.(type) {
		case OpEventMsg:
			events++
		case WriteDoneMsg:
			done++
		}
	}
	if events != 7 || done != 1 {
		t.Errorf("an inactive tab got %d events and %d ends, want 7 and 1", events, done)
	}
	h.snap(ready(t))
	h.golden("applied")
}

func TestApplyStopKeepsPending(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	p := stagedPlan(t, h)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{Result: safety.Result{Ops: 1}}, &safety.StopError{Op: p.Ops[0], Err: safety.ErrMismatch}
	}
	h.startWrite(applyRequest(p, safety.Gates{}))
	h.flush(func() bool { return h.app.write == nil })
	if h.app.Pending().Len() != 1 || !h.app.notice.bad || !strings.Contains(h.app.notice.text, "Apply stopped: 0 of 1") {
		t.Errorf("pending %d, notice %+v", h.app.Pending().Len(), h.app.notice)
	}
}

func TestDryRunModeGates(t *testing.T) {
	h := newHarness(t, ready(t), mode(ModeDryRun))
	stage(h, 1, 1600)
	h.startWrite(applyRequest(stagedPlan(t, h), safety.Gates{}))
	h.flush(func() bool { return h.app.write == nil })
	if len(h.fake.gates) != 1 || !h.fake.gates[0].DryRun {
		t.Errorf("gates %+v, want a dry run", h.fake.gates)
	}
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	h := newHarness(t, ready(t), mode(ModeReadOnly))
	stage(h, 1, 1600)
	h.keys("a")
	if !h.app.notice.bad || h.app.dialog != nil {
		t.Errorf("review opened in a read-only session: %+v", h.app.notice)
	}
	h.startWrite(writeRequest{kind: safety.KindRevert})
	if calls := h.fake.Calls(); len(calls) > 0 {
		t.Errorf("calls %v", calls)
	}
}

func TestCanApply(t *testing.T) {
	for _, tc := range []struct {
		st   session.State
		want error
	}{
		{session.Ready, nil}, {session.Offline, ErrAsleep}, {session.Recovering, ErrUnsettled},
		{session.Conflict, ErrWritesOff}, {session.SuspectedConflict, ErrWritesOff}, {session.Loading, ErrNotReady},
	} {
		h := newHarness(t, withState(ready(t), tc.st))
		if err := h.app.context().CanApply(); !errors.Is(err, ErrNothingPending) {
			t.Errorf("%v with nothing pending: %v", tc.st, err)
		}
		stage(h, 1, 1600)
		if err := h.app.context().CanApply(); !errors.Is(err, tc.want) {
			t.Errorf("%v: %v, want %v", tc.st, err, tc.want)
		}
	}
}

func TestQuitWhileWriting(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	stage(h, 2, 2000)
	p := stagedPlan(t, h)
	release := holdWrite(h, p)
	h.startWrite(applyRequest(p, safety.Gates{}))
	h.keys("q")
	if h.quit || h.app.dialog == nil {
		t.Fatal("q did not ask while writing")
	}
	h.golden("quit-while-writing")
	h.keys("y")
	if !slices.Contains(h.fake.Calls(), "abort") || h.quit {
		t.Fatalf("calls %v, quit %v", h.fake.Calls(), h.quit)
	}
	release()
	h.flush(func() bool { return h.quit })
}

func TestCtrlCTwiceQuitsDuringWrite(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	stage(h, 2, 2000)
	p := stagedPlan(t, h)
	release := holdWrite(h, p)
	defer release()
	h.startWrite(applyRequest(p, safety.Gates{}))
	h.keys("ctrl+c", "y", "ctrl+c")
	if !h.quit {
		t.Error("a second ctrl+c did not quit")
	}
	h = newHarness(t, ready(t))
	stage(h, 1, 1600)
	release2 := holdWrite(h, stagedPlan(t, h))
	defer release2()
	h.startWrite(applyRequest(stagedPlan(t, h), safety.Gates{}))
	h.keys("ctrl+c")
	if h.quit {
		t.Fatal("the first ctrl+c during a write quit")
	}
	h.keys("ctrl+c")
	if !h.quit {
		t.Error("a second ctrl+c while the prompt is open did not quit")
	}
}

func TestQuitWithPending(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	h.keys("q", "n")
	if h.quit || h.app.dialog != nil {
		t.Fatal("n did not cancel")
	}
	h.keys("q", "y")
	if !h.quit {
		t.Error("y did not quit")
	}
}

func TestDiscard(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	stage(h, 2, 2000)
	h.keys("u")
	h.golden("discard")
	h.keys("y")
	if n := h.app.Pending().Len(); n != 0 || h.app.notice.text != "2 pending edits discarded." {
		t.Errorf("pending %d, notice %q", n, h.app.notice.text)
	}
}

func TestReviewHook(t *testing.T) {
	var got *Context
	d := &Confirm{Title: "Review"}
	h := newHarness(t, ready(t), func(o *Options) {
		o.Review = func(c *Context) Dialog { got = c; return d }
	})
	h.keys("a")
	if h.app.dialog != nil || !strings.Contains(h.app.notice.text, "Nothing is pending") {
		t.Fatalf("a with nothing pending: dialog %v, notice %q", h.app.dialog, h.app.notice.text)
	}
	stage(h, 1, 1600)
	h.keys("a")
	if h.app.dialog != d || got == nil || got.Pending.Len() != 1 {
		t.Errorf("the review hook was not used")
	}
}

func TestChoose(t *testing.T) {
	sn := bare(session.Choosing)
	second := receiver
	second.Path = "emu:2/IOUSBHostInterface@1"
	sn.Answers = []session.Answer{{Candidate: receiver}, {Candidate: second}}
	h := newHarness(t, sn)
	h.keys("down", "down", "enter")
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"choose " + second.Path}) {
		t.Errorf("calls %v", calls)
	}
}

func TestClearConflict(t *testing.T) {
	h := newHarness(t, withState(ready(t), session.Conflict))
	h.keys("c")
	h.golden("clear-conflict")
	h.keys("y")
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"clear conflict"}) || h.app.notice.text != "Conflict cleared." {
		t.Errorf("calls %v, notice %q", calls, h.app.notice.text)
	}
	h.fake.err = session.ErrConflict
	h.keys("c", "y")
	if !h.app.notice.bad {
		t.Errorf("a refused clear is not reported: %+v", h.app.notice)
	}
}

func lastRun(t *testing.T, sn *session.Snapshot) *safety.Run {
	p, err := mouse.PlanEdits(sn.Model, sn.Image, []mouse.Edit{mouse.SetDPI{Stage: 1, DPI: 1600}}, mouse.Options{Device: sn.Identity})
	if err != nil {
		t.Fatal(err)
	}
	run := &safety.Run{ID: "20260926T115000Z-4242", Kind: safety.KindApply, Device: sn.Identity, Started: now.Add(-10 * time.Minute),
		Ended: true, Complete: true}
	for _, op := range p.Ops {
		run.Ops = append(run.Ops, safety.OpRecord{Op: op, State: safety.StateVerified})
	}
	return run
}

// TestRevertKeyOpensTheRevertReview checks that U opens the review the Log
// tab opens with v, which runs the revert's preflight before its typed
// phrase goes on to the session's Revert.
func TestRevertKeyOpensTheRevertReview(t *testing.T) {
	h := newHarness(t, revertSnap(t), func(o *Options) { o.Gates.AllowUntested = true })
	h.keys("U")
	d := rvDialog(t, h)
	if d.kind != safety.KindRevert {
		t.Fatalf("U opened a review of %v", d.kind)
	}
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewConfirm })
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return h.app.write == nil && len(h.fake.Calls()) > 1 })
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"preflight revert", "revert"}) || h.fake.gates[0].Confirm != "write untested" {
		t.Errorf("calls %v, gates %+v", calls, h.fake.gates)
	}
}

func TestRevertWithoutRun(t *testing.T) {
	h := newHarness(t, ready(t))
	h.keys("U")
	if h.app.dialog != nil || !strings.HasPrefix(h.app.notice.text, "Nothing to revert") {
		t.Errorf("dialog %v, notice %q", h.app.dialog, h.app.notice.text)
	}
}

func TestRecoveryPrompt(t *testing.T) {
	h := newHarness(t, unfinished(t))
	if _, ok := h.app.dialog.(*recoveryDialog); !ok {
		t.Fatalf("no recovery prompt at startup: %T", h.app.dialog)
	}
	h.keys("l")
	if !h.app.notice.bad {
		t.Error("leave was not refused on a torn record")
	}
	h.keys("esc")
	if h.app.dialog != nil {
		t.Fatal("esc did not close the prompt")
	}
	h.snap(unfinished(t))
	if h.app.dialog != nil {
		t.Fatal("the prompt opened again for the same run")
	}
	h.keys("R", "f")
	h.golden("recover-confirm")
	h.keys("y")
	h.flush(func() bool { return h.app.write == nil && len(h.fake.Calls()) > 0 })
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"recover 20260926T115900Z-4242 forward"}) {
		t.Errorf("calls %v", calls)
	}
}

func TestDifferentMouseDropsPending(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	other := ready(t)
	other.Identity.MID, other.Handshake.MID = 6, 6
	other.Model, _ = catalog.ByKey("7B06")
	h.snap(other)
	if n := h.app.Pending().Len(); n != 0 || !strings.Contains(h.app.notice.text, "different mouse") {
		t.Errorf("pending %d, notice %q", n, h.app.notice.text)
	}
}

type fakeStore struct{ saved int }

func (s *fakeStore) Save(session.Capture, string) (string, error) {
	s.saved++
	return "backups/em11/20260926T120000Z.json", nil
}

func (s *fakeStore) Image(string) (*flash.Image, error) { return nil, errors.New("no backups here") }

func TestBackupAndReload(t *testing.T) {
	store := &fakeStore{}
	h := newHarness(t, ready(t), func(o *Options) { o.Backups = store })
	h.keys("b")
	if store.saved != 1 || h.app.notice.text != "Backup saved:" || h.app.notice.path != "backups/em11/20260926T120000Z.json" {
		t.Errorf("saved %d, notice %q", store.saved, h.app.notice.text)
	}
	h.keys("r")
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"backup full=false", "reload"}) || h.app.notice.text != "Reloaded." {
		t.Errorf("calls %v, notice %q", calls, h.app.notice.text)
	}
}

func TestOpenSettings(t *testing.T) {
	opened := 0
	h := newHarness(t, bare(session.NeedsPermission), func(o *Options) {
		o.Host.OpenSettings = func() error { opened++; return nil }
	})
	h.keys("o")
	if opened != 1 {
		t.Errorf("opened %d times", opened)
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	h.keys("?")
	h.golden("help")
	h.keys("esc")
	if h.app.dialog != nil {
		t.Error("esc did not close the help")
	}
}

// TestStoppedReviewPromptsOnce checks that closing a review that showed the
// recovery prompt of the run it stopped does not open the shell's own
// prompt for that run; R still does.
func TestStoppedReviewPromptsOnce(t *testing.T) {
	const id = "20260926T120000Z-4242"
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	p := stagedPlan(t, h)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		return session.Outcome{Result: safety.Result{Run: id, Ops: 1}},
			&safety.StopError{Op: p.Ops[0], Written: true, Class: safety.ClassTorn, Err: wire.ErrNAK}
	}
	rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewDone && h.app.write == nil })
	h.snap(rvStopped(t, p, id))
	h.keys("esc")
	h.snap(rvStopped(t, p, id))
	if h.app.dialog != nil {
		t.Fatalf("the shell prompted again with %T", h.app.dialog)
	}
	h.keys("R")
	if _, ok := h.app.dialog.(*recoveryDialog); !ok {
		t.Fatalf("R opened %T", h.app.dialog)
	}
}

func TestCtrlCAsksWithPending(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	h.keys("ctrl+c")
	if h.quit {
		t.Fatalf("ctrl+c dropped %d pending edits without asking", h.app.Pending().Len())
	}
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("ctrl+c opened %T", h.app.dialog)
	}
	h.keys("ctrl+c")
	if !h.quit {
		t.Error("a second ctrl+c while the prompt is open did not quit")
	}
}

// writerTab sends a write request of its own on w.
type writerTab struct {
	stubTab
	p plan.Plan
}

func (t *writerTab) Update(c *Context, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "w" {
		return writeRequest{kind: safety.KindApply, plan: t.p, gates: c.Gates}.cmd()
	}
	return nil
}

func TestTabCannotWriteWithoutReview(t *testing.T) {
	sn := ready(t)
	p, err := mouse.PlanEdits(sn.Model, sn.Image, []mouse.Edit{mouse.SetDPI{Stage: 1, DPI: 1600}}, mouse.Options{Device: sn.Identity})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, sn, func(o *Options) { o.Tabs = []Tab{&writerTab{stubTab: stubTab{title: "Writer"}, p: p}} })
	h.keys("w")
	gone := &approval{}
	h.send(writeRequest{kind: safety.KindApply, plan: p, from: gone})
	h.flush(func() bool { return h.app.write == nil })
	if calls := h.fake.Calls(); len(calls) > 0 {
		t.Fatalf("a tab wrote without the review: %v", calls)
	}
	if !h.app.notice.bad || gone.used {
		t.Errorf("the refused write is not reported: %+v; a closed dialog was asked: %v", h.app.notice, gone.used)
	}
}

// The quit prompt of a running write closes when the write ends; its y
// never carries over to the next write.
func TestQuitPromptFollowsTheWrite(t *testing.T) {
	h := newHarness(t, ready(t))
	stage(h, 1, 1600)
	p := stagedPlan(t, h)
	release := holdWrite(h, p)
	h.startWrite(applyRequest(p, safety.Gates{}))
	h.keys("q")
	release()
	h.flush(func() bool { return h.app.write == nil })
	if d, ok := h.app.dialog.(*Confirm); ok && d.Title == "A write is running" {
		t.Fatal("the write prompt stayed open after the write ended")
	}
	h.keys("y")
	if !h.quit {
		t.Fatalf("y after the write did not quit: dialog %T", h.app.dialog)
	}
	h = newHarness(t, ready(t))
	stage(h, 1, 1600)
	p = stagedPlan(t, h)
	release = holdWrite(h, p)
	h.startWrite(applyRequest(p, safety.Gates{}))
	h.keys("q", "n")
	release()
	h.flush(func() bool { return h.app.write == nil })
	stage(h, 2, 2000)
	p = stagedPlan(t, h)
	release = holdWrite(h, p)
	h.startWrite(applyRequest(p, safety.Gates{}))
	release()
	h.flush(func() bool { return h.app.write == nil })
	if h.quit {
		t.Error("a later write quit arcctl")
	}
}

func TestQuitFromReviewReturnsToIt(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	release := holdWrite(h, stagedPlan(t, h))
	d := rvConfirm(t, h)
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return h.app.write != nil && len(h.app.write.steps) > 0 && h.app.write.cur >= 0 })
	h.keys("q")
	if c, ok := h.app.dialog.(*Confirm); !ok || c.Title != "A write is running" {
		t.Fatalf("q in the running review opened %T", h.app.dialog)
	}
	h.keys("n")
	if h.app.dialog != d {
		t.Fatalf("n left %T open, not the review", h.app.dialog)
	}
	h.keys("esc")
	if h.app.dialog != nil {
		t.Fatalf("esc left %T open", h.app.dialog)
	}
	if s := h.screen(80, 24); !strings.Contains(s, "a shows the write") {
		t.Errorf("the banner does not say how to see the write again:\n%s", s)
	}
	h.keys("a")
	if h.app.dialog != d {
		t.Fatalf("a opened %T, not the running review", h.app.dialog)
	}
	release()
	h.flush(func() bool { return d.step == reviewDone })
}

func TestReviewSubPromptsReturnToIt(t *testing.T) {
	h := newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	stage(h, 1, 1600)
	h.keys("a")
	d := rvDialog(t, h)
	h.keys("u")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("u opened %T", h.app.dialog)
	}
	h.keys("n")
	if h.app.dialog != d {
		t.Fatalf("n left %T open, not the review", h.app.dialog)
	}
	h.keys("q")
	if h.app.dialog != nil || h.quit || h.app.Pending().Len() != 1 {
		t.Fatalf("q closes the review like every dialog: %T open, quit %v, %d pending", h.app.dialog, h.quit, h.app.Pending().Len())
	}
}

// tornRun is a Recovering mouse whose journal holds an apply of slot 3 to
// Play/Pause and DPI stages 2 and 3, stopped with every record torn.
func tornRun(t *testing.T) *session.Snapshot {
	sn := withState(rvReady(t), session.Recovering)
	p, err := mouse.PlanEdits(sn.Model, sn.Image, []mouse.Edit{
		mouse.SetMedia{Slot: 3, Usage: rvPlayPause}, mouse.SetDPI{Stage: 1, DPI: 1600}, mouse.SetDPI{Stage: 2, DPI: 2000},
	}, mouse.Options{Device: sn.Identity})
	if err != nil {
		t.Fatal(err)
	}
	run := &safety.Run{ID: "20260926T115900Z-4242", Kind: safety.KindApply, Device: sn.Identity,
		Started: now.Add(-time.Minute), Err: "cmd 7 (write): wire: rejected by the device"}
	in := &safety.Inspection{Run: run, Image: sn.Image.Clone()}
	seen := map[flash.Extent]bool{}
	for _, op := range p.Ops {
		run.Ops = append(run.Ops, safety.OpRecord{Op: op, State: safety.StateSending})
		if seen[op.Extent] {
			continue
		}
		seen[op.Extent] = true
		found := make([]byte, op.Extent.Len)
		copy(found, []byte{0x12, 0x34, 0x56, 0x78})
		if err := in.Image.Set(op.Extent.Addr, found); err != nil {
			t.Fatal(err)
		}
		in.Extents = append(in.Extents, safety.ExtentState{Extent: op.Extent, Before: op.Old, After: op.New,
			Found: found, Class: safety.ClassTorn, Tier: op.Tier, Desc: op.Desc})
	}
	sn.Journal = &session.JournalState{Open: []session.OpenRun{{Run: run, Inspection: in}}}
	return sn
}

// The ways to settle a run stay on screen at 80x24 while its records
// scroll above them.
func TestRecoveryPromptFits80x24(t *testing.T) {
	h := newHarness(t, tornRun(t))
	d, ok := h.app.dialog.(*recoveryDialog)
	if !ok {
		t.Fatalf("the prompt did not open: %T", h.app.dialog)
	}
	s := h.screen(80, 24)
	for _, want := range []string{"f finish:", "b roll back:", "l leave:", "esc later", "below: pgdn", "It stopped: rejected by the device."} {
		if !strings.Contains(strings.Join(strings.Fields(s), " "), want) {
			t.Errorf("the 80x24 prompt does not show %q:\n%s", want, s)
		}
	}
	h.golden("recovery-long")
	h.keys("pgdown")
	if d.page.scroll == 0 {
		t.Error("pgdown did not scroll the records")
	}
}

// A recovery shows its progress out of the records its plan writes, and the
// prompt closes once it starts.
func TestRecoveryProgress(t *testing.T) {
	h := newHarness(t, tornRun(t), func(o *Options) { o.Gates.AllowUntested = true })
	gate := make(chan struct{})
	defer close(gate)
	h.fake.write = func(context.Context, func(safety.OpEvent)) (session.Outcome, error) {
		<-gate
		return session.Outcome{}, nil
	}
	h.keys("f")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("f opened %T", h.app.dialog)
	}
	h.keys("y")
	if h.app.write == nil || h.app.dialog != nil {
		t.Fatalf("write %v, dialog %T", h.app.write != nil, h.app.dialog)
	}
	h.snap(withState(tornRun(t), session.Applying))
	if s := h.screen(120, 40); !strings.Contains(s, "Settling the unfinished write: 0 of 4 records verified.") {
		t.Errorf("the banner does not count the recovery's records:\n%s", s)
	}
}

func footerLine(h *harness, w, ht int) string {
	lines := strings.Split(h.screen(w, ht), "\n")
	return lines[len(lines)-1]
}

// With edits pending, the footer offers the review before the tab's own
// keys, at every width.
func TestFooterOffersTheReview(t *testing.T) {
	h := newHarness(t, ready(t), func(o *Options) { o.Tabs = []Tab{NewButtons(nil, nil), NewDPITab()} })
	stage(h, 1, 1600)
	for _, tab := range []string{"1", "2"} {
		h.keys(tab)
		for _, w := range []int{80, 120} {
			if f := footerLine(h, w, 24); !strings.Contains(f, "a review & apply") || !strings.Contains(f, "q quit") {
				t.Errorf("tab %s at %d columns: %q", tab, w, f)
			}
		}
	}
}

// While a tab takes every key, the footer offers only the tab's keys.
func TestFooterWhileCapturing(t *testing.T) {
	h := newHarness(t, ready(t), func(o *Options) { o.Tabs = []Tab{NewDPITab()} })
	h.keys("enter")
	if f := footerLine(h, 80, 24); strings.Contains(f, "? help") || strings.Contains(f, "q quit") || !strings.Contains(f, "esc cancel") {
		t.Errorf("footer while typing a value: %q", f)
	}
}

// A notice takes two lines when it needs them, and a path in it keeps its
// file name.
func TestNoticeWraps(t *testing.T) {
	h := newHarness(t, ready(t))
	h.send(noticeMsg{text: "This slot is not a button the web app shows; editing it is experimental: restart arcctl with --experimental.", bad: true})
	if s := strings.Join(strings.Fields(h.screen(80, 24)), " "); !strings.Contains(s, "restart arcctl with --experimental.") {
		t.Errorf("the notice lost its tail:\n%s", h.screen(80, 24))
	}
	path := "/var/folders/xy/xxxxxxxxxxxxxxxxxxxxxxxx0000gn/T/arcctl-emulated-1542904641/backups/em11-pro-260d-1282-7b04/" +
		"20260926T120000Z-auto-before-write.json"
	h.send(writeNotice(&writing{kind: safety.KindApply}, WriteDoneMsg{Kind: safety.KindApply,
		Outcome: session.Outcome{Result: safety.Result{Ops: 1, Verified: 1}, Backups: []string{path}}}))
	if s := h.screen(80, 24); !strings.Contains(s, "20260926T120000Z-auto-before-write.json") || !strings.Contains(s, "Apply done") {
		t.Errorf("the backup path lost its file name:\n%s", s)
	}
}

// planRef matches the plan's own references: invariants, decisions, stages
// and wire details that mean nothing to a user of the tool.
var planRef = regexp.MustCompile(`\((I|D|T)\d+\)|\bH\d+b?\b|kind \d|\bcid\b|\bmid\b|op \d+ \(|\d+(\.\d+)?s needed`)

func noPlanRefs(t *testing.T, what, s string) {
	t.Helper()
	if m := planRef.FindAllString(s, -1); len(m) > 0 {
		t.Errorf("%s shows %q:\n%s", what, m, s)
	}
}

func TestNoPlanReferencesOnScreen(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	noPlanRefs(t, "the Buttons tab", h.screen(80, 24))
	b.all = true
	btnRow(t, h, b, 7)
	h.keys("enter")
	noPlanRefs(t, "the hidden slot refusal", h.app.notice.text)

	h = newHarness(t, ready(t), func(o *Options) { o.Tabs = []Tab{NewInfoTab()} })
	noPlanRefs(t, "the Info tab", h.screen(120, 80))

	h = newHarness(t, rvReady(t), rvOpts(rvUntested)...)
	menu := keys.Combo{keys.LMeta.Stroke(), keys.Stroke{Kind: keys.KindMenu, Value: 1}}
	h.app.Pending().Stage(Staged{Key: SlotKey(2), Desc: "slot 2", Edit: mouse.SetShortcut{Slot: 2, Combo: menu}})
	h.keys("a")
	noPlanRefs(t, "the review", h.screen(120, 80))

	h = newHarness(t, withState(ready(t), session.Conflict))
	h.fake.err = fmt.Errorf("%w: quiet for 1.145s, 10s needed", session.ErrConflict)
	h.keys("c", "y")
	noPlanRefs(t, "the refused clear", h.app.notice.text)
}

// The help fits at 80x24 even under a banner, in two columns.
func TestHelpFits80x24(t *testing.T) {
	h := newHarness(t, ready(t), func(o *Options) { o.Tabs = []Tab{NewDPITab(), NewInfoTab(), NewLogTab()} })
	stage(h, 1, 1600)
	h.keys("?")
	s := h.screen(80, 24)
	for _, want := range []string{"quit", "No key needs the mouse", "revert last apply", "stage count"} {
		if !strings.Contains(s, want) {
			t.Errorf("the help lacks %q:\n%s", want, s)
		}
	}
	h.golden("help-dpi")
}
