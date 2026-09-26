package mouse_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

func testdata(t testing.TB, name ...string) string {
	t.Helper()
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{root, "testdata"}, name...)...)
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func strict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := vectors.ParseHex(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

func sensor(t testing.TB, id string) *catalog.Sensor {
	t.Helper()
	s, ok := catalog.SensorByID(id)
	if !ok {
		t.Fatalf("sensor %s not in the catalog", id)
	}
	return s
}

func at(t testing.TB, table func(int) (flash.Extent, bool), i int) flash.Extent {
	t.Helper()
	e, ok := table(i)
	if !ok {
		t.Fatalf("index %d outside its table", i)
	}
	return e
}

func packets(t testing.TB, addr int, data []byte) []string {
	t.Helper()
	var out []string
	for off := 0; off < len(data); off += wire.MaxData {
		chunk := data[off:min(off+wire.MaxData, len(data))]
		p, err := wire.Build(wire.Mouse, wire.CmdWrite, uint16(addr+off), chunk)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.String())
	}
	return out
}

func pad(b []byte, n int) []byte {
	out := bytes.Repeat([]byte{0xFF}, max(n, len(b)))
	copy(out, b)
	return out
}

func checksum(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return s
}
