package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// BackupLister lists the backups of one device, oldest first; backup.Store
// is one.
type BackupLister interface {
	List(id plan.Identity) ([]backup.Listing, error)
}

// backupRelist is how old the list may get before a snapshot or a key reads
// the folder again, so a backup saved elsewhere shows up.
const backupRelist = 2 * time.Second

// backupWait bounds a full backup, and the reads a restore makes first.
const backupWait = 5 * time.Minute

// BackupTab lists the backups of the mouse (§7.8), newest first, with what
// each holds; takes a full backup; shows a backup decoded, and its diff
// against the mouse; and restores one through the review, so the write
// goes through the preflight, the journal and the read-back like any other.
type BackupTab struct {
	api    session.API
	store  session.Backups
	lister BackupLister
	source string

	id      string // the identity key the list is for
	list    []backup.Listing
	listErr error
	listed  time.Time
	listing bool
	stale   bool // the folder changed while listing read it
	cursor  int
	offset  int
	busy    string
	gen     int
}

// NewBackupTab builds the tab over api. store saves the full backups and,
// when it is a BackupLister, lists them; source is where the device comes
// from, as Options.Source says.
func NewBackupTab(api session.API, store session.Backups, source string) *BackupTab {
	t := &BackupTab{api: api, store: store, source: source}
	if l, ok := store.(BackupLister); ok {
		t.lister = l
	}
	return t
}

func (t *BackupTab) Title() string          { return "Backup" }
func (t *BackupTab) Needs() []mouse.Feature { return nil }
func (t *BackupTab) Capturing() bool        { return false }

type (
	backupListMsg struct {
		tab  *BackupTab
		key  string
		list []backup.Listing
		err  error
	}
	backupFullMsg struct {
		tab     *BackupTab
		path    string
		missing int
		err     error
	}
	// restorePrepMsg carries a restore laid out against the mouse, after
	// the reads it needed: for the diff, or for the review.
	restorePrepMsg struct {
		tab    *BackupTab
		gen    int
		review bool
		src    *backup.Source
		read   *flash.Image
		rs     *backup.Restore
		err    error
	}
)

func (t *BackupTab) Update(c *Context, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case SnapshotMsg:
		return t.relist(c, false)
	case WriteDoneMsg, backupMsg, resetDoneMsg:
		return t.relist(c, true)
	case backupListMsg:
		if msg.tab == t {
			t.took(msg)
			if t.stale {
				t.stale = false
				return t.relist(c, true)
			}
		}
	case backupFullMsg:
		if msg.tab != t {
			return nil
		}
		t.busy = ""
		n := backupMsg{path: msg.path, missing: msg.missing, err: msg.err}.notice()
		return tea.Batch(func() tea.Msg { return n }, t.relist(c, true))
	case restorePrepMsg:
		if msg.tab != t || msg.gen != t.gen {
			return nil
		}
		t.busy = ""
		return t.prepared(c, msg)
	case tea.KeyPressMsg:
		return t.key(c, msg)
	}
	return nil
}

// relist reads the backup folder again when the mouse changed, when now
// says so, or when the list is older than backupRelist. With now, while a
// read is under way, the folder is read again once that read is done: it may
// have missed the change.
func (t *BackupTab) relist(c *Context, now bool) tea.Cmd {
	key := backupKey(c)
	switch {
	case t.lister == nil:
		return nil
	case t.listing:
		t.stale = t.stale || now
		return nil
	case key != t.id:
		t.list, t.listErr, t.cursor, t.offset, t.id = nil, nil, 0, 0, key
	case !now && c.Now.Sub(t.listed) < backupRelist:
		return nil
	}
	if key == "" {
		return nil
	}
	t.listing, t.listed = true, c.Now
	lister, id := t.lister, c.Snapshot.Identity
	return func() tea.Msg {
		list, err := lister.List(id)
		return backupListMsg{tab: t, key: key, list: list, err: err}
	}
}

func backupKey(c *Context) string {
	if c.Snapshot.Handshake == nil {
		return ""
	}
	return c.Snapshot.Identity.Key()
}

func (t *BackupTab) took(m backupListMsg) {
	t.listing = false
	if m.key != t.id {
		return
	}
	var sel string
	if l, ok := t.selected(); ok {
		sel = l.Path
	}
	t.list, t.listErr = slices.Clone(m.list), m.err
	slices.Reverse(t.list)
	t.cursor = max(0, min(t.cursor, len(t.list)-1))
	if i := slices.IndexFunc(t.list, func(l backup.Listing) bool { return l.Path == sel }); i >= 0 {
		t.cursor = i
	}
}

func (t *BackupTab) selected() (backup.Listing, bool) {
	if t.cursor < len(t.list) {
		return t.list[t.cursor], true
	}
	return backup.Listing{}, false
}

func (t *BackupTab) key(c *Context, k tea.KeyPressMsg) tea.Cmd {
	relist := t.relist(c, false)
	switch k.String() {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = max(0, min(len(t.list)-1, t.cursor+1))
	case "home", "g":
		t.cursor = 0
	case "end", "G":
		t.cursor = max(0, len(t.list)-1)
	case "enter":
		if l, ok := t.readable(); ok {
			return tea.Batch(relist, OpenDialog(newBackupShow(l)))
		}
	case "f":
		return tea.Batch(relist, t.full(c))
	case "d":
		return tea.Batch(relist, t.prepare(c, false))
	case "w":
		return tea.Batch(relist, t.prepare(c, true))
	case "X":
		return tea.Batch(relist, t.reset(c))
	}
	return relist
}

// readable is the selected backup when it could be read.
func (t *BackupTab) readable() (backup.Listing, bool) {
	l, ok := t.selected()
	return l, ok && l.Err == nil && l.File != nil
}

var (
	errNoStore   = errors.New("backups are not available in this session")
	errBusy      = errors.New("the Backup tab is still busy with the last request")
	errNoBackup  = errors.New("no readable backup is selected")
	errNotLoaded = errors.New("the mouse is not loaded yet")
)

// full takes a full backup of the mouse and saves it with the others.
func (t *BackupTab) full(c *Context) tea.Cmd {
	switch {
	case t.store == nil:
		return Problem(errNoStore)
	case t.busy != "":
		return Problem(errBusy)
	case c.Writing():
		return Problem(ErrWriting)
	case c.Snapshot.Image == nil:
		return Problem(errNotLoaded)
	}
	t.busy = "Backing up the whole configuration" + c.Glyphs.Ellipsis
	api, store := t.api, t.store
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), backupWait)
		defer cancel()
		cp, err := api.Backup(ctx, true)
		if err != nil {
			return backupFullMsg{tab: t, err: err}
		}
		path, err := store.Save(cp, "")
		return backupFullMsg{tab: t, path: path, missing: len(cp.Missing), err: err}
	}
}

// prepare lays out a restore of the selected backup against the mouse: it
// checks the device and the profile first, then reads what the comparison
// needs, then plans. review opens the review; otherwise the diff shows.
func (t *BackupTab) prepare(c *Context, review bool) tea.Cmd {
	l, ok := t.readable()
	m := c.Model()
	switch {
	case !ok:
		return Problem(errNoBackup)
	case t.busy != "":
		return Problem(errBusy)
	case m == nil || m.Family != catalog.FamilyMouse || c.Image() == nil:
		return Problem(errNotLoaded)
	}
	if review {
		if err := c.CanWrite(); err != nil {
			return Problem(err)
		}
	}
	src := &backup.Source{Kind: backup.KindBackup, Path: l.Path, File: l.File, Image: l.File.Image()}
	tg := restoreTarget(c, t.source)
	if _, err := backup.CheckSource(src, tg, backup.RestoreOptions{}); err != nil {
		return Problem(err)
	}
	t.gen++
	t.busy = "Reading what the restore compares" + c.Glyphs.Ellipsis
	api, gen := t.api, t.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), backupWait)
		defer cancel()
		dev := tg.Image
		for range 3 {
			need := backup.RestoreReads(src.Image, dev)
			if len(need) == 0 {
				break
			}
			cp, err := api.Read(ctx, need...)
			if err != nil {
				return restorePrepMsg{tab: t, gen: gen, err: err}
			}
			if cp.Image == nil {
				break
			}
			dev = cp.Image
			if len(cp.Missing) > 0 {
				break
			}
		}
		tg.Image = dev
		rs, err := backup.PlanRestore(src, tg, backup.RestoreOptions{})
		return restorePrepMsg{tab: t, gen: gen, review: review, src: src, read: dev, rs: rs, err: err}
	}
}

func (t *BackupTab) prepared(c *Context, m restorePrepMsg) tea.Cmd {
	if m.err != nil {
		return Problem(fmt.Errorf("the restore cannot be laid out: %w", m.err))
	}
	if !m.review {
		return OpenDialog(newRestoreDiff(t, m.src, m.read, m.rs))
	}
	return t.openReview(c, m.src, m.read, false)
}

// openReview opens the review of a restore of src; it plans again on what
// the mouse holds whenever that changes. unknown also writes back the
// records arcctl knows no valid value for, as the backup captured them.
func (t *BackupTab) openReview(c *Context, src *backup.Source, read *flash.Image, unknown bool) tea.Cmd {
	if err := c.CanWrite(); err != nil {
		return Problem(err)
	}
	d := &reviewDialog{api: t.api, kind: safety.KindApply, restore: &restoreReview{src: src, source: t.source, read: read, unknown: unknown}}
	d.replan(c)
	return OpenDialog(d)
}

// restoreTarget is the mouse a restore writes to, as c shows it.
func restoreTarget(c *Context, source string) backup.Target {
	tg := backup.Target{Model: c.Model(), Image: c.Image(), Options: c.MouseOptions(), Source: source}
	if source == "" {
		tg.Source = backup.SourceDevice
	}
	if h := c.Snapshot.Handshake; h != nil {
		conn := h.Conn
		tg.Conn = &conn
	}
	return tg
}

func (t *BackupTab) Hints(c *Context) []key.Binding {
	out := []key.Binding{hint(dilArrows(c, "↑/↓", "up/down"), "select")}
	if _, ok := t.readable(); ok {
		out = append(out, hint("enter", "show"), hint("d", "diff"), hint("w", "restore"))
	}
	if t.store != nil {
		out = append(out, hint("f", "full backup"))
	}
	if m := c.Model(); m != nil && m.Family == catalog.FamilyMouse {
		out = append(out, hint("X", "factory reset"))
	}
	return out
}
