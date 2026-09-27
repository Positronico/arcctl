package tui

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

func btnStroke(t *testing.T, code string) keys.Stroke {
	t.Helper()
	k, ok := keys.ByCode(code)
	if !ok {
		t.Fatalf("no key %s", code)
	}
	return k.Stroke()
}

// btnCombos are the maintainer's shortcuts (§2). The dump holds only their
// bindings; the bodies are built with the encoders.
func btnCombos(t *testing.T) map[int]keys.Combo {
	cmd, ctrl := keys.LMeta.Stroke(), keys.LCtrl.Stroke()
	return map[int]keys.Combo{
		2: {cmd, btnStroke(t, "KeyV")},
		3: {ctrl, btnStroke(t, "Tab")},
		4: {cmd, btnStroke(t, "Tab")},
		5: {cmd, btnStroke(t, "KeyC")},
	}
}

func btnImage(t *testing.T) *flash.Image {
	t.Helper()
	im := dumpImage(t)
	for slot, combo := range btnCombos(t) {
		body, err := mouse.EncodeShortcut(combo)
		if err != nil {
			t.Fatal(err)
		}
		ext, _ := mouse.ShortcutExtent(slot)
		b := append([]byte(body), bytes.Repeat([]byte{0xFF}, ext.Len-len(body))...)
		if err := im.Set(ext.Addr, b); err != nil {
			t.Fatal(err)
		}
	}
	return im
}

func btnSnapshot(t *testing.T) *session.Snapshot {
	sn := ready(t)
	sn.Image = btnImage(t)
	return sn
}

// btnReads records what the tab asks the session to read.
type btnReads struct{ extents []flash.Extent }

func (r *btnReads) read(_ context.Context, e ...flash.Extent) (session.Capture, error) {
	r.extents = append(r.extents, e...)
	return session.Capture{}, nil
}

func btnHarness(t *testing.T, sn *session.Snapshot, opts ...func(*Options)) (*harness, *Buttons, *btnReads) {
	t.Helper()
	reads := &btnReads{}
	b := NewButtons(reads.read)
	set := func(o *Options) {
		o.Tabs = []Tab{b}
		o.OS = keys.Mac
	}
	h := newHarness(t, sn, append([]func(*Options){set}, opts...)...)
	return h, b, reads
}

// btnRow moves the cursor to the row of slot.
func btnRow(t *testing.T, h *harness, b *Buttons, slot int) {
	t.Helper()
	rows := b.rows(h.app.context())
	i := slices.IndexFunc(rows, func(r buttonRow) bool { return r.slot == slot })
	if i < 0 {
		t.Fatalf("no row for slot %d", slot)
	}
	b.cursor = i
}

func btnPicker(t *testing.T, h *harness) *buttonPicker {
	t.Helper()
	p, ok := h.app.dialog.(*buttonPicker)
	if !ok {
		t.Fatalf("the picker is not open (dialog %T, notice %q)", h.app.dialog, h.app.notice.text)
	}
	return p
}

func btnStaged(t *testing.T, h *harness, slot int) mouse.Edit {
	t.Helper()
	s, ok := h.app.Pending().Get(SlotKey(slot))
	if !ok {
		t.Fatalf("nothing pending for slot %d (notice %q)", slot, h.app.notice.text)
	}
	return s.Edit
}

func TestButtonsLabelTheMaintainersShortcuts(t *testing.T) {
	cfg := mouse.Decode(em11(), btnImage(t))
	want := map[keys.OS][]string{
		keys.Mac: {"Left Click", "Right Click", "Cmd+V (Paste)", "Ctrl+Tab", "Cmd+Tab", "Cmd+C (Copy)", "Profile Switch",
			"DPI Cycle", "Scroll Left", "Scroll Right", "DPI+", "DPI-", "Do nothing", "Do nothing", "Do nothing", "Do nothing"},
		keys.Win: {"Left Click", "Right Click", "Win+V", "Ctrl+Tab", "Win+Tab (Task View)", "Win+C"},
	}
	for os, fns := range want {
		for k, w := range fns {
			got, st := slotFunction(em11(), &cfg, k, os)
			if got != w || st != slotValid {
				t.Errorf("%s slot %d: %q (%s), want %q (valid)", os, k, got, st, w)
			}
		}
	}
	bare := mouse.Decode(em11(), dumpImage(t))
	if got, st := slotFunction(em11(), &bare, 2, keys.Mac); got != "shortcut (not read)" || st != slotUnread {
		t.Errorf("slot 2 without its body: %q (%s)", got, st)
	}
}

func TestButtonsScreens(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	btnRow(t, h, b, 2)
	h.golden("buttons-mac")
	h.keys("o")
	h.golden("buttons-win")
	h.keys("o", "s")
	btnRow(t, h, b, 6)
	h.golden("buttons-all-slots")
	btnRow(t, h, b, 5)
	h.keys("s")
	if rows := b.rows(h.app.context()); len(rows) != 6 || rows[b.cursor].slot != 5 {
		t.Errorf("back to the buttons, the cursor is on row %d", b.cursor)
	}
	h.screen(60, 20)
}

func TestButtonsASCII(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t), func(o *Options) { o.ASCII = true })
	btnRow(t, h, b, 2)
	h.keys("enter")
	btnPicker(t, h)
	for _, l := range strings.Split(h.screen(80, 24), "\n") {
		for _, r := range l {
			if r > 0x7E {
				t.Fatalf("non-ASCII %q in %q", r, l)
			}
		}
	}
}

func TestButtonsMediaFromThePicker(t *testing.T) {
	h, b, reads := btnHarness(t, btnSnapshot(t), func(o *Options) { o.Gates.AllowUntested = true })
	btnRow(t, h, b, 3)
	h.keys("enter")
	p := btnPicker(t, h)
	if p.current(h.app.context()) != secCombo || !slices.Equal(p.comp.mods, []keys.Modifier{keys.LCtrl}) {
		t.Fatalf("slot 3 holds Ctrl+Tab; the picker opened on %d with %v", p.sec, p.comp.mods)
	}
	h.keys("shift+tab", "/")
	h.typeText("vol")
	h.golden("buttons-picker-media-filter")
	if got := p.filtered(h.app.context(), secMedia); len(got) != 2 || got[0].name != "Volume Up" {
		t.Fatalf("filter vol: %v", got)
	}
	h.keys("backspace", "backspace", "backspace")
	h.typeText("pause")
	h.keys("enter", "enter")
	if h.app.dialog != nil {
		t.Fatalf("the picker stays open: %q", h.app.notice.text)
	}
	if got := btnStaged(t, h, 3); got != (mouse.SetMedia{Slot: 3, Usage: 0x00CD}) {
		t.Fatalf("staged %#v", got)
	}
	if len(reads.extents) != 0 {
		t.Errorf("read %v although the body was loaded", reads.extents)
	}
	h.golden("buttons-staged")
	c := h.app.context()
	p2, err := c.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Ops) != 3 || p2.Ops[0].New[0] != byte(mouse.TypeDisable) {
		t.Errorf("want the two-phase trio, got %v", p2.Ops)
	}
	if w := mouse.WebCompat(c.Model(), p2); len(w) != 0 {
		t.Errorf("web-compat warnings %v", w)
	}
}

func TestButtonsComposer(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t), func(o *Options) { o.Gates.AllowUntested = true })
	btnRow(t, h, b, 5)
	h.keys("enter")
	p := btnPicker(t, h)
	c := h.app.context()
	if s := p.current(c); s != secSpecial || p.filtered(c, s)[p.lists[s].cursor].name != "Copy" {
		t.Fatalf("slot 5 holds the mac Copy preset; the picker opened on %d", s)
	}
	if ks := p.comp.keysFor(keys.Mac); !slices.Equal(p.comp.mods, []keys.Modifier{keys.LMeta}) || ks[p.comp.list.cursor].Code != "KeyC" {
		t.Fatalf("not prefilled with Cmd+C: %v", p.comp.mods)
	}
	h.keys("tab", "tab", "0", "2", "1", "/")
	h.typeText("f5")
	h.keys("enter")
	h.golden("buttons-composer")
	h.keys("enter")
	want := keys.Combo{keys.LShift.Stroke(), keys.LCtrl.Stroke(), btnStroke(t, "F5")}
	if got, ok := btnStaged(t, h, 5).(mouse.SetShortcut); !ok || got.Slot != 5 || !slices.Equal(got.Combo, want) {
		t.Fatalf("staged %#v, want the toggle order %v", got, want)
	}
	if !strings.Contains(h.screen(80, 24), "Shift+Ctrl+F5") {
		t.Error("the row does not show the pending combo in its toggle order")
	}

	h.keys("enter")
	p = btnPicker(t, h)
	if !slices.Equal(p.comp.mods, []keys.Modifier{keys.LShift, keys.LCtrl}) {
		t.Fatalf("not prefilled with the pending combo: %v", p.comp.mods)
	}
	h.keys("3", "4", "5")
	if len(p.comp.mods) != 4 || p.comp.err == "" {
		t.Fatalf("a fifth modifier was taken: %v %q", p.comp.mods, p.comp.err)
	}
	h.keys("4", "5")
	h.golden("buttons-composer-right")
	h.keys("enter")
	e := btnStaged(t, h, 5)
	pv := previewSlot(c, 5, e)
	if pv.err != nil || pv.tier != catalog.Untested || len(pv.warns) != 1 || pv.warns[0].Reason != mouse.RightModifier {
		t.Fatalf("RCtrl: tier %s, warnings %v, err %v", pv.tier, pv.warns, pv.err)
	}

	h.keys("enter", "0", "/")
	h.typeText("menu")
	h.keys("enter")
	p = btnPicker(t, h)
	ks := p.comp.keysFor(keys.Mac)
	if len(ks) != 1 || ks[0].Kind != keys.KindMenu {
		t.Fatalf("filter menu: %v", ks)
	}
	if l := btnLine(h.screen(80, 24), "ContextMenu"); !strings.Contains(l, "untested") {
		t.Errorf("the Menu key is not tagged untested: %q", l)
	}
}

func TestButtonsPickerFilters(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	btnRow(t, h, b, 1)
	h.keys("enter")
	p := btnPicker(t, h)
	c := h.app.context()
	if p.current(c) != secSystem {
		t.Fatalf("slot 1 holds Right Click; the picker opened on %d", p.sec)
	}
	h.keys("/")
	h.typeText("dpi")
	names := func(s pickSection) []string {
		var out []string
		for _, e := range p.filtered(h.app.context(), s) {
			out = append(out, e.name+" "+e.info)
		}
		return out
	}
	if got := names(secSystem); !slices.Equal(got, []string{"DPI Cycle DPI Switch", "DPI+ DPI Switch", "DPI- DPI Switch"}) {
		t.Errorf("System, dpi: %v", got)
	}
	h.golden("buttons-picker-system-filter")
	h.keys("enter", "tab", "/")
	h.typeText("copy")
	if got := names(secSpecial); !slices.Equal(got, []string{"Copy Cmd+C"}) {
		t.Errorf("Special on mac, copy: %v", got)
	}
	h.keys("enter", "o")
	if got := names(secSpecial); !slices.Equal(got, []string{"Copy Ctrl+C"}) {
		t.Errorf("Special on win, copy: %v", got)
	}
	h.keys("esc")
	if got := names(secSpecial); len(got) != len(keys.WinPresets()) {
		t.Errorf("esc keeps a filter: %v", got)
	}
	h.keys("o")
	h.golden("buttons-picker-special")
	h.keys("esc")
	if h.app.dialog != nil {
		t.Error("esc does not close the picker")
	}
}

func TestButtonsLeftClickGuard(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	h.keys("enter")
	if h.app.dialog != nil || !strings.Contains(h.app.notice.text, "Left Click") {
		t.Fatalf("slot 0 opened the picker (notice %q)", h.app.notice.text)
	}
	if !strings.Contains(h.screen(80, 24), "guarded") {
		t.Error("slot 0 is not shown as guarded")
	}
	btnRow(t, h, b, 1)
	h.keys("enter", "home", "down", "enter")
	if got := btnStaged(t, h, 1); got != (mouse.SetKey{Slot: 1, Fn: leftClickFn}) {
		t.Fatalf("staged %#v", got)
	}
	btnRow(t, h, b, 0)
	h.keys("enter", "home", "down", "down", "enter")
	if got := btnStaged(t, h, 0); got != (mouse.SetKey{Slot: 0, Fn: mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamRight}}) {
		t.Fatalf("staged %#v", got)
	}
	if _, err := h.app.context().Plan(); err != nil {
		t.Fatalf("the swap does not plan: %v", err)
	}
	btnRow(t, h, b, 1)
	h.keys("d")
	if _, ok := h.app.Pending().Get(SlotKey(1)); !ok || !strings.Contains(h.app.notice.text, "Left Click") {
		t.Errorf("dropped the last Left Click (notice %q)", h.app.notice.text)
	}
	h.keys("enter")
	if h.app.dialog != nil {
		t.Error("the pending Left Click opened the picker")
	}
	btnRow(t, h, b, 0)
	h.keys("d")
	if h.app.Pending().Len() != 1 {
		t.Errorf("slot 0's edit was not dropped: %q", h.app.notice.text)
	}
}

func TestButtonsHiddenSlots(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	h.keys("s")
	btnRow(t, h, b, 6)
	h.keys("enter")
	if h.app.dialog != nil || h.app.notice.text != plain(errHiddenSlot) {
		t.Fatalf("slot 6 without --experimental: dialog %T, notice %q", h.app.dialog, h.app.notice.text)
	}

	h, b, _ = btnHarness(t, btnSnapshot(t), func(o *Options) { o.Gates.Experimental = true })
	h.keys("s")
	btnRow(t, h, b, 6)
	h.keys("enter")
	btnPicker(t, h)
	h.golden("buttons-picker-hidden-slot")
	h.keys("home", "enter")
	if h.app.Pending().Len() != 0 || !strings.Contains(h.app.notice.text, "no recorded hardware test") {
		t.Fatalf("an experimental edit was staged (notice %q)", h.app.notice.text)
	}
}

func TestButtonsMid6(t *testing.T) {
	sn := ready(t)
	sn.Model, _ = catalog.ByKey("7B06")
	sn.Handshake.MID, sn.Identity.MID = 6, 6
	h, b, _ := btnHarness(t, sn)
	rows := b.rows(h.app.context())
	if len(rows) != 8 || rows[6].slot != 12 || rows[7].slot != 13 {
		t.Fatalf("rows %v", rows)
	}
	btnRow(t, h, b, 12)
	for _, r := range rows[6:] {
		in := b.info(h.app.context(), r)
		if in.tier != catalog.Untested || in.webText() != "warns" {
			t.Errorf("slot %d: tier %s, web %q", r.slot, in.tier, in.webText())
		}
	}
	h.golden("buttons-mid6")
}

func TestButtonsNoChangeDropsTheEdit(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	btnRow(t, h, b, 4)
	h.keys("enter", "shift+tab", "shift+tab", "home", "enter")
	btnStaged(t, h, 4)
	h.keys("enter", "tab", "tab", "0", "4", "/")
	h.typeText("tab")
	h.keys("enter", "enter")
	if h.app.Pending().Len() != 0 || !strings.Contains(h.app.notice.text, "already does Cmd+Tab") {
		t.Fatalf("%d pending, notice %q", h.app.Pending().Len(), h.app.notice.text)
	}
}

func TestButtonsReadAnUnreadBody(t *testing.T) {
	h, b, reads := btnHarness(t, ready(t))
	if !strings.Contains(h.screen(120, 40), "shortcut (not read)") {
		t.Error("the unread bodies are not shown as such")
	}
	btnRow(t, h, b, 1)
	h.keys("enter", "shift+tab", "shift+tab", "home", "enter")
	if got := btnStaged(t, h, 1); got != (mouse.SetMedia{Slot: 1, Usage: 0x0183}) {
		t.Fatalf("staged %#v", got)
	}
	want, _ := mouse.ShortcutExtent(1)
	if !slices.Equal(reads.extents, []flash.Extent{want}) {
		t.Errorf("read %v, want %v", reads.extents, want)
	}
}

func TestButtonsReadWhenReady(t *testing.T) {
	h, b, reads := btnHarness(t, withState(ready(t), session.Offline))
	btnRow(t, h, b, 1)
	h.keys("enter", "shift+tab", "shift+tab", "home", "enter")
	btnStaged(t, h, 1)
	if len(reads.extents) != 0 || !strings.Contains(h.app.notice.text, "once the mouse is ready") {
		t.Fatalf("read %v while offline (notice %q)", reads.extents, h.app.notice.text)
	}
	h.snap(ready(t))
	want, _ := mouse.ShortcutExtent(1)
	if !slices.Equal(reads.extents, []flash.Extent{want}) {
		t.Errorf("read %v, want %v", reads.extents, want)
	}
	h.snap(withState(ready(t), session.Offline))
	h.snap(ready(t))
	if len(reads.extents) != 2 {
		t.Errorf("a new Ready did not read the body again: %v", reads.extents)
	}
}

func TestButtonsReadOnlyStagesNothing(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t), func(o *Options) { o.Mode = ModeReadOnly })
	btnRow(t, h, b, 1)
	h.keys("enter", "home", "enter")
	if h.app.Pending().Len() != 0 || h.app.notice.text != plain(ErrReadOnlyMode) {
		t.Errorf("%d pending, notice %q", h.app.Pending().Len(), h.app.notice.text)
	}
}

func btnLine(screen, sub string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// TestButtonsEmulatedApply stages two edits from the tab over a real session
// and an emulated EM11 Pro: slot 3 becomes Play/Pause (a two-phase rebind of
// a loaded body), slot 1 a media key whose body the load never read, which
// the tab reads through the session. The apply goes through the session's
// preflight, journal and read-back.
func TestButtonsEmulatedApply(t *testing.T) {
	bus := emu.New(emu.Options{})
	t.Cleanup(bus.Close)
	dev, err := bus.Add(emu.Config{Mouse: &emu.Mouse{Model: em11(), Image: btnImage(t), Firmware: emu.Version{Major: 1},
		Battery: emu.Battery{Level: 80, MilliVolts: 3900}}})
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	s := session.New(session.Options{
		Devices: bus,
		Timing:  session.Timing{Try: 25 * time.Millisecond, ProbeTry: 25 * time.Millisecond, Online: time.Hour, Battery: time.Hour},
		Writes: &session.Writes{
			Journal:  filepath.Join(tmp, "journal"),
			Backups:  backup.Store{Root: filepath.Join(tmp, "backups"), Tool: "arcctl test", Source: backup.SourceEmulator},
			Lock:     func() error { return nil },
			Executor: safety.Options{OfflineWait: 300 * time.Millisecond, Poll: 5 * time.Millisecond},
		},
	})
	b := NewButtons(s.Read)
	h := newHarness(t, s.Snapshot(), func(o *Options) {
		o.Session = s
		o.Gates.AllowUntested = true
		o.Source = "emulator"
		o.Tabs = []Tab{b}
		o.OS = keys.Mac
	})
	run := make(chan struct{})
	go func() {
		defer close(run)
		_ = s.Run(h.app.ctx)
	}()
	t.Cleanup(func() {
		h.app.Close()
		<-run
	})
	h.flush(func() bool { return h.app.sn.State == session.Ready && h.app.sn.Journal != nil && h.app.config != nil })

	btnRow(t, h, b, 3)
	h.keys("enter", "shift+tab", "/")
	h.typeText("pause")
	h.keys("enter", "enter")
	btnRow(t, h, b, 1)
	h.keys("enter", "shift+tab", "shift+tab", "/")
	h.typeText("home")
	h.keys("enter", "enter")
	if h.app.Pending().Len() != 2 {
		t.Fatalf("%d pending (notice %q)", h.app.Pending().Len(), h.app.notice.text)
	}
	body1, _ := mouse.ShortcutExtent(1)
	h.flush(func() bool {
		_, ok := h.app.sn.Image.Get(body1)
		return ok
	})

	p, err := h.app.context().Plan()
	if err != nil {
		t.Fatal(err)
	}
	g := h.app.opt.Gates
	g.Confirm = safety.ConfirmPhrase(p.Ops)
	h.startWrite(applyRequest(p, g))
	h.flush(func() bool { return h.app.write == nil && h.app.notice.text != "" })
	want := fmt.Sprintf("Apply done: %d of %d records verified", len(p.Ops), len(p.Ops))
	if !strings.HasPrefix(h.app.notice.text, want) || h.app.Pending().Len() != 0 {
		t.Fatalf("notice %q, %d pending", h.app.notice.text, h.app.Pending().Len())
	}
	for slot, usage := range map[int]uint16{1: 0x0223, 3: 0x00CD} {
		body, _ := mouse.EncodeMedia(usage)
		ext, _ := mouse.ShortcutExtent(slot)
		bind, _ := mouse.KeyFnExtent(slot)
		im := dev.Image()
		if got, _ := im.Get(flash.Extent{Addr: ext.Addr, Len: len(body)}); !bytes.Equal(got, body) {
			t.Errorf("slot %d body % x, want % x", slot, got, body)
		}
		if got, _ := im.Get(bind); !bytes.Equal(got, []byte{0x05, 0x00, 0x00, 0x50}) {
			t.Errorf("slot %d binding % x", slot, got)
		}
	}
	h.flush(func() bool {
		cfg := h.app.config
		return h.app.sn.State == session.Ready && cfg != nil && cfg.Shortcuts[3].Format(keys.Mac) == "Play/Pause"
	})
	if l := btnLine(h.screen(80, 24), "Backward"); !strings.Contains(l, "Play/Pause") || !strings.Contains(l, "valid") {
		t.Errorf("after the apply: %q", l)
	}
}

// o switches the labels of every screen: the tab's, the picker's and the
// review's, which names a combo the way the tab does.
func TestButtonsLabelsReachTheReview(t *testing.T) {
	h, _, _ := btnHarness(t, btnSnapshot(t), rvOpts(rvUntested)...)
	cmdC := keys.Combo{keys.LMeta.Stroke(), keys.Stroke{Kind: keys.KindKey, Value: 0x06}}
	h.app.Pending().Stage(Staged{Key: SlotKey(2), Desc: "Wheel Click (slot 2): Cmd+C (Copy)", Edit: mouse.SetShortcut{Slot: 2, Combo: cmdC}})
	h.keys("a")
	rvWant(t, h, "Wheel Click (slot 2): Cmd+C (Copy)", "Cmd+V (Paste) → Cmd+C (Copy)")
	h.keys("esc", "o", "a")
	rvWant(t, h, "Wheel Click (slot 2): Win+C", "Win+V → Win+C")
	if s := rvScreen(h); strings.Contains(s, "Cmd+") {
		t.Errorf("the review mixes mac and win labels:\n%s", s)
	}
}

// The composer finds a punctuation key by the character printed on it.
func TestButtonsComposerFindsPunctuation(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	btnRow(t, h, b, 5)
	h.keys("enter", "tab", "tab")
	p := btnPicker(t, h)
	for ch, code := range map[string]string{";": "Semicolon", "=": "Equal", `\`: "Backslash", "[": "BracketLeft", "'": "Quote"} {
		p.comp.list.filter = []rune(ch)
		if ks := p.comp.keysFor(keys.Mac); !slices.ContainsFunc(ks, func(k keys.Key) bool { return k.Code == code }) {
			t.Errorf("%q finds %v, not %s", ch, ks, code)
		}
	}
}

// In a dry run or a read-only session the Buttons detail does not ask for
// a flag the write does not need.
func TestButtonsGateNoteFollowsTheMode(t *testing.T) {
	for m, want := range map[Mode]string{ModeDryRun: "dry run: nothing reaches the mouse", ModeReadOnly: "read-only session"} {
		h, _, _ := btnHarness(t, btnSnapshot(t), mode(m))
		if s := strings.Join(strings.Fields(h.screen(80, 24)), " "); !strings.Contains(s, want) || strings.Contains(s, "--allow-untested") {
			t.Errorf("%v:\n%s", m, h.screen(80, 24))
		}
	}
}

// On macOS the web app drops Dictation, Action Center and nine media keys;
// the picker dims them and says so.
func TestButtonsMacHiddenEntries(t *testing.T) {
	h, b, _ := btnHarness(t, btnSnapshot(t))
	btnRow(t, h, b, 5)
	h.keys("enter")
	p := btnPicker(t, h)
	c := h.app.context()
	p.lists[secSpecial].cursor = 0
	if e := p.filtered(c, secSpecial)[0]; e.name != "Dictation" || !e.macHidden {
		t.Fatalf("first special entry %+v", e)
	}
	if s := strings.Join(strings.Fields(h.screen(120, 40)), " "); !strings.Contains(s, "web app: macOS does not offer this") {
		t.Errorf("the preview does not say the mac web app lacks Dictation:\n%s", h.screen(120, 40))
	}
	hidden := 0
	for _, e := range p.entries(c, secMedia) {
		if e.macHidden {
			hidden++
		}
	}
	if hidden != 9 {
		t.Errorf("%d media keys marked hidden on macOS, want 9", hidden)
	}
	h.keys("o")
	if e := p.filtered(h.app.context(), secSpecial)[0]; e.macHidden {
		t.Error("win labels still mark Dictation hidden")
	}
}

// Once H3 verified button.system on mid 6, slots 12 and 13 stay Untested
// until H3b maps them, and their edits ask for the typed phrase.
func TestButtonsMid6UnmappedSlots(t *testing.T) {
	sn := ready(t)
	sn.Model, _ = catalog.ByKey("7B06")
	sn.Handshake.MID, sn.Identity.MID = 6, 6
	vs := catalog.Verifications{{Model: "7B06", Feature: string(mouse.FeatureSystem), Firmware: sn.Versions.Mouse, Stage: "H3"}}
	h, b, _ := btnHarness(t, sn, func(o *Options) { o.Verified = vs })
	c := h.app.context()
	for _, r := range b.rows(c) {
		want := catalog.Verified
		if r.slot >= 12 {
			want = catalog.Untested
		}
		if in := b.info(c, r); in.tier != want {
			t.Errorf("slot %d: tier %s, want %s", r.slot, in.tier, want)
		}
	}
}
