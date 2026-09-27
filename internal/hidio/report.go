// Package hidio moves report-8 packets between arcctl and a HID interface. Device
// backends, transcript replays and emulators all produce a Raw; the only way to
// send commands through one is the Transport that Guarded wraps around it.
package hidio

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// Report is one input report: its report ID, the bytes after the ID, and when
// it arrived.
type Report struct {
	ID   byte
	Data []byte
	At   time.Time
}

// Packet returns the report as a packet when it is a report-8 frame of exactly
// 16 bytes.
func (r Report) Packet() (wire.Packet, bool) {
	var p wire.Packet
	if r.ID != wire.ReportID || len(r.Data) != wire.Size {
		return p, false
	}
	copy(p[:], r.Data)
	return p, true
}

const queueLen = 256

// queue never blocks its producers: when it is full, the oldest report is
// dropped and counted.
type queue struct {
	mu      sync.Mutex
	ch      chan Report
	closed  bool
	dropped atomic.Uint64
}

func newQueue() *queue { return &queue{ch: make(chan Report, queueLen)} }

func (q *queue) push(r Report) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	for {
		select {
		case q.ch <- r:
			return
		default:
		}
		select {
		case <-q.ch:
			q.dropped.Add(1)
		default:
		}
	}
}

func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// inbox is the input side of a Raw. Report-8 frames queue apart from every
// other report, so that a burst of mouse or keyboard input never pushes out a
// reply that arrived before it.
type inbox struct {
	frames *queue
	others *queue
}

func newInbox() inbox { return inbox{frames: newQueue(), others: newQueue()} }

func (b inbox) push(r Report) {
	if _, ok := r.Packet(); ok {
		b.frames.push(r)
	} else {
		b.others.push(r)
	}
}

func (b inbox) close() {
	b.frames.close()
	b.others.close()
}

func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// errBox keeps the first error it is given.
type errBox struct {
	mu  sync.Mutex
	err error
}

func (b *errBox) set(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = err
	}
}

func (b *errBox) get() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}
