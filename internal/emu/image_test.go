package emu_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
)

func TestDefaultsDecode(t *testing.T) {
	for _, m := range catalog.Models() {
		if m.Family != catalog.FamilyMouse {
			if _, err := emu.Defaults(m); !errors.Is(err, emu.ErrNoDefaults) {
				t.Errorf("%s: Defaults = %v, want ErrNoDefaults", m.Key, err)
			}
			continue
		}
		t.Run(m.Key, func(t *testing.T) {
			im, err := emu.Defaults(m)
			if err != nil {
				t.Fatal(err)
			}
			df := m.Defaults
			cfg := mouse.Decode(m, im)
			if cfg.Rate.Value != df.ReportRate || cfg.Stages.Value != len(df.DPIs) || cfg.Current.Value != df.CurrentDPI {
				t.Errorf("rate %d, stages %d, current %d", cfg.Rate.Value, cfg.Stages.Value, cfg.Current.Value)
			}
			for i, s := range df.DPIs {
				if got := cfg.DPI[i]; got.DPI.X != s.DPI || got.DPI.Y != s.DPI || got.Color != s.Color {
					t.Errorf("stage %d = %+v, want %d %v", i, got.DPI, s.DPI, s.Color)
				}
			}
			for _, bt := range m.Buttons {
				k := cfg.Keys[bt.Slot]
				if k.Field.State != flash.OK {
					t.Errorf("slot %d: %v", bt.Slot, k.Field.State)
				}
				if bt.Media != 0 {
					c := cfg.Shortcuts[bt.Slot]
					if k.Fn.Type != mouse.TypeShortcut || len(c) != 1 || c[0].Value != bt.Media {
						t.Errorf("slot %d: %+v with body %v, want media %#x", bt.Slot, k.Fn, c, bt.Media)
					}
				} else if k.Fn != (mouse.KeyFn{Type: mouse.KeyType(bt.Type), Param: bt.Param}) {
					t.Errorf("slot %d = %+v, want type %d param %#x", bt.Slot, k.Fn, bt.Type, bt.Param)
				}
			}
		})
	}
	if _, err := emu.Defaults(nil); !errors.Is(err, emu.ErrNoDefaults) {
		t.Errorf("Defaults(nil) = %v", err)
	}
}

func TestWithBodies(t *testing.T) {
	dump := testdata(t, "flash-dump.bin")
	src, err := flash.FromDump(0, dump)
	if err != nil {
		t.Fatal(err)
	}
	macro, err := mouse.MacroBinding(9, 1)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := mouse.EncodeKeyFn(macro)
	if err := src.Set(96+4*12, rec); err != nil {
		t.Fatal(err)
	}
	im, err := emu.WithBodies(src)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := im.Get(flash.Extent{Addr: 0, Len: 256}); !bytes.Equal(got[:96+48], dump[:96+48]) {
		t.Fatal("the settings page changed")
	}
	cfg := mouse.Decode(model(t, "7B04"), im)
	for slot := range mouse.Slots {
		want := flash.SlotUnknown
		if slot >= 2 && slot <= 5 {
			want = flash.SlotValid
		}
		if cfg.ShortcutClass[slot] != want {
			t.Errorf("shortcut slot %d: %v, want %v", slot, cfg.ShortcutClass[slot], want)
		}
	}
	for _, slot := range []int{9, 12} {
		if cfg.MacroClass[slot] != flash.SlotValid {
			t.Errorf("macro slot %d: %v, want valid", slot, cfg.MacroClass[slot])
		}
	}
	again, err := emu.WithBodies(im)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Bytes(), im.Bytes()) {
		t.Fatal("WithBodies is not idempotent")
	}
}

func TestMouseStartsFromModelDefaults(t *testing.T) {
	m := model(t, "7B05")
	b := newBus(t, emu.Options{})
	d := add(t, b, emu.Config{Mouse: &emu.Mouse{Model: m}})
	tr := open(t, b, d, 1)
	want, err := emu.Defaults(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Image().Bytes(), want.Bytes()) {
		t.Fatal("a mouse with no image does not start from its model's defaults")
	}
	rep := transact(t, tr, read(t, mouse.AddrMaxDpiStage, 2))
	if rep[5] != byte(len(m.Defaults.DPIs)) {
		t.Fatalf("stage count read %v", rep)
	}
}
