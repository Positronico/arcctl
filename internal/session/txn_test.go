package session_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func at(addr int) func(wire.Packet) bool {
	return func(p wire.Packet) bool { return int(p.Addr()) == addr }
}

// reply frames a device report.
func reply(c wire.Cmd, addr uint16, data ...byte) wire.Packet {
	var p wire.Packet
	p[0], p[2], p[3], p[4] = byte(c), byte(addr>>8), byte(addr), byte(len(data))
	copy(p[5:], data)
	p[wire.Size-1] = p.Checksum()
	return p
}

func TestDuplicateReplyIsOnlyLogged(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdRead, Match: at(0), Times: 1, Action: emu.Duplicate})
	s := start(t, b, session.Options{})
	sn := await(t, s, "ready", idle)
	if sn.Stats.Duplicates != 1 || sn.Stats.Foreign != 0 || sn.State != session.Ready {
		t.Errorf("stats %+v, state %v", sn.Stats, sn.State)
	}
	sameBytes(t, sn.Image, d.Image(), workingSet()...)
}

func TestLateRepliesAreOnlyLogged(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdFWVersion, Times: 10, Action: emu.Late, Delay: 400 * time.Millisecond})
	tm := fast()
	tm.Window = 2 * time.Second
	tm.Suspect = 200 * time.Millisecond
	s := start(t, b, session.Options{Timing: tm})
	await(t, s, "a suspected conflict", in(session.SuspectedConflict))
	sn := await(t, s, "late replies", func(sn *session.Snapshot) bool { return sn.Stats.Late >= 10 && sn.State == session.Ready })
	if sn.Stats.Foreign != 0 || sn.Versions.Mouse != "" || sn.Stats.Late != 10 {
		t.Errorf("foreign %d, late %d, mouse version %q", sn.Stats.Foreign, sn.Stats.Late, sn.Versions.Mouse)
	}
}

func TestForeignReplyIsAConflict(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	before := await(t, s, "ready", idle)
	time.Sleep(2 * fast().Window)
	d.Deliver(reply(wire.CmdFWVersion, 0, 9, 9))
	sn := await(t, s, "conflict", in(session.Conflict))
	if sn.Link != session.Ready || sn.Stats.Foreign != 1 || sn.Versions.Mouse != "v1.05" || sn.Image != before.Image {
		t.Errorf("link %v, foreign %d, version %q, image replaced %v", sn.Link, sn.Stats.Foreign, sn.Versions.Mouse, sn.Image != before.Image)
	}
	if err := s.ClearConflict(ctxT(t)); !errors.Is(err, session.ErrConflict) {
		t.Errorf("ClearConflict right away = %v", err)
	}
	time.Sleep(fast().ConflictQuiet)
	if err := s.ClearConflict(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	await(t, s, "ready", in(session.Ready))
}

func TestNAKIsAnError(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdGetProfile, Action: emu.NAK})
	d.Inject(emu.Fault{Cmd: wire.CmdRead, Match: at(6912), Action: emu.NAK})
	s := start(t, b, session.Options{})
	sn := await(t, s, "ready", idle)
	if want := (session.Probe{Asked: true}); sn.Profile != want {
		t.Errorf("Profile = %+v, want %+v", sn.Profile, want)
	}
	if want := []flash.Extent{{Addr: 6912, Len: 10}}; !slices.Equal(sn.Unread, want) {
		t.Errorf("Unread = %v, want %v", sn.Unread, want)
	}
	if sn.Image.Known(flash.Extent{Addr: 6912, Len: 1}) {
		t.Error("the rejected chunk is marked known")
	}
	if n := count(d.Writes(), wire.CmdGetProfile); n != 1 {
		t.Errorf("cmd 14 sent %d times", n)
	}
	if n := slices.Index(reads(d.Writes())[1:], flash.Extent{Addr: 6912, Len: 10}); n >= 0 && slices.Contains(reads(d.Writes())[n+2:], flash.Extent{Addr: 6912, Len: 10}) {
		t.Error("the rejected read was sent again")
	}
	if sn.Stats.NAKs != 2 || sn.State != session.Ready {
		t.Errorf("NAKs %d, state %v", sn.Stats.NAKs, sn.State)
	}
}

// scripted is a single interface whose replies a test writes by hand.
type scripted struct {
	mu     sync.Mutex
	pipe   *hidio.Pipe
	writes []wire.Packet
	answer func(p wire.Packet) []wire.Packet
}

var scriptedCandidate = hidio.Candidate{Backend: "script", Path: "script:1", VID: 0x260D, PID: 0x1282, Class: catalog.ClassComposite, Interface: 1}

func newScripted(answer func(wire.Packet) []wire.Packet) *scripted {
	sc := &scripted{answer: answer}
	sc.pipe = hidio.NewPipe(func(p wire.Packet) error {
		sc.mu.Lock()
		sc.writes = append(sc.writes, p)
		sc.mu.Unlock()
		for _, r := range sc.answer(p) {
			sc.pipe.Deliver(wire.ReportID, r[:])
		}
		return nil
	})
	return sc
}

func (sc *scripted) Enumerate() ([]hidio.Candidate, error) {
	return []hidio.Candidate{scriptedCandidate}, nil
}

func (sc *scripted) Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error) {
	return hidio.Guarded(sc.pipe, g), nil
}

func (sc *scripted) sent() []wire.Packet {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return slices.Clone(sc.writes)
}

// device answers like a receiver with an awake EM11 Pro and erased flash.
func device(p wire.Packet) []wire.Packet {
	switch p.Cmd() {
	case wire.CmdOnline:
		return []wire.Packet{reply(p.Cmd(), 0, 1, 0x33, 0x22, 0x11)}
	case wire.CmdHandshake:
		return []wire.Packet{reply(p.Cmd(), 0, p[5], p[6], p[7], p[8], 0x7B, 4, 0, 0)}
	case wire.CmdRead:
		data := make([]byte, p.Len())
		for i := range data {
			data[i] = 0xFF
		}
		return []wire.Packet{reply(p.Cmd(), p.Addr(), data...)}
	case wire.CmdBattery:
		return []wire.Packet{reply(p.Cmd(), 0, 50, 0, 0x0F, 0x3C, 0, 50)}
	}
	return []wire.Packet{reply(p.Cmd(), 0, 1, 2)}
}

func TestUnrelatedReportsDoNotUseUpTries(t *testing.T) {
	var prev wire.Packet
	sc := newScripted(func(p wire.Packet) []wire.Packet {
		out := device(p)
		if p.Cmd() == wire.CmdRead {
			noise := []wire.Packet{reply(wire.CmdStatusChanged, 0, 0, 0), reply(wire.CmdOnline, 0, 1, 0x33, 0x22, 0x11)}
			if prev != (wire.Packet{}) {
				noise = append(noise, prev)
			}
			prev = out[0]
			out = append(noise, out...)
		}
		return out
	})
	tm := fast()
	tm.Tries = 1
	s := session.New(session.Options{Devices: sc, Timing: tm})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()

	sn := await(t, s, "ready", idle)
	var rs []wire.Packet
	for _, p := range sc.sent() {
		if p.Cmd() == wire.CmdRead {
			if slices.Contains(rs, p) {
				t.Errorf("read %v sent twice", p)
			}
			rs = append(rs, p)
		}
	}
	if len(rs) != 34 || len(sn.Unread) != 0 {
		t.Errorf("%d reads, unread %v; want the 34 settings reads and nothing unread", len(rs), sn.Unread)
	}
	if st := sn.Stats; st.Pushes != 34 || st.PossiblePushes+st.Duplicates != 67 || st.Foreign != 0 {
		t.Errorf("stats %+v", sn.Stats)
	}
	if sn.Stats.FailedTries != 0 {
		t.Errorf("%d failed tries", sn.Stats.FailedTries)
	}
}

// The NAK layout is unknown until H1, and the web app ends a transaction on any
// status-1 frame. A NAK that does not echo the length still answers the read.
func TestNAKWithoutTheLengthAnswersTheRead(t *testing.T) {
	sc := newScripted(func(p wire.Packet) []wire.Packet {
		if p.Cmd() == wire.CmdRead && p.Addr() == 9504 {
			q := reply(wire.CmdRead, p.Addr())
			q[1] = byte(wire.StatusNAK)
			q[wire.Size-1] = q.Checksum()
			return []wire.Packet{q}
		}
		return device(p)
	})
	s := session.New(session.Options{Devices: sc, Timing: fast()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	await(t, s, "ready", idle)
	c, err := s.Backup(ctxT(t), true)
	if err != nil {
		t.Fatal(err)
	}
	sn := s.Snapshot()
	if sn.State != session.Ready || sn.Stats.Foreign != 0 || sn.Stats.NAKs != 1 {
		t.Errorf("state %v, foreign %d, NAKs %d; want the NAK taken as the answer", sn.State, sn.Stats.Foreign, sn.Stats.NAKs)
	}
	if want := []flash.Extent{{Addr: 9504, Len: 10}}; !slices.Equal(c.Missing, want) {
		t.Errorf("Missing = %v, want %v", c.Missing, want)
	}
}

// A status-1 frame that answers nothing arcctl asked is logged, not taken as
// another client.
func TestStrayNAKIsNotForeign(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	q := reply(wire.CmdBattery, 0)
	q[1] = byte(wire.StatusNAK)
	q[wire.Size-1] = q.Checksum()
	time.Sleep(2 * fast().Window)
	d.Deliver(q)
	sn := await(t, s, "the stray NAK", func(sn *session.Snapshot) bool { return sn.Stats.OddStatus > 0 || sn.Stats.Foreign > 0 })
	if sn.State != session.Ready || sn.Stats.Foreign != 0 {
		t.Errorf("state %v, foreign %d", sn.State, sn.Stats.Foreign)
	}
}
