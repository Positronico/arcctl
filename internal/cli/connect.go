package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/session"
)

// stage is how far a command needs the session to get before it runs.
type stage uint8

const (
	probed stage = iota // an interface answered (or it is clear none will)
	loaded              // the configuration is loaded, or the device is one arcctl cannot load
)

// conn is a running session.
type conn struct {
	w    *world
	s    *session.Session
	stop func()
}

// connect starts a session and waits until it reaches st. On failure it
// returns the last snapshot with the error, and the session is stopped.
func (r *runner) connect(st stage) (*conn, *session.Snapshot, error) {
	w, err := r.world()
	if err != nil {
		return nil, nil, err
	}
	c, sn, err := r.attach(w, st)
	if c != nil {
		c.stop = chain(c.stop, w.close)
	} else {
		w.close()
	}
	return c, sn, err
}

func chain(fs ...func()) func() {
	return func() {
		for _, f := range fs {
			f()
		}
	}
}

// attach runs a session on w's devices; stopping it leaves w open.
func (r *runner) attach(w *world, st stage) (*conn, *session.Snapshot, error) {
	var cleanup []func()
	stop := func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
		cleanup = nil
	}
	if w.locks {
		release, err := r.lock()
		if err != nil {
			stop()
			return nil, nil, err
		}
		cleanup = append(cleanup, release)
	}
	opt := r.options(w)
	logClose, err := r.logger(&opt)
	if err != nil {
		stop()
		return nil, nil, err
	}
	cleanup = append(cleanup, logClose)
	recClose, err := r.recorder(&opt)
	if err != nil {
		stop()
		return nil, nil, err
	}
	cleanup = append(cleanup, recClose)

	s := session.New(opt)
	first := s.Snapshot().Seq
	// The session outlives an interrupt until the command has taken the
	// outcome of a write, which then stops after its current record.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()
	cleanup = append(cleanup, func() {
		cancel()
		<-done
	})
	c := &conn{w: w, s: s, stop: stop}
	sn, err := r.await(c, first, st)
	if err != nil {
		stop()
		return nil, sn, err
	}
	return c, sn, nil
}

func (r *runner) lock() (func(), error) {
	paths, err := r.env.Paths()
	if err != nil {
		return nil, err
	}
	l, err := platform.AcquireLock(paths.Lock)
	var le *platform.LockedError
	switch {
	case errors.As(err, &le):
		who := "another arcctl"
		if le.PID != 0 {
			who += fmt.Sprintf(" (pid %d)", le.PID)
		}
		return nil, fail(ExitBlocked, who+" is using the receiver",
			"Close the other arcctl (the TUI or a running command), then retry.")
	case err != nil:
		return nil, err
	}
	return func() { _ = l.Release() }, nil
}

func (r *runner) logger(opt *session.Options) (func(), error) {
	if r.g.log == "" {
		return func() {}, nil
	}
	f, err := os.OpenFile(r.g.log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	opt.Log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})).With("cmd", r.cmd)
	return func() { f.Close() }, nil
}

// recorder opens the --record transcript. A bare file name goes into the logs
// folder, which is where unredacted transcripts belong (docs: files and
// paths); a path, even ./name, is taken as given.
func (r *runner) recorder(opt *session.Options) (func(), error) {
	if r.g.record == "" {
		return func() {}, nil
	}
	path := r.g.record
	if filepath.Base(path) == path {
		paths, err := r.env.Paths()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(paths.Logs, 0o700); err != nil {
			return nil, err
		}
		path = filepath.Join(paths.Logs, path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(r.errw, "arcctl: recording the HID traffic to %s; share it only through 'arcctl redact'\n", path)
	rec, err := hidio.NewRecorder(f, hidio.Header{Source: "arcctl " + r.env.Version + " " + r.cmd, Started: r.env.Now()})
	if err != nil {
		f.Close()
		return nil, err
	}
	opt.Recorder = rec
	return func() {
		if err := rec.Err(); err != nil {
			fmt.Fprintf(r.errw, "arcctl: the transcript is incomplete: %v\n", err)
		}
		f.Close()
	}, nil
}

// await follows the snapshots until the session reaches st or a state the
// command cannot get past.
func (r *runner) await(c *conn, first uint64, st stage) (*session.Snapshot, error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var asleep, suspect time.Time
	warned := false
	for {
		sn := c.s.Snapshot()
		if c.w.replay != nil {
			if err := c.w.replay.diverged(); err != nil {
				return sn, fail(ExitFailure, err.Error(), "")
			}
		}
		now := time.Now()
		switch sn.State {
		case session.NoReceiver:
			if sn.Seq > first {
				return sn, r.noReceiver(c.w, sn)
			}
		case session.NeedsPermission, session.Locked, session.Seized, session.Stalled:
			return sn, r.blocked(c.w, sn)
		case session.Choosing:
			return sn, r.choosing(sn)
		case session.Conflict:
			return sn, r.conflict(sn)
		case session.SuspectedConflict:
			if suspect.IsZero() {
				suspect = now
			}
			if (sn.Link == session.Ready && sn.Progress.Job == "") || now.Sub(suspect) > r.g.wait {
				return sn, r.suspected(sn)
			}
		case session.Offline:
			if st == probed && !sn.Online {
				return sn, nil
			}
			if sn.Online {
				asleep = time.Time{}
				break
			}
			if asleep.IsZero() {
				asleep = now
			}
			if !warned {
				fmt.Fprintf(r.errw, "arcctl: the mouse does not answer; waiting %s for it to wake (move it)\n", r.g.wait)
				warned = true
			}
			if now.Sub(asleep) > r.g.wait {
				return sn, offline(sn)
			}
		case session.Loading:
			if st == probed {
				return sn, nil
			}
		case session.Ready, session.Recovering:
			if st == probed || sn.Progress.Job == "" {
				return sn, nil
			}
		case session.Unknown:
			return sn, nil
		}
		if sn.State != session.SuspectedConflict {
			suspect = time.Time{}
		}
		select {
		case <-c.s.Changed():
		case <-tick.C:
		case <-r.ctx.Done():
			return sn, r.ctx.Err()
		}
	}
}

func (r *runner) noReceiver(w *world, sn *session.Snapshot) error {
	if errors.Is(sn.Err, session.ErrWrites) {
		return refusedWrites(w, sn)
	}
	if errors.Is(sn.Err, session.ErrNoAnswer) {
		msg := "found the receiver, but none of its interfaces answered"
		if why := writeTrouble(w, sn.Err); why != "" {
			msg += ": " + why
		}
		return fail(ExitNoReceiver, msg,
			"Unplug the receiver and plug it back in. If the ProtoArc web app is open in Chrome, close that tab.")
	}
	return fail(ExitNoReceiver, "no ProtoArc receiver or mouse found",
		"Plug in the receiver, or the mouse with its USB cable. 'arcctl doctor' checks the setup.")
}

// refusedWrites explains a device the session dropped because it kept
// refusing writes, without a sign that it went away.
func refusedWrites(w *world, sn *session.Snapshot) error {
	msg := "the receiver keeps refusing reports"
	if why := writeTrouble(w, sn.Err); why != "" {
		msg += ": " + why
	}
	return fail(ExitNoReceiver, msg, "Unplug the receiver, plug it back in and retry.")
}

// writeTrouble describes a write error that is neither a lock nor a missing
// permission, or returns "" for any other error.
func writeTrouble(w *world, err error) string {
	switch hidio.Classify(err) {
	case hidio.ClassTimeout, hidio.ClassRetry, hidio.ClassOther:
	default:
		return ""
	}
	if _, ok := hidio.IOReturn(err); !ok {
		return ""
	}
	if d := w.host.Diagnose(err); d.Summary != "" {
		return d.Summary
	}
	return plainDiagnosis(err).Summary
}

func (r *runner) blocked(w *world, sn *session.Snapshot) error {
	d := w.host.Diagnose(sn.Err)
	code := ExitBlocked
	if sn.State == session.NeedsPermission {
		code = ExitPermission
	}
	msg := d.Summary
	if msg == "" {
		msg = sn.State.String()
	}
	if sn.State == session.Stalled {
		msg, d.Hint = "a write to the receiver never completed", "Unplug the receiver, plug it back in and retry."
	}
	return fail(code, msg, d.Hint)
}

func (r *runner) choosing(sn *session.Snapshot) error {
	var b strings.Builder
	b.WriteString("Pass --device with one of:\n")
	for _, a := range sn.Answers {
		what := "mouse offline"
		switch {
		case a.Model != nil:
			what = a.Model.Name
		case a.Handshake != nil:
			what = fmt.Sprintf("unknown device cid 0x%02x mid %d", a.Handshake.CID, a.Handshake.MID)
		}
		fmt.Fprintf(&b, "  %s  (%04x:%04x, %s)\n", a.Candidate.Path, a.Candidate.VID, a.Candidate.PID, what)
	}
	return fail(ExitChoose, fmt.Sprintf("%d devices answer", len(sn.Answers)), b.String())
}

const closeOthers = "Close the ProtoArc web app (its tab in Chrome) and any other tool that talks to the mouse, wait a few seconds, then retry."

func (r *runner) conflict(sn *session.Snapshot) error {
	return fail(ExitBlocked, "another program is talking to the receiver"+clientList(sn.Clients), closeOthers)
}

func (r *runner) suspected(sn *session.Snapshot) error {
	return fail(ExitBlocked, "replies are going missing; another program may be reading them"+clientList(sn.Clients), closeOthers)
}

func clientList(cs []session.Client) string {
	if len(cs) == 0 {
		return ""
	}
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = fmt.Sprintf("%s (pid %d)", c.Name, c.PID)
	}
	return ": " + strings.Join(names, ", ")
}

func offline(sn *session.Snapshot) error {
	return fail(ExitOffline, "the receiver answers, but the mouse does not",
		"The mouse is asleep, out of range, on its Bluetooth channel or not paired. Move it or click a button, then retry.")
}

// do runs call while watching the session: a mouse that stays asleep longer
// than --wait, or another client, ends it.
func (r *runner) do(c *conn, call func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- call(ctx) }()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var paused time.Time
	shown := -1
	for {
		select {
		case err := <-res:
			r.progressDone(shown)
			return err
		case <-c.s.Changed():
		case <-tick.C:
		}
		sn := c.s.Snapshot()
		var stop error
		switch {
		case c.w.replay != nil && c.w.replay.diverged() != nil:
			stop = fail(ExitFailure, c.w.replay.diverged().Error(), "")
		case sn.State == session.Conflict:
			stop = r.conflict(sn)
		case sn.State == session.NoReceiver && errors.Is(sn.Err, session.ErrWrites):
			stop = refusedWrites(c.w, sn)
		case sn.State == session.NoReceiver:
			stop = fail(ExitNoReceiver, "the receiver went away", "Plug it back in and retry.")
		case sn.State == session.NeedsPermission, sn.State == session.Locked, sn.State == session.Seized, sn.State == session.Stalled:
			stop = r.blocked(c.w, sn)
		case sn.Progress.Paused:
			if paused.IsZero() {
				paused = time.Now()
				fmt.Fprintf(r.errw, "\narcctl: the mouse went to sleep; waiting %s for it to wake (move it)\n", r.g.wait)
			}
			if time.Since(paused) > r.g.wait {
				stop = offline(sn)
			}
		default:
			paused = time.Time{}
		}
		if stop != nil {
			cancel()
			<-res
			r.progressDone(shown)
			return stop
		}
		shown = r.progress(sn.Progress, shown)
	}
}

func (r *runner) progress(p session.Progress, shown int) int {
	if !r.env.Interactive || p.Total == 0 || p.Done == shown {
		return shown
	}
	fmt.Fprintf(r.errw, "\r%s: %d/%d", p.Job, p.Done, p.Total)
	return p.Done
}

func (r *runner) progressDone(shown int) {
	if shown >= 0 {
		fmt.Fprintln(r.errw)
	}
}

// writeOut writes to -o's file, replacing it atomically, or to stdout for ""
// and "-".
func (r *runner) writeOut(path string, write func(io.Writer) error) error {
	if path == "" || path == "-" {
		return write(r.out)
	}
	return writeFile(path, write)
}

// writeRaw is writeOut for bytes that must reach standard output unchanged.
func (r *runner) writeRaw(path string, b []byte) error {
	if path == "" || path == "-" {
		_, err := r.env.Stdout.Write(b)
		return err
	}
	return writeFile(path, func(w io.Writer) error { _, err := w.Write(b); return err })
}

func writeFile(path string, write func(io.Writer) error) error {
	var b bytes.Buffer
	if err := write(&b); err != nil {
		return err
	}
	return backup.WriteFile(path, b.Bytes())
}
