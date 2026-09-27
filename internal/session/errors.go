package session

import "errors"

var (
	ErrNoReply        = errors.New("session: no reply")
	ErrOffline        = errors.New("session: the mouse is offline")
	ErrGone           = errors.New("session: the device went away")
	ErrStopped        = errors.New("session: stopped")
	ErrNotConnected   = errors.New("session: no device is attached")
	ErrNotLoaded      = errors.New("session: the configuration is not loaded")
	ErrNotChoosing    = errors.New("session: no device choice is pending")
	ErrNoSuchDevice   = errors.New("session: no such device")
	ErrNoAnswer       = errors.New("session: no interface answered")
	ErrUnknownModel   = errors.New("session: unknown model")
	ErrChargingBase   = errors.New("session: charging base: unsupported")
	ErrDeviceChanged  = errors.New("session: a different device is paired")
	ErrProfileChanged = errors.New("session: the onboard profile changed")
	ErrUnsupported    = errors.New("session: not supported for this device")
	ErrReadOnly       = errors.New("session: writes are not enabled for this session")
	ErrBusy           = errors.New("session: another write is in progress")
	ErrPanic          = errors.New("session: the write panicked")
	ErrConflict       = errors.New("session: another client is still talking to the device")
	ErrWrites         = errors.New("session: the device keeps refusing writes")
)
