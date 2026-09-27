package backup_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// unknownChanges changes on dev, behind the backup's back, a DPI stage and
// what arcctl knows no valid value for: two unmapped pairs, the unmapped
// block at 84, the hidden setting at 185 (invalid in the dump, valid now), a
// field of other models, KeyOperation and the extended block.
func unknownChanges(t *testing.T, dev *flash.Image) {
	t.Helper()
	edit(t, dev, mouse.SetDPI{Stage: 1, DPI: 1600})
	for addr, b := range map[int][]byte{
		6: {0x01, 0x54}, 84: {0x00, 0x55}, mouse.AddrSensorMode: {0x00, 0x55}, 187: {0x01, 0x54},
		mouse.AddrAngleTune: {0x01, 0x54}, mouse.AddrKeyOperation: {0x01, 0x54}, mouse.AddrSensor3955DPI: {0x12},
	} {
		must(t, dev.Set(addr, b))
	}
}

var capturedExtents = []flash.Extent{{Addr: 6, Len: 2}, {Addr: 84, Len: 12}, {Addr: 187, Len: 2}, {Addr: 189, Len: 2}}

// --include-unknown writes back exactly the settings-page records arcctl
// knows no valid value for, each whole, as the backup captured it, at the
// experimental tier and after every other write, except a hidden setting
// without its H8 record (D5); without it they are only listed.
func TestRestoreIncludeUnknown(t *testing.T) {
	b0 := fullRich(t)
	dev := b0.Clone()
	unknownChanges(t, dev)
	want := map[string]backup.Fate{
		"DPI stage 2": backup.FateWrite, "KeyOperation @8": backup.FateRefused, "unmapped 6912+48": backup.FateUnknown,
		"unmapped @6": backup.FateUnknown, "unmapped @84": backup.FateUnknown, "SensorMode @185": backup.FateUnknown,
		"unmapped @187": backup.FateUnknown, "AngleTune @189": backup.FateUnknown,
	}
	eligible := []string{"unmapped @6", "unmapped @84", "unmapped @187", "AngleTune @189"}
	src := fromFile(newFile(t, b0))
	l := mouse.Layout(em11(t))
	for _, include := range []bool{false, true} {
		r, err := backup.PlanRestore(src, target(t, dev.Clone()), backup.RestoreOptions{IncludeUnknown: include})
		must(t, err)
		got := records(r)
		if include {
			for _, n := range eligible {
				want[n] = backup.FateWrite
			}
		}
		if !mapsEqual(got, want) {
			t.Fatalf("include %v: records\n%v\nwant\n%v", include, got, want)
		}
		for _, x := range r.Records {
			if x.Eligible != slices.Contains(eligible, x.Name) || x.Captured != (include && x.Eligible) {
				t.Errorf("include %v: %s eligible %v, captured %v", include, x.Name, x.Eligible, x.Captured)
			}
		}
		if err := r.Plan.Validate(dev, l); err != nil {
			t.Fatalf("include %v: %v", include, err)
		}
		var ops []flash.Extent
		for _, op := range r.Plan.Ops {
			if op.Phase != plan.Captured {
				continue
			}
			ops = append(ops, op.Extent)
			if b, _ := b0.Get(op.Extent); !bytes.Equal(op.New, b) || op.Tier != catalog.Experimental {
				t.Errorf("op %d writes % x at %s, the backup holds % x", op.Seq, op.New, op.Tier, b)
			}
		}
		wantOps := capturedExtents
		if !include {
			wantOps = nil
		}
		if !slices.Equal(ops, wantOps) || len(r.Plan.Ops) != 1+len(ops) || r.Plan.Ops[0].Phase != plan.Record {
			t.Fatalf("include %v: plan %v, captured ops %v, want %v", include, r.Plan.Ops, ops, wantOps)
		}
		if include {
			want := "write experimental and captured 6+2 84+12 187+2 189+2"
			if got := safety.ConfirmPhrase(r.Plan.Ops); got != want {
				t.Errorf("phrase %q, want %q", got, want)
			}
			after := dev.Clone()
			applyPlan(t, after, r.Plan)
			again, err := backup.PlanRestore(src, target(t, after), backup.RestoreOptions{IncludeUnknown: true})
			must(t, err)
			if len(again.Plan.Ops) != 0 || len(again.Records) != 3 {
				t.Errorf("after the restore: %d ops, records %v", len(again.Plan.Ops), records(again))
			}
		}
	}
}

// Bytes the backup did not capture are never written, not even in part of
// an unmapped block, and neither are invalid bindings and bodies, which lie
// in the tables.
func TestRestoreIncludeUnknownLeavesTheRest(t *testing.T) {
	full := fullRich(t)
	sc, _ := mouse.ShortcutExtent(9)
	body, err := mouse.EncodeShortcut(cmdC)
	must(t, err)
	body[len(body)-1]++
	must(t, full.Set(sc.Addr, body))
	k7, _ := mouse.KeyFnExtent(7)
	must(t, full.Set(k7.Addr, []byte{0x05, 0x07, 0x00, 0x00}))

	b0 := flash.New()
	for _, e := range []flash.Extent{{Addr: 0, Len: 90}, {Addr: 96, Len: mouse.AddrEndEeprom - 96}} {
		b, _ := full.Get(e)
		must(t, b0.Set(e.Addr, b))
	}
	dev := full.Clone()
	unknownChanges(t, dev)
	must(t, dev.Set(sc.Addr, bytes.Repeat([]byte{0xFF}, len(body))))
	must(t, dev.Set(k7.Addr, []byte{0x00, 0x00, 0x00, 0x55}))

	r, err := backup.PlanRestore(fromFile(newFile(t, b0, flash.Extent{Addr: 90, Len: 6})), target(t, dev.Clone()), backup.RestoreOptions{IncludeUnknown: true})
	must(t, err)
	got := records(r)
	for name, want := range map[string]backup.Fate{"unmapped @84": backup.FateNotCaptured, "Shortcut 9": backup.FateUnknown, "Button slot 7": backup.FateUnknown} {
		if got[name] != want {
			t.Errorf("%s: %v, want %v", name, got[name], want)
		}
	}
	for _, op := range r.Plan.Ops {
		for _, e := range []flash.Extent{{Addr: 84, Len: 12}, sc, k7} {
			if op.Extent.Overlaps(e) {
				t.Errorf("op %d (%s %s) writes %s", op.Seq, op.Phase, op.Extent, e)
			}
		}
		if b, ok := b0.Get(op.Extent); op.Phase == plan.Captured && (!ok || !bytes.Equal(b, op.New)) {
			t.Errorf("op %d writes % x, which the backup did not capture there", op.Seq, op.New)
		}
	}
	if n := len(r.Captured()); n != 3 {
		t.Errorf("%d records written back as captured, want 3: %v", n, records(r))
	}
}

// stagePair is the stage count and the current stage im holds, when both
// pairs are valid.
func stagePair(t *testing.T, im *flash.Image) (count, current int, ok bool) {
	t.Helper()
	c := mouse.Decode(em11(t), im)
	if c.Stages.State != flash.OK || c.Current.State != flash.OK {
		return 0, 0, false
	}
	return c.Stages.Value, c.Current.Value, true
}

// A captured stage count or current stage goes after every record, so the
// write of the other pair must not leave the mouse on a current stage past
// its count until then (§6.2): it is refused, as it is without
// --include-unknown on the mouse's own pair.
func TestRestoreCapturedStagePairKeepsCurrentBelowCount(t *testing.T) {
	pair := func(encode func(int) (flash.Pair, error), v int) []byte {
		p, err := encode(v)
		must(t, err)
		return p[:]
	}
	invalid := []byte{0x03, 0x00}
	for _, tc := range []struct {
		name     string
		src, dev [2][]byte
		refused  string
	}{
		{"captured count", [2][]byte{invalid, pair(mouse.EncodeCurrentStage, 4)},
			[2][]byte{pair(mouse.EncodeStageCount, 3), pair(mouse.EncodeCurrentStage, 0)}, "Current stage"},
		{"captured current", [2][]byte{pair(mouse.EncodeStageCount, 2), invalid},
			[2][]byte{pair(mouse.EncodeStageCount, 4), pair(mouse.EncodeCurrentStage, 3)}, "DPI stages"},
	} {
		b0, dev := fullRich(t), fullRich(t)
		for i, addr := range []int{mouse.AddrMaxDpiStage, mouse.AddrCurrentDPI} {
			must(t, b0.Set(addr, tc.src[i]))
			must(t, dev.Set(addr, tc.dev[i]))
		}
		src := fromFile(newFile(t, b0))
		for _, include := range []bool{false, true} {
			r, err := backup.PlanRestore(src, target(t, dev.Clone()), backup.RestoreOptions{IncludeUnknown: include})
			must(t, err)
			if got := records(r)[tc.refused]; got != backup.FateRefused {
				t.Errorf("%s, include %v: %s is %v, want refused", tc.name, include, tc.refused, got)
			}
			sim := dev.Clone()
			for _, op := range r.Plan.Ops {
				must(t, sim.Set(op.Extent.Addr, op.New))
				if n, c, ok := stagePair(t, sim); ok && c >= n {
					t.Errorf("%s, include %v: after op %d (%s %s) the mouse holds current stage %d with %d stages",
						tc.name, include, op.Seq, op.Phase, op.Extent, c+1, n)
				}
			}
		}
	}
}

// D5: a hidden setting becomes writable only once its own H8 test is
// recorded, so until then --include-unknown leaves its captured bytes out;
// unmapped bytes, other models' fields, the report rate and the colours
// still go back.
func TestRestoreIncludeUnknownWaitsForH8(t *testing.T) {
	b0 := fullRich(t)
	must(t, b0.Set(mouse.AddrReportRate, []byte{0x03, 0x00}))
	col, _ := mouse.ColorExtent(0)
	must(t, b0.Set(col.Addr, []byte{0x10, 0x20, 0x30, 0x00}))
	dev := b0.Clone()
	rate, err := mouse.EncodeRate(1000)
	must(t, err)
	for addr, b := range map[int][]byte{
		mouse.AddrReportRate: rate[:], col.Addr: mouse.EncodeColor([3]byte{1, 2, 3}), mouse.AddrSensorMode: {0x00, 0x55},
		187: {0x01, 0x54}, mouse.AddrAngleTune: {0x01, 0x54},
	} {
		must(t, dev.Set(addr, b))
	}
	src := fromFile(newFile(t, b0))
	sensor := flash.Extent{Addr: mouse.AddrSensorMode, Len: 2}
	feature, _ := mouse.FieldFeature(sensor.Addr)
	back := []string{"Report rate", "Colour 1", "unmapped @187", "AngleTune @189"}
	for _, tc := range []struct {
		name     string
		vs       catalog.Verifications
		eligible bool
	}{
		{"no record", nil, false},
		{"H8 recorded", catalog.Verifications{{Model: "7B04", Feature: string(feature), Firmware: "v1.05", Stage: "H8",
			Date: "2026-10-01"}}, true},
	} {
		tg := target(t, dev.Clone())
		tg.Options.Verified = tc.vs
		r, err := backup.PlanRestore(src, tg, backup.RestoreOptions{IncludeUnknown: true})
		must(t, err)
		for _, x := range r.Records {
			switch {
			case x.Extent == sensor:
				if x.Eligible != tc.eligible || x.Captured != tc.eligible {
					t.Errorf("%s: %s eligible %v, captured %v, fate %v: %s", tc.name, x.Name, x.Eligible, x.Captured, x.Fate, x.Why)
				}
				if !tc.eligible && !strings.Contains(x.Why, "H8") {
					t.Errorf("%s: %s left out because %q", tc.name, x.Name, x.Why)
				}
			case slices.Contains(back, x.Name):
				if !x.Captured {
					t.Errorf("%s: %s not written back: %v, %s", tc.name, x.Name, x.Fate, x.Why)
				}
			}
		}
		if got := slices.ContainsFunc(r.Plan.Ops, func(op plan.Op) bool { return op.Extent.Overlaps(sensor) }); got != tc.eligible {
			t.Errorf("%s: the plan writes %s: %v, want %v", tc.name, sensor, got, tc.eligible)
		}
	}
}
