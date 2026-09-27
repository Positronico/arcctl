package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/library"
)

type (
	macroLibMsg struct {
		entries []library.Entry
		err     error
	}
	macroSavedMsg struct{ err error }
	// macroFileMsg ends an import (entries set) or an export.
	macroFileMsg struct {
		export  bool
		path    string
		entries []library.Entry
		n       int
		err     error
	}
)

// libSaves writes the library file in the background, one write at a time
// and always the newest library the tab holds: a save that finds the file
// already holding its library or a newer one does nothing. flush writes what
// is left when arcctl quits.
type libSaves struct {
	store library.Store
	write sync.Mutex // held while the file is written
	mu    sync.Mutex
	// next numbers the newest library, latest; written is the number of the
	// one the file holds.
	next    int
	latest  []library.Entry
	written int
}

// queue takes entries as the newest library and returns the save that
// writes it.
func (s *libSaves) queue(entries []library.Entry) tea.Cmd {
	s.mu.Lock()
	s.next++
	seq := s.next
	s.latest = entries
	s.mu.Unlock()
	return func() tea.Msg { return macroSavedMsg{s.save(seq)} }
}

// save writes the newest library unless the file holds library seq or a
// newer one. After a failure the next save tries again.
func (s *libSaves) save(seq int) error {
	s.write.Lock()
	defer s.write.Unlock()
	s.mu.Lock()
	next, entries, done := s.next, s.latest, s.written >= seq
	s.mu.Unlock()
	if done {
		return nil
	}
	if err := s.store.Save(entries); err != nil {
		return err
	}
	s.mu.Lock()
	s.written = next
	s.mu.Unlock()
	return nil
}

// flush writes the newest library unless the file holds it, and waits for
// that until ctx ends.
func (s *libSaves) flush(ctx context.Context) error {
	s.mu.Lock()
	seq := s.next
	s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.save(seq) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flush writes the library changes that no background save has written
// yet; the shell calls it once the TUI has ended.
func (t *Macros) flush(ctx context.Context) error {
	switch err := t.saves.flush(ctx); {
	case err == nil:
		return nil
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		return fmt.Errorf("the last changes to the macro library may be lost: saving %s did not finish in time", t.saves.store.Path)
	default:
		return fmt.Errorf("the last changes to the macro library were not saved to %s: %w", t.saves.store.Path, err)
	}
}

var errNoLibrary = errors.New("there is no macro library in this session")

func (t *Macros) loadLibrary() tea.Cmd {
	if t.lib == nil {
		return nil
	}
	store := *t.lib
	return func() tea.Msg {
		entries, err := store.Load()
		return macroLibMsg{entries, err}
	}
}

func (t *Macros) libraryLoaded(msg macroLibMsg) tea.Cmd {
	t.loaded, t.libErr = true, msg.err
	if msg.err != nil {
		return Problem(fmt.Errorf("the macro library could not be read: %w", msg.err))
	}
	t.entries = msg.entries
	return nil
}

// writable says why the library cannot change now.
func (t *Macros) writable() error {
	switch {
	case t.lib == nil:
		return errNoLibrary
	case t.libErr != nil:
		return fmt.Errorf("the macro library could not be read, so it is not changed: %w", t.libErr)
	case !t.loaded:
		return errors.New("the macro library is still loading")
	}
	return nil
}

// setLibrary takes entries as the library and saves them in the background.
// A library the file could not hold, or that would not load again, is
// refused and the tab keeps the one it has.
func (t *Macros) setLibrary(entries []library.Entry) (tea.Cmd, error) {
	if _, err := library.Encode(entries); err != nil {
		return nil, err
	}
	t.entries = entries
	return t.saves.queue(slices.Clone(entries)), nil
}

// keep puts e in the library and reports whether the library holds it
// now. replace names the entry e comes from, which it replaces even when it
// was renamed; another entry of e's name that holds a different macro is
// never replaced.
func (t *Macros) keep(e library.Entry, replace string) (tea.Cmd, bool) {
	if err := t.writable(); err != nil {
		return Problem(err), false
	}
	if sanitizeName(e.Macro.Name) != e.Macro.Name {
		return Problem(errOddName(e.Macro.Name)), false
	}
	if err := e.Check(); err != nil {
		return Problem(fmt.Errorf("the macro cannot be saved: %s", macroRefusal(err))), false
	}
	name := strconv.Quote(shownName(e.Macro.Name))
	entries := slices.Clone(t.entries)
	same, from := library.Index(entries, e.Macro.Name), -1
	if replace != "" {
		from = library.Index(entries, replace)
	}
	switch {
	case same >= 0 && entries[same].Equal(e):
		return Notice("The library already holds " + name + "."), true
	case same >= 0 && same != from:
		return Problem(fmt.Errorf("the library already holds another macro named %s; rename this one first", name)), false
	case from >= 0:
		entries[from] = e
	default:
		entries = append(entries, e)
	}
	save, err := t.setLibrary(entries)
	if err != nil {
		return Problem(fmt.Errorf("the macro is not saved: %w", err)), false
	}
	return tea.Batch(save, Notice("Saved "+name+" in the library.")), true
}

// remove deletes library entry i after asking.
func (t *Macros) remove(i int) tea.Cmd {
	if err := t.writable(); err != nil {
		return Problem(err)
	}
	if i >= len(t.entries) {
		return nil
	}
	e := t.entries[i]
	name := strconv.Quote(shownName(e.Macro.Name))
	return OpenDialog(&Confirm{
		Title: "Delete from the library",
		Body:  []string{"Delete " + name + " from the macro library? The mouse keeps whatever it holds."},
		Yes: func(string) tea.Cmd {
			j := slices.IndexFunc(t.entries, e.Equal)
			if j < 0 {
				return nil
			}
			save, err := t.setLibrary(slices.Delete(slices.Clone(t.entries), j, j+1))
			if err != nil {
				return Problem(err)
			}
			return tea.Batch(save, Notice("Deleted "+name+" from the library."))
		},
	})
}

func (t *Macros) importPrompt() tea.Cmd {
	if err := t.writable(); err != nil {
		return Problem(err)
	}
	return OpenDialog(&pathPrompt{
		title: "Import macros",
		body: "Adds the macros of an arcctl macro file to the library. A macro whose name the library already uses " +
			"for another macro is left out. Nothing is written to the mouse.",
		run: func(path string) tea.Cmd {
			return func() tea.Msg {
				p, err := library.UserPath(path)
				if err != nil {
					return macroFileMsg{path: path, err: err}
				}
				entries, err := library.ReadFile(p)
				return macroFileMsg{path: p, entries: entries, err: err}
			}
		},
	})
}

// exportName is a file name for an export in the home folder that no file
// holds yet: the date, and a number when that is taken.
func exportName(now time.Time) string {
	base := "~/arcctl-macros-" + now.Format("2006-01-02")
	name := base + ".json"
	for i := 2; i < 100; i++ {
		p, err := library.UserPath(name)
		if err != nil {
			break
		}
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d.json", base, i)
	}
	return name
}

func (t *Macros) exportPrompt(c *Context) tea.Cmd {
	if err := t.writable(); err != nil {
		return Problem(err)
	}
	if len(t.entries) == 0 {
		return Problem(errors.New("the library is empty; s saves a macro to it first"))
	}
	entries := slices.Clone(t.entries)
	return OpenDialog(&pathPrompt{
		title: "Export the library",
		body: fmt.Sprintf("Writes the %s of the library to a new file in arcctl's macro format. "+
			"An existing file is never replaced.", plural(len(entries), "macro", "macros")),
		typed: []rune(exportName(c.Now)),
		run: func(path string) tea.Cmd {
			return func() tea.Msg {
				p, err := library.UserPath(path)
				if err == nil {
					err = library.WriteNew(p, entries)
				}
				return macroFileMsg{export: true, path: p, n: len(entries), err: err}
			}
		},
	})
}

// fileProblem is a failed import or export, in words, with the path last
// so the notice cuts it in the middle and the cause shows.
func fileProblem(msg macroFileMsg) tea.Cmd {
	what := "Import failed: "
	if msg.export {
		what = "Export failed: "
	}
	var cause string
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case msg.path == "":
		return Problem(errors.New(what + plain(msg.err)))
	case msg.export && errors.Is(msg.err, fs.ErrExist):
		cause = "the file exists; exports never replace one. Type another name"
	case errors.Is(msg.err, fs.ErrNotExist):
		cause = "there is no such file"
	case errors.As(msg.err, &pe):
		cause = pe.Err.Error()
	case errors.As(msg.err, &le):
		cause = le.Err.Error()
	default:
		cause = strings.TrimPrefix(msg.err.Error(), msg.path+": ")
	}
	cause = strings.TrimPrefix(cause, "library: ")
	return func() tea.Msg { return noticeMsg{text: what + cause + ":", path: msg.path, bad: true} }
}

// renameImported applies the name sanitiser to imported names (§7.7): a
// name it changes takes the sanitised one, unless that is empty.
func renameImported(entries []library.Entry) (out []library.Entry, renamed, dropped []string) {
	for _, e := range entries {
		n := sanitizeName(e.Macro.Name)
		switch {
		case n == e.Macro.Name:
		case n == "":
			dropped = append(dropped, strconv.Quote(shownName(e.Macro.Name)))
			continue
		default:
			renamed = append(renamed, strconv.Quote(shownName(e.Macro.Name))+" as "+strconv.Quote(n))
			e.Macro.Name = n
		}
		out = append(out, e)
	}
	return out, renamed, dropped
}

func (t *Macros) fileDone(msg macroFileMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		return fileProblem(msg)
	case msg.export:
		return func() tea.Msg {
			return noticeMsg{text: "Exported " + plural(msg.n, "macro", "macros") + " to", path: msg.path}
		}
	}
	if err := t.writable(); err != nil {
		return Problem(err)
	}
	in, renamed, dropped := renameImported(msg.entries)
	merged, added, clashes := library.Merge(t.entries, in)
	text := fmt.Sprintf("Imported %s", plural(added, "macro", "macros"))
	if same := len(in) - added - len(clashes); same > 0 {
		text += fmt.Sprintf(", %d already in the library", same)
	}
	if len(renamed) > 0 {
		text += "; renamed, as the web app leaves their characters out of names: " + strings.Join(renamed, ", ")
	}
	if len(clashes) > 0 {
		names := make([]string, len(clashes))
		for i, n := range clashes {
			names[i] = strconv.Quote(shownName(n))
		}
		text += "; left out, as the library has other macros by these names: " + strings.Join(names, ", ")
	}
	if len(dropped) > 0 {
		text += "; left out, as nothing of their names is left for the web app: " + strings.Join(dropped, ", ")
	}
	if added == 0 {
		return Notice(text + ".")
	}
	save, err := t.setLibrary(merged)
	if err != nil {
		return Problem(fmt.Errorf("nothing imported: %w", err))
	}
	return tea.Batch(Notice(text+"."), save)
}

// pathPrompt asks for a file path; enter hands it to run and closes.
type pathPrompt struct {
	title string
	body  string
	typed []rune
	run   func(path string) tea.Cmd
}

func (d *pathPrompt) Update(c *Context, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		d.typed = append(d.typed, []rune(strings.TrimRight(msg.Content, "\r\n"))...)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return CloseDialog
		case "enter":
			if strings.TrimSpace(string(d.typed)) == "" {
				return nil
			}
			return tea.Sequence(closeDialog(d), d.run(string(d.typed)))
		case "backspace":
			if n := len(d.typed); n > 0 {
				d.typed = d.typed[:n-1]
			}
		case "ctrl+u":
			d.typed = nil
		default:
			if msg.Text != "" {
				d.typed = append(d.typed, []rune(msg.Text)...)
			}
		}
	}
	return nil
}

func (d *pathPrompt) View(c *Context, w, h int) string {
	lines := []string{c.Styles.Bold.Render(d.title), ""}
	lines = append(lines, wrap(d.body, w-2, "", "")...)
	lines = append(lines, "", "File (~ is your home folder):", "> "+string(d.typed)+"_")
	return indentLines(lines, " ")
}

func (d *pathPrompt) Hints(c *Context) []key.Binding {
	return []key.Binding{hint("ctrl+u", "clear"), hint("enter", "go on"), hint("esc", "cancel")}
}
