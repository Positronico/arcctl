package session

import (
	"context"
	"errors"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/wire"
)

// runDue runs the first timed action that is due in the current state.
func (s *Session) runDue(ctx context.Context) bool {
	now := time.Now()
	due := func(t time.Time) bool { return !now.Before(t) }
	switch {
	case (s.base == NoReceiver || s.base == Stalled) && due(s.due.scan):
		s.connect(ctx)
	case s.base.blocked() && due(s.due.retry):
		s.retry(ctx)
	case s.base == Offline && due(s.due.online):
		s.due.online = now.Add(s.tm.Offline)
		s.pollOnline(ctx)
	case (s.base == Ready || s.base == Unknown) && due(s.due.online):
		s.due.online = now.Add(s.tm.Online)
		s.pollOnline(ctx)
	case s.base == Ready && s.pollsBattery() && due(s.due.battery):
		s.due.battery = now.Add(s.tm.Battery)
		s.pollBattery(ctx)
	case s.conflict == SuspectedConflict && s.base.attached() && due(s.due.suspect):
		s.due.suspect = now.Add(s.tm.Suspect)
		s.checkSuspect(ctx)
	default:
		return false
	}
	return true
}

// nextDue is when runDue next has something to do.
func (s *Session) nextDue() time.Time {
	next := time.Now().Add(time.Hour)
	at := func(t time.Time) {
		if t.Before(next) {
			next = t
		}
	}
	switch {
	case s.base == NoReceiver || s.base == Stalled:
		at(s.due.scan)
	case s.base.blocked():
		at(s.due.retry)
	case s.base == Offline:
		at(s.due.online)
	case s.base == Ready || s.base == Unknown:
		at(s.due.online)
		if s.base == Ready && s.pollsBattery() {
			at(s.due.battery)
		}
	}
	if s.conflict == SuspectedConflict && s.base.attached() {
		at(s.due.suspect)
	}
	return next
}

func (s *Session) pollsBattery() bool {
	return s.model != nil && s.model.Family == catalog.FamilyMouse
}

func (s *Session) pollOnline(ctx context.Context) {
	if _, err := s.checkOnline(ctx); err != nil && !errors.Is(err, ErrNoReply) {
		s.fail(err)
	}
}

func (s *Session) pollBattery(ctx context.Context) {
	t := s.mouseCmd(ctx, query(s.dev.target, wire.CmdBattery))
	switch {
	case t.err == nil:
		s.setBattery(t.rep)
	case errors.Is(t.err, ErrOffline), errors.Is(t.err, ErrNoReply), errors.Is(t.err, wire.ErrNAK):
	default:
		s.fail(t.err)
	}
}

func (s *Session) setBattery(p wire.Packet) {
	b := &Battery{Level: p[5], Charging: p[6] == 1, MilliVolts: uint16(p[7])<<8 | uint16(p[8]), Direct: p[9] == 1, At: time.Now()}
	if b.Direct {
		b.Level = p[10]
	}
	s.battery, s.dirty = b, true
}
