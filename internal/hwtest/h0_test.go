//go:build hwtest

package hwtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// dpiOnWheel binds the Wheel Click button to DPI Cycle, as a mouse set up
// for the trace would be.
func dpiOnWheel(t *testing.T) func(*emu.Config) {
	return func(c *emu.Config) {
		cycle, err := mouse.EncodeKeyFn(mouse.KeyFn{Type: mouse.TypeDPI, Param: mouse.ParamDPICycle})
		must(t, err)
		e, _ := mouse.KeyFnExtent(2)
		must(t, c.Mouse.Image.Set(e.Addr, cycle))
	}
}

func (r *rig) readOnly() {
	r.t.Helper()
	for _, w := range r.dev.Writes() {
		if err := wire.ReadOnly.Check(w.Packet, wire.Mouse); err != nil {
			r.t.Errorf("H0 sent %v: %v", w.Packet, err)
		}
	}
}

func TestPickSteps(t *testing.T) {
	h0 := stageNamed(t, "H0")
	for _, c := range []struct {
		names []string
		want  []string
		err   string
	}{
		{names: nil},
		{names: []string{"unplug", "trace"}, want: []string{"trace", "unplug"}},
		{names: []string{"unplug", "unplug"}, want: []string{"unplug"}},
		{names: []string{"backups", "dump"}, want: []string{"backups", "dump"}},
		{names: []string{"dump"}, err: "step dump needs step backups in the same run"},
		{names: []string{"replug"}, err: `stage H0 has no step "replug" (have doctor, trace, info,`},
		{names: Steps("H0")},
	} {
		got, err := h0.pick(c.names)
		switch {
		case c.err != "":
			if !errors.Is(err, ErrSteps) || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v: %v, want %q", c.names, err, c.err)
			}
		case err != nil || !slices.Equal(got, c.want):
			t.Errorf("%v: %v %v, want %v", c.names, got, err, c.want)
		}
	}
	if err := CheckSteps("H1", []string{"trace"}); !errors.Is(err, ErrSteps) || !strings.Contains(err.Error(), "stage H1 runs whole; --steps is for H0") {
		t.Errorf("H1 with steps: %v", err)
	}
	if got := Steps("H0"); len(got) != len(h0Steps) || got[10] != "unplug" || len(Steps("H1")) != 0 {
		t.Errorf("steps %v", got)
	}
	for _, s := range h0Steps {
		if strings.ContainsAny(s.name, " ,") {
			t.Errorf("step name %q", s.name)
		}
	}
}

// The mouse sleeps after the replug, as on the maintainer's unit: the
// receiver gives its address at once, arcctl asks once for the mouse to be
// moved, and the step passes when the session is Ready again.
func TestH0UnplugWaitsForTheMouse(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Steps = []string{"unplug"}
	r.script.yes("h0.run").
		on("h0.unplug", r.dev.Unplug).
		on("h0.plug", func() { r.dev.Sleep(); r.dev.Plug() }).
		hear(hint, r.dev.Wake)
	res := r.run("H0")
	r.passed(res)
	r.readOnly()
	if n := strings.Count(r.script.saidText(), hint); n != 1 {
		t.Errorf("the hint came %d times", n)
	}
	if len(res.Steps) != 1 || res.Steps[0].Name != "unplug" {
		t.Fatalf("steps %+v", res.Steps)
	}
	detail := strings.Join(res.Steps[0].Detail, "\n")
	for _, want := range []string{"the session saw the receiver go: yes", "the mouse asleep", "arcctl asked for it to be moved",
		"back to Ready", "cmd-3 address across the replug: unchanged"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the step lacks %q:\n%s", want, detail)
		}
	}
	if f := strings.Join(res.Findings, "\n"); !strings.Contains(f, "unplug while idle: seen yes; address across a replug unchanged; the mouse asleep after the replug; Ready again after") ||
		strings.Contains(f, "exit criteria") || strings.Contains(f, "to do by hand") {
		t.Errorf("findings:\n%s", f)
	}
	if !slices.Equal(res.Transcripts, []string{"testdata/transcripts/2026-09-27-h0-steps.jsonl"}) {
		t.Errorf("transcripts %v", res.Transcripts)
	}
	r.checkRedacted(res)
	log := r.read(logPath)
	for _, want := range []string{"### 2026-09-27 H0 (steps: unplug): passed", "- Device: ProtoArc EM11 Pro (7B04, mid 4); mouse firmware v0.42",
		"  1. unplug while idle, address across a replug (`unplug`): ok",
		"| H0 | read-only session, latency, coexistence | failed 2026-09-27 (v0.42); 14 of 15 steps not passed yet |"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q", want)
		}
	}
	if res.Status != "failed 2026-09-27 (v0.42); 14 of 15 steps not passed yet" {
		t.Errorf("status %q", res.Status)
	}
}

// A mouse that is never moved after the replug fails the step, as on
// 2026-09-27, but the address across the replug is known all the same.
func TestH0UnplugWithTheMouseLeftAsleep(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Steps = []string{"unplug"}
	r.cfg.Wait = time.Second
	r.script.yes("h0.run").
		on("h0.unplug", r.dev.Unplug).
		on("h0.plug", func() { r.dev.Sleep(); r.dev.Plug() })
	res := r.run("H0")
	if res.Passed || res.Err != nil || len(res.Steps) != 1 {
		t.Fatalf("passed %v, err %v, steps %+v", res.Passed, res.Err, res.Steps)
	}
	detail := strings.Join(res.Steps[0].Detail, "\n")
	for _, want := range []string{"the mouse asleep", "not Ready again: still offline", "cmd-3 address across the replug: unchanged"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the step lacks %q:\n%s", want, detail)
		}
	}
	if n := strings.Count(r.script.saidText(), hint); n != 1 {
		t.Errorf("the hint came %d times", n)
	}
}

// A run of some steps completes a stage whose other steps passed in an
// earlier run on the same firmware, even one logged before steps had names;
// runs on other firmware and rehearsals do not count.
func TestH0StepsCompleteTheStage(t *testing.T) {
	r := newRig(t, nil)
	var b strings.Builder
	for _, fw := range []string{"v0.42", "v1.26"} {
		b.WriteString("\n\n### 2026-09-26 H0 (read-only session, latency, coexistence): failed\n\n")
		b.WriteString("- Device: ProtoArc EM11 Pro (7B04, mid 4); mouse firmware " + fw + ", receiver v1.0; wireless 1 kHz; profile none\n")
		b.WriteString("- Run: 18:54 to 19:09 UTC\n- Steps:\n")
		for i, s := range h0Steps {
			mark := "ok"
			if s.name == "unplug" || s.name == "cable" && fw == "v0.42" {
				mark = "failed"
			}
			fmt.Fprintf(&b, "  %d. %s: %s\n     - detail: ok\n", i+1, s.title, mark)
		}
	}
	b.WriteString("\n### 2026-09-26 H0 (steps: unplug): passed\n\n- Device: ProtoArc EM11 Pro (7B04, mid 4); mouse firmware v0.42, receiver v1.0; wireless 1 kHz; profile none\n" +
		"- Run: 19:00 to 19:01 UTC; a rehearsal, not a hardware result\n- Steps:\n  1. USB-C cable (`cable`): ok\n")
	path := filepath.Join(r.repo, logPath)
	old, err := os.ReadFile(path)
	must(t, err)
	must(t, os.WriteFile(path, append(old, b.String()...), 0o644))

	r.cfg.Steps = []string{"unplug"}
	r.script.yes("h0.run").on("h0.unplug", r.dev.Unplug).on("h0.plug", r.dev.Plug)
	res := r.run("H0")
	r.passed(res)
	if want := "failed 2026-09-27 (v0.42); not passed yet: cable"; res.Status != want {
		t.Errorf("status %q, want %q", res.Status, want)
	}
	r.cfg.Steps = []string{"cable"}
	res = r.run("H0")
	r.passed(res)
	if want := "passed 2026-09-27 (v0.42), its steps over 3 runs"; res.Status != want {
		t.Errorf("status %q, want %q", res.Status, want)
	}
	log := r.read(logPath)
	if !strings.Contains(log, "| H0 | read-only session, latency, coexistence | passed 2026-09-27 (v0.42), its steps over 3 runs |") {
		t.Error("the table does not show the stage passed")
	}
	if !strings.Contains(log, "`testdata/transcripts/2026-09-27-h0-steps-2.jsonl`") {
		t.Error("the second run of some steps does not have its own transcript")
	}
}

func TestStatusNamesWhatIsLeft(t *testing.T) {
	steps := h0Infos()
	res := &Result{Stage: "H0", Started: testTime, Device: Device{Key: em11, Mouse: "v1.26"}}
	for _, s := range steps {
		res.Steps = append(res.Steps, StepResult{Name: s.name, Title: s.title, OK: s.name != "unplug" && s.name != "trace"})
	}
	if got := status("", res, steps); got != "failed 2026-09-27 (v1.26); not passed yet: trace, unplug" {
		t.Errorf("status %q", got)
	}
	res.Rehearsal = true
	for i := range res.Steps {
		res.Steps[i].OK = true
	}
	if got := status("", res, steps); got != "passed 2026-09-27 (v1.26), rehearsal" {
		t.Errorf("status %q", got)
	}
}

// Without a button that runs a DPI function the trace presses nothing and
// says the push check cannot be made; it never reads as "no pushes".
func TestH0TraceWithoutADPIButton(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Steps = []string{"trace"}
	r.script.yes("h0.run")
	res := r.run("H0")
	r.passed(res)
	r.readOnly()
	if slices.Contains(r.script.asked, "h0.trace-dpi") {
		t.Error("the trace asked for a DPI press")
	}
	f := strings.Join(res.Findings, "\n")
	if !strings.Contains(f, "trace: DPI button -> not testable: no button is bound to a DPI function | sleep -> ") ||
		strings.Contains(f, "DPI button -> no report-8 frame") {
		t.Errorf("findings:\n%s", f)
	}
	if !strings.Contains(strings.Join(res.Steps[0].Detail, "\n"), "DPI button: not testable: no button is bound to a DPI function") {
		t.Errorf("detail %v", res.Steps[0].Detail)
	}
}

// With a button on DPI Cycle the trace names it, and its press brings the
// StatusChanged push.
func TestH0TraceNamesTheDPIButton(t *testing.T) {
	r := newRig(t, dpiOnWheel(t))
	r.cfg.Steps = []string{"trace"}
	r.script.yes("h0.run").on("h0.trace-dpi", func() { must(t, r.dev.PressDPI()) })
	res := r.run("H0")
	r.passed(res)
	r.readOnly()
	if got := r.script.text("h0.trace-dpi"); got != "Press the Wheel Click button (DPI Cycle) once, then press Enter." {
		t.Errorf("the instruction says %q", got)
	}
	if f := strings.Join(res.Findings, "\n"); !strings.Contains(f, "trace: DPI button (Wheel Click, DPI Cycle) -> cmd 10 flags 01 00") {
		t.Errorf("findings:\n%s", f)
	}
}
