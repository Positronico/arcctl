package keys_test

import (
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/vectors"
)

func loadVectors(t *testing.T) map[string][]byte {
	t.Helper()
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, v := range f.Vectors {
		b, err := v.Bytes()
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		out[v.Name] = b
	}
	return out
}

func presses(t *testing.T, name string, b []byte) keys.Combo {
	t.Helper()
	n := int(b[0])
	if n%2 != 0 || len(b) != 1+3*n+1 {
		t.Fatalf("%s: count %d does not fit %d bytes", name, n, len(b))
	}
	var c keys.Combo
	for i := range n / 2 {
		press, release := b[1+3*i:4+3*i], b[1+3*(n-1-i):4+3*(n-1-i)]
		if press[0]&0xF0 != 0x80 || release[0] != press[0]&0x0F|0x40 || !slices.Equal(press[1:], release[1:]) {
			t.Fatalf("%s: event %d is % x, its release % x", name, i, press, release)
		}
		c = append(c, st(keys.Kind(press[0]&0x0F), uint16(press[1])|uint16(press[2])<<8))
	}
	return c
}

func TestMaintainerShortcuts(t *testing.T) {
	vs := loadVectors(t)
	cases := []struct {
		vector   string
		mac, win string
		preset   string
	}{
		{"Cmd+V shortcut", "Cmd+V", "Win+V", "diy11"},
		{"Ctrl+Tab shortcut", "Ctrl+Tab", "Ctrl+Tab", ""},
		{"Cmd+Tab shortcut", "Cmd+Tab", "Win+Tab", ""},
		{"Cmd+C shortcut", "Cmd+C", "Win+C", "diy10"},
		{"Media Play/Pause", "Play/Pause", "Play/Pause", ""},
	}
	for _, c := range cases {
		b, ok := vs[c.vector]
		if !ok {
			t.Fatalf("vector %q missing", c.vector)
		}
		combo := presses(t, c.vector, b)
		if mac, win := combo.Format(keys.Mac), combo.Format(keys.Win); mac != c.mac || win != c.win {
			t.Errorf("%s: mac %q, win %q; want %q, %q", c.vector, mac, win, c.mac, c.win)
		}
		if p, ok := keys.MatchPreset(keys.Mac, combo); p.ID != c.preset || ok != (c.preset != "") {
			t.Errorf("%s: mac preset %q, %v; want %q", c.vector, p.ID, ok, c.preset)
		}
	}
}

func TestPresetVectors(t *testing.T) {
	vs := loadVectors(t)
	cases := []struct {
		vector string
		os     keys.OS
		id     string
		format string
	}{
		{"P0 win diy5 (LWin, LShift, S)", keys.Win, "diy5", "Win+Shift+S"},
		{"P0 mac diy5 (LShift, LWin, 3)", keys.Mac, "diy5", "Shift+Cmd+3"},
		{"P0 mac diy17 (LCtrl, LWin, Q)", keys.Mac, "diy17", "Ctrl+Cmd+Q"},
	}
	for _, c := range cases {
		b, ok := vs[c.vector]
		if !ok {
			t.Fatalf("vector %q missing", c.vector)
		}
		p, ok := keys.PresetByID(c.os, c.id)
		if !ok {
			t.Fatalf("%s preset %s missing", c.os, c.id)
		}
		got := presses(t, c.vector, b)
		if !slices.Equal(p.Combo(), got) {
			t.Errorf("%s: preset order %+v, vector order %+v", c.vector, p.Combo(), got)
		}
		if f := got.Format(c.os); f != c.format {
			t.Errorf("%s: %q, want %q", c.vector, f, c.format)
		}
	}
}
