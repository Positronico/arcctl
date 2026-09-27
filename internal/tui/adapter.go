package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// snapshotEvery folds snapshots that arrive faster than this into the
// latest; the loads publish one per transaction.
const snapshotEvery = 33 * time.Millisecond

// watcher turns the session's coalescing Changed signal into snapshots.
// One command waits at a time and the shell arms the next after taking a
// snapshot, so Update never blocks on the session.
type watcher struct {
	ctx   context.Context
	api   session.API
	every time.Duration
}

func (w watcher) next() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-w.api.Changed():
		case <-w.ctx.Done():
			return nil
		}
		if w.every > 0 {
			t := time.NewTimer(w.every)
			defer t.Stop()
			select {
			case <-t.C:
			case <-w.ctx.Done():
				return nil
			}
		}
		return watchMsg{w.api.Snapshot()}
	}
}

// watchMsg is a snapshot the watcher took; the shell arms the next wait.
type watchMsg struct{ sn *session.Snapshot }

// actionMsg ends a session call the shell made for a key.
type actionMsg struct {
	done string // the notice on success
	err  error
}

func (a *App) call(done string, f func(ctx context.Context) error) tea.Cmd {
	ctx := a.ctx
	return func() tea.Msg { return actionMsg{done: done, err: f(ctx)} }
}

// hostMsg carries what the OS said for the Locked and NeedsPermission
// banners.
type hostMsg struct {
	state      session.State
	permission string
	console    *safety.Console
	err        error
}

func (a *App) askHost(st session.State) tea.Cmd {
	h := a.opt.Host
	switch {
	case st == session.NeedsPermission && h.Permission != nil:
		return func() tea.Msg { return hostMsg{state: st, permission: h.Permission()} }
	case st == session.Locked && h.Console != nil:
		return func() tea.Msg {
			c, err := h.Console()
			return hostMsg{state: st, console: &c, err: err}
		}
	}
	return nil
}
