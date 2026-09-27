package safety_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

const (
	tryWait = 40 * time.Millisecond
	tries   = 2
)

var errKilled = errors.New("test: executor killed")

// link is a thin safety.Link over an emulated receiver: strict matching like
// the session's transactions, pushes turned into signals, and hooks that
// make the device misbehave at an exact packet.
type link struct {
	tr  hidio.Transport
	dev *emu.Device

	mu      sync.Mutex
	pending []safety.Signal
	writes  int
	replies map[uint16]wire.Packet // the last reply to a write, by address
	offline int
	checks  int

	// recheck answers Recheck, which the executor calls after every pause;
	// nil lets the run go on.
	recheck  func() error
	rechecks int

	// beforeWrite runs before the n-th cmd 7 (from 1) is sent, afterWrite
	// after its reply arrived; onCheck after every cmd 3 with its result.
	beforeWrite func(n int, p wire.Packet)
	afterWrite  func(n int, p wire.Packet)
	onCheck     func(n int, err error)
}

func openLink(t testing.TB, b *emu.Bus, d *emu.Device) *link {
	t.Helper()
	g := hidio.NewGuard(wire.Mouse)
	if err := g.Set(wire.Edit, "safety tests write through the emulator"); err != nil {
		t.Fatal(err)
	}
	tr, err := b.Open(d.Candidates()[1], g, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return &link{tr: tr, dev: d, replies: map[uint16]wire.Packet{}}
}

func (l *link) Transact(ctx context.Context, p wire.Packet) (wire.Packet, error) {
	if err := l.drain(); err != nil {
		return wire.Packet{}, err
	}
	n := 0
	if p.Cmd() == wire.CmdWrite {
		l.mu.Lock()
		l.writes++
		n = l.writes
		l.mu.Unlock()
		if l.beforeWrite != nil {
			l.beforeWrite(n, p)
		}
	}
	var werr error
	sent := false
	for range tries {
		if err := l.tr.Write(p); err != nil {
			if hidio.Classify(err) == hidio.ClassTimeout {
				werr = err
				continue
			}
			return wire.Packet{}, err
		}
		sent = true
		rep, ok, err := l.await(ctx, p)
		if err != nil {
			return wire.Packet{}, err
		}
		if !ok {
			continue
		}
		if rep.Status() == wire.StatusNAK {
			return rep, fmt.Errorf("%v: %w", p.Cmd(), wire.ErrNAK)
		}
		if n > 0 {
			l.mu.Lock()
			l.replies[p.Addr()] = rep
			l.mu.Unlock()
			if l.afterWrite != nil {
				l.afterWrite(n, p)
			}
		}
		return rep, nil
	}
	if !sent && werr != nil {
		return wire.Packet{}, werr
	}
	return wire.Packet{}, fmt.Errorf("%w to %v after %d tries", safety.ErrTimeout, p.Cmd(), tries)
}

func (l *link) OnlineCheck(ctx context.Context) error {
	rep, err := l.Transact(ctx, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil))
	if err == nil && rep[5] != 1 {
		l.offline++
		err = safety.ErrOffline
	}
	l.checks++
	if l.onCheck != nil {
		l.onCheck(l.checks, err)
	}
	return err
}

func (l *link) Recheck(context.Context) error {
	l.rechecks++
	if l.recheck != nil {
		return l.recheck()
	}
	return nil
}

func (l *link) Pending() []safety.Signal {
	_ = l.drain()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.pending
	l.pending = nil
	return out
}

func (l *link) signal(s safety.Signal) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = append(l.pending, s)
}

func (l *link) await(ctx context.Context, req wire.Packet) (wire.Packet, bool, error) {
	t := time.NewTimer(tryWait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return wire.Packet{}, false, ctx.Err()
		case r, ok := <-l.tr.Reports():
			if !ok {
				return wire.Packet{}, false, l.gone()
			}
			p, ok := r.Packet()
			if !ok {
				continue
			}
			if answers(req, p) {
				return p, true, nil
			}
			l.dispatch(p)
		case <-t.C:
			return wire.Packet{}, false, nil
		}
	}
}

func (l *link) drain() error {
	for {
		select {
		case r, ok := <-l.tr.Reports():
			if !ok {
				return l.gone()
			}
			if p, ok := r.Packet(); ok {
				l.dispatch(p)
			}
		default:
			return nil
		}
	}
}

func (l *link) gone() error {
	if err := l.tr.Err(); err != nil {
		return err
	}
	return hidio.ErrClosed
}

// dispatch turns a StatusChanged push into a signal; every other frame that
// answers nothing (duplicates, late and stale replies) is dropped.
func (l *link) dispatch(p wire.Packet) {
	if p.Cmd() != wire.CmdStatusChanged {
		return
	}
	switch {
	case p[5]&0x04 != 0:
		l.signal(safety.Signal{Kind: safety.SignalProfile})
	case p[5]&0x01 != 0:
		l.signal(safety.Signal{Kind: safety.SignalReread, Extents: []flash.Extent{{Addr: 4, Len: 2}}})
	}
}

// awaitPush blocks until the device's push reached the link, so a hook's
// push is seen by the executor's next Pending.
func (l *link) awaitPush(t testing.TB) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case r := <-l.tr.Reports():
			if p, ok := r.Packet(); ok {
				l.dispatch(p)
				if p.Cmd() == wire.CmdStatusChanged {
					return
				}
			}
		case <-deadline:
			t.Fatal("no push reached the link")
			return
		}
	}
}

func (l *link) abort() { l.signal(safety.Signal{Kind: safety.SignalAbort}) }

func (l *link) lastReply(addr uint16) (wire.Packet, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.replies[addr]
	return p, ok
}

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
