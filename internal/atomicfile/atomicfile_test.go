package atomicfile_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/positronico/arcctl/internal/atomicfile"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// names lists dir, so a test sees any temporary file left behind.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	must(t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestWriteFileReplaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "x.bin")
	must(t, atomicfile.WriteFile(path, []byte("one")))
	must(t, atomicfile.WriteFile(path, []byte("two")))
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Errorf("content %q", b)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", st.Mode(), err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v, %v", st.Mode(), err)
	}
	if got := names(t, dir); len(got) != 1 || got[0] != "x.bin" {
		t.Errorf("the folder holds %v", got)
	}
}

func TestWriteNewNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	must(t, atomicfile.WriteNew(path, []byte("first")))
	err := atomicfile.WriteNew(path, []byte("second"))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteNew over a file = %v, want fs.ErrExist", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "first" {
		t.Errorf("the existing file changed to %q", b)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", st.Mode(), err)
	}
	if got := names(t, dir); len(got) != 1 {
		t.Errorf("the folder holds %v", got)
	}
}

func TestWriteFailsWhereNoFolderCanBe(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	must(t, os.WriteFile(file, []byte("x"), 0o600))
	for _, write := range []func(string, []byte) error{atomicfile.WriteFile, atomicfile.WriteNew} {
		if err := write(filepath.Join(file, "x.json"), []byte("y")); err == nil {
			t.Error("wrote under a regular file")
		}
	}
}
