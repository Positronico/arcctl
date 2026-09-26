package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	factsDir := flag.String("facts", "facts", "directory holding the facts JSON files")
	verified := flag.String("verified", "verified.json", "the list of hardware stages passed")
	root := flag.String("root", "../..", "module root that the generated paths are relative to")
	flag.Parse()
	if err := run(*factsDir, *verified, *root); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(factsDir, verifiedFile, root string) error {
	verified, err := os.ReadFile(verifiedFile)
	if err != nil {
		return err
	}
	out, err := build(os.DirFS(factsDir), verified)
	if err != nil {
		return err
	}
	return writeAll(root, out)
}

func build(fsys fs.FS, verifiedJSON []byte) ([]output, error) {
	f, err := loadFacts(fsys)
	if err != nil {
		return nil, err
	}
	if err := validate(f); err != nil {
		return nil, err
	}
	vs, err := parseVerified(verifiedJSON, f)
	if err != nil {
		return nil, err
	}
	out, err := generate(f)
	if err != nil {
		return nil, err
	}
	data, err := renderVerified(vs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", verifiedPath, err)
	}
	return append(out, output{verifiedPath, data}), nil
}

func writeAll(root string, out []output) (err error) {
	type pending struct{ tmp, dst string }
	var staged []pending
	defer func() {
		if err != nil {
			for _, p := range staged {
				_ = os.Remove(p.tmp)
			}
		}
	}()
	for _, o := range out {
		dst := filepath.Join(root, filepath.FromSlash(o.path))
		old, err := os.ReadFile(dst)
		if err == nil && bytes.Equal(old, o.data) {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".gen-*")
		if err != nil {
			return err
		}
		staged = append(staged, pending{tmp.Name(), dst})
		_, werr := tmp.Write(o.data)
		if err := errors.Join(werr, tmp.Close(), os.Chmod(tmp.Name(), 0o644)); err != nil {
			return err
		}
	}
	for _, p := range staged {
		if err := os.Rename(p.tmp, p.dst); err != nil {
			return err
		}
	}
	return nil
}
