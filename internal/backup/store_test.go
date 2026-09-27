package backup_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
)

func TestSaveNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	f := newFile(t, dumpImage(t))
	var paths []string
	for range 3 {
		p, err := backup.Save(root, f)
		must(t, err)
		paths = append(paths, p)
	}
	dir := filepath.Join(root, "em11-pro-260d-1282-7b04")
	want := []string{
		filepath.Join(dir, "20260926T120000Z-before-h1.json"),
		filepath.Join(dir, "20260926T120000Z-before-h1-2.json"),
		filepath.Join(dir, "20260926T120000Z-before-h1-3.json"),
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("save %d went to %s, want %s", i, paths[i], want[i])
		}
		if _, err := backup.Load(paths[i]); err != nil {
			t.Error(err)
		}
	}
	entries, err := os.ReadDir(dir)
	must(t, err)
	if len(entries) != 3 {
		t.Errorf("%d files in %s, want 3 and no temporary files", len(entries), dir)
	}
	if st, err := os.Stat(paths[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", st.Mode(), err)
	}
}

func TestSaveAsRefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.json")
	must(t, os.WriteFile(path, []byte("keep"), 0o600))
	if err := backup.SaveAs(path, newFile(t, dumpImage(t))); !errors.Is(err, fs.ErrExist) {
		t.Errorf("SaveAs over a file = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Error("the existing file changed")
	}
}

func TestWriteFileReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.bin")
	must(t, backup.WriteFile(path, []byte("one")))
	must(t, backup.WriteFile(path, []byte("two")))
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("content %q", b)
	}
}

func TestList(t *testing.T) {
	root := t.TempDir()
	c := capture(t, dumpImage(t))
	for i, at := range []time.Time{created.Add(time.Hour), created} {
		f, err := backup.New(c, backup.Meta{Created: at, Label: string(rune('a' + i))})
		must(t, err)
		_, err = backup.Save(root, f)
		must(t, err)
	}
	junk := filepath.Join(root, "em11-pro-260d-1282-7b04", "junk.json")
	must(t, os.WriteFile(junk, []byte("{}"), 0o600))
	list, err := backup.List(root)
	must(t, err)
	if len(list) != 3 {
		t.Fatalf("%d entries", len(list))
	}
	if list[0].Err == nil || list[0].Path != junk {
		t.Errorf("first entry %+v, want the unreadable file", list[0])
	}
	if list[1].File.Label != "b" || list[2].File.Label != "a" {
		t.Errorf("order %s, %s; want oldest first", list[1].File.Label, list[2].File.Label)
	}
}

func TestNames(t *testing.T) {
	f := newFile(t, dumpImage(t))
	f.Label = "  Before: H1 / reset!! "
	if got := f.Name(); got != "20260926T120000Z-before-h1-reset.json" {
		t.Errorf("Name = %s", got)
	}
	f.Model = nil
	if got := f.Dir(); got != "unknown-260d-1282-7b04" {
		t.Errorf("Dir = %s", got)
	}
}
