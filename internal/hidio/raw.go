package hidio

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// Raw is an unguarded packet pipe to one HID interface. Device backends never
// hand one out: Open wraps it with Guarded before returning.
//
// WriteRaw sends p as output report 8. WriteRawOnce sends it the same way but
// never resends it, even after an error the OS reports as transient: the
// device may have taken the report already. Reports delivers input reports
// and is closed when the input side stops, after Close or when the device
// goes away. A Raw may also implement
//
//	Others() <-chan Report   the input reports that are not report-8 frames of
//	                         16 bytes, queued apart from Reports, which then
//	                         carries only those frames; closed with Reports
//	Err() error              why its input stopped; nil after a clean Close
//	Wake() <-chan struct{}   input activity it has already coalesced
//	Dropped() uint64         report-8 frames it dropped because they were not read in time
type Raw interface {
	WriteRaw(p wire.Packet) error
	WriteRawOnce(p wire.Packet) error
	Reports() <-chan Report
	Close() error
}

type otherer interface{ Others() <-chan Report }

type errer interface{ Err() error }

type waker interface{ Wake() <-chan struct{} }

type dropper interface{ Dropped() uint64 }

func rawErr(r Raw) error {
	if e, ok := r.(errer); ok {
		return e.Err()
	}
	return nil
}

func rawOthers(r Raw) <-chan Report {
	if o, ok := r.(otherer); ok {
		return o.Others()
	}
	return nil
}

func rawWake(r Raw) <-chan struct{} {
	if w, ok := r.(waker); ok {
		return w.Wake()
	}
	return nil
}

func rawDropped(r Raw) uint64 {
	if d, ok := r.(dropper); ok {
		return d.Dropped()
	}
	return 0
}

const (
	writeTries    = 3
	writeBackoff  = 20 * time.Millisecond
	writeWatchdog = 2 * time.Second
	joinTimeout   = time.Second
)

// writePath is the write side every Raw that stands for a device shares:
// retries on transient IOKit errors unless the packet must go out once, a
// watchdog on each try, and a sticky stalled state once the watchdog fires.
type writePath struct {
	mu       sync.Mutex
	watchdog time.Duration
	stalled  atomic.Bool
	closed   atomic.Bool
}

func (w *writePath) write(send func() error, resend bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.closed.Load():
		return ErrClosed
	case w.stalled.Load():
		return ErrStalled
	}
	limit := w.watchdog
	if limit == 0 {
		limit = writeWatchdog
	}
	try := func() error { return withWatchdog(limit, send) }
	var err error
	if resend {
		err = retry(try)
	} else {
		err = try()
	}
	if errors.Is(err, ErrStalled) {
		w.stalled.Store(true)
	}
	return err
}

func retry(try func() error) error {
	var err error
	for i := range writeTries {
		if i > 0 {
			time.Sleep(time.Duration(i) * writeBackoff)
		}
		if err = try(); Classify(err) != ClassRetry {
			return err
		}
	}
	return err
}

// withWatchdog returns ErrStalled when fn has not returned after limit. fn keeps
// running; the caller must not reuse what it holds.
func withWatchdog(limit time.Duration, fn func() error) error {
	res := make(chan error, 1)
	go func() { res <- fn() }()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case err := <-res:
		return err
	case <-t.C:
		return ErrStalled
	}
}

func join(done <-chan struct{}) bool {
	t := time.NewTimer(joinTimeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
