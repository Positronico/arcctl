package safety_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

func mixedChanges(t testing.TB, f *fixture) []plan.Change {
	return slices.Concat(dpiChange(t, f.model), pairChange(t), shortcutChange(t), macroChanges(t, 5, "new", 12))
}

func TestApplyWritesAndVerifiesEveryOp(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(mixedChanges(t, f))
	want := after(t, f.image(), p)
	var events []safety.OpEvent
	res, err := f.apply(p, func(e safety.OpEvent) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified != len(p.Ops) || res.Ops != len(p.Ops) || res.Echoes != 0 || res.Run == "" {
		t.Fatalf("result %+v for %d ops", res, len(p.Ops))
	}
	if d := differ(f.image(), want, p); len(d) > 0 {
		t.Fatalf("extents %v differ from the plan", d)
	}
	f.checkOutside(p)
	checkBindings(t, f.image())

	var kinds []safety.EventKind
	for _, e := range events {
		if e.Run != res.Run {
			t.Fatalf("event for run %q, want %q", e.Run, res.Run)
		}
		if e.Kind != safety.EventChunk {
			kinds = append(kinds, e.Kind)
		}
	}
	var want2 []safety.EventKind
	for range p.Ops {
		want2 = append(want2, safety.EventStart, safety.EventWritten, safety.EventVerified)
	}
	if !slices.Equal(kinds, want2) {
		t.Fatalf("events %v, want %v", kinds, want2)
	}
	chunks := 0
	for _, e := range events {
		if e.Kind == safety.EventChunk {
			chunks++
		}
	}
	if chunks != chunksOf(p) {
		t.Fatalf("%d chunk events, want %d", chunks, chunksOf(p))
	}

	st := f.status()
	if !st.Clean() || st.Last == nil || st.Last.ID != res.Run || !st.Last.Complete {
		t.Fatalf("status: open %v, last %+v", st.Open, st.Last)
	}
	for _, o := range st.Last.Ops {
		if o.State != safety.StateVerified {
			t.Fatalf("op %d is %s", o.Seq, o.State)
		}
	}
}

// Writes go out in chunks of at most 10 bytes, each record is read back in
// full, and every record gets its own fresh cmd 3 first.
func TestApplyPacketSequence(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 12))
	if len(p.Ops) != 3 || p.Ops[0].Phase != plan.Neutralise || p.Ops[1].Phase != plan.Body || p.Ops[2].Phase != plan.Bind {
		t.Fatalf("plan is not the two-phase trio: %+v", p.Ops)
	}
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range f.dev.Writes() {
		switch w.Packet.Cmd() {
		case wire.CmdOnline:
			got = append(got, "online")
		case wire.CmdWrite, wire.CmdRead:
			if w.Packet.Len() > wire.MaxData {
				t.Fatalf("packet of %d bytes", w.Packet.Len())
			}
			got = append(got, w.Packet.Cmd().String())
		}
	}
	var want []string
	for _, op := range p.Ops {
		want = append(want, "online")
		n := (op.Extent.Len + 9) / 10
		for range n {
			want = append(want, wire.CmdWrite.String())
		}
		for range n {
			want = append(want, wire.CmdRead.String())
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("packets\n%v\nwant\n%v", got, want)
	}
}

// The journal holds each op's old and new bytes, and the op as sending,
// before the op's first packet (I6).
func TestJournalBeforeFirstPacket(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(mixedChanges(t, f))
	first := map[int]int{}
	n := 0
	for i, op := range p.Ops {
		first[n+1] = i
		n += (op.Extent.Len + 9) / 10
	}
	checked := 0
	f.link.beforeWrite = func(n int, pk wire.Packet) {
		i, ok := first[n]
		if !ok {
			return
		}
		st, err := safety.Load(f.j.Dir())
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Open) != 1 {
			t.Fatalf("before op %d: %d open runs on disk", i+1, len(st.Open))
		}
		for k, o := range st.Open[0].Ops {
			want := safety.StatePlanned
			switch {
			case k < i:
				want = safety.StateVerified
			case k == i:
				want = safety.StateSending
			}
			if o.State != want || !bytes.Equal(o.Old, p.Ops[k].Old) || !bytes.Equal(o.New, p.Ops[k].New) || o.Extent != p.Ops[k].Extent {
				t.Fatalf("before op %d, journal op %d is %s with old % x new % x", i+1, k+1, o.State, o.Old, o.New)
			}
		}
		checked++
	}
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	if checked != len(p.Ops) {
		t.Fatalf("checked %d ops, want %d", checked, len(p.Ops))
	}
}

// The cmd-7 echo is only logged: a device that echoes just the header still
// verifies by read-back (I4).
func TestEchoIsOnlyLogged(t *testing.T) {
	for _, bh := range []emu.Behavior{{Echo: emu.EchoHeader}, {DoubleWrite: true}} {
		f := newFixture(t, setup{behavior: bh})
		p := f.plan(shortcutChange(t))
		res, err := f.apply(p, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if bh.Echo == emu.EchoHeader {
			want = chunksOf(p)
		}
		if res.Echoes != want || res.Verified != len(p.Ops) {
			t.Fatalf("%+v: result %+v, want %d echo mismatches", bh, res, want)
		}
	}
}

// A DPI press while the current stage is written changes the byte under the
// write: the read-back catches it and the run stops (H9's push overlap).
func TestReadBackMismatchStops(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(pairChange(t), dpiChange(t, f.model)))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 1 {
			if err := f.dev.PressDPI(); err != nil {
				t.Fatal(err)
			}
			f.link.awaitPush(t)
		}
	}
	var failed []safety.OpEvent
	_, err := f.apply(p, func(e safety.OpEvent) {
		if e.Kind == safety.EventFailed {
			failed = append(failed, e)
		}
	})
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrMismatch) || !st.Written || st.Class != safety.ClassTorn || st.Op.Seq != 1 {
		t.Fatalf("stop %+v", st)
	}
	if len(failed) != 1 || !bytes.Equal(failed[0].Found, st.Found) {
		t.Fatalf("failed events %+v", failed)
	}
	run := f.status().Open
	if len(run) != 1 || run[0].Ops[0].State != safety.StateFailed || run[0].Ops[0].Class != safety.ClassTorn ||
		!bytes.Equal(run[0].Ops[0].Found, st.Found) || run[0].Complete || !run[0].Ended {
		t.Fatalf("journal run %+v", run)
	}
}

func TestProfileSwitchStopsBeforeNextPacket(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 2 {
			if err := f.dev.SwitchProfile(1); err != nil {
				t.Fatal(err)
			}
			f.link.awaitPush(t)
		}
	}
	_, err := f.apply(p, nil)
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrProfile) || st.Chunks != 1 || st.Class != safety.ClassUnknown {
		t.Fatalf("stop %+v", st)
	}
	writes := 0
	for _, w := range f.dev.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			writes++
		}
	}
	if writes != 2 {
		t.Fatalf("%d cmd 7 went out, want 2", writes)
	}
	if n := len(f.dev.Writes()); f.dev.Writes()[n-1].Packet.Cmd() != wire.CmdWrite {
		t.Fatalf("a packet went out after the profile switch: %v", f.dev.Writes()[n-1].Packet)
	}
	// Recovery refuses the other profile (I11) until the mouse is back on it.
	f.profile = 1
	if _, err := f.x.Recover(context.Background(), f.status().Open[0], safety.Forward, f.device(), nil); !errors.Is(err, safety.ErrProfile) {
		t.Fatalf("Recover on profile 1 = %v, want ErrProfile", err)
	}
	if err := f.dev.SwitchProfile(0); err != nil {
		t.Fatal(err)
	}
	f.link.awaitPush(t)
	f.link.Pending()
	f.profile = 0
	if _, err := f.x.Recover(context.Background(), f.status().Open[0], safety.Forward, f.device(), nil); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), after(t, f.start, p), p); len(d) > 0 {
		t.Fatalf("extents %v are not the plan's", d)
	}
}

func TestAbortStopsAfterTheCurrentOp(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro})
	p := f.plan(macroChanges(t, 5, "new", 30))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 3 {
			f.link.abort()
		}
	}
	res, err := f.apply(p, nil)
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrAborted) || st.Written || st.Op.Seq != 3 || res.Verified != 2 {
		t.Fatalf("stop %+v after %d verified", st, res.Verified)
	}
	checkBindings(t, f.image())
	in := must[*safety.Inspection](t)(f.x.Inspect(context.Background(), f.status().Open[0], f.device()))
	if in.Torn() {
		t.Fatalf("inspection %+v", in.Extents)
	}
	// Body written, binding still disabled: the binding extent is in between.
	var classes []safety.Class
	for _, e := range in.Extents {
		classes = append(classes, e.Class)
	}
	if !slices.Equal(classes, []safety.Class{safety.ClassMid, safety.ClassNew}) {
		t.Fatalf("extent classes %v", classes)
	}
}

// A push that re-reads a range the plan has yet to write stops the run
// after the current op (I7).
func TestOverlappingPushStopsAfterCurrentOp(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 1 {
			f.dev.Push(0x01, 0)
			f.link.awaitPush(t)
		}
	}
	res, err := f.apply(p, nil)
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrOverlap) || res.Verified != 1 || st.Op.Extent.Addr != mouse.AddrCurrentDPI || st.Written {
		t.Fatalf("stop %+v after %d verified", st, res.Verified)
	}
}

// A push for a range the plan does not write changes nothing.
func TestUnrelatedPushIsIgnored(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 1 {
			f.dev.Push(0x01, 0)
			f.link.awaitPush(t)
		}
	}
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
}

// The mouse falls asleep during a record: the run pauses, a cmd 3 finds it
// awake again, and the record is written again from its first chunk.
func TestSleepPausesAndRestartsTheRecord(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	f.dev.Inject(emu.Fault{Cmd: wire.CmdWrite, Skip: 2, Times: 1, Action: emu.Asleep})
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 3 {
			f.dev.Wake()
		}
	}
	var kinds []safety.EventKind
	res, err := f.apply(p, func(e safety.OpEvent) {
		if e.Kind != safety.EventChunk {
			kinds = append(kinds, e.Kind)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []safety.EventKind{
		safety.EventStart, safety.EventWritten, safety.EventVerified,
		safety.EventStart, safety.EventPaused, safety.EventResumed, safety.EventRestart, safety.EventWritten, safety.EventVerified,
		safety.EventStart, safety.EventWritten, safety.EventVerified,
	}
	if !slices.Equal(kinds, want) || res.Verified != 3 {
		t.Fatalf("events %v\nwant %v", kinds, want)
	}
	if d := differ(f.image(), after(t, f.start, p), p); len(d) > 0 {
		t.Fatalf("extents %v are not the plan's", d)
	}
}

// A mouse that stays asleep ends the run once OfflineWait is over.
func TestSleepThatLastsStops(t *testing.T) {
	f := newFixture(t, setup{})
	opt := fastOptions()
	opt.OfflineWait = 50 * time.Millisecond
	f.x = safety.NewExecutor(f.link, f.j, opt)
	p := f.plan(pairChange(t))
	f.dev.Sleep()
	_, err := f.apply(p, nil)
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrOffline) || st.Written {
		t.Fatalf("stop %+v", st)
	}
	if len(f.dev.Writes()) == 0 || slices.ContainsFunc(f.dev.Writes(), func(w emu.Write) bool { return w.Packet.Cmd() == wire.CmdWrite }) {
		t.Fatal("a record was written to a sleeping mouse")
	}
}

// A locked screen pauses the run until it unlocks, then the record starts
// again from chunk 0.
func TestLockedScreenPauses(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 2 {
			f.dev.SetLocked(true)
		}
	}
	locked := 0
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, emu.ErrLocked) {
			if locked++; locked == 3 {
				f.dev.SetLocked(false)
			}
		}
	}
	res, err := f.apply(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if locked < 3 || res.Verified != len(p.Ops) {
		t.Fatalf("locked checks %d, result %+v", locked, res)
	}
}

// While the run waited for the mouse, the device changed a record the plan
// has yet to write: the run stops instead of writing over it.
func TestPauseRechecksPendingExtents(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.dev.Inject(emu.Fault{Cmd: wire.CmdWrite, Times: 1, Action: emu.Asleep})
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 2 {
			if err := f.dev.PressDPI(); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := f.apply(p, nil)
	if !errors.Is(err, safety.ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
}

func TestCancelStopsAtOnce(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	ctx, cancel := context.WithCancel(context.Background())
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 2 {
			cancel()
		}
	}
	_, err := f.x.Apply(ctx, p, f.device(), nil)
	st := stopError(t, err)
	if !errors.Is(err, context.Canceled) || st.Chunks != 1 || !st.Written {
		t.Fatalf("stop %+v", st)
	}
	if n := len(f.dev.Writes()); f.dev.Writes()[n-1].Packet.Cmd() != wire.CmdWrite {
		t.Fatal("a packet went out after the cancel")
	}
}

// The executor refuses plans that do not match the device, its profile, the
// journal or the image, before any packet.
func TestApplyRefusesMismatchedPlans(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	other := identity
	other.PID = 0x1283
	stale := f.device()
	_ = stale.Image.Set(mouse.AddrCurrentDPI, []byte{0x01, 0x54})
	trio := f.plan(shortcutChange(t))
	bodyFirst := trio
	bodyFirst.Ops = []plan.Op{trio.Ops[1]}
	bodyFirst.Ops[0].Seq = 1
	tests := []struct {
		name string
		p    plan.Plan
		d    safety.Device
		want error
	}{
		{"other profile", p, func() safety.Device { d := f.device(); d.Profile = ptr(byte(1)); return d }(), safety.ErrProfile},
		{"no profile", p, func() safety.Device { d := f.device(); d.Profile = nil; return d }(), safety.ErrProfile},
		{"other device", p, func() safety.Device { d := f.device(); d.Identity = other; return d }(), safety.ErrIdentity},
		{"plan for another device", func() plan.Plan { q := p; q.Device = other; return q }(), f.device(), safety.ErrIdentity},
		{"stale image", p, stale, plan.ErrStale},
		{"body under its binding", bodyFirst, f.device(), plan.ErrBinding},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.x.Apply(context.Background(), tt.p, tt.d, nil)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if len(f.dev.Writes()) != 0 {
				t.Fatal("packets went out")
			}
			if st := f.status(); len(st.Runs) != 0 {
				t.Fatal("a refused plan was journaled")
			}
		})
	}
	if res, err := f.x.Apply(context.Background(), plan.Plan{Device: identity, Profile: ptr(byte(0))}, f.device(), nil); err != nil || res.Run != "" {
		t.Fatalf("empty plan: %+v, %v", res, err)
	}
}

// Packets the transport failed to send are sent again; the read-back still
// decides.
func TestTransientWriteErrorsAreResent(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	f.dev.Inject(emu.Fault{Cmd: wire.CmdWrite, Skip: 1, Times: 6, Action: emu.Fail})
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), after(t, f.start, p), p); len(d) > 0 {
		t.Fatalf("extents %v are not the plan's", d)
	}
}

func cmd7(f *fixture) int {
	n := 0
	for _, w := range f.dev.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			n++
		}
	}
	return n
}

// After a pause the link runs its own checks again before the next packet
// (§6.4 step 2): a client that opened the receiver while the mouse slept
// stops the run there (I10).
func TestPauseRunsTheLinkChecksAgain(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	blocked := fmt.Errorf("%w: Chrome (pid 4242)", safety.ErrForeignClient)
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 1 {
			f.link.recheck = func() error { return blocked }
			f.dev.Wake()
		}
	}
	sent := 0
	_, err := f.apply(p, func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			sent = cmd7(f)
			f.dev.Sleep()
		}
	})
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrForeignClient) || st.Written || st.Op.Seq != 2 || cmd7(f) != sent || f.link.rechecks == 0 {
		t.Fatalf("stop %+v, %d cmd 7 after the pause, %d rechecks", st, cmd7(f)-sent, f.link.rechecks)
	}
}

// The same checks run when the pause comes in the middle of a record; the
// record already written in part is left for recovery.
func TestPauseInARecordRunsTheLinkChecksAgain(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(shortcutChange(t))
	f.dev.Inject(emu.Fault{Cmd: wire.CmdWrite, Skip: 2, Times: 1, Action: emu.Asleep})
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 1 {
			f.link.recheck = func() error { return fmt.Errorf("%w: test", safety.ErrSecureInput) }
			f.dev.Wake()
		}
	}
	sent := 0
	_, err := f.apply(p, func(e safety.OpEvent) {
		if e.Kind == safety.EventPaused {
			sent = cmd7(f)
		}
	})
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrSecureInput) || !st.Written || sent == 0 || cmd7(f) != sent {
		t.Fatalf("stop %+v, %d cmd 7 after the pause", st, cmd7(f)-sent)
	}
	if st := f.status(); len(st.Open) != 1 {
		t.Fatalf("open runs %+v", st.Open)
	}
}

// An abort that arrives while the run waits for the mouse, before any packet
// of the next op, stops the run there: the op the user aborted is never
// written.
func TestAbortWhilePausedBeforeTheNextOp(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 1 {
			f.link.abort()
		}
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 3 {
			f.dev.Wake()
		}
	}
	sent := 0
	res, err := f.apply(p, func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			sent = cmd7(f)
			f.dev.Sleep()
		}
	})
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrAborted) || st.Written || st.Op.Seq != 2 || cmd7(f) != sent || res.Verified != 1 {
		t.Fatalf("stop %+v, %d cmd 7 of op 2, %d verified", st, cmd7(f)-sent, res.Verified)
	}
}

// The same abort does not wait for a mouse that stays asleep: nothing of the
// op went out, so the run stops at once.
func TestAbortWhilePausedDoesNotWaitForTheMouse(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 1 {
			f.link.abort()
		}
	}
	var paused time.Time
	_, err := f.apply(p, func(e safety.OpEvent) {
		switch {
		case e.Kind == safety.EventVerified && e.Op.Seq == 1:
			f.dev.Sleep()
		case e.Kind == safety.EventPaused:
			paused = time.Now()
		}
	})
	waited := time.Since(paused)
	if !errors.Is(err, safety.ErrAborted) || waited > fastOptions().OfflineWait/2 {
		t.Fatalf("err %v after waiting %s for the mouse; want the abort at once", err, waited)
	}
}

// A push that re-reads the next op's range after its fresh cmd 3 and before
// its first packet stops the run: the op was planned on bytes the device no
// longer holds (I7). The run sent nothing, so nothing is left to recover.
func TestPushBeforeTheFirstPacketStops(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	f.link.onCheck = func(n int, err error) {
		if n == 1 && err == nil {
			if err := f.dev.PressDPI(); err != nil {
				t.Error(err)
			}
			f.link.awaitPush(t)
		}
	}
	_, err := f.apply(p, nil)
	st := stopError(t, err)
	if !errors.Is(err, safety.ErrOverlap) || st.Written || cmd7(f) != 0 {
		t.Fatalf("stop %+v with %d cmd 7", st, cmd7(f))
	}
	if b, _ := f.image().Get(p.Ops[0].Extent); bytes.Equal(b, p.Ops[0].New) || bytes.Equal(b, p.Ops[0].Old) {
		t.Fatalf("the current stage holds % x, not the press", b)
	}
	if st := f.status(); !st.Clean() || st.Last != nil {
		t.Fatalf("a run that sent nothing is open %+v or revertible %+v", st.Open, st.Last)
	}
}

// A push that re-reads the last op's range while it is written cannot stop
// the run after that op, since none follows; the result says so.
func TestPushDuringTheLastOpIsReported(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.link.afterWrite = func(n int, _ wire.Packet) {
		if n == 2 {
			f.dev.Push(0x01, 0)
			f.link.awaitPush(t)
		}
	}
	res, err := f.apply(p, nil)
	if err != nil || !res.Overlap || res.Verified != 2 {
		t.Fatalf("result %+v, err %v", res, err)
	}
}

// The mouse sleeps before the next op's first packet and, while it sleeps,
// the record that op writes changes with no push to say so: the resume reads
// that record again too.
func TestPauseBeforeTheFirstPacketRechecksTheCurrentOp(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(slices.Concat(dpiChange(t, f.model), pairChange(t)))
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) && f.link.offline == 1 {
			if err := f.dev.Store(mouse.AddrCurrentDPI, []byte{0x04, 0x51}); err != nil {
				t.Error(err)
			}
			f.dev.Wake()
		}
	}
	sent := 0
	_, err := f.apply(p, func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified && e.Op.Seq == 1 {
			sent = cmd7(f)
			f.dev.Sleep()
		}
	})
	if !errors.Is(err, safety.ErrChanged) || cmd7(f) != sent {
		t.Fatalf("op 2's record changed during the pause: err %v, %d cmd 7 after it", err, cmd7(f)-sent)
	}
}

// A mouse that falls asleep at every attempt makes the op start again at
// most Options.Restarts times.
func TestRestartsAreBounded(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	f.dev.Inject(emu.Fault{Cmd: wire.CmdWrite, Action: emu.Asleep})
	f.link.onCheck = func(_ int, err error) {
		if errors.Is(err, safety.ErrOffline) {
			f.dev.Wake()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	restarts := 0
	_, err := f.x.Apply(ctx, p, f.device(), func(e safety.OpEvent) {
		if e.Kind == safety.EventRestart {
			restarts++
		}
	})
	if errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, safety.ErrOffline) || restarts != fastOptions().Restarts {
		t.Fatalf("%d restarts, err %v; want the run to stop after %d", restarts, err, fastOptions().Restarts)
	}
}
