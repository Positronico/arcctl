package emu

import (
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// Action is what a Fault does to a packet.
type Action uint8

const (
	Drop      Action = iota + 1 // the device takes the packet and never answers
	NAK                         // the device answers with status 1
	Duplicate                   // every reply is sent twice
	Late                        // every reply comes after Fault.Delay
	Asleep                      // the mouse falls asleep just before the packet reaches it
	Hang                        // the write blocks until Release; then the packet goes through
	Fail                        // the write fails with Fault.Err and the packet is lost
	Unplug                      // the device goes away; the write fails with ErrGone
	Ignore                      // a cmd 7 is answered as usual, but the flash keeps its old bytes
	Corrupt                     // a cmd 7 is answered as usual, but the flash takes its data with the first byte's low bit flipped
	Taken                       // the device takes and answers the packet, then the write fails with Fault.Err
)

var actionNames = [...]string{"none", "drop", "nak", "duplicate", "late", "asleep", "hang", "fail", "unplug", "ignore", "corrupt", "taken"}

func (a Action) String() string {
	if int(a) < len(actionNames) {
		return actionNames[a]
	}
	return "action?"
}

// Fault applies an Action to some of the packets arcctl's handles write; the
// competitor's traffic is never affected. Each fault counts the packets that
// match it, a write hidio retries once per try: it lets Skip of them through
// and then applies to the next Times (every later one when Times is 0). When
// several faults apply to a packet, the one injected first wins.
type Fault struct {
	Cmd    wire.Cmd               // 0: any command
	Match  func(wire.Packet) bool // nil: every packet of Cmd
	Skip   int
	Times  int
	Action Action
	Err    error         // Fail and Taken; nil means ErrGeneral
	Delay  time.Duration // Late
}

// Nth is a Fault.Match for the n-th packet (from 1) of command c, counted as
// Logical counts writes: a packet equal to the one right before it is a
// resend, and matches when that one did. Skip counts every try instead, so a
// resend of an earlier packet would move it. The fault leaves Cmd unset, for
// Nth must see every packet to tell a resend.
func Nth(c wire.Cmd, n int) func(wire.Packet) bool {
	var last wire.Packet
	seen := 0
	return func(p wire.Packet) bool {
		resend := p == last
		last = p
		if p.Cmd() != c {
			return false
		}
		if !resend {
			seen++
		}
		return seen == n
	}
}

type fault struct {
	Fault
	seen int
}

func (f *fault) count(p wire.Packet) bool {
	if f.Cmd != 0 && p.Cmd() != f.Cmd || f.Match != nil && !f.Match(p) {
		return false
	}
	f.seen++
	return f.seen > f.Skip && (f.Times == 0 || f.seen <= f.Skip+f.Times)
}

func (f *fault) err() error {
	if f.Err == nil {
		return ErrGeneral
	}
	return f.Err
}
