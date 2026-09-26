package wire_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/wire"
)

var (
	targets     = []wire.Target{wire.Mouse, wire.Keyboard}
	allPolicies = []wire.Policy{wire.ReadOnly, wire.Edit, wire.Reset, wire.Experimental}
)

func reseal(p wire.Packet) wire.Packet {
	p[wire.Size-1] = p.Checksum()
	return p
}

func TestBuildErrors(t *testing.T) {
	tests := []struct {
		name string
		t    wire.Target
		c    wire.Cmd
		addr uint16
		data []byte
		want error
	}{
		{"target 0x40", 0x40, wire.CmdOnline, 0, nil, wire.ErrTarget},
		{"target 0x81", 0x81, wire.CmdOnline, 0, nil, wire.ErrTarget},
		{"target 0x01", 0x01, wire.CmdRead, 0, make([]byte, 1), wire.ErrTarget},
		{"11 data bytes", wire.Mouse, wire.CmdWrite, 0, make([]byte, 11), wire.ErrFraming},
		{"address on cmd 3", wire.Mouse, wire.CmdOnline, 0x60, nil, wire.ErrFraming},
		{"address on cmd 22", wire.Mouse, wire.CmdSetLongRange, 1, make([]byte, 10), wire.ErrFraming},
		{"empty write", wire.Mouse, wire.CmdWrite, 0x60, nil, wire.ErrFraming},
		{"empty read", wire.Keyboard, wire.CmdRead, 0x60, nil, wire.ErrFraming},
		{"read with data", wire.Mouse, wire.CmdRead, 0x60, []byte{0, 1}, wire.ErrFraming},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := wire.Build(tt.t, tt.c, tt.addr, tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if p != (wire.Packet{}) {
				t.Errorf("packet on error = %v, want zero", p)
			}
		})
	}
}

func TestBuildReadErrors(t *testing.T) {
	for _, n := range []int{-1, 0, 11, 256} {
		if _, err := wire.BuildRead(wire.Mouse, 0, n); !errors.Is(err, wire.ErrFraming) {
			t.Errorf("BuildRead n=%d: err = %v, want ErrFraming", n, err)
		}
	}
	if _, err := wire.BuildRead(0x20, 0, 4); !errors.Is(err, wire.ErrTarget) {
		t.Errorf("BuildRead target 0x20: err = %v, want ErrTarget", err)
	}
}

func TestMustBuildPanics(t *testing.T) {
	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok || !errors.Is(err, wire.ErrFraming) {
			t.Errorf("recovered %v, want an ErrFraming error", r)
		}
	}()
	wire.MustBuild(wire.Mouse, wire.CmdWrite, 0, make([]byte, 11))
}

func randomRequest(r *rand.Rand) (wire.Target, wire.Cmd, uint16, []byte) {
	tg := targets[r.IntN(len(targets))]
	c := wire.Cmd(r.IntN(256))
	var addr uint16
	var data []byte
	switch c {
	case wire.CmdRead:
		addr = uint16(r.Uint32())
		data = make([]byte, 1+r.IntN(wire.MaxData))
	case wire.CmdWrite:
		addr = uint16(r.Uint32())
		data = make([]byte, 1+r.IntN(wire.MaxData))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
	default:
		data = make([]byte, r.IntN(wire.MaxData+1))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
	}
	return tg, c, addr, data
}

func TestBuildProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 50000 {
		tg, c, addr, data := randomRequest(r)
		p, err := wire.Build(tg, c, addr, data)
		if err != nil {
			t.Fatalf("Build(%v, %v, %#x, % x): %v", tg, c, addr, data, err)
		}
		if !p.Valid() {
			t.Fatalf("%v: not valid", p)
		}
		var sum byte = wire.ReportID
		for _, b := range p {
			sum += b
		}
		if sum != 0x55 {
			t.Fatalf("%v: (8 + sum) & 0xff = %#x", p, sum)
		}
		if p.Cmd() != c || p.Target() != tg || p.Addr() != addr || p.Len() != len(data) || !bytes.Equal(p.Data(), data) || p.Status() != wire.StatusOK {
			t.Fatalf("%v does not round-trip (%v, %v, %#x, % x)", p, tg, c, addr, data)
		}
		for _, pol := range allPolicies {
			err := pol.Check(p, tg)
			allowed := pol.Allows(c, tg)
			if (allowed && requestShape(p)) != (err == nil) {
				t.Fatalf("%v under %v: Allows %v, Check %v", p, pol, allowed, err)
			}
			if err != nil && !errors.Is(err, wire.ErrForbidden) && !(allowed && errors.Is(err, wire.ErrFraming)) {
				t.Fatalf("%v under %v: %v, want nil, ErrForbidden or a shape error", p, pol, err)
			}
		}
	}
}

func TestChecksumDetectsAnySingleByteChange(t *testing.T) {
	p := wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x1a0, []byte{4, 0x80, 8, 0, 0x81, 6, 0, 0x41, 6, 0})
	for i := range p {
		for d := 1; d < 256; d++ {
			q := p
			q[i] += byte(d)
			if q.Valid() {
				t.Fatalf("byte %d + %d still valid", i, d)
			}
		}
	}
}

func TestAccessors(t *testing.T) {
	rep := wire.Packet{0x08, 0x01, 0x24, 0xc0, 0x8f, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 0}
	if rep.Cmd() != wire.CmdRead || rep.Status() != wire.StatusNAK || rep.Addr() != 9408 || rep.Target() != wire.Keyboard || rep.Len() != 15 {
		t.Errorf("accessors of %v: cmd %v status %v addr %d target %v len %d", rep, rep.Cmd(), rep.Status(), rep.Addr(), rep.Target(), rep.Len())
	}
	if got := rep.Data(); !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Errorf("Data() = % x, want 10 bytes clamped", got)
	}
	d := rep.Data()
	d[0] = 0xee
	if rep[5] != 1 {
		t.Error("Data() aliases the packet")
	}
}

func TestStatusErr(t *testing.T) {
	if err := wire.StatusOK.Err(); err != nil {
		t.Errorf("OK: %v", err)
	}
	if err := wire.StatusNAK.Err(); !errors.Is(err, wire.ErrNAK) {
		t.Errorf("NAK: %v", err)
	}
	for _, s := range []wire.Status{2, 0x55, 0xff} {
		err := s.Err()
		if !errors.Is(err, wire.ErrStatus) || errors.Is(err, wire.ErrNAK) {
			t.Errorf("status %#x: %v", byte(s), err)
		}
	}
}

func TestStrings(t *testing.T) {
	tests := []struct{ got, want string }{
		{wire.CmdRead.String(), "cmd 8 (read)"},
		{wire.CmdClear.String(), "cmd 9 (clear)"},
		{wire.Cmd(46).String(), "cmd 46"},
		{wire.Mouse.String(), "mouse"},
		{wire.Keyboard.String(), "keyboard"},
		{wire.Target(0x40).String(), "target 0x40"},
		{wire.ReadOnly.String(), "read-only"},
		{wire.Experimental.String(), "experimental"},
		{wire.Policy(9).String(), "policy 9"},
		{wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil).String(), "03 00 00 00 00 00 00 00 00 00 00 00 00 00 00 4a"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
	_, err := wire.Build(wire.Mouse, wire.CmdOnline, 5, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "wire: ") || !strings.Contains(err.Error(), "cmd 3 (online)") {
		t.Errorf("error text %q", err)
	}
}

func TestMatch(t *testing.T) {
	read := wire.MustBuild(wire.Mouse, wire.CmdRead, 0x60, make([]byte, 10))
	kbRead := wire.MustBuild(wire.Keyboard, wire.CmdRead, 9408, make([]byte, 10))
	write := wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x1a0, []byte{4, 0x80, 8, 0, 0x81, 6, 0, 0x41, 6, 0})
	online := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	with := func(p wire.Packet, i int, v byte) wire.Packet {
		p[i] = v
		return p
	}
	tests := []struct {
		name     string
		req, rep wire.Packet
		want     bool
	}{
		{"read echo", read, read, true},
		{"read reply with data", read, wire.Packet{8, 0, 0, 0x60, 0x0a, 1, 1, 0, 0x53, 2, 2, 0, 0x51, 3, 1}, true},
		{"read reply NAK", read, with(read, 1, 1), true},
		{"read reply other addrLo", read, with(read, 3, 0x6a), false},
		{"read reply other addrHi", read, with(read, 2, 0x01), false},
		{"read reply other length", read, with(read, 4, 0x04), false},
		{"read reply high length bits ignored", read, with(read, 4, 0x8a), true},
		{"keyboard read reply without flag", kbRead, with(kbRead, 4, 0x0a), true},
		{"write ack", write, with(with(write, 5, 0), 15, 0), true},
		{"write ack other address", write, with(write, 3, 0xaa), false},
		{"write vs read reply", write, with(write, 0, 8), false},
		{"online reply", online, wire.Packet{3, 0, 0, 0, 4, 0, 0x33, 0x22, 0x11, 0, 0, 0, 0, 0, 0, 0xe0}, true},
		{"online vs status push", online, wire.Packet{10, 0, 0, 0, 2, 1}, false},
		{"online vs battery", online, wire.Packet{4, 0, 0, 0, 5, 80}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wire.Match(tt.req, tt.rep); got != tt.want {
				t.Errorf("Match(%v, %v) = %v, want %v", tt.req, tt.rep, got, tt.want)
			}
		})
	}
}

func TestMatchProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 200000 {
		var req, rep wire.Packet
		for i := range req {
			req[i] = byte(r.Uint32())
		}
		if r.IntN(2) == 0 {
			req[0] = byte([]wire.Cmd{wire.CmdRead, wire.CmdWrite}[r.IntN(2)])
		}
		rep = req
		for range r.IntN(4) {
			rep[r.IntN(wire.Size)] = byte(r.Uint32())
		}
		if !wire.Match(req, req) {
			t.Fatalf("%v does not match itself", req)
		}
		got := wire.Match(req, rep)
		if rep[0] != req[0] && got {
			t.Fatalf("accepted a different cmd: %v for %v", rep, req)
		}
		addressed := req[0] == byte(wire.CmdRead) || req[0] == byte(wire.CmdWrite)
		if addressed && (rep[2] != req[2] || rep[3] != req[3]) && got {
			t.Fatalf("accepted a different address: %v for %v", rep, req)
		}
		if addressed && rep[4]&0x0f != req[4]&0x0f && got {
			t.Fatalf("accepted a different length: %v for %v", rep, req)
		}
		want := rep[0] == req[0] && (!addressed || rep[2] == req[2] && rep[3] == req[3] && rep[4]&0x0f == req[4]&0x0f)
		if got != want {
			t.Fatalf("Match(%v, %v) = %v, want %v", req, rep, got, want)
		}
	}
}
