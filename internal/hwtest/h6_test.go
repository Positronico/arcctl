//go:build hwtest

package hwtest

import (
	"strings"
	"sync"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
)

func TestH6RestoresAndReadsEqual(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h6.run", "h6.buttons").set("h6.confirm", "write untested")
	res := r.run("H6")
	r.passed(res)
	r.unchanged()
	r.checkWrites(0)
	var titles []string
	for _, s := range res.Steps {
		titles = append(titles, s.Title)
	}
	joined := strings.Join(titles, "\n")
	for _, s := range []string{"restore --dry-run lists exactly the changed records", "restore the fresh backup",
		"the restore planned again writes nothing", "a full read equals the fresh backup"} {
		if !strings.Contains(joined, s) {
			t.Errorf("steps lack %q:\n%s", s, joined)
		}
	}
	if len(res.Backups) != 2 {
		t.Fatalf("backups %v", res.Backups)
	}
	after, err := backup.Load(res.Backups[1])
	must(t, err)
	if after.Label != "hwtest H6 after" || !after.Full {
		t.Errorf("the full read after: label %q, full %v", after.Label, after.Full)
	}
	if vs := r.verified(); len(vs) != 1 || vs[0].Feature != string(mouse.FeatureRestore) || vs[0].Stage != "H6" {
		t.Errorf("verified.json %+v", vs)
	}
	log := r.read(logPath)
	for _, s := range []string{"| H6 | restore round trip | passed 2026-09-27 (v0.42) |", "Current stage at 4+2: write", "Shortcut 4 at"} {
		if !strings.Contains(log, s) {
			t.Errorf("the log lacks %q", s)
		}
	}
	if strings.Contains(log, "Ctrl+F13") {
		t.Error("the log names the keys of a shortcut")
	}
	said := r.script.said.String()
	if !strings.Contains(said, "Before the stage Backward ran ") || !strings.Contains(said, "DPI Cycle ran ") {
		t.Errorf("the terminal does not say what the buttons did before the stage:\n%s", said)
	}
	if strings.Contains(log, "Before the stage") {
		t.Error("the log holds what the buttons did before the stage")
	}
}

// A record the mouse changes on its own between the changes and the
// restore makes the restore's dry run list more than the changed records:
// the stage stops before the restore writes, and puts the changes back.
func TestH6StopsWhenTheRestoreListsMore(t *testing.T) {
	r := newRig(t, nil)
	r.script.yes("h6.run", "h6.buttons").set("h6.confirm", "write untested")
	e8, _ := mouse.DPIExtent(7)
	rec, err := mouse.EncodeDPI(em11Model(t).Sensor, 1600)
	must(t, err)
	var once sync.Once
	r.cfg.hook = func(e safety.OpEvent) {
		if e.Kind == safety.EventVerified {
			once.Do(func() { must(t, r.dev.Store(e8.Addr, rec)) })
		}
	}
	res := r.run("H6")
	if res.Passed || res.Err == nil || !strings.Contains(res.Err.Error(), "DPI stage 8") {
		t.Fatalf("passed %v, err %v", res.Passed, res.Err)
	}
	if got := changedExtents(r.start, r.dev.Image()); len(got) != 1 || got[0] != e8 {
		t.Errorf("the stage left %v changed, want only %s", got, e8)
	}
}
