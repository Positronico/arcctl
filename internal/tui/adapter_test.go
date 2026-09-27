package tui

import (
	"context"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

func TestWatcherTakesTheLatestSnapshot(t *testing.T) {
	fake := newFake(bare(session.Probing))
	w := watcher{ctx: context.Background(), api: fake, every: 5 * time.Millisecond}
	got := make(chan any, 1)
	go func() { got <- w.next()() }()
	for i := range 50 {
		sn := bare(session.Loading)
		sn.Seq = uint64(i + 1)
		fake.set(sn)
	}
	select {
	case msg := <-got:
		sn := msg.(watchMsg).sn
		if sn.Seq != 50 {
			t.Errorf("snapshot %d, want the latest (50)", sn.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher never returned")
	}
}

func TestWatcherStopsWithTheShell(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := watcher{ctx: ctx, api: newFake(bare(session.Probing)), every: time.Millisecond}
	got := make(chan any, 1)
	go func() { got <- w.next()() }()
	cancel()
	select {
	case msg := <-got:
		if msg != nil {
			t.Errorf("a stopped watcher sent %T", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher outlived its context")
	}
}

// The session reports events on its own goroutine: pushing must never wait
// for the shell.
func TestEventsNeverBlock(t *testing.T) {
	q := newEvents()
	start := time.Now()
	for i := range 100000 {
		q.push(safety.OpEvent{Chunk: i})
	}
	q.finish(WriteDoneMsg{Kind: safety.KindApply})
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("pushing took %v", d)
	}
	m := q.listen()().(eventsMsg)
	if len(m.events) != 100000 || m.events[99999].Chunk != 99999 || m.end == nil {
		t.Errorf("%d events, end %v", len(m.events), m.end)
	}
}
