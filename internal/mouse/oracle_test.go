package mouse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

type oracleFile struct {
	Version   int          `json:"version"`
	Generator string       `json:"generator"`
	Seed      int          `json:"seed"`
	Area      string       `json:"area"`
	Cases     []oracleCase `json:"cases"`
}

type oracleCase struct {
	In      json.RawMessage `json:"in"`
	Writes  []oracleWrite   `json:"writes"`
	Packets []string        `json:"packets"`
	Out     json.RawMessage `json:"out"`
}

type oracleWrite struct {
	Addr int    `json:"addr"`
	Hex  string `json:"hex"`
}

type stroke struct {
	Kind  keys.Kind `json:"kind"`
	Value uint16    `json:"value"`
}

type macroEvent struct {
	Press bool      `json:"press"`
	Kind  keys.Kind `json:"kind"`
	Value uint16    `json:"value"`
	Delay uint16    `json:"delay"`
}

type decodeIn struct {
	Slot int    `json:"slot"`
	Hex  string `json:"hex"`
}

func combo(ss []stroke) keys.Combo {
	c := make(keys.Combo, len(ss))
	for i, s := range ss {
		c[i] = keys.Stroke{Kind: s.Kind, Value: s.Value}
	}
	return c
}

func events(es []macroEvent) []mouse.Event {
	out := make([]mouse.Event, len(es))
	for i, e := range es {
		out[i] = mouse.Event{Press: e.Press, Stroke: keys.Stroke{Kind: e.Kind, Value: e.Value}, Delay: e.Delay}
	}
	return out
}

var oracleAreas = map[string]func(t *testing.T, c oracleCase){
	"report_rate": func(t *testing.T, c oracleCase) {
		var in struct {
			Hz int `json:"hz"`
		}
		mustIn(t, c, &in)
		p, err := mouse.EncodeRate(in.Hz)
		checkWrite(t, c, flash.Extent{Addr: mouse.AddrReportRate, Len: 2}, p[:], err)
		if hz, err := mouse.DecodeRate(p); err != nil || hz != in.Hz {
			t.Errorf("DecodeRate(% x) = %d, %v; want %d", p, hz, err, in.Hz)
		}
	},
	"dpi_value": func(t *testing.T, c oracleCase) {
		var in struct {
			Sensor string `json:"sensor"`
			Stage  int    `json:"stage"`
			DPI    int    `json:"dpi"`
		}
		mustIn(t, c, &in)
		s := sensor(t, in.Sensor)
		r, err := mouse.EncodeDPI(s, in.DPI)
		checkWrite(t, c, at(t, mouse.DPIExtent, in.Stage), r, err)
		if d, err := mouse.DecodeDPI(s, r); err != nil || d != (mouse.DPI{X: in.DPI, Y: in.DPI}) {
			t.Errorf("DecodeDPI(%s, % x) = %+v, %v; want %d", in.Sensor, r, d, err, in.DPI)
		}
	},
	"key_function": func(t *testing.T, c oracleCase) {
		var in struct {
			Slot   int           `json:"slot"`
			Type   mouse.KeyType `json:"type"`
			Param  *uint16       `json:"param"`
			Sensor string        `json:"sensor"`
			DPI    int           `json:"dpi"`
		}
		mustIn(t, c, &in)
		k := mouse.KeyFn{Type: in.Type}
		switch {
		case in.Type == mouse.TypeDPILock:
			var err error
			if k, err = mouse.DPILock(sensor(t, in.Sensor), in.DPI); err != nil {
				t.Fatal(err)
			}
			if d, ok := k.LockedDPI(sensor(t, in.Sensor)); !ok || d != in.DPI {
				t.Errorf("LockedDPI = %d, %v; want %d", d, ok, in.DPI)
			}
		case in.Param == nil:
			t.Fatal("case has no param")
		default:
			k.Param = *in.Param
		}
		if in.Type == mouse.TypeFire {
			f, err := mouse.FireKey(int(k.Param>>8), int(k.Param&0xFF))
			if err != nil || f != k {
				t.Errorf("FireKey from %#04x = %+v, %v", k.Param, f, err)
			}
		}
		r, err := mouse.EncodeKeyFn(k)
		checkWrite(t, c, at(t, mouse.KeyFnExtent, in.Slot), r, err)
		if got, err := mouse.DecodeKeyFn(r); err != nil || got != k {
			t.Errorf("DecodeKeyFn(% x) = %+v, %v; want %+v", r, got, err, k)
		}
	},
	"macro_binding": func(t *testing.T, c oracleCase) {
		var in struct {
			Slot  int `json:"slot"`
			Cycle int `json:"cycle"`
		}
		mustIn(t, c, &in)
		k, err := mouse.MacroBinding(in.Slot, in.Cycle)
		if err != nil {
			t.Fatal(err)
		}
		r, err := mouse.EncodeKeyFn(k)
		checkWrite(t, c, at(t, mouse.KeyFnExtent, in.Slot), r, err)
		if got, err := mouse.DecodeKeyFn(r); err != nil || got != k {
			t.Errorf("DecodeKeyFn(% x) = %+v, %v; want %+v", r, got, err, k)
		}
	},
	"shortcut": func(t *testing.T, c oracleCase) {
		var in struct {
			Slot   int      `json:"slot"`
			Preset string   `json:"preset"`
			Keys   []string `json:"keys"`
			Events []stroke `json:"events"`
		}
		mustIn(t, c, &in)
		want := combo(in.Events)
		if len(in.Keys) != len(want) {
			t.Fatalf("%d key names for %d events", len(in.Keys), len(want))
		}
		for i, code := range in.Keys {
			if k, ok := keys.ByCode(code); !ok || k.Stroke() != want[i] {
				t.Errorf("key %q = %+v, %v; want %+v", code, k.Stroke(), ok, want[i])
			}
		}
		if in.Preset != "" {
			os, id, _ := strings.Cut(in.Preset, "/")
			p, ok := keys.PresetByID(map[string]keys.OS{"win": keys.Win, "mac": keys.Mac}[os], id)
			if !ok || !slices.Equal(p.Combo(), want) {
				t.Errorf("preset %s = %+v, %v; want %+v", in.Preset, p.Combo(), ok, want)
			}
		}
		r, err := mouse.EncodeShortcut(want)
		checkWrite(t, c, at(t, mouse.ShortcutExtent, in.Slot), r, err)
		if got, err := mouse.DecodeShortcut(r); err != nil || !slices.Equal(got, want) {
			t.Errorf("DecodeShortcut(% x) = %+v, %v", r, got, err)
		}
	},
	"media": func(t *testing.T, c oracleCase) {
		var in struct {
			Slot int    `json:"slot"`
			Code uint16 `json:"code"`
		}
		mustIn(t, c, &in)
		r, err := mouse.EncodeMedia(in.Code)
		checkWrite(t, c, at(t, mouse.ShortcutExtent, in.Slot), r, err)
		want := keys.Combo{{Kind: keys.KindConsumer, Value: in.Code}}
		if got, err := mouse.DecodeShortcut(r); err != nil || !slices.Equal(got, want) {
			t.Errorf("DecodeShortcut(% x) = %+v, %v", r, got, err)
		}
	},
	"macro": func(t *testing.T, c oracleCase) {
		var in struct {
			Slot   int          `json:"slot"`
			Name   string       `json:"name"`
			Events []macroEvent `json:"events"`
		}
		mustIn(t, c, &in)
		m := mouse.Macro{Name: in.Name, Events: events(in.Events)}
		b, err := mouse.EncodeMacro(m)
		checkWrite(t, c, at(t, mouse.MacroExtent, in.Slot), b, err)
		if got, err := mouse.DecodeMacro(b); err != nil || got.Name != m.Name || !slices.Equal(got.Events, m.Events) {
			t.Errorf("DecodeMacro round trip = %+v, %v", got, err)
		}
	},
	"shortcut_decode": func(t *testing.T, c oracleCase) {
		var in decodeIn
		mustIn(t, c, &in)
		var out struct {
			Media  bool     `json:"media"`
			Events []stroke `json:"events"`
		}
		if err := strict(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		raw := unhex(t, in.Hex)
		got, err := mouse.DecodeShortcut(pad(raw, mouse.ShortcutSize))
		if err != nil {
			t.Fatalf("DecodeShortcut(%s): %v", in.Hex, err)
		}
		media := len(got) == 1 && got[0].Kind == keys.KindConsumer
		if want := combo(out.Events); !slices.Equal(got, want) || media != out.Media {
			t.Errorf("DecodeShortcut(%s) = %+v media %v; want %+v media %v", in.Hex, got, media, want, out.Media)
		}
		if r, err := mouse.EncodeShortcut(got); err != nil || !bytes.Equal(r, raw) {
			t.Errorf("re-encode = % x, %v; want %s", r, err, in.Hex)
		}
	},
	"macro_decode": func(t *testing.T, c oracleCase) {
		var in decodeIn
		mustIn(t, c, &in)
		raw := unhex(t, in.Hex)
		got, err := mouse.DecodeMacro(pad(raw, mouse.MacroSize))
		if string(c.Out) == "null" {
			if !errors.Is(err, mouse.ErrEmpty) || mouse.Class(err) != flash.SlotEmpty {
				t.Errorf("DecodeMacro(slot %d) = %+v, %v; want ErrEmpty", in.Slot, got, err)
			}
			return
		}
		var out struct {
			Name   string       `json:"name"`
			Events []macroEvent `json:"events"`
		}
		if err := strict(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		if err != nil || got.Name != out.Name || !slices.Equal(got.Events, events(out.Events)) {
			t.Fatalf("DecodeMacro(slot %d) = %+v, %v; want %+v", in.Slot, got, err, out)
		}
		if b, err := mouse.EncodeMacro(got); err != nil || !bytes.Equal(b, raw) {
			t.Errorf("re-encode differs: %v", err)
		}
	},
}

func mustIn(t *testing.T, c oracleCase, v any) {
	t.Helper()
	if err := strict(c.In, v); err != nil {
		t.Fatalf("in %s: %v", c.In, err)
	}
}

func checkWrite(t *testing.T, c oracleCase, slot flash.Extent, data []byte, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("in %s: %v", c.In, err)
	}
	if len(c.Writes) != 1 || c.Out != nil {
		t.Fatalf("encoder case needs exactly one write and no out")
	}
	w := c.Writes[0]
	if want := unhex(t, w.Hex); w.Addr != slot.Addr || !bytes.Equal(data, want) {
		t.Errorf("in %s: write %d % x; oracle %d %s", c.In, slot.Addr, data, w.Addr, w.Hex)
	}
	if !slot.Contains(flash.Extent{Addr: w.Addr, Len: len(data)}) {
		t.Errorf("write %d+%d leaves slot %v", w.Addr, len(data), slot)
	}
	if c.Packets != nil && !slices.Equal(packets(t, slot.Addr, data), c.Packets) {
		t.Errorf("in %s: packets %q; oracle %q", c.In, packets(t, slot.Addr, data), c.Packets)
	}
}

func TestOracle(t *testing.T) {
	files, err := filepath.Glob(testdata(t, "oracle", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, path := range files {
		var f oracleFile
		if err := strict(readFile(t, path), &f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		check, ok := oracleAreas[f.Area]
		switch {
		case !ok:
			t.Fatalf("%s: no Go check for area %q", path, f.Area)
		case f.Version != 1 || f.Area+".json" != filepath.Base(path) || len(f.Cases) == 0:
			t.Fatalf("%s: version %d, area %q, %d cases", path, f.Version, f.Area, len(f.Cases))
		}
		seen[f.Area] = true
		t.Run(f.Area, func(t *testing.T) {
			for i, c := range f.Cases {
				t.Run(strconv.Itoa(i), func(t *testing.T) { check(t, c) })
			}
		})
	}
	for area := range oracleAreas {
		if !seen[area] {
			t.Errorf("no oracle file for area %q", area)
		}
	}
}
