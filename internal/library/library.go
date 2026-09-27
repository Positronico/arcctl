// Package library keeps the macro library, <data>/macros.json: the macros
// the user keeps whether or not a button runs them. The file, and the files
// the library imports and exports, use arcctl's own JSON schema.
package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/atomicfile"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

const (
	Format = "arcctl-macros/1"
	// MinDelay and MaxDelay bound the wait after an event, in ms.
	MinDelay = 10
	MaxDelay = 0xFFFF
	// MaxFile is the largest library file arcctl reads.
	MaxFile = 4 << 20
)

var (
	ErrFormat = errors.New("library: not an arcctl macro library")
	// ErrTooLarge is a library that would not load again: Load reads at
	// most MaxFile bytes.
	ErrTooLarge = errors.New("library: too large")
)

// Entry is a macro and the repeat mode a binding gives it: 1-250 times, or
// 253, 254 or 255.
type Entry struct {
	Macro mouse.Macro
	Cycle int
}

// Check applies the planner's macro rules: a name of 1-30 UTF-8 bytes, 1-70
// events of the kinds a macro holds, delays of 10-65535 ms and a valid
// repeat mode.
func (e Entry) Check() error {
	if _, err := mouse.EncodeMacro(e.Macro); err != nil {
		return err
	}
	for i, ev := range e.Macro.Events {
		if ev.Delay < MinDelay {
			return fmt.Errorf("event %d: a delay of %d ms is under %d ms", i+1, ev.Delay, MinDelay)
		}
	}
	_, err := mouse.MacroBinding(0, e.Cycle)
	return err
}

// Equal reports whether e and o are the same macro with the same repeat
// mode.
func (e Entry) Equal(o Entry) bool {
	return e.Cycle == o.Cycle && e.Macro.Name == o.Macro.Name && slices.Equal(e.Macro.Events, o.Macro.Events)
}

type fileJSON struct {
	Format string      `json:"format"`
	Macros []entryJSON `json:"macros"`
}

type entryJSON struct {
	Name   string      `json:"name"`
	Cycle  int         `json:"cycle"`
	Events []eventJSON `json:"events"`
}

type eventJSON struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Value  int    `json:"value"`
	Delay  int    `json:"delay_ms"`
}

const (
	press   = "press"
	release = "release"
)

var kindNames = func() map[string]keys.Kind {
	out := map[string]keys.Kind{}
	for _, k := range []keys.Kind{keys.KindModifier, keys.KindKey, keys.KindConsumer, keys.KindMouse, keys.KindMenu} {
		out[k.String()] = k
	}
	return out
}()

// Encode writes entries in the library schema. Every entry must pass Check,
// names must be unique, and the file must fit in MaxFile bytes, so that
// what is written always reads back.
func Encode(entries []Entry) ([]byte, error) {
	if err := checkAll(entries); err != nil {
		return nil, err
	}
	f := fileJSON{Format: Format, Macros: make([]entryJSON, len(entries))}
	for i, e := range entries {
		j := entryJSON{Name: e.Macro.Name, Cycle: e.Cycle, Events: make([]eventJSON, len(e.Macro.Events))}
		for k, ev := range e.Macro.Events {
			action := release
			if ev.Press {
				action = press
			}
			j.Events[k] = eventJSON{Action: action, Kind: ev.Stroke.Kind.String(), Value: int(ev.Stroke.Value), Delay: int(ev.Delay)}
		}
		f.Macros[i] = j
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxFile {
		return nil, fmt.Errorf("%w: %d macros take %d bytes, more than the %d arcctl reads back; delete or export some first",
			ErrTooLarge, len(entries), len(b)+1, MaxFile)
	}
	return append(b, '\n'), nil
}

// Decode reads the library schema. It refuses the whole file when any entry
// breaks the rules Check applies or repeats a name.
func Decode(b []byte) ([]Entry, error) {
	var f fileJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: data after the library", ErrFormat)
	}
	if f.Format != Format {
		return nil, fmt.Errorf("%w: format %q, want %q", ErrFormat, f.Format, Format)
	}
	out := make([]Entry, len(f.Macros))
	for i, j := range f.Macros {
		e := Entry{Macro: mouse.Macro{Name: j.Name, Events: make([]mouse.Event, len(j.Events))}, Cycle: j.Cycle}
		for k, ev := range j.Events {
			d, err := decodeEvent(ev)
			if err != nil {
				return nil, fmt.Errorf("%w: macro %d %q event %d: %v", ErrFormat, i+1, j.Name, k+1, err)
			}
			e.Macro.Events[k] = d
		}
		out[i] = e
	}
	if err := checkAll(out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	return out, nil
}

func decodeEvent(ev eventJSON) (mouse.Event, error) {
	var d mouse.Event
	switch ev.Action {
	case press:
		d.Press = true
	case release:
	default:
		return d, fmt.Errorf("action %q is neither %q nor %q", ev.Action, press, release)
	}
	kind, ok := kindNames[ev.Kind]
	if !ok {
		return d, fmt.Errorf("unknown kind %q", ev.Kind)
	}
	if ev.Value < 0 || ev.Value > 0xFFFF {
		return d, fmt.Errorf("value %d is out of range", ev.Value)
	}
	if ev.Delay < 0 || ev.Delay > MaxDelay {
		return d, fmt.Errorf("delay %d ms is out of range", ev.Delay)
	}
	d.Stroke = keys.Stroke{Kind: kind, Value: uint16(ev.Value)}
	d.Delay = uint16(ev.Delay)
	return d, nil
}

func checkAll(entries []Entry) error {
	for i, e := range entries {
		if err := e.Check(); err != nil {
			return fmt.Errorf("macro %d %q: %w", i+1, e.Macro.Name, err)
		}
		if j := Index(entries[:i], e.Macro.Name); j >= 0 {
			return fmt.Errorf("macro %d repeats the name %q of macro %d", i+1, e.Macro.Name, j+1)
		}
	}
	return nil
}

// Index is the position of the entry named name; -1 when there is none.
func Index(entries []Entry, name string) int {
	return slices.IndexFunc(entries, func(e Entry) bool { return e.Macro.Name == name })
}

// Merge adds the entries of in whose names have is missing. An entry equal
// to the one of the same name is already there; one that differs is left
// out and its name reported.
func Merge(have, in []Entry) (out []Entry, added int, clashes []string) {
	out = slices.Clone(have)
	for _, e := range in {
		switch i := Index(out, e.Macro.Name); {
		case i < 0:
			out = append(out, e)
			added++
		case !out[i].Equal(e):
			clashes = append(clashes, e.Macro.Name)
		}
	}
	return out, added, clashes
}

// Store is the library file.
type Store struct{ Path string }

// Load reads the library; a missing file is an empty library.
func (s Store) Load() ([]Entry, error) {
	out, err := ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// Save replaces the library file atomically.
func (s Store) Save(entries []Entry) error {
	b, err := Encode(entries)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.Path, b)
}

// ReadFile reads a file in the library schema, such as an export.
func ReadFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	switch {
	case err != nil:
		return nil, err
	case len(b) > MaxFile:
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrFormat, path, MaxFile)
	}
	out, err := Decode(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// UserPath resolves a path the user typed: a leading ~ is their home
// folder, and a relative path starts from the working folder.
func UserPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", errors.New("no file named")
	case p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// WriteNew exports entries to a new file at path; it never replaces one.
func WriteNew(path string, entries []Entry) error {
	b, err := Encode(entries)
	if err != nil {
		return err
	}
	return atomicfile.WriteNew(path, b)
}
