package safety_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

var everyChunk = flag.Bool("every-chunk", false, "run the session fault matrix at every chunk, not a sample")

type outcome uint8

const (
	completes outcome = iota + 1 // every op is verified
	stops                        // the run stops with a *safety.StopError
	either                       // whichever the seed gives
	dies                         // the process dies in the middle of the run
)

var outcomeNames = [...]string{"", "completes", "stops", "either", "dies"}

func (o outcome) String() string { return outcomeNames[o] }

func always(o outcome) func(plan.Plan, int) outcome {
	return func(plan.Plan, int) outcome { return o }
}

func classIs(c hidio.Class) func(error) bool {
	return func(err error) bool { return hidio.Classify(err) == c }
}

func is(target error) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) }
}

// faultCase injects one fault at the k-th cmd 7 (from 1) of a plan the
// executor writes over the test link. inject may return what the session or
// the user does about the fault before recovery.
type faultCase struct {
	name     string
	behavior emu.Behavior
	watchdog time.Duration
	inject   func(t testing.TB, f *fixture, p plan.Plan, k int) (mend func())
	outcome  func(p plan.Plan, k int) outcome
	cause    func(err error) bool
	pauses   bool // the run must pause and write the record again
	reads    bool // the stop must read the extent back and classify it
	// garbles: the stopped op may leave its binding undecodable.
	garbles bool
	// upTo is the last chunk that may reach the device before the run
	// stops, when the fault must stop it that early.
	upTo func(p plan.Plan, k int) int
	// changes is what the fault itself changes on the device, which no op
	// of the plan leaves there.
	changes func(p plan.Plan, k int) (flash.Extent, []byte, bool)
}

func faultAt(flt emu.Fault) func(testing.TB, *fixture, plan.Plan, int) func() {
	return func(_ testing.TB, f *fixture, _ plan.Plan, k int) func() {
		g := flt
		g.Match = emu.Nth(wire.CmdWrite, k)
		f.dev.Inject(g)
		return nil
	}
}

// readBackAt applies flt to the k-th cmd 8 of the run: the read-back of the
// k-th chunk, when the run has not paused.
func readBackAt(flt emu.Fault) func(testing.TB, *fixture, plan.Plan, int) func() {
	return func(_ testing.TB, f *fixture, _ plan.Plan, k int) func() {
		g := flt
		g.Match = emu.Nth(wire.CmdRead, k)
		f.dev.Inject(g)
		return nil
	}
}

// before runs do after the reply to the (k-1)-th cmd 7, or before the run
// for the first.
func before(do func(testing.TB, *fixture)) func(testing.TB, *fixture, plan.Plan, int) func() {
	return func(t testing.TB, f *fixture, _ plan.Plan, k int) func() {
		if k == 1 {
			do(t, f)
			return nil
		}
		f.link.afterWrite = func(n int, _ wire.Packet) {
			if n == k-1 {
				do(t, f)
			}
		}
		return nil
	}
}

// staleAck delivers, while the k-th cmd 7 waits for its reply, an ack the
// device sent before: the previous chunk's, or one for the k-th chunk's own
// address, from an earlier op of the run when there was one, else carrying
// the bytes the address held before the plan.
func staleAck(sameAddr bool) func(testing.TB, *fixture, plan.Plan, int) func() {
	return func(_ testing.TB, f *fixture, p plan.Plan, k int) func() {
		stale := staleOf(p, k, sameAddr)
		f.link.beforeWrite = func(n int, req wire.Packet) {
			if n != k {
				return
			}
			if prev, ok := f.link.lastReply(req.Addr()); ok && sameAddr {
				stale = prev
			}
			f.dev.Deliver(stale)
		}
		return nil
	}
}

func staleOf(p plan.Plan, k int, sameAddr bool) wire.Packet {
	if sameAddr || k == 1 {
		return chunkPacket(p, k, true)
	}
	return chunkPacket(p, k-1, false)
}

// chunkPacket is the k-th cmd 7 of p, which the device echoes as its ack,
// with the op's old bytes in place of its new ones when old is set.
func chunkPacket(p plan.Plan, k int, old bool) wire.Packet {
	i, c := chunkOp(p, k)
	op := p.Ops[i]
	b := op.New
	if old {
		b = op.Old
	}
	off := (c - 1) * wire.MaxData
	return wire.MustBuild(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr+off), b[off:min(off+wire.MaxData, len(b))])
}

// endOf is the last chunk of the op that holds the k-th chunk; 0 for k 0.
func endOf(p plan.Plan, k int) int {
	if k < 1 {
		return 0
	}
	i, c := chunkOp(p, k)
	return k - c + (p.Ops[i].Extent.Len+wire.MaxData-1)/wire.MaxData
}

// unchanged reports whether the k-th chunk of p writes the bytes its
// address held before its op.
func unchanged(p plan.Plan, k int) bool {
	i, c := chunkOp(p, k)
	op := p.Ops[i]
	off := (c - 1) * wire.MaxData
	end := min(off+wire.MaxData, len(op.New))
	return slices.Equal(op.New[off:end], op.Old[off:end])
}

// laterChange is what "change while asleep" changes: the extent of the first
// op after the k-th chunk's op that writes another extent, set to bytes that
// differ from what that op expects and leave every binding valid: Scroll Up
// for a binding, the op's new bytes otherwise.
func laterChange(p plan.Plan, k int) (flash.Extent, []byte, bool) {
	i, _ := chunkOp(p, k)
	for _, op := range p.Ops[i+1:] {
		switch {
		case op.Extent.Overlaps(p.Ops[i].Extent):
		case op.Phase == plan.Bind || op.Phase == plan.Neutralise:
			return op.Extent, scrollUp, true
		default:
			return op.Extent, op.New, true
		}
	}
	return flash.Extent{}, nil, false
}

var faultCases = []faultCase{
	{
		name:    "write error",
		inject:  faultAt(emu.Fault{Times: 3, Action: emu.Fail}),
		outcome: always(completes),
	},
	{
		name:    "write errors",
		inject:  faultAt(emu.Fault{Times: 3 * (1 + 2), Action: emu.Fail}),
		outcome: always(stops),
		cause:   classIs(hidio.ClassRetry),
		reads:   true,
	},
	{
		name:    "timeouts absorbed",
		inject:  faultAt(emu.Fault{Times: tries - 1, Action: emu.Drop}),
		outcome: always(completes),
	},
	{
		name:    "timeouts",
		inject:  faultAt(emu.Fault{Times: tries, Action: emu.Drop}),
		outcome: always(stops),
		cause:   is(safety.ErrTimeout),
		reads:   true,
	},
	{
		name:    "nak",
		inject:  faultAt(emu.Fault{Times: 1, Action: emu.NAK}),
		outcome: always(stops),
		cause:   is(wire.ErrNAK),
		reads:   true,
	},
	{
		name: "disconnect",
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			faultAt(emu.Fault{Times: 1, Action: emu.Unplug})(t, f, p, k)
			return func() { f.dev.Plug(); f.reconnect() }
		},
		outcome: always(stops),
		cause:   classIs(hidio.ClassGone),
	},
	{
		name: "lock",
		inject: func(_ testing.TB, f *fixture, _ plan.Plan, k int) func() {
			f.link.beforeWrite = func(n int, _ wire.Packet) {
				if n == k {
					f.dev.SetLocked(true)
				}
			}
			locked := 0
			f.link.onCheck = func(_ int, err error) {
				if hidio.Classify(err) == hidio.ClassLocked {
					if locked++; locked == 2 {
						f.dev.SetLocked(false)
					}
				}
			}
			return nil
		},
		outcome: always(completes),
		pauses:  true,
	},
	{
		name:     "stall",
		watchdog: 100 * time.Millisecond,
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			faultAt(emu.Fault{Times: 1, Action: emu.Hang})(t, f, p, k)
			return func() { f.link.tr.Close(); f.dev.Release(); f.reconnect() }
		},
		outcome: always(stops),
		cause:   classIs(hidio.ClassStalled),
	},
	{
		name: "kill",
		inject: func(_ testing.TB, f *fixture, _ plan.Plan, k int) func() {
			f.link.afterWrite = func(n int, _ wire.Packet) {
				if n == k {
					panic(errKilled)
				}
			}
			return f.restart
		},
		outcome: always(dies),
	},
	{
		name: "profile switch",
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			before(func(t testing.TB, f *fixture) {
				if err := f.dev.SwitchProfile(1); err != nil {
					t.Fatal(err)
				}
				f.link.awaitPush(t)
			})(t, f, p, k)
			return func() {
				if err := f.dev.SwitchProfile(0); err != nil {
					t.Fatal(err)
				}
				f.link.awaitPush(t)
				f.link.Pending()
			}
		},
		outcome: always(stops),
		cause:   is(safety.ErrProfile),
		upTo:    func(_ plan.Plan, k int) int { return k - 1 },
	},
	{
		name:    "stale ack of the previous chunk",
		inject:  staleAck(false),
		outcome: always(completes),
	},
	{
		name:    "stale ack of this address",
		inject:  staleAck(true),
		outcome: always(completes),
	},
	{
		name:    "duplicate ack",
		inject:  faultAt(emu.Fault{Times: 1, Action: emu.Duplicate}),
		outcome: always(completes),
	},
	{
		name: "mouse sleep",
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			faultAt(emu.Fault{Times: 1, Action: emu.Asleep})(t, f, p, k)
			f.link.onCheck = func(_ int, err error) {
				if errors.Is(err, safety.ErrOffline) && f.link.offline >= 2 {
					f.dev.Wake()
				}
			}
			return nil
		},
		outcome: always(completes),
		pauses:  true,
	},
	{
		name:     "reply stealing",
		behavior: emu.Behavior{Sharing: emu.Steal},
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			var c *emu.Competitor
			before(func(testing.TB, *fixture) { c = f.dev.Chrome(0); c.Poll() })(t, f, p, k)
			return func() {
				if c != nil {
					c.Close()
				}
			}
		},
		outcome: always(either),
	},
	{
		name:    "late reply",
		inject:  faultAt(emu.Fault{Times: 1, Action: emu.Late, Delay: tryWait * 3 / 2}),
		outcome: always(completes),
	},
	{
		name:    "read-back timeouts",
		inject:  readBackAt(emu.Fault{Times: tries, Action: emu.Drop}),
		outcome: always(stops),
		cause:   is(safety.ErrTimeout),
		reads:   true,
	},
	{
		name:    "read-back nak",
		inject:  readBackAt(emu.Fault{Times: 1, Action: emu.NAK}),
		outcome: always(stops),
		cause:   is(wire.ErrNAK),
		reads:   true,
	},
	{
		name: "read-back sleep",
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			readBackAt(emu.Fault{Times: 1, Action: emu.Asleep})(t, f, p, k)
			f.link.onCheck = func(_ int, err error) {
				if errors.Is(err, safety.ErrOffline) && f.link.offline >= 2 {
					f.dev.Wake()
				}
			}
			return nil
		},
		outcome: always(completes),
		pauses:  true,
	},
	{
		name: "abort",
		inject: func(_ testing.TB, f *fixture, _ plan.Plan, k int) func() {
			f.link.afterWrite = func(n int, _ wire.Packet) {
				if n == k {
					f.link.abort()
				}
			}
			return nil
		},
		outcome: func(p plan.Plan, k int) outcome {
			if i, _ := chunkOp(p, k); i < len(p.Ops)-1 {
				return stops
			}
			return completes
		},
		cause: is(safety.ErrAborted),
		upTo:  endOf,
	},
	{
		name:   "push",
		inject: before(func(t testing.TB, f *fixture) { f.dev.Push(0x01, 0); f.link.awaitPush(t) }),
		outcome: func(p plan.Plan, _ int) outcome {
			if slices.ContainsFunc(p.Ops, func(o plan.Op) bool { return o.Extent.Overlaps(flash.Extent{Addr: 4, Len: 2}) }) {
				return stops
			}
			return completes
		},
		cause: is(safety.ErrOverlap),
		upTo:  func(p plan.Plan, k int) int { return endOf(p, k-1) },
	},
	{
		name:   "ignored write",
		inject: faultAt(emu.Fault{Action: emu.Ignore}),
		outcome: func(p plan.Plan, k int) outcome {
			if unchanged(p, k) {
				return completes
			}
			return stops
		},
		cause: is(safety.ErrMismatch),
		reads: true,
		upTo:  endOf,
	},
	{
		name:    "corrupted write",
		inject:  faultAt(emu.Fault{Action: emu.Corrupt}),
		outcome: always(stops),
		cause:   is(safety.ErrMismatch),
		reads:   true,
		garbles: true,
		upTo:    endOf,
	},
	{
		name: "change while asleep",
		inject: func(t testing.TB, f *fixture, p plan.Plan, k int) func() {
			faultAt(emu.Fault{Times: 1, Action: emu.Asleep})(t, f, p, k)
			e, b, ok := laterChange(p, k)
			changed := false
			f.link.onCheck = func(_ int, err error) {
				if errors.Is(err, safety.ErrOffline) && !changed {
					changed = true
					if ok {
						if err := f.dev.Store(e.Addr, b); err != nil {
							t.Error(err)
						}
					}
					f.dev.Wake()
				}
			}
			return nil
		},
		outcome: func(p plan.Plan, k int) outcome {
			if _, _, ok := laterChange(p, k); ok {
				return stops
			}
			return completes
		},
		cause:   is(safety.ErrChanged),
		pauses:  true,
		upTo:    func(_ plan.Plan, k int) int { return k },
		changes: laterChange,
	},
}

// TestFaultMatrix injects every fault at every chunk of every op kind into
// the executor, over the test link (PLAN §10 item 6). A run that goes on
// must leave the device holding exactly the plan. A run that stops must leave
// every binding running a valid body, every extent but the stopped one old,
// new or in between, and nothing outside the plan written. Journal recovery,
// forward or back, must then leave the whole device image exactly as it was
// before the plan or exactly as the plan meant, with every record valid.
func TestFaultMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("fault matrix")
	}
	for ki, kind := range matrixKinds {
		probe := newFixture(t, setup{seed: kind.seed})
		n := chunksOf(probe.plan(kind.changes(t, probe.model)))
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			for fi, fc := range faultCases {
				t.Run(fc.name, func(t *testing.T) {
					t.Parallel()
					for k := 1; k <= n; k++ {
						t.Run(fmt.Sprint(k), func(t *testing.T) {
							t.Parallel()
							runFault(t, kind, fc, k, uint64(ki*10000+fi*100+k))
						})
					}
				})
			}
		})
	}
}

func newMatrixFixture(t testing.TB, kind opKind, fc faultCase, seed uint64) *fixture {
	t.Helper()
	m, ok := catalog.ByKey("7B04")
	if !ok {
		t.Fatal("no EM11 Pro in the catalog")
	}
	im := dumpImage(t)
	if kind.seed != nil {
		kind.seed(t, im)
	}
	b := emu.New(emu.Options{Seed: seed, Watchdog: fc.watchdog})
	t.Cleanup(b.Close)
	d, err := b.Add(em11(m, im, fc.behavior))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, bus: b, dev: d, model: m, root: t.TempDir(), noSync: true}
	f.start = f.image()
	f.restart()
	return f
}

func em11(m *catalog.Model, im *flash.Image, bh emu.Behavior) emu.Config {
	return emu.Config{
		RxVersion: &emu.Version{Major: 1, Minor: 2},
		Behavior:  bh,
		Mouse: &emu.Mouse{
			Model: m, Image: im, Firmware: emu.Version{Major: 1, Minor: 5},
			Battery: emu.Battery{Level: 80, MilliVolts: 3900}, Profile: ptr(byte(0)), LongRange: ptr(true),
		},
	}
}

func runFault(t *testing.T, kind opKind, fc faultCase, k int, seed uint64) {
	f := newMatrixFixture(t, kind, fc, seed)
	p := f.plan(kind.changes(t, f.model))
	pre := f.image()
	post := after(t, pre, p)
	checkBound(t, f.model, pre)
	mend := fc.inject(t, f, p, k)

	var res safety.Result
	var err error
	restarts := 0
	killed := func() (dead bool) {
		defer func() {
			if r := recover(); r != nil {
				if r != errKilled {
					panic(r)
				}
				dead = true
			}
		}()
		res, err = f.apply(p, func(e safety.OpEvent) {
			if e.Kind == safety.EventRestart {
				restarts++
			}
		})
		return false
	}()

	want := fc.outcome(p, k)
	switch {
	case (want == dies) != killed:
		t.Fatalf("killed %v, err %v; want the run to %v", killed, err, want)
	case want == completes && err != nil:
		t.Fatalf("run stopped: %v", err)
	case want == stops && err == nil:
		t.Fatal("run went on")
	}
	checkFreshOnline(t, f.dev.Writes(), p)
	if err != nil && fc.upTo != nil {
		noChunkAfter(t, f.dev.Writes(), p, fc.upTo(p, k))
	}
	if err == nil && !killed {
		if res.Verified != len(p.Ops) || fc.pauses && restarts == 0 {
			t.Fatalf("result %+v, %d restarts", res, restarts)
		}
		sameImage(t, "device after the run", f.image(), post)
		checkRecords(t, f.model, f.image(), p)
		checkBound(t, f.model, f.image())
		f.checkOutside(p)
		if st := f.status(); !st.Clean() {
			t.Fatalf("journal has open runs: %+v", st.Open)
		}
		return
	}

	stopped := opAt(p, k)
	if !killed {
		st := stopError(t, err)
		if fc.cause != nil && !fc.cause(err) {
			t.Fatalf("stopped with %v", err)
		}
		if fc.reads && (st.Class == safety.ClassUnknown || len(st.Found) != st.Op.Extent.Len) {
			t.Fatalf("stop %+v did not classify its extent", st)
		}
		stopped = slices.IndexFunc(p.Ops, func(o plan.Op) bool { return o.Seq == st.Op.Seq })
	}
	if mend != nil {
		mend()
	}
	now := f.image()
	checkBound(t, f.model, now, garbled(fc.garbles, p, stopped)...)
	var changed []flash.Extent
	if fc.changes != nil {
		if e, _, ok := fc.changes(p, k); ok {
			changed = append(changed, e)
		}
	}
	checkMidway(t, now, p, stopped, changed...)
	f.checkOutside(p)

	st := f.status()
	if len(st.Open) == 0 && len(chunksSent(f.dev.Writes(), p)) == 0 {
		sameImage(t, "device after a run that sent nothing", now, pre)
		return
	}
	if len(st.Open) != 1 || st.Open[0].ID == "" {
		t.Fatalf("open runs %+v", st.Open)
	}
	how, target := safety.Forward, post
	if k%2 == 0 {
		how, target = safety.Back, pre
	}
	if _, err := f.x.Recover(context.Background(), st.Open[0], how, f.device(), nil); err != nil {
		t.Fatalf("recover %s: %v", how, err)
	}
	sameImage(t, "device after recovery "+how.String(), f.image(), target)
	checkRecords(t, f.model, f.image(), p)
	checkBound(t, f.model, f.image())
	f.checkOutside(p)
	st = f.status()
	if !st.Clean() || st.Runs[0].Resolved != how {
		t.Fatalf("after recovery: open %+v, first run resolved %s", st.Open, st.Runs[0].Resolved)
	}
}

// sessionCase injects one fault at the k-th cmd 7 (from 1) of a plan the
// session writes through its own link. inject arms it before the apply and
// may return a hook, which sees every event of the run on the session's
// goroutine, and what the user does about the fault before recovery. kill
// stops the session from inside the run, as a crash stops arcctl.
type sessionCase struct {
	name     string
	behavior emu.Behavior
	watchdog time.Duration
	inject   func(r *srig, p plan.Plan, k int, kill context.CancelFunc) (hook func(safety.OpEvent), mend func())
	outcome  func(p plan.Plan, k int) outcome
	cause    func(err error) bool
	pauses   bool
	reads    bool
	// extra: the device sends a reply the session no longer waits for,
	// which must count as a duplicate and never as foreign traffic.
	extra   bool
	garbles bool
	upTo    func(p plan.Plan, k int) int
}

func deviceFault(flt emu.Fault) func(*srig, plan.Plan, int, context.CancelFunc) (func(safety.OpEvent), func()) {
	return func(r *srig, _ plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
		g := flt
		g.Match = emu.Nth(wire.CmdWrite, k)
		r.dev.Inject(g)
		return nil, nil
	}
}

// readBack applies flt to the k-th cmd 8 from the start of the run, which is
// the read-back of the k-th chunk when the run does not pause; the reads of
// the checks and the backups before it do not count.
func readBack(flt emu.Fault, then func(*srig) func(safety.OpEvent)) func(*srig, plan.Plan, int, context.CancelFunc) (func(safety.OpEvent), func()) {
	return func(r *srig, _ plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
		armed := false
		var next func(safety.OpEvent)
		if then != nil {
			next = then(r)
		}
		return func(e safety.OpEvent) {
			if !armed && e.Kind == safety.EventStart {
				armed = true
				g := flt
				g.Match = emu.Nth(wire.CmdRead, k)
				r.dev.Inject(g)
			}
			if next != nil {
				next(e)
			}
		}, nil
	}
}

// nextIs reports, once, the event after which the k-th cmd 7 is the next
// packet: the start of its op, or the reply to the chunk before it.
func nextIs(p plan.Plan, k int) func(safety.OpEvent) bool {
	i, c := chunkOp(p, k)
	seq, done := p.Ops[i].Seq, false
	return func(e safety.OpEvent) bool {
		hit := !done && e.Op.Seq == seq && (c == 1 && e.Kind == safety.EventStart || c > 1 && e.Kind == safety.EventChunk && e.Chunk == c-1)
		done = done || hit
		return hit
	}
}

// acked reports, once, the event of the reply to the k-th cmd 7.
func acked(p plan.Plan, k int) func(safety.OpEvent) bool {
	i, c := chunkOp(p, k)
	seq, done := p.Ops[i].Seq, false
	return func(e safety.OpEvent) bool {
		hit := !done && e.Op.Seq == seq && e.Kind == safety.EventChunk && e.Chunk == c
		done = done || hit
		return hit
	}
}

// onFirstPause runs do once, when the run first pauses.
func onFirstPause(do func()) func(safety.OpEvent) {
	done := false
	return func(e safety.OpEvent) {
		if !done && e.Kind == safety.EventPaused {
			done = true
			do()
		}
	}
}

var sessionCases = []sessionCase{
	{
		name:    "write error",
		inject:  deviceFault(emu.Fault{Times: 3, Action: emu.Fail}),
		outcome: always(completes),
	},
	{
		name:    "write errors",
		inject:  deviceFault(emu.Fault{Times: 3 * (1 + safety.DefaultResends), Action: emu.Fail}),
		outcome: always(stops),
		cause:   classIs(hidio.ClassRetry),
		reads:   true,
	},
	{
		name:    "5 timeouts",
		inject:  deviceFault(emu.Fault{Times: 5, Action: emu.Drop}),
		outcome: always(stops),
		cause:   is(safety.ErrTimeout),
		reads:   true,
	},
	{
		// A mouse command that needed more than three tries is a suspected
		// conflict (I10): the run may still read, but writes no more.
		name:   "4 timeouts",
		inject: deviceFault(emu.Fault{Times: 4, Action: emu.Drop}),
		outcome: func(p plan.Plan, k int) outcome {
			if k == chunksOf(p) {
				return completes
			}
			return stops
		},
		cause: is(safety.ErrConflict),
		upTo:  func(_ plan.Plan, k int) int { return k },
	},
	{
		name:    "nak",
		inject:  deviceFault(emu.Fault{Times: 1, Action: emu.NAK}),
		outcome: always(stops),
		cause:   is(wire.ErrNAK),
		reads:   true,
	},
	{
		name: "disconnect",
		inject: func(r *srig, p plan.Plan, k int, c context.CancelFunc) (func(safety.OpEvent), func()) {
			deviceFault(emu.Fault{Times: 1, Action: emu.Unplug})(r, p, k, c)
			return nil, r.dev.Plug
		},
		outcome: always(stops),
		cause:   classIs(hidio.ClassGone),
	},
	{
		name: "lock",
		inject: func(r *srig, p plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
			at := nextIs(p, k)
			unlock := onFirstPause(func() { time.AfterFunc(40*time.Millisecond, func() { r.dev.SetLocked(false) }) })
			return func(e safety.OpEvent) {
				if at(e) {
					r.dev.SetLocked(true)
				}
				unlock(e)
			}, nil
		},
		outcome: always(completes),
		pauses:  true,
	},
	{
		name:     "stall",
		watchdog: 300 * time.Millisecond,
		inject:   deviceFault(emu.Fault{Times: 1, Action: emu.Hang}),
		outcome:  always(stops),
		cause:    classIs(hidio.ClassStalled),
	},
	{
		name: "kill",
		inject: func(_ *srig, p plan.Plan, k int, kill context.CancelFunc) (func(safety.OpEvent), func()) {
			at := acked(p, k)
			return func(e safety.OpEvent) {
				if at(e) {
					kill()
					panic(errKilled)
				}
			}, nil
		},
		outcome: always(dies),
	},
	{
		name: "profile switch",
		inject: func(r *srig, p plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
			at := nextIs(p, k)
			switchTo := func(v byte) {
				if err := r.dev.SwitchProfile(v); err != nil {
					panic(err)
				}
			}
			return func(e safety.OpEvent) {
				if at(e) {
					switchTo(1)
				}
			}, func() { switchTo(0) }
		},
		outcome: always(stops),
		cause:   is(safety.ErrProfile),
		upTo:    func(_ plan.Plan, k int) int { return k },
	},
	{
		// No push says so (M21): only the profile the session asks for
		// after the mouse woke shows the switch.
		name: "profile switch while asleep",
		inject: func(r *srig, p plan.Plan, k int, c context.CancelFunc) (func(safety.OpEvent), func()) {
			deviceFault(emu.Fault{Times: 1, Action: emu.Asleep})(r, p, k, c)
			asleep := onFirstPause(func() {
				if err := r.dev.SetProfile(1); err != nil {
					panic(err)
				}
				time.AfterFunc(40*time.Millisecond, r.dev.Wake)
			})
			return func(e safety.OpEvent) {
				asleep(e)
				if e.Kind == safety.EventFailed {
					if err := r.dev.SetProfile(0); err != nil {
						panic(err)
					}
				}
			}, nil
		},
		outcome: always(stops),
		cause:   is(safety.ErrProfile),
		upTo:    func(_ plan.Plan, k int) int { return k },
	},
	{
		// A reply nobody asked for is a Conflict (I10): no cmd 7 goes out
		// after it.
		name: "foreign reply",
		inject: func(r *srig, p plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
			at := nextIs(p, k)
			return func(e safety.OpEvent) {
				if at(e) {
					r.dev.Deliver(wire.MustBuild(wire.Mouse, wire.CmdRead, 12000, make([]byte, wire.MaxData)))
				}
			}, nil
		},
		outcome: func(p plan.Plan, k int) outcome {
			if k == chunksOf(p) {
				return either
			}
			return stops
		},
		cause: is(safety.ErrConflict),
		upTo:  func(_ plan.Plan, k int) int { return k },
	},
	{
		name:   "ignored write",
		inject: deviceFault(emu.Fault{Action: emu.Ignore}),
		outcome: func(p plan.Plan, k int) outcome {
			if unchanged(p, k) {
				return completes
			}
			return stops
		},
		cause: is(safety.ErrMismatch),
		reads: true,
		upTo:  endOf,
	},
	{
		name:    "corrupted write",
		inject:  deviceFault(emu.Fault{Action: emu.Corrupt}),
		outcome: always(stops),
		cause:   is(safety.ErrMismatch),
		reads:   true,
		garbles: true,
		upTo:    endOf,
	},
	{
		name:    "stale ack of the previous chunk",
		inject:  staleAckOnDevice(false),
		outcome: always(completes),
		extra:   true,
	},
	{
		name:    "stale ack of this address",
		inject:  staleAckOnDevice(true),
		outcome: always(completes),
		extra:   true,
	},
	{
		name:    "duplicate ack",
		inject:  deviceFault(emu.Fault{Times: 1, Action: emu.Duplicate}),
		outcome: always(completes),
		extra:   true,
	},
	{
		name: "mouse sleep",
		inject: func(r *srig, p plan.Plan, k int, c context.CancelFunc) (func(safety.OpEvent), func()) {
			deviceFault(emu.Fault{Times: 1, Action: emu.Asleep})(r, p, k, c)
			return onFirstPause(func() { time.AfterFunc(40*time.Millisecond, r.dev.Wake) }), nil
		},
		outcome: always(completes),
		pauses:  true,
	},
	{
		name:    "read-back 5 timeouts",
		inject:  readBack(emu.Fault{Times: 5, Action: emu.Drop}, nil),
		outcome: always(stops),
		cause:   is(safety.ErrTimeout),
		reads:   true,
	},
	{
		name: "read-back sleep",
		inject: readBack(emu.Fault{Times: 1, Action: emu.Asleep}, func(r *srig) func(safety.OpEvent) {
			return onFirstPause(func() { time.AfterFunc(40*time.Millisecond, r.dev.Wake) })
		}),
		outcome: always(completes),
		pauses:  true,
	},
	{
		name:     "reply stealing",
		behavior: emu.Behavior{Sharing: emu.Steal},
		inject: func(r *srig, p plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
			at := nextIs(p, k)
			var c *emu.Competitor
			return func(e safety.OpEvent) {
					if at(e) {
						c = r.competitor()
					}
				}, func() {
					if c != nil {
						c.Close()
					}
				}
		},
		outcome: always(either),
	},
}

// staleAckOnDevice has the device answer the k-th cmd 7 late and, while the
// session waits for that reply, send again an ack it sent before: the
// previous chunk's, or one for the k-th chunk's own address that carries the
// bytes the address held before the plan.
func staleAckOnDevice(sameAddr bool) func(*srig, plan.Plan, int, context.CancelFunc) (func(safety.OpEvent), func()) {
	return func(r *srig, p plan.Plan, k int, _ context.CancelFunc) (func(safety.OpEvent), func()) {
		stale := staleOf(p, k, sameAddr)
		r.dev.Inject(emu.Fault{Match: emu.Nth(wire.CmdWrite, k), Times: 1, Action: emu.Late, Delay: 15 * time.Millisecond})
		at, sent := emu.Nth(wire.CmdWrite, k), false
		r.dev.Inject(emu.Fault{Match: func(p wire.Packet) bool {
			if at(p) && !sent {
				sent = true
				go r.dev.Deliver(stale)
			}
			return false
		}})
		return nil, nil
	}
}

// TestSessionFaultMatrix injects the faults of PLAN §10 item 6 into writes
// the session makes through its own link, at the first two, the middle and
// the last two chunks of every op of every op kind (every chunk with
// -every-chunk). Each run then goes through the session's automatic
// recovery, in the same session or in a new one as after a crash, and
// through journal recovery when the session asks for a decision. At the end
// the journal is clean, the guard is ReadOnly, every record the plan touches
// is valid, no binding runs an invalid body, every packet the device got is
// one the Edit policy allows, and the whole device image equals the one from
// before the plan or the one the plan meant.
func TestSessionFaultMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("fault matrix")
	}
	for ki, kind := range matrixKinds {
		probe := newFixture(t, setup{seed: kind.seed})
		chunks := sampleChunks(probe.plan(kind.changes(t, probe.model)), *everyChunk)
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			for fi, fc := range sessionCases {
				t.Run(fc.name, func(t *testing.T) {
					t.Parallel()
					for _, k := range chunks {
						t.Run(fmt.Sprint(k), func(t *testing.T) {
							t.Parallel()
							runSessionFault(t, kind, fc, k, uint64(ki*10000+fi*100+k), (k+ki)%2 == 0)
						})
					}
				})
			}
		})
	}
}

func runSessionFault(t *testing.T, kind opKind, fc sessionCase, k int, seed uint64, restart bool) {
	r := newSRig(t, kind.seed, emu.Options{Seed: seed, Watchdog: fc.watchdog}, fc.behavior)
	_, kill := r.run()
	sn := r.await("ready", func(sn *session.Snapshot) bool {
		return sn.State == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
	})
	p := r.plan(sn, kind.changes(t, r.model))
	pre := r.dev.Image()
	post := after(t, pre, p)
	checkBound(t, r.model, pre)
	hook, mend := fc.inject(r, p, k, kill)

	var mu sync.Mutex
	var run string
	var events []safety.EventKind
	journal := sn.Journal
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := r.s.Apply(ctx, p, allowAll(p), func(e safety.OpEvent) {
		mu.Lock()
		run = e.Run
		events = append(events, e.Kind)
		mu.Unlock()
		if hook != nil {
			hook(e)
		}
	})
	mu.Lock()
	defer mu.Unlock()

	want := fc.outcome(p, k)
	died := errors.Is(err, session.ErrPanic)
	checkFreshOnline(t, r.dev.Writes(), p)
	if err != nil && !died && fc.upTo != nil {
		noChunkAfter(t, r.dev.Writes(), p, fc.upTo(p, k))
	}
	switch {
	case (want == dies) != died:
		t.Fatalf("apply: %v; want the run to %v", err, want)
	case want == completes && err != nil:
		t.Fatalf("apply stopped: %v", err)
	case want == stops && err == nil:
		t.Fatal("apply went on")
	case err == nil:
		if out.Verified != len(p.Ops) || fc.pauses && !slices.Contains(events, safety.EventRestart) {
			t.Fatalf("outcome %+v, events %v", out.Result, events)
		}
	case died:
		checkStop(t, r, p, pre, opAt(p, k), false)
	default:
		st := stopError(t, err)
		if fc.cause != nil && !fc.cause(err) {
			t.Fatalf("stopped with %v", err)
		}
		if fc.reads && (st.Class == safety.ClassUnknown || len(st.Found) != st.Op.Extent.Len) {
			t.Fatalf("stop %+v did not classify its extent", st)
		}
		checkStop(t, r, p, pre, slices.IndexFunc(p.Ops, func(o plan.Op) bool { return o.Seq == st.Op.Seq }), fc.garbles)
	}
	if run == "" {
		t.Fatal("the run was never journaled")
	}
	if fc.extra {
		sn := r.await("the extra reply", func(sn *session.Snapshot) bool { return sn.Stats.Duplicates > 0 })
		if sn.Stats.Foreign > 0 || sn.State == session.Conflict {
			t.Fatalf("an extra reply was taken as foreign: stats %+v, state %v", sn.Stats, sn.State)
		}
	}
	if mend != nil {
		mend()
	}
	if died || restart {
		r.stop()
		r.run()
		journal = nil
	}
	r.clearConflict()

	sn = r.settled(journal)
	if open := sn.Journal.Open; len(open) > 0 {
		if len(open) != 1 || open[0].Run.ID != run {
			t.Fatalf("open runs %+v, want %s", open, run)
		}
		how := safety.Forward
		if k%2 == 0 {
			how = safety.Back
		}
		if _, err := r.s.Recover(ctx, run, how, safety.Gates{}, nil); err != nil {
			t.Fatalf("recover %s: %v", how, err)
		}
		sn = r.settled(sn.Journal)
		if len(sn.Journal.Open) > 0 {
			t.Fatalf("still open after recovery: %+v", sn.Journal.Open)
		}
	}
	if sn.State != session.Ready || sn.Policy != wire.ReadOnly {
		t.Fatalf("state %v, policy %v", sn.State, sn.Policy)
	}

	st := r.journal(sn.Identity)
	if !st.Clean() {
		t.Fatalf("journal has open runs %+v", st.Open)
	}
	target := post
	switch rn := st.Find(run); {
	case rn == nil:
		t.Fatalf("run %s is not in the journal", run)
	case rn.Complete, rn.Resolved == safety.Forward:
	case rn.Resolved == safety.Back, !slices.ContainsFunc(rn.Ops, func(o safety.OpRecord) bool { return o.State != safety.StatePlanned }):
		target = pre
	default:
		t.Fatalf("run %s: complete %v, resolved %v", run, rn.Complete, rn.Resolved)
	}
	final := r.dev.Image()
	sameImage(t, "device at the end", final, target)
	checkRecords(t, r.model, final, p)
	checkBound(t, r.model, final)
	checkPackets(t, r.dev.Writes(), p)
}

// checkStop checks the device right after a run stopped in op stopped.
func checkStop(t testing.TB, r *srig, p plan.Plan, pre *flash.Image, stopped int, garbles bool) {
	t.Helper()
	now := r.dev.Image()
	checkBound(t, r.model, now, garbled(garbles, p, stopped)...)
	checkMidway(t, now, p, stopped)
	checkOutsideImage(t, now, pre, p)
}

// checkOutsideImage fails when a byte outside p's extents differs between now
// and was.
func checkOutsideImage(t testing.TB, now, was *flash.Image, p plan.Plan) {
	t.Helper()
	a, b := now.Bytes(), was.Bytes()
	for i := range a {
		if a[i] != b[i] && !slices.ContainsFunc(p.Ops, func(o plan.Op) bool { return o.Extent.Contains(flash.Extent{Addr: i, Len: 1}) }) {
			t.Fatalf("byte %d changed from %02x to %02x outside the plan", i, b[i], a[i])
		}
	}
}

// TestFullBackupResumes makes the mouse sleep, or another client steal the
// replies, at a chunk of a full backup (PLAN §10 item 6, read side): one
// asked for directly, and the one that precedes the first write ever to a
// device (I1). Once the mouse is awake or the other client has gone, the
// backup must go on from that chunk and end complete; asked for directly, it
// must also read no other chunk twice. The first write then goes ahead.
func TestFullBackupResumes(t *testing.T) {
	if testing.Short() {
		t.Skip("fault matrix")
	}
	n := fullBackupReads(t)
	for _, fault := range []string{"mouse sleep", "reply stealing"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			for _, first := range []bool{false, true} {
				t.Run(map[bool]string{false: "backup", true: "first write"}[first], func(t *testing.T) {
					t.Parallel()
					for _, k := range []int{1, 2, n / 2, n - 1, n} {
						t.Run(fmt.Sprint(k), func(t *testing.T) {
							t.Parallel()
							runBackupFault(t, fault, k, first)
						})
					}
				})
			}
		})
	}
}

// fullBackupReads counts the cmd-8 packets of a full backup after a load,
// resends left out.
func fullBackupReads(t *testing.T) int {
	r := newSRig(t, nil, emu.Options{}, emu.Behavior{})
	r.run()
	r.await("ready", func(sn *session.Snapshot) bool { return sn.State == session.Ready && sn.Progress.Job == "" })
	was := len(r.dev.Writes())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := r.s.Backup(ctx, true)
	if err != nil || len(c.Missing) > 0 {
		t.Fatalf("clean full backup: %v, missing %v", err, c.Missing)
	}
	return cmdCount(emu.Logical(r.dev.Writes()[was:]), wire.CmdRead)
}

var fullRanges = []flash.Extent{{Addr: 0, Len: 6987}, {Addr: 9504, Len: 256}}

// runBackupFault faults the k-th read of a full backup. With first, the
// backup is the one the first write runs, after the checks re-read the one
// chunk of the plan.
func runBackupFault(t *testing.T, fault string, k int, first bool) {
	r := newSRig(t, nil, emu.Options{}, emu.Behavior{})
	r.run()
	sn := r.await("ready", func(sn *session.Snapshot) bool {
		return sn.State == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
	})
	base := len(r.dev.Writes())
	var p plan.Plan
	skip := k - 1
	if first {
		p = r.plan(sn, pairChange(t))
		skip += chunksOf(p)
	}
	at := emu.Nth(wire.CmdRead, skip+1)

	switch fault {
	case "mouse sleep":
		r.dev.Inject(emu.Fault{Match: at, Times: 1, Action: emu.Asleep})
	case "reply stealing":
		// Another client opens the receiver when the k-th read goes out and
		// takes every reply to that chunk until the session has asked 20
		// times or gone on to another chunk; then it closes.
		start, end := make(chan struct{}), make(chan struct{})
		var left atomic.Bool
		started, stolen, ended := false, 0, false
		var addr uint16
		stop := func() {
			if !ended {
				ended = true
				close(end)
			}
		}
		r.dev.Inject(emu.Fault{Action: emu.Drop, Match: func(p wire.Packet) bool {
			hit := at(p) && !started
			switch {
			case left.Load() || p.Cmd() != wire.CmdRead:
				return false
			case hit:
				started, addr = true, p.Addr()
				close(start)
			case !started:
				return false
			case p.Addr() != addr:
				stop()
				return false
			}
			if stolen++; stolen == 20 {
				stop()
			}
			return true
		}})
		go func() {
			select {
			case <-start:
			case <-t.Context().Done():
				return
			}
			c := r.dev.Chrome(0)
			select {
			case <-end:
			case <-t.Context().Done():
			}
			c.Close()
			left.Store(true)
		}()
	}

	type res struct {
		c   session.Capture
		out session.Outcome
		err error
	}
	got := make(chan res, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if first {
			out, err := r.s.Apply(ctx, p, allowAll(p), nil)
			got <- res{out: out, err: err}
			return
		}
		c, err := r.s.Backup(ctx, true)
		got <- res{c: c, err: err}
	}()
	if fault == "mouse sleep" {
		r.await("the pause", func(sn *session.Snapshot) bool { return sn.Progress.Job == "backup" && sn.Progress.Paused })
		r.dev.Wake()
	}
	b := <-got
	if first {
		saved := b.out.Backups
		if errors.Is(b.err, safety.ErrConflict) {
			// The replies that went missing leave a suspected conflict,
			// which blocks writes until it clears (I10); the backup stays.
			r.clearConflict()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			b.out, b.err = r.s.Apply(ctx, p, allowAll(p), nil)
			saved = append(saved, b.out.Backups...)
		}
		if b.err != nil {
			t.Fatal(b.err)
		}
		if len(saved) != 1 {
			t.Fatalf("backups %v, want the first-write one", saved)
		}
		f, err := backup.Load(saved[0])
		if err != nil {
			t.Fatal(err)
		}
		if !f.Full || len(f.Missing()) > 0 || f.Label != session.LabelFirstWrite {
			t.Fatalf("first-write backup: full %v, missing %v, label %q", f.Full, f.Missing(), f.Label)
		}
		sameImage(t, "the first-write backup", f.Image(), r.start)
		sameImage(t, "device after the write", r.dev.Image(), after(t, r.start, p))
		return
	}
	if b.err != nil {
		t.Fatal(b.err)
	}
	dev := r.dev.Image()
	if !b.c.Full || len(b.c.Missing) > 0 {
		t.Fatalf("full %v, missing %v", b.c.Full, b.c.Missing)
	}
	for _, e := range fullRanges {
		g, ok := b.c.Image.Get(e)
		w, _ := dev.Get(e)
		if !ok || !slices.Equal(g, w) {
			t.Fatalf("the backup does not hold the device's bytes over %v", e)
		}
	}
	var reads []flash.Extent
	for _, w := range emu.Logical(r.dev.Writes()[base:]) {
		if w.Packet.Cmd() == wire.CmdRead {
			reads = append(reads, flash.Extent{Addr: int(w.Packet.Addr()), Len: w.Packet.Len()})
		}
	}
	failed := reads[k-1]
	if n := len(slices.DeleteFunc(slices.Clone(reads), func(e flash.Extent) bool { return e != failed })); n < 2 {
		t.Fatalf("chunk %v was read %d times; the backup did not go back to it", failed, n)
	}
	for i, e := range reads {
		if e != failed && slices.Contains(reads[:i], e) {
			t.Fatalf("chunk %v was read twice; the backup started over instead of going on", e)
		}
	}
}

// TestSessionFaultDuringRecovery makes the recovery of a crashed run fail
// too: the device refuses a chunk of the recovery's own body write. The
// failed recovery is folded into the run it was settling, which the session
// offers again, and the second recovery settles both.
func TestSessionFaultDuringRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("fault matrix")
	}
	for _, how := range []safety.Strategy{safety.Forward, safety.Back} {
		t.Run(how.String(), func(t *testing.T) {
			t.Parallel()
			r := newSRig(t, nil, emu.Options{}, emu.Behavior{})
			_, kill := r.run()
			sn := r.await("ready", func(sn *session.Snapshot) bool {
				return sn.State == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
			})
			p := r.plan(sn, shortcutChange(t))
			pre := r.dev.Image()
			target := after(t, pre, p)
			if how == safety.Back {
				target = pre
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var run string
			at := acked(p, 3)
			_, err := r.s.Apply(ctx, p, allowAll(p), func(e safety.OpEvent) {
				run = e.Run
				if at(e) {
					kill()
					panic(errKilled)
				}
			})
			if !errors.Is(err, session.ErrPanic) {
				t.Fatalf("apply: %v, want it killed", err)
			}
			r.stop()
			r.run()
			sn = r.settled(nil)
			if len(sn.Journal.Open) != 1 || !sn.Journal.Open[0].Inspection.Torn() {
				t.Fatalf("open runs %+v, want the torn one", sn.Journal.Open)
			}

			r.dev.Inject(emu.Fault{Match: emu.Nth(wire.CmdWrite, 2), Times: 1, Action: emu.NAK})
			if _, err := r.s.Recover(ctx, run, how, safety.Gates{}, nil); !errors.Is(err, wire.ErrNAK) {
				t.Fatalf("recovery: %v, want the NAK", err)
			}
			sn = r.settled(sn.Journal)
			if len(sn.Journal.Open) != 1 || sn.Journal.Open[0].Run.ID != run {
				t.Fatalf("open runs %+v, want %s again", sn.Journal.Open, run)
			}
			checkBound(t, r.model, r.dev.Image())
			if _, err := r.s.Recover(ctx, run, how, safety.Gates{}, nil); err != nil {
				t.Fatalf("second recovery: %v", err)
			}
			sn = r.settled(sn.Journal)
			st := r.journal(sn.Identity)
			if !st.Clean() || st.Find(run).Resolved != how || sn.State != session.Ready {
				t.Fatalf("journal: open %+v, resolved %v; state %v", st.Open, st.Find(run).Resolved, sn.State)
			}
			for _, rn := range st.Runs {
				if rn.Kind == safety.KindRecover && rn.Of != run {
					t.Errorf("recovery %s settles %q, not %s", rn.ID, rn.Of, run)
				}
			}
			sameImage(t, "device after recovery "+how.String(), r.dev.Image(), target)
			checkRecords(t, r.model, r.dev.Image(), p)
			checkBound(t, r.model, r.dev.Image())
			checkPackets(t, r.dev.Writes(), p)
		})
	}
}

// TestSessionEchoModes writes a two-phase trio and a 39-chunk macro under
// each way the device may answer cmd 7 until H1 settles it: the full echo,
// the header only, and every reply sent twice; and it checks that a NAK
// without the length still stops the run. The read-back decides every time.
func TestSessionEchoModes(t *testing.T) {
	if testing.Short() {
		t.Skip("fault matrix")
	}
	modes := []struct {
		name string
		bh   emu.Behavior
	}{
		{"full echo", emu.Behavior{Echo: emu.EchoFull}},
		{"header echo", emu.Behavior{Echo: emu.EchoHeader}},
		{"double reply", emu.Behavior{DoubleWrite: true}},
		{"short nak", emu.Behavior{ShortNAK: true}},
	}
	for _, m := range modes {
		for _, kind := range []opKind{kindNamed(t, "macro 39 chunks"), kindNamed(t, "shortcut trio")} {
			t.Run(m.name+"/"+kind.name, func(t *testing.T) {
				t.Parallel()
				r := newSRig(t, kind.seed, emu.Options{}, m.bh)
				r.run()
				sn := r.await("ready", func(sn *session.Snapshot) bool {
					return sn.State == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
				})
				p := r.plan(sn, kind.changes(t, r.model))
				pre := r.dev.Image()
				nak := m.bh.ShortNAK
				if nak {
					r.dev.Inject(emu.Fault{Match: emu.Nth(wire.CmdWrite, 2), Times: 1, Action: emu.NAK})
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, err := r.s.Apply(ctx, p, allowAll(p), nil)
				switch {
				case nak && !errors.Is(err, wire.ErrNAK):
					t.Fatalf("apply: %v, want the NAK", err)
				case nak:
					checkStop(t, r, p, pre, opAt(p, 2), false)
					return
				case err != nil:
					t.Fatal(err)
				case out.Verified != len(p.Ops):
					t.Fatalf("outcome %+v", out.Result)
				}
				sameImage(t, "device after the apply", r.dev.Image(), after(t, pre, p))
				if sn := r.settled(sn.Journal); sn.Stats.Foreign > 0 || len(sn.Journal.Open) > 0 {
					t.Fatalf("stats %+v, journal %+v", sn.Stats, sn.Journal)
				}
			})
		}
	}
}
