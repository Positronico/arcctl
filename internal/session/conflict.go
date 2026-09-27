package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
)

// Thresholds of the conflict signals, set from H0. The receiver answers cmd 3
// itself within 10 ms and never left one unanswered, so two unanswered tries
// in a row point at another client taking the replies. A mouse command must
// go unanswered 4 times before its fifth try answers: at a 2.5% loss per try
// that happens once in 2.6 million transactions, while a client that steals
// half the replies trips it once in 16.
const (
	suspectCmd3  = 2 // consecutive unanswered cmd-3 tries
	suspectMouse = 4 // unanswered tries of one mouse command while the mouse is online
	cleanToClear = 3 // consecutive clean transactions before SuspectedConflict can clear
)

type runs struct {
	cmd3  int
	clean int
}

func (s *Session) noteCmd3(t txn) {
	if t.err != nil && !errors.Is(t.err, ErrNoReply) {
		return
	}
	s.runs.cmd3 += t.failed
	if s.runs.cmd3 >= suspectCmd3 {
		s.suspect(fmt.Sprintf("%d cmd-3 tries in a row went unanswered", s.runs.cmd3))
	}
	if t.err == nil {
		s.runs.cmd3 = 0
	}
}

// noteClean counts transactions answered on their first try.
func (s *Session) noteClean(t txn, answered bool) {
	if answered && t.failed == 0 {
		s.runs.clean++
		return
	}
	s.runs.clean = 0
}

func (s *Session) suspect(why string) {
	s.runs.clean = 0
	if s.conflict != 0 {
		return
	}
	s.log.Warn("suspected conflict", "why", why)
	s.conflict, s.conflictSince = SuspectedConflict, time.Now()
	s.due.suspect = time.Now().Add(s.tm.Suspect)
	s.dirty = true
}

func (s *Session) foreignSeen(l *link) {
	s.lastForeign = l.lastForeign
	s.dirty = true
	if s.conflict == Conflict {
		return
	}
	s.log.Warn("conflict: a reply arcctl did not ask for", "path", l.c.Path)
	s.conflict, s.conflictSince = Conflict, time.Now()
	s.conflictOn = device{path: l.c.Path, vid: l.c.VID, pid: l.c.PID}
	if s.hs != nil {
		s.conflictOn.cid, s.conflictOn.mid = s.hs.CID, s.hs.MID
	}
}

// device is what tells two attachments apart: the interface path, or else
// the VID, PID and the handshake's cid and mid.
type device struct {
	path     string
	vid, pid uint16
	cid, mid byte
}

// keepConflict decides, when a device is attached or identified, whether a
// Conflict seen earlier still applies. It follows a reattach of the same
// device, whose competitor may still be there, and ends when another device
// takes its place. Only ClearConflict ends it otherwise.
func (s *Session) keepConflict(c hidio.Candidate, hs *Handshake) {
	on := &s.conflictOn
	if s.conflict != Conflict || c.Path == on.path {
		if s.conflict == Conflict && hs != nil && on.cid == 0 {
			on.cid, on.mid = hs.CID, hs.MID
		}
		return
	}
	other := c.VID != on.vid || c.PID != on.pid
	if hs != nil && on.cid != 0 {
		other = other || hs.CID != on.cid || hs.MID != on.mid
	}
	if other {
		s.log.Info("conflict dropped: another device is attached", "was", on.path, "now", c.Path)
		s.endConflict()
		s.lastForeign = time.Time{}
	}
}

func (s *Session) endConflict() {
	s.conflict, s.conflictSince, s.conflictOn = 0, time.Time{}, device{}
	s.dirty = true
}

// checkSuspect runs while SuspectedConflict: it polls cmd 3, and once enough
// transactions in a row were clean and the client scan shows nobody else, the
// suspicion clears.
func (s *Session) checkSuspect(ctx context.Context) {
	if _, err := s.checkOnline(ctx); err != nil && !errors.Is(err, ErrNoReply) {
		s.fail(err)
		return
	}
	if s.conflict != SuspectedConflict || s.runs.clean < cleanToClear || !s.scanClients() {
		return
	}
	s.log.Info("suspected conflict cleared")
	s.conflict, s.conflictSince = 0, time.Time{}
	s.dirty = true
}

func (s *Session) clearConflict() error {
	if s.conflict != Conflict {
		return nil
	}
	if quiet := time.Since(s.lastForeign); quiet < s.tm.ConflictQuiet {
		return fmt.Errorf("%w: quiet for %v, %v needed", ErrConflict, quiet.Round(time.Millisecond), s.tm.ConflictQuiet)
	}
	s.log.Info("conflict cleared by the user")
	s.endConflict()
	s.runs = runs{}
	return nil
}
