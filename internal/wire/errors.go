package wire

import "errors"

var (
	ErrForbidden = errors.New("wire: forbidden by policy")
	ErrTarget    = errors.New("wire: wrong target")
	ErrFraming   = errors.New("wire: malformed packet")
	ErrChecksum  = errors.New("wire: bad checksum")
	ErrNAK       = errors.New("wire: rejected by the device")
	ErrStatus    = errors.New("wire: unexpected reply status")
)

type wireError struct {
	kind   error
	detail string
}

func (e *wireError) Error() string { return e.kind.Error() + ": " + e.detail }

func (e *wireError) Unwrap() error { return e.kind }

func newError(kind error, detail string) error {
	return &wireError{kind: kind, detail: detail}
}
