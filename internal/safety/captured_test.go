package safety_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// capturedSeed leaves the pair at 185 valid, as a factory reset might, so a
// restore writes the invalid pair of the backup back over it.
func capturedSeed(t testing.TB, im *flash.Image) {
	if err := im.Set(185, []byte{0x00, 0x55}); err != nil {
		t.Fatal(err)
	}
}

var unknownBlock = []byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B}

// capturedChanges write a pair of the current stage, then captured bytes
// over an unmapped pair, the 12-byte unmapped block (two chunks) and the
// pair at 185, left invalid.
func capturedChanges(t testing.TB) []plan.Change {
	c := func(addr int, b []byte) plan.Change {
		return plan.Change{Addr: addr, New: b, Tier: catalog.Experimental, Captured: true, Desc: "captured"}
	}
	return slices.Concat(pairChange(t), []plan.Change{c(185, []byte{0x03, 0x53}), c(84, unknownBlock), c(6, []byte{0x01, 0x54})})
}

func phases(ops []safety.OpRecord) []plan.Phase {
	var out []plan.Phase
	for _, o := range ops {
		out = append(out, o.Phase)
	}
	return out
}

func TestCapturedApplyVerifies(t *testing.T) {
	f := newFixture(t, setup{seed: capturedSeed})
	p := f.plan(capturedChanges(t))
	res, err := f.apply(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified != 4 {
		t.Fatalf("%d of %d ops verified", res.Verified, res.Ops)
	}
	if d := differ(f.image(), after(t, f.start, p), p); len(d) > 0 {
		t.Fatalf("extents %v are not the plan's", d)
	}
	f.checkOutside(p)
	st := f.status()
	r := st.Find(res.Run)
	want := []plan.Phase{plan.Record, plan.Captured, plan.Captured, plan.Captured}
	if !st.Clean() || st.Last != r || !r.Complete || !slices.Equal(phases(r.Ops), want) {
		t.Fatalf("run %+v, phases %v", r, phases(r.Ops))
	}
	if r.Ops[2].Extent != (flash.Extent{Addr: 84, Len: 12}) {
		t.Fatalf("op 3 is %v", r.Ops[2].Extent)
	}
}

// A crash between the two chunks of the unmapped block leaves it torn:
// recovery reads it again and settles it either way with captured bytes.
func TestCapturedKilledMidExtent(t *testing.T) {
	for _, how := range []safety.Strategy{safety.Forward, safety.Back} {
		t.Run(how.String(), func(t *testing.T) {
			f := newFixture(t, setup{seed: capturedSeed})
			p := f.plan(capturedChanges(t))
			f.applyKilled(p, 3)
			st := f.status()
			if len(st.Open) != 1 {
				t.Fatalf("open runs %+v", st.Open)
			}
			r := st.Open[0]
			in := must[*safety.Inspection](t)(f.x.Inspect(context.Background(), r, f.device()))
			want := []safety.Class{safety.ClassNew, safety.ClassNew, safety.ClassTorn, safety.ClassOld}
			if got := classes(in); !slices.Equal(got, want) {
				t.Fatalf("extent classes %v, want %v", got, want)
			}
			if _, err := f.x.Recover(context.Background(), r, safety.Leave, f.device(), nil); !errors.Is(err, safety.ErrTorn) {
				t.Fatalf("Leave over a torn block = %v, want ErrTorn", err)
			}
			rp := must[plan.Plan](t)(safety.RecoveryPlan(in, how, f.device()))
			for _, op := range rp.Ops {
				if op.Extent.Addr != mouse.AddrCurrentDPI && op.Phase != plan.Captured {
					t.Errorf("recovery op %d writes %v as %v", op.Seq, op.Extent, op.Phase)
				}
			}
			res, err := f.x.Recover(context.Background(), r, how, f.device(), nil)
			if err != nil {
				t.Fatal(err)
			}
			target := after(t, f.start, p)
			if how == safety.Back {
				target = f.start
			}
			if d := differ(f.image(), target, p); len(d) > 0 {
				t.Fatalf("extents %v were not settled %s", d, how)
			}
			f.checkOutside(p)
			st = f.status()
			if rec := st.Find(res.Run); !st.Clean() || rec == nil || !rec.Complete || st.Find(r.ID).Resolved != how {
				t.Fatalf("after recovery: open %v, run %+v", st.Open, rec)
			}
		})
	}
}

// A revert writes the bytes from before the restore back as captured bytes
// too, the valid pair at 185 included.
func TestCapturedRevert(t *testing.T) {
	f := newFixture(t, setup{seed: capturedSeed})
	p := f.plan(capturedChanges(t))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	res, err := f.revertLast()
	if err != nil {
		t.Fatal(err)
	}
	if d := differ(f.image(), f.start, p); len(d) > 0 {
		t.Fatalf("revert left extents %v", d)
	}
	f.checkOutside(p)
	rev := f.status().Find(res.Run)
	if got := phases(rev.Ops); !slices.Equal(got, []plan.Phase{plan.Record, plan.Captured, plan.Captured, plan.Captured}) {
		t.Fatalf("revert phases %v", got)
	}
	if b, _ := f.image().Get(flash.Extent{Addr: 185, Len: 2}); b[0] != 0x00 || b[1] != 0x55 {
		t.Fatalf("185 holds % x after the revert", b)
	}
}

// Captured bytes are experimental: they need --experimental, and a phrase
// that names every extent they cover.
func TestCapturedGates(t *testing.T) {
	f := newFixture(t, setup{seed: capturedSeed})
	p := f.plan(capturedChanges(t))
	want := "write experimental and captured 6+2 84+12 185+2"
	if got := safety.ConfirmPhrase(p.Ops); got != want {
		t.Fatalf("ConfirmPhrase = %q, want %q", got, want)
	}
	facts := goodFacts(f.image())
	for _, tt := range []struct {
		name string
		g    safety.Gates
		want error
	}{
		{"every gate", safety.Gates{AllowUntested: true, Experimental: true, Confirm: want}, nil},
		{"no --experimental", safety.Gates{AllowUntested: true, Confirm: want}, safety.ErrExperimental},
		{"the tier's phrase only", safety.Gates{AllowUntested: true, Experimental: true, Confirm: "write experimental"}, safety.ErrConfirm},
		{"an extent left out", safety.Gates{AllowUntested: true, Experimental: true, Confirm: "write experimental and captured 6+2 84+12"}, safety.ErrConfirm},
	} {
		err := safety.Preflight(safety.KindApply, p, facts, tt.g)
		if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
}
