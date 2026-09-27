package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

type backupMsg struct {
	path    string
	missing int
	err     error
}

func (m backupMsg) notice() noticeMsg {
	switch {
	case m.err != nil:
		return noticeMsg{text: "Backup failed: " + plain(m.err), bad: true}
	case m.missing > 0:
		return noticeMsg{text: fmt.Sprintf("Backup saved without %s that could not be read:", plural(m.missing, "range", "ranges")), path: m.path, bad: true}
	}
	return noticeMsg{text: "Backup saved:", path: m.path}
}

func (a *App) backup(c *Context) tea.Cmd {
	store := a.opt.Backups
	switch {
	case store == nil:
		return Notice("Backups are not available in this session.")
	case c.Writing():
		return Problem(ErrWriting)
	case a.sn.Image == nil:
		return Notice("Nothing is loaded to back up yet.")
	}
	api, ctx := a.api, a.ctx
	return tea.Batch(Notice("Backing up the loaded configuration."), func() tea.Msg {
		cp, err := api.Backup(ctx, false)
		if err != nil {
			return backupMsg{err: err}
		}
		path, err := store.Save(cp, "")
		return backupMsg{path: path, missing: len(cp.Missing), err: err}
	})
}
