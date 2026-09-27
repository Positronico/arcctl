package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

// demoCombos are the shortcuts flash-dump.bin binds to slots 2 to 5, as the
// test vectors give them; the dump holds only the bindings.
func demoCombos(t *testing.T) map[int]keys.Combo {
	key := func(code string) keys.Stroke {
		k, ok := keys.ByCode(code)
		if !ok {
			t.Fatalf("no key %s", code)
		}
		return k.Stroke()
	}
	cmd, ctrl := keys.LMeta.Stroke(), keys.LCtrl.Stroke()
	return map[int]keys.Combo{
		2: {cmd, key("KeyV")},
		3: {ctrl, key("Tab")},
		4: {cmd, key("Tab")},
		5: {cmd, key("KeyC")},
	}
}

// TestDemoBackup rebuilds testdata/demo-em11.json, the emulator demo: the
// dump plus the shortcut bodies its bindings run, built with the mouse
// encoders and stored as an emulator backup. -update rewrites it.
func TestDemoBackup(t *testing.T) {
	h := newHarness(t)
	im := dumpImage(t, h.root)
	for slot, combo := range demoCombos(t) {
		body, err := mouse.EncodeShortcut(combo)
		must(t, err)
		ext, _ := mouse.ShortcutExtent(slot)
		must(t, im.Set(ext.Addr, append(body, bytes.Repeat([]byte{0xFF}, ext.Len-len(body))...)))
	}
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f, err := backup.New(session.Capture{
		Device:   plan.Identity{CID: 0x7B, MID: 0x04, VID: 0x260D, PID: 0x1282},
		Model:    em11(t),
		Image:    im,
		Started:  at,
		Finished: at,
	}, backup.Meta{Tool: "arcctl demo", Source: backup.SourceEmulator, Label: "demo", Created: at, OS: keys.Mac})
	must(t, err)
	var got bytes.Buffer
	must(t, f.Encode(&got))
	path := filepath.Join(h.root, "testdata", "demo-em11.json")
	if *update {
		must(t, os.WriteFile(path, got.Bytes(), 0o644))
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("%s is not what the encoders build; run the test with -update", path)
	}

	out, errs, code := h.run("--emulate", path, "--os", "mac", "dump", "--json")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, label := range []string{"Cmd+V", "Ctrl+Tab", "Cmd+Tab", "Cmd+C"} {
		if !strings.Contains(out, label) {
			t.Errorf("the emulated demo does not run %s:\n%s", label, out)
		}
	}
}

// TestTUINotesMissingBodies checks that a TUI over an emulated dump says
// which bound bodies the dump lacks, and that the demo backup lacks none.
func TestTUINotesMissingBodies(t *testing.T) {
	for _, tc := range []struct {
		file, want string
	}{
		{"flash-dump.bin", "flash-dump.bin holds no body for the shortcuts or macros bound to slots 2, 3, 4 and 5"},
		{"demo-em11.json", ""},
	} {
		t.Run(tc.file, func(t *testing.T) {
			h := newHarness(t)
			var out, errb bytes.Buffer
			env := h.env(&out, &errb)
			env.Terminal = true
			var got cli.TUI
			env.TUI = func(_ context.Context, t cli.TUI) error { got = t; return nil }
			if code := cli.Run(context.Background(), []string{"--emulate", filepath.Join(h.root, "testdata", tc.file)}, env); code != cli.ExitOK {
				t.Fatalf("exit %d: %s", code, errb.String())
			}
			errs := errb.String()
			for _, l := range strings.Split(errs, "\n") {
				if tmp, ok := strings.CutPrefix(l, "arcctl: the emulated mouse's journal and backups go to "); ok {
					os.RemoveAll(tmp)
				}
			}
			if got := strings.Contains(errs, "holds no body"); got != (tc.want != "") || !strings.Contains(errs, tc.want) {
				t.Errorf("stderr:\n%s", errs)
			}
			notes := strings.Join(got.Notes, "\n")
			if strings.Contains(notes, "holds no body") != (tc.want != "") || !strings.Contains(notes, tc.want) ||
				got.DataDir == "" || !strings.Contains(notes, got.DataDir) {
				t.Errorf("the TUI gets the notes %q and the data folder %q", got.Notes, got.DataDir)
			}
		})
	}
}
