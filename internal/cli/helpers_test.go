package cli_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func fast() session.Timing {
	return session.Timing{
		Try:           25 * time.Millisecond,
		ProbeTry:      25 * time.Millisecond,
		Window:        time.Second,
		Debounce:      5 * time.Millisecond,
		Rescan:        20 * time.Millisecond,
		RescanMax:     50 * time.Millisecond,
		Retry:         20 * time.Millisecond,
		RetryMax:      50 * time.Millisecond,
		Offline:       40 * time.Millisecond,
		Online:        time.Hour,
		Battery:       time.Hour,
		Suspect:       30 * time.Millisecond,
		ConflictQuiet: 100 * time.Millisecond,
		LoadWatchdog:  10 * time.Second,
	}
}

// harness runs arcctl in a temporary data folder against an emulator bus that
// stands in for the real HID backend.
type harness struct {
	t      *testing.T
	root   string
	tmp    string
	bus    *emu.Bus
	host   *fakeHost
	paths  platform.Paths
	timing session.Timing
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root, err := vectors.ModuleRoot(".")
	must(t, err)
	tmp := t.TempDir()
	bus := emu.New(emu.Options{})
	t.Cleanup(bus.Close)
	h := &harness{t: t, root: root, tmp: tmp, bus: bus, timing: fast()}
	h.host = &fakeHost{bus: bus, perm: platform.Permission{Access: platform.AccessGranted,
		App: platform.App{Name: "Ghostty", Via: platform.ViaResponsibility}}}
	h.paths = platform.Paths{
		Data: tmp, Backups: filepath.Join(tmp, "backups"), Journal: filepath.Join(tmp, "journal"),
		Macros: filepath.Join(tmp, "macros.json"), Clients: filepath.Join(tmp, "clients.json"),
		Lock: filepath.Join(tmp, "arcctl.lock"), Cache: filepath.Join(tmp, "cache"), Logs: filepath.Join(tmp, "logs"),
	}
	return h
}

func (h *harness) env(stdout, stderr *bytes.Buffer) cli.Env {
	return cli.Env{
		Stdout:   stdout,
		Stderr:   stderr,
		Version:  "test",
		Now:      func() time.Time { return now },
		Paths:    func() (platform.Paths, error) { return h.paths, nil },
		Host:     h.host,
		HID:      func(string) (session.Devices, error) { return h.bus, nil },
		Timing:   h.timing,
		Executor: execOptions(),
	}
}

// forReplay has arcctl wait for each reply as long as on hardware: a replay
// follows the recorded packets one by one, so a resend on either side
// diverges.
func (h *harness) forReplay() {
	hw := session.DefaultTiming()
	h.timing.Try, h.timing.ProbeTry = hw.Try, hw.ProbeTry
}

// execOptions keeps the write executor's waits short.
func execOptions() safety.Options {
	return safety.Options{OfflineWait: 300 * time.Millisecond, LockWait: 300 * time.Millisecond, Poll: 5 * time.Millisecond}
}

// run runs one command line and returns its output with the harness's paths
// replaced by $TMP and $ROOT.
func (h *harness) run(args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	return h.runCtx(context.Background(), args...)
}

// runCtx is run with a context the test may cancel, as an interrupt does.
func (h *harness) runCtx(ctx context.Context, args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code = cli.Run(ctx, args, h.env(&out, &errb))
	return h.clean(out.String()), h.clean(errb.String()), code
}

func (h *harness) clean(s string) string {
	s = strings.ReplaceAll(s, h.tmp, "$TMP")
	s = strings.ReplaceAll(s, h.root, "$ROOT")
	s = strings.ReplaceAll(s, runtime.GOOS+"/"+runtime.GOARCH, "$PLATFORM")
	s = strings.ReplaceAll(s, "pid "+strconv.Itoa(os.Getpid())+")", "pid $PID)")
	s = started.ReplaceAllString(s, "started $$TIME")
	ids := map[string]string{}
	s = runID.ReplaceAllStringFunc(s, func(id string) string {
		if n, ok := ids[id]; ok {
			return n
		}
		ids[id] = "$RUN" + strconv.Itoa(len(ids)+1)
		return ids[id]
	})
	return goVersion.ReplaceAllString(s, "$$GO")
}

var (
	goVersion = regexp.MustCompile(regexp.QuoteMeta(runtime.Version()))
	// runID matches journal run IDs, which hold the time a session started
	// and its pid; clean numbers them in order of appearance. Runs start at
	// the real time, unlike backups.
	runID   = regexp.MustCompile(`\d{8}T\d{6}\.\d{9}Z-\d+(?:-\d+)?/\d+`)
	started = regexp.MustCompile(`started \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UTC`)
)

func (h *harness) dump() string { return filepath.Join(h.root, "testdata", "flash-dump.bin") }

// add plugs in a device and, when the test ends, checks that nothing but
// read-only commands for its target reached it.
func (h *harness) add(c emu.Config) *emu.Device {
	h.t.Helper()
	d, err := h.bus.Add(c)
	must(h.t, err)
	h.t.Cleanup(func() { checkReadOnly(h.t, d) })
	return d
}

func checkReadOnly(t *testing.T, d *emu.Device) {
	t.Helper()
	for _, w := range d.Writes() {
		p := w.Packet
		if err := wire.ReadOnly.Check(p, p.Target()); err != nil {
			t.Errorf("packet %v reached the device: %v", p, err)
		}
		if slices.Contains([]wire.Cmd{wire.CmdWrite, wire.CmdClear, wire.CmdSetLongRange, wire.CmdPair, wire.CmdPairState, wire.CmdSetProfile}, p.Cmd()) {
			t.Errorf("%v reached the device", p.Cmd())
		}
	}
}

// golden compares got with testdata/golden/<name>.txt, which -update
// rewrites. No line may be wider than 80 columns.
func golden(t *testing.T, name, got string) {
	t.Helper()
	for i, line := range strings.Split(got, "\n") {
		if n := utf8.RuneCountInString(line); n > 80 {
			t.Errorf("%s line %d has %d columns: %q", name, i+1, n, line)
		}
	}
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		must(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if got != string(want) && !rewrapped(string(want), got) {
		t.Errorf("%s differs from %s:\n%s", name, path, firstDiff(string(want), got))
	}
}

// rewrapped reports whether want and got differ only in where lines break,
// in an output that names a path under $ROOT or $TMP: arcctl wraps a
// message around the real path, whose length depends on the checkout and
// the temporary folder.
func rewrapped(want, got string) bool {
	if !strings.Contains(got, "$ROOT") && !strings.Contains(got, "$TMP") {
		return false
	}
	return slices.Equal(strings.Fields(want), strings.Fields(got))
}

func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + strconv.Itoa(i+1) + "\n want: " + a + "\n  got: " + b + "\n--- got in full ---\n" + got
		}
	}
	return ""
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

func em11(t testing.TB) *catalog.Model {
	t.Helper()
	m, ok := catalog.ByKey("7B04")
	if !ok {
		t.Fatal("no EM11 Pro")
	}
	return m
}

func dumpImage(t testing.TB, root string) *flash.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "testdata", "flash-dump.bin"))
	must(t, err)
	im, err := flash.FromDump(0, b)
	must(t, err)
	return im
}

// richImage is the dump with shortcut bodies in the slots it binds, a media
// key on slot 5, and slot 8 bound to a macro in slot 3.
func richImage(t testing.TB, root string) *flash.Image {
	t.Helper()
	im := dumpImage(t, root)
	combos := map[int]keys.Combo{
		2: {keys.LMeta.Stroke(), {Kind: keys.KindKey, Value: 0x06}},
		3: {keys.LMeta.Stroke(), keys.LShift.Stroke(), {Kind: keys.KindKey, Value: 0x17}},
		4: {keys.LCtrl.Stroke(), {Kind: keys.KindKey, Value: 0x2B}},
		5: {{Kind: keys.KindConsumer, Value: 0xCD}},
	}
	for slot, c := range combos {
		b, err := mouse.EncodeShortcut(c)
		must(t, err)
		e, _ := mouse.ShortcutExtent(slot)
		must(t, im.Set(e.Addr, b))
	}
	mb, err := mouse.EncodeMacro(mouse.Macro{Name: "hello", Events: []mouse.Event{
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x0B}, Delay: 40},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x0B}, Delay: 20},
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x08}, Delay: 40},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x08}, Delay: 20},
	}})
	must(t, err)
	e, _ := mouse.MacroExtent(3)
	must(t, im.Set(e.Addr, mb))
	fn, err := mouse.MacroBinding(3, 254)
	must(t, err)
	r, err := mouse.EncodeKeyFn(fn)
	must(t, err)
	e, _ = mouse.KeyFnExtent(8)
	must(t, im.Set(e.Addr, r))
	return im
}

func em11Mouse(t testing.TB, im *flash.Image) *emu.Mouse {
	return &emu.Mouse{
		Model:     em11(t),
		Image:     im,
		Firmware:  emu.Version{Major: 1, Minor: 5},
		Battery:   emu.Battery{Level: 80, MilliVolts: 3900},
		Profile:   ptr(byte(0)),
		LongRange: ptr(true),
	}
}

func receiver(m *emu.Mouse) emu.Config {
	return emu.Config{RxVersion: &emu.Version{Major: 1, Minor: 2}, Mouse: m}
}

// fakeHost is the OS as a test wants it: the permission and console it is
// given, and the bus's clients as the IORegistry scan.
type fakeHost struct {
	bus     *emu.Bus
	perm    platform.Permission
	console platform.Console
}

func (h *fakeHost) Permission() (platform.Permission, error)    { return h.perm, nil }
func (h *fakeHost) RequestPermission() (platform.Access, error) { return h.perm.Access, nil }
func (h *fakeHost) Console() (platform.Console, error)          { return h.console, nil }

func (h *fakeHost) Clients() ([]platform.DeviceClients, error) {
	cands, err := h.bus.Enumerate()
	if err != nil {
		return nil, err
	}
	var out []platform.DeviceClients
	for _, c := range cands {
		d := platform.DeviceClients{VID: c.VID, PID: c.PID, Interface: c.Interface, Path: c.Path}
		for _, cl := range h.bus.Clients() {
			if cl.Path == c.Path {
				d.Clients = append(d.Clients, platform.Client{
					Process: platform.Process{PID: cl.PID, Name: cl.Process}, Seized: cl.Seized, Self: cl.PID == os.Getpid(),
				})
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (h *fakeHost) Diagnose(err error) platform.Diagnosis {
	d := platform.Diagnosis{Class: hidio.Classify(err)}
	switch d.Class {
	case hidio.ClassPermission:
		d.Summary, d.Hint = "Input Monitoring is not granted to Ghostty", "Allow it in System Settings, then reopen Ghostty."
	case hidio.ClassLocked:
		d.Summary, d.Hint = "Secure Input is on, held by loginwindow (pid 200)", "Close the password field, then retry."
	case hidio.ClassSeized:
		d.Summary, d.Hint = "the device is held exclusively by Other (pid 7)", "Quit Other, then retry."
	}
	return d
}
