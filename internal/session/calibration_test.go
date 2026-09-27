package session_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// These tests run the session against the EM11 Pro as H0 measured it
// (emu.EM11Pro), with the committed dump and a bound macro.

func unit(t testing.TB) emu.Config { return emu.EM11Pro(macroImage(t)) }

// lossy is fast timing for runs where 2.5% of the radio replies are lost: a
// short try keeps the lost ones cheap, and a SuspectedConflict never clears,
// so the end state shows whether one was raised.
func lossy() session.Timing {
	tm := fast()
	tm.Try = 10 * time.Millisecond
	tm.Suspect = time.Hour
	return tm
}

const lossRate = 0.025

// tally counts, over many runs, the transactions whose first 3 tries went
// unanswered: each would have raised SuspectedConflict under the threshold
// H0 replaced.
type tally struct {
	mu            sync.Mutex
	runs, reads   int
	threePlus, hi int
}

func (ty *tally) add(ws []emu.Write) {
	ty.mu.Lock()
	defer ty.mu.Unlock()
	ty.runs++
	for _, n := range resends(ws) {
		ty.reads++
		if n >= 4 {
			ty.threePlus++
		}
		ty.hi = max(ty.hi, n)
	}
}

func (ty *tally) log(t *testing.T) {
	t.Logf("%d runs, %d transactions: %d needed 4 or more tries, the most took %d", ty.runs, ty.reads, ty.threePlus, ty.hi)
}

// resends counts how many times each radio request in ws went out in a row.
func resends(ws []emu.Write) []int {
	var out []int
	for i := 0; i < len(ws); {
		j := i + 1
		for j < len(ws) && ws[j].Packet == ws[i].Packet {
			j++
		}
		if c := ws[i].Packet.Cmd(); c != wire.CmdOnline && ws[i].Interface == 1 {
			out = append(out, j-i)
		}
		i = j
	}
	return out
}

func runs(t *testing.T, normal, short int) int {
	if testing.Short() {
		return short
	}
	return normal
}

// At 2.5% of radio replies lost, full backups complete and never raise
// SuspectedConflict: the tries of one read cover the losses.
func TestLossDuringFullBackups(t *testing.T) {
	var ty tally
	t.Cleanup(func() { ty.log(t) })
	for i := range runs(t, 48, 8) {
		t.Run(fmt.Sprint("seed ", i+1), func(t *testing.T) {
			t.Parallel()
			b := newBus(t, emu.Options{Seed: uint64(i + 1)})
			c := unit(t)
			c.Behavior.Loss = lossRate
			d := add(t, b, c)
			s := start(t, b, session.Options{Timing: lossy()})
			await(t, s, "ready", idle)
			capt, err := s.Backup(ctxT(t), true)
			if err != nil {
				t.Fatal(err)
			}
			if !capt.Full || len(capt.Missing) != 0 {
				t.Fatalf("backup full %v, missing %v", capt.Full, capt.Missing)
			}
			sn := s.Snapshot()
			if sn.State != session.Ready || sn.Stats.FailedTries == 0 || len(sn.Unread) != 0 {
				t.Fatalf("state %v, failed tries %d, unread %v", sn.State, sn.Stats.FailedTries, sn.Unread)
			}
			ty.add(d.Writes())
		})
	}
}

// longMacro is the longest macro a slot holds: 70 events, 39 chunks.
func longMacro() mouse.Macro {
	m := mouse.Macro{Name: "long"}
	for i := range mouse.MaxMacroEvents {
		m.Events = append(m.Events, mouse.Event{Press: i%2 == 0, Stroke: keys.Stroke{Kind: keys.KindKey, Value: 0x04}, Delay: 10})
	}
	return m
}

// At 2.5% of radio replies lost, the first write to the device (a full
// backup, then a two-phase rebind around a 39-chunk macro, read back and
// reloaded) completes and never raises SuspectedConflict.
func TestLossDuringLongApply(t *testing.T) {
	var ty tally
	t.Cleanup(func() { ty.log(t) })
	for i := range runs(t, 24, 4) {
		t.Run(fmt.Sprint("seed ", i+1), func(t *testing.T) {
			t.Parallel()
			c := unit(t)
			c.Behavior.Loss = lossRate
			r := newRigOn(t, emu.Options{Seed: uint64(100 + i)}, c, checkEdit)
			s, _ := r.ready(func(o *session.Options) { o.Timing = lossy() })
			e, _ := mouse.MacroExtent(4)
			if _, err := s.Read(ctxT(t), e); err != nil {
				t.Fatal(err)
			}
			sn := await(t, s, "the macro slot", func(sn *session.Snapshot) bool { return sn.Image.Known(e) })
			p := planOn(t, sn, sn.Image, mouse.SetMacro{Slot: 4, Macro: longMacro(), Cycle: 1})
			out, err := s.Apply(ctxT(t), p, allow(p), nil)
			if err != nil {
				t.Fatalf("apply: %v (state %v)", err, s.Snapshot().State)
			}
			if out.Verified != len(p.Ops) || len(r.logicalCmd7()) < 39 {
				t.Fatalf("verified %d of %d ops, %d chunks", out.Verified, len(p.Ops), len(r.logicalCmd7()))
			}
			holds(t, "device", r.dev.Image(), afterPlan(t, sn.Image, p), p)
			sn = await(t, s, "the reload", idle)
			if sn.Stats.FailedTries == 0 {
				t.Error("no reply was lost")
			}
			ty.add(r.dev.Writes())
		})
	}
}

// A client that steals half the replies is still caught by the mouse
// threshold alone, with no cmd-3 poll to help.
func TestStealIsSuspectedDuringALoad(t *testing.T) {
	for seed := range uint64(8) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			b := newBus(t, emu.Options{Seed: seed})
			c := unit(t)
			c.Behavior.Sharing = emu.Steal
			d := add(t, b, c)
			comp := d.Chrome(0)
			t.Cleanup(comp.Close)
			tm := fast()
			tm.Suspect = time.Hour
			s := start(t, b, session.Options{Timing: tm})
			await(t, s, "a suspected conflict", in(session.SuspectedConflict))
		})
	}
}

// A replugged receiver answers cmd 3 at once while the mouse sleeps, and
// cmd 29, which the mouse answers, goes unanswered: the session waits in
// Offline without resending it, asks again once the mouse wakes and loads.
func TestReplugWhileTheMouseSleeps(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, unit(t))
	s := start(t, b, session.Options{})
	sn := await(t, s, "ready", idle)
	if sn.Versions.Receiver != "v1.0" || sn.Versions.Mouse != "v1.26" || sn.Profile.Supported || sn.LongRange.Supported {
		t.Fatalf("versions %+v, profile %+v, long range %+v", sn.Versions, sn.Profile, sn.LongRange)
	}
	d.Sleep()
	d.Unplug()
	await(t, s, "no receiver", in(session.NoReceiver))
	n := len(d.Writes())
	d.Plug()
	sn = await(t, s, "offline", in(session.Offline))
	if sn.Err != nil || sn.Versions.Receiver != "" {
		t.Fatalf("offline after the replug: err %v, versions %+v", sn.Err, sn.Versions)
	}
	time.Sleep(4 * fast().Offline)
	after := d.Writes()[n:]
	if got := count(after, wire.CmdRxVersion); got != 1 {
		t.Errorf("cmd 29 sent %d times to a sleeping mouse, want 1", got)
	}
	if got := count(after, wire.CmdHandshake); got != 0 {
		t.Errorf("%d handshakes while the mouse sleeps", got)
	}
	d.Wake()
	sn = await(t, s, "ready after the wake", idle)
	if sn.Versions.Receiver != "v1.0" {
		t.Errorf("receiver version %q after the wake", sn.Versions.Receiver)
	}
	if got := count(d.Writes()[n:], wire.CmdRxVersion); got != 2 {
		t.Errorf("cmd 29 sent %d times since the replug, want 2", got)
	}
}

// The unit pushes nothing when it falls asleep or wakes: the session finds
// out from its cmd-3 poll and from the input reports.
func TestSleepAndWakeWithoutPushes(t *testing.T) {
	b := newBus(t, emu.Options{})
	c := unit(t)
	c.Mouse.SleepAfter = time.Second
	d := add(t, b, c)
	tm := fast()
	tm.Online = 20 * time.Millisecond
	s := start(t, b, session.Options{Timing: tm})
	await(t, s, "ready", idle)
	sn := await(t, s, "offline once the mouse sleeps", in(session.Offline))
	if sn.Stats.Pushes != 0 || sn.Stats.PossiblePushes != 0 {
		t.Fatalf("pushes %d, cmd-3 reports nobody asked for %d", sn.Stats.Pushes, sn.Stats.PossiblePushes)
	}
	handshakes := count(d.Writes(), wire.CmdHandshake)
	d.Wake()
	sn = await(t, s, "ready after the wake", idle)
	if count(d.Writes(), wire.CmdHandshake) == handshakes || sn.Stats.Pushes != 0 {
		t.Errorf("no handshake after the wake, or pushes %d", sn.Stats.Pushes)
	}
}

// With a push, a DPI press shows up at once; without one, only a read of the
// device does: the preflight's re-read refuses a plan made on the stale image
// and shows the new stage, and a reload does too.
func TestDPIPressWithAndWithoutPush(t *testing.T) {
	cur := flash.Extent{Addr: mouse.AddrCurrentDPI, Len: 2}
	for _, silent := range []bool{false, true} {
		t.Run(fmt.Sprint("silent ", silent), func(t *testing.T) {
			c := unit(t)
			c.Behavior.SilentDPI = silent
			r := newRigOn(t, emu.Options{}, c, checkEdit)
			s, sn := r.ready(nil)
			stale := planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1})
			must(t, r.dev.PressDPI())
			if !silent {
				sn = await(t, s, "the re-read", func(sn *session.Snapshot) bool { return idle(sn) && sn.Stats.Pushes == 1 })
				sameBytes(t, sn.Image, r.dev.Image(), cur)
				return
			}
			time.Sleep(3 * fast().Window)
			a, _ := s.Snapshot().Image.Get(cur)
			b, _ := r.dev.Image().Get(cur)
			if s.Snapshot().Stats.Pushes != 0 || slices.Equal(a, b) {
				t.Fatal("the session saw a silent press")
			}
			if _, err := s.Apply(ctxT(t), stale, allow(stale), nil); !errors.Is(err, safety.ErrStale) {
				t.Fatalf("apply on the stale image: %v, want ErrStale", err)
			}
			sameBytes(t, s.Snapshot().Image, r.dev.Image(), cur)
			must(t, r.dev.PressDPI())
			must(t, s.Reload(ctxT(t)))
			sameBytes(t, s.Snapshot().Image, r.dev.Image(), cur)
		})
	}
}

// A DPI press during an apply that writes the current stage later stops the
// apply when the mouse pushes (I7). When it does not, the apply still checks
// that the mouse is online before each record and reads every record back
// (I4), so it ends with the plan's bytes, the press overwritten.
func TestDPIPressDuringApply(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(fmt.Sprint("silent ", silent), func(t *testing.T) {
			c := unit(t)
			c.Behavior.SilentDPI = silent
			r := newRigOn(t, emu.Options{}, c, checkEdit)
			s, sn := r.ready(nil)
			p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900}, mouse.SetCurrent{Stage: 1})
			n := len(r.dev.Writes())
			pressed := false
			out, err := s.Apply(ctxT(t), p, allow(p), func(e safety.OpEvent) {
				if e.Kind == safety.EventWritten && !pressed {
					pressed = true
					must(t, r.dev.PressDPI())
				}
			})
			await(t, s, "the reload", func(sn *session.Snapshot) bool { return sn.Link == session.Ready && sn.Progress.Job == "" })
			if !silent {
				if !errors.Is(err, safety.ErrOverlap) {
					t.Fatalf("err = %v, want ErrOverlap", err)
				}
				return
			}
			if err != nil || out.Verified != len(p.Ops) {
				t.Fatalf("apply: %v, %d of %d verified", err, out.Verified, len(p.Ops))
			}
			holds(t, "device", r.dev.Image(), afterPlan(t, sn.Image, p), p)
			checkRecords(t, emu.Logical(r.dev.Writes()[n:]), p)
		})
	}
}

// checkRecords fails unless, in ws, a cmd 3 comes right before the first
// chunk of each record of p, and a read of the record right after its last.
func checkRecords(t *testing.T, ws []emu.Write, p plan.Plan) {
	t.Helper()
	for _, op := range p.Ops {
		first := slices.IndexFunc(ws, func(w emu.Write) bool {
			return w.Packet.Cmd() == wire.CmdWrite && int(w.Packet.Addr()) == op.Extent.Addr
		})
		last := first + (len(op.New)+wire.MaxData-1)/wire.MaxData - 1
		switch {
		case first < 1 || last+1 >= len(ws):
			t.Errorf("%v: not written", op.Extent)
		case ws[first-1].Packet.Cmd() != wire.CmdOnline:
			t.Errorf("%v: %v before its first chunk, want cmd 3", op.Extent, ws[first-1].Packet.Cmd())
		case ws[last+1].Packet.Cmd() != wire.CmdRead || int(ws[last+1].Packet.Addr()) != op.Extent.Addr:
			t.Errorf("%v: %v after its last chunk, want its read-back", op.Extent, ws[last+1].Packet)
		}
	}
}

// With the latency H0 measured, a load needs no second try and no reply
// comes late: 200 ms a try is ample for replies that take up to 56 ms.
func TestMeasuredLatencyNeedsNoRetry(t *testing.T) {
	b := newBus(t, emu.Options{Seed: 5})
	c := unit(t)
	c.Latency = emu.EM11ProLatency()
	d := add(t, b, c)
	tm := fast()
	tm.Try, tm.ProbeTry = 0, 0
	s := start(t, b, session.Options{Timing: tm})
	sn := await(t, s, "ready", idle)
	ws := slices.DeleteFunc(d.Writes(), func(w emu.Write) bool { return w.Interface != sn.Device.Interface })
	if n := len(ws) - len(emu.Logical(ws)); n != 0 || sn.Stats.Late != 0 || len(sn.Unread) != 0 {
		t.Errorf("%d tries sent again, late %d, unread %v", n, sn.Stats.Late, sn.Unread)
	}
}
