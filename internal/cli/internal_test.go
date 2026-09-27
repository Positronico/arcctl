package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/vectors"
)

func TestUsbhidMatchesTheVendoredCopy(t *testing.T) {
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, filepath.FromSlash(usbhidDir))
	files, err := filepath.Glob(filepath.Join(dir, "patches", "*.patch"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".patch"))
	}
	slices.Sort(names)
	if !slices.Equal(names, usbhidPatches) {
		t.Errorf("patches folder has %v, version prints %v", names, usbhidPatches)
	}
	notice, err := os.ReadFile(filepath.Join(dir, "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notice), "Version:  "+usbhidVersion+"\n") || !strings.Contains(string(notice), usbhidModule) {
		t.Errorf("version prints %s %s, which %s/NOTICE does not name", usbhidModule, usbhidVersion, usbhidDir)
	}
}

func TestParseRange(t *testing.T) {
	good := map[string]flash.Extent{
		"0:256":       {Addr: 0, Len: 256},
		"0x60:0x80":   {Addr: 0x60, Len: 0x20},
		"6912+75":     {Addr: 6912, Len: 75},
		"0x1b00+10":   {Addr: 6912, Len: 10},
		"16383:16384": {Addr: 16383, Len: 1},
	}
	for s, want := range good {
		if got, err := parseRange(s); err != nil || got != want {
			t.Errorf("parseRange(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "10", "10:10", "20:10", "0+0", "-1:5", "0:16385", "16384+1", "a:b", "1+x"} {
		if _, err := parseRange(s); err == nil {
			t.Errorf("parseRange(%q) accepted", s)
		}
	}
}

func TestParseArgsInterleaved(t *testing.T) {
	r := &runner{g: newGlobals()}
	fs := r.flagSet("show", "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseArgs(fs, []string{"--os", "win", "a.json", "--json", "--device", "x", "--", "-b"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pos, []string{"a.json", "-b"}) || !*asJSON || r.g.os != "win" || r.g.device != "x" {
		t.Errorf("pos %v, json %v, globals %+v", pos, *asJSON, r.g)
	}
}

func TestWrapKeepsWidth(t *testing.T) {
	words := strings.Fields(strings.Repeat("setting.dynamic-sensitivity ", 12))
	for _, l := range strings.Split(wrap("  untested      ", strings.Repeat(" ", 16), words, " ", 80), "\n") {
		if len(l) > 80 {
			t.Errorf("line of %d columns: %q", len(l), l)
		}
	}
}

func TestASCIIWriter(t *testing.T) {
	var b strings.Builder
	w := asciiWriter{&b}
	if _, err := w.Write([]byte("Cmd+↑, Ctrl+→, é")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "Cmd+Up, Ctrl+Right, ?" {
		t.Errorf("got %q", got)
	}
}

func TestFill(t *testing.T) {
	got := fill("arcctl: "+strings.Repeat("word ", 20)+"\n  a b", "  ", 30)
	for _, l := range strings.Split(got, "\n") {
		if len(l) > 30 {
			t.Errorf("line %q is wider than 30", l)
		}
	}
	if !strings.HasSuffix(got, "\n  a b") || !strings.HasPrefix(strings.Split(got, "\n")[1], "  word") {
		t.Errorf("fill kept the wrong layout:\n%s", got)
	}
}
