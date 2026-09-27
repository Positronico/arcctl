// Package safety writes plans to a mouse. It journals every op before the
// op's first packet, writes each record in chunks, re-reads it and compares it
// byte for byte, pauses while the mouse sleeps or the screen is locked, and
// stops on anything else. From the journal it finds the runs a crash or a
// failure left unfinished, settles them, and reverts the last run.
package safety

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

// Link is the session's side of a run: one strict transaction at a time with
// the device the plan was made for, through its guarded transport.
type Link interface {
	// Transact sends p and returns the reply that answers it. A NAK returns an
	// error wrapping wire.ErrNAK, and a request still unanswered after the
	// link's own tries one wrapping ErrTimeout. Write errors come back as the
	// transport returned them, so hidio.Classify can read them.
	Transact(ctx context.Context, p wire.Packet) (wire.Packet, error)
	// OnlineCheck sends a fresh cmd 3. It returns nil when the mouse is online
	// and is still the device, with the profile, the run started with; an
	// error wrapping ErrOffline while the mouse sleeps; and any other error
	// when the device must not be written.
	OnlineCheck(ctx context.Context) error
	// Recheck runs again, after a pause, the checks of the preflight that
	// do not read the device: no other client on the receiver, the lock, the
	// console and the session's own state. An error stops the run before
	// its next packet.
	Recheck(ctx context.Context) error
	// Pending first handles every report that has already arrived, then
	// returns the pushes and abort requests seen since the last call.
	Pending() []Signal
}

type SignalKind uint8

const (
	SignalProfile SignalKind = iota + 1 // StatusChanged 0x04: stop before the next packet
	SignalReread                        // a push re-reads Extents: stop after the current op when they touch it or a later one
	SignalAbort                         // the user asked to stop: stop after the current op
)

type Signal struct {
	Kind    SignalKind
	Extents []flash.Extent
}

// Device is the device a run writes to, as the session sees it now.
type Device struct {
	Identity plan.Identity
	Profile  *byte
	// Image holds what the device holds: every binding and every bound body,
	// and each extent the plan writes.
	Image  *flash.Image
	Layout plan.Layout
}

type EventKind uint8

const (
	EventStart    EventKind = iota + 1 // the op is next: journaled as sending, the mouse online
	EventChunk                         // Chunk of Chunks acknowledged
	EventWritten                       // every chunk acknowledged; the read-back is next
	EventVerified                      // the read-back equals the new bytes
	EventFailed                        // the run stops at this op
	EventPaused                        // waiting for the mouse to wake or the screen to unlock; Err says which
	EventResumed
	EventRestart // the op is written again from its first chunk
)

var eventNames = [...]string{"", "start", "chunk", "written", "verified", "failed", "paused", "resumed", "restart"}

func (k EventKind) String() string {
	if k >= EventStart && int(k) < len(eventNames) {
		return eventNames[k]
	}
	return fmt.Sprintf("event %d", uint8(k))
}

// OpEvent reports the progress of one op of a run.
type OpEvent struct {
	Run    string
	Kind   EventKind
	Op     plan.Op
	Chunk  int
	Chunks int
	Class  Class  // EventFailed
	Found  []byte // EventFailed
	Err    error  // EventFailed, EventPaused
}

// Result is what a run did. Run is its journal ID, empty when the plan had no
// ops.
type Result struct {
	Run      string
	Ops      int
	Verified int
	// Echoes counts cmd-7 replies whose data differed from the request. The
	// echo is only logged; the read-back decides.
	Echoes int
	// Overlap reports a push that re-read the last op's extent while the op
	// was written: the device may have changed it in between, so it must be
	// read again.
	Overlap bool
}

type Options struct {
	Log         *slog.Logger
	OfflineWait time.Duration // how long a run waits for the mouse to wake (60 s)
	LockWait    time.Duration // how long it waits for the screen to unlock (10 min)
	Poll        time.Duration // cmd-3 interval while waiting (1 s)
	Resends     int           // resends of a packet the OS failed to send (2)
	Restarts    int           // times one op is written again after a pause (3)
}

const (
	DefaultOfflineWait = 60 * time.Second
	DefaultLockWait    = 10 * time.Minute
	DefaultPoll        = time.Second
	DefaultResends     = 2
	DefaultRestarts    = 3
)

func (o Options) withDefaults() Options {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.OfflineWait <= 0 {
		o.OfflineWait = DefaultOfflineWait
	}
	if o.LockWait <= 0 {
		o.LockWait = DefaultLockWait
	}
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	if o.Resends <= 0 {
		o.Resends = DefaultResends
	}
	if o.Restarts <= 0 {
		o.Restarts = DefaultRestarts
	}
	return o
}

// Executor runs plans over a Link and records them in a Journal.
type Executor struct {
	link Link
	j    *Journal
	opt  Options
}

func NewExecutor(l Link, j *Journal, o Options) *Executor {
	if l == nil || j == nil {
		panic("safety: NewExecutor needs a Link and a Journal")
	}
	return &Executor{link: l, j: j, opt: o.withDefaults()}
}

// Apply writes p, which must be valid against d.Image and made for d's
// device and profile. It returns a *StopError when the run stopped before
// its last op was verified; the run is then open in the journal, unless no
// packet of it went out.
func (x *Executor) Apply(ctx context.Context, p plan.Plan, d Device, on func(OpEvent)) (Result, error) {
	return x.run(x.runner(ctx, on), KindApply, "", p, d)
}

func (x *Executor) runner(ctx context.Context, on func(OpEvent)) *runner {
	return &runner{x: x, ctx: ctx, on: on, res: &Result{}}
}

// run writes p with r, which may already hold the signals taken while the
// run's extents were read.
func (x *Executor) run(r *runner, kind RunKind, of string, p plan.Plan, d Device) (Result, error) {
	res := Result{Ops: len(p.Ops)}
	r.res = &res
	if err := x.check(p, d); err != nil {
		return res, err
	}
	if len(p.Ops) == 0 {
		return res, nil
	}
	id, err := x.j.begin(kind, of, p)
	if err != nil {
		return res, err
	}
	res.Run = id
	r.id, r.ops = id, p.Ops
	r.overlap = r.overlap || slices.ContainsFunc(r.early, func(e flash.Extent) bool {
		return slices.ContainsFunc(p.Ops, func(op plan.Op) bool { return op.Extent.Overlaps(e) })
	})
	for i, op := range p.Ops {
		r.cur = i
		if st := r.do(op); st != nil {
			if err := x.j.end(id, st.Err); err != nil {
				st.Err = errors.Join(st.Err, err)
			}
			return res, st
		}
		res.Verified++
	}
	_ = r.poll()
	if res.Overlap = r.overlap; res.Overlap {
		x.opt.Log.Warn("a push re-read the last op's extent while it was written", "run", id, "op", r.ops[r.cur].Seq)
	}
	return res, x.j.end(id, nil)
}

func (x *Executor) check(p plan.Plan, d Device) error {
	if p.Device.Key() != x.j.key || p.Device.Key() != d.Identity.Key() {
		return fmt.Errorf("%w: plan for %s, device %s, journal of %s", ErrIdentity, p.Device.Key(), d.Identity.Key(), x.j.key)
	}
	if !sameProfile(p.Profile, d.Profile) {
		return fmt.Errorf("%w: plan for profile %s, the active one is %s", ErrProfile, profileString(p.Profile), profileString(d.Profile))
	}
	return p.Validate(d.Image, d.Layout)
}

// runner is one run in progress.
type runner struct {
	x   *Executor
	ctx context.Context
	id  string
	ops []plan.Op
	cur int
	on  func(OpEvent)
	res *Result

	profile bool
	abort   bool
	overlap bool
	// early are the ranges pushes re-read before the run had ops, while its
	// extents were read for a recovery or a revert.
	early []flash.Extent
}

func (r *runner) emit(e OpEvent) {
	if r.on != nil {
		e.Run = r.id
		r.on(e)
	}
}

// do writes one op and verifies it. A pause (sleep or lock) starts the op
// again from its first chunk; anything else stops the run.
func (r *runner) do(op plan.Op) *StopError {
	if err := r.between(); err != nil {
		return r.fail(op, false, 0, err)
	}
	chunks := (op.Extent.Len + wire.MaxData - 1) / wire.MaxData
	written := false
	for attempt := 0; ; attempt++ {
		resumed, err := r.ready(written)
		if err == nil && resumed {
			err = r.recheck(written)
		}
		if err == nil && !written {
			err = r.between()
		}
		if err != nil {
			return r.fail(op, written, 0, err)
		}
		if attempt == 0 {
			if err := r.x.j.state(r.id, op.Seq, StateSending, nil); err != nil {
				return r.fail(op, false, 0, err)
			}
			r.emit(OpEvent{Kind: EventStart, Op: op, Chunks: chunks})
		} else {
			r.emit(OpEvent{Kind: EventRestart, Op: op, Chunks: chunks})
		}
		acked, err := r.write(op, chunks, &written)
		if err == nil {
			if err := r.x.j.state(r.id, op.Seq, StateSent, nil); err != nil {
				return r.fail(op, true, acked, err)
			}
			r.emit(OpEvent{Kind: EventWritten, Op: op, Chunk: acked, Chunks: chunks})
			var found []byte
			if found, err = r.read(op.Extent, false); err == nil {
				if !bytes.Equal(found, op.New) {
					return r.failed(&StopError{Op: op, Written: true, Chunks: acked, Found: found, Class: classify(op, found),
						Err: fmt.Errorf("%w at %s", ErrMismatch, op.Extent)})
				}
				if err := r.x.j.state(r.id, op.Seq, StateVerified, nil); err != nil {
					return r.failed(&StopError{Op: op, Written: true, Chunks: acked, Class: ClassNew, Found: found, Err: err})
				}
				r.emit(OpEvent{Kind: EventVerified, Op: op, Chunk: acked, Chunks: chunks})
				return nil
			}
		}
		cause, pause := r.hold(err)
		if !pause || attempt >= r.x.opt.Restarts {
			return r.fail(op, written, acked, cause)
		}
		if err := r.pause(cause, written); err != nil {
			return r.fail(op, written, acked, err)
		}
		if err := r.recheck(written); err != nil {
			return r.fail(op, written, acked, err)
		}
	}
}

func (r *runner) write(op plan.Op, chunks int, written *bool) (int, error) {
	for i := range chunks {
		if err := r.poll(); err != nil {
			return i, err
		}
		off := i * wire.MaxData
		data := op.New[off:min(off+wire.MaxData, len(op.New))]
		p, err := wire.Build(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr+off), data)
		if err != nil {
			return i, err
		}
		*written = true
		rep, err := r.transact(p)
		if err != nil {
			return i, err
		}
		if !bytes.Equal(rep.Data(), data) {
			r.res.Echoes++
			r.x.opt.Log.Info("cmd-7 echo differs from the request", "request", p, "reply", rep)
		}
		r.emit(OpEvent{Kind: EventChunk, Op: op, Chunk: i + 1, Chunks: chunks})
	}
	return chunks, nil
}

// read reads e back in chunks of at most 10 bytes. Once is the last read of
// a run that stopped: no signals are taken and nothing is resent.
func (r *runner) read(e flash.Extent, once bool) ([]byte, error) {
	if once && r.profile {
		return nil, ErrProfile
	}
	out := make([]byte, 0, e.Len)
	for off := 0; off < e.Len; off += wire.MaxData {
		if !once {
			if err := r.poll(); err != nil {
				return nil, err
			}
		}
		n := min(wire.MaxData, e.Len-off)
		p, err := wire.BuildRead(wire.Mouse, uint16(e.Addr+off), n)
		if err != nil {
			return nil, err
		}
		var rep wire.Packet
		if once {
			rep, err = r.x.link.Transact(r.ctx, p)
		} else {
			rep, err = r.transact(p)
		}
		if err != nil {
			return nil, err
		}
		if rep.Len() != n {
			return nil, fmt.Errorf("%w: %d bytes for a read of %d at %d", ErrReply, rep.Len(), n, e.Addr+off)
		}
		out = append(out, rep.Data()...)
	}
	return out, nil
}

// transact resends a packet the OS failed to send; the link has already
// retried missing replies.
func (r *runner) transact(p wire.Packet) (wire.Packet, error) {
	for try := 0; ; try++ {
		rep, err := r.x.link.Transact(r.ctx, p)
		if err == nil {
			return rep, nil
		}
		if c := hidio.Classify(err); (c != hidio.ClassRetry && c != hidio.ClassTimeout) || try >= r.x.opt.Resends || r.ctx.Err() != nil {
			return rep, err
		}
		r.x.opt.Log.Info("resending after a write error", "packet", p, "err", err)
	}
}

// poll takes the link's signals. A profile switch, and a cancelled context,
// stop the run before its next packet.
func (r *runner) poll() error {
	for _, s := range r.x.link.Pending() {
		switch s.Kind {
		case SignalProfile:
			r.profile = true
		case SignalAbort:
			r.abort = true
		case SignalReread:
			if r.ops == nil {
				r.early = append(r.early, s.Extents...)
			}
			for _, op := range r.ops[r.cur:] {
				if slices.ContainsFunc(s.Extents, op.Extent.Overlaps) {
					r.overlap = true
				}
			}
		}
	}
	if r.profile {
		return fmt.Errorf("%w: the mouse switched profiles", ErrProfile)
	}
	return r.ctx.Err()
}

// between is the check before an op's first packet: an abort or an
// overlapping push stops the run here.
func (r *runner) between() error {
	if err := r.poll(); err != nil {
		return err
	}
	return r.stopping()
}

// stopping is the stop an abort or an overlapping push asks for, if any.
func (r *runner) stopping() error {
	switch {
	case r.abort:
		return ErrAborted
	case r.overlap:
		return ErrOverlap
	}
	return nil
}

// ready sends the fresh cmd 3 an op needs before its first packet (I7),
// waiting while the mouse sleeps or the screen is locked. resumed reports a
// wait; written that a packet of the op went out already.
func (r *runner) ready(written bool) (resumed bool, err error) {
	if err := r.poll(); err != nil {
		return false, err
	}
	err = r.x.link.OnlineCheck(r.ctx)
	if err == nil {
		return false, nil
	}
	if !waitable(err) {
		return false, err
	}
	if err := r.pause(err, written); err != nil {
		return false, err
	}
	return true, nil
}

// hold decides whether err is a pause: the screen is locked, or a request
// went unanswered and a fresh cmd 3 says the mouse sleeps. It returns the
// cause to wait on or to stop with.
func (r *runner) hold(err error) (error, bool) {
	switch {
	case hidio.Classify(err) == hidio.ClassLocked:
		return err, true
	case !errors.Is(err, ErrTimeout) || r.ctx.Err() != nil:
		return err, false
	}
	switch cerr := r.x.link.OnlineCheck(r.ctx); {
	case cerr == nil:
		return err, false
	case waitable(cerr):
		return cerr, true
	default:
		return errors.Join(err, cerr), false
	}
}

func waitable(err error) bool {
	return errors.Is(err, ErrOffline) || hidio.Classify(err) == hidio.ClassLocked
}

// pause waits until a cmd 3 finds the mouse online again, for up to
// OfflineWait while it sleeps and LockWait while the screen is locked. Until
// a packet of the op went out (written), an abort or an overlapping push
// ends the wait at once.
func (r *runner) pause(cause error, written bool) error {
	r.emit(OpEvent{Kind: EventPaused, Op: r.ops[r.cur], Err: cause})
	started := time.Now()
	t := time.NewTimer(r.x.opt.Poll)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-t.C:
		}
		if err := r.poll(); err != nil {
			return err
		}
		if !written {
			if err := r.stopping(); err != nil {
				return err
			}
		}
		err := r.x.link.OnlineCheck(r.ctx)
		switch {
		case err == nil:
			r.emit(OpEvent{Kind: EventResumed, Op: r.ops[r.cur]})
			return nil
		case !waitable(err):
			return err
		}
		limit := r.x.opt.OfflineWait
		if hidio.Classify(err) == hidio.ClassLocked {
			limit = r.x.opt.LockWait
		}
		if waited := time.Since(started); waited >= limit {
			return fmt.Errorf("gave up after %s: %w", waited.Round(time.Millisecond), err)
		}
		t.Reset(r.x.opt.Poll)
	}
}

// recheck is the part of the preflight a pause makes stale: the link's own
// checks, then every extent the run has yet to write must still hold the
// bytes its next op expects. The current op's extent is left out once a
// packet of it went out.
func (r *runner) recheck(written bool) error {
	if err := r.x.link.Recheck(r.ctx); err != nil {
		return err
	}
	cur := r.ops[r.cur]
	var seen []flash.Extent
	for _, op := range r.ops[r.cur:] {
		if written && op.Extent.Overlaps(cur.Extent) || slices.Contains(seen, op.Extent) {
			continue
		}
		seen = append(seen, op.Extent)
		found, err := r.read(op.Extent, false)
		if err != nil {
			return err
		}
		if !bytes.Equal(found, op.Old) {
			return fmt.Errorf("%w: %s holds % x, op %d expects % x", ErrChanged, op.Extent, found, op.Seq, op.Old)
		}
	}
	return nil
}

// fail ends the run at op. When a packet of op went out and the device can
// still be read, it reads the extent back to say what the op left there.
func (r *runner) fail(op plan.Op, written bool, acked int, err error) *StopError {
	st := &StopError{Op: op, Written: written, Chunks: acked, Err: err}
	if written && readable(err) {
		if found, rerr := r.read(op.Extent, true); rerr == nil {
			st.Found, st.Class = found, classify(op, found)
		}
	}
	if !written {
		r.emit(OpEvent{Kind: EventFailed, Op: op, Err: err})
		return st
	}
	return r.failed(st)
}

func (r *runner) failed(st *StopError) *StopError {
	if err := r.x.j.state(r.id, st.Op.Seq, StateFailed, st); err != nil {
		st.Err = errors.Join(st.Err, err)
	}
	r.emit(OpEvent{Kind: EventFailed, Op: st.Op, Class: st.Class, Found: st.Found, Err: st.Err})
	return st
}

// readable reports whether the device may still be read after err: not
// after a profile switch, a cancel, a sleeping mouse or a transport that is
// gone, stalled or refused.
func readable(err error) bool {
	switch {
	case errors.Is(err, ErrProfile), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrOffline), errors.Is(err, ErrJournal):
		return false
	}
	switch hidio.Classify(err) {
	case hidio.ClassGone, hidio.ClassStalled, hidio.ClassSeized, hidio.ClassPermission, hidio.ClassRefused, hidio.ClassLocked:
		return false
	}
	return true
}

func classify(op plan.Op, found []byte) Class {
	switch {
	case found == nil:
		return ClassUnknown
	case bytes.Equal(found, op.New):
		return ClassNew
	case bytes.Equal(found, op.Old):
		return ClassOld
	}
	return ClassTorn
}
