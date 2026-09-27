package session

import "strconv"

type State uint8

const (
	NoReceiver State = iota
	NeedsPermission
	Probing
	Choosing
	Locked
	Seized
	Stalled
	Offline
	Handshaking
	Loading
	Ready
	Conflict
	SuspectedConflict
	Applying
	Recovering
	Unknown
)

var stateNames = [...]string{
	"no receiver", "needs permission", "probing", "choosing", "locked", "seized", "stalled", "offline",
	"handshaking", "loading", "ready", "conflict", "suspected conflict", "applying", "recovering", "unknown device",
}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "state " + strconv.Itoa(int(s))
}

// attached reports whether the state has a chosen device whose mouse the
// session talks to; a conflict shows over these states.
func (s State) attached() bool {
	switch s {
	case Offline, Handshaking, Loading, Ready, Unknown:
		return true
	}
	return false
}

func (s State) blocked() bool {
	return s == Locked || s == Seized || s == NeedsPermission
}
