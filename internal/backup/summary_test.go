package backup_test

import (
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
)

func TestSummaryOfDump(t *testing.T) {
	s := backup.Summarize(em11(t), dumpImage(t), keys.Mac)
	if *s.Rate != 250 || *s.Stages != 6 || *s.Current != 4 {
		t.Errorf("rate %d, stages %d, current %d", *s.Rate, *s.Stages, *s.Current)
	}
	wantDPI := [][2]int{{800, 800}, {1200, 1200}, {1600, 1600}, {2400, 2400}, {3200, 3200}, {4800, 2400}, {4000, 4000}, {4000, 4000}}
	for i, d := range s.DPI {
		if [2]int{d.X, d.Y} != wantDPI[i] || d.Active != (i < 6) || d.Current != (i == 3) {
			t.Errorf("stage %d = %+v", i+1, d)
		}
	}
	if s.DPI[0].Color != "#ff0000" {
		t.Errorf("stage 1 colour %s", s.DPI[0].Color)
	}
	actions := []string{
		"Left Click", "Right Click", "shortcut (body not read)", "shortcut (body not read)",
		"shortcut (body not read)", "shortcut (body not read)", "Profile switch", "DPI Cycle",
		"Scroll Left", "Scroll Right", "DPI+", "DPI-", "Do nothing", "Do nothing", "Do nothing", "Do nothing",
	}
	for i, b := range s.Buttons {
		if b.Action != actions[i] {
			t.Errorf("slot %d = %q, want %q", i, b.Action, actions[i])
		}
	}
	if b := s.Buttons[3]; b.Button != "Backward" || b.Hidden {
		t.Errorf("slot 3 button %+v", b)
	}
	if len(s.Invalid) != 1 || s.Invalid[0].Addr != 185 || s.Invalid[0].Raw != "03 53" {
		t.Errorf("invalid %+v, want only 185", s.Invalid)
	}
	for _, addr := range []int{6, 84, 181, 187} {
		if !slices.ContainsFunc(s.Settings, func(st backup.Setting) bool { return st.Addr == addr }) {
			t.Errorf("field @%d is not in the summary", addr)
		}
	}
	for _, st := range s.Settings {
		if st.Addr == 181 && *st.Value != 4 {
			t.Errorf("181 = %d, want raw 4", *st.Value)
		}
		if st.Addr == 189 {
			t.Error("an absent optional field is listed")
		}
	}
	if len(s.Shortcuts) != 0 || len(s.Macros) != 0 {
		t.Errorf("bodies decoded from a settings page: %+v %+v", s.Shortcuts, s.Macros)
	}
}

func TestSummaryBodies(t *testing.T) {
	tests := []struct {
		os      keys.OS
		actions map[int]string
		keys    map[int]string
	}{
		{keys.Mac, map[int]string{
			2: "Copy (Cmd+C)", 3: "Cmd+Shift+T", 5: "Media: Play/Pause", 8: `Macro 3 "ab", once`,
		}, map[int]string{2: "Cmd+C", 3: "Cmd+Shift+T", 5: "Media: Play/Pause"}},
		{keys.Win, map[int]string{2: "Win+C", 8: `Macro 3 "ab", once`}, map[int]string{2: "Win+C"}},
	}
	for _, tt := range tests {
		s := backup.Summarize(em11(t), richImage(t), tt.os)
		for slot, want := range tt.actions {
			if got := s.Buttons[slot].Action; got != want {
				t.Errorf("%v slot %d = %q, want %q", tt.os, slot, got, want)
			}
		}
		for slot, want := range tt.keys {
			i := slices.IndexFunc(s.Shortcuts, func(sc backup.Shortcut) bool { return sc.Slot == slot })
			if i < 0 || s.Shortcuts[i].Keys != want || !s.Shortcuts[i].Bound {
				t.Errorf("%v shortcut %d = %+v, want %q", tt.os, slot, s.Shortcuts, want)
			}
		}
		if len(s.Macros) != 1 {
			t.Fatalf("macros %+v", s.Macros)
		}
		m := s.Macros[0]
		if m.Slot != 3 || m.Name != "ab" || len(m.Events) != 4 || !slices.Equal(m.BoundBy, []int{8}) {
			t.Errorf("macro %+v", m)
		}
		if e := m.Events[0]; !e.Press || e.Key != "A" || e.Delay != 50 {
			t.Errorf("first event %+v", e)
		}
	}
}

func TestSummaryInvalidBody(t *testing.T) {
	im := richImage(t)
	must(t, im.Set(256+2*32+3, []byte{0x99}))
	s := backup.Summarize(em11(t), im, keys.Mac)
	if got := s.Buttons[2].Action; got != "shortcut with an invalid body" {
		t.Errorf("slot 2 = %q", got)
	}
	i := slices.IndexFunc(s.Invalid, func(iv backup.Invalid) bool { return iv.Name == "Shortcut 2" })
	if i < 0 || s.Invalid[i].Error == "" {
		t.Errorf("invalid %+v", s.Invalid)
	}
}

func TestRepeat(t *testing.T) {
	for cycle, want := range map[byte]string{1: "once", 5: "5 times", 253: "until pressed again", 254: "while held", 255: "until any key"} {
		if got := backup.Repeat(cycle); got != want {
			t.Errorf("Repeat(%d) = %q, want %q", cycle, got, want)
		}
	}
}

func TestSummaryUnknownImage(t *testing.T) {
	s := backup.Summarize(em11(t), flash.New(), keys.Mac)
	if s.Rate != nil || s.Buttons[0].Action != "not read" || s.DPI[0].State != "unknown" {
		t.Errorf("summary of nothing: %+v", s)
	}
}
