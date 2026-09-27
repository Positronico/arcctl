//go:build hwtest

package hwtest

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// actor acts out the user of H9 on the emulator: at a chunk of the drill's
// write it locks the screen, puts the mouse to sleep, unplugs the receiver
// or presses the DPI button, once per drill.
type actor struct {
	r    *rig
	mu   sync.Mutex
	mode string
	done map[string]int
	skip map[string]int // tries of a drill in which the actor does nothing
}

func newActor(r *rig) *actor {
	a := &actor{r: r, done: map[string]int{}, skip: map[string]int{}}
	for _, m := range []string{"lock", "sleep", "unplug", "dpi"} {
		r.script.on("h9."+m, func() { a.set(m) })
	}
	r.script.on("h9.unplug-back", r.dev.Plug)
	r.cfg.hook = a.act
	r.cfg.pace = time.Millisecond
	return a
}

func (a *actor) set(m string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = m
}

func (a *actor) act(e safety.OpEvent) {
	if e.Kind != safety.EventChunk {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	area, _ := mouse.MacroExtent(scratchSlot)
	dpi, _ := mouse.DPIExtent(6)
	d := a.r.dev
	switch {
	case a.done[a.mode] > 0:
		return
	case a.mode == "dpi" && e.Op.Extent == dpi && e.Chunk == 1:
	case a.mode != "dpi" && e.Op.Extent.Addr == area.Addr && e.Chunk == 5:
	default:
		return
	}
	if a.skip[a.mode] > 0 {
		a.skip[a.mode]--
		a.mode = ""
		return
	}
	a.done[a.mode]++
	switch a.mode {
	case "lock":
		d.SetLocked(true)
		time.AfterFunc(300*time.Millisecond, func() { d.SetLocked(false) })
	case "sleep":
		d.Sleep()
		time.AfterFunc(300*time.Millisecond, d.Wake)
	case "unplug":
		d.Unplug()
	case "dpi":
		must(a.r.t, d.PressDPI())
	}
}

func TestH9DrillsEndOldOrNew(t *testing.T) {
	r := newRig(t, nil)
	a := newActor(r)
	r.script.yes("h9.run", "h9.buttons").set("h9.confirm", "write experimental")
	res := r.run("H9")
	r.passed(res)
	r.unchanged()
	r.checkWrites(0)
	for _, m := range []string{"lock", "sleep", "unplug", "dpi"} {
		if a.done[m] != 1 {
			t.Errorf("the %s drill acted %d times", m, a.done[m])
		}
	}
	joined := strings.Join(res.Findings, "\n")
	for _, f := range []string{
		"screen lock during a write: after chunk 5 of 39 of op 1 the write paused",
		"the mouse asleep during a write: after chunk 5 of 39 of op 1 the write paused",
		"the receiver unplugged during a write: the write stopped after chunk 5 of 39 of op 1",
		"and its recovery wrote the old bytes back",
		"the journal held the run open (6528+383 torn",
		"the DPI button during a DPI-stage write: the push of the DPI button re-read the current stage before its op; the run stopped before op 2 of 3",
		"4+2 torn",
	} {
		if !strings.Contains(joined, f) {
			t.Errorf("findings lack %q:\n%s", f, joined)
		}
	}
	s, stop := r.session()
	defer stop()
	sn := r.await(s, func(sn *session.Snapshot) bool {
		return sn.State == session.Ready && sn.Progress.Job == "" && sn.Journal != nil
	})
	if len(sn.Journal.Open) != 0 {
		t.Errorf("the journal holds %d open runs", len(sn.Journal.Open))
	}
	if len(res.Promoted) != 0 || r.generated != 0 {
		t.Error("H9 promoted")
	}
}

// A drill that does not happen in the middle of the write is tried again
// when the user says so; the records are put back after each try.
func TestH9TriesADrillAgain(t *testing.T) {
	r := newRig(t, nil)
	a := newActor(r)
	a.skip["lock"] = 1
	r.script.yes("h9.run", "h9.buttons", "h9.lock-again").set("h9.confirm", "write experimental")
	res := r.run("H9")
	r.passed(res)
	r.unchanged()
	if n := strings.Count(strings.Join(r.script.asked, " ")+" ", "h9.lock "); a.done["lock"] != 1 || n != 2 {
		t.Errorf("the lock drill acted %d times in %d tries", a.done["lock"], n)
	}
}

// When the user gives up a drill that did not happen, the stage fails but
// goes on with the others and leaves the mouse as it found it.
func TestH9GivingUpFailsTheStage(t *testing.T) {
	r := newRig(t, nil)
	a := newActor(r)
	a.skip["sleep"] = 1
	r.script.yes("h9.run", "h9.buttons").set("h9.sleep-again", false).set("h9.confirm", "write experimental")
	res := r.run("H9")
	if res.Passed || res.Err != nil {
		t.Fatalf("passed %v, err %v", res.Passed, res.Err)
	}
	r.unchanged()
	if a.done["unplug"] != 1 || a.done["dpi"] != 1 {
		t.Errorf("the drills after the sleep drill did not run: %v", a.done)
	}
}
