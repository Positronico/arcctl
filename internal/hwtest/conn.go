//go:build hwtest

package hwtest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/session"
)

var (
	ErrNotReady  = errors.New("hwtest: the device is not ready")
	ErrUnhealthy = errors.New("hwtest: the session left Ready")
)

// conn is one session on the tapped devices.
type conn struct {
	s      *session.Session
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *conn) close() {
	c.cancel()
	<-c.done
}

type connectOpts struct {
	rec    *hidio.Recorder
	device string
	writes bool
}

// connect starts a session and waits until its load and journal check are
// done.
func (r *runner) connect(ctx context.Context, o connectOpts) (*conn, *session.Snapshot, error) {
	opt := r.cfg.Session
	opt.Devices = r.devs
	opt.Recorder = o.rec
	if o.device != "" {
		opt.Device = o.device
	}
	if !o.writes {
		opt.Writes = nil
	} else if opt.Writes == nil {
		return nil, nil, fmt.Errorf("%w: writes are not configured", ErrNotReady)
	}
	s := session.New(opt)
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &conn{s: s, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		_ = s.Run(sctx)
	}()
	sn, err := r.ready(ctx, c)
	if err != nil {
		c.close()
		return nil, sn, err
	}
	return c, sn, nil
}

// ready waits until the session is Ready with nothing left to do. A sleeping
// mouse gets one reminder; anything that blocks the session ends the wait.
func (r *runner) ready(ctx context.Context, c *conn) (*session.Snapshot, error) {
	return r.readyAfter(ctx, c, 0)
}

// readyAfter is ready for a snapshot newer than seq, so that one published
// before a call that just returned does not count.
func (r *runner) readyAfter(ctx context.Context, c *conn, seq uint64) (*session.Snapshot, error) {
	first := c.s.Snapshot().Seq
	deadline := time.Now().Add(r.cfg.Wait)
	told := false
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		sn := c.s.Snapshot()
		switch sn.State {
		case session.Ready, session.Recovering:
			if sn.Progress.Job == "" && sn.Seq > seq {
				return sn, nil
			}
		case session.NoReceiver:
			if sn.Seq > first {
				return sn, fmt.Errorf("%w: no receiver found (%v)", ErrNotReady, sn.Err)
			}
		case session.NeedsPermission, session.Locked, session.Seized, session.Stalled, session.Unknown, session.Conflict:
			return sn, fmt.Errorf("%w: the session is %s: %v", ErrNotReady, sn.State, sn.Err)
		case session.Choosing:
			return sn, fmt.Errorf("%w: %d devices answer; pass --device", ErrNotReady, len(sn.Answers))
		case session.Offline:
			if !told && !sn.Online {
				r.say("The mouse does not answer. Move it or click a button to wake it.")
				told = true
			}
		}
		if time.Now().After(deadline) {
			return sn, fmt.Errorf("%w: still %s (job %q) after %s", ErrNotReady, sn.State, sn.Progress.Job, r.cfg.Wait)
		}
		select {
		case <-ctx.Done():
			return sn, ctx.Err()
		case <-c.s.Changed():
		case <-tick.C:
		}
	}
}

// healthy fails when the session left Ready while the stage ran.
func healthy(sn *session.Snapshot) error {
	switch sn.State {
	case session.Ready:
		return nil
	case session.Recovering:
		return fmt.Errorf("%w: the journal holds an unfinished run", ErrUnhealthy)
	}
	return fmt.Errorf("%w: it is %s: %v", ErrUnhealthy, sn.State, sn.Err)
}

// rawPath is the raw path of the interface the session attached.
func (r *runner) rawPath(c *conn) (*rawPath, error) {
	sn := c.s.Snapshot()
	if sn.Device == nil {
		return nil, errNoTap
	}
	t, err := r.devs.tap(sn.Device.Path)
	if err != nil {
		return nil, err
	}
	return &rawPath{t: t, note: r.note, listen: r.cfg.Listen}, nil
}
