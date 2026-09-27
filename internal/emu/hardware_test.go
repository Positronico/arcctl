package emu_test

import (
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// The EM11 Pro profile answers as the unit did in H0.
func TestEM11ProAnswersLikeH0(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, emu.EM11Pro(dumpImage(t)))
	if cs := d.Candidates(); cs[0].VID != 0x260D || cs[0].PID != 0x1282 || len(cs) != 2 {
		t.Fatalf("candidates %v", cs)
	}
	silent := open(t, b, d, 0)
	write(t, silent, req(wire.CmdOnline))
	none(t, silent)

	tr := open(t, b, d, 1)
	if rep := transact(t, tr, req(wire.CmdFWVersion)); rep[5] != 1 || rep[6] != 0x26 {
		t.Errorf("firmware reply %v, want v1.26", rep)
	}
	for _, c := range []wire.Cmd{wire.CmdGetProfile, wire.CmdGetLongRange, wire.CmdRxVersion} {
		if rep := transact(t, tr, req(c)); rep.Status() != wire.StatusNAK || !rep.Valid() {
			t.Errorf("%v: reply %v, want a NAK", c, rep)
		}
	}
	var want wire.Packet
	copy(want[:], []byte{4, 0, 0, 0, 4, 100, 0, 0x10, 0x30})
	want[wire.Size-1] = want.Checksum()
	if rep := transact(t, tr, req(wire.CmdBattery)); rep != want {
		t.Errorf("battery reply %v, want %v", rep, want)
	}
	if rep := transact(t, tr, handshake()); rep[9] != 0x7b || rep[10] != 4 || rep[11] != 0 {
		t.Errorf("handshake %v, want 7b 04, wireless 1 kHz", rep)
	}

	d.Sleep()
	none(t, tr)
	if rep := transact(t, tr, req(wire.CmdOnline)); rep[5] != 0 {
		t.Fatalf("online %v while asleep", rep)
	}
	for _, p := range []wire.Packet{req(wire.CmdRxVersion), req(wire.CmdBattery), read(t, 0, 10)} {
		write(t, tr, p)
		none(t, tr)
	}
	d.Wake()
	<-tr.Wake()
	none(t, tr)
	if rep := transact(t, tr, req(wire.CmdRxVersion)); rep.Status() != wire.StatusNAK {
		t.Errorf("cmd 29 after wake: %v", rep)
	}
}

// The mouse sleeps EM11ProSleep after its last input or radio packet; cmd 3
// never reaches it, so polling cmd 3 lets it sleep. No frame marks the sleep.
func TestEM11ProSleepsWhenIdle(t *testing.T) {
	clock := emu.NewManualClock()
	b := newBus(t, emu.Options{Clock: clock})
	d := add(t, b, emu.EM11Pro(nil))
	tr := open(t, b, d, 1)
	step := func(dt time.Duration, p wire.Packet) {
		t.Helper()
		clock.Advance(dt)
		transact(t, tr, p)
	}
	for range 3 {
		step(10*time.Second, read(t, 0, 10))
	}
	if !d.Awake() {
		t.Fatal("radio traffic did not keep the mouse awake")
	}
	d.Noise(1)
	for range 2 {
		step(6*time.Second, req(wire.CmdOnline))
	}
	if !d.Awake() {
		t.Fatal("asleep 12 s after the last input")
	}
	clock.Advance(time.Second)
	if d.Awake() {
		t.Fatal("still awake 13 s after the last input")
	}
	none(t, tr)
	d.Wake()
	clock.Advance(emu.EM11ProSleep - time.Millisecond)
	if !d.Awake() {
		t.Fatal("asleep before its idle time")
	}
	clock.Advance(time.Millisecond)
	if d.Awake() {
		t.Fatal("awake after its idle time")
	}
}

func TestSilentDPI(t *testing.T) {
	b := newBus(t, emu.Options{})
	c := emu.EM11Pro(dumpImage(t))
	c.Behavior.SilentDPI = true
	d := add(t, b, c)
	tr := open(t, b, d, 1)
	must(t, d.PressDPI())
	none(t, tr)
	if rep := transact(t, tr, read(t, mouse.AddrCurrentDPI, 2)); rep[5] != 4 {
		t.Fatalf("current stage % x after a silent press, want 4", rep[5:7])
	}
	c.Behavior.SilentDPI = false
	loud := add(t, b, c)
	tr = open(t, b, loud, 1)
	must(t, loud.PressDPI())
	if p := next(t, tr); p.Cmd() != wire.CmdStatusChanged || p[5] != 0x01 || p.Len() != 10 {
		t.Fatalf("push %v, want StatusChanged 0x01 with 10 data bytes", p)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
