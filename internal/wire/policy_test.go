package wire_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

var readCmds = []byte{1, 3, 4, 8, 14, 18, 23, 29}

var mouseAllow = map[wire.Policy][]byte{
	wire.ReadOnly:     readCmds,
	wire.Edit:         {1, 3, 4, 7, 8, 14, 18, 23, 29},
	wire.Reset:        {1, 3, 4, 8, 9, 14, 18, 23, 29},
	wire.Experimental: {1, 3, 4, 7, 8, 14, 18, 22, 23, 29},
}

var neverAllowed = func() []byte {
	s := []byte{2, 5, 6, 10, 15, 20, 21, 24, 25, 44, 45, 46, 47, 48, 49, 50}
	for c := 176; c <= 183; c++ {
		s = append(s, byte(c))
	}
	return s
}()

func tableAllows(p wire.Policy, t wire.Target, c byte) bool {
	switch t {
	case wire.Mouse:
		return slices.Contains(mouseAllow[p], c)
	case wire.Keyboard:
		_, known := mouseAllow[p]
		return known && slices.Contains(readCmds, c)
	}
	return false
}

var policiesUnderTest = []wire.Policy{wire.ReadOnly, wire.Edit, wire.Reset, wire.Experimental, 4, 5, 128, 255}

func TestAllowlistTables(t *testing.T) {
	for _, p := range policiesUnderTest {
		for tg := range 256 {
			for c := range 256 {
				pol, target, cmd := p, wire.Target(tg), wire.Cmd(c)
				want := tableAllows(pol, target, byte(c))
				if got := pol.Allows(cmd, target); got != want {
					t.Errorf("%v.Allows(%v, %v) = %v, want %v", pol, cmd, target, got, want)
				}
			}
		}
	}
}

func TestNeverAllowed(t *testing.T) {
	named := []wire.Cmd{
		wire.CmdDriverStatus, wire.CmdPair, wire.CmdPairState, wire.CmdStatusChanged, wire.CmdSetProfile,
		wire.CmdSetRxLED4K, wire.CmdGetRxLED4K, wire.CmdSetRxLEDBar, wire.CmdGetRxLEDBar, wire.CmdSetRxLED3, wire.CmdGetRxLED3,
	}
	for _, c := range named {
		if !slices.Contains(neverAllowed, byte(c)) {
			t.Errorf("%v missing from the never-allowed table", c)
		}
	}
	for _, p := range policiesUnderTest {
		for tg := range 256 {
			for _, c := range neverAllowed {
				if p.Allows(wire.Cmd(c), wire.Target(tg)) {
					t.Errorf("%v allows %v for %v", p, wire.Cmd(c), wire.Target(tg))
				}
			}
		}
	}
}

func TestPolicyLadder(t *testing.T) {
	tests := []struct {
		c    wire.Cmd
		want []wire.Policy
	}{
		{wire.CmdHandshake, allPolicies},
		{wire.CmdWrite, []wire.Policy{wire.Edit, wire.Experimental}},
		{wire.CmdClear, []wire.Policy{wire.Reset}},
		{wire.CmdSetLongRange, []wire.Policy{wire.Experimental}},
		{wire.CmdSetProfile, nil},
		{wire.CmdPair, nil},
	}
	for _, tt := range tests {
		for _, p := range allPolicies {
			if got, want := p.Allows(tt.c, wire.Mouse), slices.Contains(tt.want, p); got != want {
				t.Errorf("%v.Allows(%v, mouse) = %v, want %v", p, tt.c, got, want)
			}
		}
	}
	var zero wire.Policy
	if zero != wire.ReadOnly {
		t.Errorf("zero Policy is %v, want read-only", zero)
	}
}

func TestCheck(t *testing.T) {
	online := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	kbOnline := wire.MustBuild(wire.Keyboard, wire.CmdOnline, 0, nil)
	write := wire.MustBuild(wire.Mouse, wire.CmdWrite, 4, []byte{3, 0x52})
	kbWrite := wire.MustBuild(wire.Keyboard, wire.CmdWrite, 9504, []byte{1, 2, 3, 4, 0, 0, 0, 0})
	read := wire.MustBuild(wire.Mouse, wire.CmdRead, 0x60, make([]byte, 10))
	with := func(p wire.Packet, i int, v byte) wire.Packet {
		p[i] = v
		return reseal(p)
	}
	forbidden := func(c byte) wire.Packet { return reseal(wire.Packet{c}) }
	tests := []struct {
		name string
		p    wire.Policy
		pk   wire.Packet
		t    wire.Target
		want error
	}{
		{"online read-only", wire.ReadOnly, online, wire.Mouse, nil},
		{"read read-only", wire.ReadOnly, read, wire.Mouse, nil},
		{"keyboard online", wire.ReadOnly, kbOnline, wire.Keyboard, nil},
		{"write edit", wire.Edit, write, wire.Mouse, nil},
		{"write experimental", wire.Experimental, write, wire.Mouse, nil},
		{"write read-only", wire.ReadOnly, write, wire.Mouse, wire.ErrForbidden},
		{"write reset", wire.Reset, write, wire.Mouse, wire.ErrForbidden},
		{"clear edit", wire.Edit, forbidden(9), wire.Mouse, wire.ErrForbidden},
		{"clear reset", wire.Reset, forbidden(9), wire.Mouse, nil},
		{"long range edit", wire.Edit, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, make([]byte, 10)), wire.Mouse, wire.ErrForbidden},
		{"cmd 20 experimental", wire.Experimental, forbidden(20), wire.Mouse, wire.ErrForbidden},
		{"cmd 176 experimental", wire.Experimental, forbidden(176), wire.Mouse, wire.ErrForbidden},
		{"keyboard write edit", wire.Edit, kbWrite, wire.Keyboard, wire.ErrForbidden},
		{"keyboard write experimental", wire.Experimental, kbWrite, wire.Keyboard, wire.ErrForbidden},
		{"unknown policy", wire.Policy(4), online, wire.Mouse, wire.ErrForbidden},
		{"keyboard flag, mouse session", wire.ReadOnly, kbOnline, wire.Mouse, wire.ErrTarget},
		{"keyboard write, mouse session", wire.Edit, kbWrite, wire.Mouse, wire.ErrTarget},
		{"mouse packet, keyboard session", wire.ReadOnly, online, wire.Keyboard, wire.ErrTarget},
		{"invalid session target", wire.ReadOnly, online, 0x40, wire.ErrTarget},
		{"byte 1 set", wire.ReadOnly, with(online, 1, 1), wire.Mouse, wire.ErrFraming},
		{"reserved bit 0x10", wire.ReadOnly, with(online, 4, 0x10), wire.Mouse, wire.ErrFraming},
		{"reserved bit 0x20", wire.ReadOnly, with(online, 4, 0x20), wire.Mouse, wire.ErrFraming},
		{"reserved bit 0x40", wire.ReadOnly, with(online, 4, 0x40), wire.Mouse, wire.ErrFraming},
		{"reserved bits with a length", wire.Edit, with(write, 4, 0x52), wire.Mouse, wire.ErrFraming},
		{"online with data", wire.ReadOnly, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, []byte{9, 9}), wire.Mouse, wire.ErrFraming},
		{"clear with data", wire.Reset, wire.MustBuild(wire.Mouse, wire.CmdClear, 0, []byte{1, 2, 3}), wire.Mouse, wire.ErrFraming},
		{"version with data", wire.ReadOnly, wire.MustBuild(wire.Mouse, wire.CmdFWVersion, 0, []byte{1}), wire.Mouse, wire.ErrFraming},
		{"handshake", wire.ReadOnly, wire.MustBuild(wire.Mouse, wire.CmdHandshake, 0, []byte{1, 2, 3, 4, 0, 0, 0, 0}), wire.Mouse, nil},
		{"short handshake", wire.ReadOnly, wire.MustBuild(wire.Mouse, wire.CmdHandshake, 0, []byte{1, 2, 3, 4}), wire.Mouse, wire.ErrFraming},
		{"long range on", wire.Experimental, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0}), wire.Mouse, nil},
		{"long range off", wire.Experimental, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, make([]byte, 10)), wire.Mouse, nil},
		{"long range length 1", wire.Experimental, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1}), wire.Mouse, wire.ErrFraming},
		{"long range value 2", wire.Experimental, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0}), wire.Mouse, wire.ErrFraming},
		{"long range trailing data", wire.Experimental, wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 7}), wire.Mouse, wire.ErrFraming},
		{"length 11", wire.Edit, with(write, 4, 11), wire.Mouse, wire.ErrFraming},
		{"length 15", wire.ReadOnly, with(read, 4, 15), wire.Mouse, wire.ErrFraming},
		{"write length 0", wire.Edit, with(write, 4, 0), wire.Mouse, wire.ErrFraming},
		{"read length 0", wire.ReadOnly, with(read, 4, 0), wire.Mouse, wire.ErrFraming},
		{"byte past the data", wire.Edit, with(write, 7, 1), wire.Mouse, wire.ErrFraming},
		{"data in a read", wire.ReadOnly, with(read, 5, 1), wire.Mouse, wire.ErrFraming},
		{"address on online", wire.ReadOnly, with(online, 3, 0x60), wire.Mouse, wire.ErrFraming},
		{"bad checksum", wire.Edit, func() wire.Packet { p := write; p[15]--; return p }(), wire.Mouse, wire.ErrChecksum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Check(tt.pk, tt.t)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Check(%v): %v", tt.pk, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Check(%v) = %v, want %v", tt.pk, err, tt.want)
			}
		})
	}
}

// requestShape is the per-command request layout from docs/protocol.md.
func requestShape(pk wire.Packet) bool {
	d := pk.Data()
	switch pk[0] {
	case 3, 4, 9, 14, 18, 23, 29:
		return pk.Len() == 0
	case 1:
		return pk.Len() == 8
	case 22:
		return pk.Len() == 10 && d[0] <= 1 && !slices.ContainsFunc(d[1:], func(b byte) bool { return b != 0 })
	}
	return true
}

var checkErrors = []error{wire.ErrForbidden, wire.ErrTarget, wire.ErrFraming, wire.ErrChecksum}

func FuzzCheck(f *testing.F) {
	path, err := vectors.DefaultPath(".")
	if err != nil {
		f.Fatal(err)
	}
	file, err := vectors.Load(path)
	if err != nil {
		f.Fatal(err)
	}
	for _, v := range file.Vectors {
		b, err := v.Bytes()
		if err != nil || len(b) != wire.Size {
			continue
		}
		for p := range 5 {
			f.Add(b, byte(p), byte(wire.Mouse))
			f.Add(b, byte(p), byte(wire.Keyboard))
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte, pol, tgt byte) {
		var pk wire.Packet
		copy(pk[:], raw)
		p, target := wire.Policy(pol), wire.Target(tgt)
		err := p.Check(pk, target)
		if err != nil {
			if !slices.ContainsFunc(checkErrors, func(e error) bool { return errors.Is(err, e) }) {
				t.Fatalf("Check(%v, %v) under %v: untyped error %v", pk, target, p, err)
			}
			return
		}
		switch {
		case slices.Contains(neverAllowed, pk[0]):
			t.Fatalf("%v passed %v for %v with a never-allowed command", pk, p, target)
		case !tableAllows(p, target, pk[0]):
			t.Fatalf("%v passed %v for %v outside the allowlist", pk, p, target)
		case !pk.Valid():
			t.Fatalf("%v passed with a bad checksum", pk)
		case pk.Target() != target:
			t.Fatalf("%v passed with flag %v for %v", pk, pk.Target(), target)
		case pk[1] != 0 || pk.Len() > wire.MaxData || pk[4]&0x70 != 0:
			t.Fatalf("%v passed with a malformed header", pk)
		case !requestShape(pk):
			t.Fatalf("%v passed with a length or data its command never takes", pk)
		}
		rebuilt, err := wire.Build(pk.Target(), pk.Cmd(), pk.Addr(), pk.Data())
		if err != nil || rebuilt != pk {
			t.Fatalf("%v passed but Build gives %v, %v", pk, rebuilt, err)
		}
	})
}
