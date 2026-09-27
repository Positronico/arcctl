package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

// link is one open interface and the requests recently sent on it.
type link struct {
	c      hidio.Candidate
	tr     hidio.Transport
	target wire.Target
	cur    *wire.Packet
	recent []sent

	online      bool
	addr        [3]byte
	hs          *Handshake
	model       *catalog.Model
	foreign     int
	lastForeign time.Time
	stalled     bool
}

// sent is a request that completed or timed out, kept for Timing.Window so
// that a second or late reply to it is recognised.
type sent struct {
	p        wire.Packet
	at       time.Time
	timedOut bool
}

func (l *link) goneErr() error {
	if err := l.tr.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrGone, err)
	}
	return ErrGone
}

func (l *link) close() {
	if l.stalled {
		go l.tr.Close()
		return
	}
	l.tr.Close()
}

func (l *link) remember(p wire.Packet, timedOut bool, window time.Duration) {
	now := time.Now()
	l.recent = slices.DeleteFunc(l.recent, func(e sent) bool { return now.Sub(e.at) > window })
	l.recent = append(l.recent, sent{p: p, at: now, timedOut: timedOut})
}

func (l *link) explains(p wire.Packet, timedOut bool, window time.Duration) bool {
	now := time.Now()
	return slices.ContainsFunc(l.recent, func(e sent) bool {
		return e.timedOut == timedOut && now.Sub(e.at) <= window && wire.Match(e.p, p)
	})
}

// txn is the outcome of one transaction: the reply, and how many tries were
// sent and went unanswered before it (or in total).
type txn struct {
	rep    wire.Packet
	failed int
	err    error
}

// errWrite marks the error of a write the OS or the device refused, which
// says nothing about the replies: the request never went out.
var errWrite = errors.New("write failed")

// transact sends req on l and waits for its reply: stale reports are drained
// first, each try waits per, and reports that do not answer req are
// dispatched without using up the try. A NAK ends the transaction with
// wire.ErrNAK; running out of tries ends it with ErrNoReply, or with the
// write error when no try was sent at all. A write that times out uses up the
// try; any other write error ends the transaction.
func (s *Session) transact(ctx context.Context, l *link, req wire.Packet, tries int, per time.Duration) txn {
	s.flush()
	if err := s.drain(l); err != nil {
		return txn{err: err}
	}
	if s.guard.Target() != l.target {
		if err := s.guard.SetTarget(l.target, "talk to "+l.c.Path); err != nil {
			return txn{err: err}
		}
	}
	s.stats.Transactions++
	s.dirty = true
	l.cur = &req
	defer func() { l.cur = nil }()
	var t txn
	var werr error
	sent, got := false, false
	for range tries {
		if err := l.tr.Write(req); err != nil {
			if hidio.Classify(err) != hidio.ClassRefused {
				s.stats.WriteErrors++
				err = fmt.Errorf("%w: %w", errWrite, err)
			}
			if hidio.Classify(err) == hidio.ClassTimeout {
				werr = err
				continue
			}
			t.err = err
			break
		}
		sent = true
		rep, ok, err := s.await(ctx, l, req, per)
		if err != nil {
			t.err = err
			break
		}
		if ok {
			t.rep, got = rep, true
			if rep.Status() == wire.StatusNAK {
				s.stats.NAKs++
				t.err = fmt.Errorf("%v: %w", req.Cmd(), wire.ErrNAK)
			}
			break
		}
		t.failed++
	}
	switch {
	case t.err != nil || got:
	case !sent && werr != nil:
		t.err = werr
	default:
		t.err = fmt.Errorf("%w to %v after %d tries", ErrNoReply, req.Cmd(), tries)
	}
	s.stats.FailedTries += t.failed
	if !sent {
		return t
	}
	if l == s.dev {
		s.writeRun = 0
		s.noteClean(t, got)
	}
	l.remember(req, errors.Is(t.err, ErrNoReply), s.tm.Window)
	return t
}

func (s *Session) await(ctx context.Context, l *link, req wire.Packet, per time.Duration) (wire.Packet, bool, error) {
	expire := time.NewTimer(per)
	defer expire.Stop()
	for {
		select {
		case <-ctx.Done():
			return wire.Packet{}, false, ctx.Err()
		case r, ok := <-l.tr.Reports():
			if !ok {
				return wire.Packet{}, false, l.goneErr()
			}
			p, ok := r.Packet()
			if !ok {
				continue
			}
			if answers(req, p) {
				return s.reply(p), true, nil
			}
			s.dispatch(l, p)
		case <-l.tr.Wake():
			s.wakeHint = true
		case <-expire.C:
			return s.queued(l, req)
		}
	}
}

// queued looks, once a try has expired, through the reports that arrived in
// time but were not read yet, so a busy host does not lose a reply.
func (s *Session) queued(l *link, req wire.Packet) (wire.Packet, bool, error) {
	for {
		select {
		case r, ok := <-l.tr.Reports():
			if !ok {
				return wire.Packet{}, false, l.goneErr()
			}
			if p, ok := r.Packet(); ok {
				if answers(req, p) {
					return s.reply(p), true, nil
				}
				s.dispatch(l, p)
			}
		default:
			return wire.Packet{}, false, nil
		}
	}
}

func (s *Session) reply(p wire.Packet) wire.Packet {
	if !p.Valid() {
		s.stats.BadChecksums++
		s.log.Warn("reply checksum", "reply", p)
	}
	return p
}

// drain dispatches every report that arrived before a request is sent.
func (s *Session) drain(l *link) error {
	for {
		select {
		case r, ok := <-l.tr.Reports():
			if !ok {
				return l.goneErr()
			}
			if p, ok := r.Packet(); ok {
				s.dispatch(l, p)
			}
		case <-l.tr.Wake():
			s.wakeHint = true
		default:
			return nil
		}
	}
}

// dispatch classifies a report-8 frame that is not the reply being waited for.
func (s *Session) dispatch(l *link, p wire.Packet) {
	s.dirty = true
	switch {
	case p.Cmd() == wire.CmdStatusChanged:
		s.onPush(l, p)
	case l.cur != nil && wire.Match(*l.cur, p):
		s.stats.OddStatus++
		s.log.Warn("reply with an unknown status", "reply", p)
	case l.explains(p, false, s.tm.Window):
		s.stats.Duplicates++
		s.log.Info("duplicate reply", "reply", p)
	case l.explains(p, true, s.tm.Window):
		s.stats.Late++
		s.log.Info("late reply", "reply", p)
	case p.Cmd() == wire.CmdClear && l == s.dev && !s.resetAt.IsZero() && time.Since(s.resetAt) < resetLate:
		s.stats.Late++
		s.log.Info("late reply to the factory reset", "reply", p)
	case p.Cmd() == wire.CmdOnline:
		s.stats.PossiblePushes++
		s.log.Info("unrequested cmd-3 report", "report", p)
		if l == s.dev && p[5] == 1 {
			s.wakeHint = true
		}
	case p.Status() == wire.StatusNAK:
		s.stats.OddStatus++
		s.log.Warn("NAK that answers no request", "reply", p)
	default:
		s.stats.Foreign++
		l.foreign++
		l.lastForeign = time.Now()
		s.log.Warn("foreign reply", "reply", p, "path", l.c.Path)
		if l == s.dev {
			s.foreignSeen(l)
		}
	}
}

// answers reports whether p ends the transaction of req: a reply that matches
// it, or a NAK for the same command (and address, for cmds 7 and 8) whatever
// length it echoes. The NAK layout is unknown until H1, and the web app ends
// a transaction on any status-1 frame.
func answers(req, p wire.Packet) bool {
	switch p.Status() {
	case wire.StatusOK:
		return wire.Match(req, p)
	case wire.StatusNAK:
		addressed := req.Cmd() == wire.CmdWrite || req.Cmd() == wire.CmdRead
		return p.Cmd() == req.Cmd() && (!addressed || p.Addr() == req.Addr())
	}
	return false
}

func online(p wire.Packet) (bool, [3]byte) {
	return p[5] == 1, [3]byte{p[8], p[7], p[6]}
}

func version(p wire.Packet) string { return fmt.Sprintf("v%d.%02x", p[5], p[6]) }
