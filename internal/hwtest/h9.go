//go:build hwtest

package hwtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// How slowly the H9 drills write, so the user has time to act while a
// record is half written: the executor waits this long after each chunk
// until the drill sees what it waits for. The sleep drill spreads its 39
// chunks over about six minutes, so the mouse falls asleep in the middle.
const (
	lockPace   = time.Second
	sleepPace  = 10 * time.Second
	unplugPace = time.Second
	dpiPace    = 5 * time.Second
	// drillTries is how often a drill is tried before it counts as not
	// observed.
	drillTries = 3
	// longChunks is the size of the H9 writes: a 70-event macro.
	longChunks = 39
)

func h9Reads() []flash.Extent {
	a, _ := mouse.MacroExtent(scratchSlot)
	return []flash.Extent{a}
}

// longMacro is a 70-event macro, 39 chunks: 35 letters typed.
func longMacro(name string) mouse.Macro {
	u := make([]uint16, mouse.MaxMacroEvents/2)
	for i := range u {
		u[i] = usageA + uint16(i%26)
	}
	return taps(name, 10, u...)
}

// drillKind is what an H9 drill does to a write.
type drillKind uint8

const (
	drillLock drillKind = iota + 1
	drillSleep
	drillUnplug
	drillDPI
)

// h9Drill is one H9 drill: its write, the plan that puts the records back
// (for the preview), and what the user is asked to do.
type h9Drill struct {
	kind    drillKind
	id      string
	title   string
	text    string
	pace    time.Duration
	changes []plan.Change
}

// H9: a 39-chunk write to the unbound slot-15 area during which the screen
// is locked, then one during which the mouse falls asleep, then one during
// which the receiver is unplugged; a DPI-stage write during which the DPI
// button is pressed. Each write is slowed down so the user can act in the
// middle of it. Every record must end old or new, the journal clean, and
// each drill puts its records back from the fresh backup.
func buildH9(b *builder) error {
	if err := b.unboundMacro(scratchSlot); err != nil {
		return err
	}
	long, err := b.macroChange(scratchSlot, longMacro("arcctl H9"), "slot-15 area: 70-event macro")
	if err != nil {
		return err
	}
	if n := (len(long.New) + 9) / 10; n != longChunks {
		return fmt.Errorf("H9 writes %d chunks, want %d", n, longChunks)
	}
	drills := []*h9Drill{
		{kind: drillLock, id: "lock", title: "screen lock during a write", pace: lockPace, changes: []plan.Change{long},
			text: fmt.Sprintf("When you press Enter, the stage writes a %d-chunk macro to the slot-15 area, slowly: about %s. "+
				"While it writes, lock the screen (Ctrl-Cmd-Q), wait five seconds, unlock it and come back here.", longChunks, time.Duration(longChunks)*lockPace)},
		{kind: drillSleep, id: "sleep", title: "the mouse asleep during a write", pace: sleepPace, changes: []plan.Change{long},
			text: fmt.Sprintf("When you press Enter, the stage writes the same macro very slowly: about %s. Leave the mouse untouched "+
				"so that it falls asleep in the middle; when the stage says the write paused, move the mouse to wake it.", time.Duration(longChunks)*sleepPace)},
		{kind: drillUnplug, id: "unplug", title: "the receiver unplugged during a write", pace: unplugPace, changes: []plan.Change{long},
			text: fmt.Sprintf("When you press Enter, the stage writes the same macro slowly: about %s. While it writes, unplug the "+
				"receiver; plug it back in when the stage asks.", time.Duration(longChunks)*unplugPace)},
	}
	for _, d := range drills {
		if err := b.h9Step(d); err != nil {
			return err
		}
	}
	if err := b.dpiSteps(); err != nil {
		return err
	}
	b.ask("buttons", "Press every button, turn the wheel and move the pointer. Does the mouse work as it did before the stage?", true)
	return nil
}

// dpiButton is the visible button the DPI drill has the user press: one
// that runs a DPI function now or, failing that, the one the catalog gives
// a DPI function by default, which then needs binding to it for the drill.
func (b *builder) dpiButton() (slot int, bind, ok bool) {
	for _, bindNow := range []bool{false, true} {
		for _, x := range b.m.Buttons {
			if !x.Visible || x.Slot < 2 {
				continue
			}
			e, _ := mouse.KeyFnExtent(x.Slot)
			cur, known := b.img.Get(e)
			switch {
			case !bindNow && known && mouse.KeyType(cur[0]) == mouse.TypeDPI:
				return x.Slot, false, true
			case bindNow && mouse.KeyType(x.Type) == mouse.TypeDPI:
				return x.Slot, true, true
			}
		}
	}
	return 0, false, false
}

// dpiSteps lay out the DPI drill on the button dpiButton finds, bound to
// DPI cycle before the drill and put back after it when it runs something
// else, since only a DPI function makes the mouse push its new stage.
// Without such a button the drill is skipped, with the reason recorded.
func (b *builder) dpiSteps() error {
	slot, bind, ok := b.dpiButton()
	if !ok {
		why := "no visible button runs a DPI function or has one by default, so no press can push a stage change"
		b.custom("the DPI button during a DPI-stage write", &custom{
			lines: []string{"Skipped: " + why + "."},
			run: func(ctx context.Context, r *runner, c *conn) error {
				r.finding("the DPI drill was skipped: %s", why)
				r.step("the DPI button during a DPI-stage write", true, "skipped: "+why)
				return nil
			},
		})
		return nil
	}
	label, action := b.button(slot)
	var bound int
	if bind {
		rec, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle})
		if err != nil {
			return err
		}
		e, _ := mouse.KeyFnExtent(slot)
		c, err := b.change(e, rec, fmt.Sprintf("slot %d: DPI cycle, for the DPI drill", slot), slot, mouse.FeatureSystem)
		if err != nil {
			return err
		}
		if bound, err = b.apply(fmt.Sprintf("%s -> DPI cycle for the DPI drill (it runs %s now)", label, action), c); err != nil {
			return err
		}
	}
	d, err := b.dpiDrill(label, bind)
	if err != nil {
		return err
	}
	if err := b.h9Step(d); err != nil {
		return err
	}
	if bind {
		return b.revert(fmt.Sprintf("revert: %s back to %s", label, action), bound)
	}
	return nil
}

// dpiDrill writes the DPI stages the mouse does not use, then the current
// stage one below it: a press of the DPI button pushes a re-read of the
// current stage, which the plan has yet to write, so the run stops.
func (b *builder) dpiDrill(label string, bound bool) (*h9Drill, error) {
	count, cur, err := stagesOf(b.img)
	if err != nil {
		return nil, err
	}
	if count < 2 {
		return nil, fmt.Errorf("H9's DPI drill changes the current stage, which needs two DPI stages; the mouse has %d", count)
	}
	var changes []plan.Change
	for stage := min(count, mouse.MaxStages-1); stage < mouse.MaxStages; stage++ {
		e, rec, dpi, err := b.dpiRecord(stage, 1000, 1100)
		if err != nil {
			return nil, err
		}
		c, err := b.change(e, rec, fmt.Sprintf("DPI stage %d: %d", stage+1, dpi), -1, mouse.FeatureDPI)
		if err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	to := (cur + count - 1) % count
	pr, err := mouse.EncodeCurrentStage(to)
	if err != nil {
		return nil, err
	}
	c, err := b.change(pairExtent(mouse.AddrCurrentDPI), pr[:], fmt.Sprintf("current stage: %d", to+1), -1, mouse.FeatureCurrent)
	if err != nil {
		return nil, err
	}
	changes = append(changes, c)
	window := time.Duration(len(changes)-1) * dpiPace
	which := "the " + label + " button"
	if bound {
		which += ", bound to DPI cycle for this drill,"
	}
	return &h9Drill{kind: drillDPI, id: "dpi", title: "the DPI button during a DPI-stage write", pace: dpiPace, changes: changes,
		text: fmt.Sprintf("When you press Enter, the stage writes %d DPI stages the mouse does not use, slowly, then its current stage: "+
			"about %s before the current stage. Press %s once while it writes.", len(changes)-1, window, which)}, nil
}

// h9Step lays out one drill: its write and the put-back, for the preview,
// and the run that repeats the drill until it is observed.
func (b *builder) h9Step(d *h9Drill) error {
	p, err := plan.New(b.dev, b.profile, b.img, b.layout, d.changes)
	if err != nil {
		return fmt.Errorf("%s: %w", d.title, err)
	}
	after, err := applyPlan(b.img, p)
	if err != nil {
		return err
	}
	var back []plan.Change
	var touched []flash.Extent
	for _, op := range p.Ops {
		touched = append(touched, op.Extent)
		back = append(back, plan.Change{Addr: op.Extent.Addr, New: op.Old, Desc: "put back: " + op.Desc, Tier: op.Tier})
	}
	bp, err := plan.New(b.dev, b.profile, after, b.layout, back)
	if err != nil {
		return fmt.Errorf("%s: putting it back: %w", d.title, err)
	}
	b.custom(d.title, &custom{
		plans:   []plan.Plan{p, bp},
		lines:   []string{d.text, "Afterwards the records go back to the fresh backup's bytes, as the second write below shows."},
		touched: touched,
		run: func(ctx context.Context, r *runner, c *conn) error {
			return r.runDrill9(ctx, c, d, touched)
		},
	})
	return nil
}

// runDrill9 runs a drill until it is observed, or until the user gives up
// after drillTries: the instruction, the slow write, what came of it, the
// journal recovery when the write stopped, and the records put back.
func (r *runner) runDrill9(ctx context.Context, c *conn, d *h9Drill, touched []flash.Extent) error {
	id := stageID(r.def.name) + "." + d.id
	for try := 1; ; try++ {
		text := d.text + " Press Enter to start."
		if try > 1 {
			text = "Again: " + text
		}
		if err := r.wait(id, text); err != nil {
			return err
		}
		seen, err := r.drillOnce(ctx, c, d, id)
		if err != nil {
			return err
		}
		if seen != "" {
			r.finding("%s: %s", d.title, seen)
			r.step(d.title, true, seen, "every record ended old or new, and the journal is clean")
		} else if try >= drillTries {
			r.step(d.title, false, fmt.Sprintf("not observed in %d tries", try))
		}
		if err := r.putBack(ctx, c, touched, r.fresh.Image(), "put back after "+d.title); err != nil {
			return err
		}
		if seen != "" || try >= drillTries {
			return nil
		}
		again, err := r.ask(id+"-again", "The stage did not see it happen during the write. Try again?", true)
		if err != nil {
			return err
		}
		if !again {
			r.step(d.title, false, fmt.Sprintf("not observed in %d tries; the user stopped", try))
			return nil
		}
	}
}

// drillOnce writes the drill's records once, slowly, and returns what it
// saw, or "" when the event did not come in the middle of a record. A
// write that stopped is recovered from the journal before it returns.
func (r *runner) drillOnce(ctx context.Context, c *conn, d *h9Drill, id string) (string, error) {
	p, err := r.planOn(ctx, c, d.freshChanges(c.s.Snapshot()))
	if err == nil {
		err = spares(p)
	}
	if err != nil {
		return "", err
	}
	tr := &trace{pace: r.hwPace(d.pace)}
	if d.kind == drillDPI {
		tr.pacing = func(e safety.OpEvent) bool { return e.Op.Extent != pairExtent(mouse.AddrCurrentDPI) }
	}
	out, err := c.s.Apply(ctx, p, r.gates(p.Ops), tr.on(ctx, r))
	st, isStop := stopped(err)
	if err != nil && !isStop {
		var pe *safety.PreflightError
		if !errors.As(err, &pe) {
			return "", err
		}
		r.note("the drill's write was refused: " + err.Error())
	}
	var seen string
	switch d.kind {
	case drillLock, drillSleep:
		if pz := tr.midRecord(d.kind == drillLock); pz != nil && err == nil && tr.restarts > 0 {
			seen = fmt.Sprintf("after chunk %d of %d of op %d the write paused (%v), resumed, wrote the record again from its first chunk and verified it",
				pz.after, pz.of, pz.op.Seq, pz.err)
		}
	case drillUnplug:
		if isStop && st.Written {
			seen = fmt.Sprintf("the write stopped after chunk %d of %d of op %d (%v)", tr.chunk, tr.chunks, st.Op.Seq, st.Err)
		}
	case drillDPI:
		if isStop && errors.Is(err, safety.ErrOverlap) {
			seen = fmt.Sprintf("the push of the DPI button re-read the current stage before its op; the run stopped before op %d of %d", st.Op.Seq, len(p.Ops))
		} else if out.Overlap {
			r.note("the DPI button came during the last op; the run finished and read it again")
		}
	}
	sn, werr := r.awaitDevice(ctx, c, id+"-back", backText[d.kind], r.cfg.Wait)
	if werr != nil {
		return "", werr
	}
	if seen != "" && openRun(sn, out.Run) == nil && isStop && out.Run != "" {
		if run, rerr := r.runStatus(sn, out.Run); rerr == nil && run.Resolved != 0 {
			seen += fmt.Sprintf("; the session settled the run %s without a write when it loaded again", run.Resolved)
		}
	}
	held, err := r.settleOpen(ctx, c, d.title)
	if err != nil {
		return "", err
	}
	if seen != "" && len(held) > 0 {
		seen += "; the journal held the run open (" + strings.Join(held, "; ") + ") and its recovery wrote the old bytes back"
	}
	return seen, nil
}

// backText asks for the device back after a drill, when it is not.
var backText = map[drillKind]string{
	drillLock:   "The mouse cannot be reached while the screen is locked. Unlock it, then press Enter.",
	drillSleep:  "The mouse sleeps. Move it to wake it, then press Enter.",
	drillUnplug: "The receiver is gone. Plug it back in, then press Enter.",
	drillDPI:    "The mouse does not answer. Move it to wake it, then press Enter.",
}

// freshChanges are the drill's changes for what the device holds now: the
// current stage a press of the DPI button moved is planned from where it
// is.
func (d *h9Drill) freshChanges(sn *session.Snapshot) []plan.Change {
	if d.kind != drillDPI || sn.Image == nil {
		return d.changes
	}
	count, cur, err := stagesOf(sn.Image)
	if err != nil {
		return d.changes
	}
	out := append([]plan.Change(nil), d.changes...)
	last := &out[len(out)-1]
	pr, err := mouse.EncodeCurrentStage((cur + count - 1) % count)
	if err == nil {
		last.New = pr[:]
		last.Desc = fmt.Sprintf("current stage: %d", (cur+count-1)%count+1)
	}
	return out
}

func (r *runner) hwPace(d time.Duration) time.Duration {
	if r.cfg.pace > 0 {
		return r.cfg.pace
	}
	return d
}
