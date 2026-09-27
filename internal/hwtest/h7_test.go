//go:build hwtest

package hwtest

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// factory is the start image as a reset would leave it when it clears only
// records a plan can write: the rate, the current stage, a DPI stage and a
// colour, the bindings of slots 2 to 11 and the shortcut bodies of 2 to 5.
func factory(t *testing.T) func(*emu.Config) {
	return func(c *emu.Config) {
		im := c.Mouse.Image.Clone()
		set := func(addr int, b []byte) { must(t, im.Set(addr, b)) }
		rate, err := mouse.EncodeRate(1000)
		must(t, err)
		set(mouse.AddrReportRate, rate[:])
		cur, err := mouse.EncodeCurrentStage(0)
		must(t, err)
		set(mouse.AddrCurrentDPI, cur[:])
		e, _ := mouse.DPIExtent(0)
		d, err := mouse.EncodeDPI(em11Model(t).Sensor, 1600)
		must(t, err)
		set(e.Addr, d)
		e, _ = mouse.ColorExtent(1)
		set(e.Addr, mouse.EncodeColor([3]byte{0x12, 0x34, 0x56}))
		for k := 2; k < 12; k++ {
			e, _ := mouse.KeyFnExtent(k)
			fn := mouse.KeyFn{Type: mouse.TypeDisable}
			if k < 6 {
				fn = mouse.KeyFn{Type: mouse.TypeMouse, Param: mouse.ParamMiddle}
			}
			rec, err := mouse.EncodeKeyFn(fn)
			must(t, err)
			set(e.Addr, rec)
			if k < 6 {
				s, _ := mouse.ShortcutExtent(k)
				set(s.Addr, bytes.Repeat([]byte{0xFF}, s.Len))
			}
		}
		c.Mouse.Factory = im
	}
}

func h7Script(r *rig) {
	r.script.yes("h7.prep-repair", "h7.prep-pointer", "h7.prep-webapp", "h7.prep-drill", "h7.run", "h7.buttons", "h7.write-back-extra",
		"h7.write-back-unknown", "h7.resume").
		set("h7.confirm", "write experimental").set("h7.phrase", "reset").set("h7.repaired", false)
}

// cmd7After counts the cmd-7 packets that reached the device after the
// reset.
func (r *rig) cmd7After() int {
	n, reset := 0, false
	for _, w := range r.dev.Writes() {
		switch {
		case w.Packet.Cmd() == wire.CmdClear:
			reset = true
		case reset && w.Packet.Cmd() == wire.CmdWrite:
			n++
		}
	}
	return n
}

func (r *rig) checkpoint() *checkpoint {
	r.t.Helper()
	cp, err := loadCheckpoint(r.cfg.Logs, "H7")
	must(r.t, err)
	return cp
}

// resets counts the cmd-9 packets that reached the device.
func (r *rig) resets() int {
	n := 0
	for _, w := range r.dev.Writes() {
		if w.Packet.Cmd() == wire.CmdClear {
			n++
		}
	}
	return n
}

// checkH7Writes fails on any packet but the one reset that the Edit policy
// does not allow.
func (r *rig) checkH7Writes() {
	r.t.Helper()
	for _, w := range r.dev.Writes() {
		if err := wire.Edit.Check(w.Packet, wire.Mouse); err != nil && w.Packet != resetPacket() {
			r.t.Errorf("packet %v reached the device: %v", w.Packet, err)
		}
	}
}

// H7 end to end: the reset clears settings, bindings and bodies, the stage
// writes all of it back, and the record it promotes opens the gate of the
// release build's reset for this model and firmware.
func TestH7ResetsRestoresAndUnlocks(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	res := r.run("H7")
	r.passed(res)
	r.unchanged()
	r.checkH7Writes()
	if n := r.resets(); n != 1 {
		t.Fatalf("%d resets reached the device, want 1", n)
	}
	joined := strings.Join(res.Findings, "\n")
	for _, f := range []string{"cmd 9: one reply after", "reset scope: 18 records changed: Report rate, Current stage, DPI stage 1, Colour 2, Button slot 2",
		"Shortcut 5", "pairing survived the reset", "cmd 23 before the reset", "the release restore leaves out Report rate, Colour 2, Button slot 6",
		"Button slot 11 after a reset; the stage wrote them back at the experimental tier"} {
		if !strings.Contains(joined, f) {
			t.Errorf("findings lack %q:\n%s", f, joined)
		}
	}
	if len(res.Backups) != 3 {
		t.Errorf("backups %v, want before, after the reset and after the write-back", res.Backups)
	}
	vs := r.verified()
	if len(vs) != 1 || vs[0] != (verification{"7B04", "device.reset", "v0.42", "H7", "2026-09-27"}) {
		t.Fatalf("verified.json %+v", vs)
	}
	m := em11Model(t)
	gate := catalog.Verifications{{Model: vs[0].Model, Feature: vs[0].Feature, Firmware: vs[0].Firmware, Stage: vs[0].Stage, Date: vs[0].Date}}
	if err := safety.ResetGate(m, "v0.42", gate); err != nil {
		t.Errorf("the record does not open the reset: %v", err)
	}
	if err := safety.ResetGate(m, "v0.43", gate); err == nil {
		t.Error("the record opens the reset on another firmware")
	}
	if !strings.Contains(r.read(logPath), "| H7 | factory reset | passed 2026-09-27 (v0.42) |") {
		t.Error("H7 is not logged as passed")
	}
}

// The reset is sent once even when nothing answers it, and the stage still
// reads the mouse again to find out what it did.
func TestH7NoReplyIsNeverResent(t *testing.T) {
	r := newRig(t, func(c *emu.Config) {
		factory(t)(c)
		c.Behavior.ClearSilent = true
	})
	h7Script(r)
	res := r.run("H7")
	r.passed(res)
	if n := r.resets(); n != 1 {
		t.Fatalf("%d resets reached the device, want 1", n)
	}
	if !strings.Contains(strings.Join(res.Findings, "\n"), "cmd 9: no reply within 3s") {
		t.Errorf("findings %v", res.Findings)
	}
	r.unchanged()
}

// A mouse that loses its pairing to the reset is re-paired along the
// documented path; the stage writes the backup back, but it neither passes
// nor promotes (D6).
func TestH7UnpairedIsNotPromoted(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	r.cfg.Wait = 500 * time.Millisecond
	var after *flash.Image
	r.cfg.afterReset = func() {
		after = r.dev.Image()
		must(t, r.dev.Pair(nil))
	}
	r.script.on("h7.repair", func() {
		must(t, r.dev.Pair(&emu.Mouse{Model: em11Model(t), Image: after, Firmware: testFirmware, Battery: emu.Battery{Level: 80}}))
	})
	res := r.run("H7")
	if res.Passed || len(res.Promoted) != 0 || len(r.verified()) != 0 {
		t.Fatalf("passed %v, promoted %v", res.Passed, res.Promoted)
	}
	if !strings.Contains(strings.Join(res.Findings, "\n"), "the reset unpaired the mouse") {
		t.Errorf("findings %v", res.Findings)
	}
	r.unchanged()
}

// A reset to the catalog's defaults also clears fields arcctl knows no
// valid value for: after asking, the stage writes back the bytes the fresh
// backup captured there, as --include-unknown does, and lists what it never
// writes. Declined, they stay as the reset left them.
func TestH7WritesBackUnknownBytes(t *testing.T) {
	for _, yes := range []bool{true, false} {
		t.Run(yesNo(yes), func(t *testing.T) {
			r := newRig(t, nil)
			h7Script(r)
			r.script.set("h7.write-back-unknown", yes)
			res := r.run("H7")
			r.passed(res)
			if !slices.Contains(r.script.asked, "h7.write-back-unknown") {
				t.Fatalf("asked %v", r.script.asked)
			}
			joined := strings.Join(res.Findings, "\n")
			want := []string{"not written back, arcctl never writes them: KeyOperation @8 (not written)",
				"the pair at 185, flagged invalid on this unit, changed"}
			names := "unmapped @6, unmapped @84, unmapped @167, SensorMode @185, unmapped @187, AngleTune @189"
			if yes {
				want = append(want, "the stage wrote back the captured bytes of "+names+" at the experimental tier (--include-unknown)")
			} else {
				want = append(want, "left as the reset changed them, at the user's choice: "+names)
			}
			for _, f := range want {
				if !strings.Contains(joined, f) {
					t.Errorf("findings lack %q:\n%s", f, joined)
				}
			}
			now := r.dev.Image()
			for _, e := range []flash.Extent{{Addr: 6, Len: 2}, {Addr: 84, Len: 12}, {Addr: 167, Len: 2}, {Addr: mouse.AddrSensorMode, Len: 2},
				{Addr: 187, Len: 2}, {Addr: mouse.AddrAngleTune, Len: 2}} {
				got, _ := now.Get(e)
				was, _ := r.start.Get(e)
				if bytes.Equal(got, was) != yes {
					t.Errorf("%v holds % x, % x before the reset", e, got, was)
				}
			}
			e8 := flash.Extent{Addr: 8, Len: 2}
			got, _ := now.Get(e8)
			was, _ := r.start.Get(e8)
			if bytes.Equal(got, was) {
				t.Errorf("KeyOperation holds % x, as before the reset", got)
			}
		})
	}
}

// The fresh backup holds an invalid stage count pair and stage 5, and the
// reset leaves 3 stages and stage 1. Whatever the answer to the unknown
// records, the write-back never leaves the mouse on a current stage past
// its count, after it or between two of its packets. The current stage goes
// back only once the count is back, which takes a second try; the first one
// does not pass.
func TestH7WriteBackKeepsCurrentBelowCount(t *testing.T) {
	for _, yes := range []bool{true, false} {
		t.Run(yesNo(yes), func(t *testing.T) {
			var reset *flash.Image
			r := newRig(t, func(c *emu.Config) {
				im := c.Mouse.Image
				must(t, im.Set(mouse.AddrMaxDpiStage, []byte{0x03, 0x00}))
				cur, err := mouse.EncodeCurrentStage(4)
				must(t, err)
				must(t, im.Set(mouse.AddrCurrentDPI, cur[:]))
				reset = im.Clone()
				cnt, err := mouse.EncodeStageCount(3)
				must(t, err)
				must(t, reset.Set(mouse.AddrMaxDpiStage, cnt[:]))
				cur, err = mouse.EncodeCurrentStage(0)
				must(t, err)
				must(t, reset.Set(mouse.AddrCurrentDPI, cur[:]))
				c.Mouse.Factory = reset.Clone()
			})
			h7Script(r)
			r.script.set("h7.write-back-unknown", yes).set("h7.write-back-again", yes)
			res := r.run("H7")
			check := func(im *flash.Image, when string) {
				cfg := mouse.Decode(em11Model(t), im)
				if cfg.Stages.State == flash.OK && cfg.Current.State == flash.OK && cfg.Current.Value >= cfg.Stages.Value {
					t.Errorf("%s the mouse holds current stage %d with %d stages (stage passed %v)", when, cfg.Current.Value+1, cfg.Stages.Value, res.Passed)
				}
			}
			sim, after := reset.Clone(), false
			for _, w := range emu.Logical(r.dev.Writes()) {
				switch p := w.Packet; {
				case p.Cmd() == wire.CmdClear:
					after = true
				case after && p.Cmd() == wire.CmdWrite:
					must(t, sim.Set(int(p.Addr()), p.Data()))
					check(sim, "after "+p.String())
				}
			}
			now := r.dev.Image()
			check(now, "after the stage")
			for _, e := range []flash.Extent{{Addr: mouse.AddrMaxDpiStage, Len: 2}, {Addr: mouse.AddrCurrentDPI, Len: 2}} {
				got, _ := now.Get(e)
				was, _ := r.start.Get(e)
				if left, _ := reset.Get(e); !bytes.Equal(got, was) && (yes || !bytes.Equal(got, left)) {
					t.Errorf("%v holds % x; % x before the reset, % x after it", e, got, was, left)
				}
			}
		})
	}
}

// A write-back the mouse's sleep stops can be tried again, from a new
// session, without another reset; the stage then does not pass.
func TestH7WriteBackCanBeRetried(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	r.script.yes("h7.write-back-again").on("h7.write-back-again", r.dev.Wake)
	var once sync.Once
	r.cfg.hook = func(e safety.OpEvent) {
		if e.Kind == safety.EventChunk && strings.HasPrefix(e.Op.Desc, "restore ") {
			once.Do(r.dev.Sleep)
		}
	}
	res := r.run("H7")
	if res.Passed || len(res.Promoted) != 0 || r.resets() != 1 {
		t.Fatalf("passed %v, promoted %v, %d resets, err %v", res.Passed, res.Promoted, r.resets(), res.Err)
	}
	r.unchanged()
	var ok int
	for _, s := range res.Steps {
		if s.Title == "every record equals the fresh backup" && s.OK {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("steps %+v", res.Steps)
	}
}

// A preparation question answered no stops the stage before the reset.
func TestH7PreparationGatesTheReset(t *testing.T) {
	r := newRig(t, nil)
	h7Script(r)
	r.script.set("h7.prep-pointer", false)
	res := r.run("H7")
	if !errors.Is(res.Err, ErrDeclined) || res.Recorded || r.resets() != 0 || len(r.cmd7()) != 0 {
		t.Fatalf("err %v, recorded %v, %d resets", res.Err, res.Recorded, r.resets())
	}
}

// Without "reset" typed out, nothing is sent.
func TestH7NeedsThePhrase(t *testing.T) {
	r := newRig(t, nil)
	h7Script(r)
	r.script.set("h7.phrase", "reset please")
	if res := r.run("H7"); !errors.Is(res.Err, ErrConfirm) || r.resets() != 0 {
		t.Fatalf("err %v, %d resets", res.Err, r.resets())
	}
}

// A dry run of H7 shows the reset packet and sends nothing.
func TestH7DryRunSendsNothing(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Gates.DryRun = true
	res := r.run("H7")
	if res.Err != nil || r.resets() != 0 || len(r.cmd7()) != 0 {
		t.Fatalf("err %v, %d resets", res.Err, r.resets())
	}
	if !strings.Contains(r.script.said.String(), "raw  09 00 00 00 00 00 00 00 00 00 00 00 00 00 00 44") {
		t.Errorf("the preview lacks the reset:\n%s", r.script.said.String())
	}
	if !strings.Contains(r.script.said.String(), `you will type "write experimental", then "reset", to go on`) {
		t.Errorf("the preview does not name both phrases:\n%s", r.script.said.String())
	}
}

// A run that stops once the reset went out leaves a checkpoint; the next
// run never sends the reset again, takes the first run's backup, not the
// reset mouse, as the one to write back, and removes the checkpoint once
// the write-back verified.
func TestH7ResumesAfterAStopWithoutResetting(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	delete(r.script.answers, "h7.write-back-extra")
	r.script.eof = true
	first := r.run("H7")
	if first.Passed || !first.Recorded || r.resets() != 1 || r.cmd7After() != 0 {
		t.Fatalf("first run: passed %v, recorded %v, %d resets, %d cmd 7 after it, err %v", first.Passed, first.Recorded, r.resets(), r.cmd7After(), first.Err)
	}
	cp := r.checkpoint()
	if cp == nil || cp.Kind != checkpointReset || !cp.Recorded || cp.Backup != first.Backups[0] {
		t.Fatalf("checkpoint %+v", cp)
	}
	r.script.eof = false
	r.script.yes("h7.write-back-extra")
	asked := len(r.script.asked)
	res := r.run("H7")
	if n := r.resets(); n != 1 {
		t.Fatalf("the mouse got cmd 9 %d times", n)
	}
	r.passed(res)
	r.unchanged()
	if r.checkpoint() != nil {
		t.Error("the checkpoint is still there after the write-back verified")
	}
	if again := r.script.asked[asked:]; !slices.Contains(again, "h7.resume") || slices.Contains(again, "h7.phrase") {
		t.Errorf("asked %v", r.script.asked)
	}
	if len(res.Transcripts) != 1 {
		t.Errorf("transcripts %v: the first run's was committed by its own record", res.Transcripts)
	}
}

// The unpaired case of the review: the first run stops at the re-pair
// prompt, the user re-pairs, and the next run writes the first run's
// backup back without a second reset; it does not pass, since whether the
// pairing survived is not known.
func TestH7InterruptedAtRepairIsNotResent(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	r.cfg.Wait = 500 * time.Millisecond
	var after *flash.Image
	r.cfg.afterReset = func() {
		after = r.dev.Image()
		must(t, r.dev.Pair(nil))
	}
	r.script.eof = true
	r.run("H7")
	if r.resets() != 1 || r.checkpoint() == nil {
		t.Fatalf("setup: %d resets, checkpoint %v", r.resets(), r.checkpoint())
	}
	must(t, r.dev.Pair(&emu.Mouse{Model: em11Model(t), Image: after, Factory: after, Firmware: testFirmware, Battery: emu.Battery{Level: 80}}))
	r.cfg.afterReset = nil
	r.script.eof = false
	r.script.set("h7.repaired", true)
	res := r.run("H7")
	if n := r.resets(); n != 1 {
		t.Fatalf("the mouse got cmd 9 %d times", n)
	}
	if res.Passed || len(res.Promoted) != 0 {
		t.Fatalf("passed %v, promoted %v", res.Passed, res.Promoted)
	}
	r.unchanged()
}

// The OS may report an error for a report the receiver already took: the
// reset still reaches the mouse once, and the next run goes on after it.
func TestH7ResetTakenWithAnErrorIsNotResent(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	r.dev.Inject(emu.Fault{Cmd: wire.CmdClear, Action: emu.Taken})
	first := r.run("H7")
	if first.Passed || r.resets() != 1 || r.checkpoint() == nil {
		t.Fatalf("first run: passed %v, %d resets, err %v", first.Passed, r.resets(), first.Err)
	}
	res := r.run("H7")
	if n := r.resets(); n != 1 {
		t.Fatalf("the mouse got cmd 9 %d times", n)
	}
	r.passed(res)
	r.unchanged()
}

// I11: when the mouse answers on another onboard profile after the reset,
// the stage asks before it writes the backup there, and never passes.
func TestH7OtherProfileAsksBeforeTheWriteBack(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			r := newRig(t, func(c *emu.Config) {
				factory(t)(c)
				p := byte(0)
				c.Mouse.Profile = &p
			})
			h7Script(r)
			r.script.set("h7.write-back-other", allow)
			r.cfg.afterReset = func() { must(t, r.dev.SetProfile(1)) }
			res := r.run("H7")
			if res.Passed || len(res.Promoted) != 0 || !slices.Contains(r.script.asked, "h7.write-back-other") {
				t.Fatalf("passed %v, promoted %v, asked %v", res.Passed, res.Promoted, r.script.asked)
			}
			if !strings.Contains(strings.Join(res.Findings, "\n"), "after the reset the mouse answers on onboard profile 1 (the fresh backup: onboard profile 0)") {
				t.Errorf("findings %v", res.Findings)
			}
			if n := r.cmd7After(); allow != (n > 0) {
				t.Errorf("%d cmd-7 packets after the reset with the override allowed %v", n, allow)
			}
			if !allow && r.checkpoint() == nil {
				t.Error("the checkpoint went while the backup is not back")
			}
		})
	}
}

// Records the release restore leaves out go back only when the user says
// so; otherwise they are listed as left changed.
func TestH7AsksBeforeWritingBackWhatTheRestoreLeavesOut(t *testing.T) {
	r := newRig(t, factory(t))
	h7Script(r)
	r.script.set("h7.write-back-extra", false)
	res := r.run("H7")
	r.passed(res)
	for _, op := range r.cmd7() {
		if a := int(op.Addr()); a == mouse.AddrReportRate {
			t.Errorf("the report rate was written back: %v", op)
		}
	}
	joined := strings.Join(res.Findings, "\n")
	if !strings.Contains(joined, "left as the reset changed them, at the user's choice: Report rate, Colour 2, Button slot 6") ||
		strings.Contains(joined, "the stage wrote them back at the experimental tier") {
		t.Errorf("findings:\n%s", joined)
	}
	if r.checkpoint() != nil {
		t.Error("the checkpoint is still there")
	}
}
