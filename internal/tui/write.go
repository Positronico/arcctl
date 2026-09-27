package tui

import (
	"context"
	"fmt"
	"slices"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// writeRequest asks the shell to start a write: an apply of plan, a revert
// of the journal's last run (plan is then its preview), or a recovery of
// run. The session's Apply, Revert or Recover runs it: its preflight, the
// backups, the journal and the read-back. Progress arrives as OpEventMsg and
// the end as WriteDoneMsg.
type writeRequest struct {
	kind  safety.RunKind
	plan  plan.Plan
	run   string
	how   safety.Strategy
	gates safety.Gates
	ops   int // the records a recovery expects to write; 0 when unknown
	// from is the open dialog where the user approved this write; the shell
	// starts nothing else (D2).
	from approver
}

// approver is a dialog that approves the writes the user confirmed in it,
// each once.
type approver interface {
	Dialog
	approves(r writeRequest) bool
}

func (r writeRequest) cmd() tea.Cmd { return func() tea.Msg { return r } }

// sameRequest reports whether r asks for the write w describes.
func sameRequest(r, w writeRequest) bool {
	return r.kind == w.kind && r.run == w.run && r.how == w.how && r.gates == w.gates && reviewSamePlan(r.plan, w.plan)
}

// events queues the executor's events, then the write's end, without ever
// blocking the session goroutine that reports them. One listener drains it,
// so the shell sees every event before the end.
type events struct {
	mu    sync.Mutex
	queue []safety.OpEvent
	end   *WriteDoneMsg
	ready chan struct{}
}

func newEvents() *events { return &events{ready: make(chan struct{}, 1)} }

func (q *events) push(e safety.OpEvent) {
	q.mu.Lock()
	q.queue = append(q.queue, e)
	q.mu.Unlock()
	q.signal()
}

func (q *events) finish(d WriteDoneMsg) {
	q.mu.Lock()
	q.end = &d
	q.mu.Unlock()
	q.signal()
}

func (q *events) signal() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

type eventsMsg struct {
	q      *events
	events []safety.OpEvent
	end    *WriteDoneMsg
}

func (q *events) listen() tea.Cmd {
	return func() tea.Msg {
		<-q.ready
		q.mu.Lock()
		defer q.mu.Unlock()
		m := eventsMsg{q: q, events: q.queue, end: q.end}
		q.queue = nil
		return m
	}
}

func runWrite(ctx context.Context, api session.API, r writeRequest, q *events) tea.Cmd {
	return func() tea.Msg {
		var (
			out session.Outcome
			err error
		)
		switch r.kind {
		case safety.KindApply:
			out, err = api.Apply(ctx, r.plan, r.gates, q.push)
		case safety.KindRevert:
			out, err = api.Revert(ctx, r.gates, q.push)
		case safety.KindRecover:
			out, err = api.Recover(ctx, r.run, r.how, r.gates, q.push)
		}
		q.finish(WriteDoneMsg{Kind: r.kind, Outcome: out, Err: err})
		return nil
	}
}

// opStep is where one op of the running write got to.
type opStep struct {
	op     plan.Op
	kind   safety.EventKind // 0 while planned
	chunk  int
	chunks int
}

// writing is the running write as its events describe it.
type writing struct {
	kind   safety.RunKind
	dryRun bool
	run    string
	steps  []opStep
	total  int
	cur    int
	paused error
	abort  bool
	q      *events
}

func newWriting(r writeRequest, q *events) *writing {
	w := &writing{kind: r.kind, dryRun: r.gates.DryRun, cur: -1, q: q, total: r.ops}
	if r.kind != safety.KindRecover {
		for _, op := range r.plan.Ops {
			w.steps = append(w.steps, opStep{op: op})
		}
	}
	return w
}

// progress is how many records are verified, out of how many when that is
// known.
func (w *writing) progress() string {
	if n := max(w.total, len(w.steps)); n > 0 {
		return fmt.Sprintf("%d of %d records verified", w.verified(), n)
	}
	return plural(w.verified(), "record verified", "records verified")
}

func (w *writing) add(e safety.OpEvent) {
	if e.Run != "" {
		w.run = e.Run
	}
	i := slices.IndexFunc(w.steps, func(s opStep) bool { return s.op.Seq == e.Op.Seq })
	if i < 0 {
		w.steps = append(w.steps, opStep{op: e.Op})
		i = len(w.steps) - 1
	}
	s := &w.steps[i]
	w.cur = i
	w.paused = nil
	switch e.Kind {
	case safety.EventPaused:
		w.paused = e.Err
		return
	case safety.EventChunk:
		s.chunk, s.chunks = e.Chunk, e.Chunks
	case safety.EventStart, safety.EventRestart:
		s.chunk = 0
	}
	s.kind = e.Kind
}

func (w *writing) verified() int {
	n := 0
	for _, s := range w.steps {
		if s.kind == safety.EventVerified {
			n++
		}
	}
	return n
}
