package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
)

func TestOpenDetectsKinds(t *testing.T) {
	dir := t.TempDir()
	bin, err := backup.ExportPartialBin(em11(t), richImage(t))
	must(t, err)
	full := richImage(t).Bytes()[:flash.Size]
	files := map[string][]byte{
		"backup.json": append([]byte("\n  "), encode(t, newFile(t, dumpImage(t)))...),
		"web.bin":     bin,
		"dump.bin":    dumpBytes(t),
		"full.bin":    full,
	}
	want := map[string]struct {
		kind  backup.Kind
		known int
	}{
		"backup.json": {backup.KindBackup, 256},
		"web.bin":     {backup.KindBin, 191 + 14 + 20 + 14 + 8},
		"dump.bin":    {backup.KindDump, 256},
		"full.bin":    {backup.KindDump, flash.Size},
	}
	for name, b := range files {
		path := filepath.Join(dir, name)
		must(t, os.WriteFile(path, b, 0o600))
		s, err := backup.Open(path)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if s.Kind != want[name].kind || known(s.Image) != want[name].known || s.Path != path {
			t.Errorf("%s: %v with %d bytes, want %v with %d", name, s.Kind, known(s.Image), want[name].kind, want[name].known)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":      nil,
		"too long":   make([]byte, flash.Size+1),
		"bin-sized":  make([]byte, backup.BinSize),
		"other json": []byte(`{"format":"something"}`),
	} {
		if _, err := backup.Parse(b); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name == "empty" && !errors.Is(err, backup.ErrUnknownFile) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"format":"arcctl-backup/1"}`))
	f.Add(make([]byte, 256))
	bin := make([]byte, backup.BinSize)
	copy(bin[flash.Size:], "Compx Inc")
	copy(bin[flash.Size+32:], "mouse")
	f.Add(bin)
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := backup.Parse(b)
		if err != nil {
			return
		}
		if s.Image == nil {
			t.Fatal("parsed without an image")
		}
		if s.Kind == backup.KindBackup {
			if err := s.File.Validate(); err != nil {
				t.Fatalf("a decoded backup does not validate: %v", err)
			}
		}
	})
}
