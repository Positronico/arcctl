package session

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// checkJournal reads the device's journal after a load, which is how a
// session finds, at startup or after a failed write, the runs a crash or a
// failure left unfinished. Each one is read again from the device. A run
// whose extents all hold the bytes it meant to leave is settled forward, and
// one whose extents all hold the bytes from before it is settled back; both
// write nothing. Any other run keeps the session Recovering until the user
// settles it with Recover, and blocks writes until then.
func (s *Session) checkJournal(ctx context.Context) {
	if s.opt.Writes == nil || s.dev == nil || s.hs == nil || s.model == nil || s.image == nil {
		s.journalDue = false
		return
	}
	id := s.identity()
	js := &JournalState{}
	defer func() {
		s.jstate, s.journalDue, s.dirty = js, false, true
	}()
	j, err := s.journal(id)
	if err != nil {
		js.Err = err
		return
	}
	st, err := j.Status()
	if err != nil {
		js.Err = err
		return
	}
	if len(st.Open) > 0 {
		profile := s.profilePtr()
		w := s.newLink(context.Background(), id, profile, safety.Gates{})
		defer s.release(w)
		x := safety.NewExecutor(w, j, s.execOptions())
		dev := safety.Device{Identity: id, Profile: profile, Image: s.image, Layout: mouse.Layout(s.model)}
		settled := false
		for _, r := range st.Open {
			in, err := x.Inspect(ctx, r, dev)
			if err == nil {
				if how, ok := settle(in); ok {
					if _, err = x.Recover(ctx, r, how, dev, nil); err == nil {
						s.log.Info("journal: unfinished run settled", "run", r.ID, "as", how)
						settled = true
						continue
					}
				}
			}
			if err != nil {
				s.log.Warn("journal: an unfinished run could not be read again", "run", r.ID, "err", err)
			} else {
				s.log.Warn("journal: an unfinished run needs a decision", "run", r.ID, "torn", in.Torn())
			}
			js.Open = append(js.Open, OpenRun{Run: r, Inspection: in, Err: err})
		}
		if settled {
			if st, err = j.Status(); err != nil {
				js.Err = err
				return
			}
		}
	}
	js.Last, js.Resets = st.Last, st.Unsettled
	if len(st.Unsettled) > 0 {
		s.settleResets(j, st.Unsettled)
	}
	legacy, err := s.legacyRuns(id)
	if err != nil {
		js.Err = err
		return
	}
	for _, r := range legacy {
		js.Open = append(js.Open, OpenRun{Run: r, Err: legacyError([]*safety.Run{r})})
	}
}

// legacyRuns are the unfinished runs of this mouse that the journal kept
// under the identity it had before its address was trusted (I6): trusting
// the address changes the identity key, and with it the journal folder.
// Runs recorded for another address belong to another mouse of the model.
func (s *Session) legacyRuns(id plan.Identity) ([]*safety.Run, error) {
	if !id.AddrTrusted {
		return nil, nil
	}
	old := id
	old.AddrTrusted = false
	st, err := safety.Load(filepath.Join(s.opt.Writes.Journal, old.Key()))
	if err != nil {
		return nil, err
	}
	var out []*safety.Run
	for _, r := range st.Open {
		if r.Device.Addr == id.Addr || r.Device.Addr == [3]byte{} {
			out = append(out, r)
		}
	}
	return out, nil
}

// legacyOpen blocks writes while legacyRuns finds any.
func (s *Session) legacyOpen(id plan.Identity) error {
	runs, err := s.legacyRuns(id)
	if err != nil || len(runs) == 0 {
		return err
	}
	return legacyError(runs)
}

func legacyError(runs []*safety.Run) error {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	return fmt.Errorf("%w: %s was written before the mouse's address was trusted; settle it with address trust off",
		safety.ErrNotClean, strings.Join(ids, ", "))
}

// settle picks the strategy that writes nothing, when there is one: every
// extent holds what the run meant to leave, or every extent holds what it
// held before. A binding the run disabled and bound again to the same body
// holds both at once.
func settle(in *safety.Inspection) (safety.Strategy, bool) {
	all := func(want func(safety.ExtentState) []byte) bool {
		return !slices.ContainsFunc(in.Extents, func(e safety.ExtentState) bool { return !bytes.Equal(e.Found, want(e)) })
	}
	switch {
	case all(func(e safety.ExtentState) []byte { return e.After }):
		return safety.Forward, true
	case all(func(e safety.ExtentState) []byte { return e.Before }):
		return safety.Back, true
	}
	return 0, false
}
