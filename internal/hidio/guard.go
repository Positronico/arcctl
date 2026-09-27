package hidio

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// enabled lists the policies this build lets a session switch to. Reset stays
// off until H7 (D4), and Experimental, which adds cmd 22, until long range has
// an H8 measurement (D5).
var enabled = []wire.Policy{wire.ReadOnly, wire.Edit}

// Change is one entry of a Guard's log: the policy and target after the call,
// or the refusal.
type Change struct {
	At     time.Time
	Policy wire.Policy
	Target wire.Target
	Reason string
	Err    error
}

// Guard holds the policy and target a session writes under. Only the session
// calls Set and SetTarget, and every call is logged. The zero value is
// read-only for the mouse.
type Guard struct {
	mu      sync.Mutex
	policy  wire.Policy
	target  wire.Target
	changes []Change
}

func NewGuard(t wire.Target) *Guard {
	return &Guard{target: t}
}

// Set switches the policy. A policy this build does not enable is refused and
// leaves the current one in place.
func (g *Guard) Set(p wire.Policy, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var err error
	if !slices.Contains(enabled, p) {
		err = fmt.Errorf("%w: policy %s is not enabled in this build", ErrForbidden, p)
	} else {
		g.policy = p
	}
	g.log(reason, err)
	return err
}

// SetTarget switches the device flag packets must carry, for example to retry
// a keyboard probe without the 0x80 flag.
func (g *Guard) SetTarget(t wire.Target, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var err error
	if t != wire.Mouse && t != wire.Keyboard {
		err = fmt.Errorf("%w: invalid %s", ErrForbidden, t)
	} else {
		g.target = t
	}
	g.log(reason, err)
	return err
}

func (g *Guard) log(reason string, err error) {
	g.changes = append(g.changes, Change{At: time.Now(), Policy: g.policy, Target: g.target, Reason: reason, Err: err})
}

func (g *Guard) Policy() wire.Policy {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.policy
}

func (g *Guard) Target() wire.Target {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.target
}

func (g *Guard) Changes() []Change {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.changes)
}

// Check applies the active policy and target to p. Every refusal wraps
// ErrForbidden and the wire error that explains it.
func (g *Guard) Check(p wire.Packet) error {
	g.mu.Lock()
	pol, t := g.policy, g.target
	g.mu.Unlock()
	if err := pol.Check(p, t); err != nil {
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	}
	return nil
}
