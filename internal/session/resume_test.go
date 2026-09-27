package session_test

import (
	"testing"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func TestSleepMidLoadResumesFromTheFailedChunk(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdRead, Skip: 10, Times: 1, Action: emu.Asleep})
	s := start(t, b, session.Options{})
	sn := await(t, s, "the pause", func(sn *session.Snapshot) bool { return sn.State == session.Offline && sn.Progress.Job == "load" })
	if !sn.Progress.Paused || sn.Progress.Done != 10 {
		t.Errorf("Progress = %+v, want paused after 10 reads", sn.Progress)
	}
	d.Wake()
	sn = await(t, s, "ready", idle)

	want := workingSet()
	got := reads(d.Writes())
	failed := want[10]
	for i, e := range want {
		n := 0
		for _, g := range got {
			if g == e {
				n++
			}
		}
		switch {
		case i == 10 && n < 2:
			t.Errorf("the failed chunk %v was read %d times, want it again after the wake", e, n)
		case i != 10 && n != 1:
			t.Errorf("chunk %v read %d times", e, n)
		}
	}
	if last := lastIndex(got, failed); last < 0 || got[last+1] != want[11] {
		t.Errorf("the load did not resume at %v: %v", failed, got)
	}
	if n := count(d.Writes(), wire.CmdHandshake); n != 2 {
		t.Errorf("%d handshakes, want one per wake", n)
	}
	if len(sn.Unread) != 0 {
		t.Errorf("Unread = %v", sn.Unread)
	}
	sameBytes(t, sn.Image, d.Image(), want...)
}

func lastIndex(s []flash.Extent, e flash.Extent) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == e {
			return i
		}
	}
	return -1
}

func TestSleepMidBackupResumes(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	d.Inject(emu.Fault{Cmd: wire.CmdRead, Skip: 100, Times: 1, Action: emu.Asleep})

	type res struct {
		c   session.Capture
		err error
	}
	done := make(chan res, 1)
	go func() {
		c, err := s.Backup(ctxT(t), true)
		done <- res{c, err}
	}()
	await(t, s, "the pause", func(sn *session.Snapshot) bool { return sn.Progress.Job == "backup" && sn.Progress.Paused })
	d.Wake()
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.c.Missing) != 0 || !r.c.Full {
		t.Errorf("Missing %v, Full %v", r.c.Missing, r.c.Full)
	}
	sameBytes(t, r.c.Image, d.Image(), flash.Extent{Addr: 0, Len: 6987}, flash.Extent{Addr: 9504, Len: 256})
}
