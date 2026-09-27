package internal_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	hidioPath   = module + "/internal/hidio"
	sessionPath = module + "/internal/session"
	safetyPath  = module + "/internal/safety"
)

type goPackage struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
}

// TestGuardWiring enforces the guard wiring in docs/decisions.md. Only the
// session (and the tests of hidio and safety, whose emulator link stands in
// for the session) may call Guard.Set, no exported function
// outside hidio returns a hidio.Raw, and no type outside hidio that embeds a
// Transport replaces its Write. So a packet reaches a device only through a
// Transport that Guarded made, under the policy the session chose. The device
// Raws themselves are unexported types of hidio.
func TestGuardWiring(t *testing.T) {
	pkgs := listPackages(t)
	fset := token.NewFileSet()
	imp := exportImporter(t, fset)
	hidio, err := imp.Import(hidioPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := hidio.Scope().Lookup("Raw").Type().Underlying().(*types.Interface)
	transport := hidio.Scope().Lookup("Transport").Type().Underlying().(*types.Interface)
	guard := hidio.Scope().Lookup("Guard").(*types.TypeName)

	for _, p := range pkgs {
		if p.ImportPath == hidioPath {
			checkHidioExports(t, hidio, raw)
			continue
		}
		files := map[string][]string{p.ImportPath: append(slices.Clone(p.GoFiles), p.TestGoFiles...)}
		if len(p.XTestGoFiles) > 0 {
			files[p.ImportPath+"_test"] = p.XTestGoFiles
		}
		for path, names := range files {
			pkg, info := typeCheck(t, fset, imp, path, p.Dir, names)
			if p.ImportPath != sessionPath {
				for _, pos := range guardSets(info, guard) {
					where := fset.Position(pos)
					if p.ImportPath == safetyPath && strings.HasSuffix(where.Filename, "_test.go") {
						continue
					}
					t.Errorf("%s calls hidio.Guard.Set; only the session may switch the policy", where)
				}
			}
			if path == p.ImportPath {
				for _, where := range rawResults(pkg, raw) {
					t.Errorf("%s.%s returns a hidio.Raw; only hidio may hand one out", path, where)
				}
			}
			for _, name := range writeOverrides(pkg, transport) {
				t.Errorf("%s.%s is a hidio.Transport with its own Write, which would bypass the guard", path, name)
			}
		}
	}
}

// checkHidioExports allows hidio to return a Raw only from constructors whose
// Raw is not a device: the Pipe, the Replay and a Recorder's wrapper of a Raw
// the caller already holds.
func checkHidioExports(t *testing.T, hidio *types.Package, raw *types.Interface) {
	allowed := []string{"NewPipe", "NewReplay", "OpenReplay", "Recorder.Wrap"}
	for _, where := range rawResults(hidio, raw) {
		if !slices.Contains(allowed, where) {
			t.Errorf("hidio.%s returns a hidio.Raw; device Raws must stay inside hidio", where)
		}
	}
}

func listPackages(t *testing.T) []goPackage {
	t.Helper()
	out, err := exec.Command("go", "list", "-json=ImportPath,Dir,GoFiles,TestGoFiles,XTestGoFiles", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []goPackage
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func exportImporter(t *testing.T, fset *token.FileSet) types.Importer {
	t.Helper()
	out, err := exec.Command("go", "list", "-export", "-deps", "-test", "-f", "{{.ImportPath}}={{.Export}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	files := map[string]string{}
	for sc := bufio.NewScanner(bytes.NewReader(out)); sc.Scan(); {
		if path, file, ok := strings.Cut(sc.Text(), "="); ok && file != "" && !strings.Contains(path, " ") {
			files[path] = file
		}
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := files[path]
		if !ok {
			return nil, errors.New("no export data for " + path)
		}
		return os.Open(f)
	})
}

// typeCheck checks the files of one package against the compiled export data.
// Errors are ignored: a test file may use names that only its package's own
// test files export, and the selections that matter still resolve.
func typeCheck(t *testing.T, fset *token.FileSet, imp types.Importer, path, dir string, names []string) (*types.Package, *types.Info) {
	t.Helper()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: imp, Error: func(error) {}}
	pkg, _ := conf.Check(path, fset, files, info)
	return pkg, info
}

func guardSets(info *types.Info, guard *types.TypeName) []token.Pos {
	var out []token.Pos
	for expr, sel := range info.Selections {
		fn, ok := sel.Obj().(*types.Func)
		if !ok || fn.Name() != "Set" {
			continue
		}
		recv := fn.Signature().Recv().Type()
		if p, ok := recv.(*types.Pointer); ok {
			recv = p.Elem()
		}
		if n, ok := recv.(*types.Named); ok && n.Obj() == guard {
			out = append(out, expr.Sel.Pos())
		}
	}
	return out
}

// writeOverrides names the types of pkg that are a hidio.Transport, which only
// embedding one makes them, and declare a Write of their own.
func writeOverrides(pkg *types.Package, transport *types.Interface) []string {
	if pkg == nil {
		return nil
	}
	var out []string
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !types.Implements(types.NewPointer(tn.Type()), transport) {
			continue
		}
		for sel := range types.NewMethodSet(types.NewPointer(tn.Type())).Methods() {
			if sel.Obj().Name() == "Write" && len(sel.Index()) == 1 {
				out = append(out, name)
			}
		}
	}
	return out
}

// rawResults names the exported functions and methods of pkg with a result
// that is a hidio.Raw.
func rawResults(pkg *types.Package, raw *types.Interface) []string {
	if pkg == nil {
		return nil
	}
	isRaw := func(sig *types.Signature) bool {
		for v := range sig.Results().Variables() {
			if types.Implements(v.Type(), raw) {
				return true
			}
		}
		return false
	}
	var out []string
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types.Func:
			if isRaw(obj.Signature()) {
				out = append(out, name)
			}
		case *types.TypeName:
			ms := types.NewMethodSet(types.NewPointer(obj.Type()))
			for sel := range ms.Methods() {
				if m := sel.Obj(); m.Exported() && isRaw(m.(*types.Func).Signature()) {
					out = append(out, name+"."+m.Name())
				}
			}
		}
	}
	return out
}
