package hidio_test

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const hidioPath = "github.com/positronico/arcctl/internal/hidio"

// exportImporter type-checks against the compiled packages, found with go list.
func exportImporter(t *testing.T, fset *token.FileSet) types.Importer {
	t.Helper()
	out, err := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", hidioPath).Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	files := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if path, file, ok := strings.Cut(sc.Text(), "="); ok && file != "" {
			files[path] = file
		}
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(files[path])
	})
}

// TestOnlyGuardedMakesTransports checks at compile time that a Raw, a Pipe or a
// look-alike type from another package cannot be used as a Transport, while
// the result of Guarded can.
func TestOnlyGuardedMakesTransports(t *testing.T) {
	const src = `package p

import (
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

type lookalike struct{}

func (lookalike) Write(wire.Packet) error        { return nil }
func (lookalike) Reports() <-chan hidio.Report  { return nil }
func (lookalike) Wake() <-chan struct{}         { return nil }
func (lookalike) Err() error                    { return nil }
func (lookalike) Dropped() uint64               { return 0 }
func (lookalike) Close() error                  { return nil }
func (lookalike) guarded()                      {}

var raw hidio.Raw = hidio.NewPipe(nil)

var _ hidio.Transport = raw                // want error
var _ hidio.Transport = hidio.NewPipe(nil) // want error
var _ hidio.Transport = &hidio.Replay{}    // want error
var _ hidio.Transport = lookalike{}        // want error
var _ hidio.Transport = hidio.Guarded(raw, hidio.NewGuard(wire.Mouse))
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{}
	for _, cg := range file.Comments {
		if strings.Contains(cg.Text(), "want error") {
			want[fset.Position(cg.Pos()).Line] = true
		}
	}
	got := map[int]string{}
	conf := types.Config{
		Importer: exportImporter(t, fset),
		Error: func(err error) {
			te := err.(types.Error)
			got[fset.Position(te.Pos).Line] = te.Msg
		},
	}
	conf.Check("p", fset, []*ast.File{file}, nil)
	for line := range want {
		msg, ok := got[line]
		if !ok || !strings.Contains(msg, "does not implement") || !strings.Contains(msg, "Transport") {
			t.Errorf("line %d: got %q, want a Transport implementation error", line, msg)
		}
	}
	for line, msg := range got {
		if !want[line] {
			t.Errorf("line %d: unexpected error %q", line, msg)
		}
	}
}
