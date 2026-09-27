package session_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// crash runs p on s and makes it die, as a killed arcctl would, when kill
// says so. It returns the run's journal ID.
func crash(t *testing.T, s *session.Session, p plan.Plan, kill func(safety.OpEvent) bool) string {
	t.Helper()
	var run string
	_, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
		run = e.Run
		if kill(e) {
			panic("killed")
		}
	})
	if !errors.Is(err, session.ErrPanic) || run == "" {
		t.Fatalf("err = %v, run %q; want the write killed", err, run)
	}
	return run
}

// A torn record found at startup keeps the session Recovering and blocks
// writes until the user settles it; finishing the run writes what it meant
// to leave.
func TestStartupRecoveryOfATornWrite(t *testing.T) {
	r := newRig(t)
	s, stop := r.run(nil)
	sn := await(t, s, "ready", idle)
	p := mixed(t, sn)
	want := afterPlan(t, sn.Image, p)
	run := crash(t, s, p, func(e safety.OpEvent) bool {
		return e.Kind == safety.EventChunk && e.Op.Phase == plan.Body && e.Chunk == 1
	})
	stop()

	s2, _ := r.run(nil)
	sn = await(t, s2, "recovering at startup", in(session.Recovering))
	if len(sn.Journal.Open) != 1 || sn.Journal.Open[0].Run.ID != run {
		t.Fatalf("open runs %+v, want %s", sn.Journal.Open, run)
	}
	in := sn.Journal.Open[0].Inspection
	if in == nil || !in.Torn() {
		t.Fatalf("inspection %+v, want the torn body", in)
	}
	next := planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1})
	if _, err := s2.Apply(ctxT(t), next, allow(next), nil); !errors.Is(err, safety.ErrNotClean) {
		t.Fatalf("apply while recovering: %v", err)
	}
	n := len(r.cmd7())
	out, err := s2.Recover(ctxT(t), run, safety.Forward, safety.Gates{DryRun: true}, nil)
	if err != nil || len(out.Packets) == 0 || len(r.cmd7()) != n {
		t.Fatalf("dry-run recovery: %v, %d packets, %d writes", err, len(out.Packets), len(r.cmd7())-n)
	}
	var policies []wire.Policy
	out, err = s2.Recover(ctxT(t), run, safety.Forward, safety.Gates{}, func(safety.OpEvent) {
		policies = append(policies, s2.Snapshot().Policy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Run == "" || len(policies) == 0 || policies[len(policies)-1] != wire.Edit || s2.Snapshot().Policy != wire.ReadOnly {
		t.Fatalf("recovery run %q, policies %v then %v", out.Run, policies, s2.Snapshot().Policy)
	}
	holds(t, "device after finishing the run", r.dev.Image(), want, p)
	sn = await(t, s2, "clean", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil && len(sn.Journal.Open) == 0 })
	if sn.State != session.Ready || sn.Journal.Last == nil || sn.Journal.Last.ID != run {
		t.Fatalf("state %v, last %+v", sn.State, sn.Journal.Last)
	}
	checkBodies(t, r)
}

// A run killed after its last op was verified left the device as it meant
// to. The next session settles it without asking and without writing.
func TestStartupSettlesAFinishedRun(t *testing.T) {
	r := newRig(t)
	s, stop := r.run(nil)
	sn := await(t, s, "ready", idle)
	p := mixed(t, sn)
	last := p.Ops[len(p.Ops)-1].Seq
	run := crash(t, s, p, func(e safety.OpEvent) bool {
		if e.Kind == safety.EventVerified && e.Op.Seq == last {
			r.dev.Sleep()
			return true
		}
		return false
	})
	stop()
	r.dev.Wake()
	n := len(r.cmd7())

	s2, _ := r.run(nil)
	sn = await(t, s2, "settled", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil })
	if sn.State != session.Ready || len(sn.Journal.Open) != 0 || sn.Journal.Last == nil || sn.Journal.Last.ID != run {
		t.Fatalf("state %v, journal %+v", sn.State, sn.Journal)
	}
	if st := journalOf(t, r, sn); !st.Clean() || st.Find(run).Resolved != safety.Forward {
		t.Fatalf("journal: open %v, resolved %v", st.Open, st.Find(run).Resolved)
	}
	if len(r.cmd7()) != n {
		t.Fatal("settling a finished run wrote")
	}
}

// A run killed before its first packet changed nothing: the session settles
// it back on its own, and nothing is left to revert.
func TestFailedApplyThatWroteNothingSettlesBack(t *testing.T) {
	r := newRig(t)
	s, sn := r.ready(nil)
	p := mixed(t, sn)
	run := crash(t, s, p, func(e safety.OpEvent) bool { return e.Kind == safety.EventStart })
	sn = await(t, s, "settled", func(sn *session.Snapshot) bool { return idle(sn) && sn.Journal != nil })
	if sn.State != session.Ready || len(sn.Journal.Open) != 0 || sn.Journal.Last != nil {
		t.Fatalf("state %v, journal %+v", sn.State, sn.Journal)
	}
	if st := journalOf(t, r, sn); st.Find(run).Resolved != safety.Back {
		t.Fatalf("resolved %v", st.Find(run).Resolved)
	}
	if len(r.cmd7()) != 0 || !bytes.Equal(r.dev.Image().Bytes(), r.start.Bytes()) {
		t.Fatal("the device changed")
	}
}

// checkBodies fails when a binding points at a shortcut body that does not
// decode.
func checkBodies(t *testing.T, r *rig) {
	t.Helper()
	im := r.dev.Image()
	for k := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(k)
		b, _ := im.Get(e)
		fn, err := mouse.DecodeKeyFn(b)
		if err != nil || fn.Type != mouse.TypeShortcut {
			continue
		}
		be, _ := mouse.ShortcutExtent(k)
		body, _ := im.Get(be)
		if _, err := mouse.DecodeShortcut(body); err != nil {
			t.Errorf("binding %d points at a body that does not decode: %v", k, err)
		}
	}
}
