package hidio_test

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

var readCmds = []wire.Cmd{
	wire.CmdHandshake, wire.CmdOnline, wire.CmdBattery, wire.CmdRead,
	wire.CmdGetProfile, wire.CmdFWVersion, wire.CmdGetLongRange, wire.CmdRxVersion,
}

// sink is a Raw that remembers every packet that reached it.
type sink struct {
	*hidio.Pipe
	sent []wire.Packet
}

func newSink() *sink {
	s := &sink{}
	s.Pipe = hidio.NewPipe(func(p wire.Packet) error {
		s.sent = append(s.sent, p)
		return nil
	})
	return s
}

func guarded(t *testing.T, target wire.Target) (hidio.Transport, *sink, *hidio.Guard) {
	t.Helper()
	s := newSink()
	g := hidio.NewGuard(target)
	tr := hidio.Guarded(s, g)
	t.Cleanup(func() { tr.Close() })
	return tr, s, g
}

func TestGuardZeroValueIsReadOnlyMouse(t *testing.T) {
	var g hidio.Guard
	if g.Policy() != wire.ReadOnly || g.Target() != wire.Mouse {
		t.Fatalf("zero Guard is %s for the %s, want read-only for the mouse", g.Policy(), g.Target())
	}
	if err := g.Check(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); err != nil {
		t.Fatalf("zero Guard refuses cmd 3: %v", err)
	}
}

func TestGuardSetEnablesOnlyReadOnly(t *testing.T) {
	tests := []struct {
		policy wire.Policy
		ok     bool
	}{
		{wire.ReadOnly, true},
		{wire.Edit, false},
		{wire.Reset, false},
		{wire.Experimental, false},
		{wire.Policy(9), false},
	}
	for _, tt := range tests {
		t.Run(tt.policy.String(), func(t *testing.T) {
			g := hidio.NewGuard(wire.Mouse)
			err := g.Set(tt.policy, "test")
			if tt.ok != (err == nil) {
				t.Fatalf("Set(%s) = %v, want ok %v", tt.policy, err, tt.ok)
			}
			if !tt.ok && !errors.Is(err, hidio.ErrForbidden) {
				t.Fatalf("Set(%s) = %v, want ErrForbidden", tt.policy, err)
			}
			if g.Policy() != wire.ReadOnly {
				t.Fatalf("policy after Set(%s) is %s", tt.policy, g.Policy())
			}
			log := g.Changes()
			if len(log) != 1 || log[0].Reason != "test" || (log[0].Err == nil) != tt.ok {
				t.Fatalf("change log = %+v", log)
			}
		})
	}
}

func TestGuardSetTarget(t *testing.T) {
	g := hidio.NewGuard(wire.Keyboard)
	if err := g.SetTarget(wire.Mouse, "retry without the flag"); err != nil {
		t.Fatal(err)
	}
	if err := g.SetTarget(wire.Target(0x40), "bad"); !errors.Is(err, hidio.ErrForbidden) {
		t.Fatalf("SetTarget(0x40) = %v, want ErrForbidden", err)
	}
	if g.Target() != wire.Mouse {
		t.Fatalf("target = %s, want mouse", g.Target())
	}
	if log := g.Changes(); len(log) != 2 || log[0].Target != wire.Mouse || log[1].Err == nil {
		t.Fatalf("change log = %+v", log)
	}
}

func TestGuardedRefusesBeforeIO(t *testing.T) {
	tests := []struct {
		name   string
		target wire.Target
		p      wire.Packet
		want   error
	}{
		{"cmd 7 write", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 1, 0, 0x53}), wire.ErrForbidden},
		{"cmd 9 clear", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil), wire.ErrForbidden},
		{"cmd 22 long range", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0}), wire.ErrForbidden},
		{"cmd 5 pair", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdPair, 0, []byte{0, 0x7b}), wire.ErrForbidden},
		{"cmd 6 pair state", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdPairState, 0, nil), wire.ErrForbidden},
		{"cmd 15 set profile", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdSetProfile, 0, []byte{1}), wire.ErrForbidden},
		{"cmd 2 driver status", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdDriverStatus, 0, []byte{1}), wire.ErrForbidden},
		{"cmd 20 receiver led", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdSetRxLED4K, 0, []byte{1}), wire.ErrForbidden},
		{"cmd 176 keyboard music", wire.Keyboard, wire.MustBuild(wire.Keyboard, wire.Cmd(176), 0, nil), wire.ErrForbidden},
		{"keyboard cmd 7", wire.Keyboard, wire.MustBuild(wire.Keyboard, wire.CmdWrite, 0x24c0, []byte{1}), wire.ErrForbidden},
		{"keyboard flag on a mouse session", wire.Mouse, wire.MustBuild(wire.Keyboard, wire.CmdOnline, 0, nil), wire.ErrTarget},
		{"no flag on a keyboard session", wire.Keyboard, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil), wire.ErrTarget},
		{"bad checksum", wire.Mouse, func() wire.Packet { p := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil); p[15]++; return p }(), wire.ErrChecksum},
		{"cmd 3 with data", wire.Mouse, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, []byte{1}), wire.ErrFraming},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, s, _ := guarded(t, tt.target)
			err := tr.Write(tt.p)
			if !errors.Is(err, hidio.ErrForbidden) || !errors.Is(err, tt.want) {
				t.Fatalf("Write = %v, want ErrForbidden and %v", err, tt.want)
			}
			if hidio.Classify(err) != hidio.ClassRefused {
				t.Fatalf("Classify = %s, want refused", hidio.Classify(err))
			}
			if len(s.sent) != 0 {
				t.Fatalf("refused packet reached the Raw: %v", s.sent)
			}
		})
	}
}

func TestGuardedPassesReads(t *testing.T) {
	for _, target := range []wire.Target{wire.Mouse, wire.Keyboard} {
		tr, s, _ := guarded(t, target)
		read, err := wire.BuildRead(target, 0x60, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []wire.Packet{wire.MustBuild(target, wire.CmdOnline, 0, nil), read} {
			if err := tr.Write(p); err != nil {
				t.Fatalf("%s: Write(%s) = %v", target, p, err)
			}
		}
		if len(s.sent) != 2 {
			t.Fatalf("%s: %d packets reached the Raw, want 2", target, len(s.sent))
		}
	}
}

func TestGuardedRefusedPolicyChangeKeepsWritesOut(t *testing.T) {
	tr, s, g := guarded(t, wire.Mouse)
	if err := g.Set(wire.Edit, "M2 must stay read-only"); err == nil {
		t.Fatal("Set(Edit) succeeded")
	}
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 1, 0, 0x53})); !errors.Is(err, hidio.ErrForbidden) {
		t.Fatalf("cmd 7 after a refused Set(Edit) = %v", err)
	}
	if len(s.sent) != 0 {
		t.Fatal("cmd 7 reached the Raw")
	}
}

// requestTemplates builds well-formed requests for every command the protocol
// knows, forbidden ones included, so that the property test exercises the
// policy and not only the framing checks.
func requestTemplates(r *rand.Rand, t wire.Target) []wire.Packet {
	data := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		return b
	}
	addr := uint16(r.IntN(16384))
	read, _ := wire.BuildRead(t, addr, 1+r.IntN(wire.MaxData))
	lr := make([]byte, wire.MaxData)
	lr[0] = byte(r.IntN(2))
	ps := []wire.Packet{
		wire.MustBuild(t, wire.CmdHandshake, 0, append(data(4), 0, 0, 0, 0)),
		wire.MustBuild(t, wire.CmdOnline, 0, nil),
		wire.MustBuild(t, wire.CmdBattery, 0, nil),
		read,
		wire.MustBuild(t, wire.CmdGetProfile, 0, nil),
		wire.MustBuild(t, wire.CmdFWVersion, 0, nil),
		wire.MustBuild(t, wire.CmdGetLongRange, 0, nil),
		wire.MustBuild(t, wire.CmdRxVersion, 0, nil),
		wire.MustBuild(t, wire.CmdWrite, addr, data(1+r.IntN(wire.MaxData))),
		wire.MustBuild(t, wire.CmdClear, 0, nil),
		wire.MustBuild(t, wire.CmdSetLongRange, 0, lr),
		wire.MustBuild(t, wire.CmdPair, 0, data(2)),
		wire.MustBuild(t, wire.CmdPairState, 0, nil),
		wire.MustBuild(t, wire.CmdSetProfile, 0, data(1)),
		wire.MustBuild(t, wire.CmdStatusChanged, 0, data(r.IntN(wire.MaxData+1))),
	}
	for _, c := range []int{2, 20, 21, 24, 25, 44, 45, 46, 47, 48, 49, 50, 176, 177, 178, 179, 180, 181, 182, 183} {
		ps = append(ps, wire.MustBuild(t, wire.Cmd(c), 0, data(r.IntN(wire.MaxData+1))))
	}
	return ps
}

func randomPacket(r *rand.Rand) wire.Packet {
	var p wire.Packet
	target := []wire.Target{wire.Mouse, wire.Keyboard}[r.IntN(2)]
	switch r.IntN(4) {
	case 0:
		for i := range p {
			p[i] = byte(r.Uint32())
		}
	case 1:
		ps := requestTemplates(r, target)
		p = ps[r.IntN(len(ps))]
		p[r.IntN(wire.Size)] ^= byte(1 << r.IntN(8))
	default:
		ps := requestTemplates(r, target)
		p = ps[r.IntN(len(ps))]
	}
	return p
}

// TestGuardedAllowlistProperty sends random packets through read-only guards
// for both targets: only well-formed read commands for the guard's target may
// reach the Raw, and everything else is refused before I/O.
func TestGuardedAllowlistProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, target := range []wire.Target{wire.Mouse, wire.Keyboard} {
		tr, s, _ := guarded(t, target)
		var passed, forbidden int
		for range 20000 {
			p := randomPacket(r)
			before := len(s.sent)
			err := tr.Write(p)
			if !slices.Contains(readCmds, p.Cmd()) {
				forbidden++
			}
			switch {
			case err == nil:
				passed++
				if len(s.sent) != before+1 || s.sent[before] != p {
					t.Fatalf("%s: accepted %s did not reach the Raw exactly once", target, p)
				}
			case !errors.Is(err, hidio.ErrForbidden):
				t.Fatalf("%s: Write(%s) = %v, want nil or ErrForbidden", target, p, err)
			case len(s.sent) != before:
				t.Fatalf("%s: refused %s reached the Raw", target, p)
			}
		}
		for _, p := range s.sent {
			if !slices.Contains(readCmds, p.Cmd()) || p.Target() != target || wire.ReadOnly.Check(p, target) != nil {
				t.Fatalf("%s: %s reached the Raw", target, p)
			}
		}
		if passed == 0 || forbidden == 0 {
			t.Fatalf("%s: generator produced %d accepted and %d forbidden packets", target, passed, forbidden)
		}
	}
}

func FuzzGuardedReadOnly(f *testing.F) {
	for _, p := range []wire.Packet{
		wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil),
		wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 1, 0, 0x53}),
		wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil),
		wire.MustBuild(wire.Keyboard, wire.CmdRead, 0x24c0, make([]byte, 10)),
	} {
		f.Add(p[:], p.Target() == wire.Keyboard)
	}
	f.Fuzz(func(t *testing.T, b []byte, keyboard bool) {
		target := wire.Mouse
		if keyboard {
			target = wire.Keyboard
		}
		var p wire.Packet
		copy(p[:], b)
		s := newSink()
		tr := hidio.Guarded(s, hidio.NewGuard(target))
		defer tr.Close()
		err := tr.Write(p)
		if err != nil {
			if !errors.Is(err, hidio.ErrForbidden) || len(s.sent) != 0 {
				t.Fatalf("Write(%s) = %v with %d packets sent", p, err, len(s.sent))
			}
			return
		}
		if len(s.sent) != 1 || !slices.Contains(readCmds, p.Cmd()) || p.Target() != target {
			t.Fatalf("%s reached the Raw of a read-only %s guard", p, target)
		}
	})
}
