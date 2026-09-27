//go:build hwtest

package hwtest

import (
	"context"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

// A raw read's claim keeps a late second answer from the session, until the
// session asks the same question itself: from then on the answers are its
// own.
func TestClaimsYieldToTheSession(t *testing.T) {
	var pipe *hidio.Pipe
	pipe = hidio.NewPipe(func(p wire.Packet) error {
		rep := p
		rep[5] = 0x42
		rep[15] = rep.Checksum()
		pipe.Deliver(wire.ReportID, rep[:])
		return nil
	})
	tp := newTap(pipe)
	defer tp.Close()
	rp := &rawPath{t: tp, note: func(string) {}, listen: 10 * time.Millisecond}
	ctx := context.Background()
	p, _ := wire.BuildRead(wire.Mouse, 0x60, 1)
	rep, _, err := rp.transact(ctx, p)
	must(t, err)
	if rep[5] != 0x42 {
		t.Fatalf("reply %v", rep)
	}
	late := p
	late[5] = 0x42
	late[15] = late.Checksum()
	pipe.Deliver(wire.ReportID, late[:])
	select {
	case r := <-tp.Reports():
		t.Fatalf("a late answer to the raw read reached the session: %v", r)
	case <-time.After(50 * time.Millisecond):
	}
	if tp.lateReplies() != 1 {
		t.Errorf("late replies %d", tp.lateReplies())
	}
	must(t, tp.WriteRaw(p))
	select {
	case r := <-tp.Reports():
		if f, ok := r.Packet(); !ok || f.Cmd() != wire.CmdRead {
			t.Fatalf("the session got %v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("the session's own read was taken by the raw path")
	}
}

// Replies to cmd 3 go to both: the raw path reads the online flag, and the
// session sees a report it takes as a push.
func TestOnlineCheckShares(t *testing.T) {
	var pipe *hidio.Pipe
	pipe = hidio.NewPipe(func(p wire.Packet) error {
		rep := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
		rep[4], rep[5], rep[6], rep[7], rep[8] = 4, 1, 0x33, 0x22, 0x11
		rep[15] = rep.Checksum()
		pipe.Deliver(wire.ReportID, rep[:])
		return nil
	})
	tp := newTap(pipe)
	defer tp.Close()
	rp := &rawPath{t: tp, note: func(string) {}}
	on, addr, err := rp.online(context.Background())
	must(t, err)
	if !on || addr != [3]byte{0x33, 0x22, 0x11} {
		t.Fatalf("online %v, addr % x", on, addr)
	}
	select {
	case <-tp.Reports():
	case <-time.After(time.Second):
		t.Fatal("the session did not see the cmd-3 reply")
	}
}

type pipeRaws struct {
	pipes []*hidio.Pipe
}

func (p *pipeRaws) Enumerate() ([]hidio.Candidate, error) { return nil, nil }

func (p *pipeRaws) OpenRaw(hidio.Candidate) (hidio.Raw, error) {
	pipe := hidio.NewPipe(func(wire.Packet) error { return nil })
	p.pipes = append(p.pipes, pipe)
	return pipe, nil
}

// A reply to the reset that comes after the stage opened a new session is
// kept from that session too, and counted late.
func TestLateResetRepliesReachNoSession(t *testing.T) {
	raws := &pipeRaws{}
	d := newDevices(raws)
	d.expect(isCmd(wire.CmdClear), time.Minute)
	tr, err := d.Open(hidio.Candidate{Path: "a"}, hidio.NewGuard(wire.Mouse), nil)
	must(t, err)
	defer tr.Close()
	tp, err := d.tap("a")
	must(t, err)
	late := resetPacket()
	raws.pipes[0].Deliver(wire.ReportID, late[:])
	other := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	raws.pipes[0].Deliver(wire.ReportID, other[:])
	select {
	case r := <-tr.Reports():
		if p, _ := r.Packet(); p.Cmd() != wire.CmdOnline {
			t.Fatalf("the session got %v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("the session got nothing")
	}
	if tp.lateReplies() != 1 {
		t.Errorf("late replies %d", tp.lateReplies())
	}
}
