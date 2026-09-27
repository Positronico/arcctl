package tui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// rvEmulated is the shell with the review over a real session on an emulated
// EM11 Pro whose flash holds the shortcut bodies its bindings run.
type rvEmulated struct {
	h   *harness
	dev *emu.Device
	dir string
}

func rvEmulate(t *testing.T, opts ...func(*Options)) *rvEmulated {
	t.Helper()
	bus := emu.New(emu.Options{})
	t.Cleanup(bus.Close)
	im, err := emu.WithBodies(dumpImage(t))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := bus.Add(emu.Config{Mouse: &emu.Mouse{Model: em11(), Image: im, Firmware: emu.Version{Major: 1},
		Battery: emu.Battery{Level: 80, MilliVolts: 3900}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := session.New(session.Options{
		Devices: bus,
		Clients: rvClientsOf(bus),
		Timing:  session.Timing{Try: 25 * time.Millisecond, ProbeTry: 25 * time.Millisecond, Online: time.Hour, Battery: time.Hour},
		Writes: &session.Writes{
			Journal:  filepath.Join(dir, "journal"),
			Backups:  backup.Store{Root: filepath.Join(dir, "backups"), Tool: "arcctl test", Source: backup.SourceEmulator},
			Lock:     func() error { return nil },
			Executor: safety.Options{OfflineWait: 300 * time.Millisecond, Poll: 5 * time.Millisecond},
		},
	})
	opts = append([]func(*Options){func(o *Options) {
		o.Session = s
		o.Source = "emulator"
		o.Review = NewReview(s)
	}}, opts...)
	h := newHarness(t, s.Snapshot(), opts...)
	run := make(chan struct{})
	go func() {
		defer close(run)
		_ = s.Run(h.app.ctx)
	}()
	t.Cleanup(func() {
		h.app.Close()
		<-run
	})
	h.flush(func() bool { return h.app.sn.State == session.Ready && h.app.sn.Journal != nil })
	return &rvEmulated{h: h, dev: dev, dir: dir}
}

// rvClientsOf is the IORegistry scan over the emulator's clients, arcctl's
// own handles left out.
func rvClientsOf(b *emu.Bus) func(hidio.Candidate) ([]session.Client, error) {
	return func(c hidio.Candidate) ([]session.Client, error) {
		var out []session.Client
		for _, x := range b.Clients() {
			if x.Path == c.Path && x.PID != os.Getpid() {
				out = append(out, session.Client{PID: x.PID, Name: x.Process, Seized: x.Seized})
			}
		}
		return out, nil
	}
}

// playPause stages the M4 acceptance edit: slot 3 to Play/Pause.
func (e *rvEmulated) playPause() {
	e.h.app.Pending().Stage(Staged{Key: SlotKey(3), Desc: "Back (side): Play/Pause", Edit: mouse.SetMedia{Slot: 3, Usage: rvPlayPause}})
}

// confirm opens the review and waits for the session's preflight to pass.
func (e *rvEmulated) confirm(t *testing.T) *reviewDialog {
	t.Helper()
	h := e.h
	h.keys("a", "enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewConfirm {
		t.Fatalf("the preflight did not pass: %v\n%s", d.checked, rvScreen(h))
	}
	return d
}

func (e *rvEmulated) holds(t *testing.T, ext flash.Extent, want []byte) {
	t.Helper()
	if got, _ := e.dev.Image().Get(ext); !bytes.Equal(got, want) {
		t.Errorf("the mouse holds % x at %v, want % x", got, ext, want)
	}
}

// final is what each extent the plan writes holds once it is done.
func rvFinal(p plan.Plan) map[flash.Extent][]byte {
	out := map[flash.Extent][]byte{}
	for _, op := range p.Ops {
		out[op.Extent] = op.New
	}
	return out
}

func (e *rvEmulated) journal(t *testing.T) *safety.Status {
	t.Helper()
	st, err := safety.Load(filepath.Join(e.dir, "journal", e.h.app.sn.Identity.Key()))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (e *rvEmulated) writes() []wire.Packet {
	var out []wire.Packet
	for _, w := range e.dev.Writes() {
		if w.Packet.Cmd() == wire.CmdWrite {
			out = append(out, w.Packet)
		}
	}
	return out
}

func TestReviewEmulatedDryRun(t *testing.T) {
	e := rvEmulate(t, mode(ModeDryRun))
	h := e.h
	e.playPause()
	before := e.dev.Image()
	d := e.confirm(t)
	p := d.plan
	if len(p.Ops) != 3 {
		t.Fatalf("%d ops, want the two-phase three", len(p.Ops))
	}
	h.keys("y")
	h.flush(func() bool { return d.step == reviewDone })
	if d.run.end() != reviewEndVerified {
		t.Fatalf("the dry run ended with %v\n%s", d.run.done.Err, rvScreen(h))
	}
	var want []wire.Packet
	for _, op := range p.Ops {
		want = append(want, wire.MustBuild(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr), op.New))
	}
	got := d.run.done.Outcome.Packets
	if !slices.Equal(got, want) {
		t.Errorf("the overlay got\n%v\nwant\n%v", got, want)
	}
	if w := e.writes(); len(w) > 0 {
		t.Errorf("a dry run sent %d cmd-7 packets to the mouse", len(w))
	}
	for ext := range rvFinal(p) {
		b, _ := before.Get(ext)
		e.holds(t, ext, b)
	}
	lines := []string{"Dry run done: 3 of 3 records verified against the overlay", "3 packets sent to the overlay"}
	for _, pk := range want {
		lines = append(lines, pk.String())
	}
	rvWant(t, h, lines...)
	h.flush(func() bool {
		if h.app.sn.DryRun == nil {
			return false
		}
		for ext, b := range rvFinal(p) {
			if got, _ := h.app.sn.DryRun.Get(ext); !bytes.Equal(got, b) {
				return false
			}
		}
		return true
	})
	if n := h.app.Pending().Len(); n != 0 {
		t.Errorf("%d edits pending after the dry run", n)
	}
}

func TestReviewEmulatedApply(t *testing.T) {
	e := rvEmulate(t, rvUntested)
	h := e.h
	e.playPause()
	stage(h, 1, 1600)
	d := e.confirm(t)
	p := d.plan
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	if d.run.end() != reviewEndVerified {
		t.Fatalf("the apply ended with %v\n%s", d.run.done.Err, rvScreen(h))
	}
	out := d.run.done.Outcome
	if out.Verified != 4 || len(out.Backups) == 0 || !strings.Contains(filepath.Base(out.Backups[0]), "first-write") {
		t.Errorf("outcome %+v", out)
	}
	for ext, b := range rvFinal(p) {
		e.holds(t, ext, b)
	}
	st := e.journal(t)
	if r := st.Find(out.Run); r == nil || !r.Complete || !st.Clean() {
		t.Errorf("the journal holds %+v", r)
	}
	for _, s := range d.run.w.steps {
		if s.kind != safety.EventVerified {
			t.Errorf("op %d ended %v", s.op.Seq, s.kind)
		}
	}
	rvWant(t, h, "Applied: 4 of 4 records verified", "is in the journal; U reverts it")
	h.flush(func() bool {
		cfg := h.app.config
		return h.app.sn.State == session.Ready && cfg != nil && cfg.DPI[1].DPI.X == 1600
	})
}

// A NAK on the body write stops the two-phase rebind with the binding
// disabled; the journal holds the run open and the review settles it.
func TestReviewEmulatedStopAndRecover(t *testing.T) {
	e := rvEmulate(t, rvUntested)
	h := e.h
	e.playPause()
	d := e.confirm(t)
	p := d.plan
	neutralise, body := p.Ops[0], p.Ops[1]
	h.typeText("write untested")
	e.dev.Inject(emu.Fault{Match: emu.Nth(wire.CmdWrite, 2), Action: emu.NAK, Times: 1})
	h.keys("enter")
	h.flush(func() bool { return d.step == reviewDone })
	var stop *safety.StopError
	if !errors.As(d.run.done.Err, &stop) || stop.Op.Seq != body.Seq || !errors.Is(stop, wire.ErrNAK) {
		t.Fatalf("the apply ended with %v", d.run.done.Err)
	}
	e.holds(t, neutralise.Extent, neutralise.New)
	h.flush(func() bool { return d.run.rec != nil })
	if h.app.sn.State != session.Recovering {
		t.Errorf("state %v, want Recovering", h.app.sn.State)
	}
	rvWant(t, h, "Stopped: 1 of 3 records verified", "rejected by the device", "Unfinished write", "mid", "l  leave: mark it settled")
	if n := h.app.Pending().Len(); n != 1 {
		t.Errorf("%d edits pending after the stop, want 1", n)
	}
	h.keys("f")
	if _, ok := h.app.dialog.(*Confirm); !ok {
		t.Fatalf("f opened %T", h.app.dialog)
	}
	h.keys("y")
	h.flush(func() bool { return h.app.write == nil && strings.HasPrefix(h.app.notice.text, "Recovery done") })
	h.flush(func() bool {
		return h.app.sn.State == session.Ready && h.app.sn.Journal != nil && len(h.app.sn.Journal.Open) == 0
	})
	for ext, b := range rvFinal(p) {
		e.holds(t, ext, b)
	}
	st := e.journal(t)
	if !st.Clean() {
		t.Errorf("open runs %v", st.Open)
	}
	if r := st.Find(d.run.done.Outcome.Run); r == nil || r.Resolved != safety.Forward {
		t.Errorf("the stopped run is %+v", r)
	}
}

func TestReviewEmulatedRefusedByForeignClient(t *testing.T) {
	e := rvEmulate(t, rvUntested)
	h := e.h
	stage(h, 1, 1600)
	e.dev.AddClient(emu.Client{PID: 4242, Process: "Google Chrome Helper"})
	h.keys("a", "enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewPlan || len(d.checked) == 0 || !errors.Is(d.checked[0], safety.ErrForeignClient) {
		t.Fatalf("step %d, checked %v", d.step, d.checked)
	}
	rvWant(t, h, "Google Chrome Helper")
	if w := e.writes(); len(w) > 0 {
		t.Errorf("a refused write sent %d packets", len(w))
	}
	if _, err := os.Stat(filepath.Join(e.dir, "backups")); err == nil {
		t.Error("a refused write saved a backup")
	}
}

// applied applies slot 3 -> Play/Pause through the review and waits for the
// journal to hold it.
func (e *rvEmulated) applied(t *testing.T) {
	t.Helper()
	h := e.h
	e.playPause()
	d := e.confirm(t)
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return h.app.write == nil && d.step == reviewDone })
	h.keys("esc")
	h.flush(func() bool {
		sn := h.app.sn
		return sn.State == session.Ready && sn.Journal != nil && sn.Journal.Last != nil && h.app.config != nil &&
			h.app.config.Shortcuts[3].Format(keys.Mac) == "Play/Pause"
	})
}

// The revert review decodes each record before and after, as the apply
// review does, so a button revert names the function it puts back.
func TestReviewEmulatedRevertNamesBothSides(t *testing.T) {
	e := rvEmulate(t, rvUntested)
	h := e.h
	e.applied(t)
	h.keys("U")
	d := rvDialog(t, h)
	if d.kind != safety.KindRevert || d.planErr != nil {
		t.Fatalf("U opened a review of %v: %v", d.kind, d.planErr)
	}
	rvWant(t, h, "Review the revert", "Play/Pause → Ctrl+Tab", "which changed 2 records")
	h.keys("enter")
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewConfirm {
		t.Fatalf("the preflight did not pass: %v", d.checked)
	}
	h.typeText("write untested")
	h.keys("enter")
	h.flush(func() bool { return h.app.write == nil && d.step == reviewDone })
	if d.run.end() != reviewEndVerified {
		t.Fatalf("the revert ended with %v", d.run.done.Err)
	}
	rvWant(t, h, "Reverted: ")
}

// The revert review runs the session's preflight before it asks for the
// phrase, and names what blocks the write.
func TestReviewEmulatedRevertPreflight(t *testing.T) {
	e := rvEmulate(t, rvUntested)
	h := e.h
	e.applied(t)
	n := len(e.writes())
	e.dev.AddClient(emu.Client{PID: 4242, Process: "Google Chrome Helper"})
	h.keys("U", "enter")
	d := rvDialog(t, h)
	h.flush(func() bool { return d.step != reviewChecking })
	if d.step != reviewPlan || len(d.checked) == 0 || !errors.Is(d.checked[0], safety.ErrForeignClient) {
		t.Fatalf("step %d, checked %v", d.step, d.checked)
	}
	rvWant(t, h, "Google Chrome Helper")
	if len(e.writes()) != n {
		t.Error("the refused revert sent packets")
	}
}
