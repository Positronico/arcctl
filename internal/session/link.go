package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// writeLink is the session as the executor sees it: strict transactions on
// the attached device, with no cmd 7 during a Conflict or SuspectedConflict,
// a fresh cmd 3 that identifies the mouse again once it has slept, and the
// pushes and abort requests that arrived since the last call. It runs on the
// owner goroutine, inside a write, its checks or a journal inspection.
type writeLink struct {
	s       *Session
	ctx     context.Context // the caller's: once it is done, the run is asked to stop
	id      plan.Identity
	profile *byte
	gates   safety.Gates
	slept   bool
	aborted bool

	signals  []safety.Signal
	rereads  []flash.Extent
	switched bool // a push said the onboard profile changed
}

func (s *Session) newLink(ctx context.Context, id plan.Identity, profile *byte, g safety.Gates) *writeLink {
	w := &writeLink{s: s, ctx: ctx, id: id, profile: profile, gates: g}
	s.wl = w
	return w
}

// release ends the link: pushes it held back get their usual handling.
func (s *Session) release(w *writeLink) {
	if s.wl != w {
		return
	}
	s.wl = nil
	switch {
	case s.dev == nil:
	case w.switched:
		s.profileSwitched()
	case len(w.rereads) > 0 && s.model != nil && s.model.Family == catalog.FamilyMouse:
		s.addJob(&job{kind: jobReread, want: w.rereads})
	}
}

func (w *writeLink) Transact(ctx context.Context, p wire.Packet) (wire.Packet, error) {
	s := w.s
	switch {
	case s.dev == nil:
		return wire.Packet{}, ErrNotConnected
	case p.Cmd() != wire.CmdWrite:
	case s.conflict == Conflict:
		return wire.Packet{}, fmt.Errorf("%w: a reply arcctl did not ask for arrived", safety.ErrConflict)
	case s.conflict == SuspectedConflict:
		return wire.Packet{}, fmt.Errorf("%w: replies went missing", safety.ErrConflict)
	}
	t := s.transact(ctx, s.dev, p, s.tm.Tries, s.tm.Try)
	switch {
	case errors.Is(t.err, ErrNoReply):
		return t.rep, fmt.Errorf("%w: %w", safety.ErrTimeout, t.err)
	case (t.err == nil || errors.Is(t.err, wire.ErrNAK)) && t.failed >= suspectMouse && p.Cmd() != wire.CmdOnline:
		s.suspect(fmt.Sprintf("%v needed %d tries", p.Cmd(), t.failed+1))
	}
	return t.rep, t.err
}

// OnlineCheck sends cmd 3. After the mouse slept it also handshakes and asks
// for the profile again, since another mouse or profile may have woken up.
func (w *writeLink) OnlineCheck(ctx context.Context) error {
	s := w.s
	if s.dev == nil {
		return ErrNotConnected
	}
	on, err := s.checkOnline(ctx)
	switch {
	case err != nil:
		return err
	case !on:
		w.slept = true
		return fmt.Errorf("%w: cmd 3 says the mouse sleeps", safety.ErrOffline)
	case !w.slept:
		return nil
	}
	t := s.transact(ctx, s.dev, handshake(s.dev.target), s.tm.Tries, s.tm.Try)
	if t.err != nil {
		return fmt.Errorf("handshake after the mouse woke: %w", t.err)
	}
	h := parseHandshake(t.rep)
	now := plan.Identity{CID: h.CID, MID: h.MID, Addr: s.addr, AddrTrusted: s.opt.TrustAddress, VID: w.id.VID, PID: w.id.PID}
	if now.Key() != w.id.Key() {
		return fmt.Errorf("%w: %s woke up in place of %s", safety.ErrIdentity, now.Key(), w.id.Key())
	}
	profile, err := s.askProfile(ctx)
	if err != nil {
		return err
	}
	if !sameProfile(profile, w.profile) {
		w.switched = true
		return fmt.Errorf("%w: the mouse woke up on profile %s, the write began on %s", safety.ErrProfile, profileName(profile), profileName(w.profile))
	}
	w.slept = false
	return nil
}

// Recheck asks again, after a pause, what the preflight asked the OS: no
// Conflict, no other client on the receiver unless the write allows one, the
// lock and the console (I10, §6.4).
func (w *writeLink) Recheck(context.Context) error {
	s := w.s
	if s.dev == nil {
		return ErrNotConnected
	}
	f := safety.Facts{State: s.conflictState()}
	if !w.gates.DryRun && !w.gates.AllowForeignClient {
		f.Clients, f.ScanErr = s.foreignClients()
	}
	f.Lock, f.Console, f.ConsoleErr = s.hostFacts()
	if err := safety.Recheck(f, w.gates); err != nil {
		s.log.Warn("the checks after a pause failed", "err", err)
		return err
	}
	return nil
}

// Pending hands over what the pushes and the user asked for since the last
// call: a push re-reads a range or switches the profile, and an Abort or a
// cancelled caller asks the run to stop.
func (w *writeLink) Pending() []safety.Signal {
	s := w.s
	if s.dev != nil {
		_ = s.drain(s.dev)
	}
	if !w.aborted && (s.abort.Swap(false) || w.ctx.Err() != nil) {
		w.aborted = true
		w.signals = append(w.signals, safety.Signal{Kind: safety.SignalAbort})
	}
	out := w.signals
	w.signals = nil
	return out
}

// push turns a StatusChanged push that arrived during a write into signals.
func (w *writeLink) push(f1, f2 byte) {
	if f1&pushProfile != 0 {
		w.switched = true
		w.signals = append(w.signals, safety.Signal{Kind: safety.SignalProfile})
	}
	if e := rereads(f1, f2); len(e) > 0 {
		w.rereads = append(w.rereads, e...)
		w.signals = append(w.signals, safety.Signal{Kind: safety.SignalReread, Extents: e})
	}
}

// askProfile sends cmd 14; a NAK means the mouse has no profiles.
func (s *Session) askProfile(ctx context.Context) (*byte, error) {
	t := s.transact(ctx, s.dev, query(s.dev.target, wire.CmdGetProfile), s.tm.Tries, s.tm.Try)
	switch {
	case errors.Is(t.err, wire.ErrNAK):
		return nil, nil
	case t.err != nil:
		return nil, fmt.Errorf("cmd 14: %w", t.err)
	}
	v := t.rep[5]
	return &v, nil
}

// readExtents reads extents through l, at most 10 bytes a packet.
func readExtents(ctx context.Context, l safety.Link, extents []flash.Extent) (*flash.Image, error) {
	im := flash.New()
	for _, e := range extents {
		for a := e.Addr; a < e.End(); a += wire.MaxData {
			n := min(wire.MaxData, e.End()-a)
			p, err := wire.BuildRead(wire.Mouse, uint16(a), n)
			if err != nil {
				return nil, err
			}
			rep, err := l.Transact(ctx, p)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", e, err)
			}
			if rep.Len() != n {
				return nil, fmt.Errorf("reading %s: %w: %d bytes for a read of %d", e, safety.ErrReply, rep.Len(), n)
			}
			if err := im.Set(a, rep.Data()); err != nil {
				return nil, err
			}
		}
	}
	return im, nil
}

func sameProfile(a, b *byte) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func profileName(p *byte) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}
