package tui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// TestEmulatedApply runs the shell over a real session and an emulated EM11
// Pro: the snapshots arrive through the watcher, and a staged DPI edit goes
// through the session's apply, journal and read-back.
func TestEmulatedApply(t *testing.T) {
	bus := emu.New(emu.Options{})
	t.Cleanup(bus.Close)
	dev, err := bus.Add(emu.Config{Mouse: &emu.Mouse{Model: em11(), Image: dumpImage(t), Firmware: emu.Version{Major: 1},
		Battery: emu.Battery{Level: 80, MilliVolts: 3900}}})
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	s := session.New(session.Options{
		Devices: bus,
		Timing:  session.Timing{Try: 25 * time.Millisecond, ProbeTry: 25 * time.Millisecond, Online: time.Hour, Battery: time.Hour},
		Writes: &session.Writes{
			Journal:  filepath.Join(tmp, "journal"),
			Backups:  backup.Store{Root: filepath.Join(tmp, "backups"), Tool: "arcctl test", Source: backup.SourceEmulator},
			Lock:     func() error { return nil },
			Executor: safety.Options{OfflineWait: 300 * time.Millisecond, Poll: 5 * time.Millisecond},
		},
	})
	run := make(chan struct{})
	h := newHarness(t, s.Snapshot(), func(o *Options) {
		o.Session = s
		o.Gates.AllowUntested = true
		o.Source = "emulator"
	})
	go func() {
		defer close(run)
		_ = s.Run(h.app.ctx)
	}()
	t.Cleanup(func() {
		h.app.Close()
		<-run
	})
	h.flush(func() bool { return h.app.sn.State == session.Ready && h.app.sn.Journal != nil })
	h.app.Pending().Stage(Staged{Key: DPIKey(1), Desc: "DPI 2", Edit: mouse.SetDPI{Stage: 1, DPI: 1600}})
	p := stagedPlan(t, h)
	g := h.app.opt.Gates
	g.Confirm = safety.ConfirmPhrase(p.Ops)
	h.startWrite(applyRequest(p, g))
	h.flush(func() bool { return h.app.write == nil && h.app.notice.text != "" })
	if !strings.HasPrefix(h.app.notice.text, "Apply done: 1 of 1 records verified") || h.app.Pending().Len() != 0 {
		t.Fatalf("notice %q, %d pending", h.app.notice.text, h.app.Pending().Len())
	}
	op := p.Ops[0]
	if got := dev.Image().Bytes()[op.Extent.Addr:op.Extent.End()]; !bytes.Equal(got, op.New) {
		t.Errorf("the mouse holds % x at %v, want % x", got, op.Extent, op.New)
	}
	h.flush(func() bool {
		cfg := h.app.config
		return h.app.sn.State == session.Ready && cfg != nil && cfg.DPI[1].DPI.X == 1600
	})
}
