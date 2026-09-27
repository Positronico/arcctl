package tui

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

func showLog(h *harness) { dilKeys(h, "3") }

// revertSnap is a Ready mouse whose image holds what the journal's last run
// wrote: DPI stage 2 changed from 1200 to 1600.
func revertSnap(t *testing.T) *session.Snapshot {
	t.Helper()
	sn := ready(t)
	p, err := mouse.PlanEdits(sn.Model, sn.Image, []mouse.Edit{mouse.SetDPI{Stage: 1, DPI: 1600}}, mouse.Options{Device: sn.Identity})
	if err != nil {
		t.Fatal(err)
	}
	im := sn.Image.Clone()
	run := &safety.Run{ID: "20260926T115000Z-4242", Kind: safety.KindApply, Device: sn.Identity,
		Started: now.Add(-10 * time.Minute), Ended: true, Complete: true}
	for _, op := range p.Ops {
		if err := im.Set(op.Extent.Addr, op.New); err != nil {
			t.Fatal(err)
		}
		run.Ops = append(run.Ops, safety.OpRecord{Op: op, State: safety.StateVerified})
	}
	sn.Image = im
	sn.Journal = &session.JournalState{Last: run}
	return sn
}

func TestLogEvents(t *testing.T) {
	loading := attached(session.Loading)
	loading.Progress = session.Progress{Job: "load", Done: 3, Total: 20}
	h := dilHarness(t, loading)
	h.snap(loading)
	sn := ready(t)
	h.snap(sn)
	busy := ready(t)
	busy.Stats = session.Stats{Transactions: 40, Foreign: 2, Duplicates: 1, Pushes: 1}
	busy.Clients = []session.Client{{PID: 4242, Name: "Google Chrome"}}
	busy.State = session.Conflict
	h.snap(busy)
	h.snap(ready(t))

	rs := revertSnap(t)
	op := rs.Journal.Last.Ops[0].Op
	for _, k := range []safety.EventKind{safety.EventStart, safety.EventChunk, safety.EventWritten, safety.EventVerified} {
		h.send(OpEventMsg{safety.OpEvent{Run: "20260926T120000Z-4242", Kind: k, Op: op, Chunk: 1, Chunks: 1}})
	}
	h.send(WriteDoneMsg{Kind: safety.KindApply, Outcome: session.Outcome{
		Result:  safety.Result{Run: "20260926T120000Z-4242", Ops: 1, Verified: 1},
		Backups: []string{"backups/em11-pro-260d-1282-7b04/20260926T120000Z-auto-before-write.json"}}})
	h.snap(rs)
	showLog(h)
	h.golden("log")
	text := h.screen(120, 40)
	for _, want := range []string{"State: loading", "Loading the configuration finished", "Handshake: 7B/04 ProtoArc EM11 Pro",
		"2 foreign replies (2 in all)", "Other programs on the receiver: Google Chrome (pid 4242)", "op 1 16+4 DPI stage 2: 1600: verified",
		"Apply 20260926T120000Z-4242 done: 1 of 1 records verified", "Journal: last change apply 20260926T115000Z-4242"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q", want)
		}
	}
	if strings.Contains(text, ": chunk") {
		t.Error("chunk events are logged")
	}
}

func TestLogScroll(t *testing.T) {
	h := dilHarness(t, ready(t))
	showLog(h)
	for i := range 40 {
		sn := ready(t)
		sn.Stats.Pushes = i + 1
		h.snap(sn)
	}
	h.screen(80, 24)
	dilKeys(h, "k", "k", "k")
	if s := h.screen(80, 24); !strings.Contains(s, "4 newer (G shows the newest)") || strings.Contains(s, "40 in all") {
		t.Errorf("scrolled back 3:\n%s", s)
	}
	sn := ready(t)
	sn.Stats.Pushes = 41
	h.snap(sn)
	if s := h.screen(80, 24); !strings.Contains(s, "5 newer") {
		t.Errorf("a new event moved the scrolled view:\n%s", s)
	}
	dilKeys(h, "G")
	if s := h.screen(80, 24); !strings.Contains(s, "(41 in all)") || strings.Contains(s, "newer") {
		t.Errorf("G:\n%s", s)
	}
	dilKeys(h, "g")
	if s := h.screen(80, 24); !strings.Contains(s, "State: ready") {
		t.Errorf("g does not show the oldest:\n%s", s)
	}
}

func TestLogRevertReview(t *testing.T) {
	h := dilHarness(t, revertSnap(t))
	showLog(h)
	dilKeys(h, "v", "enter")
	if s := h.screen(80, 24); len(h.fake.Calls()) > 0 || !strings.Contains(s, "--allow-untested") {
		t.Fatalf("without --allow-untested: %v\n%s", h.fake.Calls(), s)
	}

	h = dilHarness(t, revertSnap(t), func(o *Options) { o.Gates.AllowUntested = true })
	showLog(h)
	h.golden("log-journal")
	dilKeys(h, "v")
	d := rvDialog(t, h)
	if d.kind != safety.KindRevert || len(d.plan.Ops) != 1 || d.planErr != nil || !slices.Equal(d.plan.Ops[0].New, revertSnap(t).Journal.Last.Ops[0].Old) {
		t.Fatalf("revert plan %+v, %v", d.plan.Ops, d.planErr)
	}
	rvWant(t, h, "Review the revert", "1600 → 1200", "It undoes apply 20260926T115000Z-4242")
	dilKeys(h, "enter")
	h.flush(func() bool { return d.step == reviewConfirm })
	h.typeText("write unt")
	dilKeys(h, "enter")
	h.golden("log-revert")
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"preflight revert"}) {
		t.Fatalf("a partial phrase went on: %v", calls)
	}
	h.typeText("ested")
	dilKeys(h, "enter")
	h.flush(func() bool { return h.app.write == nil && len(h.fake.Calls()) > 1 })
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"preflight revert", "revert"}) || h.fake.gates[0].Confirm != "write untested" {
		t.Errorf("calls %v, gates %+v", calls, h.fake.gates)
	}
}

func TestLogRevertDiverged(t *testing.T) {
	sn := ready(t)
	sn.Journal = &session.JournalState{Last: lastRun(t, sn)}
	h := dilHarness(t, sn, func(o *Options) { o.Gates.AllowUntested = true })
	showLog(h)
	dilKeys(h, "v")
	h.golden("log-revert-diverged")
	dilKeys(h, "enter")
	h.typeText("write untested")
	dilKeys(h, "enter", "y")
	if len(h.fake.Calls()) > 0 {
		t.Errorf("a diverged revert went on: %v", h.fake.Calls())
	}
}

func TestLogRevertDryRun(t *testing.T) {
	h := dilHarness(t, revertSnap(t), mode(ModeDryRun))
	showLog(h)
	dilKeys(h, "v", "enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step == reviewConfirm })
	dilKeys(h, "y")
	h.flush(func() bool { return h.app.write == nil && len(h.fake.Calls()) > 1 })
	if calls := h.fake.Calls(); !slices.Equal(calls, []string{"preflight revert", "revert"}) || !h.fake.gates[0].DryRun {
		t.Errorf("calls %v, gates %+v", calls, h.fake.gates)
	}
}

func TestLogRevertRefused(t *testing.T) {
	h := dilHarness(t, revertSnap(t), mode(ModeReadOnly))
	showLog(h)
	dilKeys(h, "v")
	if h.app.dialog != nil || !strings.Contains(h.app.notice.text, "read-only") {
		t.Errorf("read-only: dialog %T, notice %q", h.app.dialog, h.app.notice.text)
	}
	h = dilHarness(t, ready(t))
	showLog(h)
	dilKeys(h, "v")
	if h.app.dialog != nil || !strings.HasPrefix(h.app.notice.text, "Nothing to revert") {
		t.Errorf("no run: dialog %T, notice %q", h.app.dialog, h.app.notice.text)
	}
	h = dilHarness(t, withState(revertSnap(t), session.Offline), func(o *Options) { o.Gates.AllowUntested = true })
	showLog(h)
	dilKeys(h, "v", "enter")
	if s := h.screen(80, 24); len(h.fake.Calls()) > 0 || !strings.Contains(s, "Blocked. The mouse is asleep") || !strings.Contains(h.app.notice.text, "asleep") {
		t.Errorf("offline: %v, notice %q\n%s", h.fake.Calls(), h.app.notice.text, s)
	}
}

func TestLogRevertFollowsTheJournal(t *testing.T) {
	h := dilHarness(t, revertSnap(t), func(o *Options) { o.Gates.AllowUntested = true })
	showLog(h)
	dilKeys(h, "v")
	next := revertSnap(t)
	next.Journal.Last.ID = "20260926T115500Z-4242"
	h.snap(next)
	dilKeys(h, "enter")
	h.typeText("write untested")
	dilKeys(h, "enter")
	if len(h.fake.Calls()) > 0 || !strings.Contains(h.screen(120, 40), "The journal's last change is now 20260926T115500Z-4242") {
		t.Errorf("a newer run: %v\n%s", h.fake.Calls(), h.screen(120, 40))
	}
}

func TestLogDiff(t *testing.T) {
	a := ready(t)
	b := ready(t)
	b.Policy = 1
	b.Stats.NAKs = 2
	b.Stats.Dropped = 1
	b.Progress = session.Progress{Job: "reread"}
	b.Journal = &session.JournalState{Open: []session.OpenRun{{Run: &safety.Run{ID: "r1", Kind: safety.KindApply,
		Ops: []safety.OpRecord{{Op: plan.Op{Seq: 1}}}}}}}
	var got []string
	for _, e := range logDiff(a, b, false) {
		got = append(got, e.text)
	}
	want := []string{"Reading changed records again started", "2 NAKs (2 in all)", "1 dropped report (1 in all)",
		"Journal: apply r1 is unfinished (1 record)", "Guard: edit"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if n := len(logDiff(a, a, false)); n != 0 {
		t.Errorf("no change logs %d events", n)
	}
}

// TestLogRevertEmulated edits DPI stage 2 from the DPI tab, applies it
// through a real session over an emulated EM11 Pro, and reverts it from the
// Log tab: both writes end verified in the journal, and the emulated flash
// is back to what it held.
func TestLogRevertEmulated(t *testing.T) {
	bus := emu.New(emu.Options{})
	t.Cleanup(bus.Close)
	dev, err := bus.Add(emu.Config{Mouse: &emu.Mouse{Model: em11(), Image: dumpImage(t), Firmware: emu.Version{Major: 1},
		Battery: emu.Battery{Level: 80, MilliVolts: 3900}}})
	if err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(dev.Image().Bytes())
	tmp := t.TempDir()
	s := session.New(session.Options{
		Devices: bus,
		Timing:  session.Timing{Try: 25 * time.Millisecond, ProbeTry: 25 * time.Millisecond, Online: time.Hour, Battery: time.Hour},
		Writes: &session.Writes{
			Journal:  filepath.Join(tmp, "journal"),
			Backups:  backup.Store{Root: filepath.Join(tmp, "backups"), Tool: "arcctl test", Source: backup.SourceEmulator},
			Lock:     func() error { return nil },
			Executor: safety.Options{OfflineWait: 300 * time.Millisecond, Poll: 5 * time.Millisecond},
		},
	})
	done := make(chan struct{})
	h := dilHarness(t, s.Snapshot(), func(o *Options) {
		o.Session = s
		o.Gates.AllowUntested = true
		o.Source = "emulator"
	})
	go func() {
		defer close(done)
		_ = s.Run(h.app.ctx)
	}()
	t.Cleanup(func() {
		h.app.Close()
		<-done
	})
	h.flush(func() bool { return h.app.sn.State == session.Ready && h.app.sn.Journal != nil })
	dilKeys(h, "1", "down", "right")
	p := stagedPlan(t, h)
	g := h.app.opt.Gates
	g.Confirm = safety.ConfirmPhrase(p.Ops)
	h.startWrite(applyRequest(p, g))
	h.flush(func() bool { return h.app.write == nil && h.app.notice.text != "" })
	if !strings.HasPrefix(h.app.notice.text, "Apply done: 1 of 1 records verified") {
		t.Fatalf("apply: %q", h.app.notice.text)
	}
	h.flush(func() bool {
		js := h.app.sn.Journal
		return h.app.sn.State == session.Ready && js != nil && js.Last != nil && js.Last.Kind == safety.KindApply &&
			h.app.config != nil && h.app.config.DPI[1].DPI.X == 1300
	})
	showLog(h)
	dilKeys(h, "v")
	d := rvDialog(t, h)
	if err := d.blocker(h.app.context()); err != nil || len(d.plan.Ops) != 1 {
		t.Fatalf("review: %v, %+v", err, d.plan.Ops)
	}
	dilKeys(h, "enter")
	h.flush(func() bool { return d.step == reviewConfirm })
	h.typeText("write untested")
	dilKeys(h, "enter")
	h.flush(func() bool { return h.app.write == nil && strings.HasPrefix(h.app.notice.text, "Revert") })
	if !strings.HasPrefix(h.app.notice.text, "Revert done: 1 of 1 records verified") {
		t.Fatalf("revert: %q", h.app.notice.text)
	}
	if !bytes.Equal(dev.Image().Bytes(), before) {
		t.Error("the emulated flash differs from before the apply")
	}
	dilKeys(h, "esc")
	h.flush(func() bool {
		js := h.app.sn.Journal
		return js != nil && js.Last != nil && js.Last.Kind == safety.KindRevert
	})
	last := h.app.sn.Journal.Last
	if !last.Complete || last.Ops[0].State != safety.StateVerified {
		t.Errorf("journal: %+v", last)
	}
	if s := h.screen(120, 40); !strings.Contains(s, "Revert "+last.ID+" done: 1 of 1 records verified") {
		t.Errorf("log:\n%s", s)
	}
}

func TestLogRevertReviewScrolls(t *testing.T) {
	sn := ready(t)
	var edits []mouse.Edit
	for i := range 6 {
		edits = append(edits, mouse.SetDPI{Stage: i, DPI: 1000 + 100*i})
	}
	edits = append(edits, mouse.SetStages{Count: 5})
	p, err := mouse.PlanEdits(sn.Model, sn.Image, edits, mouse.Options{Device: sn.Identity})
	if err != nil {
		t.Fatal(err)
	}
	im := sn.Image.Clone()
	run := &safety.Run{ID: "20260926T115000Z-4242", Kind: safety.KindApply, Device: sn.Identity, Started: now, Ended: true, Complete: true}
	for _, op := range p.Ops {
		if err := im.Set(op.Extent.Addr, op.New); err != nil {
			t.Fatal(err)
		}
		run.Ops = append(run.Ops, safety.OpRecord{Op: op, State: safety.StateVerified})
	}
	sn.Image, sn.Journal = im, &session.JournalState{Last: run}
	h := dilHarness(t, sn, func(o *Options) { o.Gates.AllowUntested = true })
	showLog(h)
	dilKeys(h, "v")
	h.golden("log-revert-scroll")
	dilKeys(h, "pgdown", "pgdown", "pgdown", "pgdown")
	s := h.screen(80, 24)
	if !strings.Contains(s, "5 stages → 6 stages") || !strings.Contains(s, "write untested") || strings.Contains(s, "below: pgdn") {
		t.Errorf("scrolled to the end:\n%s", s)
	}
}

// retier gives the ops of the journal's last run the tiers given, in order,
// repeating its first op.
func retier(sn *session.Snapshot, tiers ...catalog.Tier) {
	run := sn.Journal.Last
	op := run.Ops[0]
	run.Ops = nil
	for i, tier := range tiers {
		o := op
		o.Seq, o.Tier = i+1, tier
		run.Ops = append(run.Ops, o)
	}
}

// A revert is gated by every tier of the run it undoes: an Untested op
// needs --allow-untested even when an Experimental one sets the phrase.
func TestLogRevertGatesEachTier(t *testing.T) {
	sn := revertSnap(t)
	retier(sn, catalog.Untested, catalog.Experimental)
	h := dilHarness(t, sn, func(o *Options) { o.Gates.Experimental = true })
	showLog(h)
	dilKeys(h, "v")
	d := rvDialog(t, h)
	if err := d.blocker(h.app.context()); !errors.Is(err, safety.ErrUntested) {
		t.Errorf("blocker %v, want the untested gate", err)
	}
	if d.phrase != "write experimental" {
		t.Errorf("phrase %q", d.phrase)
	}
}

// U on a revert undoes that revert; the review and the key say so.
func TestLogUndoRevert(t *testing.T) {
	sn := revertSnap(t)
	sn.Journal.Last.Kind, sn.Journal.Last.Of = safety.KindRevert, "20260926T114000Z-4242"
	h := dilHarness(t, sn, func(o *Options) { o.Gates.AllowUntested = true })
	h.keys("?")
	if s := h.screen(120, 40); !strings.Contains(s, "undo last revert") {
		t.Errorf("the help does not say U undoes the revert:\n%s", s)
	}
	h.keys("esc", "U")
	rvDialog(t, h)
	rvWant(t, h, "Review: undo the last revert", "undoing it puts back what 20260926T114000Z-4242 wrote")
}

// A job cut short by a lock stopped; one the session moved on from
// finished, whatever the last snapshot of it counted; a write's end comes
// from its result.
func TestLogProgressStops(t *testing.T) {
	for _, tc := range []struct {
		job  string
		st   session.State
		want string
	}{
		{"load", session.Locked, "Loading the configuration stopped at 3 of 34: locked"},
		{"load", session.Ready, "Loading the configuration finished"},
		{"revert", session.Ready, ""},
	} {
		got := logProgress(session.Progress{Job: tc.job, Done: 3, Total: 34}, session.Progress{}, tc.st)
		if tc.want == "" && len(got) != 0 || tc.want != "" && (len(got) != 1 || got[0].text != tc.want) {
			t.Errorf("%s then %v: %+v, want %q", tc.job, tc.st, got, tc.want)
		}
	}
}
