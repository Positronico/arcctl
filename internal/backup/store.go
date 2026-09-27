package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Dir is the folder, under the backups root, that holds the backups of this
// device: the model's name and the identity key.
func (f *File) Dir() string {
	model := "unknown"
	if f.Model != nil && f.Model.Name != "" {
		model = strings.TrimPrefix(slug(f.Model.Name), "protoarc-")
	}
	return model + "-" + f.Key()
}

// Name is the file name of the backup: its creation time in UTC, then the
// label, if any.
func (f *File) Name() string {
	name := f.Created.UTC().Format("20060102T150405Z")
	if l := slug(f.Label); l != "" {
		name += "-" + l
	}
	return name + ".json"
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}

// Save writes f under root as Dir()/Name(). An existing backup is never
// replaced: a numbered name is used instead. It returns the path written.
func Save(root string, f *File) (string, error) {
	data, err := encode(f)
	if err != nil {
		return "", err
	}
	base := filepath.Join(root, f.Dir(), strings.TrimSuffix(f.Name(), ".json"))
	for i := 1; ; i++ {
		path := base + ".json"
		if i > 1 {
			path = base + "-" + strconv.Itoa(i) + ".json"
		}
		err := WriteNew(path, data)
		if !errors.Is(err, fs.ErrExist) {
			return path, err
		}
		if i >= 100 {
			return "", err
		}
	}
}

// SaveAs writes f to path, which must not exist yet.
func SaveAs(path string, f *File) error {
	data, err := encode(f)
	if err != nil {
		return err
	}
	return WriteNew(path, data)
}

// Load reads and validates the backup at path.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Decode(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Listing is one backup found under the backups root; Err says why it could
// not be read.
type Listing struct {
	Path string
	File *File
	Err  error
}

// List finds every backup under root, oldest first.
func List(root string) ([]Listing, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]Listing, 0, len(paths))
	for _, p := range paths {
		f, err := Load(p)
		out = append(out, Listing{Path: p, File: f, Err: err})
	}
	slices.SortStableFunc(out, func(a, b Listing) int {
		return created(a).Compare(created(b))
	})
	return out, nil
}

func created(l Listing) time.Time {
	if l.File == nil {
		return time.Time{}
	}
	return l.File.Created
}

func encode(f *File) ([]byte, error) {
	var buf bytes.Buffer
	if err := f.Encode(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteNew writes data to a new file at path, all or nothing: the bytes go to
// a temporary file that is synced and then linked into place, which fails
// with fs.ErrExist rather than replace a file. On file systems without hard
// links it renames instead, after checking that path is free.
func WriteNew(path string, data []byte) error {
	return write(path, data, false)
}

// WriteFile replaces the file at path atomically.
func WriteFile(path string, data []byte) error {
	return write(path, data, true)
}

func write(path string, data []byte, replace bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	switch {
	case replace:
		err = os.Rename(name, path)
	default:
		err = os.Link(name, path)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			if _, serr := os.Lstat(path); serr == nil {
				return &fs.PathError{Op: "create", Path: path, Err: fs.ErrExist}
			}
			err = os.Rename(name, path)
		}
	}
	if err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes the new directory entry durable where the OS allows it.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
