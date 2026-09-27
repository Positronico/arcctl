package backup

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

var _ session.Backups = Store{}

// Store saves backups under a backups root and lists them per device. The
// session saves the backups I1 requires before a write through it.
type Store struct {
	Root   string
	Tool   string
	Source string // SourceDevice when empty
	OS     keys.OS
	Now    func() time.Time // time.Now when nil
}

// Save writes a backup of c, with the label, under the root and returns its
// path. It never replaces a backup.
// Image reads the backup at path, checked as Load checks it, and returns
// the bytes it holds.
func (s Store) Image(path string) (*flash.Image, error) {
	f, err := Load(path)
	if err != nil {
		return nil, err
	}
	return f.Image(), nil
}

func (s Store) Save(c session.Capture, label string) (string, error) {
	if s.Root == "" {
		return "", errors.New("backup: no backups folder")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	meta := Meta{Tool: s.Tool, Source: s.Source, Label: label, Created: now(), OS: s.OS}
	if h := c.Handshake; h != nil {
		conn := h.Conn
		meta.Conn = &conn
	}
	f, err := New(c, meta)
	if err != nil {
		return "", err
	}
	return Save(s.Root, f)
}

// List returns the backups of one device, oldest first: the files of its
// folders under the root, those that cannot be read included.
func (s Store) List(id plan.Identity) ([]Listing, error) {
	all, err := List(s.Root)
	if err != nil {
		return nil, err
	}
	suffix := "-" + id.Key()
	var out []Listing
	for _, l := range all {
		if strings.HasSuffix(filepath.Base(filepath.Dir(l.Path)), suffix) {
			out = append(out, l)
		}
	}
	return out, nil
}

// Automatic reports whether a write saved the backup on its own (I1).
func (f *File) Automatic() bool {
	return f.Label == session.LabelBeforeWrite || f.Label == session.LabelFirstWrite
}
