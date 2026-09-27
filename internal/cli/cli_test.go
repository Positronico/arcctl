package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/wire"
)

func expect(t *testing.T, code, want int, stderr string) {
	t.Helper()
	if code != want {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, want, stderr)
	}
}

func TestShowDump(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("--os", "mac", "show", h.dump())
	expect(t, code, cli.ExitOK, errs)
	golden(t, "show-dump", out)
}

func TestShowJSON(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("show", "--json", h.dump(), "--os", "win")
	expect(t, code, cli.ExitOK, errs)
	var s backup.Summary
	must(t, json.Unmarshal([]byte(out), &s))
	if s.OS != "win" || *s.Rate != 250 || len(s.Invalid) != 1 {
		t.Errorf("summary %+v", s)
	}
}

func TestInfoEmulated(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("--emulate", h.dump(), "info")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "info-emulated", out)
	out, errs, code = h.run("info", "--json", "--emulate", h.dump())
	expect(t, code, cli.ExitOK, errs)
	golden(t, "info-emulated-json", out)
}

func TestInfoDevice(t *testing.T) {
	h := newHarness(t)
	h.add(receiver(em11Mouse(t, richImage(t, h.root))))
	out, errs, code := h.run("info")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "info-device", out)
}

func TestDoctorEmulated(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("--emulate", h.dump(), "doctor")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "doctor-emulated", out)
}

func TestDoctorDevice(t *testing.T) {
	h := newHarness(t)
	d := h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
	d.AddClient(emu.Client{PID: 409, Process: "karabiner_observer"})
	h.host.perm.App.Tmux = true
	out, errs, code := h.run("doctor")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "doctor-device", out)
}

func TestDoctorProblems(t *testing.T) {
	h := newHarness(t)
	d := h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
	comp := d.Chrome(0)
	t.Cleanup(comp.Close)
	h.host.console = platform.Console{SecureInput: platform.Process{PID: 200, Name: "loginwindow"}}
	out, errs, code := h.run("doctor")
	expect(t, code, cli.ExitBlocked, errs)
	golden(t, "doctor-problems", out+"--- stderr\n"+errs)
}

func TestDoctorScreenLockedIsOneProblem(t *testing.T) {
	h := newHarness(t)
	h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
	h.host.console = platform.Console{ScreenLocked: true, SecureInput: platform.Process{PID: 200, Name: "loginwindow"}}
	out, errs, code := h.run("doctor")
	expect(t, code, cli.ExitBlocked, errs)
	if !strings.Contains(out, "the screen is locked") || strings.Contains(out, "Secure Keyboard Entry") || !strings.Contains(errs, "1 problem(s)") {
		t.Errorf("doctor with the screen locked:\n%s--- stderr\n%s", out, errs)
	}
}

func TestBackupShowAndList(t *testing.T) {
	h := newHarness(t)
	h.add(receiver(em11Mouse(t, richImage(t, h.root))))
	out, errs, code := h.run("backup", "--label", "before H1", "--os", "mac")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "backup-saved", out)

	path := filepath.Join(h.paths.Backups, "em11-pro-260d-1282-7b04", "20260926T120000Z-before-h1.json")
	f, err := backup.Load(path)
	must(t, err)
	if f.Source != backup.SourceDevice || f.Full || f.Summary == nil || f.Summary.OS != "mac" || f.Device.Addr != "11:22:33" {
		t.Errorf("backup %+v", f)
	}
	im := f.Image()
	want := richImage(t, h.root)
	for _, e := range im.KnownExtents() {
		got, _ := im.Get(e)
		w, _ := want.Get(e)
		if !bytes.Equal(got, w) {
			t.Errorf("%v differs from the device", e)
		}
	}

	out, errs, code = h.run("--os", "mac", "show", path)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "show-backup", out)
	out, errs, code = h.run("--os", "win", "show", path)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "show-backup-win", out)

	_, errs, code = h.run("backup")
	expect(t, code, cli.ExitOK, errs)
	out, errs, code = h.run("backups")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "backups", out)
}

func TestFullBackup(t *testing.T) {
	h := newHarness(t)
	d := h.add(receiver(em11Mouse(t, richImage(t, h.root))))
	path := filepath.Join(h.tmp, "full.json")
	out, errs, code := h.run("backup", "--full", "-o", path)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "backup-full", out)
	f, err := backup.Load(path)
	must(t, err)
	if !f.Full || len(f.Missing()) != 0 || f.Known() != 6987+256 {
		t.Errorf("full %v, missing %v, known %d", f.Full, f.Missing(), f.Known())
	}
	if !bytes.Equal(f.Image().Bytes(), d.Image().Bytes()) {
		t.Error("the full backup differs from the device's flash")
	}
	if _, errs, code := h.run("backup", "-o", path); code != cli.ExitFailure || !strings.Contains(errs, "exists") {
		t.Errorf("a second backup to the same file: exit %d, %s", code, errs)
	}
}

func TestDiff(t *testing.T) {
	h := newHarness(t)
	a := richImage(t, h.root)
	b := changed(t, a)
	pa, pb := filepath.Join(h.tmp, "a.bin"), filepath.Join(h.tmp, "b.bin")
	end := mouse.AddrEndEeprom
	must(t, os.WriteFile(pa, a.Bytes()[:end], 0o600))
	must(t, os.WriteFile(pb, b.Bytes()[:end], 0o600))
	out, errs, code := h.run("--os", "mac", "diff", pa, pb)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "diff-files", out)

	h.add(receiver(em11Mouse(t, b)))
	out, errs, code = h.run("--os", "mac", "diff", pa)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "diff-device", out)

	out, errs, code = h.run("diff", pa, pa)
	expect(t, code, cli.ExitOK, errs)
	if !strings.Contains(out, "No differences.") {
		t.Errorf("a file against itself:\n%s", out)
	}
}

// changed edits a: DPI stage 2, the report rate, slot 3's function, shortcut
// 2, the macro in slot 3 and a byte of the extended block.
func changed(t *testing.T, a *flash.Image) *flash.Image {
	b := a.Clone()
	dpi, err := mouse.EncodeDPI(em11(t).Sensor, 1600)
	must(t, err)
	e, _ := mouse.DPIExtent(1)
	must(t, b.Set(e.Addr, dpi))
	rate, err := mouse.EncodeRate(1000)
	must(t, err)
	must(t, b.Set(mouse.AddrReportRate, rate[:]))
	fn, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamBack})
	must(t, err)
	e, _ = mouse.KeyFnExtent(3)
	must(t, b.Set(e.Addr, fn))
	sc, err := mouse.EncodeShortcut(keys.Combo{keys.LMeta.Stroke(), {Kind: keys.KindKey, Value: 0x19}})
	must(t, err)
	e, _ = mouse.ShortcutExtent(2)
	must(t, b.Set(e.Addr, sc))
	mb, err := mouse.EncodeMacro(mouse.Macro{Name: "bye", Events: []mouse.Event{
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x05}, Delay: 30},
		{Press: false, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x05}, Delay: 30},
	}})
	must(t, err)
	e, _ = mouse.MacroExtent(3)
	must(t, b.Set(e.Addr, mb))
	must(t, b.Set(6920, []byte{0x12, 0x34}))
	return b
}

func TestDump(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("--emulate", h.dump(), "dump", "--range", "0x60:0x80")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "dump-range", out)

	out, errs, code = h.run("--emulate", h.dump(), "dump", "--bin")
	expect(t, code, cli.ExitOK, errs)
	raw, err := os.ReadFile(h.dump())
	must(t, err)
	if out != string(raw) {
		t.Error("dump --bin differs from the flash dump")
	}

	path := filepath.Join(h.tmp, "range.bin")
	_, errs, code = h.run("--emulate", h.dump(), "dump", "--bin", "--range", "6912+8", "-o", path)
	expect(t, code, cli.ExitOK, errs)
	if b, _ := os.ReadFile(path); !bytes.Equal(b, bytes.Repeat([]byte{0xFF}, 8)) {
		t.Errorf("range 6912+8 = % x", b)
	}

	out, errs, code = h.run("--emulate", h.dump(), "dump", "--json")
	expect(t, code, cli.ExitOK, errs)
	var s backup.Summary
	must(t, json.Unmarshal([]byte(out), &s))
	if *s.Stages != 6 {
		t.Errorf("decoded %+v", s)
	}
}

func TestExportBin(t *testing.T) {
	h := newHarness(t)
	full := filepath.Join(h.tmp, "full.bin")
	must(t, os.WriteFile(full, richImage(t, h.root).Bytes()[:mouse.AddrEndEeprom], 0o600))
	path := filepath.Join(h.tmp, "x.bin")
	out, errs, code := h.run("export-bin", full, "-o", path)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "export-bin", out)
	got, err := os.ReadFile(path)
	must(t, err)
	im, err := flash.FromDump(0, richImage(t, h.root).Bytes()[:mouse.AddrEndEeprom])
	must(t, err)
	want, err := backup.ExportBin(em11(t), im)
	must(t, err)
	if !bytes.Equal(got, want) || len(got) != 16448 {
		t.Errorf("export is %d bytes and differs from backup.ExportBin", len(got))
	}
	out, errs, code = h.run("--os", "mac", "show", path)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "show-bin", out)
}

// A .bin made from an image that lacks bytes the web app's import writes
// would overwrite the mouse's bound bodies and virtual center with 0xFF.
func TestExportBinRefusesPartialImages(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.tmp, "x.bin")
	_, errs, code := h.run("export-bin", h.dump(), "-o", path)
	if code != cli.ExitFailure {
		t.Fatalf("exit %d, want %d:\n%s", code, cli.ExitFailure, errs)
	}
	golden(t, "export-bin-partial", errs)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the refused export was written: %v", err)
	}
	out, errs, code := h.run("export-bin", h.dump(), "-o", path, "--allow-partial")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "export-bin-allow-partial", out)
	got, err := os.ReadFile(path)
	must(t, err)
	want, err := backup.ExportPartialBin(em11(t), dumpImage(t, h.root))
	must(t, err)
	if !bytes.Equal(got, want) {
		t.Error("the partial export differs from backup.ExportPartialBin")
	}

	emulated := filepath.Join(h.tmp, "emu.json")
	_, errs, code = h.run("--emulate", h.dump(), "backup", "--full", "-o", emulated)
	expect(t, code, cli.ExitOK, errs)
	_, errs, code = h.run("export-bin", emulated, "-o", filepath.Join(h.tmp, "e.bin"), "--allow-partial")
	if code != cli.ExitFailure || !strings.Contains(errs, "emulator") {
		t.Errorf("export of an emulated backup: exit %d, %s", code, errs)
	}
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("version")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "version", out)
}

func TestHelp(t *testing.T) {
	h := newHarness(t)
	for name, args := range map[string][]string{
		"no-args":              nil,
		"help":                 {"help"},
		"help-dump":            {"help", "dump"},
		"help-flag":            {"dump", "-h"},
		"help-journal":         {"help", "journal"},
		"help-journal-recover": {"journal", "recover", "-h"},
	} {
		out, errs, code := h.run(args...)
		expect(t, code, cli.ExitOK, errs)
		switch name {
		case "help-flag":
			name = "help-dump"
		case "help-journal-recover":
			name = "help-journal"
		}
		// The hwtest build lists one more command.
		if hwtestBuild && (name == "no-args" || name == "help") {
			name += ".hwtest"
		}
		golden(t, name, out)
	}
}

func TestTraceSendsNothing(t *testing.T) {
	h := newHarness(t)
	d := h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
	go func() {
		time.Sleep(150 * time.Millisecond)
		d.Push(0x01, 0)
		d.Noise(2)
	}()
	out, errs, code := h.run("trace", "--for", "500ms")
	expect(t, code, cli.ExitOK, errs)
	if n := len(d.Writes()); n != 0 {
		t.Fatalf("trace wrote %d packets", n)
	}
	for _, want := range []string{
		"Listening on 2 interface(s) for 500ms; nothing is sent.",
		"if1  id 8   0a 00 00 00 0a 01 00",
		"status-changed",
		"if1  id 7 ",
		"if0: 0 report(s)",
		"if1: 3 report(s) (id 7: 2, id 8: 1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trace output lacks %q:\n%s", want, out)
		}
	}
}

func TestRecordAndReplay(t *testing.T) {
	h := newHarness(t)
	h.forReplay()
	rec := filepath.Join(h.tmp, "info.jsonl")
	live, errs, code := h.run("--emulate", h.dump(), "--device", "emu:1/IOUSBHostInterface@1", "--record", rec, "info")
	expect(t, code, cli.ExitOK, errs)
	replayed, errs, code := h.run("--replay", rec, "info")
	expect(t, code, cli.ExitOK, errs)
	if a, b := withoutDevice(live), withoutDevice(replayed); a != b {
		t.Errorf("the replay shows\n%s\nthe live run\n%s", b, a)
	}
	_, errs, code = h.run("--replay", rec, "backup", "--full", "-o", filepath.Join(h.tmp, "x.json"))
	if code != cli.ExitFailure || !strings.Contains(errs, "replay diverged") {
		t.Errorf("a full backup over a recorded info: exit %d, %s", code, errs)
	}
}

// A transcript for the repo goes through redact, and the copy still replays.
func TestRedact(t *testing.T) {
	h := newHarness(t)
	h.forReplay()
	rec := filepath.Join(h.tmp, "info.jsonl")
	live, errs, code := h.run("--emulate", h.dump(), "--device", "emu:1/IOUSBHostInterface@1", "--record", rec, "info")
	expect(t, code, cli.ExitOK, errs)
	red := filepath.Join(h.tmp, "redacted.jsonl")
	out, errs, code := h.run("redact", rec, "-o", red)
	expect(t, code, cli.ExitOK, errs)
	golden(t, "redact", out)
	b, err := os.ReadFile(red)
	must(t, err)
	hd, entries, err := hidio.ReadTranscript(bytes.NewReader(b))
	must(t, err)
	if !hd.Redacted {
		t.Error("the copy is not marked redacted")
	}
	for _, e := range entries {
		if again := hidio.Redact(e); !slices.Equal(again.Data.Bytes, e.Data.Bytes) || again.Data.Mask != e.Data.Mask {
			t.Errorf("entry %+v is not redacted", e)
		}
	}
	replayed, errs, code := h.run("--replay", red, "info")
	expect(t, code, cli.ExitOK, errs)
	if a, b := withoutDevice(live), withoutDevice(replayed); a != b {
		t.Errorf("the redacted replay shows\n%s\nthe live run\n%s", b, a)
	}
	if _, errs, code := h.run("redact", rec, "-o", red); code != cli.ExitFailure {
		t.Errorf("redact over an existing file: exit %d, %s", code, errs)
	}
}

// A bare --record name goes to the logs folder, where unredacted transcripts
// belong, rather than into whatever folder arcctl runs in.
func TestRecordBareNameGoesToTheLogs(t *testing.T) {
	h := newHarness(t)
	const name = "arcctl-test-record.jsonl"
	t.Cleanup(func() { os.Remove(name) })
	_, errs, code := h.run("--emulate", h.dump(), "--record", name, "info")
	expect(t, code, cli.ExitOK, errs)
	if _, err := os.Stat(filepath.Join(h.paths.Logs, name)); err != nil {
		t.Errorf("no transcript in the logs folder: %v", err)
	}
	if _, err := os.Stat(name); err == nil {
		t.Error("the transcript went into the working folder")
	}
	if want := "recording the HID traffic to $TMP/logs/" + name; !strings.Contains(errs, want) {
		t.Errorf("stderr lacks %q:\n%s", want, errs)
	}
}

// trace prints report-8 frames in full, but only the ID and length of other
// reports unless --raw asks for them: those carry what the user types.
func TestTraceHidesOtherInput(t *testing.T) {
	for _, raw := range []bool{false, true} {
		h := newHarness(t)
		d := h.add(receiver(em11Mouse(t, dumpImage(t, h.root))))
		go func() {
			time.Sleep(150 * time.Millisecond)
			d.Noise(1)
		}()
		args := []string{"trace", "--for", "400ms"}
		if raw {
			args = append(args, "--raw")
		}
		out, errs, code := h.run(args...)
		expect(t, code, cli.ExitOK, errs)
		data := strings.Contains(out, "00 01 00 ff ff 00 00")
		if data != raw || !strings.Contains(out, "if1  id 7 ") {
			t.Errorf("raw %v: trace output\n%s", raw, out)
		}
	}
}

func withoutDevice(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(l, "Device ") && !strings.HasPrefix(l, "Path ") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *harness)
		args  []string
		code  int
	}{
		{"no-receiver", nil, []string{"info"}, cli.ExitNoReceiver},
		{"no-answer", func(h *harness) {
			d := h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
			d.Inject(emu.Fault{Cmd: wire.CmdOnline, Action: emu.Drop})
		}, []string{"info"}, cli.ExitNoReceiver},
		{"asleep", func(h *harness) {
			m := em11Mouse(h.t, dumpImage(h.t, h.root))
			m.Asleep = true
			h.add(receiver(m))
		}, []string{"--wait", "200ms", "info"}, cli.ExitOffline},
		{"not-paired", func(h *harness) { h.add(emu.Config{}) }, []string{"--wait", "100ms", "dump"}, cli.ExitOffline},
		{"permission-denied", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
			h.host.perm.Access = platform.AccessDenied
		}, []string{"info"}, cli.ExitPermission},
		{"permission-refused", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root)))).SetDenied(true)
		}, []string{"backup"}, cli.ExitPermission},
		{"locked", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root)))).SetLocked(true)
		}, []string{"info"}, cli.ExitBlocked},
		{"seized", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root)))).SetSeized(true)
		}, []string{"info"}, cli.ExitBlocked},
		{"chrome", func(h *harness) {
			c := receiver(em11Mouse(h.t, dumpImage(h.t, h.root)))
			c.Latency.Mouse = 2 * time.Millisecond
			comp := h.add(c).Chrome(time.Millisecond)
			h.t.Cleanup(comp.Close)
		}, []string{"info"}, cli.ExitBlocked},
		{"stolen-replies", func(h *harness) {
			h.bus = emu.New(emu.Options{Seed: 1})
			h.host.bus = h.bus
			h.t.Cleanup(h.bus.Close)
			c := receiver(em11Mouse(h.t, dumpImage(h.t, h.root)))
			c.Behavior.Sharing = emu.Steal
			comp := h.add(c).Chrome(0)
			h.t.Cleanup(comp.Close)
		}, []string{"--wait", "2s", "info"}, cli.ExitBlocked},
		{"two-devices", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
		}, []string{"info"}, cli.ExitChoose},
		{"lock-held", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
			l, err := platform.AcquireLock(h.paths.Lock)
			must(h.t, err)
			h.t.Cleanup(func() { l.Release() })
		}, []string{"info"}, cli.ExitBlocked},
		{"unknown-model", func(h *harness) {
			h.add(receiver(&emu.Mouse{CID: 0x7B, MID: 9, Image: flash.New()}))
		}, []string{"dump"}, cli.ExitFailure},
		{"unknown-command", nil, []string{"frobnicate"}, cli.ExitUsage},
		{"unknown-flag", nil, []string{"info", "--frob"}, cli.ExitUsage},
		{"missing-argument", nil, []string{"show"}, cli.ExitUsage},
		{"extra-argument", nil, []string{"info", "now"}, cli.ExitUsage},
		{"emulate-and-replay", nil, []string{"--emulate", "a", "--replay", "b", "info"}, cli.ExitUsage},
		{"bad-range", nil, []string{"dump", "--range", "300:200"}, cli.ExitUsage},
		{"missing-file", nil, []string{"show", "/nonexistent/backup.json"}, cli.ExitFailure},
		{"emulated-backup-without-o", func(h *harness) {}, []string{"--emulate", "$DUMP", "backup"}, cli.ExitUsage},
		{"export-without-o", nil, []string{"export-bin", "$DUMP"}, cli.ExitUsage},
		{"journal-missing-subcommand", nil, []string{"journal"}, cli.ExitUsage},
		{"journal-unknown-subcommand", nil, []string{"journal", "undo"}, cli.ExitUsage},
		{"journal-two-choices", nil, []string{"journal", "recover", "--forward", "--back"}, cli.ExitUsage},
		{"journal-emulated", nil, []string{"--emulate", "$DUMP", "journal", "recover"}, cli.ExitUsage},
		{"journal-unknown-run", func(h *harness) {
			h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
		}, []string{"journal", "recover", "--run", "20260926T120000.000000000Z-1/1", "--forward"}, cli.ExitUsage},
		{"writes-time-out", func(h *harness) {
			d := h.add(receiver(em11Mouse(h.t, dumpImage(h.t, h.root))))
			d.Inject(emu.Fault{Cmd: wire.CmdOnline, Action: emu.Fail, Err: emu.IOError(0xE00002D6)})
		}, []string{"info"}, cli.ExitNoReceiver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.setup != nil {
				tt.setup(h)
			}
			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = strings.ReplaceAll(a, "$DUMP", h.dump())
			}
			out, errs, code := h.run(args...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.code, out, errs)
			}
			text := errs
			if out != "" {
				text = out + "--- stderr\n" + errs
			}
			golden(t, "exit-"+tt.name, text)
		})
	}
}

func TestUnknownModelInfo(t *testing.T) {
	h := newHarness(t)
	h.add(receiver(&emu.Mouse{CID: 0x7B, MID: 9, Image: flash.New()}))
	out, errs, code := h.run("info")
	expect(t, code, cli.ExitOK, errs)
	golden(t, "info-unknown-model", out)
}

func TestLogFile(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.tmp, "session.log")
	_, errs, code := h.run("--emulate", h.dump(), "--log", path, "info")
	expect(t, code, cli.ExitOK, errs)
	b, err := os.ReadFile(path)
	must(t, err)
	if !strings.Contains(string(b), "cmd=info") || !strings.Contains(string(b), "to=ready") {
		t.Errorf("log:\n%s", b)
	}
}
