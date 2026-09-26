package plan_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

var recordPool = func() []flash.Extent {
	var out []flash.Extent
	for _, a := range []int{0, 2, 4, 10, 169, 171, 173, 175, 177, 179, 183} {
		out = append(out, flash.Extent{Addr: a, Len: 2})
	}
	for i := range 16 {
		out = append(out, flash.Extent{Addr: 12 + 4*i, Len: 4})
	}
	return out
}()

type gen struct{ r *rand.Rand }

func (g gen) bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.r.IntN(256))
	}
	return b
}

func (g gen) record(n int) []byte { return cks(g.bytes(n - 1)...) }

func (g gen) binding(k int, garbage bool) []byte {
	switch n := g.r.IntN(20); {
	case n < 5:
		return slices.Clone(leftClick)
	case n < 7:
		return slices.Clone(right)
	case n < 9:
		return slices.Clone(disable)
	case n < 10:
		return h("02 01 00 52")
	case n < 14:
		return slices.Clone(shortcut)
	case n < 17:
		return macroBinding(byte(k), byte(1+g.r.IntN(250)))
	case n < 18:
		return macroBinding(byte(g.r.IntN(20)), 1)
	case n < 19 || !garbage:
		return cks(byte(g.r.IntN(12)), byte(g.r.IntN(256)), byte(g.r.IntN(256)))
	}
	b := g.bytes(4)
	if g.r.IntN(2) == 0 {
		b[0] = byte(5 + g.r.IntN(2))
	}
	if b[0]+b[1]+b[2]+b[3] == 0x55 {
		b[3]++
	}
	return b
}

func (g gen) body(macro bool) []byte {
	if !macro {
		n := 2 * (1 + g.r.IntN(5))
		return cks(append([]byte{byte(n)}, g.bytes(3*n)...)...)
	}
	name := g.bytes(1 + g.r.IntN(30))
	head := append([]byte{byte(len(name))}, name...)
	for len(head) < 31 {
		head = append(head, 0xFF)
	}
	count := 1 + g.r.IntN(6)
	if g.r.IntN(10) == 0 {
		count = 1 + g.r.IntN(70)
	}
	tail := cks(append([]byte{byte(count)}, g.bytes(5*count)...)...)
	return append(head, tail...)
}

func (g gen) slotContent(macro bool, size int, garbage bool) []byte {
	out := fill(0xFF, size)
	switch n := g.r.IntN(10); {
	case n < 6:
		copy(out, g.body(macro))
	case n < 7:
		out = fill(0, size)
	case n < 8 && garbage:
		out = g.bytes(size)
	}
	return out
}

func (g gen) newBody(macro bool, size int) []byte {
	if g.r.IntN(6) == 0 {
		return fill([]byte{0, 0xFF}[g.r.IntN(2)], 1+g.r.IntN(size))
	}
	return g.body(macro)
}

func (g gen) tier() catalog.Tier {
	return []catalog.Tier{catalog.Experimental, catalog.Untested, catalog.Verified}[g.r.IntN(3)]
}

func (g gen) scenario() (*flash.Image, []plan.Change) {
	b := g.bytes(knownEnd)
	for _, e := range recordPool {
		copy(b[e.Addr:], g.record(e.Len))
	}
	for k := range slots {
		copy(b[bindAddr(k):], g.binding(k, true))
		copy(b[shortcutAddr(k):], g.slotContent(false, shortcutSize, true))
		copy(b[macroAddr(k):], g.slotContent(true, macroSize, true))
	}
	if g.r.IntN(4) != 0 {
		copy(b[bindAddr(0):], leftClick)
	}
	im, _ := flash.FromDump(0, b)

	var changes []plan.Change
	add := func(addr int, nb []byte) {
		changes = append(changes, plan.Change{Addr: addr, New: nb, Desc: strconv.Itoa(len(changes)), Tier: g.tier()})
	}
	rate := 0.05 + 0.3*g.r.Float64()
	for _, e := range recordPool {
		if g.r.Float64() < rate {
			add(e.Addr, g.record(e.Len))
		}
	}
	for k := range slots {
		if g.r.Float64() < rate {
			add(bindAddr(k), g.binding(k, false))
		}
		if g.r.Float64() < rate {
			add(shortcutAddr(k), g.newBody(false, shortcutSize))
		}
		if g.r.Float64() < rate {
			add(macroAddr(k), g.newBody(true, macroSize))
		}
	}
	for i := range changes {
		if g.r.IntN(12) == 0 {
			changes[i].New, _ = im.Get(flash.Extent{Addr: changes[i].Addr, Len: len(changes[i].New)})
		}
	}
	g.r.Shuffle(len(changes), func(i, j int) { changes[i], changes[j] = changes[j], changes[i] })
	return im, changes
}

type target struct {
	macro bool
	slot  int
}

func oracleTargets(k int, b []byte) []target {
	switch b[0] {
	case 5:
		return []target{{false, k}}
	case 6:
		return []target{{true, k}, {true, int(b[1])}}
	}
	return nil
}

func oracleValid(im *flash.Image, t target) bool {
	base, head, event, size := shortcutAddr(t.slot), 0, 3, shortcutSize
	if t.macro {
		base, head, event, size = macroAddr(t.slot), 31, 5, macroSize
	}
	if t.slot >= slots {
		return false
	}
	n, _ := im.Byte(base + head)
	d := head + 2 + event*int(n)
	if n == 0 || d > size {
		return false
	}
	b, _ := im.Get(flash.Extent{Addr: base, Len: d})
	var s byte
	for _, x := range b[head:] {
		s += x
	}
	return s == 0x55
}

func oracleLefts(im *flash.Image) int {
	n := 0
	for _, s := range em11Layout().Buttons {
		if b, _ := im.Get(flash.Extent{Addr: bindAddr(s), Len: 4}); bytes.Equal(b, leftClick) {
			n++
		}
	}
	return n
}

func binding(im *flash.Image, k int) []byte {
	b, _ := im.Get(flash.Extent{Addr: bindAddr(k), Len: 4})
	return b
}

func expected(im *flash.Image, changes []plan.Change) (*flash.Image, error) {
	final := im.Clone()
	rewritten := map[target]bool{}
	changed := map[int]bool{}
	for _, c := range changes {
		old, _ := im.Get(flash.Extent{Addr: c.Addr, Len: len(c.New)})
		_ = final.Set(c.Addr, c.New)
		if bytes.Equal(old, c.New) {
			continue
		}
		switch {
		case c.Addr >= macroBase:
			rewritten[target{true, (c.Addr - macroBase) / macroSize}] = true
		case c.Addr >= shortcutBase:
			rewritten[target{false, (c.Addr - shortcutBase) / shortcutSize}] = true
		case c.Addr >= bindBase && c.Addr < bindAddr(slots):
			changed[(c.Addr-bindBase)/4] = true
		}
	}
	hits := func(ts []target) bool {
		return slices.ContainsFunc(ts, func(t target) bool { return rewritten[t] })
	}
	for k := range slots {
		b := binding(im, k)
		if !changed[k] && hits(oracleTargets(k, b)) && b[0]+b[1]+b[2]+b[3] != 0x55 {
			return final, plan.ErrChecksum
		}
	}
	if oracleLefts(im) > 0 && oracleLefts(final) == 0 {
		return final, plan.ErrLeftClick
	}
	for k := range slots {
		if !changed[k] && !hits(oracleTargets(k, binding(im, k))) {
			continue
		}
		for _, t := range oracleTargets(k, binding(final, k)) {
			if !oracleValid(final, t) {
				return final, plan.ErrBinding
			}
		}
	}
	return final, nil
}

func checkInvariants(t *testing.T, im *flash.Image, p plan.Plan) {
	t.Helper()
	sim := im.Clone()
	lefts := oracleLefts(sim)
	for i, o := range p.Ops {
		if o.Seq != i+1 || (i > 0 && o.Phase < p.Ops[i-1].Phase) {
			t.Fatalf("op %d: seq %d phase %v after %v", i, o.Seq, o.Phase, p.Ops[max(i-1, 0)].Phase)
		}
		if o.Phase == plan.Body {
			body := target{o.Extent.Addr >= macroBase, (o.Extent.Addr - shortcutBase) / shortcutSize}
			if body.macro {
				body.slot = (o.Extent.Addr - macroBase) / macroSize
			}
			for k := range slots {
				if slices.Contains(oracleTargets(k, binding(sim, k)), body) {
					t.Fatalf("op %d rewrites %+v while binding %d points at it", o.Seq, body, k)
				}
			}
		}
		_ = sim.Set(o.Extent.Addr, o.New)
		if lefts > 0 && oracleLefts(sim) == 0 {
			t.Fatalf("op %d leaves no Left Click", o.Seq)
		}
	}
}

func keepRecordOrder(r *rand.Rand, changes []plan.Change) []plan.Change {
	out := slices.Clone(changes)
	var free []int
	var records []plan.Change
	for i, c := range out {
		if c.Addr < bindBase || (c.Addr >= bindAddr(slots) && c.Addr < shortcutBase) {
			records = append(records, c)
		} else {
			free = append(free, i)
		}
	}
	r.Shuffle(len(free), func(i, j int) { out[free[i]], out[free[j]] = out[free[j]], out[free[i]] })
	for i, j := 0, 0; i < len(out); i++ {
		if !slices.Contains(free, i) {
			out[i] = records[j]
			j++
		}
	}
	return out
}

func TestNewProperties(t *testing.T) {
	l := em11Layout()
	outcomes := map[error]int{}
	var twoPhase int
	for seed := range uint64(4000) {
		r := rand.New(rand.NewPCG(seed, 7))
		im, changes := gen{r}.scenario()
		final, want := expected(im, changes)
		p, err := plan.New(em11, nil, im, l, changes)
		outcomes[want]++
		if !errors.Is(err, want) {
			t.Fatalf("seed %d: New = %v, want %v", seed, err, want)
		}
		if err != nil {
			continue
		}
		if err := p.Validate(im, l); err != nil {
			t.Fatalf("seed %d: Validate: %v", seed, err)
		}
		if got := apply(t, im, p); !bytes.Equal(got.Bytes(), final.Bytes()) {
			t.Fatalf("seed %d: applying the plan does not reach the requested image", seed)
		}
		checkInvariants(t, im, p)
		if slices.ContainsFunc(p.Ops, func(o plan.Op) bool { return o.Phase == plan.Neutralise }) {
			twoPhase++
		}
		q, err := plan.New(em11, nil, im, l, keepRecordOrder(r, changes))
		if err != nil || !reflect.DeepEqual(p, q) {
			t.Fatalf("seed %d: plan depends on the order of body and binding changes (%v)", seed, err)
		}
	}
	for _, e := range []error{nil, plan.ErrChecksum, plan.ErrLeftClick, plan.ErrBinding} {
		if outcomes[e] < 40 {
			t.Errorf("only %d scenarios expect %v", outcomes[e], e)
		}
	}
	if twoPhase < 100 {
		t.Errorf("only %d plans use two-phase rebinding", twoPhase)
	}
	t.Logf("outcomes %v, two-phase %d", outcomes, twoPhase)
}

func TestValidateRejectsMutations(t *testing.T) {
	l := em11Layout()
	type mutation struct {
		name  string
		apply func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool)
		want  []error
	}
	pick := func(r *rand.Rand, ops []plan.Op, keep func(plan.Op) bool) int {
		var idx []int
		for i, o := range ops {
			if keep(o) {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			return -1
		}
		return idx[r.IntN(len(idx))]
	}
	all := func(plan.Op) bool { return true }
	mutations := []mutation{
		{"old", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			i := pick(r, ops, all)
			ops[i].Old = slices.Clone(ops[i].Old)
			ops[i].Old[r.IntN(len(ops[i].Old))] ^= byte(1 + r.IntN(255))
			return ops, true
		}, []error{plan.ErrStale, plan.ErrMalformed}},
		{"new", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			i := pick(r, ops, func(o plan.Op) bool { return o.Phase == plan.Bind || o.Phase == plan.Record })
			if i < 0 {
				return nil, false
			}
			ops[i].New = slices.Clone(ops[i].New)
			ops[i].New[r.IntN(len(ops[i].New))] ^= byte(1 + r.IntN(255))
			return ops, true
		}, []error{plan.ErrChecksum, plan.ErrMalformed}},
		{"seq", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			ops[r.IntN(len(ops))].Seq += 1 + r.IntN(3)
			return ops, true
		}, []error{plan.ErrMalformed}},
		{"tier", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			ops[r.IntN(len(ops))].Tier = catalog.Tier(r.IntN(2))
			return ops, true
		}, []error{plan.ErrTier}},
		{"drop neutralise", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			i := pick(r, ops, func(o plan.Op) bool { return o.Phase == plan.Neutralise })
			if i < 0 {
				return nil, false
			}
			return numbered(slices.Delete(ops, i, i+1)...), true
		}, []error{plan.ErrBinding}},
		{"swap phases", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			var idx []int
			for i := 1; i < len(ops); i++ {
				if ops[i].Phase != ops[i-1].Phase {
					idx = append(idx, i)
				}
			}
			if len(idx) == 0 {
				return nil, false
			}
			i := idx[r.IntN(len(idx))]
			ops[i], ops[i-1] = ops[i-1], ops[i]
			return numbered(ops...), true
		}, []error{plan.ErrOrder}},
		{"duplicate", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			i := r.IntN(len(ops))
			return numbered(slices.Insert(ops, i+1, ops[i])...), true
		}, []error{plan.ErrOverlap}},
		{"reverse", func(r *rand.Rand, ops []plan.Op) ([]plan.Op, bool) {
			if ops[0].Phase == ops[len(ops)-1].Phase {
				return nil, false
			}
			slices.Reverse(ops)
			return numbered(ops...), true
		}, []error{plan.ErrOrder}},
	}
	applied := map[string]int{}
	for seed := range uint64(3000) {
		r := rand.New(rand.NewPCG(seed, 11))
		im, changes := gen{r}.scenario()
		p, err := plan.New(em11, nil, im, l, changes)
		if err != nil || len(p.Ops) == 0 {
			continue
		}
		m := mutations[r.IntN(len(mutations))]
		ops, ok := m.apply(r, slices.Clone(p.Ops))
		if !ok {
			continue
		}
		applied[m.name]++
		err = plan.Plan{Device: p.Device, Ops: ops}.Validate(im, l)
		if !slices.ContainsFunc(m.want, func(e error) bool { return errors.Is(err, e) }) {
			t.Fatalf("seed %d, mutation %s: Validate = %v, want one of %v", seed, m.name, err, m.want)
		}
	}
	for _, m := range mutations {
		if applied[m.name] < 30 {
			t.Errorf("mutation %s applied only %d times", m.name, applied[m.name])
		}
	}
}
