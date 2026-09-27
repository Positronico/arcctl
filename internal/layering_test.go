package internal_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/positronico/arcctl"

var (
	pure     = []string{"wire", "flash", "catalog", "caps", "keys", "mouse", "keyboard", "plan"}
	optional = []string{"caps"}
	banned   = []string{"os", "net", "time", "syscall"}
)

type listed struct {
	ImportPath string
	Imports    []string
	Deps       []string
}

// TestPureLayering enforces the layering rule in docs/decisions.md. A pure package may
// not import the OS, network or clock packages itself, and everything it depends on
// must be the standard library or another pure package. Standard-library packages
// may pull in os or time. Test files are not checked.
func TestPureLayering(t *testing.T) {
	out, err := goList(t, "-json=ImportPath,Imports,Deps", "./...")
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	pkgs := map[string]listed{}
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listed
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("go list output: %v", err)
		}
		pkgs[p.ImportPath] = p
	}

	isPure := func(path string) bool {
		name, ok := strings.CutPrefix(path, module+"/internal/")
		return ok && slices.Contains(pure, name)
	}
	for _, name := range pure {
		path := module + "/internal/" + name
		p, ok := pkgs[path]
		if !ok {
			if !slices.Contains(optional, name) {
				t.Errorf("pure package %s not found", path)
			}
			continue
		}
		for _, imp := range p.Imports {
			root, _, _ := strings.Cut(imp, "/")
			if slices.Contains(banned, root) {
				t.Errorf("pure package %s imports %s", name, imp)
			}
		}
		for _, dep := range p.Deps {
			first, _, _ := strings.Cut(dep, "/")
			switch {
			case dep == module || strings.HasPrefix(dep, module+"/"):
				if !isPure(dep) {
					t.Errorf("pure package %s depends on %s, which is not a pure package", name, dep)
				}
			case strings.Contains(first, "."):
				t.Errorf("pure package %s depends on %s, outside the standard library", name, dep)
			}
		}
	}
}
