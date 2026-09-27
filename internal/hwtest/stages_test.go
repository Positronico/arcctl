//go:build hwtest

package hwtest

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// erased reads the bodies the dump lacks as a full backup of the emulator
// does: erased flash.
func erased(b *builder) *builder {
	for k := range mouse.Slots {
		for _, table := range []func(int) (flash.Extent, bool){mouse.ShortcutExtent, mouse.MacroExtent} {
			e, _ := table(k)
			if !b.img.Known(e) {
				cur, _ := b.img.Get(e)
				_ = b.img.Set(e.Addr, cur)
			}
		}
	}
	return b
}

// withSource gives b the dump as its fresh backup.
func withSource(t *testing.T, b *builder) *builder {
	t.Helper()
	f, err := backup.New(session.Capture{Device: b.dev, Model: b.m, Image: b.img.Clone(), Versions: session.Versions{Mouse: b.opt.Firmware}},
		backup.Meta{Tool: "arcctl test", Created: testTime})
	must(t, err)
	b.src = &backup.Source{Kind: backup.KindBackup, File: f, Image: f.Image()}
	return b
}

// sparesPrimary fails when a custom step plans a write of slot 0 or 1.
func sparesPrimary(t *testing.T, b *builder) {
	t.Helper()
	for _, s := range b.steps {
		if s.kind != stepCustom {
			continue
		}
		for _, p := range s.custom.plans {
			if err := spares(p); err != nil {
				t.Errorf("%s: %v", s.title, err)
			}
		}
	}
}

func kinds(b *builder) []stepKind {
	out := make([]stepKind, len(b.steps))
	for i, s := range b.steps {
		out[i] = s.kind
	}
	return out
}

func TestH5Plan(t *testing.T) {
	b := erased(dumpBuilder(t, "H5", nil))
	must(t, buildH5(b))
	want := []stepKind{stepApply, stepCheck, stepRevert, stepCustom, stepWait}
	for _, c := range h5Cycles {
		want = append(want, stepApply, stepWait, stepAsk)
		if c != 1 {
			want = append(want, stepAsk)
		}
		want = append(want, stepRevert)
	}
	want = append(want, stepAsk)
	if got := kinds(b); !slices.Equal(got, want) {
		for i, s := range b.steps {
			t.Logf("%d. %s", i+1, s.describe())
		}
		t.Fatalf("steps %v, want %v", got, want)
	}
	for _, s := range b.steps {
		if s.kind == stepWait && strings.HasPrefix(s.id, "h5.cycle") &&
			(!strings.Contains(s.text, "Click into the document") || !strings.Contains(s.text, "switch the mouse off and on again")) {
			t.Errorf("%s does not say where to type or how to stop a macro that keeps typing: %s", s.id, s.text)
		}
	}
	area, _ := mouse.MacroExtent(scratchSlot)
	if op := b.steps[0].plan.Ops; len(op) != 1 || op[0].Extent != (flash.Extent{Addr: area.Addr, Len: 48}) || op[0].Tier != catalog.Experimental {
		t.Errorf("the three-event macro: %+v", op)
	}
	if got := packets(b.steps[0].plan); len(got) != 5 {
		t.Errorf("the three-event macro takes %d packets, want 5", len(got))
	}
	d := b.steps[3].custom
	if !d.abort || len(d.plans) != 2 || len(packets(d.plans[0])) != drillChunks || d.touched[0] != (flash.Extent{Addr: area.Addr, Len: 103}) {
		t.Errorf("the drill: abort %v, %d plans, touched %v", d.abort, len(d.plans), d.touched)
	}
	if back := d.plans[1].Ops; len(back) != 1 || !slices.Equal(back[0].New, d.plans[0].Ops[0].Old) {
		t.Errorf("the drill's roll back: %+v", back)
	}
	bind := b.steps[5].plan
	e4, _ := mouse.KeyFnExtent(bindSlot)
	m4, _ := mouse.MacroExtent(bindSlot)
	var got []string
	for _, op := range bind.Ops {
		got = append(got, op.Phase.String()+" "+op.Extent.String()+" "+op.Tier.String())
	}
	if want := []string{"body " + (flash.Extent{Addr: m4.Addr, Len: 53}).String() + " untested", "bind " + e4.String() + " untested"}; !slices.Equal(got, want) {
		t.Errorf("binding with cycle 1: %v, want %v", got, want)
	}
	if bind.Ops[1].New[2] != 1 {
		t.Errorf("binding % x, want cycle 1", bind.Ops[1].New)
	}
	var revert []string
	for _, op := range b.steps[8].plan.Ops {
		revert = append(revert, op.Phase.String()+" "+op.Extent.String())
	}
	if want := []string{"neutralise " + e4.String(), "body " + (flash.Extent{Addr: m4.Addr, Len: 53}).String(), "bind " + e4.String()}; !slices.Equal(revert, want) {
		t.Errorf("the two-phase revert: %v, want %v", revert, want)
	}
	var cycles []byte
	for _, s := range b.steps {
		if s.kind == stepApply && len(s.plan.Ops) == 2 {
			cycles = append(cycles, s.plan.Ops[1].New[2])
		}
	}
	if !slices.Equal(cycles, []byte{1, 253, 254, 255}) {
		t.Errorf("cycles %v", cycles)
	}
	sparesPrimary(t, b)
	if !equalImages(b.img, dumpImage(t)) {
		t.Error("H5 does not leave the image as it found it")
	}
}

func TestH5RefusesABoundScratchArea(t *testing.T) {
	b := erased(dumpBuilder(t, "H5", nil))
	e, _ := mouse.KeyFnExtent(6)
	rec, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeMacro, Param: uint16(scratchSlot)<<8 | 1})
	must(t, err)
	must(t, b.img.Set(e.Addr, rec))
	if err := buildH5(b); err == nil || !strings.Contains(err.Error(), "runs the macro of slot 15") {
		t.Fatalf("H5 with slot 6 running macro 15: %v", err)
	}
}

func TestH6Plan(t *testing.T) {
	b := withSource(t, erased(dumpBuilder(t, "H6", nil)))
	must(t, buildH6(b))
	if got, want := kinds(b), []stepKind{stepApply, stepCustom, stepCustom, stepAsk}; !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	var ext []string
	phases := map[plan.Phase]int{}
	for _, op := range b.steps[0].plan.Ops {
		ext = append(ext, op.Extent.String())
		phases[op.Phase]++
		if op.Tier != catalog.Untested {
			t.Errorf("%s is %s", op.Desc, op.Tier)
		}
	}
	if phases[plan.Neutralise] != 1 || phases[plan.Body] != 2 || phases[plan.Bind] != 3 || phases[plan.Record] != 2 {
		t.Errorf("the five changes: %v (%v)", ext, phases)
	}
	restore := b.steps[1].custom.plans[0]
	changed := changedExtents(dumpImage(t), mustApply(t, dumpImage(t), b.steps[0].plan))
	if len(changed) != 6 {
		t.Errorf("changed records %v, want the current stage, DPI stage 1, two bindings and two bodies", changed)
	}
	for _, op := range restore.Ops {
		if touchesPrimary(op.Extent) {
			t.Errorf("the restore writes %s", op.Extent)
		}
	}
	if !equalImages(b.img, dumpImage(t)) {
		t.Error("the restore does not bring the image back")
	}
}

func mustApply(t *testing.T, im *flash.Image, p plan.Plan) *flash.Image {
	t.Helper()
	out, err := applyPlan(im, p)
	must(t, err)
	return out
}

func TestH7Plan(t *testing.T) {
	b := withSource(t, erased(dumpBuilder(t, "H7", nil)))
	must(t, buildH7(b))
	if got, want := kinds(b), []stepKind{stepCustom, stepCustom, stepAsk}; !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	raw := b.steps[0].custom.raw
	if len(raw) != 1 || raw[0].String() != "09 00 00 00 00 00 00 00 00 00 00 00 00 00 00 44" {
		t.Fatalf("the reset packet: %v", raw)
	}
	if err := wire.Reset.Check(raw[0], wire.Mouse); err != nil {
		t.Errorf("the Reset policy refuses the reset: %v", err)
	}
	if err := wire.Edit.Check(raw[0], wire.Mouse); err == nil {
		t.Error("the Edit policy lets the reset through")
	}
	r := &runner{cfg: Config{}, def: stageNamed(t, "H7")}
	need := r.missingFlags(b.steps)
	if !slices.Contains(need, "--allow-untested") || !slices.Contains(need, "--experimental") {
		t.Errorf("H7 needs %v", need)
	}
	if got := safety.ConfirmPhrase(stageOps(b.steps)); got != "write experimental" {
		t.Errorf("the tier phrase is %q", got)
	}
}

// The DPI Cycle button of the dump runs Cmd+C, so the drill binds it to
// DPI cycle first and puts it back afterwards; the drill names it.
func TestH9Plan(t *testing.T) {
	b := erased(dumpBuilder(t, "H9", nil))
	must(t, buildH9(b))
	if got, want := kinds(b), []stepKind{stepCustom, stepCustom, stepCustom, stepApply, stepCustom, stepRevert, stepAsk}; !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	e5, _ := mouse.KeyFnExtent(5)
	if ops := b.steps[3].plan.Ops; len(ops) != 1 || ops[0].Extent != e5 || !slices.Equal(ops[0].New, []byte{0x02, 0x01, 0x00, 0x52}) {
		t.Errorf("the bind before the DPI drill: %+v", ops)
	}
	if ops := b.steps[5].plan.Ops; b.steps[5].of != 3 || len(ops) != 1 || ops[0].Extent != e5 {
		t.Errorf("the revert after the DPI drill: of %d, %+v", b.steps[5].of, ops)
	}
	if text := strings.Join(b.steps[4].custom.lines, " "); !strings.Contains(text, "Press the DPI Cycle button, bound to DPI cycle for this drill, once") {
		t.Errorf("the DPI drill says: %s", text)
	}
	area, _ := mouse.MacroExtent(scratchSlot)
	for _, s := range b.steps[:3] {
		p := s.custom.plans[0]
		if len(p.Ops) != 1 || p.Ops[0].Extent != (flash.Extent{Addr: area.Addr, Len: 383}) || len(packets(p)) != longChunks {
			t.Errorf("%s: %+v", s.title, p.Ops)
		}
	}
	dpi := b.steps[4].custom.plans[0]
	var got []string
	for _, op := range dpi.Ops {
		got = append(got, op.Extent.String()+" "+op.Tier.String())
	}
	if want := []string{"36+4 untested", "40+4 untested", "4+2 untested"}; !slices.Equal(got, want) {
		t.Errorf("the DPI drill writes %v, want %v", got, want)
	}
	if cur := dpi.Ops[2].New; cur[0] != 2 {
		t.Errorf("the DPI drill moves the current stage to % x, want stage 3 (one below 4)", cur)
	}
	sparesPrimary(t, b)

	b = erased(dumpBuilder(t, "H9", nil))
	rec, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPIUp})
	must(t, err)
	e2, _ := mouse.KeyFnExtent(2)
	must(t, b.img.Set(e2.Addr, rec))
	must(t, buildH9(b))
	if got, want := kinds(b), []stepKind{stepCustom, stepCustom, stepCustom, stepCustom, stepAsk}; !slices.Equal(got, want) {
		t.Fatalf("with a DPI button: steps %v, want %v", got, want)
	}
	if text := strings.Join(b.steps[3].custom.lines, " "); !strings.Contains(text, "Press the Wheel Click button once") {
		t.Errorf("the DPI drill says: %s", text)
	}
}

// A stage whose feature internal/mouse does not list refuses to start, so
// the drill is never spent without a record; promote still leaves out a
// feature it does not know.
func TestUnknownFeatureRefusesTheStage(t *testing.T) {
	r := newRig(t, nil)
	h7Script(r)
	old := knownFeatures
	knownFeatures = func() []mouse.Feature {
		return slices.DeleteFunc(old(), func(f mouse.Feature) bool { return f == mouse.FeatureReset })
	}
	t.Cleanup(func() { knownFeatures = old })
	_, err := Run(t.Context(), r.cfg, "H7")
	if !errors.Is(err, ErrUnknownFeature) || r.resets() != 0 || len(r.script.asked) != 0 {
		t.Fatalf("err %v, %d resets, asked %v", err, r.resets(), r.script.asked)
	}
	run := &runner{cfg: r.cfg.withDefaults(), def: stageNamed(t, "H7"), res: &Result{Device: Device{Key: em11, Mouse: "v0.42"}, Started: testTime}}
	vs, err := run.promote(t.Context())
	must(t, err)
	if len(vs) != 0 || r.generated != 0 || !strings.Contains(strings.Join(run.res.Findings, "\n"), "not promoted: this build has no feature device.reset") {
		t.Fatalf("promoted %v, generated %d, findings %v", vs, r.generated, run.res.Findings)
	}
	knownFeatures = old
	vs, err = run.promote(t.Context())
	must(t, err)
	if len(vs) != 1 || vs[0].Feature != "device.reset" || vs[0].Stage != "H7" {
		t.Fatalf("promoted %v", vs)
	}
}

func stageNamed(t *testing.T, name string) *stageDef {
	t.Helper()
	i := slices.IndexFunc(stages, func(s *stageDef) bool { return s.name == name })
	if i < 0 {
		t.Fatalf("no stage %s", name)
	}
	return stages[i]
}
