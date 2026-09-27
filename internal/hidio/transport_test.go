package hidio_test

import (
	"errors"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

func receive[T any](t *testing.T, ch <-chan T) (T, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(time.Second):
		t.Fatal("nothing received")
	}
	var zero T
	return zero, false
}

func quiet[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %v", v)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestTransportSplitsReports(t *testing.T) {
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	tr := hidio.Guarded(pipe, hidio.NewGuard(wire.Mouse))
	defer tr.Close()

	reply := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, []byte{1, 0x33, 0x22, 0x11})
	pipe.Deliver(7, []byte{0, 1, 0, 0xff, 0xff, 0, 0})
	pipe.Deliver(wire.ReportID, reply[:10])
	pipe.Deliver(5, []byte{0xcd, 0})
	pipe.Deliver(wire.ReportID, reply[:])

	r, ok := receive(t, tr.Reports())
	if !ok {
		t.Fatal("Reports closed")
	}
	if p, ok := r.Packet(); !ok || p != reply {
		t.Fatalf("Reports delivered %+v, want the 16-byte report 8", r)
	}
	quiet(t, tr.Reports())
	receive(t, tr.Wake())
	quiet(t, tr.Wake())
}

// A burst of movement reports must not push out a reply that arrived before
// it, even when nothing reads the input in the meantime.
func TestMovementCannotEvictAReply(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	tr := hidio.Guarded(pipe, hidio.NewGuard(wire.Mouse))
	defer tr.Close()
	reply := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	pipe.Deliver(wire.ReportID, reply[:])
	for range 1000 {
		pipe.Deliver(7, []byte{0, 1, 0, 0xff, 0xff, 0, 0})
	}
	r, ok := receive(t, tr.Reports())
	if p, _ := r.Packet(); !ok || p != reply {
		t.Fatalf("Reports delivered %+v, want the reply sent before the movement (dropped %d)", r, tr.Dropped())
	}
	receive(t, tr.Wake())
}

type wakingRaw struct {
	*hidio.Pipe
	wake chan struct{}
}

func (w wakingRaw) Wake() <-chan struct{} { return w.wake }

func TestTransportForwardsRawWake(t *testing.T) {
	raw := wakingRaw{hidio.NewPipe(func(wire.Packet) error { return nil }), make(chan struct{})}
	tr := hidio.Guarded(raw, hidio.NewGuard(wire.Mouse))
	defer tr.Close()
	raw.wake <- struct{}{}
	receive(t, tr.Wake())
	close(raw.wake)
	quiet(t, tr.Wake())
}

func TestTransportInputEnd(t *testing.T) {
	gone := errors.New("device removed")
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	tr := hidio.Guarded(pipe, hidio.NewGuard(wire.Mouse))
	pipe.Fail(gone)
	if _, ok := receive(t, tr.Reports()); ok {
		t.Fatal("Reports still open after the device went away")
	}
	if !errors.Is(tr.Err(), gone) {
		t.Fatalf("Err = %v, want %v", tr.Err(), gone)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); !errors.Is(err, hidio.ErrClosed) {
		t.Fatalf("Write after Close = %v, want ErrClosed", err)
	}
}

func TestTransportCloseEndsReports(t *testing.T) {
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	tr := hidio.Guarded(pipe, hidio.NewGuard(wire.Mouse))
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := receive(t, tr.Reports()); ok {
		t.Fatal("Reports still open after Close")
	}
	if tr.Err() != nil {
		t.Fatalf("Err after Close = %v, want nil", tr.Err())
	}
}

func TestTransportCountsDrops(t *testing.T) {
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	tr := hidio.Guarded(pipe, hidio.NewGuard(wire.Mouse))
	defer tr.Close()
	p := wire.MustBuild(wire.Mouse, wire.CmdStatusChanged, 0, []byte{4})
	const n = 3000
	for range n {
		pipe.Deliver(wire.ReportID, p[:])
	}
	deadline := time.Now().Add(time.Second)
	for {
		queued := len(tr.Reports())
		if uint64(queued)+tr.Dropped() == n && len(pipe.Reports()) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued %d + dropped %d, want %d", queued, tr.Dropped(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPipeStall(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	pipe := hidio.NewPipe(func(wire.Packet) error { <-block; return nil })
	pipe.SetWatchdog(20 * time.Millisecond)
	tr := hidio.Guarded(pipe, hidio.NewGuard(wire.Mouse))
	defer tr.Close()
	p := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	if err := tr.Write(p); !errors.Is(err, hidio.ErrStalled) || hidio.Classify(err) != hidio.ClassStalled {
		t.Fatalf("Write = %v, want ErrStalled", err)
	}
	start := time.Now()
	if err := tr.Write(p); !errors.Is(err, hidio.ErrStalled) || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("second Write = %v after %v, want an immediate ErrStalled", err, time.Since(start))
	}
}

func TestOpenRefusesBeforeTouchingDevices(t *testing.T) {
	g := hidio.NewGuard(wire.Mouse)
	if _, err := hidio.Open(hidio.Candidate{Backend: "usbhid", Path: "x", VID: 0x046D, PID: 0xC52B}, g, nil); !errors.Is(err, hidio.ErrNotFound) {
		t.Fatalf("Open of an unknown VID/PID = %v, want ErrNotFound", err)
	}
	if _, err := hidio.Open(hidio.Candidate{Backend: "nope", VID: 0x260D, PID: 0x1282}, g, nil); !errors.Is(err, hidio.ErrBackend) {
		t.Fatalf("Open with an unknown backend = %v, want ErrBackend", err)
	}
	if _, err := hidio.Enumerate("nope"); !errors.Is(err, hidio.ErrBackend) {
		t.Fatalf("Enumerate with an unknown backend = %v, want ErrBackend", err)
	}
	if !slices.Contains(hidio.Backends(), hidio.DefaultBackend) {
		t.Fatalf("Backends() = %v lacks the default", hidio.Backends())
	}
}
