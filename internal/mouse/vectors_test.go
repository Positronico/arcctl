package mouse_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/vectors"
)

var (
	lMeta = keys.LMeta.Stroke()
	lCtrl = keys.LCtrl.Stroke()
	keyC  = keys.Stroke{Kind: keys.KindKey, Value: 0x06}
	keyV  = keys.Stroke{Kind: keys.KindKey, Value: 0x19}
	tab   = keys.Stroke{Kind: keys.KindKey, Value: 0x2B}
	keyA  = keys.Stroke{Kind: keys.KindKey, Value: 0x04}
)

func preset(t *testing.T, os keys.OS, id string) keys.Combo {
	t.Helper()
	p, ok := keys.PresetByID(os, id)
	if !ok {
		t.Fatalf("preset %v/%s missing", os, id)
	}
	return p.Combo()
}

func recordVectors(t *testing.T) map[string]func() ([]byte, error) {
	shortcut := func(c keys.Combo) func() ([]byte, error) {
		return func() ([]byte, error) { return mouse.EncodeShortcut(c) }
	}
	keyFn := func(k mouse.KeyFn) func() ([]byte, error) {
		return func() ([]byte, error) { return mouse.EncodeKeyFn(k) }
	}
	return map[string]func() ([]byte, error){
		"Cmd+C shortcut":                shortcut(keys.Combo{lMeta, keyC}),
		"Cmd+V shortcut":                shortcut(keys.Combo{lMeta, keyV}),
		"Ctrl+Tab shortcut":             shortcut(keys.Combo{lCtrl, tab}),
		"Cmd+Tab shortcut":              shortcut(keys.Combo{lMeta, tab}),
		"P0 win diy5 (LWin, LShift, S)": shortcut(preset(t, keys.Win, "diy5")),
		"P0 mac diy5 (LShift, LWin, 3)": shortcut(preset(t, keys.Mac, "diy5")),
		"P0 mac diy17 (LCtrl, LWin, Q)": shortcut(preset(t, keys.Mac, "diy17")),
		"Media Play/Pause":              func() ([]byte, error) { return mouse.EncodeMedia(0x00CD) },
		"Macro binding":                 func() ([]byte, error) { k, _ := mouse.MacroBinding(4, 1); return mouse.EncodeKeyFn(k) },
		"Drag Scroll":                   keyFn(mouse.KeyFn{Type: mouse.TypeDragScroll, Param: mouse.ParamDragScroll}),
		"Rate Switch":                   keyFn(mouse.KeyFn{Type: mouse.TypeRateSwitch}),
		"Disable":                       keyFn(mouse.KeyFn{}),
		`Macro "ab"`: func() ([]byte, error) {
			return mouse.EncodeMacro(mouse.Macro{Name: "ab", Events: []mouse.Event{
				{Press: true, Stroke: keyA, Delay: 50},
				{Stroke: keyA, Delay: 10},
			}})
		},
	}
}

type write struct {
	addr int
	data []byte
}

func writeVectors(t *testing.T) map[string]write {
	b := func(r []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	p := func(p flash.Pair, err error) []byte { return b(p[:], err) }
	s := sensor(t, "3104")
	cmdC := b(mouse.EncodeShortcut(keys.Combo{lMeta, keyC}))
	return map[string]write{
		"lt(0,4)":                  {mouse.AddrReportRate, p(mouse.EncodeRate(250))},
		"ND(6)":                    {mouse.AddrMaxDpiStage, p(mouse.EncodeStageCount(6))},
		"FD(2)":                    {mouse.AddrCurrentDPI, p(mouse.EncodeCurrentStage(2))},
		"CurrentDPI identity (H1)": {mouse.AddrCurrentDPI, p(mouse.EncodeCurrentStage(3))},
		"DPI 800 at stage 0":       {at(t, mouse.DPIExtent, 0).Addr, b(mouse.EncodeDPI(s, 800))},
		"DPI 4800 at stage 5":      {at(t, mouse.DPIExtent, 5).Addr, b(mouse.EncodeDPI(s, 4800))},
		"Red, colour stage 0":      {at(t, mouse.ColorExtent, 0).Addr, mouse.EncodeColor([3]byte{0xFF, 0, 0})},
		"btn0 Left":                {at(t, mouse.KeyFnExtent, 0).Addr, b(mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamLeft}))},
		"btn5 DPI cycle":           {at(t, mouse.KeyFnExtent, 5).Addr, b(mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle}))},
		"btn5 Cmd+C write/1":       {at(t, mouse.ShortcutExtent, 5).Addr, cmdC[:10]},
		"btn5 Cmd+C write/2":       {at(t, mouse.ShortcutExtent, 5).Addr + 10, cmdC[10:]},
	}
}

func loadVectors(t *testing.T) []vectors.Vector {
	t.Helper()
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	return f.Vectors
}

func TestRecordVectors(t *testing.T) {
	encoders := recordVectors(t)
	seen := 0
	for _, v := range loadVectors(t) {
		if v.Group != "record" {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			want := unhex(t, v.Hex)
			encode, ok := encoders[v.Name]
			if f := strings.Fields(v.Name); !ok && len(f) == 3 && f[1] == "DPI" {
				n, err := strconv.Atoi(f[2])
				if err != nil {
					t.Fatal(err)
				}
				s := sensor(t, f[0])
				encode, ok = func() ([]byte, error) { return mouse.EncodeDPI(s, n) }, true
				if d, err := mouse.DecodeDPI(s, want); err != nil || d != (mouse.DPI{X: n, Y: n}) {
					t.Errorf("DecodeDPI = %+v, %v", d, err)
				}
			}
			if !ok {
				t.Fatalf("no encoder for record vector %q", v.Name)
			}
			seen++
			got, err := encode()
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("encode = % x, %v; want %s", got, err, v.Hex)
			}
		})
	}
	if seen == 0 {
		t.Fatal("no record vectors")
	}
}

func TestWritePacketVectors(t *testing.T) {
	writes := writeVectors(t)
	found := map[string]bool{}
	for _, v := range loadVectors(t) {
		w, ok := writes[v.Name]
		if !ok {
			continue
		}
		found[v.Name] = true
		if got := packets(t, w.addr, w.data); len(got) != 1 || got[0] != v.Hex {
			t.Errorf("%s: packets %q; want %s", v.Name, got, v.Hex)
		}
	}
	for name := range writes {
		if !found[name] {
			t.Errorf("vector %q not in vectors.json", name)
		}
	}
}
