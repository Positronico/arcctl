package emu

import (
	"sync"
	"time"
)

// Clock schedules the emulator's delayed events: reply latency, late replies
// and competitor polls.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a scheduled call; *time.Timer is one.
type Timer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// ManualClock is a Clock that moves only when Advance is called, so a test
// decides exactly when delayed replies and polls happen.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    uint64
	timers []*manualTimer
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func NewManualClock() *ManualClock { return &ManualClock{now: epoch} }

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &manualTimer{c: c, when: c.now.Add(max(d, 0)), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d. Timers that fall due run in order of
// due time, then of creation, on the calling goroutine, with the clock set to
// their due time; timers they schedule within the window run too.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		t := c.next(end)
		if t == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		if t.when.After(c.now) {
			c.now = t.when
		}
		c.mu.Unlock()
		t.f()
	}
}

// Pending counts the timers that have not run or been stopped.
func (c *ManualClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

func (c *ManualClock) next(end time.Time) *manualTimer {
	best := -1
	for i, t := range c.timers {
		if t.when.After(end) {
			continue
		}
		if best < 0 || t.when.Before(c.timers[best].when) || t.when.Equal(c.timers[best].when) && t.seq < c.timers[best].seq {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	t := c.timers[best]
	c.timers = append(c.timers[:best], c.timers[best+1:]...)
	return t
}

type manualTimer struct {
	c    *ManualClock
	when time.Time
	seq  uint64
	f    func()
}

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	for i, x := range t.c.timers {
		if x == t {
			t.c.timers = append(t.c.timers[:i], t.c.timers[i+1:]...)
			return true
		}
	}
	return false
}
