package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// runIn is run with stdin as what the user types.
func (h *harness) runIn(stdin string, args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	var out, errb bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	env := h.env(&out, &errb)
	env.Stdin = strings.NewReader(stdin)
	code = runCLI(ctx, args, env)
	return h.clean(out.String()), h.clean(errb.String()), code
}

// restoreChanges is what restoreSetup changes on the mouse after the backup: DPI
// stage 2, the binding of slot 3, the shortcut of slot 4 and the colour of
// stage 1, which no feature writes.
type restoreChanges struct {
	dpi2, key3, short4, col1 flash.Extent
}

// restoreSetup backs up a writable emulated mouse to b0.json, then changes
// four records on it behind arcctl's back.
func restoreSetup(t *testing.T) (*harness, *emu.Device, string, restoreChanges) {
	t.Helper()
	h := newHarness(t)
	d := h.addWritable(receiver(em11Mouse(t, richImage(t, h.root))))
	b0 := filepath.Join(h.tmp, "b0.json")
	_, errs, code := h.run("backup", "-o", b0)
	expect(t, code, cli.ExitOK, errs)

	var c restoreChanges
	c.dpi2, _ = mouse.DPIExtent(1)
	dpi, err := mouse.EncodeDPI(em11(t).Sensor, 1600)
	must(t, err)
	must(t, d.Store(c.dpi2.Addr, dpi))
	c.key3, _ = mouse.KeyFnExtent(3)
	back, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack})
	must(t, err)
	must(t, d.Store(c.key3.Addr, back))
	body, err := mouse.EncodeShortcut(cmdAltTab)
	must(t, err)
	c.short4, _ = mouse.ShortcutExtent(4)
	must(t, d.Store(c.short4.Addr, body))
	c.col1, _ = mouse.ColorExtent(0)
	must(t, d.Store(c.col1.Addr, mouse.EncodeColor([3]byte{1, 2, 3})))
	return h, d, b0, c
}

func cmd7s(d *emu.Device) int {
	n := 0
	for _, w := range d.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			n++
		}
	}
	return n
}

func TestRestore(t *testing.T) {
	h, d, b0, c := restoreSetup(t)
	src, err := backup.Load(filepath.Join(h.tmp, "b0.json"))
	must(t, err)
	want := src.Image()

	out, errs, code := h.run("--os", "mac", "restore", b0, "--dry-run")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "restore-dry-run", out)
	if n := cmd7s(d); n != 0 {
		t.Fatalf("the dry run sent %d writes", n)
	}

	_, errs, code = h.runIn("", "restore", b0)
	if code != cli.ExitFailure || !strings.Contains(errs, "--allow-untested") {
		t.Errorf("without --allow-untested: exit %d\n%s", code, errs)
	}
	_, errs, code = h.runIn("write it\n", "restore", b0, "--allow-untested")
	if code != cli.ExitAborted || cmd7s(d) != 0 {
		t.Errorf("a wrong phrase: exit %d, %d writes\n%s", code, cmd7s(d), errs)
	}

	out, errs, code = h.runIn("write untested\n", "--os", "mac", "restore", b0, "--allow-untested")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "restore", out)
	im := d.Image()
	for _, e := range []flash.Extent{c.dpi2, c.key3, {Addr: c.short4.Addr, Len: 14}} {
		got, _ := im.Get(e)
		w, _ := want.Get(e)
		if !bytes.Equal(got, w) {
			t.Errorf("%v holds % x after the restore, the backup % x", e, got, w)
		}
	}
	if got, _ := im.Get(c.col1); !bytes.Equal(got, mouse.EncodeColor([3]byte{1, 2, 3})) {
		t.Errorf("the colour was written: % x", got)
	}

	_, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
	out, errs, code = h.run("restore", b0)
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(out, "Nothing to write") {
		t.Errorf("a second restore:\n%s", out)
	}
}

func TestRestoreRefusals(t *testing.T) {
	h, d, b0, _ := restoreSetup(t)
	f, err := backup.Load(b0)
	must(t, err)
	other := *f
	other.Device.Addr, other.Device.AddrTrusted = "44:55:66", true
	other.Device.Key = other.Identity().Key()
	otherPath := filepath.Join(h.tmp, "other.json")
	must(t, backup.SaveAs(otherPath, &other))
	profile := *f
	p := *f.Device.Profile
	p.Value = 1
	profile.Device.Profile = &p
	profilePath := filepath.Join(h.tmp, "profile1.json")
	must(t, backup.SaveAs(profilePath, &profile))
	bin := filepath.Join(h.tmp, "b0.bin")
	_, errs, code := h.run("export-bin", b0, "-o", bin)
	expect(t, code, cli.ExitOK, errs)
	must(t, d.Store(84, []byte{0x00, 0x55}))

	var all strings.Builder
	for _, tt := range []struct {
		args []string
		code int
	}{
		{[]string{otherPath}, cli.ExitFailure},
		{[]string{otherPath, "--other-device", "--dry-run"}, cli.ExitOK},
		{[]string{profilePath}, cli.ExitFailure},
		{[]string{profilePath, "--other-profile", "--dry-run"}, cli.ExitOK},
		{[]string{bin}, cli.ExitFailure},
		{[]string{bin, "--other-device"}, cli.ExitFailure},
		{[]string{bin, "--other-device", "--other-profile", "--dry-run"}, cli.ExitOK},
		{[]string{h.dump()}, cli.ExitFailure},
		{[]string{b0, "--replay", h.dump()}, cli.ExitUsage},
	} {
		out, errs, code := h.run(append([]string{"restore"}, tt.args...)...)
		if code != tt.code {
			t.Errorf("restore %v: exit %d, want %d\n%s%s", tt.args, code, tt.code, out, errs)
		}
		if tt.code != cli.ExitOK {
			all.WriteString(h.clean("$ arcctl restore "+strings.Join(tt.args, " ")) + "\n" + errs)
		}
		if tt.args[0] == bin && tt.code == cli.ExitOK {
			golden(t, "restore-bin", out)
		}
	}
	golden(t, "restore-refusals", all.String())
	if n := cmd7s(d); n != 0 {
		t.Errorf("%d writes reached the mouse", n)
	}
}

// A restore onto the emulator writes to a journal of its own and ends
// verified.
func TestRestoreEmulated(t *testing.T) {
	h, _, b0, _ := restoreSetup(t)
	b1 := filepath.Join(h.tmp, "b1.json")
	_, errs, code := h.run("backup", "-o", b1)
	expect(t, code, cli.ExitOK, errs)
	out, errs, code := h.runIn("write untested\n", "--emulate", b1, "restore", b0, "--allow-untested")
	expect(t, code, cli.ExitOK, errs)
	_, rest, ok := strings.Cut(errs, "the emulated mouse's journal and backups go to ")
	tmp, _, _ := strings.Cut(rest, ", which is removed on exit")
	if !ok || tmp == "" {
		t.Errorf("stderr does not name the emulator's folder:\n%s", errs)
	} else if _, err := os.Stat(tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the emulator's folder %s is left behind (%v)", tmp, err)
	}
	for _, want := range []string{"Shortcut 3", "4 records to write", "Restore run $RUN1: 6 writes verified.", "Checked: the mouse now holds"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	_, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
}

// --include-unknown writes back the bytes the backup captured where arcctl
// knows no valid value, after --experimental and a phrase that names every
// extent, verifies them like any record and leaves the rest alone, a hidden
// setting without its H8 record included (D5).
func TestRestoreIncludeUnknown(t *testing.T) {
	h, d, b0, c := restoreSetup(t)
	src, err := backup.Load(b0)
	must(t, err)
	want := src.Image()
	unknown := []flash.Extent{{Addr: 6, Len: 2}, {Addr: 84, Len: 12}, {Addr: 187, Len: 2}}
	sensor := flash.Extent{Addr: mouse.AddrSensorMode, Len: 2}
	for _, e := range append(unknown, sensor) {
		must(t, d.Store(e.Addr, []byte{0x01, 0x54}))
	}

	out, errs, code := h.run("--os", "mac", "restore", b0, "--include-unknown", "--dry-run")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "restore-include-unknown", out)
	if n := cmd7s(d); n != 0 {
		t.Fatalf("the dry run sent %d writes", n)
	}
	_, errs, code = h.runIn("", "restore", b0, "--include-unknown", "--allow-untested")
	if code != cli.ExitFailure || !strings.Contains(errs, "--experimental") {
		t.Errorf("without --experimental: exit %d\n%s", code, errs)
	}
	_, errs, code = h.runIn("write experimental\n", "restore", b0, "--include-unknown", "--allow-untested", "--experimental")
	if code != cli.ExitAborted || cmd7s(d) != 0 {
		t.Errorf("a phrase without the extents: exit %d, %d writes\n%s", code, cmd7s(d), errs)
	}

	phrase := "write experimental and captured 6+2 84+12 187+2"
	out, errs, code = h.runIn(phrase+"\n", "restore", b0, "--include-unknown", "--allow-untested", "--experimental")
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(errs, strconv.Quote(phrase)) || !strings.Contains(out, "Checked: the mouse now holds") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out, errs)
	}
	im := d.Image()
	for _, e := range append(unknown, c.dpi2) {
		got, _ := im.Get(e)
		w, _ := want.Get(e)
		if !bytes.Equal(got, w) {
			t.Errorf("%v holds % x after the restore, the backup % x", e, got, w)
		}
	}
	if got, _ := im.Get(c.col1); !bytes.Equal(got, mouse.EncodeColor([3]byte{1, 2, 3})) {
		t.Errorf("the colour was written: % x", got)
	}
	if got, _ := im.Get(sensor); !bytes.Equal(got, []byte{0x01, 0x54}) {
		t.Errorf("the hidden setting at %v was written: % x", sensor, got)
	}
	_, errs, code = h.run("journal", "status")
	expect(t, code, cli.ExitOK, errs)
	out, errs, code = h.run("restore", b0, "--include-unknown")
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(out, "Nothing to write") {
		t.Errorf("a second restore:\n%s", out)
	}
}
