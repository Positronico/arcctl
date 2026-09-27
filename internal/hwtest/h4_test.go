//go:build hwtest

package hwtest

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// demoImage is the settings page and the shortcut bodies of the demo
// backup, which hold the maintainer's buttons: Cmd+V on the wheel click,
// Ctrl+Tab on Backward, Cmd+Tab on Forward.
func demoImage(t testing.TB) *flash.Image {
	t.Helper()
	f, err := backup.Load(filepath.Join(moduleRoot(t), "testdata", "demo-em11.json"))
	must(t, err)
	return f.Image()
}

func demoBuilder(t *testing.T, verified catalog.Verifications) *builder {
	t.Helper()
	b := dumpBuilder(t, "H4", verified)
	b.img = demoImage(t)
	return b
}

func demoRig(t *testing.T) *rig {
	t.Helper()
	return newRig(t, func(c *emu.Config) { c.Mouse.Image = demoImage(t) })
}

const (
	cmdV        = "04 80 08 00 81 19 00 41 19 00 40 08 00 8d"
	cmdTabSlot  = "04800800812b00412b0040080069ffffffffffff"
	cmdShiftTab = "06800800800200812b00412b00400200400800a3"
	ctrlTab     = "04800100812b00412b0040010077"
	rCmdTab     = "04808000812b00412b0040800079"
	playPause   = "0282cd0042cd00f5"
	menuBody    = "0287010047010083"
)

// twoPhaseOps are the ops of writing body extent e from old to new while
// binding k runs it.
func twoPhaseOps(k int, e flash.Extent, old, new, tier string) []string {
	b, _ := mouse.KeyFnExtent(k)
	return []string{
		b.String() + " 05000050 -> 00000055 " + tier,
		e.String() + " " + old + " -> " + new + " " + tier,
		b.String() + " 00000055 -> 05000050 " + tier,
	}
}

// The H4 steps on the maintainer's buttons, as §11 of the plan lists them.
func TestH4Plan(t *testing.T) {
	b := demoBuilder(t, nil)
	must(t, buildH4(b))
	body4 := ext(384, 20)
	checkH4 := func(ws []want) {
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
		if !equalImages(b.img, demoImage(t)) {
			t.Error("the stage does not leave the image as it found it")
		}
	}
	checkH4([]want{
		{kind: stepIdentity, ext: ext(320, 14), bytes: cmdV},
		{kind: stepAsk, id: "h4.identity"},
		{kind: stepApply, ops: twoPhaseOps(4, body4, cmdTabSlot, cmdShiftTab, "untested")},
		{kind: stepAsk, id: "h4.shift"},
		{kind: stepRevert, ops: twoPhaseOps(4, body4, cmdShiftTab, cmdTabSlot, "untested"), of: 2},
		{kind: stepAsk, id: "h4.shift-back"},
		{kind: stepApply, ops: twoPhaseOps(3, ext(352, 8), ctrlTab[:16], playPause, "untested")},
		{kind: stepAsk, id: "h4.media"},
		{kind: stepRevert, ops: twoPhaseOps(3, ext(352, 8), playPause, ctrlTab[:16], "untested"), of: 6},
		{kind: stepApply, ops: twoPhaseOps(3, ext(352, 14), ctrlTab, rCmdTab, "untested")},
		{kind: stepAsk, id: "h4.right-modifier"},
		{kind: stepRevert, ops: twoPhaseOps(3, ext(352, 14), rCmdTab, ctrlTab, "untested"), of: 9},
		{kind: stepApply, ops: twoPhaseOps(3, ext(352, 8), ctrlTab[:16], menuBody, "untested")},
		{kind: stepAsk, id: "h4.menu"},
		{kind: stepRevert, ops: twoPhaseOps(3, ext(352, 8), menuBody, ctrlTab[:16], "untested"), of: 12},
		{kind: stepAsk, id: "h4.restored"},
	})
	if id := identityPackets(b.steps[0]); len(id) != 2 {
		t.Errorf("the identity write takes %d packets, want 2", len(id))
	}
	if s := b.steps[0]; s.tier != catalog.Untested || !strings.Contains(s.title, "(Wheel Click, 2 chunks)") {
		t.Errorf("identity step %q at %s", s.title, s.tier)
	}
	extras := map[string]mouse.Feature{"h4.right-modifier": mouse.FeatureShortcutRightModifier, "h4.menu": mouse.FeatureShortcutMenu}
	for _, s := range b.steps {
		if s.kind != stepAsk {
			continue
		}
		if f, ok := extras[s.id]; (s.extra != f) || (ok != s.observe) {
			t.Errorf("%s: observe %v, promotes %q, want %q", s.id, s.observe, s.extra, f)
		}
		for _, private := range []string{"Cmd+V", "Ctrl+Tab", "Cmd+Shift+Tab"} {
			if strings.Contains(s.text, private) {
				t.Errorf("%s names the maintainer's shortcut %s in the records: %s", s.id, private, s.text)
			}
		}
	}
	if !strings.Contains(b.steps[3].aside, "Forward now sends Cmd+Shift+Tab; before the stage it sent Cmd+Tab.") {
		t.Errorf("the shift question's aside: %q", b.steps[3].aside)
	}
}

// Once H4 verified shortcuts and media, the extra cases still write at
// the tier of their own feature.
func TestH4ExtrasKeepTheirTier(t *testing.T) {
	vs := catalog.Verifications{
		{Model: em11, Feature: string(mouse.FeatureShortcut), Firmware: "v1.05", Stage: "H4"},
		{Model: em11, Feature: string(mouse.FeatureMedia), Firmware: "v1.05", Stage: "H4"},
	}
	b := demoBuilder(t, vs)
	must(t, buildH4(b))
	tiers := map[string]catalog.Tier{}
	for _, s := range b.steps {
		if s.kind == stepApply {
			tiers[s.title] = s.plan.Ops[1].Tier
		}
	}
	for title, tier := range tiers {
		want := catalog.Verified
		if strings.Contains(title, "RCmd") || strings.Contains(title, "Menu") {
			want = catalog.Untested
		}
		if tier != want {
			t.Errorf("%s: %s, want %s", title, tier, want)
		}
	}
	if b.steps[0].tier != catalog.Verified {
		t.Errorf("the identity write is %s", b.steps[0].tier)
	}
}

// On the dump's placeholder bodies Forward holds Cmd+Shift+Tab, so the
// change takes Shift out.
func TestH4TakesShiftOut(t *testing.T) {
	b := erased(dumpBuilder(t, "H4", nil))
	must(t, buildH4(b))
	ops := b.steps[2].plan.Ops
	if len(ops) != 3 {
		t.Fatalf("ops %+v", ops)
	}
	combo, err := mouse.DecodeShortcut(ops[1].New)
	must(t, err)
	if got := combo.Format(keys.Mac); got != "Cmd+Tab" {
		t.Errorf("Forward becomes %s, want Cmd+Tab", got)
	}
	if !strings.Contains(b.steps[2].title, "Shift taken out") || !strings.Contains(b.steps[3].text, "with Shift taken out?") {
		t.Errorf("step %q asks %q", b.steps[2].title, b.steps[3].text)
	}
}

func TestH4NeedsShortcutsBound(t *testing.T) {
	for _, slot := range []int{h4IdentitySlot, h4ShortcutSlot} {
		b := demoBuilder(t, nil)
		e, _ := mouse.KeyFnExtent(slot)
		must(t, b.img.Set(e.Addr, disabled))
		if err := buildH4(b); err == nil || !strings.Contains(err.Error(), "bound to its shortcut body") {
			t.Errorf("slot %d disabled: %v", slot, err)
		}
	}
}

// The identity write takes the features PlanEdits would for its body.
func TestH4BodyFeatures(t *testing.T) {
	b := demoBuilder(t, nil)
	tab, v := keys.Stroke{Kind: keys.KindKey, Value: usageTab}, keys.Stroke{Kind: keys.KindKey, Value: 0x19}
	shortcut, media := mouse.FeatureShortcut, mouse.FeatureMedia
	for _, c := range []struct {
		combo keys.Combo
		want  []mouse.Feature
	}{
		{keys.Combo{keys.LMeta.Stroke(), v}, []mouse.Feature{shortcut}},
		{keys.Combo{{Kind: keys.KindConsumer, Value: usagePlayPause}}, []mouse.Feature{media}},
		{keys.Combo{{Kind: keys.KindConsumer, Value: 0x0001}}, []mouse.Feature{media, mouse.FeatureMediaCustom}},
		{keys.Combo{keys.RMeta.Stroke(), tab}, []mouse.Feature{shortcut, mouse.FeatureShortcutRightModifier}},
		{keys.Combo{{Kind: keys.KindMenu, Value: menuKey}}, []mouse.Feature{shortcut, mouse.FeatureShortcutMenu}},
		{keys.Combo{tab, keys.LMeta.Stroke()}, []mouse.Feature{shortcut, mouse.FeatureShortcutCustom}},
	} {
		body, err := mouse.EncodeShortcut(c.combo)
		must(t, err)
		e, _ := mouse.ShortcutExtent(h4IdentitySlot)
		e.Len = len(body)
		if got := b.bodyFeatures(e, c.combo, body); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.combo.Format(keys.Mac), got, c.want)
		}
	}
}

func TestToggleShift(t *testing.T) {
	tab := keys.Stroke{Kind: keys.KindKey, Value: usageTab}
	shift, cmd, ctrl, alt := keys.LShift.Stroke(), keys.LMeta.Stroke(), keys.LCtrl.Stroke(), keys.LAlt.Stroke()
	for _, c := range []struct {
		in, out keys.Combo
		added   bool
	}{
		{keys.Combo{cmd, tab}, keys.Combo{cmd, shift, tab}, true},
		{keys.Combo{tab}, keys.Combo{shift, tab}, true},
		{keys.Combo{shift, cmd, tab}, keys.Combo{cmd, tab}, false},
		{keys.Combo{ctrl, alt, cmd, shift, tab}, keys.Combo{ctrl, alt, cmd, tab}, false},
	} {
		out, added, err := toggleShift(c.in)
		if err != nil || added != c.added || !slices.Equal(out, c.out) {
			t.Errorf("toggleShift(%s) = %s, %v, %v", c.in.Format(keys.Mac), out.Format(keys.Mac), added, err)
		}
	}
	for _, in := range []keys.Combo{
		nil,
		{{Kind: keys.KindConsumer, Value: usagePlayPause}},
		{cmd, {Kind: keys.KindMenu, Value: menuKey}},
		{ctrl, alt, cmd, keys.RShift.Stroke(), tab},
	} {
		if _, _, err := toggleShift(in); !errors.Is(err, errShift) {
			t.Errorf("toggleShift(%s): %v", in.Format(keys.Mac), err)
		}
	}
}

// h4Answers are the answers of a run in which everything works as it
// should; extras are the answers to the two extra cases.
func h4Answers(r *rig, rightModifier, menu bool) {
	r.script.yes("h4.run", "h4.identity", "h4.shift", "h4.shift-back", "h4.media", "h4.restored").
		set("h4.right-modifier", rightModifier).set("h4.menu", menu).set("h4.confirm", "write untested")
}

// logicalAddrs are the addresses of the cmd-7 packets that reached the
// device, resends left out.
func (r *rig) logicalAddrs() []int {
	var out []int
	for _, p := range r.logicalCmd7() {
		out = append(out, int(p.Addr()))
	}
	return out
}

// H4 end to end on the maintainer's buttons: the identity write, the
// two-phase change and the media case, each tried and reverted, and the
// extra cases, of which only the one the user saw is promoted.
func TestH4RunsAndPromotes(t *testing.T) {
	r := demoRig(t)
	h4Answers(r, true, false)
	res := r.run("H4")
	r.passed(res)
	r.unchanged()
	r.checkWrites(0)
	r.checkRedacted(res)

	const bind4, body4, bind3, body3 = 112, 384, 108, 352
	twoPhase := func(bind, body, chunks int) []int {
		out := []int{bind}
		for i := range chunks {
			out = append(out, body+i*wire.MaxData)
		}
		return append(out, bind)
	}
	want := []int{320, 330}
	for _, w := range [][]int{twoPhase(bind4, body4, 2), twoPhase(bind4, body4, 2), twoPhase(bind3, body3, 1), twoPhase(bind3, body3, 1),
		twoPhase(bind3, body3, 2), twoPhase(bind3, body3, 2), twoPhase(bind3, body3, 1), twoPhase(bind3, body3, 1)} {
		want = append(want, w...)
	}
	if got := r.logicalAddrs(); !slices.Equal(got, want) {
		t.Fatalf("cmd 7 went to\n got %v\nwant %v", got, want)
	}
	sent := r.logicalCmd7()
	if id := strings.Join(strs(sent[:2]), "\n"); !strings.Contains(id, "07 00 01 40 0a "+cmdV[:29]) || !strings.Contains(id, "07 00 01 4a 04 "+cmdV[30:]) {
		t.Errorf("the identity write sent\n%s", id)
	}
	if d := sent[2].Data(); !slices.Equal(d, disabled) {
		t.Errorf("the two-phase change starts with % x, want Forward disabled", d)
	}

	vs := r.verified()
	var got []string
	for _, v := range vs {
		got = append(got, v.Feature+" "+v.Stage)
	}
	if want := []string{"button.media H4", "button.shortcut H4", "button.shortcut.right-modifier H4"}; !slices.Equal(got, want) {
		t.Errorf("verified.json %v, want %v", got, want)
	}
	if len(res.Promoted) != 3 || r.generated != 1 {
		t.Errorf("promoted %v, generated %d times", res.Promoted, r.generated)
	}
	findings := strings.Join(res.Findings, "\n")
	for _, f := range []string{"RCmd+Tab acts as Cmd+Tab: yes", "the Menu key (kind 7) reaches the Mac: no",
		"not promoted: button.shortcut.menu, whose effect was not seen; it stays Untested"} {
		if !strings.Contains(findings, f) {
			t.Errorf("findings lack %q:\n%s", f, findings)
		}
	}
	log := r.read(logPath)
	for _, s := range []string{"### 2026-09-27 H4 (shortcut and media bodies): passed", "| H4 | shortcut and media bodies | passed 2026-09-27 (v0.42) |",
		"- Promoted: `button.shortcut.right-modifier` for 7B04 on v0.42", "read-back 14 bytes (not shown), equal"} {
		if !strings.Contains(log, s) {
			t.Errorf("the log lacks %q:\n%s", s, log)
		}
	}
	if strings.Contains(log, "button.shortcut.menu` for") {
		t.Error("the log promotes the Menu key")
	}
	said := r.script.said.String()
	if !strings.Contains(said, "Forward now sends Cmd+Shift+Tab; before the stage it sent Cmd+Tab.") || !strings.Contains(said, "Wheel Click runs Paste (Cmd+V).") {
		t.Errorf("the asides were not said:\n%s", said)
	}
	records := log
	for _, rel := range res.Transcripts {
		records += r.read(rel)
	}
	for _, private := range []string{cmdV[3:17], "80 01 00 81 2b", "80 08 00 81 2b", "Paste", "Cmd+V", "Ctrl+Tab", "Cmd+Shift+Tab"} {
		if strings.Contains(records, private) {
			t.Errorf("the records hold %q, from the maintainer's shortcuts", private)
		}
	}
}

// An extra case the user did not see stays Untested, and the stage still
// passes; a failed stage promotes nothing, the extras it saw included.
func TestH4ExtrasPromoteOnlyWhatWasSeen(t *testing.T) {
	t.Run("none seen", func(t *testing.T) {
		r := demoRig(t)
		h4Answers(r, false, false)
		res := r.run("H4")
		r.passed(res)
		r.unchanged()
		var got []string
		for _, v := range res.Promoted {
			got = append(got, v.Feature)
		}
		if want := []string{"button.shortcut", "button.media"}; !slices.Equal(got, want) {
			t.Errorf("promoted %v, want %v", got, want)
		}
		for _, f := range []string{"not promoted: button.shortcut.right-modifier", "not promoted: button.shortcut.menu"} {
			if !strings.Contains(strings.Join(res.Findings, "\n"), f) {
				t.Errorf("findings lack %q: %v", f, res.Findings)
			}
		}
	})
	t.Run("stage failed", func(t *testing.T) {
		r := demoRig(t)
		h4Answers(r, true, true)
		r.script.set("h4.media", false)
		res := r.run("H4")
		if res.Passed || res.Err != nil {
			t.Fatalf("passed %v, err %v", res.Passed, res.Err)
		}
		r.unchanged()
		if len(res.Promoted) != 0 || r.generated != 0 || len(r.verified()) != 0 {
			t.Errorf("a failed stage promoted %v", res.Promoted)
		}
	})
}

// A stopped H4 undoes the two-phase change it applied.
func TestH4StoppedUndoesTheChange(t *testing.T) {
	r := demoRig(t)
	r.script.yes("h4.run", "h4.identity").set("h4.confirm", "write untested")
	r.script.eof = true
	res := r.run("H4")
	if res.Passed || !errors.Is(res.Err, ErrNoInput) {
		t.Fatalf("passed %v, err %v", res.Passed, res.Err)
	}
	r.unchanged()
	if n := len(r.logicalCmd7()); n != 2+4+4 {
		t.Errorf("%d cmd-7 packets, want the identity write, the change and its revert", n)
	}
}

func TestDryRunPrintsH4(t *testing.T) {
	r := demoRig(t)
	r.cfg.Gates = safety.Gates{DryRun: true}
	log := r.read(logPath)
	res := r.run("H4")
	if res.Err != nil || !res.DryRun || res.Recorded {
		t.Fatalf("err %v, dry run %v, recorded %v", res.Err, res.DryRun, res.Recorded)
	}
	said := r.script.said.String()
	pk := previewPackets(said)
	if len(pk) != 30 {
		t.Errorf("the preview shows %d packets, want 30:\n%s", len(pk), said)
	}
	if len(pk) < 2 || !strings.HasPrefix(pk[0], "07 00 01 40 0a "+cmdV[:29]) || !strings.HasPrefix(pk[1], "07 00 01 4a 04 "+cmdV[30:]) {
		t.Errorf("the identity packets %v", pk[:min(2, len(pk))])
	}
	for _, s := range []string{"identity write of slot 2's shortcut body at 320+14 (Wheel Click, 2 chunks) (raw path; its 14 bytes stay as they are)",
		"The stage itself needs --allow-untested.", "Dry run: no backup was taken and nothing was written."} {
		if !strings.Contains(said, s) {
			t.Errorf("the dry run did not say %q:\n%s", s, said)
		}
	}
	if len(r.script.asked) != 0 {
		t.Errorf("the dry run asked %v", r.script.asked)
	}
	if n := len(r.cmd7()); n != 0 {
		t.Errorf("%d cmd-7 packets reached the device", n)
	}
	r.unchanged()
	if r.read(logPath) != log || r.read(verifiedPath) != "[]\n" || r.generated != 0 {
		t.Error("the dry run changed the records")
	}
	for _, dir := range []string{filepath.Join(r.repo, filepath.FromSlash(transcriptsDir)), r.cfg.Logs, r.cfg.Backups,
		r.cfg.Session.Writes.Backups.(backup.Store).Root, r.cfg.Session.Writes.Journal} {
		noFiles(t, dir)
	}
}

func TestStagesInOrder(t *testing.T) {
	want := []string{"H0", "H1", "H2", "H3", "H3b", "H4", "H5", "H6", "H7", "H9"}
	if got := Stages(); !slices.Equal(got, want) {
		t.Errorf("Stages() = %v, want %v", got, want)
	}
}

// The records show settings bytes but never those of a shortcut or macro
// slot.
func TestRecordsHideBodies(t *testing.T) {
	settings := wire.MustBuild(wire.Mouse, wire.CmdWrite, 4, []byte{0x03, 0x52})
	if got := shownPacket(settings); got != settings.String() {
		t.Errorf("a settings packet shows as %s", got)
	}
	body := wire.MustBuild(wire.Mouse, wire.CmdWrite, 320, []byte{0x04, 0x80, 0x08, 0x00})
	if got := shownPacket(body); got != "07 00 01 40 04 xx xx xx xx 00 00 00 00 00 00 xx" {
		t.Errorf("a body packet shows as %s", got)
	}
	e := ext(320, 4)
	for _, s := range []string{shown(e, []byte{4, 0x80, 8, 0}), holds(e, []byte{4, 0x80, 8, 0}, []byte{4, 0x80, 9, 0})} {
		if strings.Contains(s, "80") {
			t.Errorf("%q shows the body", s)
		}
	}
	if got := holds(ext(4, 2), []byte{3, 0x52}, []byte{2, 0x53}); got != "4+2 holds 03 52, want 02 53" {
		t.Errorf("holds on a settings pair: %s", got)
	}
}
