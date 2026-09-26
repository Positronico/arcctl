package vectors

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func loadFile(t *testing.T) *File {
	t.Helper()
	path, err := DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestVectorsFile(t *testing.T) {
	f := loadFile(t)
	if f.Version != FileVersion {
		t.Fatalf("version %d, want %d", f.Version, FileVersion)
	}
	if f.Generator == "" {
		t.Error("empty generator")
	}
	if len(f.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	seen := map[string]bool{}
	for i, v := range f.Vectors {
		if seen[v.Name] {
			t.Errorf("vector %d: duplicate name %q", i, v.Name)
		}
		seen[v.Name] = true
		t.Run(v.Name, func(t *testing.T) {
			if err := v.Validate(); err != nil {
				t.Errorf("%s [%s, check %s]: %v", v.Hex, v.Source, v.Check, err)
			}
		})
	}
}

func TestNamedExpectations(t *testing.T) {
	byName := map[string]Vector{}
	for _, v := range loadFile(t).Vectors {
		byName[v.Name] = v
	}
	get := func(name string, n int) []byte {
		t.Helper()
		v, ok := byName[name]
		if !ok {
			t.Fatalf("vector %q missing", name)
		}
		b, err := v.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(b) != n {
			t.Fatalf("%s: %d bytes, want %d", name, len(b), n)
		}
		return b
	}

	probe, identity := get("H1 NAK probe", 16), get("CurrentDPI identity (H1)", 16)
	if Satisfies("packet", probe) == nil {
		t.Error("H1 NAK probe passes the packet rule; it must fail it")
	}
	if !bytes.Equal(probe[:15], identity[:15]) {
		t.Error("H1 NAK probe must match the identity write in bytes 0-14")
	}

	reply := get("Offline cmd-3 reply", 16)
	if reply[0] != 3 || reply[5] != 0 {
		t.Errorf("offline cmd-3 reply: cmd %d, online %d; want cmd 3, online 0", reply[0], reply[5])
	}
	addr := fmt.Sprintf("%02x %02x %02x", reply[8], reply[7], reply[6])
	if note := byName["Offline cmd-3 reply"].Note; !strings.Contains(note, "("+addr+")") {
		t.Errorf("offline cmd-3 reply: address %s (bytes 8, 7, 6) is not the one the note names: %q", addr, note)
	}

	slot := get("kb F1 slot 0x82", 4)
	kind, flag, usage := slot[0]&0x0f, slot[0]&0x80, uint16(slot[1])|uint16(slot[2])<<8
	if kind != 2 || flag == 0 || usage != 0x3a {
		t.Errorf("kb F1 slot: kind %d, flag %#02x, usage %#04x; want kind 2, flag 0x80, usage 0x3a", kind, flag, usage)
	}
}

func TestSatisfies(t *testing.T) {
	cases := []struct {
		check string
		hex   string
		ok    bool
	}{
		{"packet", "07 00 00 00 00 00 00 00 00 00 00 00 00 00 00 46", true},
		{"packet", "07 00 00 00 00 00 00 00 00 00 00 00 00 00 00 47", false},
		{"packet", "07 00 00 00 00 00 00 00 00 00 00 00 00 00 46", false},
		{"sum55", "01 54", true},
		{"sum55", "01 55", false},
		{"sum55_from:1", "ff 01 54", true},
		{"sum55_from:0", "ff 01 54", false},
		{"sum55_from:3", "ff 01 54", false},
		{"sum55_from:-1", "ff 01 54", false},
		{"sum55_from:01", "ff 01 54", false},
		{"sum55_from:", "ff 01 54", false},
		{"none", "12 34", true},
		{"crc", "12 34", false},
	}
	for _, c := range cases {
		b, err := ParseHex(c.hex)
		if err != nil {
			t.Fatalf("%q: %v", c.hex, err)
		}
		if err := Satisfies(c.check, b); (err == nil) != c.ok {
			t.Errorf("Satisfies(%q, %s) = %v, want ok=%v", c.check, c.hex, err, c.ok)
		}
	}
}

func TestParseHex(t *testing.T) {
	good := map[string][]byte{
		"00":       {0x00},
		"07 ef 55": {0x07, 0xef, 0x55},
	}
	for s, want := range good {
		got, err := ParseHex(s)
		if err != nil || string(got) != string(want) {
			t.Errorf("ParseHex(%q) = % x, %v; want % x", s, got, err, want)
		}
	}
	for _, s := range []string{"", "0", "000", "EF", "07  00", " 07", "07 ", "07\t00", "07 00…ef", "07 zz", "0x07"} {
		if _, err := ParseHex(s); err == nil {
			t.Errorf("ParseHex(%q) accepted", s)
		}
	}
}

func TestValidate(t *testing.T) {
	base := Vector{Name: "x", Group: "record", Hex: "01 54", Check: "sum55", Source: "WE,TS"}
	if err := base.Validate(); err != nil {
		t.Fatalf("base: %v", err)
	}
	bad := map[string]func(*Vector){
		"empty name":      func(v *Vector) { v.Name = " " },
		"unknown group":   func(v *Vector) { v.Group = "pair" },
		"unknown source":  func(v *Vector) { v.Source = "WE,XX" },
		"empty source":    func(v *Vector) { v.Source = "" },
		"none needs note": func(v *Vector) { v.Check = "none" },
		"bad checksum":    func(v *Vector) { v.Hex = "01 55" },
	}
	for name, mutate := range bad {
		v := base
		mutate(&v)
		if err := v.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	withNote := base
	withNote.Check, withNote.Note = "none", "keyboard CRC-32 entry"
	if err := withNote.Validate(); err != nil {
		t.Errorf("none with note: %v", err)
	}
}

func TestFileValidate(t *testing.T) {
	f, err := Parse([]byte(`{"version":1,"generator":"g","vectors":[
		{"name":"a","group":"record","hex":"01 54","check":"sum55","source":"WE"},
		{"name":"a","group":"record","hex":"01 54","check":"sum55","source":"WE"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err == nil {
		t.Error("duplicate names accepted")
	}
	f.Vectors = f.Vectors[:1]
	if err := f.Validate(); err != nil {
		t.Errorf("single vector: %v", err)
	}
	f.Version = 2
	if err := f.Validate(); err == nil {
		t.Error("version 2 accepted")
	}
}

func TestParseStrict(t *testing.T) {
	for _, s := range []string{
		`{"version":1,"generator":"g","vectors":[],"extra":1}`,
		`{"version":1,"generator":"g","vectors":[{"name":"a","chek":"none"}]}`,
		`{"version":1,"generator":"g","vectors":[]} {}`,
		`[]`,
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("Parse(%s) accepted", s)
		}
	}
}
