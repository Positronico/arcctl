//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
)

// The raw path's identity write and NAK probe are writes like any other
// (I10): with a browser holding the receiver and no --allow-foreign-client,
// nothing reaches the mouse.
func TestRawWritesPassThePreflight(t *testing.T) {
	r := newRig(t, nil)
	r.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
	r.script.yes("h1.run", "h1.speed", "h1.speed-back").set("h1.confirm", "write untested")
	res := r.run("H1")
	if res.Passed || !errors.Is(res.Err, safety.ErrForeignClient) {
		t.Errorf("stage passed %v, err %v; want the foreign client to stop it", res.Passed, res.Err)
	}
	if got := r.cmd7(); len(got) > 0 {
		t.Fatalf("%d cmd 7 reached the mouse with a browser on the receiver: %v", len(got), strs(got))
	}
	r.unchanged()

	r2 := newRig(t, nil)
	r2.dev.AddClient(emu.Client{PID: 4242, Process: emu.ChromeProcess})
	r2.cfg.Gates.AllowForeignClient = true
	r2.script.yes("h1.run", "h1.speed", "h1.speed-back").set("h1.confirm", "write untested")
	r2.passed(r2.run("H1"))
}

// The raw path refuses the Secure Input state the preflight refuses.
func TestRawWritesRefuseSecureInput(t *testing.T) {
	r := newRig(t, nil)
	r.cfg.Session.Writes.Console = func() (safety.Console, error) { return safety.Console{SecureInput: "Terminal (pid 77)"}, nil }
	r.script.yes("h1.run").set("h1.confirm", "write untested")
	res := r.run("H1")
	if !errors.Is(res.Err, safety.ErrSecureInput) || len(r.cmd7()) > 0 {
		t.Fatalf("err %v, %d cmd 7", res.Err, len(r.cmd7()))
	}
}

// Ctrl-C while a write stage waits for the user does not leave its changes
// on the mouse: the reverts still owed run anyway, and the stage says so.
func TestInterruptStillReverts(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.script.yes("h3.run", "h3.inert", "h3.scroll", "h3.restored").set("h3.confirm", "write experimental")
	r.script.on("h3.scroll", cancel)
	res, err := Run(ctx, r.cfg, "H3")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(res.Err, context.Canceled) || res.Passed {
		t.Errorf("stage err %v, passed %v; want the interrupt", res.Err, res.Passed)
	}
	e3, _ := mouse.KeyFnExtent(3)
	now, _ := r.dev.Image().Get(e3)
	was, _ := r.start.Get(e3)
	if !bytes.Equal(now, was) {
		t.Fatalf("after the interrupt slot 3 holds % x; it held % x", now, was)
	}
	r.unchanged()
}
