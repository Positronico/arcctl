package session_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// TestOnlyReadsReachTheDevice drives sessions through random device events,
// faults and API calls, and checks that each one recovers. The cleanup of add
// then checks that every packet the emulator received is one the read-only
// policy allows.
func TestOnlyReadsReachTheDevice(t *testing.T) {
	seeds := uint64(8)
	if testing.Short() {
		seeds = 2
	}
	for seed := range seeds {
		t.Run("", func(t *testing.T) { chaos(t, seed) })
	}
}

func chaos(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 0xa11))
	b := newBus(t, emu.Options{Seed: seed, Watchdog: 40 * time.Millisecond})
	c := receiver(em11(t))
	c.Behavior.Sharing = emu.Sharing(rng.IntN(2))
	d := add(t, b, c)
	tm := fast()
	tm.Online, tm.Battery = 15*time.Millisecond, 25*time.Millisecond
	s := start(t, b, session.Options{Timing: tm})

	var calls sync.WaitGroup
	defer calls.Wait()
	call := func(f func(context.Context)) {
		calls.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			f(ctx)
		})
	}
	cmds := []wire.Cmd{0, wire.CmdOnline, wire.CmdHandshake, wire.CmdRead, wire.CmdBattery, wire.CmdGetProfile, wire.CmdRxVersion}
	actions := []emu.Action{emu.Drop, emu.NAK, emu.Duplicate, emu.Late, emu.Asleep, emu.Hang, emu.Fail}
	var comp *emu.Competitor
	for range 80 {
		switch rng.IntN(16) {
		case 0:
			d.Push(byte(rng.IntN(256)), byte(rng.IntN(256)))
		case 1:
			d.Sleep()
		case 2, 3:
			d.Wake()
		case 4:
			_ = d.PressDPI()
		case 5:
			_ = d.SwitchProfile(byte(rng.IntN(3)))
		case 6:
			call(func(ctx context.Context) { _ = s.Reload(ctx) })
		case 7:
			full := rng.IntN(2) == 0
			call(func(ctx context.Context) { _, _ = s.Backup(ctx, full) })
		case 8:
			e := flash.Extent{Addr: rng.IntN(flash.Size - 40), Len: 1 + rng.IntN(40)}
			call(func(ctx context.Context) { _, _ = s.Read(ctx, e) })
		case 9:
			d.Inject(emu.Fault{
				Cmd:    cmds[rng.IntN(len(cmds))],
				Skip:   rng.IntN(5),
				Times:  1 + rng.IntN(3),
				Action: actions[rng.IntN(len(actions))],
				Delay:  time.Duration(rng.IntN(80)) * time.Millisecond,
			})
		case 10:
			d.Release()
			d.Noise(1 + rng.IntN(5))
		case 11:
			var p wire.Packet
			for i := range p {
				p[i] = byte(rng.IntN(256))
			}
			d.Deliver(p)
		case 12:
			d.SetLocked(true)
			time.Sleep(time.Duration(rng.IntN(60)) * time.Millisecond)
			d.SetLocked(false)
		case 13:
			d.Unplug()
			time.Sleep(time.Duration(rng.IntN(60)) * time.Millisecond)
			d.Plug()
		case 14:
			if comp == nil {
				comp = d.Chrome(time.Duration(5+rng.IntN(20)) * time.Millisecond)
			} else {
				comp.Close()
				comp = nil
			}
		case 15:
			call(func(ctx context.Context) {
				if _, err := s.Apply(ctx, plan.Plan{Ops: []plan.Op{{New: []byte{1}}}}, safety.Gates{}, nil); !errors.Is(err, session.ErrReadOnly) {
					t.Errorf("Apply = %v", err)
				}
			})
		}
		time.Sleep(time.Duration(rng.IntN(15)) * time.Millisecond)
	}
	if comp != nil {
		comp.Close()
	}
	d.Release()
	d.Wake()
	sn := await(t, s, "recovery", func(sn *session.Snapshot) bool { return sn.Link == session.Ready && sn.Image != nil })
	if sn.Policy != wire.ReadOnly {
		t.Errorf("Policy = %v", sn.Policy)
	}
}
