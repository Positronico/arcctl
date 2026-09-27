package backup_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
)

func newFile(t *testing.T, im *flash.Image, missing ...flash.Extent) *backup.File {
	t.Helper()
	c := capture(t, im)
	c.Missing = missing
	conn := byte(0)
	f, err := backup.New(c, backup.Meta{Tool: "arcctl test", Label: "before H1", Created: created, Conn: &conn, OS: keys.Mac})
	must(t, err)
	return f
}

func encode(t *testing.T, f *backup.File) []byte {
	t.Helper()
	var buf bytes.Buffer
	must(t, f.Encode(&buf))
	return buf.Bytes()
}

func TestRoundTrip(t *testing.T) {
	im := richImage(t)
	missing := flash.Extent{Addr: 9504, Len: 20}
	f := newFile(t, im, missing, flash.Extent{Addr: 250, Len: 10})
	g, err := backup.Decode(encode(t, f))
	must(t, err)

	if !bytes.Equal(encode(t, g), encode(t, f)) {
		t.Error("a decoded backup encodes differently")
	}
	got := g.Image()
	if a, b := got.KnownExtents(), im.KnownExtents(); !equalExtents(a, b) {
		t.Fatalf("known extents %v, want %v", a, b)
	}
	if !bytes.Equal(got.Bytes(), im.Bytes()) {
		t.Error("image bytes differ")
	}
	if m, want := g.Missing(), []flash.Extent{{Addr: 256, Len: 4}, missing}; !equalExtents(m, want) {
		t.Errorf("missing %v, want %v: bytes 250..255 are known", m, want)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"format", g.Format, backup.Format},
		{"source", g.Source, backup.SourceDevice},
		{"key", g.Key(), "260d-1282-7b04"},
		{"stored key", g.Device.Key, "260d-1282-7b04"},
		{"address", g.Device.Addr, "11:22:33"},
		{"identity address", g.Identity().Addr, [3]byte{0x11, 0x22, 0x33}},
		{"trusted", g.Device.AddrTrusted, false},
		{"mouse firmware", g.Device.FWMouse, "v1.05"},
		{"receiver firmware", g.Device.FWReceiver, "v1.02"},
		{"profile", *g.Device.Profile, backup.Profile{Supported: true, Value: 0}},
		{"conn", *g.Device.Conn, byte(0)},
		{"model", *g.Model, backup.Model{Key: "7B04", Name: "ProtoArc EM11 Pro", Sensor: "3104"}},
		{"created", g.Created, created},
		{"label", g.Label, "before H1"},
		{"known", g.Known(), known(im)},
		{"summary rate", *g.Summary.Rate, 250},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if m, ok := g.CatalogModel(); !ok || m.Key != "7B04" {
		t.Errorf("CatalogModel = %v, %v", m, ok)
	}
}

func TestJSONLayout(t *testing.T) {
	text := string(encode(t, newFile(t, dumpImage(t))))
	for _, want := range []string{
		`"format": "arcctl-backup/1"`, `"vid": "260D"`, `"pid": "1282"`, `"cid": "7B"`, `"mid": 4`,
		`"created": "2026-09-26T12:00:00Z"`, `"status": "ok"`, `"hex": "0451064f`, `"sha256": "`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("backup JSON lacks %s", want)
		}
	}
}

func TestUnaskedProfileIsNull(t *testing.T) {
	c := capture(t, dumpImage(t))
	c.Profile.Asked = false
	f, err := backup.New(c, backup.Meta{Created: created})
	must(t, err)
	if f.Device.Profile != nil || !strings.Contains(string(encode(t, f)), `"profile": null`) {
		t.Errorf("profile %+v", f.Device.Profile)
	}
}

func TestDecodeRejects(t *testing.T) {
	good := string(encode(t, newFile(t, dumpImage(t), flash.Extent{Addr: 6912, Len: 10})))
	tests := []struct {
		name string
		edit func(string) string
		want error
	}{
		{"not JSON", func(string) string { return "nope" }, backup.ErrFormat},
		{"other format", func(s string) string { return strings.Replace(s, "arcctl-backup/1", "arcctl-backup/9", 1) }, backup.ErrFormat},
		{"no format", func(s string) string { return strings.Replace(s, `"format"`, `"formats"`, 1) }, backup.ErrFormat},
		{"changed byte", func(s string) string { return strings.Replace(s, `"hex": "0451`, `"hex": "0551`, 1) }, backup.ErrChecksum},
		{"changed checksum", func(s string) string { return strings.Replace(s, `"sha256": "`, `"sha256": "00`, 1) }, backup.ErrChecksum},
		{"unknown field", func(s string) string { return strings.Replace(s, `"full"`, `"extra": 1, "full"`, 1) }, backup.ErrInvalid},
		{"short hex", func(s string) string { return strings.Replace(s, `"len": 256`, `"len": 257`, 1) }, backup.ErrInvalid},
		{"key", func(s string) string { return strings.Replace(s, `"key": "260d-1282-7b04"`, `"key": "7b04-112233"`, 1) }, backup.ErrInvalid},
		{"address", func(s string) string { return strings.Replace(s, `"11:22:33"`, `"11:22"`, 1) }, backup.ErrInvalid},
		{"vid", func(s string) string { return strings.Replace(s, `"260D"`, `"260"`, 1) }, backup.ErrInvalid},
		{"source", func(s string) string { return strings.Replace(s, `"source": "device"`, `"source": "guess"`, 1) }, backup.ErrInvalid},
		{"missing with bytes", func(s string) string {
			return strings.Replace(s, `"status": "missing"`, `"status": "missing", "hex": "00"`, 1)
		}, backup.ErrInvalid},
		{"overlap", func(s string) string { return strings.Replace(s, `"addr": 6912`, `"addr": 200`, 1) }, backup.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := tt.edit(good)
			if text == good {
				t.Fatal("the edit changed nothing")
			}
			if _, err := backup.Decode([]byte(text)); !errors.Is(err, tt.want) {
				t.Errorf("Decode = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewNeedsImage(t *testing.T) {
	c := capture(t, nil)
	if _, err := backup.New(c, backup.Meta{}); !errors.Is(err, backup.ErrNoImage) {
		t.Errorf("New without image = %v", err)
	}
}

func TestSources(t *testing.T) {
	f := newFile(t, dumpImage(t))
	for _, src := range []string{backup.SourceEmulator, backup.SourceReplay} {
		c := capture(t, dumpImage(t))
		g, err := backup.New(c, backup.Meta{Source: src, Created: created})
		must(t, err)
		if h, err := backup.Decode(encode(t, g)); err != nil || h.Source != src {
			t.Errorf("source %s: %v, %v", src, h, err)
		}
	}
	if f.Source != backup.SourceDevice {
		t.Errorf("default source %q", f.Source)
	}
}

func equalExtents(a, b []flash.Extent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func known(im *flash.Image) int {
	n := 0
	for _, e := range im.KnownExtents() {
		n += e.Len
	}
	return n
}
