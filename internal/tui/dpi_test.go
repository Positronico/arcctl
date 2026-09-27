package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

func dilTabs() []Tab { return []Tab{NewDPITab(), NewInfoTab(), NewLogTab()} }

// dilHarness runs the shell with the DPI, Info and Log tabs.
func dilHarness(t *testing.T, sn *session.Snapshot, opts ...func(*Options)) *harness {
	t.Helper()
	return newHarness(t, sn, append([]func(*Options){func(o *Options) { o.Tabs = dilTabs() }}, opts...)...)
}

// dilKeys sends key presses, naming the keys keyPress does not know.
func dilKeys(h *harness, names ...string) {
	h.t.Helper()
	codes := map[string]rune{"left": tea.KeyLeft, "right": tea.KeyRight, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"home": tea.KeyHome, "end": tea.KeyEnd, "delete": tea.KeyDelete}
	for _, n := range names {
		if r, ok := codes[n]; ok {
			h.send(tea.KeyPressMsg{Code: r})
			continue
		}
		h.send(keyPress(n))
	}
}

func dpiTab(h *harness) *DPITab { return h.app.tabs[0].(*DPITab) }

func stagedEdit(h *harness, key string) (mouse.Edit, bool) {
	s, ok := h.app.Pending().Get(key)
	return s.Edit, ok
}

func TestDPILegalValues(t *testing.T) {
	legal := dpiLegal(em11())
	if len(legal) != 55 || legal[0] != 200 || legal[len(legal)-1] != 7200 {
		t.Fatalf("legal: %d values, %v...%v", len(legal), legal[0], legal[len(legal)-1])
	}
	for _, v := range []int{4000, 4200, 7200} {
		if !slices.Contains(legal, v) {
			t.Errorf("%d missing", v)
		}
	}
	for _, v := range []int{4100, 7400, 8000} {
		if slices.Contains(legal, v) {
			t.Errorf("%d is not legal on the EM11 Pro", v)
		}
	}
	for _, c := range []struct{ in, snap int }{{1, 200}, {200, 200}, {1650, 1700}, {4001, 4200}, {4300, 4400}, {7201, 7200}, {99999, 7200}} {
		if got := dpiSnap(legal, c.in); got != c.snap {
			t.Errorf("snap %d = %d, want %d", c.in, got, c.snap)
		}
	}
	for _, c := range []struct{ from, dir, want int }{{1200, 1, 1300}, {1200, -1, 1100}, {4000, 1, 4200}, {4200, -1, 4000},
		{200, -1, 200}, {7200, 1, 7200}, {1250, 1, 1300}, {1250, -1, 1200}, {0, 0, 200}} {
		if got := dpiNext(legal, c.from, c.dir); got != c.want {
			t.Errorf("next(%d, %d) = %d, want %d", c.from, c.dir, got, c.want)
		}
	}
}

func TestDPIView(t *testing.T) {
	h := dilHarness(t, ready(t))
	h.golden("dpi")
	dilKeys(h, "x")
	h.golden("dpi-details")
}

func TestDPIStepAndType(t *testing.T) {
	h := dilHarness(t, ready(t))
	dilKeys(h, "down", "right")
	if e, ok := stagedEdit(h, DPIKey(1)); !ok || e != (mouse.SetDPI{Stage: 1, DPI: 1300}) {
		t.Fatalf("after right: %v %v", e, ok)
	}
	dilKeys(h, "left")
	if h.app.Pending().Len() != 0 {
		t.Fatalf("back to the device value still pending: %v", h.app.Pending().List())
	}
	dilKeys(h, "l", "l", "h")
	if e, _ := stagedEdit(h, DPIKey(1)); e != (mouse.SetDPI{Stage: 1, DPI: 1300}) {
		t.Fatalf("hl: %v", e)
	}
	dilKeys(h, "enter")
	h.typeText("1650")
	if !dpiTab(h).Capturing() {
		t.Fatal("typing does not capture the keys")
	}
	h.golden("dpi-typing")
	dilKeys(h, "enter")
	if e, _ := stagedEdit(h, DPIKey(1)); e != (mouse.SetDPI{Stage: 1, DPI: 1700}) {
		t.Fatalf("typed 1650: %v", e)
	}
	if !strings.Contains(h.app.notice.text, "1650 snaps up to 1700") {
		t.Errorf("notice %q", h.app.notice.text)
	}
	h.golden("dpi-pending")
	p := stagedPlan(t, h)
	want, _ := mouse.EncodeDPI(em11().Sensor, 1700)
	if len(p.Ops) != 1 || p.Ops[0].Extent != (flash.Extent{Addr: 16, Len: 4}) || !slices.Equal(p.Ops[0].New, want) {
		t.Errorf("plan %+v", p.Ops)
	}
}

func TestDPITypedValueCapped(t *testing.T) {
	h := dilHarness(t, ready(t))
	dilKeys(h, "enter")
	h.typeText("123456")
	dilKeys(h, "enter")
	if e, _ := stagedEdit(h, DPIKey(0)); e != (mouse.SetDPI{Stage: 0, DPI: 7200}) {
		t.Fatalf("typed past the maximum: %v", e)
	}
	if !strings.Contains(h.app.notice.text, "12345 is above the 7200 DPI maximum") {
		t.Errorf("notice %q", h.app.notice.text)
	}
	dilKeys(h, "enter", "2", "esc")
	if h.app.activeTab(h.app.context()).Title() != "DPI" || dpiTab(h).Capturing() {
		t.Error("esc did not leave the value entry, or a digit switched tabs")
	}
	if e, _ := stagedEdit(h, DPIKey(0)); e != (mouse.SetDPI{Stage: 0, DPI: 7200}) {
		t.Errorf("esc changed the edit: %v", e)
	}
}

func TestDPIAsymmetricStage(t *testing.T) {
	h := dilHarness(t, ready(t))
	dilKeys(h, "down", "down", "down", "down", "down")
	h.golden("dpi-asymmetric")
	if h.app.Pending().Len() != 0 {
		t.Fatal("selecting an asymmetric stage staged an edit")
	}
	dilKeys(h, "right")
	p := stagedPlan(t, h)
	if len(p.Ops) != 1 {
		t.Fatalf("plan %+v", p.Ops)
	}
	d, err := mouse.DecodeDPI(em11().Sensor, p.Ops[0].New)
	if err != nil || d != (mouse.DPI{X: 5000, Y: 5000}) {
		t.Errorf("stage 6 written as %+v (%v), want X = Y = 5000", d, err)
	}
}

func TestDPICountAndCurrent(t *testing.T) {
	h := dilHarness(t, ready(t))
	dilKeys(h, "+")
	if h.app.Pending().Len() != 0 || !strings.Contains(h.app.notice.text, "1 to 6 stages") {
		t.Fatalf("count past 6: %v, %q", h.app.Pending().List(), h.app.notice.text)
	}
	dilKeys(h, "-", "-")
	if e, _ := stagedEdit(h, StagesKey); e != (mouse.SetStages{Count: 4}) {
		t.Fatalf("count: %v", e)
	}
	if _, ok := stagedEdit(h, CurrentKey); ok {
		t.Fatal("the current stage 4 moved while still active")
	}
	dilKeys(h, "-")
	if e, _ := stagedEdit(h, CurrentKey); e != (mouse.SetCurrent{Stage: 2}) {
		t.Fatalf("current after the count dropped below it: %v", e)
	}
	stagedPlan(t, h)
	dilKeys(h, "+", "+", "+")
	if _, ok := stagedEdit(h, StagesKey); ok {
		t.Fatal("count back to 6 is still pending")
	}
	dilKeys(h, "down", "space")
	if e, _ := stagedEdit(h, CurrentKey); e != (mouse.SetCurrent{Stage: 1}) {
		t.Fatalf("space on stage 2: %v", e)
	}
	dilKeys(h, "down", "down", "space")
	if _, ok := stagedEdit(h, CurrentKey); ok {
		t.Fatal("setting the device's current stage is still pending")
	}
	dilKeys(h, "-", "-", "-", "down", "space")
	if e, _ := stagedEdit(h, CurrentKey); e != (mouse.SetCurrent{Stage: 2}) || !strings.Contains(h.app.notice.text, "Stage 5 is inactive") {
		t.Errorf("space on an inactive stage: %v, %q", e, h.app.notice.text)
	}
	h.golden("dpi-count")
}

func TestDPIUndoStage(t *testing.T) {
	h := dilHarness(t, ready(t))
	dilKeys(h, "right", "down", "right", "up", "delete")
	if _, ok := stagedEdit(h, DPIKey(0)); ok {
		t.Fatal("delete kept stage 1's edit")
	}
	if _, ok := stagedEdit(h, DPIKey(1)); !ok {
		t.Fatal("delete dropped another stage's edit")
	}
}

func TestDPIRefusesEdits(t *testing.T) {
	h := dilHarness(t, ready(t), mode(ModeReadOnly))
	dilKeys(h, "right", "+", "space", "enter")
	if h.app.Pending().Len() != 0 || dpiTab(h).Capturing() || !strings.Contains(h.app.notice.text, "read-only") {
		t.Errorf("read-only session: %v, %q", h.app.Pending().List(), h.app.notice.text)
	}

	sn := ready(t)
	sn.Image = nil
	h = dilHarness(t, sn)
	dilKeys(h, "right")
	if h.app.Pending().Len() != 0 || !strings.Contains(h.app.notice.text, "Nothing is loaded") {
		t.Errorf("nothing loaded: %q", h.app.notice.text)
	}
}

func TestDPIPlanProblem(t *testing.T) {
	sn := ready(t)
	im := sn.Image.Clone()
	if err := im.Set(2, []byte{4, 0x55 - 4}); err != nil {
		t.Fatal(err)
	}
	if err := im.Set(28, []byte{0x12, 0x34, 0x56, 0x78}); err != nil {
		t.Fatal(err)
	}
	sn.Image = im
	h := dilHarness(t, sn)
	dilKeys(h, "+")
	h.golden("dpi-problem")
	if !strings.Contains(h.screen(80, 24), "Cannot plan") {
		t.Error("no planner objection shown")
	}
	dilKeys(h, "down", "down", "down", "down", "right")
	if strings.Contains(h.screen(80, 24), "Cannot plan") {
		t.Error("the objection stays once stage 5 has a value")
	}
}

func TestDPIFollowsTheMouse(t *testing.T) {
	h := dilHarness(t, ready(t))
	sn := ready(t)
	im := sn.Image.Clone()
	if err := im.Set(4, []byte{1, 0x54}); err != nil {
		t.Fatal(err)
	}
	sn.Image = im
	h.snap(sn)
	if !strings.Contains(h.screen(80, 24), "current ● 2") {
		t.Errorf("the marker did not follow a DPI-button press:\n%s", h.screen(80, 24))
	}
}

func TestDPIASCII(t *testing.T) {
	h := dilHarness(t, ready(t), func(o *Options) { o.ASCII = true })
	dilKeys(h, "down", "right", "down", "down", "space")
	h.golden("dpi-ascii")
	if s := h.screen(120, 40); strings.ContainsFunc(s, func(r rune) bool { return r > 0x7F }) {
		t.Errorf("non-ASCII output:\n%s", s)
	}
}

// The current stage a lower count moved goes back once the count leaves
// room for the mouse's own again; one the user chose stays.
func TestDPICountGivesTheCurrentStageBack(t *testing.T) {
	h := dilHarness(t, ready(t))
	dilKeys(h, "-", "-", "-")
	if e, _ := stagedEdit(h, CurrentKey); e != (mouse.SetCurrent{Stage: 2}) {
		t.Fatalf("current after the count dropped below it: %v", e)
	}
	dilKeys(h, "+")
	if _, ok := stagedEdit(h, CurrentKey); ok {
		t.Fatal("the moved current stage stayed pending once stage 4 was active again")
	}
	dilKeys(h, "+", "+")
	if n := h.app.Pending().Len(); n != 0 {
		t.Fatalf("%d edits pending after the count went back: %v", n, h.app.Pending().List())
	}
	if s := h.screen(80, 24); !strings.Contains(s, "current ● 4") || strings.Contains(s, "pending") && strings.Contains(s, "was 4") {
		t.Errorf("header:\n%s", s)
	}
	dilKeys(h, "down", "space", "-", "-", "-", "-", "-")
	if e, _ := stagedEdit(h, CurrentKey); e != (mouse.SetCurrent{Stage: 0}) {
		t.Fatalf("current with one stage: %v", e)
	}
	dilKeys(h, "+", "+", "+", "+", "+")
	if e, _ := stagedEdit(h, CurrentKey); e != (mouse.SetCurrent{Stage: 1}) {
		t.Errorf("a current stage the user chose changed with the count: %v", e)
	}
}
