package hidio

import (
	"sync"
	"sync/atomic"

	"github.com/positronico/arcctl/internal/wire"
)

// Transport is the only way arcctl sends packets to a device. Only Guarded
// makes one.
//
// Write checks p against the guard before any I/O. Reports carries only
// report-8 frames of 16 bytes; every other input report, and any activity the
// Raw coalesced itself, becomes one pending signal on Wake. Reports is closed
// when input stops, and Err then says why (nil after Close). Dropped counts the
// frames discarded because they were not read in time, plus the reports of any
// ID a backend lost before it could tell them apart.
type Transport interface {
	Write(p wire.Packet) error
	Reports() <-chan Report
	Wake() <-chan struct{}
	Err() error
	Dropped() uint64
	Close() error
	guarded()
}

// Guarded wraps r so that every packet passes g before it reaches r.
func Guarded(r Raw, g *Guard) Transport {
	if r == nil || g == nil {
		panic("hidio: Guarded needs a Raw and a Guard")
	}
	t := &transport{
		raw:   r,
		guard: g,
		q:     newQueue(),
		wake:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go t.forward()
	return t
}

type transport struct {
	raw       Raw
	guard     *Guard
	q         *queue
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (*transport) guarded() {}

func (t *transport) Write(p wire.Packet) error {
	if err := t.guard.Check(p); err != nil {
		return err
	}
	if t.closed.Load() {
		return ErrClosed
	}
	return t.raw.WriteRaw(p)
}

func (t *transport) Reports() <-chan Report { return t.q.ch }

func (t *transport) Wake() <-chan struct{} { return t.wake }

func (t *transport) Err() error { return rawErr(t.raw) }

func (t *transport) Dropped() uint64 { return t.q.dropped.Load() + rawDropped(t.raw) }

// Close closes the Raw, which applies its backend's close order, then stops
// the forwarder.
func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		t.closeErr = t.raw.Close()
		close(t.stop)
		<-t.done
	})
	return t.closeErr
}

func (t *transport) forward() {
	defer close(t.done)
	defer t.q.close()
	in, others, wake := t.raw.Reports(), rawOthers(t.raw), rawWake(t.raw)
	for {
		select {
		case <-t.stop:
			return
		case r, ok := <-in:
			if !ok {
				return
			}
			if _, ok := r.Packet(); ok {
				t.q.push(r)
			} else {
				poke(t.wake)
			}
		case _, ok := <-others:
			if !ok {
				others = nil
				continue
			}
			poke(t.wake)
		case _, ok := <-wake:
			if !ok {
				wake = nil
				continue
			}
			poke(t.wake)
		}
	}
}
