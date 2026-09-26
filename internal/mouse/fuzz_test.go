package mouse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
)

func knownErr(err error) bool {
	for _, e := range []error{mouse.ErrEmpty, mouse.ErrTruncated, mouse.ErrInvalid} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func seedOracle(f *testing.F, area string) {
	var file oracleFile
	if err := strict(readFile(f, testdata(f, "oracle", area+".json")), &file); err != nil {
		f.Fatal(err)
	}
	for _, c := range file.Cases {
		var in decodeIn
		if err := strict(c.In, &in); err == nil {
			f.Add(unhex(f, in.Hex))
		}
		for _, w := range c.Writes {
			f.Add(unhex(f, w.Hex))
		}
	}
}

func FuzzDecodeShortcut(f *testing.F) {
	seedOracle(f, "shortcut_decode")
	seedOracle(f, "media")
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xFF}, mouse.ShortcutSize))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := mouse.DecodeShortcut(b)
		if err != nil {
			if !knownErr(err) || c != nil || mouse.Class(err) == flash.SlotValid {
				t.Fatalf("DecodeShortcut(% x) = %+v, %v", b, c, err)
			}
			return
		}
		r, err := mouse.EncodeShortcut(c)
		if err != nil || !bytes.HasPrefix(b, r) || !r.Verify() {
			t.Fatalf("DecodeShortcut(% x) = %+v re-encodes to % x, %v", b, c, r, err)
		}
	})
}

func FuzzDecodeMacro(f *testing.F) {
	seedOracle(f, "macro_decode")
	seedOracle(f, "macro")
	f.Add([]byte{})
	f.Add(make([]byte, mouse.MacroSize))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := mouse.DecodeMacro(b)
		if err != nil {
			if !knownErr(err) || m.Name != "" || m.Events != nil || mouse.Class(err) == flash.SlotValid {
				t.Fatalf("DecodeMacro(% x) = %+v, %v", b, m, err)
			}
			return
		}
		r, err := mouse.EncodeMacro(m)
		if err != nil || !bytes.HasPrefix(b, r) || len(r) > mouse.MacroSize {
			t.Fatalf("DecodeMacro(% x) = %+v re-encodes to % x, %v", b, m, r, err)
		}
	})
}

func FuzzDecodeKeyFn(f *testing.F) {
	seedOracle(f, "key_function")
	seedOracle(f, "macro_binding")
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		k, err := mouse.DecodeKeyFn(b)
		if err != nil {
			if !knownErr(err) || k != (mouse.KeyFn{}) {
				t.Fatalf("DecodeKeyFn(% x) = %+v, %v", b, k, err)
			}
			return
		}
		r, err := mouse.EncodeKeyFn(k)
		if err != nil || !bytes.Equal(r, b[:4]) || k.Check() != nil {
			t.Fatalf("DecodeKeyFn(% x) = %+v re-encodes to % x, %v", b, k, r, err)
		}
	})
}

func FuzzDecodeDPI(f *testing.F) {
	sensors := catalog.Sensors()
	var file oracleFile
	if err := strict(readFile(f, testdata(f, "oracle", "dpi_value.json")), &file); err != nil {
		f.Fatal(err)
	}
	for _, c := range file.Cases {
		var in struct {
			Sensor string `json:"sensor"`
		}
		if err := json.Unmarshal(c.In, &in); err != nil {
			f.Fatal(err)
		}
		i := slices.IndexFunc(sensors, func(s *catalog.Sensor) bool { return s.ID == in.Sensor })
		if i < 0 || len(c.Writes) != 1 {
			f.Fatalf("case %s", c.In)
		}
		f.Add(byte(i), unhex(f, c.Writes[0].Hex))
	}
	f.Fuzz(func(t *testing.T, sensor byte, b []byte) {
		s := sensors[int(sensor)%len(sensors)]
		d, err := mouse.DecodeDPI(s, b)
		if err != nil {
			if !knownErr(err) || d != (mouse.DPI{}) {
				t.Fatalf("DecodeDPI(%s, % x) = %+v, %v", s.ID, b, d, err)
			}
			return
		}
		for _, v := range []int{d.X, d.Y} {
			if _, err := mouse.EncodeDPI(s, v); err != nil {
				t.Fatalf("DecodeDPI(%s, % x) = %+v, but %d does not encode: %v", s.ID, b, d, v, err)
			}
		}
	})
}
