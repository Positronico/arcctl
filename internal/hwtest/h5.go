//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// scratchSlot is the slot whose macro area H5 and H9 write: no control uses
// slot 15 on the EM11 Pro, and its binding must not run it.
const scratchSlot = 15

// bindSlot is the button H5 binds its macros to: Forward.
const bindSlot = 4

// drillChunks is the size of H5's torn write: 11 chunks of 10 bytes.
const drillChunks = 11

// h5Cycles are the repeat modes H5 binds, in the order of the plan.
var h5Cycles = []int{1, 253, 254, 255}

func h5Reads() []flash.Extent {
	a, _ := mouse.MacroExtent(scratchSlot)
	b, _ := mouse.MacroExtent(bindSlot)
	return []flash.Extent{a, b}
}

// taps is a macro that types keys, each pressed and released, delay ms
// after each event.
func taps(name string, delay uint16, usages ...uint16) mouse.Macro {
	m := mouse.Macro{Name: name}
	for _, u := range usages {
		s := keys.Stroke{Kind: keys.KindKey, Value: u}
		m.Events = append(m.Events, mouse.Event{Press: true, Stroke: s, Delay: delay}, mouse.Event{Stroke: s, Delay: delay})
	}
	return m
}

const (
	usageA = 0x04
	usageH = 0x0B
	usage5 = 0x22
)

// The H5 macros. The three-event one ends with a second release of its key,
// which does nothing; it is never bound.
var (
	h5Short = mouse.Macro{Name: "arcctl H5 short", Events: []mouse.Event{
		{Press: true, Stroke: keys.Stroke{Kind: keys.KindKey, Value: usageA}, Delay: 20},
		{Stroke: keys.Stroke{Kind: keys.KindKey, Value: usageA}, Delay: 20},
		{Stroke: keys.Stroke{Kind: keys.KindKey, Value: usageA}, Delay: 20},
	}}
	h5Drill = taps("arcctl H5 drill", 10, usageA, usageH, usage5, usageA, usageH, usage5, usageA)
	h5Bind  = taps("arcctl H5", 150, usageH, usage5)
)

// H5: a three-event macro into the unbound slot-15 area, read back and
// reverted; the torn-write drill there; a short macro bound to Forward with
// each repeat mode, observed and reverted.
func buildH5(b *builder) error {
	if err := b.unboundMacro(scratchSlot); err != nil {
		return err
	}
	area, _ := mouse.MacroExtent(scratchSlot)
	short, err := b.macroChange(scratchSlot, h5Short, "slot-15 area: three-event macro")
	if err != nil {
		return err
	}
	i, err := b.apply("three-event macro into the unbound slot-15 area", short)
	if err != nil {
		return err
	}
	if err := b.check("the slot-15 area reads back as written", flash.Extent{Addr: area.Addr, Len: len(short.New)}); err != nil {
		return err
	}
	if err := b.revert("revert: the slot-15 area back", i); err != nil {
		return err
	}
	if err := b.drill(); err != nil {
		return err
	}

	label, action := b.button(bindSlot)
	b.wait("editor", "Open an empty text document (TextEdit will do) and click into it: the next steps type into it with the "+
		label+" button. Come back here to answer. Press Enter when it is ready.")
	for _, cycle := range h5Cycles {
		p, err := mouse.PlanEdits(b.m, b.img, []mouse.Edit{mouse.SetMacro{Slot: bindSlot, Macro: h5Bind, Cycle: cycle}}, b.opt)
		if err != nil {
			return fmt.Errorf("binding the macro with cycle %d: %w", cycle, err)
		}
		i, err := b.applied(fmt.Sprintf("%s -> macro, repeat %s", label, backup.Repeat(byte(cycle))), p)
		if err != nil {
			return err
		}
		b.cycleQuestions(label, cycle)
		if err := b.revert(fmt.Sprintf("revert: %s back to %s", label, action), i); err != nil {
			return err
		}
	}
	b.ask("restored", fmt.Sprintf("Press the %s button once more. Does it do what it did before the stage (%s), and has the typing stopped?", label, action), true)
	return nil
}

// cycleQuestions have the user try the bound macro in the repeat mode and
// stop it, then ask what it did, so no answer is typed while it may still
// type; each answer is recorded as what the mode does, next to its label.
func (b *builder) cycleQuestions(label string, cycle int) {
	id := fmt.Sprintf("cycle%d", cycle)
	mode := fmt.Sprintf("repeat %d (%s): ", cycle, backup.Repeat(byte(cycle)))
	stop := "If the typing does not stop, switch the mouse off and on again (or unplug the receiver and plug it back in). " +
		"Then come back here and press Enter."
	switch cycle {
	case 1:
		b.wait(id+"-try", fmt.Sprintf("Click into the document and press and release %s once. %s", label, stop))
		b.ask(id, fmt.Sprintf("Did %s type h5 exactly once?", label), true)
	case 253:
		b.wait(id+"-try", fmt.Sprintf("Click into the document, press and release %s once and watch for three seconds, then press %s "+
			"again and watch whether the typing stops. %s", label, label, stop))
		b.observe(id+"-repeat", "Did it keep typing h5 after the first press?", mode+"repeats after one press")
		b.observe(id+"-stop", fmt.Sprintf("Did the second press of %s stop it?", label), mode+"a second press stops it")
	case 254:
		b.wait(id+"-try", fmt.Sprintf("Click into the document, press and hold %s for two seconds, then let go and watch for three "+
			"seconds. %s", label, stop))
		b.observe(id+"-held", "Did it type h5 again and again while you held the button?", mode+"repeats while held")
		b.observe(id+"-stop", "Did the typing stop when you let go?", mode+"letting go stops it")
	case 255:
		b.wait(id+"-try", fmt.Sprintf("Click into the document, press and release %s once and watch for three seconds, then press "+
			"Shift on the keyboard and watch whether the typing stops. %s", label, stop))
		b.observe(id+"-repeat", "Did it keep typing h5 after one press?", mode+"repeats after one press")
		b.observe(id+"-stop", "Did pressing Shift stop it?", mode+"a key press stops it")
	}
}

// unboundMacro fails when a binding runs the macro of slot.
func (b *builder) unboundMacro(slot int) error {
	for k := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(k)
		cur, err := b.current(e)
		if err != nil {
			if errors.Is(err, errPrimary) {
				cur, _ = b.img.Get(e)
			} else {
				return err
			}
		}
		if mouse.KeyType(cur[0]) == mouse.TypeMacro && (k == slot || int(cur[1]) == slot) {
			return fmt.Errorf("%s: binding %d runs the macro of slot %d, which the stage needs unbound", b.stage, k, slot)
		}
	}
	return nil
}

// macroChange encodes m into the macro area of slot, whose name no other
// macro on the device may carry with other events.
func (b *builder) macroChange(slot int, m mouse.Macro, desc string) (plan.Change, error) {
	body, err := mouse.EncodeMacro(m)
	if err != nil {
		return plan.Change{}, err
	}
	cfg := mouse.Decode(b.m, b.img)
	for k, o := range cfg.Macros {
		if k != slot && o != nil && o.Name == m.Name && !slices.Equal(o.Events, m.Events) {
			return plan.Change{}, fmt.Errorf("%s: slot %d already holds another macro named %q", b.stage, k, m.Name)
		}
	}
	e, _ := mouse.MacroExtent(slot)
	if _, err := b.current(flash.Extent{Addr: e.Addr, Len: len(body)}); err != nil {
		return plan.Change{}, err
	}
	return b.change(e, body, desc, slot, mouse.FeatureMacro)
}

// applied adds a plan the release planner made, as an apply step.
func (b *builder) applied(title string, p plan.Plan) (int, error) {
	if len(p.Ops) == 0 {
		return 0, fmt.Errorf("%s: the device already holds these bytes", title)
	}
	next := b.img.Clone()
	for _, op := range p.Ops {
		if touchesPrimary(op.Extent) {
			return 0, fmt.Errorf("%w: op %d writes %s", errPrimary, op.Seq, op.Extent)
		}
		if err := next.Set(op.Extent.Addr, op.New); err != nil {
			return 0, err
		}
	}
	b.img = next
	return b.add(step{kind: stepApply, title: title, plan: p}), nil
}

// drillState is the torn-write drill while it writes.
type drillState struct {
	extent flash.Extent
	plan   plan.Plan
	err    error
}

// drill lays out H5's torn-write drill: an 11-chunk macro into the slot-15
// area, which --debug-abort-after-chunk ends after chunk n as a crash would.
// The next run of the stage recovers the journal and goes on. The preview
// shows the write, then the recovery that writes the old bytes back.
func (b *builder) drill() error {
	c, err := b.macroChange(scratchSlot, h5Drill, "slot-15 area: drill macro")
	if err != nil {
		return err
	}
	p, err := plan.New(b.dev, b.profile, b.img, b.layout, []plan.Change{c})
	if err != nil {
		return err
	}
	if len(p.Ops) != 1 || chunksOf(p.Ops[0]) != drillChunks {
		return fmt.Errorf("%s: the drill writes %d ops, want one op of %d chunks", b.stage, len(p.Ops), drillChunks)
	}
	op := p.Ops[0]
	after := b.img.Clone()
	if err := after.Set(op.Extent.Addr, op.New); err != nil {
		return err
	}
	back, err := plan.New(b.dev, b.profile, after, b.layout, []plan.Change{{Addr: op.Extent.Addr, New: op.Old, Desc: "roll back: " + op.Desc, Tier: op.Tier}})
	if err != nil {
		return fmt.Errorf("%s: the drill's record could not be rolled back: %w", b.stage, err)
	}
	st := &drillState{extent: op.Extent, plan: p}
	b.custom(fmt.Sprintf("torn-write drill: an %d-chunk macro into the slot-15 area, ended after chunk n", drillChunks), &custom{
		abort: true,
		plans: []plan.Plan{p, back},
		lines: []string{
			"--debug-abort-after-chunk n ends the process after chunk n of this write, as a crash would.",
			"Run the same command again: the session finds the torn record in the journal, the stage writes",
			"the old bytes back (the second write below), checks them, and goes on with the steps after it.",
		},
		touched: []flash.Extent{op.Extent},
		run: func(ctx context.Context, r *runner, c *conn) error {
			return r.runDrill(ctx, c, st)
		},
	})
	return nil
}

func chunksOf(op plan.Op) int { return (op.Extent.Len + wire.MaxData - 1) / wire.MaxData }

// runDrill writes the drill's macro; the event handler saves the checkpoint
// and ends the process after chunk n.
func (r *runner) runDrill(ctx context.Context, c *conn, st *drillState) error {
	r.drill = st
	defer func() { r.drill = nil }()
	title := fmt.Sprintf("torn-write drill: write of %d chunks ended after chunk %d", drillChunks, r.cfg.AbortAfterChunk)
	if err := spares(st.plan); err != nil {
		r.step(title, false, err.Error())
		return err
	}
	out, err := c.s.Apply(ctx, st.plan, r.gates(st.plan.Ops), r.event)
	switch {
	case r.ended:
		return ErrEnded
	case st.err != nil:
		err = st.err
	case err == nil:
		err = fmt.Errorf("the write finished without the debug abort after chunk %d", r.cfg.AbortAfterChunk)
	}
	r.step(title, false, fmt.Sprintf("run %q", out.Run), err.Error())
	return err
}

// abortsAt reports whether the debug abort ends the stage at e: in a stage
// with a torn-write drill only the drill's own write, in any other every
// write.
func (r *runner) abortsAt(e safety.OpEvent) bool {
	if !r.drills {
		return true
	}
	return r.drill != nil && e.Op.Extent == r.drill.extent
}

// hasDrill reports whether steps hold a torn-write drill.
func hasDrill(steps []step) bool {
	return slices.ContainsFunc(steps, func(s step) bool { return s.kind == stepCustom && s.custom.abort })
}

// checkpoint is what a stage leaves for its next run, in the logs folder,
// when it may stop past a point it must never repeat: H5's torn-write
// drill, which ends the process, and H7's reset, which is never sent twice.
// It holds enough to lay the same steps out again and go on after that
// point.
type checkpoint struct {
	Kind       string       `json:"kind"`
	Stage      string       `json:"stage"`
	Started    time.Time    `json:"started"`
	Transcript string       `json:"transcript"`
	Backup     string       `json:"backup"`
	Step       int          `json:"step"`
	Run        string       `json:"run"`
	Chunk      int          `json:"chunk"`
	Plan       string       `json:"plan"`
	Device     Device       `json:"device"`
	Steps      []StepResult `json:"steps"`
	Answers    []Answer     `json:"answers"`
	Findings   []string     `json:"findings"`
	Backups    []string     `json:"backups"`
	// LongRange is H7's cmd-23 reply from before the reset; nil when none
	// came. Recorded says that the run that left the checkpoint recorded
	// itself, its transcript included.
	LongRange []byte `json:"long_range,omitempty"`
	Recorded  bool   `json:"recorded"`
}

// The kinds of checkpoint.
const (
	checkpointDrill = "drill"
	checkpointReset = "reset"
)

func checkpointPath(logs, stage string) string {
	return filepath.Join(logs, "hwtest-"+strings.ToLower(stage)+"-checkpoint.json")
}

// newCheckpoint is a checkpoint of kind with everything the stage recorded
// so far.
func (r *runner) newCheckpoint(kind string) checkpoint {
	cp := checkpoint{
		Kind: kind, Stage: r.def.name, Started: r.res.Started, Transcript: r.rec.path, Step: r.at, Plan: r.fingerprint,
		Device: r.res.Device, Steps: slices.Clone(r.res.Steps), Answers: slices.Clone(r.res.Answers),
		Findings: slices.Clone(r.res.Findings), Backups: slices.Clone(r.res.Backups),
	}
	if len(r.res.Backups) > 0 {
		cp.Backup = r.res.Backups[0]
	}
	return cp
}

// saveCheckpoint writes cp, synced, before the point it guards.
func (r *runner) saveCheckpoint(cp checkpoint) error {
	if cp.Backup == "" || cp.Plan == "" {
		return errors.New("hwtest: nothing to resume the stage from")
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return backup.WriteFile(checkpointPath(r.cfg.Logs, r.def.name), append(data, '\n'))
}

func loadCheckpoint(logs, stage string) (*checkpoint, error) {
	if logs == "" {
		return nil, nil
	}
	b, err := os.ReadFile(checkpointPath(logs, stage))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var cp checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, fmt.Errorf("%s: %w", checkpointPath(logs, stage), err)
	}
	if !strings.EqualFold(cp.Stage, stage) {
		return nil, fmt.Errorf("%s is for stage %s", checkpointPath(logs, stage), cp.Stage)
	}
	return &cp, nil
}

func removeCheckpoint(logs, stage string) error {
	err := os.Remove(checkpointPath(logs, stage))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// hwCheckpoint writes the checkpoint just before the debug abort: the
// run the drill's write belongs to and everything the stage recorded so
// far. The process ends right after, so the file is synced.
func (r *runner) hwCheckpoint(e safety.OpEvent) error {
	if r.drill == nil {
		return errors.New("hwtest: nothing to resume the stage from")
	}
	cp := r.newCheckpoint(checkpointDrill)
	cp.Run, cp.Chunk = e.Run, e.Chunk
	return r.saveCheckpoint(cp)
}

// fingerprint identifies a stage's steps: a resumed run must lay out the
// same ones.
func fingerprint(steps []step) string {
	h := sha256.New()
	for _, s := range steps {
		fmt.Fprintln(h, s.kind, s.describe())
		for _, op := range s.ops() {
			fmt.Fprintln(h, op.Extent, hex.EncodeToString(op.Old), hex.EncodeToString(op.New))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resume goes on with a stage whose drill ended the process: the same
// steps laid out on the stage's fresh backup, the journal recovery of the
// drill's write, then the steps after it. Until the recovery starts, a
// failure keeps the checkpoint and records nothing.
func (r *runner) resume(ctx context.Context, cp *checkpoint) error {
	r.say(fmt.Sprintf("Resuming stage %s after its torn-write drill (run %s, ended after chunk %d).", r.def.name, cp.Run, cp.Chunk))
	c, sn, err := r.connect(ctx, connectOpts{rec: r.rec.rec, writes: true})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	defer c.close()
	r.describe(sn)
	switch d := r.res.Device; {
	case d.Key != cp.Device.Key || d.Mouse != cp.Device.Mouse:
		return fmt.Errorf("%w: the drill ran on %s %s, this is %s %s", ErrResume, cp.Device.Key, cp.Device.Mouse, d.Key, d.Mouse)
	case sn.Journal == nil || sn.Journal.Err != nil:
		return fmt.Errorf("%w: %w: the journal could not be read", ErrResume, ErrJournal)
	}
	for _, o := range sn.Journal.Open {
		if o.Run.ID != cp.Run {
			return fmt.Errorf("%w: %w: run %s is open too; settle it with 'arcctl journal recover' first", ErrResume, ErrJournal, o.Run.ID)
		}
	}
	f, err := backup.Load(cp.Backup)
	if err != nil {
		return fmt.Errorf("%w: the stage's backup: %w", ErrResume, err)
	}
	img := f.Image()
	b := r.builder(sn, img)
	b.src = &backup.Source{Kind: backup.KindBackup, Path: cp.Backup, File: f, Image: img}
	if err := r.def.build(b); err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	if fingerprint(b.steps) != cp.Plan || cp.Step >= len(b.steps) || b.steps[cp.Step].kind != stepCustom {
		return fmt.Errorf("%w: the stage lays out other steps than the run the drill ended; delete %s to start over",
			ErrResume, checkpointPath(r.cfg.Logs, r.def.name))
	}
	if err := r.checkFlags(b.steps[cp.Step+1:]); err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	r.res.Started, r.res.Steps, r.res.Answers, r.res.Findings = cp.Started, cp.Steps, cp.Answers, cp.Findings
	r.res.Backups = append(slices.Clone(cp.Backups), r.res.Backups...)
	r.fresh = f
	r.rec.extra = append(r.rec.extra, &recording{name: r.rec.name, path: cp.Transcript})
	r.fingerprint, r.drills = cp.Plan, true
	r.begun = true
	st := b.steps[cp.Step]
	if err := r.recoverDrill(ctx, c, st, cp, img); err != nil {
		err = fmt.Errorf("step %d (%s): %w", cp.Step+1, st.title, err)
		r.restore(ctx, c, b.steps, make([]string, len(b.steps)), make([]bool, len(b.steps)), cp.Step, img)
		return err
	}
	return r.execute(ctx, c, b.steps, img, cp.Step+1)
}

// recoverDrill settles the drill's write after the restart: the session
// found it torn in the journal, and the recovery writes the old bytes back.
// A run settled meanwhile, at load or by 'arcctl journal recover', is
// reported, and its record put back when it kept the new bytes.
func (r *runner) recoverDrill(ctx context.Context, c *conn, s step, cp *checkpoint, img *flash.Image) error {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return err
	}
	e := s.custom.touched[0]
	r.step(fmt.Sprintf("torn-write drill: the process ended after chunk %d of %d", cp.Chunk, drillChunks), true,
		"--debug-abort-after-chunk ended it as a crash would; the stage resumed in its next run")
	if o := openRun(sn, cp.Run); o != nil {
		if o.Inspection == nil {
			return fmt.Errorf("%w: run %s could not be read again: %v", ErrJournal, cp.Run, o.Err)
		}
		r.finding("after the restart the journal holds the drill's run open: %s", classes(o.Inspection))
		r.step("the restart finds the drill's write unfinished in the journal", true, classes(o.Inspection))
		dry, err := c.s.Recover(ctx, cp.Run, safety.Back, safety.Gates{DryRun: true}, nil)
		if err == nil && !slices.Equal(dry.Packets, packets(s.custom.plans[1])) {
			err = fmt.Errorf("the recovery would send %s, the preview showed %s", shownPackets(dry.Packets), shownPackets(packets(s.custom.plans[1])))
		}
		if err != nil {
			r.step("dry run of the journal recovery", false, err.Error())
			return err
		}
		if err := r.recoverBack(ctx, c, cp.Run, "journal recovery: the drill's record back to its old bytes"); err != nil {
			return err
		}
	} else {
		run, err := r.runStatus(sn, cp.Run)
		if err != nil {
			return err
		}
		r.finding("the drill's run was settled before the stage resumed: %s", run.Resolved)
		r.step("the drill's write was settled before the stage resumed", true, fmt.Sprintf("settled %s", run.Resolved))
	}
	rp, err := r.rawPath(c)
	if err != nil {
		return err
	}
	got, _, err := rp.read(ctx, e)
	if err != nil {
		return err
	}
	want, _ := img.Get(e)
	if !bytes.Equal(got, want) {
		if !bytes.Equal(got, s.custom.plans[0].Ops[0].New) {
			err := fmt.Errorf("%s holds neither the old nor the new bytes", e)
			r.step("the drill's record holds its old bytes", false, err.Error())
			return err
		}
		if err := r.putBack(ctx, c, []flash.Extent{e}, img, "the drill's record back to its old bytes"); err != nil {
			return err
		}
	}
	r.step("the drill's record holds its old bytes", true, fmt.Sprintf("%s read back through the raw path", e))
	return nil
}
