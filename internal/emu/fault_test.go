package emu_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

func TestReplyFaults(t *testing.T) {
	tests := []struct {
		name   string
		faults []emu.Fault
		// replies[i] is how many replies the i-th cmd-8 read gets; -1 means a NAK.
		replies []int
	}{
		{"drop the second read", []emu.Fault{{Cmd: wire.CmdRead, Skip: 1, Times: 1, Action: emu.Drop}}, []int{1, 0, 1}},
		{"NAK every read", []emu.Fault{{Cmd: wire.CmdRead, Action: emu.NAK}}, []int{-1, -1}},
		{"duplicate the first read", []emu.Fault{{Cmd: wire.CmdRead, Times: 1, Action: emu.Duplicate}}, []int{2, 1}},
		{"match by address", []emu.Fault{{Match: func(p wire.Packet) bool { return p.Addr() == 10 }, Action: emu.Drop}}, []int{1, 0, 1}},
		{"other command untouched", []emu.Fault{{Cmd: wire.CmdBattery, Action: emu.Drop}}, []int{1, 1}},
		{"first fault wins, both count", []emu.Fault{
			{Cmd: wire.CmdRead, Times: 1, Action: emu.Duplicate},
			{Cmd: wire.CmdRead, Times: 2, Action: emu.Drop},
		}, []int{2, 0, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBus(t, emu.Options{})
			d := add(t, b, receiver(em11(t)))
			for _, f := range tt.faults {
				d.Inject(f)
			}
			tr := open(t, b, d, 1)
			for i, want := range tt.replies {
				p := read(t, i*10, 10)
				write(t, tr, p)
				if want < 0 {
					if rep := next(t, tr); rep.Status() != wire.StatusNAK || !wire.Match(p, rep) {
						t.Fatalf("read %d: reply %v, want a NAK", i, rep)
					}
					continue
				}
				for range want {
					if rep := next(t, tr); !wire.Match(p, rep) || rep.Status() != wire.StatusOK {
						t.Fatalf("read %d: reply %v", i, rep)
					}
				}
			}
			none(t, tr)
		})
	}
}

// A resend, the same packet right after itself, counts as the packet it
// repeats; the same read after another packet is a read of its own. Nth
// faults the second read and Logical leaves the resends out.
func TestResendsAreOnePacket(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Match: emu.Nth(wire.CmdRead, 2), Times: 1, Action: emu.Drop})
	tr := open(t, b, d, 1)
	rd, online := read(t, 0, 10), req(wire.CmdOnline)
	transact(t, tr, rd)
	transact(t, tr, rd)
	transact(t, tr, online)
	write(t, tr, rd)
	none(t, tr)
	transact(t, tr, rd)
	var got []wire.Packet
	for _, w := range emu.Logical(d.Writes()) {
		got = append(got, w.Packet)
	}
	if want := []wire.Packet{rd, online, rd}; !slices.Equal(got, want) {
		t.Fatalf("logical writes %v, want %v", got, want)
	}
	if n := len(d.Writes()); n != 5 {
		t.Fatalf("%d writes, want 5", n)
	}
}

func TestLateReply(t *testing.T) {
	clock := emu.NewManualClock()
	b := newBus(t, emu.Options{Clock: clock})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Times: 1, Action: emu.Late, Delay: 300 * time.Millisecond})
	tr := open(t, b, d, 1)
	write(t, tr, req(wire.CmdOnline))
	clock.Advance(299 * time.Millisecond)
	none(t, tr)
	transact(t, tr, req(wire.CmdBattery))
	clock.Advance(time.Millisecond)
	if rep := next(t, tr); rep.Cmd() != wire.CmdOnline {
		t.Fatalf("late reply %v, want cmd 3", rep)
	}
}

func TestSleepDuringRead(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdRead, Skip: 2, Times: 1, Action: emu.Asleep})
	tr := open(t, b, d, 1)
	transact(t, tr, read(t, 0, 10))
	transact(t, tr, read(t, 10, 10))
	write(t, tr, read(t, 20, 10))
	none(t, tr)
	if rep := transact(t, tr, req(wire.CmdOnline)); rep[5] != 0 {
		t.Fatalf("online after sleeping mid-read: %v", rep)
	}
	write(t, tr, read(t, 20, 10))
	none(t, tr)
	d.Wake()
	transact(t, tr, read(t, 20, 10))
}

func TestWriteErrors(t *testing.T) {
	tests := []struct {
		name  string
		fault emu.Fault
		class hidio.Class
		sent  int
	}{
		{"locked", emu.Fault{Action: emu.Fail, Err: emu.ErrLocked, Times: 1}, hidio.ClassLocked, 0},
		{"seized", emu.Fault{Action: emu.Fail, Err: emu.ErrSeized, Times: 1}, hidio.ClassSeized, 0},
		{"transient error retried", emu.Fault{Action: emu.Fail, Times: 2}, hidio.ClassNone, 1},
		{"persistent general error", emu.Fault{Action: emu.Fail}, hidio.ClassRetry, 0},
		{"taken, then a transient error", emu.Fault{Action: emu.Taken, Times: 1}, hidio.ClassNone, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBus(t, emu.Options{})
			d := add(t, b, receiver(em11(t)))
			d.Inject(tt.fault)
			tr := open(t, b, d, 1)
			err := tr.Write(req(wire.CmdOnline))
			if got := hidio.Classify(err); got != tt.class {
				t.Fatalf("Write = %v (%v), want %v", err, got, tt.class)
			}
			if got := len(d.Writes()); got != tt.sent {
				t.Fatalf("device received %d packets, want %d", got, tt.sent)
			}
		})
	}
}

func TestLockedSeizedDenied(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	d.SetLocked(true)
	if err := tr.Write(req(wire.CmdOnline)); hidio.Classify(err) != hidio.ClassLocked {
		t.Fatalf("Write while locked = %v", err)
	}
	d.SetLocked(false)
	transact(t, tr, req(wire.CmdOnline))

	d.SetSeized(true)
	if _, err := b.Open(d.Candidates()[1], hidio.NewGuard(wire.Mouse), nil); hidio.Classify(err) != hidio.ClassSeized {
		t.Fatalf("Open while seized = %v", err)
	}
	if err := tr.Write(req(wire.CmdOnline)); hidio.Classify(err) != hidio.ClassSeized {
		t.Fatalf("Write while seized = %v", err)
	}
	d.SetSeized(false)
	transact(t, tr, req(wire.CmdOnline))

	d.SetDenied(true)
	if _, err := b.Open(d.Candidates()[1], hidio.NewGuard(wire.Mouse), nil); hidio.Classify(err) != hidio.ClassPermission {
		t.Fatalf("Open while denied = %v", err)
	}
	if err := tr.Write(req(wire.CmdOnline)); hidio.Classify(err) != hidio.ClassPermission {
		t.Fatalf("Write while denied = %v", err)
	}
	d.SetDenied(false)
	transact(t, tr, req(wire.CmdOnline))
}

func TestHang(t *testing.T) {
	b := newBus(t, emu.Options{Watchdog: 20 * time.Millisecond})
	d := add(t, b, receiver(em11(t)))
	// The watchdog can fire before the write reaches the device, and Release
	// frees only the writes already hung.
	hung := make(chan struct{}, 1)
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Times: 1, Action: emu.Hang, Match: func(wire.Packet) bool {
		select {
		case hung <- struct{}{}:
		default:
		}
		return true
	}})
	tr := open(t, b, d, 1)
	if err := tr.Write(req(wire.CmdOnline)); hidio.Classify(err) != hidio.ClassStalled {
		t.Fatalf("hung Write = %v, want stalled", err)
	}
	if err := tr.Write(req(wire.CmdBattery)); !errors.Is(err, hidio.ErrStalled) {
		t.Fatalf("Write after a stall = %v, want ErrStalled", err)
	}
	select {
	case <-hung:
	case <-time.After(waitFor):
		t.Fatal("the hung write never reached the device")
	}
	d.Release()
	if rep := next(t, tr); rep.Cmd() != wire.CmdOnline {
		t.Fatalf("released write answered with %v", rep)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnplug(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdBattery, Action: emu.Unplug})
	tr := open(t, b, d, 1)
	if err := tr.Write(req(wire.CmdBattery)); hidio.Classify(err) != hidio.ClassGone {
		t.Fatalf("Write = %v, want gone", err)
	}
	if _, ok := <-tr.Reports(); ok {
		t.Fatal("Reports still open")
	}
	if hidio.Classify(tr.Err()) != hidio.ClassGone {
		t.Fatalf("Err = %v, want gone", tr.Err())
	}
	if cs, _ := b.Enumerate(); len(cs) != 0 {
		t.Fatalf("Enumerate after unplug = %v", cs)
	}
	if _, err := b.Open(d.Candidates()[1], hidio.NewGuard(wire.Mouse), nil); !errors.Is(err, hidio.ErrNotFound) {
		t.Fatalf("Open after unplug = %v", err)
	}
	d.Plug()
	if cs, _ := b.Enumerate(); len(cs) != 2 {
		t.Fatalf("Enumerate after replug = %v", cs)
	}
	transact(t, open(t, b, d, 1), req(wire.CmdOnline))
}

func TestBusCloseEndsEverything(t *testing.T) {
	b := emu.New(emu.Options{Watchdog: 20 * time.Millisecond})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Action: emu.Hang})
	tr := open(t, b, d, 1)
	if err := tr.Write(req(wire.CmdOnline)); !errors.Is(err, hidio.ErrStalled) {
		t.Fatalf("Write = %v", err)
	}
	b.Close()
	if _, ok := <-tr.Reports(); ok {
		t.Fatal("Reports still open after Bus.Close")
	}
	if cs, _ := b.Enumerate(); len(cs) != 0 {
		t.Fatalf("Enumerate after Close = %v", cs)
	}
}

func TestIOErrorsClassify(t *testing.T) {
	tests := []struct {
		err   emu.IOError
		class hidio.Class
	}{
		{emu.ErrGeneral, hidio.ClassRetry},
		{emu.ErrGone, hidio.ClassGone},
		{emu.ErrDenied, hidio.ClassPermission},
		{emu.ErrSeized, hidio.ClassSeized},
		{emu.ErrLocked, hidio.ClassLocked},
	}
	for _, tt := range tests {
		if got := hidio.Classify(tt.err); got != tt.class {
			t.Errorf("Classify(%v) = %v, want %v", tt.err, got, tt.class)
		}
		if code, ok := hidio.IOReturn(tt.err); !ok || code != uint32(tt.err) {
			t.Errorf("IOReturn(%v) = %#x, %v", tt.err, code, ok)
		}
	}
}
