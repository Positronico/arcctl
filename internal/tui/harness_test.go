package tui

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// fakeSession is a session.API whose snapshot the test sets and whose calls
// it records.
type fakeSession struct {
	mu      sync.Mutex
	sn      *session.Snapshot
	changed chan struct{}
	calls   []string
	gates   []safety.Gates
	// write runs in place of Apply, Revert and Recover when set.
	write func(ctx context.Context, on func(safety.OpEvent)) (session.Outcome, error)
	err   error // what the other calls return
}

func newFake(sn *session.Snapshot) *fakeSession {
	return &fakeSession{sn: sn, changed: make(chan struct{}, 1)}
}

func (f *fakeSession) set(sn *session.Snapshot) {
	f.mu.Lock()
	f.sn = sn
	f.mu.Unlock()
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (f *fakeSession) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeSession) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSession) Snapshot() *session.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sn
}

func (f *fakeSession) Changed() <-chan struct{} { return f.changed }

func (f *fakeSession) Choose(_ context.Context, path string) error {
	f.record("choose " + path)
	return f.err
}

func (f *fakeSession) Reload(context.Context) error {
	f.record("reload")
	return f.err
}

func (f *fakeSession) Read(context.Context, ...flash.Extent) (session.Capture, error) {
	f.record("read")
	return session.Capture{}, f.err
}

func (f *fakeSession) Backup(_ context.Context, full bool) (session.Capture, error) {
	f.record(fmt.Sprintf("backup full=%v", full))
	return session.Capture{Image: f.Snapshot().Image}, f.err
}

func (f *fakeSession) doWrite(ctx context.Context, call string, g safety.Gates, on func(safety.OpEvent)) (session.Outcome, error) {
	f.record(call)
	f.mu.Lock()
	f.gates = append(f.gates, g)
	w := f.write
	f.mu.Unlock()
	if w == nil {
		return session.Outcome{}, f.err
	}
	return w(ctx, on)
}

func (f *fakeSession) Apply(ctx context.Context, p plan.Plan, g safety.Gates, on func(safety.OpEvent)) (session.Outcome, error) {
	return f.doWrite(ctx, fmt.Sprintf("apply %d ops", len(p.Ops)), g, on)
}

func (f *fakeSession) Recover(ctx context.Context, run string, how safety.Strategy, g safety.Gates, on func(safety.OpEvent)) (session.Outcome, error) {
	return f.doWrite(ctx, "recover "+run+" "+how.String(), g, on)
}

func (f *fakeSession) Revert(ctx context.Context, g safety.Gates, on func(safety.OpEvent)) (session.Outcome, error) {
	return f.doWrite(ctx, "revert", g, on)
}

func (f *fakeSession) Preflight(context.Context, plan.Plan, safety.Gates) error {
	f.record("preflight")
	return f.err
}

func (f *fakeSession) PreflightRevert(context.Context, safety.Gates) error {
	f.record("preflight revert")
	return f.err
}

func (f *fakeSession) Abort() { f.record("abort") }

func (f *fakeSession) ClearConflict(context.Context) error {
	f.record("clear conflict")
	return f.err
}

// approval stands in for the dialog where the user approves a write.
type approval struct{ used bool }

func (d *approval) Update(*Context, tea.Msg) tea.Cmd { return nil }
func (d *approval) View(*Context, int, int) string   { return "" }
func (d *approval) Hints(*Context) []key.Binding     { return nil }
func (d *approval) approves(writeRequest) bool       { ok := !d.used; d.used = true; return ok }

// startWrite sends r as a dialog that approved it would, and closes that
// dialog.
func (h *harness) startWrite(r writeRequest) {
	h.t.Helper()
	d := &approval{}
	r.from = d
	h.send(openDialogMsg{d}, r, closeDialogMsg{d})
}

func applyRequest(p plan.Plan, g safety.Gates) writeRequest {
	return writeRequest{kind: safety.KindApply, plan: p, gates: g}
}

// stubTab stands in for the real tabs: it shows its size and what it got.
type stubTab struct {
	title   string
	needs   []mouse.Feature
	keys    []string
	msgs    []tea.Msg
	capture bool
}

func (t *stubTab) Title() string          { return t.title }
func (t *stubTab) Needs() []mouse.Feature { return t.needs }
func (t *stubTab) Capturing() bool        { return t.capture }

func (t *stubTab) Update(c *Context, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		t.keys = append(t.keys, k.String())
		return nil
	}
	t.msgs = append(t.msgs, msg)
	return nil
}

func (t *stubTab) View(c *Context, w, h int) string {
	lines := []string{fmt.Sprintf(" %s tab body, %d x %d", t.title, w, h)}
	if cfg := c.Config(); cfg != nil {
		lines = append(lines, fmt.Sprintf(" decoded: %d stages", cfg.Stages.Value))
	}
	return strings.Join(lines, "\n")
}

func (t *stubTab) Hints(c *Context) []key.Binding {
	return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "change"))}
}

func stubTabs() []Tab {
	return []Tab{
		&stubTab{title: "Buttons", needs: []mouse.Feature{mouse.FeatureSystem, mouse.FeatureMedia, mouse.FeatureShortcut, mouse.FeatureMacro}},
		&stubTab{title: "DPI", needs: []mouse.Feature{mouse.FeatureStages, mouse.FeatureDPI, mouse.FeatureCurrent}},
		&stubTab{title: "Info"},
		&stubTab{title: "Advanced", needs: []mouse.Feature{mouse.FeatureHiddenSlot}},
		&stubTab{title: "Log"},
	}
}

// harness drives the shell's Update with synthetic messages. Commands run
// one at a time; one that has not returned after a short wait (the snapshot
// watcher, the event listener, a write the test holds) is kept, and flush
// delivers its message once it arrives.
type harness struct {
	t    *testing.T
	fake *fakeSession
	app  *App
	late []chan tea.Msg
	quit bool
	seq  uint64
}

func newHarness(t *testing.T, sn *session.Snapshot, opts ...func(*Options)) *harness {
	t.Helper()
	fake := newFake(sn)
	o := Options{
		Session:  fake,
		Mode:     ModeEdit,
		Source:   "device",
		OS:       keys.Mac,
		Verified: catalog.Verifications{},
		Now:      func() time.Time { return now },
		Tabs:     stubTabs(),
	}
	for _, f := range opts {
		f(&o)
	}
	h := &harness{t: t, fake: fake, app: New(context.Background(), o), seq: sn.Seq}
	t.Cleanup(h.app.Close)
	h.deliver(h.run(h.app.Init()))
	return h
}

const blocked = 30 * time.Millisecond

func (h *harness) send(msgs ...tea.Msg) {
	h.t.Helper()
	h.deliver(msgs)
}

func (h *harness) deliver(queue []tea.Msg) {
	for n := 0; len(queue) > 0; n++ {
		if n > 1000 {
			h.t.Fatal("the shell keeps sending itself messages")
		}
		msg := queue[0]
		queue = queue[1:]
		_, cmd := h.app.Update(msg)
		queue = append(queue, h.run(cmd)...)
	}
}

func (h *harness) run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return h.expand(msg)
	case <-time.After(blocked):
		h.late = append(h.late, ch)
		return nil
	}
}

var cmdType = reflect.TypeFor[tea.Cmd]()

// expand runs the commands of a Batch or Sequence in order.
func (h *harness) expand(msg tea.Msg) []tea.Msg {
	if msg == nil {
		return nil
	}
	if _, ok := msg.(tea.QuitMsg); ok {
		h.quit = true
		return nil
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Slice && v.Type().Elem() == cmdType {
		var out []tea.Msg
		for i := range v.Len() {
			c, _ := v.Index(i).Interface().(tea.Cmd)
			out = append(out, h.run(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// flush delivers the messages of kept commands as they arrive, until done
// holds.
func (h *harness) flush(done func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			h.t.Fatal("timed out waiting for the shell")
		}
		var ready []tea.Msg
		keep := h.late[:0]
		for _, ch := range h.late {
			select {
			case msg := <-ch:
				ready = append(ready, h.expand(msg)...)
			default:
				keep = append(keep, ch)
			}
		}
		h.late = keep
		if len(ready) == 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		h.deliver(ready)
	}
}

// snap delivers sn as the session's next snapshot.
func (h *harness) snap(sn *session.Snapshot) {
	h.t.Helper()
	h.seq++
	c := *sn
	c.Seq = h.seq
	h.fake.set(&c)
	h.send(SnapshotMsg{&c})
}

func (h *harness) keys(ks ...string) {
	h.t.Helper()
	for _, k := range ks {
		h.send(keyPress(k))
	}
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	k := tea.KeyPressMsg{Code: r, Text: s}
	if r >= 'A' && r <= 'Z' {
		k.Code, k.Mod, k.ShiftedCode = r-'A'+'a', tea.ModShift, r
	}
	return k
}

// typeText sends each character of s as a key press.
func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		if r == ' ' {
			h.send(keyPress("space"))
			continue
		}
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// screen renders the shell at w x h with the escape codes stripped.
func (h *harness) screen(w, ht int) string {
	h.t.Helper()
	h.send(tea.WindowSizeMsg{Width: w, Height: ht})
	s := ansi.Strip(h.app.View().Content)
	lines := strings.Split(s, "\n")
	if len(lines) != ht {
		h.t.Errorf("%dx%d: %d lines", w, ht, len(lines))
	}
	for i, l := range lines {
		if n := ansi.StringWidth(l); n != w {
			h.t.Errorf("%dx%d: line %d is %d wide: %q", w, ht, i, n, l)
		}
	}
	return s
}

// golden compares the screen at 80x24 and 120x40 with the golden files.
func (h *harness) golden(name string) {
	h.t.Helper()
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		got := trimRight(h.screen(size[0], size[1]))
		path := filepath.Join("testdata", "golden", fmt.Sprintf("%s.%dx%d.txt", name, size[0], size[1]))
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				h.t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				h.t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			h.t.Fatalf("%v (run with -update)", err)
		}
		if got != string(want) {
			h.t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", path, got, want)
		}
	}
}

func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// Snapshots of the states the tests show.

func em11() *catalog.Model {
	m, ok := catalog.ByKey("7B04")
	if !ok {
		panic("no 7B04 in the catalog")
	}
	return m
}

func dumpImage(t *testing.T) *flash.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "flash-dump.bin"))
	if err != nil {
		t.Fatal(err)
	}
	im, err := flash.FromDump(0, b)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

var receiver = hidio.Candidate{Backend: "emu", Path: "emu:1/IOUSBHostInterface@1", VID: 0x260D, PID: 0x1282,
	Interface: 1, Product: "Emulated receiver"}

func bare(st session.State) *session.Snapshot {
	return &session.Snapshot{State: st, Link: st, Since: now}
}

func attached(st session.State) *session.Snapshot {
	sn := bare(st)
	c := receiver
	sn.Device = &c
	sn.Versions.Receiver = "v1.0"
	return sn
}

func ready(t *testing.T) *session.Snapshot {
	sn := attached(session.Ready)
	sn.Online = true
	sn.Handshake = &session.Handshake{CID: 0x7B, MID: 4, Conn: 0}
	sn.Model = em11()
	sn.Identity = plan.Identity{CID: 0x7B, MID: 4, VID: 0x260D, PID: 0x1282}
	sn.Versions.Mouse = "v1.00"
	sn.Battery = &session.Battery{Level: 80, At: now}
	sn.Profile = session.Probe{Asked: true}
	sn.Image = dumpImage(t)
	sn.Journal = &session.JournalState{}
	return sn
}

func withState(sn *session.Snapshot, st session.State) *session.Snapshot {
	c := *sn
	c.State = st
	return &c
}
