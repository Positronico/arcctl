package vectors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const FileVersion = 1

var (
	Groups  = []string{"packet", "record", "keyboard"}
	Sources = []string{"WE", "TS", "UI", "GO", "MC", "GM", "KP", "KU", "dump"}
)

type File struct {
	Version   int      `json:"version"`
	Generator string   `json:"generator"`
	Vectors   []Vector `json:"vectors"`
}

type Vector struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Hex    string `json:"hex"`
	Check  string `json:"check"`
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
}

func Parse(data []byte) (*File, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the top-level object")
	}
	return &f, nil
}

func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func ModuleRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func DefaultPath(dir string) (string, error) {
	root, err := ModuleRoot(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "testdata", "vectors.json"), nil
}

func ParseHex(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty hex")
	}
	fields := strings.Split(s, " ")
	out := make([]byte, len(fields))
	for i, tok := range fields {
		if len(tok) != 2 || !isLowerHex(tok[0]) || !isLowerHex(tok[1]) {
			return nil, fmt.Errorf("token %d %q is not two lowercase hex digits separated by single spaces", i, tok)
		}
		b, err := strconv.ParseUint(tok, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("token %d %q: %w", i, tok, err)
		}
		out[i] = byte(b)
	}
	return out, nil
}

func isLowerHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f')
}

func (v Vector) Bytes() ([]byte, error) {
	return ParseHex(v.Hex)
}

func sum(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return s
}

func Satisfies(check string, b []byte) error {
	switch {
	case check == "packet":
		if len(b) != 16 {
			return fmt.Errorf("packet has %d bytes, want 16", len(b))
		}
		if s := 8 + sum(b); s != 0x55 {
			return fmt.Errorf("(8 + sum) & 0xff = 0x%02x, want 0x55", s)
		}
	case check == "sum55":
		if s := sum(b); s != 0x55 {
			return fmt.Errorf("sum & 0xff = 0x%02x, want 0x55", s)
		}
	case strings.HasPrefix(check, "sum55_from:"):
		arg := strings.TrimPrefix(check, "sum55_from:")
		n, err := strconv.Atoi(arg)
		if err != nil || strconv.Itoa(n) != arg {
			return fmt.Errorf("bad offset in check %q", check)
		}
		if n < 0 || n >= len(b) {
			return fmt.Errorf("offset %d outside %d bytes", n, len(b))
		}
		if s := sum(b[n:]); s != 0x55 {
			return fmt.Errorf("sum of bytes[%d:] & 0xff = 0x%02x, want 0x55", n, s)
		}
	case check == "none":
	default:
		return fmt.Errorf("unknown check %q", check)
	}
	return nil
}

func (v Vector) Validate() error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("empty name")
	}
	if !slices.Contains(Groups, v.Group) {
		return fmt.Errorf("unknown group %q", v.Group)
	}
	if v.Source == "" {
		return errors.New("empty source")
	}
	for _, tag := range strings.Split(v.Source, ",") {
		if !slices.Contains(Sources, strings.TrimSpace(tag)) {
			return fmt.Errorf("unknown source tag %q", tag)
		}
	}
	if v.Check == "none" && strings.TrimSpace(v.Note) == "" {
		return errors.New(`check "none" needs a note explaining why`)
	}
	b, err := v.Bytes()
	if err != nil {
		return err
	}
	return Satisfies(v.Check, b)
}

func (f *File) Validate() error {
	var errs []error
	if f.Version != FileVersion {
		errs = append(errs, fmt.Errorf("version %d, want %d", f.Version, FileVersion))
	}
	if strings.TrimSpace(f.Generator) == "" {
		errs = append(errs, errors.New("empty generator"))
	}
	if len(f.Vectors) == 0 {
		errs = append(errs, errors.New("no vectors"))
	}
	seen := make(map[string]int, len(f.Vectors))
	for i, v := range f.Vectors {
		if j, dup := seen[v.Name]; dup {
			errs = append(errs, fmt.Errorf("vector %d: name %q already used by vector %d", i, v.Name, j))
		} else {
			seen[v.Name] = i
		}
		if err := v.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("vector %d %q: %w", i, v.Name, err))
		}
	}
	return errors.Join(errs...)
}
