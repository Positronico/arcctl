// Package atomicfile writes files all or nothing: the bytes go to a
// temporary file in the same folder, which is synced and then put in place,
// so a reader sees either the old file or the whole new one.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

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
