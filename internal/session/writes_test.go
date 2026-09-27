package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

const ioReturnTimeout = emu.IOError(0xE00002D6)

// A report the receiver did not take (kIOReturnTimeout) was never sent, so it
// says nothing about another client taking the replies.
func TestWriteTimeoutsAreNotConflictSignals(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tm := fast()
	tm.Online = 20 * time.Millisecond
	s := start(t, b, session.Options{Timing: tm})
	base := await(t, s, "ready", idle).Stats
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Times: 2, Action: emu.Fail, Err: ioReturnTimeout})
	sn := await(t, s, "the two timed-out writes", func(sn *session.Snapshot) bool {
		return sn.Stats.WriteErrors >= base.WriteErrors+2 && sn.Stats.Transactions > base.Transactions+1
	})
	if sn.State != session.Ready || sn.Stats.FailedTries != base.FailedTries {
		t.Errorf("state %v, failed tries %d -> %d; timed-out writes count as unanswered tries", sn.State, base.FailedTries, sn.Stats.FailedTries)
	}
}

// A transaction whose every write timed out ends with the write error, not
// with ErrNoReply, and a device that keeps refusing writes is dropped and
// found again by a rescan.
func TestWritesThatKeepFailingDetach(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tm := fast()
	tm.Online = 20 * time.Millisecond
	tm.Rescan, tm.RescanMax = time.Second, time.Second
	s := start(t, b, session.Options{Timing: tm})
	base := await(t, s, "ready", idle).Stats
	tries := session.DefaultTiming().Tries
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Times: 3 * tries, Action: emu.Fail, Err: ioReturnTimeout})
	sn := await(t, s, "the device dropped", in(session.NoReceiver))
	if !errors.Is(sn.Err, session.ErrWrites) || hidio.Classify(sn.Err) != hidio.ClassTimeout || errors.Is(sn.Err, session.ErrNoReply) {
		t.Errorf("err %v, want ErrWrites with the timeout", sn.Err)
	}
	if sn.Stats.FailedTries != base.FailedTries || sn.Stats.WriteErrors != base.WriteErrors+3*tries {
		t.Errorf("failed tries %d -> %d, write errors %d -> %d; want only 3 transactions of write errors",
			base.FailedTries, sn.Stats.FailedTries, base.WriteErrors, sn.Stats.WriteErrors)
	}
	await(t, s, "ready again", idle)
}

// hidio retries kIOReturnError three times; a burst that outlasts them fails
// one transaction, which the op retries, and the device stays attached.
func TestTransientWriteErrorsKeepTheDevice(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	d.Inject(emu.Fault{Cmd: wire.CmdRead, Skip: 50, Times: 3, Action: emu.Fail})
	c, err := s.Backup(ctxT(t), true)
	if err != nil {
		t.Fatalf("a burst of kIOReturnError failed the full backup: %v (state %v)", err, s.Snapshot().State)
	}
	if len(c.Missing) != 0 {
		t.Errorf("Missing = %v", c.Missing)
	}
	if sn := s.Snapshot(); sn.State != session.Ready || sn.Stats.WriteErrors != 1 {
		t.Errorf("state %v, write errors %d", sn.State, sn.Stats.WriteErrors)
	}
}
