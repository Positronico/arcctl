package tui

import (
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
)

// Staged is one edit waiting for Review & Apply. Key names what it changes:
// staging another edit with the same key replaces it.
type Staged struct {
	Key  string
	Desc string
	Edit mouse.Edit
}

// Keys of the staged edits the tabs share, so that one tab's edit replaces
// another's for the same field.
func SlotKey(slot int) string    { return "slot " + strconv.Itoa(slot) }
func DPIKey(stage int) string    { return "dpi " + strconv.Itoa(stage) }
func SettingKey(addr int) string { return "setting @" + strconv.Itoa(addr) }

const (
	StagesKey  = "dpi stages"
	CurrentKey = "dpi current"
)

// Pending holds the staged edits (D2) of every tab, in the order they were
// first staged. Only the Update goroutine touches it.
type Pending struct {
	edits []Staged
	rev   int
}

func NewPending() *Pending { return &Pending{} }

// Stage adds e, or replaces the edit staged under e.Key in place.
func (p *Pending) Stage(e Staged) {
	p.rev++
	if i := p.index(e.Key); i >= 0 {
		p.edits[i] = e
		return
	}
	p.edits = append(p.edits, e)
}

// Drop removes the edit staged under key and reports whether there was one.
func (p *Pending) Drop(key string) bool {
	i := p.index(key)
	if i < 0 {
		return false
	}
	p.edits = slices.Delete(p.edits, i, i+1)
	p.rev++
	return true
}

func (p *Pending) Clear() {
	if len(p.edits) > 0 {
		p.edits = nil
		p.rev++
	}
}

func (p *Pending) Len() int { return len(p.edits) }

// Rev changes whenever the staged edits change.
func (p *Pending) Rev() int { return p.rev }

func (p *Pending) Get(key string) (Staged, bool) {
	if i := p.index(key); i >= 0 {
		return p.edits[i], true
	}
	return Staged{}, false
}

func (p *Pending) List() []Staged { return slices.Clone(p.edits) }

func (p *Pending) Edits() []mouse.Edit {
	out := make([]mouse.Edit, len(p.edits))
	for i, e := range p.edits {
		out[i] = e.Edit
	}
	return out
}

// Plan turns the staged edits into a validated plan against im.
func (p *Pending) Plan(m *catalog.Model, im *flash.Image, opt mouse.Options) (plan.Plan, error) {
	return mouse.PlanEdits(m, im, p.Edits(), opt)
}

func (p *Pending) index(key string) int {
	return slices.IndexFunc(p.edits, func(e Staged) bool { return e.Key == key })
}
