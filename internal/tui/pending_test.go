package tui

import (
	"errors"
	"testing"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
)

func TestPending(t *testing.T) {
	p := NewPending()
	rev := p.Rev()
	p.Stage(Staged{Key: SlotKey(3), Desc: "a", Edit: mouse.SetMedia{Slot: 3, Usage: 0xCD}})
	p.Stage(Staged{Key: DPIKey(1), Desc: "b", Edit: mouse.SetDPI{Stage: 1, DPI: 1600}})
	p.Stage(Staged{Key: SlotKey(3), Desc: "c", Edit: mouse.SetMedia{Slot: 3, Usage: 0xE2}})
	if p.Len() != 2 || p.Rev() == rev {
		t.Fatalf("len %d, rev %d", p.Len(), p.Rev())
	}
	if l := p.List(); l[0].Desc != "c" || l[1].Desc != "b" {
		t.Errorf("a replaced edit moved or kept its old value: %+v", l)
	}
	if e, ok := p.Get(SlotKey(3)); !ok || e.Edit != (mouse.SetMedia{Slot: 3, Usage: 0xE2}) {
		t.Errorf("Get: %+v %v", e, ok)
	}
	if !p.Drop(SlotKey(3)) || p.Drop(SlotKey(3)) || p.Len() != 1 {
		t.Errorf("Drop: len %d", p.Len())
	}
	if edits := p.Edits(); len(edits) != 1 || edits[0] != (mouse.SetDPI{Stage: 1, DPI: 1600}) {
		t.Errorf("Edits: %v", edits)
	}
	rev = p.Rev()
	p.Clear()
	p.Clear()
	if p.Len() != 0 || p.Rev() != rev+1 {
		t.Errorf("Clear: len %d, rev %d after %d", p.Len(), p.Rev(), rev)
	}
}

func TestPendingPlan(t *testing.T) {
	sn := ready(t)
	p := NewPending()
	p.Stage(Staged{Key: DPIKey(1), Edit: mouse.SetDPI{Stage: 1, DPI: 1600}})
	opt := mouse.Options{Device: sn.Identity}
	pl, err := p.Plan(sn.Model, sn.Image, opt)
	if err != nil || len(pl.Ops) != 1 || pl.Device != sn.Identity {
		t.Fatalf("plan %+v, %v", pl, err)
	}
	p.Stage(Staged{Key: DPIKey(1), Edit: mouse.SetDPI{Stage: 1, DPI: 1650}})
	if _, err := p.Plan(sn.Model, sn.Image, opt); err == nil {
		t.Error("an illegal DPI planned")
	}
	if _, err := p.Plan(sn.Model, sn.Image, mouse.Options{}); !errors.Is(err, plan.ErrDevice) {
		t.Errorf("a plan without an identity: %v", err)
	}
}
