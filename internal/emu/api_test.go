package emu_test

import (
	"bufio"
	"bytes"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const (
	emuPath   = "github.com/positronico/arcctl/internal/emu"
	hidioPath = "github.com/positronico/arcctl/internal/hidio"
)

// TestNoUnguardedWritePath checks that nothing emu exports is, returns or
// holds a hidio.Raw: every path to the emulated device goes through a
// Transport that hidio.Guarded made.
func TestNoUnguardedWritePath(t *testing.T) {
	out, err := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", emuPath).Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	files := map[string]string{}
	for sc := bufio.NewScanner(bytes.NewReader(out)); sc.Scan(); {
		if path, file, ok := strings.Cut(sc.Text(), "="); ok && file != "" {
			files[path] = file
		}
	}
	imp := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(files[path])
	})
	emu, err := imp.Import(emuPath)
	if err != nil {
		t.Fatal(err)
	}
	hidio, err := imp.Import(hidioPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := hidio.Scope().Lookup("Raw").Type().Underlying().(*types.Interface)
	isRaw := func(typ types.Type) bool {
		return types.Implements(typ, raw) || types.Implements(types.NewPointer(typ), raw)
	}
	check := func(where string, typ types.Type) {
		if isRaw(typ) {
			t.Errorf("%s is a hidio.Raw (%s)", where, typ)
		}
	}
	checkSig := func(where string, sig *types.Signature) {
		for v := range sig.Results().Variables() {
			check(where+" result", v.Type())
		}
	}
	scope := emu.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types.Func:
			checkSig(name, obj.Signature())
		case *types.Var:
			check(name, obj.Type())
		case *types.TypeName:
			check(name, obj.Type())
			if st, ok := obj.Type().Underlying().(*types.Struct); ok {
				for f := range st.Fields() {
					if f.Exported() {
						check(name+"."+f.Name(), f.Type())
					}
				}
			}
			ms := types.NewMethodSet(types.NewPointer(obj.Type()))
			for sel := range ms.Methods() {
				if m := sel.Obj(); m.Exported() {
					checkSig(name+"."+m.Name(), m.Type().(*types.Signature))
				}
			}
		}
	}
}
