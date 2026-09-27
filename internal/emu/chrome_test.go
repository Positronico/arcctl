package emu_test

import (
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

func TestChromeBroadcast(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	chrome := d.Chrome(0)
	defer chrome.Close()

	chrome.Poll()
	if p := next(t, tr); p.Cmd() != wire.CmdOnline {
		t.Fatalf("first foreign reply %v, want cmd 3", p)
	}
	if p := next(t, tr); p.Cmd() != wire.CmdBattery {
		t.Fatalf("second foreign reply %v, want cmd 4", p)
	}
	transact(t, tr, req(wire.CmdFWVersion))
	seen := chrome.Seen()
	if len(seen) != 3 || seen[2].Cmd() != wire.CmdFWVersion {
		t.Fatalf("competitor saw %v, want its own two replies and arcctl's cmd 18", seen)
	}
	if w := d.Writes(); len(w) != 1 {
		t.Fatalf("Writes = %v, want only arcctl's packet", w)
	}

	d.Sleep()
	chrome.Poll()
	if p := next(t, tr); p.Cmd() != wire.CmdOnline || p[5] != 0 {
		t.Fatalf("foreign reply %v, want cmd 3 offline", p)
	}
	none(t, tr)
}

// steal sends n cmd-3 requests with a competitor attached and returns which
// of them reached arcctl.
func steal(t *testing.T, seed uint64, n int) []bool {
	b := newBus(t, emu.Options{Seed: seed})
	d := add(t, b, emu.Config{Mouse: em11(t), Behavior: emu.Behavior{Sharing: emu.Steal}})
	tr := open(t, b, d, 1)
	chrome := d.Chrome(0)
	defer chrome.Close()
	got := make([]bool, n)
	for i := range got {
		before := len(chrome.Seen())
		write(t, tr, req(wire.CmdOnline))
		if len(chrome.Seen()) == before {
			next(t, tr)
			got[i] = true
		}
	}
	none(t, tr)
	if stolen := len(chrome.Seen()); stolen+count(got) != n {
		t.Fatalf("%d stolen + %d delivered != %d replies", stolen, count(got), n)
	}
	return got
}

func count(v []bool) int {
	n := 0
	for _, x := range v {
		if x {
			n++
		}
	}
	return n
}

func TestChromeSteal(t *testing.T) {
	const n = 40
	a := steal(t, 7, n)
	if c := count(a); c == 0 || c == n {
		t.Fatalf("arcctl got %d of %d replies, want some stolen and some not", c, n)
	}
	if b := steal(t, 7, n); !slices.Equal(a, b) {
		t.Fatalf("the same seed stole different replies:\n%v\n%v", a, b)
	}
}

func TestChromePollsOnTheClock(t *testing.T) {
	clock := emu.NewManualClock()
	b := newBus(t, emu.Options{Clock: clock})
	d := add(t, b, receiver(em11(t)))
	tr := open(t, b, d, 1)
	chrome := d.Chrome(5 * time.Second)
	clock.Advance(4 * time.Second)
	if len(chrome.Seen()) != 0 {
		t.Fatal("polled early")
	}
	clock.Advance(11 * time.Second)
	if got := len(chrome.Seen()); got != 6 {
		t.Fatalf("competitor saw %d replies after 15 s, want 3 polls of 2", got)
	}
	for range 6 {
		next(t, tr)
	}
	chrome.Close()
	clock.Advance(time.Minute)
	none(t, tr)
	if clock.Pending() != 0 {
		t.Fatalf("%d timers left after Close", clock.Pending())
	}
}

func TestClients(t *testing.T) {
	b := newBus(t, emu.Options{PID: 321})
	d := add(t, b, receiver(em11(t)))
	d.AddClient(emu.Client{PID: 777, Process: "karabiner_observer"})
	paths := []string{d.Candidates()[0].Path, d.Candidates()[1].Path}
	tr, err := b.Open(d.Candidates()[1], hidio.NewGuard(wire.Mouse), nil)
	if err != nil {
		t.Fatal(err)
	}
	chrome := d.Chrome(0)
	want := []emu.Client{
		{Path: paths[1], PID: 321, Process: "arcctl"},
		{Path: paths[1], PID: 4000, Process: emu.ChromeProcess},
		{Path: paths[0], PID: 777, Process: "karabiner_observer"},
		{Path: paths[1], PID: 777, Process: "karabiner_observer"},
	}
	if got := b.Clients(); !slices.Equal(got, want) {
		t.Fatalf("Clients = %+v\nwant %+v", got, want)
	}
	tr.Close()
	chrome.Close()
	if got := b.Clients(); !slices.Equal(got, want[2:]) {
		t.Fatalf("Clients after closing = %+v", got)
	}
	d.Unplug()
	if got := b.Clients(); len(got) != 0 {
		t.Fatalf("Clients of an unplugged device = %+v", got)
	}
}
