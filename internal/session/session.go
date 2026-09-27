// Package session owns the connection to one receiver or device: it finds and
// probes the interfaces, runs one strict transaction at a time, loads and
// re-reads the flash, polls, follows the device's pushes and watches for other
// clients. Everything runs on the goroutine of Run; the rest of arcctl sees
// immutable snapshots and asks for work through the API.
package session

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

// API is what the TUI and the CLI use; *Session implements it.
type API interface {
	Snapshot() *Snapshot
	// Changed signals, coalesced, that a newer snapshot exists.
	Changed() <-chan struct{}
	// Choose picks the interface to use while the session is Choosing.
	Choose(ctx context.Context, path string) error
	// Reload reads the working set again and returns when it is loaded.
	Reload(ctx context.Context) error
	// Read makes sure the extents are loaded, reading only unknown bytes.
	Read(ctx context.Context, extents ...flash.Extent) (Capture, error)
	// Backup captures what is loaded; with full it first reads every
	// backup range that is still unknown.
	Backup(ctx context.Context, full bool) (Capture, error)
	Apply(ctx context.Context, p plan.Plan, on func(OpEvent)) error
	// ClearConflict leaves Conflict once the device has been quiet for
	// Timing.ConflictQuiet; the user confirms it.
	ClearConflict(ctx context.Context) error
}

// OpEvent reports the progress of one op of an Apply.
type OpEvent struct {
	Op  plan.Op
	Err error
}

type Session struct {
	opt     Options
	tm      Timing
	log     *slog.Logger
	guard   *hidio.Guard
	reqs    chan request
	changed chan struct{}
	snap    atomic.Pointer[Snapshot]
	done    chan struct{}
	started atomic.Bool

	seq   uint64
	dirty bool
	base  State
	prev  State
	since time.Time
	err   error

	scanBackoff  time.Duration
	retryBackoff time.Duration

	dev     *link
	choices []*link

	online    bool
	addr      [3]byte
	idAddr    [3]byte // the address at the last handshake
	lastCheck time.Time
	hs        *Handshake
	model     *catalog.Model
	versions  Versions
	battery   *Battery
	profile   Probe
	longRange Probe

	image  *flash.Image // the working copy; every successful read lands here
	shown  *flash.Image // the published copy, never changed
	unread []flash.Extent
	jobs   []*job

	conflict      State
	conflictSince time.Time
	conflictOn    device // where the Conflict was seen
	lastForeign   time.Time
	runs          runs
	writeRun      int // transactions in a row on the device whose every write failed
	clients       []Client
	stalls        int
	wakeHint      bool
	stats         Stats

	due struct{ scan, retry, online, battery, suspect time.Time }
}

func New(opt Options) *Session {
	if opt.Devices == nil {
		panic("session: Options.Devices is required")
	}
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Session{
		opt:     opt,
		tm:      opt.Timing.withDefaults(),
		log:     log,
		guard:   hidio.NewGuard(wire.Mouse),
		reqs:    make(chan request),
		changed: make(chan struct{}, 1),
		done:    make(chan struct{}),
		since:   time.Now(),
	}
	s.snap.Store(s.snapshot())
	return s
}

// Run is the session's owner goroutine. It returns when ctx is done, after
// closing every interface it opened.
func (s *Session) Run(ctx context.Context) error {
	if s.started.Swap(true) {
		return errors.New("session: Run called twice")
	}
	defer close(s.done)
	defer s.shutdown()
	if err := s.guard.Set(wire.ReadOnly, "session start"); err != nil {
		return err
	}
	wait := time.NewTimer(time.Hour)
	defer wait.Stop()
	for ctx.Err() == nil {
		s.flush()
		switch {
		case s.pending(ctx):
		case s.runDue(ctx):
		case s.work(ctx):
		default:
			s.block(ctx, wait)
		}
	}
	return ctx.Err()
}

func (s *Session) Snapshot() *Snapshot { return s.snap.Load() }

func (s *Session) Changed() <-chan struct{} { return s.changed }

func (s *Session) Choose(ctx context.Context, path string) error {
	return s.call(ctx, request{kind: reqChoose, path: path}).err
}

func (s *Session) Reload(ctx context.Context) error {
	return s.call(ctx, request{kind: reqReload}).err
}

func (s *Session) Read(ctx context.Context, extents ...flash.Extent) (Capture, error) {
	for _, e := range extents {
		if e.Len <= 0 || !(flash.Extent{Addr: 0, Len: flash.Size}).Contains(e) {
			return Capture{}, flash.ErrRange
		}
	}
	r := s.call(ctx, request{kind: reqRead, extents: extents})
	return r.capture, r.err
}

func (s *Session) Backup(ctx context.Context, full bool) (Capture, error) {
	r := s.call(ctx, request{kind: reqBackup, full: full})
	return r.capture, r.err
}

// Apply is refused: this build only reads.
func (s *Session) Apply(context.Context, plan.Plan, func(OpEvent)) error { return ErrReadOnly }

func (s *Session) ClearConflict(ctx context.Context) error {
	return s.call(ctx, request{kind: reqClearConflict}).err
}

// Await waits until pred accepts a snapshot of a and returns that snapshot.
func Await(ctx context.Context, a API, pred func(*Snapshot) bool) (*Snapshot, error) {
	for {
		if sn := a.Snapshot(); pred(sn) {
			return sn, nil
		}
		select {
		case <-a.Changed():
		case <-ctx.Done():
			return a.Snapshot(), ctx.Err()
		}
	}
}

type reqKind uint8

const (
	reqChoose reqKind = iota
	reqReload
	reqRead
	reqBackup
	reqClearConflict
)

type request struct {
	ctx     context.Context
	kind    reqKind
	path    string
	full    bool
	extents []flash.Extent
	reply   chan result
}

type result struct {
	capture Capture
	err     error
}

func (r request) answer(res result) { r.reply <- res }

func (s *Session) call(ctx context.Context, r request) result {
	r.ctx, r.reply = ctx, make(chan result, 1)
	select {
	case s.reqs <- r:
	case <-ctx.Done():
		return result{err: ctx.Err()}
	case <-s.done:
		return result{err: ErrStopped}
	}
	select {
	case res := <-r.reply:
		return res
	case <-ctx.Done():
		return result{err: ctx.Err()}
	case <-s.done:
		select {
		case res := <-r.reply:
			return res
		default:
			return result{err: ErrStopped}
		}
	}
}

func (s *Session) handle(ctx context.Context, r request) {
	switch r.kind {
	case reqChoose:
		s.choose(ctx, r)
	case reqReload:
		s.reload(r)
	case reqRead, reqBackup:
		switch {
		case s.dev == nil:
			r.answer(result{err: ErrNotConnected})
		case s.hs != nil && !s.pollsBattery():
			r.answer(result{err: ErrUnsupported})
		case r.kind == reqRead:
			s.addJob(&job{kind: jobRead, want: r.extents, waiters: []request{r}})
		case r.full:
			s.addJob(&job{kind: jobBackup, full: true, want: []flash.Extent{fullSettings, fullCRC}, waiters: []request{r}})
		default:
			s.addJob(&job{kind: jobBackup, waiters: []request{r}})
		}
	case reqClearConflict:
		r.answer(result{err: s.clearConflict()})
	}
}

// pending handles whatever is ready without waiting. It reports whether it
// handled anything.
func (s *Session) pending(ctx context.Context) bool {
	reports, wake := s.devChans()
	select {
	case r := <-s.reqs:
		s.handle(ctx, r)
	case r, ok := <-reports:
		s.onReport(r, ok)
	case <-wake:
		s.onWake()
	default:
		return false
	}
	return true
}

// block waits for the next event or the next due time.
func (s *Session) block(ctx context.Context, wait *time.Timer) {
	reports, wake := s.devChans()
	wait.Reset(max(time.Until(s.nextDue()), 0))
	defer wait.Stop()
	select {
	case <-ctx.Done():
	case r := <-s.reqs:
		s.handle(ctx, r)
	case r, ok := <-reports:
		s.onReport(r, ok)
	case <-wake:
		s.onWake()
	case <-wait.C:
	}
}

func (s *Session) devChans() (<-chan hidio.Report, <-chan struct{}) {
	if s.dev == nil {
		return nil, nil
	}
	return s.dev.tr.Reports(), s.dev.tr.Wake()
}

func (s *Session) onReport(r hidio.Report, ok bool) {
	if !ok {
		s.gone(s.dev.goneErr())
		return
	}
	if p, ok := r.Packet(); ok {
		s.dispatch(s.dev, p)
	}
}

func (s *Session) onWake() {
	s.wakeHint = true
	s.useWakeHint()
}

// useWakeHint turns input activity into an immediate cmd-3 check while the
// mouse is thought to be asleep.
func (s *Session) useWakeHint() {
	if !s.wakeHint {
		return
	}
	s.wakeHint = false
	if at := s.lastCheck.Add(s.tm.Debounce); s.base == Offline && at.Before(s.due.online) {
		s.due.online = at
	}
}

// work runs one unit of pending work: a scan, a handshake or one job step.
func (s *Session) work(ctx context.Context) bool {
	s.useWakeHint()
	switch s.base {
	case Offline:
		if s.online {
			s.handshake(ctx)
			return true
		}
	case Loading, Ready:
		return s.step(ctx)
	}
	return false
}

func (s *Session) enter(st State, err error) {
	if st != s.base || !errors.Is(err, s.err) {
		s.log.Info("state", "from", s.base, "to", st, "err", err)
		s.since = time.Now()
	}
	if !st.blocked() && st != Probing {
		s.retryBackoff = 0
	}
	s.base, s.err = st, err
	s.dirty = true
}

func (s *Session) flush() {
	if !s.dirty {
		return
	}
	s.dirty = false
	s.snap.Store(s.snapshot())
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *Session) shutdown() {
	s.failJobs(ErrStopped)
	s.closeChoices(nil)
	if s.dev != nil {
		s.dev.close()
		s.dev = nil
	}
	s.dirty = true
	s.flush()
}
