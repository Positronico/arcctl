//go:build hwtest

package hwtest

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

var dumpDevice = plan.Identity{CID: 0x7B, MID: 4, VID: 0x260D, PID: 0x1282}

func dumpBuilder(t *testing.T, stage string, verified catalog.Verifications) *builder {
	t.Helper()
	m := em11Model(t)
	return &builder{
		stage:  stage,
		m:      m,
		dev:    dumpDevice,
		opt:    mouse.Options{Device: dumpDevice, Firmware: "v1.05", Verified: verified},
		os:     keys.Mac,
		img:    dumpImage(t),
		layout: mouse.Layout(m),
	}
}

// want is one step as §11 of the plan lists it: raw bytes for identity
// writes and the probe, and the ops of each write.
type want struct {
	kind  stepKind
	ext   flash.Extent
	bytes string   // identity, probe, check
	ops   []string // apply, revert: "extent old -> new tier"
	of    int
	id    string
}

func (w want) check(t *testing.T, i int, s step) {
	t.Helper()
	if s.kind != w.kind {
		t.Fatalf("step %d is kind %d (%s), want %d", i+1, s.kind, s.describe(), w.kind)
	}
	switch s.kind {
	case stepIdentity, stepProbe, stepCheck:
		if s.extent != w.ext || hex.EncodeToString(s.bytes) != strings.ReplaceAll(w.bytes, " ", "") {
			t.Errorf("step %d: %s % x, want %s %s", i+1, s.extent, s.bytes, w.ext, w.bytes)
		}
	case stepApply, stepRevert:
		var got []string
		for _, op := range s.plan.Ops {
			got = append(got, op.Extent.String()+" "+hex.EncodeToString(op.Old)+" -> "+hex.EncodeToString(op.New)+" "+op.Tier.String())
		}
		if strings.Join(got, "; ") != strings.Join(w.ops, "; ") {
			t.Errorf("step %d (%s) ops\n got %v\nwant %v", i+1, s.title, got, w.ops)
		}
		if s.kind == stepRevert && s.of != w.of {
			t.Errorf("step %d reverts step %d, want %d", i+1, s.of+1, w.of+1)
		}
	case stepAsk, stepName:
		if s.id != w.id {
			t.Errorf("step %d asks %s, want %s", i+1, s.id, w.id)
		}
	}
}

func checkSteps(t *testing.T, b *builder, ws []want) {
	t.Helper()
	if len(b.steps) != len(ws) {
		for i, s := range b.steps {
			t.Logf("%d. %s", i+1, s.describe())
		}
		t.Fatalf("%d steps, want %d", len(b.steps), len(ws))
	}
	for i, w := range ws {
		w.check(t, i, b.steps[i])
	}
	for _, s := range b.steps {
		for _, op := range s.plan.Ops {
			if touchesPrimary(op.Extent) {
				t.Errorf("%s writes %s", s.title, op.Extent)
			}
		}
	}
	if !equalImages(b.img, dumpImage(t)) {
		t.Error("the stage does not leave the image as it found it")
	}
}

func equalImages(a, b *flash.Image) bool {
	return string(a.Bytes()) == string(b.Bytes())
}

func ext(addr, n int) flash.Extent { return flash.Extent{Addr: addr, Len: n} }

// The H1 packets are the identity write of CurrentDPI, 07 00 00 04 02 03 52
// ... eb, and the probe with byte 15 = ea.
func TestH1Plan(t *testing.T) {
	b := dumpBuilder(t, "H1", nil)
	must(t, buildH1(b))
	checkSteps(t, b, []want{
		{kind: stepIdentity, ext: ext(4, 2), bytes: "03 52"},
		{kind: stepProbe, ext: ext(4, 2), bytes: "03 52"},
		{kind: stepApply, ops: []string{"4+2 0352 -> 0253 untested"}},
		{kind: stepAsk, id: "h1.speed"},
		{kind: stepRevert, ops: []string{"4+2 0253 -> 0352 untested"}, of: 2},
		{kind: stepAsk, id: "h1.speed-back"},
	})
	id := identityPackets(b.steps[0])
	if len(id) != 1 || id[0].String() != "07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 eb" {
		t.Errorf("identity packets %v", id)
	}
	p, err := probePacket(b.steps[1].extent, b.steps[1].bytes)
	must(t, err)
	if p.String() != "07 00 00 04 02 03 52 00 00 00 00 00 00 00 00 ea" {
		t.Errorf("probe %v", p)
	}
	if err := wire.Edit.Check(p, wire.Mouse); !errors.Is(err, wire.ErrChecksum) {
		t.Errorf("the probe fails %v, want only its checksum", err)
	}
	if got := packets(b.steps[2].plan); len(got) != 1 || got[0].String() != "07 00 00 04 02 02 53 00 00 00 00 00 00 00 00 eb" {
		t.Errorf("current 3 -> 2 sends %v", got)
	}
}

func TestH2Plan(t *testing.T) {
	b := dumpBuilder(t, "H2", nil)
	must(t, buildH2(b))
	checkSteps(t, b, []want{
		{kind: stepIdentity, ext: ext(40, 4), bytes: "69 69 00 83"},
		{kind: stepApply, ops: []string{"40+4 69690083 -> 1a1a0021 untested"}},
		{kind: stepRevert, ops: []string{"40+4 1a1a0021 -> 69690083 untested"}, of: 1},
		{kind: stepApply, ops: []string{"12+4 1515002b -> 17170027 untested"}},
		{kind: stepAsk, id: "h2.stage1"},
		{kind: stepRevert, ops: []string{"12+4 17170027 -> 1515002b untested"}, of: 3},
		{kind: stepApply, ops: []string{"2+2 064f -> 0550 untested"}},
		{kind: stepCheck, ext: ext(32, 4), bytes: "3f 3f 01 d6"},
		{kind: stepAsk, id: "h2.count"},
		{kind: stepRevert, ops: []string{"2+2 0550 -> 064f untested"}, of: 6},
		{kind: stepCheck, ext: ext(32, 4), bytes: "3f 3f 01 d6"},
	})
	if got := identityPackets(b.steps[0]); len(got) != 1 || got[0].String() != "07 00 00 28 04 69 69 00 83 00 00 00 00 00 00 c5" {
		t.Errorf("identity write of stage 8: %v", got)
	}
}

func TestH3Plan(t *testing.T) {
	b := dumpBuilder(t, "H3", nil)
	must(t, buildH3(b))
	checkSteps(t, b, []want{
		{kind: stepIdentity, ext: ext(156, 4), bytes: "00 00 00 55"},
		{kind: stepApply, ops: []string{"156+4 00000055 -> 01010053 experimental"}},
		{kind: stepAsk, id: "h3.inert"},
		{kind: stepRevert, ops: []string{"156+4 01010053 -> 00000055 experimental"}, of: 1},
		{kind: stepApply, ops: []string{"108+4 05000050 -> 0b010049 untested"}},
		{kind: stepAsk, id: "h3.scroll"},
		{kind: stepRevert, ops: []string{"108+4 0b010049 -> 05000050 untested"}, of: 4},
		{kind: stepAsk, id: "h3.restored"},
		{kind: stepApply, ops: []string{"108+4 05000050 -> 02010052 untested"}},
		{kind: stepCustom},
		{kind: stepRevert, ops: []string{"108+4 02010052 -> 05000050 untested"}, of: 8},
		{kind: stepCustom},
	})
	if b.steps[4].title != "slot 3 (Backward, now its shortcut) -> Scroll Up" {
		t.Errorf("slot 3 step is titled %q", b.steps[4].title)
	}
	check, back := b.steps[9].custom, b.steps[11].custom
	pair := pairExtent(mouse.AddrCurrentDPI)
	if len(check.plans) != 0 || len(check.need) != 0 || !slices.Equal(check.touched, []flash.Extent{pair}) {
		t.Errorf("the push check writes: plans %v, need %v, touched %v", check.plans, check.need, check.touched)
	}
	if len(back.plans) != 0 || !slices.Equal(back.need, []catalog.Tier{catalog.Untested}) || !slices.Equal(back.touched, []flash.Extent{pair}) {
		t.Errorf("the stage put back: plans %v, need %v, touched %v", back.plans, back.need, back.touched)
	}
	if b.steps[11].title != "the current stage back to 4" {
		t.Errorf("the put back is titled %q", b.steps[11].title)
	}
}

// A slot 3 that already runs DPI Cycle is pressed as it is: no binding and
// no revert.
func TestH3PushCaseOnABoundSlot(t *testing.T) {
	b := dumpBuilder(t, "H3", nil)
	cycle, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle})
	must(t, err)
	e3, _ := mouse.KeyFnExtent(3)
	must(t, b.img.Set(e3.Addr, cycle))
	must(t, b.pushCase())
	if got := kinds(b); !slices.Equal(got, []stepKind{stepCustom, stepCustom}) {
		t.Fatalf("steps %v", got)
	}
}

func TestH3bPlan(t *testing.T) {
	b := dumpBuilder(t, "H3b", nil)
	must(t, buildH3b(b))
	old := []string{"0900004c", "02010052", "03010051", "03020050", "02020051", "02030050"}
	var ws []want
	for i, o := range old {
		slot := 6 + i
		e, _ := mouse.KeyFnExtent(slot)
		ws = append(ws,
			want{kind: stepApply, ops: []string{e.String() + " " + o + " -> 00000055 experimental"}},
			want{kind: stepName, id: fmt.Sprintf("h3b.slot%d", slot)},
			want{kind: stepRevert, ops: []string{e.String() + " 00000055 -> " + o + " experimental"}, of: 3 * i},
		)
	}
	checkSteps(t, b, ws)
}

// On mid 6, H3b also maps slots 12 and 13, the thumb wheel's two sides.
func TestH3bMapsTheThumbWheelOnMid6(t *testing.T) {
	b := dumpBuilder(t, "H3b", nil)
	m, ok := catalog.ByKey("7B06")
	if !ok {
		t.Fatal("no mid-6 EM11 Pro in the catalog")
	}
	b.m, b.dev.MID = m, 6
	b.opt.Device = b.dev
	for slot, rec := range map[int]string{12: "03010051", 13: "03020050"} {
		e, _ := mouse.KeyFnExtent(slot)
		v, err := hex.DecodeString(rec)
		must(t, err)
		must(t, b.img.Set(e.Addr, v))
	}
	must(t, buildH3b(b))
	var ids []string
	for _, s := range b.steps {
		if s.kind == stepName {
			ids = append(ids, s.id)
		}
	}
	want := []string{"h3b.slot6", "h3b.slot7", "h3b.slot8", "h3b.slot9", "h3b.slot10", "h3b.slot11", "h3b.slot12", "h3b.slot13"}
	if !slices.Equal(ids, want) {
		t.Fatalf("questions %v, want %v", ids, want)
	}
}

// A feature verified on this firmware plans at Verified, so the stage needs
// no flag and no typed confirmation.
func TestVerifiedFeatureNeedsNoConfirmation(t *testing.T) {
	b := dumpBuilder(t, "H1", catalog.Verifications{{Model: em11, Feature: string(mouse.FeatureCurrent), Firmware: "v1.05", Stage: "H1", Date: "2026-09-27"}})
	must(t, buildH1(b))
	for _, op := range stageOps(b.steps) {
		if op.Tier != catalog.Verified {
			t.Errorf("%s is %s", op.Desc, op.Tier)
		}
	}
	r := &runner{cfg: Config{}}
	if err := r.checkFlags(b.steps); err != nil {
		t.Error(err)
	}
}

func TestNeverTouchesLeftAndRight(t *testing.T) {
	b := dumpBuilder(t, "H3", nil)
	for slot := range 2 {
		for _, table := range []func(int) (flash.Extent, bool){mouse.KeyFnExtent, mouse.ShortcutExtent, mouse.MacroExtent} {
			e, _ := table(slot)
			if err := b.identity("slot", e, slot, mouse.FeatureSystem); !errors.Is(err, errPrimary) {
				t.Errorf("identity write of %s: %v", e, err)
			}
		}
		e, _ := mouse.KeyFnExtent(slot)
		c := plan.Change{Addr: e.Addr, New: disabled, Tier: catalog.Untested}
		if _, err := b.apply("slot", c); err == nil {
			t.Errorf("a write of %s was planned", e)
		}
	}
	if touchesPrimary(ext(96+8, 4)) || !touchesPrimary(ext(100, 1)) {
		t.Error("touchesPrimary draws the line in the wrong place")
	}
}

func TestStagesNeedTheirPreconditions(t *testing.T) {
	b := dumpBuilder(t, "H2", nil)
	count, _ := mouse.EncodeStageCount(8)
	must(t, b.img.Set(mouse.AddrMaxDpiStage, count[:]))
	if err := buildH2(b); err == nil || !strings.Contains(err.Error(), "stage 8 inactive") {
		t.Errorf("H2 with 8 stages: %v", err)
	}
	b = dumpBuilder(t, "H2", nil)
	cur, _ := mouse.EncodeCurrentStage(5)
	must(t, b.img.Set(mouse.AddrCurrentDPI, cur[:]))
	if err := buildH2(b); err == nil || !strings.Contains(err.Error(), "switch to a lower stage") {
		t.Errorf("H2 on the last stage: %v", err)
	}
	b = dumpBuilder(t, "H3", nil)
	up, _ := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeWheel, Param: mouse.ParamScrollUp})
	e, _ := mouse.KeyFnExtent(3)
	must(t, b.img.Set(e.Addr, up))
	if err := buildH3(b); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Errorf("H3 with slot 3 on Scroll Up already: %v", err)
	}
}
