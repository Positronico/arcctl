package internal_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/positronico/arcctl/internal/vectors"
)

// machinePaths match parts of a device path that belong to one machine: the
// SoC's IORegistry classes and the USB port a device sits on. Committed
// fixtures use placeholders such as IOService:/x/XHC@00000000/... instead.
var machinePaths = []*regexp.Regexp{
	regexp.MustCompile(`AppleT[0-9]{4}`),
	regexp.MustCompile(`usb-drd[0-9]`),
	regexp.MustCompile(`XHCI@[0-9a-f]{8}`),
	regexp.MustCompile(`-port-(hs|ss)@[0-9a-f]{8}`),
}

func TestNoMachineDevicePaths(t *testing.T) {
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "dist"):
			return filepath.SkipDir
		case d.IsDir() || !d.Type().IsRegular():
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return nil
		}
		for _, re := range machinePaths {
			if loc := re.FindIndex(b); loc != nil {
				line := bytes.Count(b[:loc[0]], []byte("\n")) + 1
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d holds %q, part of a real device path; use a placeholder", rel, line, b[loc[0]:loc[1]])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A --record transcript holds the receiver's address and the user's shortcut
// and macro bytes until 'arcctl redact' copies it, so git ignores transcripts
// anywhere but testdata/transcripts, where a test checks they are redacted.
func TestTranscriptsAreIgnored(t *testing.T) {
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--git-dir").Run(); err != nil {
		t.Skip("not a git checkout")
	}
	for path, ignored := range map[string]bool{
		"info.jsonl":                         true,
		"internal/cli/f.jsonl":               true,
		"testdata/transcripts/h0-info.jsonl": false,
	} {
		err := exec.Command("git", "-C", root, "check-ignore", "-q", "--no-index", path).Run()
		if got := err == nil; got != ignored {
			t.Errorf("git ignores %s: %v, want %v", path, got, ignored)
		}
	}
}
