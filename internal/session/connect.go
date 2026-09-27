package session

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

// connect enumerates, probes every candidate with cmd 3 and attaches the one
// that answers, or offers a choice when several do.
func (s *Session) connect(ctx context.Context) {
	s.closeChoices(nil)
	s.enter(Probing, nil)
	if s.opt.Preflight != nil {
		if err := s.opt.Preflight(); err != nil {
			s.blockOn(NeedsPermission, err)
			return
		}
	}
	cands, err := s.opt.Devices.Enumerate()
	if err != nil {
		s.noReceiver(err)
		return
	}
	if s.opt.Device != "" {
		cands = slices.DeleteFunc(cands, func(c hidio.Candidate) bool { return c.Path != s.opt.Device })
	}
	if len(cands) == 0 {
		s.noReceiver(nil)
		return
	}
	var answered []*link
	var errs []error
	for _, c := range cands {
		l, err := s.probe(ctx, c)
		switch {
		case ctx.Err() != nil:
			for _, l := range answered {
				l.close()
			}
			return
		case err != nil:
			errs = append(errs, err)
		case l != nil:
			answered = append(answered, l)
		}
	}
	switch len(answered) {
	case 0:
		s.probeFailed(errs)
	case 1:
		s.attach(ctx, answered[0])
	default:
		for _, l := range answered {
			s.identify(ctx, l)
		}
		s.choices = answered
		s.enter(Choosing, nil)
	}
}

func (s *Session) probe(ctx context.Context, c hidio.Candidate) (*link, error) {
	target := wire.Mouse
	if c.Class == catalog.ClassKeyboard {
		target = wire.Keyboard
	}
	if s.guard.Target() != target {
		if err := s.guard.SetTarget(target, "probe "+c.Path); err != nil {
			return nil, err
		}
	}
	tr, err := s.opt.Devices.Open(c, s.guard, s.opt.Recorder)
	switch {
	case errors.Is(err, hidio.ErrNotVendor):
		s.log.Info("not the vendor channel", "path", c.Path, "err", err)
		return nil, nil
	case err != nil:
		s.log.Info("open failed", "path", c.Path, "err", err)
		return nil, err
	}
	l := &link{c: c, tr: tr, target: target}
	t := s.transact(ctx, l, query(target, wire.CmdOnline), s.tm.ProbeTries, s.tm.ProbeTry)
	if errors.Is(t.err, ErrNoReply) && target == wire.Keyboard {
		s.log.Info("keyboard probe got no reply; retrying without the keyboard flag", "path", c.Path)
		if err := s.guard.SetTarget(wire.Mouse, "keyboard probe got no reply on "+c.Path); err != nil {
			l.close()
			return nil, err
		}
		l.target = wire.Mouse
		t = s.transact(ctx, l, query(wire.Mouse, wire.CmdOnline), s.tm.ProbeTries, s.tm.ProbeTry)
	}
	switch {
	case t.err == nil:
		l.online, l.addr = online(t.rep)
		return l, nil
	case errors.Is(t.err, ErrNoReply), errors.Is(t.err, wire.ErrNAK):
		s.log.Info("no answer", "path", c.Path, "err", t.err)
		l.close()
		return nil, nil
	}
	l.stalled = hidio.Classify(t.err) == hidio.ClassStalled
	l.close()
	return nil, t.err
}

// probeFailed picks the state when nothing answered. A lock or a missing
// permission explains every silent interface, so they win.
func (s *Session) probeFailed(errs []error) {
	for _, st := range []struct {
		class hidio.Class
		state State
	}{{hidio.ClassLocked, Locked}, {hidio.ClassPermission, NeedsPermission}, {hidio.ClassSeized, Seized}} {
		if i := slices.IndexFunc(errs, func(e error) bool { return hidio.Classify(e) == st.class }); i >= 0 {
			s.blockOn(st.state, errs[i])
			return
		}
	}
	if i := slices.IndexFunc(errs, func(e error) bool { return hidio.Classify(e) == hidio.ClassStalled }); i >= 0 {
		s.stall(errs[i])
		return
	}
	s.noReceiver(errors.Join(append([]error{ErrNoAnswer}, errs...)...))
}

// identify handshakes an answering interface so the choice can show what is
// behind it.
func (s *Session) identify(ctx context.Context, l *link) {
	if !l.online {
		return
	}
	t := s.transact(ctx, l, handshake(l.target), s.tm.Tries, s.tm.Try)
	if t.err != nil {
		return
	}
	h := parseHandshake(t.rep)
	l.hs = &h
	l.model, _ = catalog.Resolve(h.CID, h.MID)
}

func (s *Session) choose(ctx context.Context, r request) {
	if s.base != Choosing {
		r.answer(result{err: ErrNotChoosing})
		return
	}
	i := slices.IndexFunc(s.choices, func(l *link) bool { return l.c.Path == r.path })
	if i < 0 {
		r.answer(result{err: fmt.Errorf("%w: %s", ErrNoSuchDevice, r.path)})
		return
	}
	l := s.choices[i]
	s.closeChoices(l)
	s.attach(ctx, l)
	r.answer(result{})
}

func (s *Session) closeChoices(keep *link) {
	for _, l := range s.choices {
		if l != keep {
			l.close()
		}
	}
	s.choices = nil
}

// attach makes l the session's device and asks the receiver for its version.
func (s *Session) attach(ctx context.Context, l *link) {
	s.dev = l
	s.scanBackoff, s.writeRun = 0, 0
	s.online, s.addr, s.lastCheck = l.online, l.addr, time.Now()
	s.log.Info("attached", "device", l.c.String(), "online", l.online)
	s.keepConflict(l.c, l.hs)
	s.scanClients()
	if l.foreign > 0 {
		s.foreignSeen(l)
	}
	t := s.transact(ctx, l, query(l.target, wire.CmdRxVersion), s.tm.Tries, s.tm.Try)
	switch {
	case t.err == nil:
		s.versions.Receiver = version(t.rep)
	case errors.Is(t.err, wire.ErrNAK):
		s.versions.Receiver = "v1.0"
	case errors.Is(t.err, ErrNoReply):
	case transient(t.err):
		if s.writeFailed(t.err); s.dev != l {
			return
		}
	default:
		s.fail(t.err)
		return
	}
	s.enter(Offline, nil)
	s.due.online = time.Now().Add(s.tm.Offline)
}

// handshake identifies the mouse that just came online and starts, or
// resumes, its load.
func (s *Session) handshake(ctx context.Context) {
	s.enter(Handshaking, nil)
	t := s.mouseCmd(ctx, handshake(s.dev.target))
	switch {
	case errors.Is(t.err, ErrOffline), ctx.Err() != nil:
		return
	case t.err != nil && !errors.Is(t.err, ErrNoReply) && !errors.Is(t.err, wire.ErrNAK):
		s.fail(t.err)
		return
	case t.err != nil:
		s.notReady(t.err)
		return
	}
	h := parseHandshake(t.rep)
	if h.CID == 0 || h.MID == 0 {
		s.notReady(nil)
		return
	}
	if s.hs != nil && (s.hs.CID != h.CID || s.hs.MID != h.MID || s.opt.TrustAddress && s.idAddr != s.addr) {
		s.log.Warn("a different device is paired", "was", *s.hs, "now", h)
		s.failJobs(ErrDeviceChanged)
		s.forgetMouse()
	}
	s.hs, s.idAddr = &h, s.addr
	s.keepConflict(s.dev.c, &h)
	if h.Conn == connChargingBase {
		s.unsupported(ErrChargingBase)
		return
	}
	m, ok := catalog.Resolve(h.CID, h.MID)
	if !ok {
		s.unsupported(fmt.Errorf("%w: cid 0x%02x, mid %d", ErrUnknownModel, h.CID, h.MID))
		return
	}
	s.model = m
	if m.Family != catalog.FamilyMouse {
		s.failJobs(ErrUnsupported)
		s.ready()
		return
	}
	if !slices.ContainsFunc(s.jobs, func(j *job) bool { return j.kind == jobLoad }) {
		s.addJob(&job{kind: jobLoad})
	}
	s.resumeJobs()
	s.enter(Loading, nil)
}

// notReady leaves the mouse Offline after a handshake without a usable
// answer, until the next poll says it is online.
func (s *Session) notReady(err error) {
	s.online = false
	s.enter(Offline, err)
	s.due.online = time.Now().Add(s.tm.Offline)
}

func (s *Session) unsupported(err error) {
	s.model = nil
	s.failJobs(ErrUnsupported)
	s.enter(Unknown, err)
	s.due.online = time.Now().Add(s.tm.Online)
}

func (s *Session) ready() {
	s.enter(Ready, nil)
	now := time.Now()
	s.due.online = now.Add(s.tm.Online)
	s.due.battery = now.Add(s.tm.Battery)
}

// checkOnline sends cmd 3 and moves an attached session to Offline when the
// mouse is not there.
func (s *Session) checkOnline(ctx context.Context) (bool, error) {
	t := s.transact(ctx, s.dev, query(s.dev.target, wire.CmdOnline), s.tm.Tries, s.tm.Try)
	s.lastCheck = time.Now()
	s.noteCmd3(t)
	if t.err != nil {
		return false, t.err
	}
	on, addr := online(t.rep)
	if on != s.online {
		s.log.Info("online", "online", on)
	}
	s.online, s.addr = on, addr
	s.dirty = true
	if !on && s.base.attached() && s.base != Offline {
		s.enter(Offline, nil)
		s.due.online = time.Now().Add(s.tm.Offline)
	}
	return on, nil
}

// mouseCmd runs a command the mouse answers over the radio. When it runs out
// of tries, cmd 3 tells a sleeping mouse (ErrOffline) from a lost reply.
func (s *Session) mouseCmd(ctx context.Context, req wire.Packet) txn {
	t := s.transact(ctx, s.dev, req, s.tm.Tries, s.tm.Try)
	switch {
	case t.err == nil, errors.Is(t.err, wire.ErrNAK):
		if t.failed >= suspectMouse {
			s.suspect(fmt.Sprintf("%v needed %d tries", req.Cmd(), t.failed+1))
		}
	case errors.Is(t.err, ErrNoReply):
		on, err := s.checkOnline(ctx)
		switch {
		case err != nil && !errors.Is(err, ErrNoReply):
			t.err = err
		case err != nil:
		case !on:
			t.err = fmt.Errorf("%v: %w", req.Cmd(), ErrOffline)
		default:
			s.suspect(fmt.Sprintf("%v went unanswered while the mouse is online", req.Cmd()))
		}
	}
	return t
}

// fail moves the session to the state a device error calls for.
func (s *Session) fail(err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, ErrGone):
		s.gone(err)
		return
	case transient(err):
		s.writeFailed(err)
		return
	}
	switch hidio.Classify(err) {
	case hidio.ClassLocked:
		s.blockOn(Locked, err)
	case hidio.ClassPermission:
		s.blockOn(NeedsPermission, err)
	case hidio.ClassSeized:
		s.blockOn(Seized, err)
	case hidio.ClassStalled:
		s.stall(err)
	case hidio.ClassRefused:
		s.log.Error("the guard refused a packet", "err", err)
		s.err, s.dirty = err, true
	default:
		s.gone(err)
	}
}

// writeFailLimit is how many transactions in a row may fail to write before
// the device is dropped and found again by a rescan.
const writeFailLimit = 3

// transient reports whether err is a write error that does not say the device
// is gone or blocked: kIOReturnError after hidio's retries, a timeout, or an
// error arcctl does not know.
func transient(err error) bool {
	if !errors.Is(err, errWrite) {
		return false
	}
	switch hidio.Classify(err) {
	case hidio.ClassRetry, hidio.ClassTimeout, hidio.ClassOther:
		return true
	}
	return false
}

// writeFailed handles a transaction that ended in a transient write error. It
// counts as a failed transaction, not as a missing reply, and the device stays
// attached: it is dropped only when it no longer enumerates, or after
// writeFailLimit such transactions in a row.
func (s *Session) writeFailed(err error) {
	s.writeRun++
	s.dirty = true
	s.log.Warn("write failed", "err", err, "in a row", s.writeRun)
	switch {
	case s.dev == nil:
	case !s.listed(s.dev.c):
		s.gone(err)
	case s.writeRun >= writeFailLimit:
		err = fmt.Errorf("%w: %w", ErrWrites, err)
		s.detach(err)
		s.writeRun = 0
		s.enter(NoReceiver, err)
		s.due.scan = later(&s.scanBackoff, s.tm.Rescan, s.tm.RescanMax)
	case s.base == Handshaking:
		s.notReady(err)
	case s.base.blocked():
		s.due.retry = later(&s.retryBackoff, s.tm.Retry, s.tm.RetryMax)
	}
}

// listed reports whether c still enumerates; when enumeration itself fails
// it gives the device the benefit of the doubt.
func (s *Session) listed(c hidio.Candidate) bool {
	cands, err := s.opt.Devices.Enumerate()
	return err != nil || slices.ContainsFunc(cands, func(x hidio.Candidate) bool { return x.Path == c.Path })
}

// blockOn enters a state that waits for the OS or another process, and
// schedules a retry with backoff. A retry re-probes through Probing, which is
// neither remembered as the state to go back to nor resets the backoff.
func (s *Session) blockOn(st State, err error) {
	if !s.base.blocked() && s.base != Probing {
		s.prev = s.base
	}
	s.enter(st, err)
	s.due.retry = later(&s.retryBackoff, s.tm.Retry, s.tm.RetryMax)
}

// retry checks whether a blocked session can go on: a fresh probe when no
// device is attached, else cmd 3 on it.
func (s *Session) retry(ctx context.Context) {
	if s.dev == nil {
		s.connect(ctx)
		return
	}
	_, err := s.checkOnline(ctx)
	if err != nil && !errors.Is(err, ErrNoReply) {
		s.fail(err)
		return
	}
	next := s.prev
	if !next.attached() || next == Handshaking || !s.online {
		next = Offline
	}
	s.enter(next, nil)
	s.due.online = time.Now().Add(s.tm.Offline)
	if next != Offline {
		s.due.online = time.Now().Add(s.tm.Online)
	}
	s.resumeJobs()
}

func (s *Session) stall(err error) {
	s.stalls++
	s.log.Error("write stalled", "stalls", s.stalls, "err", err)
	if s.dev != nil {
		s.dev.stalled = true
	}
	s.detach(err)
	s.enter(Stalled, err)
	s.due.scan = time.Now().Add(s.tm.Rescan)
}

func (s *Session) gone(err error) {
	if !errors.Is(err, ErrGone) {
		err = fmt.Errorf("%w: %w", ErrGone, err)
	}
	s.detach(err)
	s.scanBackoff = 0
	s.enter(NoReceiver, err)
	s.due.scan = time.Now().Add(s.tm.Rescan)
}

func (s *Session) detach(err error) {
	s.closeChoices(nil)
	if s.dev != nil {
		s.dev.close()
		s.dev = nil
	}
	s.failJobs(err)
	s.forget()
}

func (s *Session) noReceiver(err error) {
	s.enter(NoReceiver, err)
	s.due.scan = later(&s.scanBackoff, s.tm.Rescan, s.tm.RescanMax)
}

// forgetMouse drops what belongs to the paired mouse; forget drops the
// receiver's state too.
func (s *Session) forgetMouse() {
	s.hs, s.model, s.idAddr = nil, nil, [3]byte{}
	s.image, s.shown, s.unread = nil, nil, nil
	s.battery, s.profile, s.longRange = nil, Probe{}, Probe{}
	s.versions.Mouse = ""
	s.dirty = true
}

// forget keeps a Conflict: it lasts across a reattach of the same device
// until ClearConflict (keepConflict).
func (s *Session) forget() {
	s.forgetMouse()
	s.versions = Versions{}
	s.online, s.addr = false, [3]byte{}
	s.runs, s.clients, s.writeRun = runs{}, nil, 0
	if s.conflict != Conflict {
		s.endConflict()
		s.lastForeign = time.Time{}
	}
}

// scanClients refreshes the foreign clients of the device and reports
// whether there are none.
func (s *Session) scanClients() bool {
	if s.opt.Clients == nil || s.dev == nil {
		return true
	}
	cs, err := s.opt.Clients(s.dev.c)
	if err != nil {
		s.log.Warn("client scan", "err", err)
		return false
	}
	s.clients, s.dirty = cs, true
	return len(cs) == 0
}

func later(b *time.Duration, first, limit time.Duration) time.Time {
	if *b < first {
		*b = first
	} else {
		*b = min(2**b, limit)
	}
	return time.Now().Add(*b)
}

func query(t wire.Target, c wire.Cmd) wire.Packet { return wire.MustBuild(t, c, 0, nil) }

func handshake(t wire.Target) wire.Packet {
	n := rand.Uint32()
	return wire.MustBuild(t, wire.CmdHandshake, 0, []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24), 0, 0, 0, 0})
}

func parseHandshake(p wire.Packet) Handshake { return Handshake{CID: p[9], MID: p[10], Conn: p[11]} }
