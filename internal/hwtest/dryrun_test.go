//go:build hwtest

package hwtest

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

var previewPacket = regexp.MustCompile(`(?m)^ +(?:raw|cmd7) +((?:[0-9a-f]{2} ){15}[0-9a-f]{2})`)

// previewPackets lists the packets the preview printed, in order.
func previewPackets(said string) []string {
	var out []string
	for _, m := range previewPacket.FindAllStringSubmatch(said, -1) {
		out = append(out, m[1])
	}
	return out
}

// preview is the preview block of what the stage said.
func preview(t *testing.T, said string) string {
	t.Helper()
	start := strings.Index(said, "\nWhat the stage does:")
	end := strings.Index(said, "  dry-run preview: ok\n")
	if start < 0 || end < start {
		t.Fatalf("no preview in:\n%s", said)
	}
	return said[start:end]
}

// noFiles fails when dir holds a file, at any depth.
func noFiles(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("the dry run wrote %s", path)
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestDryRunPrintsH1AndWritesNothing(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Gates = safety.Gates{DryRun: true}
	log := r.read(logPath)
	res := r.run("H1")
	if res.Err != nil || !res.DryRun || res.Recorded || res.Passed {
		t.Fatalf("err %v, dry run %v, recorded %v, passed %v", res.Err, res.DryRun, res.Recorded, res.Passed)
	}
	said := r.script.said.String()
	want := []string{
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb",
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 ea",
		"07 00 00 04 02 02 53 00 00 00 00 00 00 00 00 eb",
		"07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb",
	}
	if got := previewPackets(said); !slices.Equal(got, want) {
		t.Errorf("preview packets\n got %v\nwant %v\n%s", got, want, said)
	}
	for _, s := range []string{"The stage itself needs --allow-untested.", "Dry run: no backup was taken and nothing was written."} {
		if !strings.Contains(said, s) {
			t.Errorf("the dry run did not say %q:\n%s", s, said)
		}
	}
	if len(r.script.asked) != 0 {
		t.Errorf("the dry run asked %v", r.script.asked)
	}

	if n := len(r.cmd7()); n != 0 {
		t.Errorf("%d cmd-7 packets reached the device", n)
	}
	for _, w := range r.dev.Writes() {
		if err := wire.ReadOnly.Check(w.Packet, wire.Mouse); err != nil {
			t.Errorf("packet %v reached the device: %v", w.Packet, err)
		}
	}
	r.unchanged()

	if r.read(logPath) != log || r.read(verifiedPath) != "[]\n" || r.generated != 0 {
		t.Error("the dry run changed the records")
	}
	for _, dir := range []string{filepath.Join(r.repo, filepath.FromSlash(transcriptsDir)), r.cfg.Logs, r.cfg.Backups,
		r.cfg.Session.Writes.Backups.(backup.Store).Root, r.cfg.Session.Writes.Journal} {
		noFiles(t, dir)
	}
}

// A dry run shows the same preview a run shows before it asks to go on.
func TestDryRunPreviewMatchesTheRun(t *testing.T) {
	for _, stage := range []string{"H1", "H2", "H3", "H3b"} {
		t.Run(stage, func(t *testing.T) {
			dry := newRig(t, nil)
			dry.cfg.Gates.DryRun = true
			if res := dry.run(stage); res.Err != nil {
				t.Fatalf("dry run: %v", res.Err)
			}
			if n := len(dry.cmd7()); n != 0 {
				t.Errorf("the dry run sent %d cmd-7 packets", n)
			}

			run := newRig(t, nil)
			run.script.set(stageID(stage)+".run", false)
			if res := run.run(stage); !errors.Is(res.Err, ErrDeclined) {
				t.Fatalf("declined run: %v", res.Err)
			}
			got, want := preview(t, dry.script.said.String()), preview(t, run.script.said.String())
			if got != want {
				t.Errorf("dry run preview:\n%s\nrun preview:\n%s", got, want)
			}
		})
	}
}

func TestH0HasNoDryRun(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Gates.DryRun = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, r.cfg, "H0")
	if !errors.Is(err, ErrNoDryRun) || res != nil {
		t.Fatalf("result %v, err %v", res, err)
	}
	if n := len(r.dev.Writes()); n != 0 {
		t.Errorf("%d packets reached the device", n)
	}
}
