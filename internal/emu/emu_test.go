package emu_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

func TestEnumerate(t *testing.T) {
	b := newBus(t, emu.Options{})
	rx := add(t, b, emu.Config{})
	add(t, b, emu.Config{VID: 0x046D, PID: 0xC52B})
	cs, err := b.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("Enumerate = %v, want the receiver's two interfaces only", cs)
	}
	for i, c := range cs {
		if c.Backend != emu.Backend || c.VID != 0x260D || c.PID != 0x1282 || c.Interface != i || c.Class != catalog.ClassComposite || c.UsagePage != 0xFF02 {
			t.Errorf("candidate %d = %+v", i, c)
		}
	}
	if cs[0].Path == cs[1].Path {
		t.Errorf("both interfaces have path %q", cs[0].Path)
	}
	if !slices.Equal(cs, rx.Candidates()) {
		t.Errorf("Candidates() = %v, want %v", rx.Candidates(), cs)
	}
	if _, err := hidio.Open(cs[1], hidio.NewGuard(wire.Mouse), nil); !errors.Is(err, hidio.ErrBackend) {
		t.Errorf("hidio.Open of an emulated candidate = %v, want ErrBackend", err)
	}
	unknown := hidio.Candidate{Backend: emu.Backend, Path: cs[1].Path, VID: 0x046D, PID: 0xC52B}
	if _, err := b.Open(unknown, hidio.NewGuard(wire.Mouse), nil); !errors.Is(err, hidio.ErrNotFound) {
		t.Errorf("Open of an unknown VID/PID = %v, want ErrNotFound", err)
	}
}

func TestOnlyTheAnsweringInterfaceReplies(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	silent, live := open(t, b, d, 0), open(t, b, d, 1)
	write(t, silent, req(wire.CmdOnline))
	none(t, silent)
	transact(t, live, req(wire.CmdOnline))
	got := d.Writes()
	if len(got) != 2 || got[0].Interface != 0 || got[1].Interface != 1 {
		t.Fatalf("Writes = %+v, want cmd 3 on interface 0, then on 1", got)
	}
}

func TestReceiverAnswersWhileTheMouseSleeps(t *testing.T) {
	tests := []struct {
		name     string
		p        wire.Packet
		receiver bool
	}{
		{"online", req(wire.CmdOnline), true},
		{"receiver version", req(wire.CmdRxVersion), true},
		{"handshake", handshake(), false},
		{"battery", req(wire.CmdBattery), false},
		{"read", read(t, 0x60, 10), false},
		{"profile", req(wire.CmdGetProfile), false},
		{"firmware", req(wire.CmdFWVersion), false},
		{"long range", req(wire.CmdGetLongRange), false},
	}
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Asleep = true
	d := add(t, b, receiver(m))
	tr := open(t, b, d, 1)
	for _, tt := range tests {
		t.Run("asleep/"+tt.name, func(t *testing.T) {
			if tt.receiver {
				transact(t, tr, tt.p)
				return
			}
			write(t, tr, tt.p)
			none(t, tr)
		})
	}
	d.Wake()
	<-tr.Wake()
	for _, tt := range tests {
		t.Run("awake/"+tt.name, func(t *testing.T) {
			if rep := transact(t, tr, tt.p); rep.Status() != wire.StatusOK || !rep.Valid() {
				t.Fatalf("reply %v", rep)
			}
		})
	}
}

func TestOfflineReplyMatchesVector(t *testing.T) {
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Asleep = true
	tr := open(t, b, add(t, b, receiver(m)), 1)
	rep := transact(t, tr, req(wire.CmdOnline))
	if want := vector(t, "Offline cmd-3 reply"); !bytes.Equal(rep[:], want) {
		t.Fatalf("offline reply %v, want % x", rep, want)
	}
}

func TestReplies(t *testing.T) {
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Conn = 4
	m.Addr = [3]byte{0xaa, 0xbb, 0xcc}
	m.Battery = emu.Battery{Level: 55, Charging: true, MilliVolts: 0x0f3c, Direct: true}
	tr := open(t, b, add(t, b, receiver(m)), 1)
	tests := []struct {
		name string
		p    wire.Packet
		want []byte
	}{
		{"handshake", handshake(), []byte{1, 0, 0, 0, 8, 0xa1, 0xb2, 0xc3, 0xd4, 0x7b, 0x04, 0x04, 0}},
		{"online", req(wire.CmdOnline), []byte{3, 0, 0, 0, 4, 1, 0xcc, 0xbb, 0xaa}},
		{"battery", req(wire.CmdBattery), []byte{4, 0, 0, 0, 6, 55, 1, 0x0f, 0x3c, 1, 55}},
		{"profile", req(wire.CmdGetProfile), []byte{14, 0, 0, 0, 1, 0}},
		{"firmware", req(wire.CmdFWVersion), []byte{18, 0, 0, 0, 2, 1, 5}},
		{"long range", req(wire.CmdGetLongRange), []byte{23, 0, 0, 0, 1, 1}},
		{"receiver", req(wire.CmdRxVersion), []byte{29, 0, 0, 0, 2, 1, 2}},
		{"read", read(t, 0, 4), []byte{8, 0, 0, 0, 4, 0x04, 0x51, 0x06, 0x4f}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := transact(t, tr, tt.p)
			var want wire.Packet
			copy(want[:], tt.want)
			want[wire.Size-1] = want.Checksum()
			if rep != want {
				t.Fatalf("reply %v, want %v", rep, want)
			}
		})
	}
	hs := transact(t, tr, handshake())
	if got, ok := catalog.Resolve(hs[9], hs[10]); !ok || got.Key != "7B04" {
		t.Errorf("handshake resolves to %v, %v", got, ok)
	}
}

func TestUnsupportedQueriesNAK(t *testing.T) {
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Profile, m.LongRange = nil, nil
	tr := open(t, b, add(t, b, emu.Config{Mouse: m}), 1)
	for _, c := range []wire.Cmd{wire.CmdGetProfile, wire.CmdGetLongRange, wire.CmdRxVersion} {
		if rep := transact(t, tr, req(c)); rep.Status() != wire.StatusNAK || !rep.Valid() {
			t.Errorf("%v: reply %v, want a NAK", c, rep)
		}
	}
}

func TestReadsServeTheImage(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	got := flash.New()
	load := func(e flash.Extent) {
		for a := e.Addr; a < e.End(); a += wire.MaxData {
			n := min(wire.MaxData, e.End()-a)
			rep := transact(t, tr, read(t, a, n))
			if err := got.Set(a, rep[5:5+n]); err != nil {
				t.Fatal(err)
			}
		}
	}
	load(flash.Extent{Addr: 0, Len: 256})
	for slot := range mouse.Slots {
		e, _ := mouse.ShortcutExtent(slot)
		load(e)
	}
	want := d.Image()
	for _, e := range got.KnownExtents() {
		g, _ := got.Get(e)
		w, _ := want.Get(e)
		if !bytes.Equal(g, w) {
			t.Fatalf("%v: read % x, image has % x", e, g, w)
		}
	}
	cfg := mouse.Decode(model(t, "7B04"), got)
	for slot := 2; slot <= 5; slot++ {
		if cfg.Slots[slot] != flash.SlotValid || len(cfg.Shortcuts[slot]) == 0 {
			t.Errorf("slot %d: class %v, shortcut %v", slot, cfg.Slots[slot], cfg.Shortcuts[slot])
		}
	}
	if rep := transact(t, tr, read(t, 6912, 10)); !bytes.Equal(rep[5:15], bytes.Repeat([]byte{0xff}, 10)) {
		t.Errorf("unread flash reads % x, want erased", rep[5:15])
	}
}

func TestPushesAndNoise(t *testing.T) {
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Asleep = true
	d := add(t, b, emu.Config{Mouse: m, Behavior: emu.Behavior{PushOnline: true}})
	tr := open(t, b, d, 1)

	d.Push(0x20, 0x01)
	if p := next(t, tr); p.Cmd() != wire.CmdStatusChanged || p[5] != 0x20 || p[6] != 0x01 || !p.Valid() {
		t.Fatalf("push %v", p)
	}
	d.Noise(3)
	<-tr.Wake()

	d.Wake()
	<-tr.Wake()
	if p := next(t, tr); p.Cmd() != wire.CmdOnline || p[5] != 1 {
		t.Fatalf("wake push %v, want cmd 3 online", p)
	}
	d.Sleep()
	if p := next(t, tr); p.Cmd() != wire.CmdOnline || p[5] != 0 {
		t.Fatalf("sleep push %v, want cmd 3 offline", p)
	}
	if d.Awake() {
		t.Fatal("still awake")
	}
}

func TestPressDPI(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	for _, want := range []byte{4, 5, 0} {
		if err := d.PressDPI(); err != nil {
			t.Fatal(err)
		}
		if p := next(t, tr); p.Cmd() != wire.CmdStatusChanged || p[5] != 0x01 {
			t.Fatalf("push %v, want StatusChanged 0x01", p)
		}
		if rep := transact(t, tr, read(t, mouse.AddrCurrentDPI, 2)); rep[5] != want || rep[6] != 0x55-want {
			t.Fatalf("current stage % x, want %d", rep[5:7], want)
		}
	}
	if err := add(t, b, emu.Config{}).PressDPI(); !errors.Is(err, emu.ErrNotPaired) {
		t.Fatalf("PressDPI with nothing paired = %v", err)
	}
}

func TestSwitchProfile(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	if err := d.SwitchProfile(2); err != nil {
		t.Fatal(err)
	}
	if p := next(t, tr); p.Cmd() != wire.CmdStatusChanged || p[5] != 0x04 {
		t.Fatalf("push %v, want StatusChanged 0x04", p)
	}
	if rep := transact(t, tr, req(wire.CmdGetProfile)); rep[5] != 2 {
		t.Fatalf("profile %d, want 2", rep[5])
	}
	m := em11(t)
	m.Profile = nil
	if err := add(t, b, emu.Config{Mouse: m}).SwitchProfile(1); err == nil {
		t.Fatal("SwitchProfile without profiles succeeded")
	}
}

func TestPairAnotherMouse(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	if err := d.Pair(&emu.Mouse{Model: model(t, "7B05"), Conn: 1}); err != nil {
		t.Fatal(err)
	}
	if rep := transact(t, tr, handshake()); rep[9] != 0x7b || rep[10] != 5 || rep[11] != 1 {
		t.Fatalf("handshake %v, want cid 7b mid 5 type 1", rep)
	}
	if err := d.Pair(nil); err != nil {
		t.Fatal(err)
	}
	if rep := transact(t, tr, req(wire.CmdOnline)); rep[5] != 0 {
		t.Fatalf("online %v with nothing paired", rep)
	}
	write(t, tr, handshake())
	none(t, tr)
	if d.Image() != nil {
		t.Fatal("Image with nothing paired")
	}
}

func TestSecondAnsweringDevice(t *testing.T) {
	b := newBus(t, emu.Options{})
	one := em11(t)
	two := em11(t)
	two.Addr = [3]byte{0x44, 0x55, 0x66}
	d1, d2 := add(t, b, receiver(one)), add(t, b, receiver(two))
	cs, _ := b.Enumerate()
	if len(cs) != 4 {
		t.Fatalf("Enumerate = %d candidates, want 4", len(cs))
	}
	r1 := transact(t, open(t, b, d1, 1), req(wire.CmdOnline))
	r2 := transact(t, open(t, b, d2, 1), req(wire.CmdOnline))
	if r1[5] != 1 || r2[5] != 1 || r1 == r2 {
		t.Fatalf("replies %v and %v, want two online devices with different addresses", r1, r2)
	}
}

func TestRecorder(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	var buf bytes.Buffer
	rec, err := hidio.NewRecorder(&buf, hidio.Header{Source: "emu"})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := b.Open(d.Candidates()[1], hidio.NewGuard(wire.Mouse), rec)
	if err != nil {
		t.Fatal(err)
	}
	transact(t, tr, req(wire.CmdOnline))
	tr.Close()
	_, entries, err := hidio.ReadTranscript(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Dir != hidio.DirOut || entries[1].Dir != hidio.DirIn {
		t.Fatalf("entries %+v, want the write and its reply", entries)
	}
}

func TestGuardStillApplies(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	for _, p := range []wire.Packet{
		wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 1, 0, 0x53}),
		req(wire.CmdClear),
		wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0}),
	} {
		if err := tr.Write(p); !errors.Is(err, hidio.ErrForbidden) {
			t.Errorf("Write(%v) = %v, want ErrForbidden", p, err)
		}
	}
	if w := d.Writes(); len(w) != 0 {
		t.Fatalf("the device received %v", w)
	}
}

// TestConnectSequence walks the connect sequence of the plan against a
// receiver whose mouse starts asleep.
func TestConnectSequence(t *testing.T) {
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Asleep = true
	d := add(t, b, receiver(m))
	cs, _ := b.Enumerate()
	var live hidio.Transport
	for _, c := range cs {
		tr, err := b.Open(c, hidio.NewGuard(wire.Mouse), nil)
		if err != nil {
			t.Fatal(err)
		}
		write(t, tr, req(wire.CmdOnline))
		select {
		case <-tr.Reports():
			if live != nil {
				t.Fatal("two interfaces answered")
			}
			live = tr
			t.Cleanup(func() { tr.Close() })
		case <-time.After(quiet):
			tr.Close()
		}
	}
	if live == nil {
		t.Fatal("no interface answered")
	}
	if got := len(b.Clients()); got != 1 {
		t.Fatalf("%d clients after probing, want 1", got)
	}
	transact(t, live, req(wire.CmdRxVersion))
	write(t, live, handshake())
	none(t, live)

	d.Wake()
	<-live.Wake()
	if rep := transact(t, live, req(wire.CmdOnline)); rep[5] != 1 {
		t.Fatalf("online %v after wake", rep)
	}
	if rep := transact(t, live, handshake()); rep[9] != 0x7b || rep[10] != 4 {
		t.Fatalf("handshake %v", rep)
	}
	for _, e := range []flash.Extent{{Addr: 0, Len: 256}, {Addr: 6912, Len: 75}} {
		for a := e.Addr; a < e.End(); a += wire.MaxData {
			transact(t, live, read(t, a, min(wire.MaxData, e.End()-a)))
		}
	}
	for _, c := range []wire.Cmd{wire.CmdGetProfile, wire.CmdFWVersion, wire.CmdBattery, wire.CmdGetLongRange} {
		transact(t, live, req(c))
	}
	if got := len(d.Writes()); got != 2+1+1+1+1+26+8+4 {
		t.Fatalf("the device received %d packets", got)
	}
}
