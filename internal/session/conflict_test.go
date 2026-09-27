package session_test

import (
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func TestChromeBroadcastIsAConflict(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	before := await(t, s, "ready", idle)
	comp := d.Chrome(0)
	t.Cleanup(comp.Close)
	time.Sleep(2 * fast().Window)
	comp.Poll()
	sn := await(t, s, "conflict", in(session.Conflict))
	if sn.Link != session.Ready || sn.Stats.Foreign != 1 || sn.Stats.PossiblePushes != 1 {
		t.Errorf("link %v, stats %+v", sn.Link, sn.Stats)
	}
	if *sn.Battery != *before.Battery || sn.Image != before.Image {
		t.Error("a foreign reply changed the session's data")
	}
	if got := comp.Seen(); len(got) != 2 || got[0].Cmd() != wire.CmdOnline || got[1].Cmd() != wire.CmdBattery {
		t.Errorf("the competitor saw %v", got)
	}
}

func TestChromeStealIsASuspectedConflict(t *testing.T) {
	b := newBus(t, emu.Options{Seed: 7})
	c := receiver(em11(t))
	c.Behavior.Sharing = emu.Steal
	d := add(t, b, c)
	comp := d.Chrome(0)
	tm := fast()
	tm.Online = 20 * time.Millisecond
	s := start(t, b, session.Options{Timing: tm})
	sn := await(t, s, "a suspected conflict over a loaded device", func(sn *session.Snapshot) bool {
		return sn.State == session.SuspectedConflict && sn.Link == session.Ready && sn.Image != nil
	})
	if sn.Stats.FailedTries == 0 || len(comp.Seen()) == 0 {
		t.Errorf("failed tries %d, stolen %d", sn.Stats.FailedTries, len(comp.Seen()))
	}
	await(t, s, "the scan to show the competitor", func(sn *session.Snapshot) bool {
		return slices.ContainsFunc(sn.Clients, func(c session.Client) bool { return c.Name == emu.ChromeProcess })
	})
	var loaded []int
	for i, e := range workingSet() {
		if !slices.Contains(sn.Unread, e) {
			loaded = append(loaded, i)
			sameBytes(t, sn.Image, d.Image(), e)
		}
	}
	if len(loaded) == 0 {
		t.Error("nothing loaded")
	}
	comp.Close()
	sn = await(t, s, "the suspicion to clear", in(session.Ready))
	if len(sn.Clients) != 0 {
		t.Errorf("Clients = %v", sn.Clients)
	}
}

// Leaving Conflict takes ClearConflict; a reattach of the same device keeps it.
func TestConflictSurvivesAReattach(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tm := fast()
	tm.ConflictQuiet = time.Hour
	s := start(t, b, session.Options{Timing: tm})
	await(t, s, "ready", idle)
	comp := d.Chrome(0)
	t.Cleanup(comp.Close)
	time.Sleep(2 * tm.Window)
	comp.Poll()
	await(t, s, "conflict", in(session.Conflict))
	d.Unplug()
	await(t, s, "no receiver", in(session.NoReceiver))
	d.Plug()
	sn := await(t, s, "the device loaded again", func(sn *session.Snapshot) bool { return sn.Link == session.Ready && sn.Progress.Job == "" })
	if sn.State != session.Conflict || sn.LastForeign.IsZero() {
		t.Errorf("state %v, last foreign %v after a reattach; only ClearConflict ends a conflict", sn.State, sn.LastForeign)
	}
}

// A conflict belongs to the device it was seen on; another receiver starts clean.
func TestConflictEndsWithAnotherDevice(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tm := fast()
	tm.ConflictQuiet = time.Hour
	s := start(t, b, session.Options{Timing: tm})
	await(t, s, "ready", idle)
	comp := d.Chrome(0)
	t.Cleanup(comp.Close)
	time.Sleep(2 * tm.Window)
	comp.Poll()
	await(t, s, "conflict", in(session.Conflict))
	d.Unplug()
	await(t, s, "no receiver", in(session.NoReceiver))
	other := receiver(em11(t))
	other.VID, other.PID = 0x3554, 0x1282
	add(t, b, other)
	sn := await(t, s, "the other device loaded", func(sn *session.Snapshot) bool { return sn.Link == session.Ready && sn.Progress.Job == "" })
	if sn.State != session.Ready {
		t.Errorf("state %v on another receiver", sn.State)
	}
}
