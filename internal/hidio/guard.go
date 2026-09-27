package hidio

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/wire"
)

// enabled lists the policies a session may switch to at any time.
// Experimental, which adds cmd 22, stays off until long range has an H8
// measurement (D5).
var enabled = []wire.Policy{wire.ReadOnly, wire.Edit}

// gated lists the policies a session may switch to only with a Grant whose
// records hold the gate's hardware stage for the device's model and
// firmware. Reset needs H7's record of the factory reset (D4), so a release
// build reaches it only once verified.json holds one for that firmware.
var gated = map[wire.Policy]Gate{
	wire.Reset: {Feature: ResetFeature, Stage: "H7", Cmd: wire.CmdClear},
}

// ResetFeature is the feature hardware test H7 records in verified.json for
// the factory reset.
const ResetFeature = "device.reset"

// Gate is what a gated policy needs: a record of Feature by hardware stage
// Stage for the device's model and firmware. Cmd is the command the policy
// adds; the guard lets one packet of it through per switch, so it is never
// sent twice.
type Gate struct {
	Feature string
	Stage   string
	Cmd     wire.Cmd
}

// GateOf returns the gate of p, and whether p is gated.
func GateOf(p wire.Policy) (Gate, bool) {
	g, ok := gated[p]
	return g, ok
}

// Grant is what a session knows of the device when it asks for a gated
// policy: the catalog model key, the firmware version the device reports,
// and the hardware records in force.
type Grant struct {
	Model    string
	Firmware string
	Verified catalog.Verifications
}

// Admits returns nil when gr's records hold the gate's stage for exactly
// gr's model and firmware, and the refusal otherwise.
func (g Gate) Admits(gr Grant) error {
	if gr.Model != "" && gr.Firmware != "" {
		for _, v := range gr.Verified.Find(gr.Model, g.Feature) {
			if v.Firmware == gr.Firmware && v.Stage == g.Stage {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: no hardware test %s record of %s for model %q on firmware %q", ErrForbidden, g.Stage, g.Feature, gr.Model, gr.Firmware)
}

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
	once    bool // the gated command of the policy may still go out
	changes []Change
}

func NewGuard(t wire.Target) *Guard {
	return &Guard{target: t}
}

// Set switches the policy. A policy this build does not enable is refused and
// leaves the current one in place. A gated policy also needs exactly one
// grant its gate admits; it then lets one packet of the gated command
// through, until the next Set.
func (g *Guard) Set(p wire.Policy, reason string, grant ...Grant) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate, isGated := gated[p]
	var err error
	switch {
	case slices.Contains(enabled, p):
	case !isGated:
		err = fmt.Errorf("%w: policy %s is not enabled in this build", ErrForbidden, p)
	case len(grant) != 1:
		err = fmt.Errorf("%w: policy %s needs the record of hardware test %s", ErrForbidden, p, gate.Stage)
	default:
		err = gate.Admits(grant[0])
	}
	if err == nil {
		g.policy, g.once = p, isGated
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

// Check applies the active policy and target to p, and lets the gated command
// of a gated policy through once. Every refusal wraps ErrForbidden and the
// wire error that explains it.
func (g *Guard) Check(p wire.Packet) error {
	_, err := g.check(p)
	return err
}

// check is Check, and also reports whether p is the gated packet it let
// through, which must then reach the device at most once.
func (g *Guard) check(p wire.Packet) (once bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.policy.Check(p, g.target); err != nil {
		return false, fmt.Errorf("%w: %w", ErrForbidden, err)
	}
	if gate, ok := gated[g.policy]; ok && p.Cmd() == gate.Cmd {
		if !g.once {
			return false, fmt.Errorf("%w: %v already went out under %s and is never sent again", ErrForbidden, p.Cmd(), g.policy)
		}
		g.once = false
		return true, nil
	}
	return false, nil
}
