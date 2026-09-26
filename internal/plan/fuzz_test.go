package plan_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/plan"
)

var sentinels = []error{
	plan.ErrMalformed, plan.ErrDevice, plan.ErrExtent, plan.ErrPhase, plan.ErrOrder, plan.ErrOverlap, plan.ErrTier,
	plan.ErrFrozen, plan.ErrChecksum, plan.ErrUnread, plan.ErrStale, plan.ErrBinding, plan.ErrLeftClick,
}

func checkSentinel(t *testing.T, err error) {
	t.Helper()
	if !slices.ContainsFunc(sentinels, func(s error) bool { return errors.Is(err, s) }) {
		t.Fatalf("error %v wraps no sentinel", err)
	}
}

func FuzzNew(f *testing.F) {
	f.Add(uint64(1), []byte{})
	f.Add(uint64(2), []byte{0x01, 0x60, 0x04, 0x01, 0x01, 0x00, 0x53})
	f.Add(uint64(3), []byte{0x01, 0x60, 0x08, 0x02, 0x82, 0xcd, 0x00, 0x42, 0xcd, 0x00, 0xf5})
	f.Add(uint64(4), []byte{0x00, 0x08, 0x02, 0x01, 0x54, 0x1b, 0x00, 0x03, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, seed uint64, raw []byte) {
		im, changes := gen{rand.New(rand.NewPCG(seed, 3))}.scenario()
		for len(raw) >= 3 && len(changes) < 64 {
			addr, n := (int(raw[0])<<8|int(raw[1]))%(knownEnd+16), int(raw[2])%40
			tier := catalog.Tier(raw[2] >> 5 % 5)
			raw = raw[3:]
			nb := make([]byte, n)
			copy(nb, raw)
			raw = raw[min(n, len(raw)):]
			changes = append(changes, plan.Change{Addr: addr, New: nb, Tier: tier})
		}
		l := em11Layout()
		p, err := plan.New(em11, nil, im, l, changes)
		if err != nil {
			checkSentinel(t, err)
			return
		}
		if err := p.Validate(im, l); err != nil {
			t.Fatalf("New returned a plan that fails Validate: %v", err)
		}
		want := im.Clone()
		for _, c := range changes {
			set(t, want, c.Addr, c.New)
		}
		if got := apply(t, im, p); !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatal("applying the plan does not reach the requested image")
		}
		checkInvariants(t, im, p)
	})
}

func FuzzValidate(f *testing.F) {
	f.Add(uint64(1), []byte{})
	f.Add(uint64(5), []byte{0, 1, 3})
	f.Add(uint64(6), []byte{1, 5, 2, 0, 6, 0})
	f.Add(uint64(7), []byte{2, 3, 7, 1, 4, 9, 0, 8, 0})
	f.Fuzz(func(t *testing.T, seed uint64, edits []byte) {
		im, changes := gen{rand.New(rand.NewPCG(seed, 5))}.scenario()
		l := em11Layout()
		p, err := plan.New(em11, nil, im, l, changes)
		if err != nil || len(p.Ops) == 0 {
			return
		}
		ops := slices.Clone(p.Ops)
		for ; len(edits) >= 3 && len(ops) > 0; edits = edits[3:] {
			i, v := int(edits[0])%len(ops), edits[2]
			o := &ops[i]
			switch edits[1] % 9 {
			case 0:
				o.Seq = int(v)
			case 1:
				o.Phase = plan.Phase(v % 6)
			case 2:
				o.Tier = catalog.Tier(v % 5)
			case 3:
				o.Old = slices.Clone(o.Old)
				o.Old[int(v)%len(o.Old)] ^= v | 1
			case 4:
				o.New = slices.Clone(o.New)
				o.New[int(v)%len(o.New)] ^= v | 1
			case 5:
				j := int(v) % len(ops)
				ops[i], ops[j] = ops[j], ops[i]
			case 6:
				ops = slices.Delete(ops, i, i+1)
			case 7:
				ops = slices.Insert(ops, i+1, *o)
			case 8:
				ops = numbered(ops...)
			}
		}
		q := plan.Plan{Device: p.Device, Ops: ops}
		if err := q.Validate(im, l); err != nil {
			checkSentinel(t, err)
			return
		}
		checkInvariants(t, im, q)
		after := im.Clone()
		for _, o := range q.Ops {
			if got := get(t, after, o.Extent.Addr, o.Extent.Len); !bytes.Equal(got, o.Old) {
				t.Fatalf("op %d accepted with stale old bytes", o.Seq)
			}
			set(t, after, o.Extent.Addr, o.New)
		}
	})
}
