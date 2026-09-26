package plan

import "errors"

var (
	ErrMalformed = errors.New("plan: malformed")
	ErrDevice    = errors.New("plan: writes refused for this device")
	ErrExtent    = errors.New("plan: bad extent")
	ErrPhase     = errors.New("plan: wrong phase")
	ErrOrder     = errors.New("plan: ops out of order")
	ErrOverlap   = errors.New("plan: overlapping ops")
	ErrTier      = errors.New("plan: tier forbids writing")
	ErrFrozen    = errors.New("plan: extent is never written")
	ErrChecksum  = errors.New("plan: record breaks its checksum")
	ErrUnread    = errors.New("plan: bytes were never read")
	ErrStale     = errors.New("plan: old bytes differ from the image")
	ErrBinding   = errors.New("plan: binding points at a body being rewritten or invalid")
	ErrLeftClick = errors.New("plan: the last Left Click would be reassigned")
)

type planError struct {
	kind   error
	detail string
}

func (e *planError) Error() string { return e.kind.Error() + ": " + e.detail }

func (e *planError) Unwrap() error { return e.kind }

func newError(kind error, detail string) error {
	return &planError{kind: kind, detail: detail}
}
