package library

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

func tap(kind keys.Kind, v uint16, delay uint16) []mouse.Event {
	s := keys.Stroke{Kind: kind, Value: v}
	return []mouse.Event{{Press: true, Stroke: s, Delay: delay}, {Stroke: s, Delay: delay}}
}

func sample() []Entry {
	return []Entry{
		{Macro: mouse.Macro{Name: "ab", Events: append(tap(keys.KindKey, 0x04, 50), tap(keys.KindKey, 0x05, 10)...)}, Cycle: 1},
		{Macro: mouse.Macro{Name: "click", Events: tap(keys.KindMouse, 1, 20)}, Cycle: 254},
		{Macro: mouse.Macro{Name: "复制", Events: append([]mouse.Event{{Press: true, Stroke: keys.LMeta.Stroke(), Delay: 10}},
			append(tap(keys.KindMenu, 1, 10), mouse.Event{Stroke: keys.LMeta.Stroke(), Delay: 65535})...)}, Cycle: 250},
	}
}

func TestRoundTrip(t *testing.T) {
	b, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"format": "arcctl-macros/1"`, `"action": "press"`, `"kind": "mouse"`, `"delay_ms": 65535`, `"cycle": 254`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the file lacks %s:\n%s", want, b)
		}
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range sample() {
		if !got[i].Equal(e) {
			t.Errorf("entry %d: %+v, want %+v", i, got[i], e)
		}
	}
}

func TestDecodeRefuses(t *testing.T) {
	good, err := Encode(sample()[:1])
	if err != nil {
		t.Fatal(err)
	}
	s := string(good)
	cases := map[string]string{
		"format":       strings.Replace(s, Format, "arcctl-backup/1", 1),
		"unknown kind": strings.Replace(s, `"kind": "key"`, `"kind": "joystick"`, 1),
		"consumer":     strings.Replace(s, `"kind": "key"`, `"kind": "consumer"`, 1),
		"action":       strings.Replace(s, `"action": "press"`, `"action": "hold"`, 1),
		"short delay":  strings.Replace(s, `"delay_ms": 50`, `"delay_ms": 9`, 1),
		"long delay":   strings.Replace(s, `"delay_ms": 50`, `"delay_ms": 65536`, 1),
		"value":        strings.Replace(s, `"value": 4`, `"value": 256`, 1),
		"zero value":   strings.Replace(s, `"value": 4`, `"value": 0`, 1),
		"cycle":        strings.Replace(s, `"cycle": 1`, `"cycle": 251`, 1),
		"empty name":   strings.Replace(s, `"name": "ab"`, `"name": ""`, 1),
		"long name":    strings.Replace(s, `"name": "ab"`, `"name": "`+strings.Repeat("x", 31)+`"`, 1),
		"extra field":  strings.Replace(s, `"cycle": 1`, `"cycle": 1, "slot": 3`, 1),
		"not json":     s[:len(s)/2],
	}
	for name, in := range cases {
		if in == s {
			t.Fatalf("%s: the replacement did not apply", name)
		}
		if _, err := Decode([]byte(in)); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v, want ErrFormat", name, err)
		}
	}
	twice := append(sample()[:1], sample()[0])
	if _, err := Encode(twice); err == nil {
		t.Error("two macros with one name encoded")
	}
	many := Entry{Macro: mouse.Macro{Name: "many"}, Cycle: 1}
	for range 36 {
		many.Macro.Events = append(many.Macro.Events, tap(keys.KindKey, 4, 10)...)
	}
	if err := many.Check(); err == nil {
		t.Error("72 events passed the check")
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s := Store{Path: filepath.Join(dir, "sub", "macros.json")}
	got, err := s.Load()
	if err != nil || got != nil {
		t.Fatalf("missing library: %v, %v", got, err)
	}
	if err := s.Save(sample()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(s.Path); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("library file: %v, mode %v", err, fi.Mode())
	}
	got, err = s.Load()
	if err != nil || len(got) != 3 {
		t.Fatalf("load: %d entries, %v", len(got), err)
	}
	if err := s.Save(got[:1]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(); len(got) != 1 {
		t.Errorf("after the second save: %d entries", len(got))
	}
	if err := os.WriteFile(s.Path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrFormat) {
		t.Errorf("a broken library loads: %v", err)
	}

	out := filepath.Join(dir, "export.json")
	if err := WriteNew(out, sample()); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(out, sample()[:1]); !errors.Is(err, fs.ErrExist) {
		t.Errorf("export over a file: %v", err)
	}
	in, err := ReadFile(out)
	if err != nil || len(in) != 3 {
		t.Fatalf("import: %d entries, %v", len(in), err)
	}
}

func TestMerge(t *testing.T) {
	have := sample()[:2]
	other := sample()[2]
	changed := sample()[0]
	changed.Cycle = 3
	out, added, clashes := Merge(have, []Entry{sample()[1], other, changed})
	if added != 1 || len(out) != 3 || !out[2].Equal(other) {
		t.Errorf("added %d, %d entries", added, len(out))
	}
	if len(clashes) != 1 || clashes[0] != "ab" {
		t.Errorf("clashes %q", clashes)
	}
	if len(have) != 2 {
		t.Error("Merge changed its input")
	}
}

func TestUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got, err := UserPath(" ~/m.json "); err != nil || got != filepath.Join(home, "m.json") {
		t.Errorf("~/m.json: %q, %v", got, err)
	}
	if got, err := UserPath("m.json"); err != nil || !filepath.IsAbs(got) {
		t.Errorf("m.json: %q, %v", got, err)
	}
	if _, err := UserPath("  "); err == nil {
		t.Error("an empty path resolved")
	}
}

// What Save writes always loads again: a library past MaxFile is refused
// before it is written.
func TestSaveRefusesWhatLoadWouldRefuse(t *testing.T) {
	var evs []mouse.Event
	for len(evs) < mouse.MaxMacroEvents {
		evs = append(evs, tap(keys.KindKey, 0x04, 65535)...)
	}
	var entries []Entry
	for i := range 800 {
		entries = append(entries, Entry{Macro: mouse.Macro{Name: "m" + strconv.Itoa(i), Events: evs}, Cycle: 250})
	}
	s := Store{Path: filepath.Join(t.TempDir(), "macros.json")}
	if err := s.Save(entries); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(s.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refused library was written: %v", err)
	}
	if err := s.Save(entries[:100]); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || len(got) != 100 {
		t.Fatalf("load: %d, %v", len(got), err)
	}
}

func TestDecodeRefusesTrailingData(t *testing.T) {
	b, err := Encode(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"{}", "x", `{"format":"arcctl-macros/1","macros":[]}`} {
		if _, err := Decode(append(bytes.Clone(b), tail...)); !errors.Is(err, ErrFormat) {
			t.Errorf("%q after the library: %v", tail, err)
		}
	}
	if _, err := Decode(append(bytes.Clone(b), " \n\t"...)); err != nil {
		t.Errorf("white space after the library: %v", err)
	}
}
