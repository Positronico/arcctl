//go:build hwtest

package hwtest

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

type exitCode struct{ code int }

// h5Script answers every question of H5 the way the plan expects.
func h5Script(r *rig) {
	r.script.yes("h5.run", "h5.cycle1", "h5.restored").set("h5.confirm", "write experimental")
	for _, id := range []string{"h5.cycle253-repeat", "h5.cycle253-stop", "h5.cycle254-held", "h5.cycle254-stop", "h5.cycle255-repeat", "h5.cycle255-stop"} {
		r.script.set(id, true)
	}
}

// crashAt makes the debug abort end the stage as the process would end:
// the tests' exit panics, and the session takes the panic as a crash.
func crashAt(r *rig, n int) *int {
	code := new(int)
	r.cfg.AbortAfterChunk = n
	r.cfg.Exit = func(c int) {
		*code = c
		panic(exitCode{c})
	}
	return code
}

// H5 end to end: the macro writes, the drill that ends the first run after
// chunk 2 of 11, the second run that recovers the torn record from the
// journal and finishes the stage, and one log entry for both runs.
func TestH5DrillResumesAndPromotes(t *testing.T) {
	r := newRig(t, nil)
	h5Script(r)
	code := crashAt(r, 2)
	area, _ := mouse.MacroExtent(scratchSlot)
	drill := flash.Extent{Addr: area.Addr, Len: 103}

	first := r.run("H5")
	if *code != abortCode || !errors.Is(first.Err, ErrEnded) || first.Recorded || first.Passed {
		t.Fatalf("first run: exit %d, err %v, recorded %v", *code, first.Err, first.Recorded)
	}
	if _, err := os.Stat(checkpointPath(r.cfg.Logs, "H5")); err != nil {
		t.Fatalf("no checkpoint: %v", err)
	}
	torn, _ := r.dev.Image().Get(drill)
	was, _ := r.start.Get(drill)
	body, err := mouse.EncodeMacro(h5Drill)
	must(t, err)
	if !bytes.Equal(torn[:20], body[:20]) || !bytes.Equal(torn[20:], was[20:]) {
		t.Fatalf("after the abort the drill's record holds % x", torn)
	}
	if strings.Contains(r.read(logPath), "### 2026-09-27 H5") {
		t.Error("the first run is in the log")
	}
	res := r.run("H5")
	r.passed(res)
	r.unchanged()
	if _, err := os.Stat(checkpointPath(r.cfg.Logs, "H5")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the checkpoint is still there: %v", err)
	}
	joined := strings.Join(res.Findings, "\n")
	for _, f := range []string{"after the restart the journal holds the drill's run", drill.String() + " torn (", "repeat 253 (until pressed again): repeats after one press: yes"} {
		if !strings.Contains(joined, f) {
			t.Errorf("findings lack %q:\n%s", f, joined)
		}
	}
	if len(res.Transcripts) != 2 || !strings.HasSuffix(res.Transcripts[0], "-h5.jsonl") || !strings.HasSuffix(res.Transcripts[1], "-h5-2.jsonl") {
		t.Errorf("transcripts %v", res.Transcripts)
	}
	r.checkRedacted(res)
	if vs := r.verified(); len(vs) != 1 || vs[0].Feature != string(mouse.FeatureMacro) || vs[0].Stage != "H5" {
		t.Errorf("verified.json %+v", vs)
	}
	log := r.read(logPath)
	if strings.Count(log, "### 2026-09-27 H5") != 1 || !strings.Contains(log, "| H5 | macros and journal recovery | passed 2026-09-27 (v0.42) |") {
		t.Errorf("the log:\n%s", log)
	}

	second := 0
	for _, p := range r.logicalCmd7() {
		if int(p.Addr()) == drill.Addr+wire.MaxData && bytes.Equal(p.Data(), body[wire.MaxData:2*wire.MaxData]) {
			second++
		}
	}
	if second != 1 {
		t.Errorf("the drill's second chunk went out %d times, want once", second)
	}
	r.checkWrites(0)
	for _, id := range []string{"h5.editor", "h5.cycle1", "h5.restored"} {
		if !slices.Contains(r.script.asked, id) {
			t.Errorf("%s was never asked", id)
		}
	}
	for _, c := range []string{"253", "254", "255"} {
		try := slices.Index(r.script.asked, "h5.cycle"+c+"-try")
		first := slices.IndexFunc(r.script.asked, func(id string) bool { return strings.HasPrefix(id, "h5.cycle"+c+"-") && !strings.HasSuffix(id, "-try") })
		if try < 0 || first < try {
			t.Errorf("repeat %s: a question comes before the user stopped the macro: %v", c, r.script.asked)
		}
	}
}

// Without --debug-abort-after-chunk, H5 stops before its first write.
func TestH5NeedsTheAbortFlag(t *testing.T) {
	r := newRig(t, nil)
	h5Script(r)
	res := r.run("H5")
	if !errors.Is(res.Err, ErrFlags) || !strings.Contains(res.Err.Error(), "--debug-abort-after-chunk") || len(r.cmd7()) != 0 {
		t.Fatalf("err %v, %d cmd 7", res.Err, len(r.cmd7()))
	}
}

// A checkpoint whose run the journal no longer holds open, because the
// user settled it meanwhile, is reported, and the stage goes on.
func TestH5ResumesAfterJournalRecover(t *testing.T) {
	r := newRig(t, nil)
	h5Script(r)
	crashAt(r, 2)
	r.run("H5")
	cp, err := loadCheckpoint(r.cfg.Logs, "H5")
	must(t, err)
	s, stop := r.session()
	defer stop()
	r.await(s, func(sn *session.Snapshot) bool {
		return sn.State == session.Recovering && sn.Progress.Job == "" && sn.Journal != nil && len(sn.Journal.Open) == 1
	})
	if _, err := s.Recover(t.Context(), cp.Run, safety.Back, safety.Gates{}, nil); err != nil {
		t.Fatal(err)
	}
	stop()
	res := r.run("H5")
	r.passed(res)
	r.unchanged()
	if !strings.Contains(strings.Join(res.Findings, "\n"), "was settled before the stage resumed: back") {
		t.Errorf("findings %v", res.Findings)
	}
}

// A dry run while the drill waits for its recovery refuses to run.
func TestH5DryRunWaitsForTheRecovery(t *testing.T) {
	r := newRig(t, nil)
	h5Script(r)
	crashAt(r, 2)
	r.run("H5")
	r.cfg.Gates.DryRun = true
	if _, err := Run(t.Context(), r.cfg, "H5"); !errors.Is(err, ErrResume) {
		t.Fatalf("dry run: %v", err)
	}
}

// A rehearsal's drill cannot end the process: the emulated mouse would go
// with it. The stage takes the abort as a crash and resumes in the same
// process, as its next run would.
func TestH5RehearsalResumesInProcess(t *testing.T) {
	r := newRig(t, nil)
	h5Script(r)
	r.cfg.Source = backup.SourceEmulator
	r.cfg.AbortAfterChunk = 2
	res := r.run("H5")
	if !res.Passed || !res.Rehearsal || len(res.Promoted) != 0 {
		t.Fatalf("passed %v, rehearsal %v, promoted %v, err %v", res.Passed, res.Rehearsal, res.Promoted, res.Err)
	}
	r.unchanged()
	if !strings.Contains(r.script.said.String(), "the stage resumes here") || len(res.Transcripts) != 2 {
		t.Errorf("transcripts %v", res.Transcripts)
	}
}
