//go:build hwtest

package hwtest

import (
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// H3's push check on the Backward button bound to DPI Cycle, on a mouse
// that pushes StatusChanged when its stage moves, on one that moves it
// without a push, and when the press does nothing: each is recorded, and
// the stage leaves the binding and the current stage as they were.
func TestH3PushCheck(t *testing.T) {
	for _, c := range []struct {
		name  string
		press func(r *rig) error
		saw   string
		back  bool
	}{
		{"push", func(r *rig) error { return r.dev.PressDPI() },
			"brought 1 StatusChanged push (cmd 10, flags 01 00); the current stage moved from 4 to 5", true},
		{"no push", func(r *rig) error {
			next, err := mouse.EncodeCurrentStage(4)
			if err != nil {
				return err
			}
			return r.dev.Store(mouse.AddrCurrentDPI, next[:])
		}, "brought no report-8 StatusChanged frame; the current stage moved from 4 to 5", true},
		{"nothing", func(*rig) error { return nil },
			"brought no report-8 StatusChanged frame; the current stage did not move", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.script.yes("h3.run", "h3.inert", "h3.scroll", "h3.restored").set("h3.confirm", "write experimental").
				on("h3.push", func() { must(t, c.press(r)) })
			res := r.run("H3")
			r.passed(res)
			r.unchanged()
			r.checkWrites(0)
			if got := r.script.text("h3.push"); got != "Press the Backward button once, then press Enter." {
				t.Errorf("the press says %q", got)
			}
			if f := strings.Join(res.Findings, "\n"); !strings.Contains(f, "DPI push: a press of the Backward button bound to DPI Cycle "+c.saw) {
				t.Errorf("findings:\n%s", f)
			}
			i := slices.IndexFunc(res.Steps, func(s StepResult) bool { return s.Title == "the current stage back to 4" })
			if i < 0 {
				t.Fatalf("no put back in %+v", res.Steps)
			}
			wrote := strings.Contains(strings.Join(res.Steps[i].Detail, "\n"), "records written and verified")
			if wrote != c.back {
				t.Errorf("the put back wrote %v, want %v: %v", wrote, c.back, res.Steps[i].Detail)
			}
			bound := false
			for _, p := range r.logicalCmd7() {
				bound = bound || p.Addr() == 108 && p.Data()[0] == byte(mouse.TypeDPI)
			}
			if !bound {
				t.Error("slot 3 was never bound to DPI Cycle")
			}
			var stage []wire.Packet
			for _, p := range r.logicalCmd7() {
				if p.Addr() == mouse.AddrCurrentDPI {
					stage = append(stage, p)
				}
			}
			if len(stage) > 0 != c.back {
				t.Errorf("current-stage writes %v", strs(stage))
			}
		})
	}
}
