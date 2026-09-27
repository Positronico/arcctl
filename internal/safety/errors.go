package safety

import (
	"errors"
	"strconv"

	"github.com/positronico/arcctl/internal/plan"
)

var (
	ErrOffline  = errors.New("safety: the mouse is offline")
	ErrTimeout  = errors.New("safety: no reply")
	ErrReply    = errors.New("safety: malformed reply")
	ErrMismatch = errors.New("safety: read-back differs from the bytes written")
	ErrProfile  = errors.New("safety: the onboard profile differs")
	ErrIdentity = errors.New("safety: a different device")
	ErrOverlap  = errors.New("safety: a push re-read a range the plan has yet to write")
	ErrAborted  = errors.New("safety: aborted")
	ErrChanged  = errors.New("safety: the device changed while the apply was paused")
	ErrJournal  = errors.New("safety: journal")
	ErrCorrupt  = errors.New("safety: journal is corrupt")
	ErrNotClean = errors.New("safety: the journal has unfinished runs")
	ErrNotOpen  = errors.New("safety: the run has nothing to recover")
	ErrTorn     = errors.New("safety: an extent is torn")
	ErrDiverged = errors.New("safety: the device no longer holds what the run left")
	ErrNotLast  = errors.New("safety: only the last run can be reverted")
	ErrNothing  = errors.New("safety: the run changed nothing")
	ErrStrategy = errors.New("safety: unknown recovery strategy")
)

// Class is what an extent holds compared with the op or run that wrote it.
type Class uint8

const (
	ClassUnknown Class = iota // not read
	ClassOld                  // the bytes before the op or run
	ClassNew                  // the bytes it meant to leave
	ClassMid                  // bytes an earlier op of the run left, such as a binding disabled for two-phase rebinding
	ClassTorn                 // anything else
)

var classNames = [...]string{"unknown", "old", "new", "mid", "torn"}

func (c Class) String() string {
	if int(c) < len(classNames) {
		return classNames[c]
	}
	return "class " + strconv.Itoa(int(c))
}

func parseClass(s string) (Class, bool) {
	return parseName(s, classNames[:], Class(0))
}

// StopError says why an apply stopped. Op is the op that was running, or the
// next one when the apply stopped between ops. Class and Found say what Op's
// extent held when the executor read it after the failure.
type StopError struct {
	Op      plan.Op
	Written bool // a packet of Op went out
	Chunks  int  // chunks of Op acknowledged in its last attempt
	Class   Class
	Found   []byte
	Err     error
}

func (e *StopError) Error() string {
	s := "safety: stopped at op " + strconv.Itoa(e.Op.Seq) + " (" + e.Op.Phase.String() + " " + e.Op.Extent.String() + ")"
	if e.Written {
		s += " after " + strconv.Itoa(e.Chunks) + " chunks, extent " + e.Class.String()
	}
	return s + ": " + e.Err.Error()
}

func (e *StopError) Unwrap() error { return e.Err }

func parseName[T ~uint8](s string, names []string, first T) (T, bool) {
	for i, n := range names {
		if n == s && T(i) >= first {
			return T(i), true
		}
	}
	return 0, false
}
