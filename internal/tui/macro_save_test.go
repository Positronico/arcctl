package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/library"
)

// A save writes the newest library the tab holds, and a save that finds it
// written does nothing; flush writes a library whose save never ran.
func TestLibrarySavesWriteTheNewest(t *testing.T) {
	lib := macLibrary(t)
	s := &libSaves{store: *lib}
	hello, greet := library.Entry{Macro: macHello(t), Cycle: 1}, macGreet(t)
	first := s.queue([]library.Entry{hello})
	second := s.queue([]library.Entry{hello, greet})
	if msg := second(); msg.(macroSavedMsg).err != nil {
		t.Fatal(msg)
	}
	if msg := first(); msg.(macroSavedMsg).err != nil {
		t.Fatal(msg)
	}
	if got, _ := lib.Load(); len(got) != 2 {
		t.Fatalf("an older save replaced the newer library: %d entries", len(got))
	}
	s.queue([]library.Entry{greet})
	if err := s.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := lib.Load(); len(got) != 1 || !got[0].Equal(greet) {
		t.Fatalf("flush left the library at %+v", got)
	}
}

// flush waits only until its context ends, and names the library in what
// it reports.
func TestLibraryFlushReports(t *testing.T) {
	m := NewMacros(nil, macLibrary(t))
	m.saves.queue([]library.Entry{macGreet(t)})
	m.saves.write.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := m.flush(ctx)
	m.saves.write.Unlock()
	if err == nil || !strings.Contains(err.Error(), "did not finish in time") || !strings.Contains(err.Error(), m.lib.Path) {
		t.Errorf("a save that holds the file: %v", err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := NewMacros(nil, &library.Store{Path: filepath.Join(file, "macros.json")})
	bad.saves.queue([]library.Entry{macGreet(t)})
	err = bad.flush(context.Background())
	if err == nil || !strings.Contains(err.Error(), "were not saved to "+bad.lib.Path) {
		t.Errorf("a save that fails: %v", err)
	}
	if err := NewMacros(nil, nil).flush(context.Background()); err != nil {
		t.Errorf("a tab without a library: %v", err)
	}
}

// saveStep types keys into the real program once a message that after
// accepts has arrived since the step before.
type saveStep struct {
	after func(tea.Msg) bool
	keys  string
}

func isMsg[T any](msg tea.Msg) bool { _, ok := msg.(T); return ok }

// runSaving runs the shell as Run does, over the Macros tab m of a mouse
// that holds macros, and types a new macro "abc" into the editor, saves it
// to the library and quits at once. quitting runs when the program takes
// the quit.
func runSaving(t *testing.T, m *Macros, wait time.Duration, quitting func()) error {
	t.Helper()
	msgs := make(chan tea.Msg, 256)
	filter := func(_ tea.Model, msg tea.Msg) tea.Msg {
		if isMsg[tea.QuitMsg](msg) && quitting != nil {
			quitting()
		}
		select {
		case msgs <- msg:
		default:
		}
		return msg
	}
	in, typing := io.Pipe()
	defer typing.Close()
	steps := []saveStep{
		{isMsg[macroLibMsg], "n"},
		{isMsg[openDialogMsg], "\rabc\ri"},
		{isMsg[openDialogMsg], "\r"},
		{isMsg[closeDialogMsg], "sq"},
		{isMsg[closeDialogMsg], "q"},
	}
	go func() {
		for _, s := range steps {
			for msg := range msgs {
				if s.after(msg) {
					break
				}
			}
			if _, err := io.WriteString(typing, s.keys); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := run(ctx, Options{Session: newFake(macSnapshot(t)), Mode: ModeEdit, Tabs: []Tab{m}, OS: keys.Mac,
		Verified: catalog.Verifications{}, Now: func() time.Time { return now }}, wait,
		tea.WithInput(in), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignals(),
		tea.WithWindowSize(80, 24), tea.WithFilter(filter))
	close(msgs)
	return err
}

func wantAbc(t *testing.T, lib *library.Store) {
	t.Helper()
	got, err := lib.Load()
	if err != nil || len(got) != 1 || got[0].Macro.Name != "abc" || len(got[0].Macro.Events) != 2 || got[0].Macro.Events[0].Stroke != keys.LCtrl.Stroke() {
		t.Fatalf("the library holds %+v, %v", got, err)
	}
}

// Quitting right after a save waits for it: the save cannot write until the
// program has quit, and Run returns only once the file holds the macro.
func TestRunWaitsForTheLibrary(t *testing.T) {
	lib := macLibrary(t)
	m := NewMacros(nil, lib)
	m.saves.write.Lock()
	unlock := func() {
		go func() {
			time.Sleep(50 * time.Millisecond)
			m.saves.write.Unlock()
		}()
	}
	if err := runSaving(t, m, 5*time.Second, unlock); err != nil {
		t.Fatal(err)
	}
	wantAbc(t, lib)
}

// A save that cannot finish in time, or fails, is Run's error, which names
// the library file.
func TestRunReportsTheLibrary(t *testing.T) {
	lib := macLibrary(t)
	m := NewMacros(nil, lib)
	m.saves.write.Lock()
	err := runSaving(t, m, 50*time.Millisecond, nil)
	m.saves.write.Unlock()
	if err == nil || !strings.Contains(err.Error(), "did not finish in time") || !strings.Contains(err.Error(), lib.Path) {
		t.Errorf("a save still held: %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	ro := &library.Store{Path: filepath.Join(dir, "macros.json")}
	if err := ro.Save(nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err = runSaving(t, NewMacros(nil, ro), 5*time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "were not saved to "+ro.Path) || !errors.Is(err, os.ErrPermission) {
		t.Errorf("a save the folder refuses: %v", err)
	}
	if got, err := ro.Load(); err != nil || len(got) != 0 {
		t.Errorf("the refused library reads %+v, %v", got, err)
	}
}
