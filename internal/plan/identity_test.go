package plan_test

import (
	"math/rand/v2"
	"testing"

	"github.com/positronico/arcctl/internal/plan"
)

func TestIdentityKey(t *testing.T) {
	id := plan.Identity{CID: 0x7B, MID: 4, Addr: [3]byte{0x11, 0x22, 0x33}, VID: 0x260D, PID: 0x1282}
	cases := []struct {
		name string
		id   plan.Identity
		want string
	}{
		{"untrusted address", id, "260d-1282-7b04"},
		{"trusted address", func() plan.Identity { id := id; id.AddrTrusted = true; return id }(), "7b04-112233"},
		{"zero", plan.Identity{}, "0000-0000-0000"},
		{"zero trusted", plan.Identity{AddrTrusted: true}, "0000-000000"},
		{"keyboard", plan.Identity{CID: 3, MID: 1, VID: 0x062A, PID: 0xFA0A}, "062a-fa0a-0301"},
	}
	for _, c := range cases {
		if got := c.id.Key(); got != c.want {
			t.Errorf("%s: Key() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIdentityKeyFields(t *testing.T) {
	type tuple struct {
		trusted  bool
		cid, mid byte
		addr     [3]byte
		vid, pid uint16
	}
	relevant := func(id plan.Identity) tuple {
		if id.AddrTrusted {
			return tuple{trusted: true, cid: id.CID, mid: id.MID, addr: id.Addr}
		}
		return tuple{cid: id.CID, mid: id.MID, vid: id.VID, pid: id.PID}
	}
	r := rand.New(rand.NewPCG(1, 2))
	small := func() byte { return byte(r.IntN(3)) }
	random := func() plan.Identity {
		return plan.Identity{
			CID: small(), MID: small(), Addr: [3]byte{small(), small(), small()},
			AddrTrusted: r.IntN(2) == 0, VID: uint16(small()) << 8, PID: uint16(small()),
		}
	}
	for range 20000 {
		a, b := random(), random()
		if same := relevant(a) == relevant(b); same != (a.Key() == b.Key()) {
			t.Fatalf("%+v and %+v: keys %q and %q, same relevant fields %v", a, b, a.Key(), b.Key(), same)
		}
	}
}
