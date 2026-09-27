//go:build hwtest

package hwtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
)

func TestPromoteMergesAndRegenerates(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, filepath.FromSlash(verifiedPath))
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	old := `[
  {"model": "7B04", "feature": "dpi.value", "firmware": "v1.05", "stage": "H2", "date": "2026-09-20"}
]
`
	must(t, os.WriteFile(path, []byte(old), 0o644))
	calls := 0
	gen := func(context.Context, string) error { calls++; return nil }
	add := []catalog.Verification{
		{Model: "7B04", Feature: "dpi.value", Firmware: "v1.05", Stage: "H2", Date: "2026-09-27"},
		{Model: "7B04", Feature: "dpi.current", Firmware: "v1.05", Stage: "H1", Date: "2026-09-27"},
	}
	got, err := promote(context.Background(), repo, add, gen)
	must(t, err)
	if len(got) != 1 || got[0].Feature != "dpi.current" || calls != 1 {
		t.Fatalf("promoted %v, generated %d times", got, calls)
	}
	b, err := os.ReadFile(path)
	must(t, err)
	want := `[
  {
    "model": "7B04",
    "feature": "dpi.current",
    "firmware": "v1.05",
    "stage": "H1",
    "date": "2026-09-27"
  },
  {
    "model": "7B04",
    "feature": "dpi.value",
    "firmware": "v1.05",
    "stage": "H2",
    "date": "2026-09-20"
  }
]
`
	if string(b) != want {
		t.Errorf("verified.json:\n%s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode())
	}
	if got, err := promote(context.Background(), repo, add, gen); err != nil || got != nil || calls != 1 {
		t.Errorf("promoting again: %v, %v, generated %d times", got, err, calls)
	}
}

// A generator that fails leaves verified.json as it was, so the file and
// the compiled table never disagree.
func TestPromoteRollsBack(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, filepath.FromSlash(verifiedPath))
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte("[]\n"), 0o644))
	boom := errors.New("no go toolchain")
	_, err := promote(context.Background(), repo, []catalog.Verification{{Model: "7B04", Feature: "button.system", Firmware: "v1.05", Stage: "H3", Date: "2026-09-27"}},
		func(context.Context, string) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "[]\n" {
		t.Errorf("verified.json after the failure: %s", b)
	}
}

func TestLogEntryHoldsNothingPrivate(t *testing.T) {
	res := &Result{
		Stage: "H1", Title: "settings pairs, echo and NAK behaviour", Passed: true, Started: testTime, Ended: testTime,
		Device:      Device{Model: "ProtoArc EM11 Pro", Key: em11, MID: 4, Mouse: "v1.05", Receiver: "v1.02", Conn: "wireless 1 kHz", Profile: "none"},
		Steps:       []StepResult{{Title: "fresh full backup", OK: true, Detail: []string{"7243 bytes"}}},
		Answers:     []Answer{{ID: "h1.speed", Question: "Did\nit change?", Answer: "yes", OK: true}},
		Findings:    []string{"cmd 7 at 4: one reply"},
		Promoted:    []catalog.Verification{{Model: em11, Feature: "dpi.current", Firmware: "v1.05"}},
		Transcripts: []string{"testdata/transcripts/2026-09-27-h1.jsonl"},
		Backups:     []string{"/Users/someone/Library/Application Support/arcctl/backups/x.json"},
	}
	e := entry(res)
	for _, s := range []string{"### 2026-09-27 H1 (settings pairs, echo and NAK behaviour): passed", "- Device: ProtoArc EM11 Pro (7B04, mid 4); mouse firmware v1.05, receiver v1.02; wireless 1 kHz; profile none",
		"  1. fresh full backup: ok", "  - Did it change? yes", "- Promoted: `dpi.current` for 7B04 on v1.05"} {
		if !strings.Contains(e, s) {
			t.Errorf("entry lacks %q:\n%s", s, e)
		}
	}
	if strings.Contains(e, "/Users/") {
		t.Error("the entry names a local path")
	}
	res.Err = errors.New(`the backup at /Users/someone/x.json (IOService:/x/XHC@00000000/y) failed, see C:\logs\a.txt; ok`)
	if e := entry(res); strings.Contains(e, "/Users") || strings.Contains(e, "IOService") || strings.Contains(e, `C:\`) || !strings.Contains(e, "the backup at <path> (<path>) failed, see <path>; ok") {
		t.Errorf("the stop reason is not scrubbed:\n%s", e)
	}
	doc := "| Stage | Covers | Status |\n|---|---|---|\n| H1 | settings pairs | not run |\n| H10 | x | not run |"
	got := setStatus(doc, res.Stage, status(doc, res, nil))
	if !strings.Contains(got, "| H1 | settings pairs | passed 2026-09-27 (v1.05) |") || !strings.Contains(got, "| H10 | x | not run |") {
		t.Errorf("status:\n%s", got)
	}
}
