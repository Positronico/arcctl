package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// Outcome is what a write did.
type Outcome struct {
	safety.Result
	// Backups are the backups saved before the write, in order.
	Backups []string
	DryRun  bool
	// Packets are, for a dry run, the cmd-7 packets it would have sent.
	Packets []wire.Packet
	// Image is, after a dry run, the loaded image with every dry-run write
	// of this session on top.
	Image *flash.Image
}

// writeTask is one Apply, Recover or Revert. It runs on the owner goroutine
// in passes: checks, then the backups I1 calls for, then the write. A pass
// that has to wait for the full backup leaves the task in place, and the
// next pass starts over from fresh checks.
type writeTask struct {
	req   request
	kind  safety.RunKind
	plan  plan.Plan
	run   string
	how   safety.Strategy
	gates safety.Gates
	on    func(safety.OpEvent)
	out   Outcome
	job   *job // the full backup the task waits for
}

type writeProgress struct {
	kind        safety.RunKind
	done, total int
	paused      bool
}

func (s *Session) Apply(ctx context.Context, p plan.Plan, g safety.Gates, on func(safety.OpEvent)) (Outcome, error) {
	return s.write(ctx, &writeTask{kind: safety.KindApply, plan: p, gates: g, on: on})
}

func (s *Session) Recover(ctx context.Context, run string, how safety.Strategy, g safety.Gates, on func(safety.OpEvent)) (Outcome, error) {
	return s.write(ctx, &writeTask{kind: safety.KindRecover, run: run, how: how, gates: g, on: on})
}

func (s *Session) Revert(ctx context.Context, g safety.Gates, on func(safety.OpEvent)) (Outcome, error) {
	return s.write(ctx, &writeTask{kind: safety.KindRevert, gates: g, on: on})
}

// Abort asks the running write to stop after its current op, or a write
// waiting for its backup to give up. It does nothing when no write runs.
func (s *Session) Abort() { s.abort.Store(true) }

// write hands t to the owner goroutine. Once it is taken, a cancelled ctx
// only asks it to stop, so the caller always learns what was written.
func (s *Session) write(ctx context.Context, t *writeTask) (Outcome, error) {
	r := request{ctx: ctx, kind: reqWrite, task: t, reply: make(chan result, 1)}
	select {
	case s.reqs <- r:
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	case <-s.done:
		return Outcome{}, ErrStopped
	}
	select {
	case res := <-r.reply:
		return res.outcome, res.err
	case <-s.done:
		select {
		case res := <-r.reply:
			return res.outcome, res.err
		default:
			return Outcome{}, ErrStopped
		}
	}
}

func (s *Session) startWrite(ctx context.Context, r request) {
	switch {
	case s.opt.Writes == nil:
		r.answer(result{err: ErrReadOnly})
		return
	case s.task != nil:
		r.answer(result{err: ErrBusy})
		return
	}
	t := r.task
	t.req = r
	t.out.DryRun = t.gates.DryRun
	s.task = t
	s.abort.Store(false)
	s.advance(ctx)
}

// endTask answers the write after publishing the snapshot it left, so the
// caller never sees the guard or the state from before the answer.
func (s *Session) endTask(err error) {
	t := s.task
	s.task = nil
	if t.job != nil {
		s.removeJob(t.job)
	}
	s.dirty = true
	s.flush()
	t.req.answer(result{outcome: t.out, err: err})
}

// advance runs the next pass of the task, unless the caller gave up while
// it waited for its backup.
func (s *Session) advance(ctx context.Context) {
	t := s.task
	switch {
	case t.req.ctx.Err() != nil:
		s.endTask(fmt.Errorf("%w before anything was written: %w", safety.ErrAborted, t.req.ctx.Err()))
		return
	case s.abort.Swap(false):
		s.endTask(fmt.Errorf("%w before anything was written", safety.ErrAborted))
		return
	case t.job != nil:
		return
	}
	s.pass(ctx, t)
}

func (s *Session) pass(ctx context.Context, t *writeTask) {
	if t.kind == safety.KindApply && len(t.plan.Ops) == 0 {
		s.endTask(nil)
		return
	}
	c, err := s.check(ctx, t)
	if c.w != nil {
		defer s.release(c.w)
	}
	if err != nil {
		s.endTask(err)
		return
	}
	id, st, w, f, j := c.id, c.st, c.w, c.f, c.j
	if s.backsUp(t) {
		for {
			b := s.backupState(id, st)
			err := safety.BackupGate(b, t.gates)
			switch {
			case err == nil:
			case !b.Written && b.Full == "":
				s.backUpFull(t)
				return
			case b.Session == "":
				if err := s.backUpLoaded(t, id); err != nil {
					s.endTask(&safety.PreflightError{Failures: []error{err}})
					return
				}
				continue
			default:
				s.endTask(err)
				return
			}
			break
		}
	}
	s.execute(ctx, t, w, f, st, j)
}

// checked is what the checks of a write found: the loaded identity, the
// journal and its status, the link the write goes through, and the facts.
type checked struct {
	id plan.Identity
	j  *safety.Journal
	st *safety.Status
	w  *writeLink
	f  safety.Facts
}

// check runs the preflight of t (§6.3) with fresh answers from the device
// and the OS. The caller releases c.w when it is set.
func (s *Session) check(ctx context.Context, t *writeTask) (c checked, err error) {
	if s.dev == nil || s.hs == nil || s.model == nil || s.model.Family != catalog.FamilyMouse {
		err := s.writeState()
		if err == nil {
			err = fmt.Errorf("%w: the mouse is not identified yet", safety.ErrNotReady)
		}
		return c, &safety.PreflightError{Failures: []error{err}}
	}
	c.id = s.identity()
	c.j, err = s.journal(c.id)
	if err == nil {
		c.st, err = c.j.Status()
	}
	if err == nil {
		err = s.legacyOpen(c.id)
	}
	p, perr := s.taskPlan(t, c.st, err)
	if perr != nil {
		return c, perr
	}
	c.w = s.newLink(t.req.ctx, p.Device, p.Profile, t.gates)
	f, ferr := s.gather(ctx, t, p, c.w, c.id)
	if ferr != nil {
		s.troubled(ferr)
		return c, ferr
	}
	f.Journal, f.JournalErr = c.st, err
	c.f = f
	return c, safety.Preflight(t.kind, p, f, t.gates)
}

// Preflight runs the checks of an apply of p (§6.3), with fresh answers
// from the device and the OS, and writes nothing. The I1 backups are not
// part of it. A write that does not go through Apply, such as an identity
// write of the hardware tests, must pass it first.
func (s *Session) Preflight(ctx context.Context, p plan.Plan, g safety.Gates) error {
	return s.call(ctx, request{kind: reqPreflight, task: &writeTask{kind: safety.KindApply, plan: p, gates: g}}).err
}

// PreflightRevert runs the checks of a revert of the journal's last run
// (§6.3) and writes nothing, as Preflight does for an apply.
func (s *Session) PreflightRevert(ctx context.Context, g safety.Gates) error {
	return s.call(ctx, request{kind: reqPreflight, task: &writeTask{kind: safety.KindRevert, gates: g}}).err
}

func (s *Session) preflight(ctx context.Context, r request) {
	switch {
	case s.opt.Writes == nil:
		r.answer(result{err: ErrReadOnly})
		return
	case s.task != nil:
		r.answer(result{err: ErrBusy})
		return
	}
	t := r.task
	t.req = r
	c, err := s.check(ctx, t)
	if c.w != nil {
		s.release(c.w)
	}
	s.dirty = true
	r.answer(result{err: err})
}

// taskPlan is the plan the checks look at: an apply's own; for a recovery or
// a revert, the identity and profile of the run it works on, and for a
// revert the run's ops, whose tiers it gates.
func (s *Session) taskPlan(t *writeTask, st *safety.Status, jerr error) (plan.Plan, error) {
	if t.kind == safety.KindApply {
		return t.plan, nil
	}
	if jerr != nil {
		return plan.Plan{}, jerr
	}
	var r *safety.Run
	switch t.kind {
	case safety.KindRecover:
		if r = st.Find(t.run); r == nil || !slices.Contains(st.Open, r) {
			return plan.Plan{}, fmt.Errorf("%w: %q is not an unfinished run of this device", safety.ErrNotOpen, t.run)
		}
	case safety.KindRevert:
		if r = st.Last; r == nil {
			return plan.Plan{}, fmt.Errorf("%w: the journal has no run to revert", safety.ErrNothing)
		}
	}
	p := plan.Plan{Device: r.Device, Profile: r.Profile}
	if t.kind == safety.KindRevert {
		for _, o := range r.Ops {
			p.Ops = append(p.Ops, o.Op)
		}
	}
	return p, nil
}

func (s *Session) journal(id plan.Identity) (*safety.Journal, error) {
	if j := s.journals[id.Key()]; j != nil {
		return j, nil
	}
	j, err := safety.OpenJournal(s.opt.Writes.Journal, id)
	if err != nil {
		return nil, err
	}
	s.journals[id.Key()] = j
	return j, nil
}

// execute runs the write. A dry run goes through the session's overlay and a
// throwaway journal; a real one switches the guard to Edit for as long as it
// runs and queues a reload afterwards.
func (s *Session) execute(ctx context.Context, t *writeTask, w *writeLink, f safety.Facts, st *safety.Status, j *safety.Journal) {
	dev := safety.Device{Identity: f.Identity, Profile: f.Profile, Image: s.image.Clone(), Layout: mouse.Layout(s.model)}
	var link safety.Link = w
	var ov *safety.Overlay
	sent := 0
	if t.gates.DryRun {
		if s.overlay == nil || s.overlayKey != f.Identity.Key() {
			s.overlay, s.overlayKey = safety.NewOverlay(s.log), f.Identity.Key()
		}
		ov = s.overlay
		sent = len(ov.Packets())
		link = ov.Link(w)
		dev.Image = ov.Apply(dev.Image)
		tmp, err := os.MkdirTemp("", "arcctl-dry-run-")
		if err != nil {
			s.endTask(err)
			return
		}
		defer os.RemoveAll(tmp)
		if j, err = safety.OpenJournal(tmp, f.Identity); err != nil {
			s.endTask(err)
			return
		}
		defer j.Close()
	} else {
		s.overlay = nil
	}
	res, err := s.run(ctx, t, link, j, dev, st)
	t.out.Result = res
	if ov != nil {
		t.out.Packets = ov.Packets()[sent:]
		t.out.Image = ov.Apply(s.image)
	}
	s.endTask(err)
}

// run calls the executor with the session in Applying. The guard is Edit
// only while a real write runs, and goes back to ReadOnly however it ends,
// a panic included; the panic becomes an error, and the run it interrupted
// is left open for recovery like a crash.
func (s *Session) run(ctx context.Context, t *writeTask, link safety.Link, j *safety.Journal, dev safety.Device, st *safety.Status) (res safety.Result, err error) {
	writes := s.backsUp(t)
	s.writing = &writeProgress{kind: t.kind, total: len(t.plan.Ops)}
	s.enter(Applying, nil)
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("the write panicked", "panic", v, "stack", string(debug.Stack()))
			err = fmt.Errorf("%w: %v", ErrPanic, v)
		}
		s.writing = nil
		s.afterWrite(err, writes)
		if !t.gates.DryRun {
			s.journalDue = true
		}
	}()
	if writes {
		if err := s.setPolicy(wire.Edit, t.kind.String()+" to "+dev.Identity.Key()); err != nil {
			return res, err
		}
		defer s.setPolicy(wire.ReadOnly, t.kind.String()+" ended")
	}
	x := safety.NewExecutor(link, j, s.execOptions())
	on := s.progressOf(t)
	switch t.kind {
	case safety.KindApply:
		return x.Apply(ctx, t.plan, dev, on)
	case safety.KindRecover:
		return x.Recover(ctx, st.Find(t.run), t.how, dev, on)
	}
	if !t.gates.DryRun {
		return x.Revert(ctx, st.Last, dev, on)
	}
	p, dev, err := x.PlanRevert(ctx, st.Last, dev)
	if err != nil {
		return res, err
	}
	return x.Apply(ctx, p, dev, on)
}

func (s *Session) setPolicy(p wire.Policy, reason string) error {
	err := s.guard.Set(p, reason)
	if err != nil {
		s.log.Error("guard", "policy", p, "reason", reason, "err", err)
	} else {
		s.log.Info("guard", "policy", p, "reason", reason)
	}
	s.dirty = true
	return err
}

func (s *Session) execOptions() safety.Options {
	o := s.opt.Writes.Executor
	if o.Log == nil {
		o.Log = s.log
	}
	return o
}

// progressOf keeps Snapshot.Progress up to date with the run's events and
// passes them on to the caller.
func (s *Session) progressOf(t *writeTask) func(safety.OpEvent) {
	return func(e safety.OpEvent) {
		if w := s.writing; w != nil {
			w.total = max(w.total, e.Op.Seq)
			switch e.Kind {
			case safety.EventVerified:
				w.done++
			case safety.EventPaused:
				w.paused = true
			case safety.EventResumed:
				w.paused = false
			}
			s.dirty = true
		}
		if t.on != nil {
			t.on(e)
		}
	}
}

// afterWrite leaves Applying. A device that went away or is blocked takes
// the usual path, and another mouse that woke up in place of this one goes
// through the handshake like any wake; otherwise the session is Ready or
// Offline again, and after a real write it reloads, which also inspects the
// journal again.
func (s *Session) afterWrite(err error, reload bool) {
	if s.dev == nil || s.troubled(err) {
		return
	}
	if errors.Is(err, safety.ErrIdentity) {
		s.online = false
		s.enter(Offline, ErrDeviceChanged)
		s.due.online = time.Now()
		return
	}
	if s.online && s.base != Offline {
		s.ready()
	} else {
		s.online = false
		s.enter(Offline, nil)
		s.due.online = time.Now().Add(s.tm.Offline)
	}
	if !reload {
		return
	}
	if i := slices.IndexFunc(s.jobs, func(j *job) bool { return j.kind == jobLoad }); i >= 0 {
		s.jobs[i].own = true
		return
	}
	s.addJob(&job{kind: jobLoad, own: true})
}

// troubled hands an error that says the device went away or is blocked to
// the usual state handling, and reports whether it did.
func (s *Session) troubled(err error) bool {
	if err == nil || s.dev == nil {
		return false
	}
	switch hidio.Classify(err) {
	case hidio.ClassGone, hidio.ClassStalled, hidio.ClassPermission, hidio.ClassSeized, hidio.ClassLocked:
	default:
		if !errors.Is(err, ErrGone) {
			return false
		}
	}
	s.fail(err)
	return true
}
