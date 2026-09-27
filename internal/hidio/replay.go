package hidio

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// placeholderAddr stands in for a redacted cmd-3 address (11 22 33, stored
// reversed), the same placeholder the test vectors use.
var placeholderAddr = [3]byte{0x33, 0x22, 0x11}

// Replay is a Raw that plays a transcript back. Each write must match the next
// recorded write; the input reports recorded after that write are then
// delivered, and a recorded write error is returned. Leading input reports are
// delivered at once. Redacted bytes, and the random bytes 5 to 8 of a
// handshake with the checksum that covers them, are ignored when matching.
// Redacted input bytes are replayed as 0xFF (erased flash) or the placeholder
// address, with the checksum recomputed.
type Replay struct {
	mu      sync.Mutex
	entries []Entry
	pos     int
	writes  int
	in      inbox
	err     errBox
	div     error
	closed  bool
}

func NewReplay(r io.Reader) (*Replay, error) {
	_, entries, err := ReadTranscript(r)
	if err != nil {
		return nil, err
	}
	rp := &Replay{entries: entries, in: newInbox()}
	rp.advance()
	return rp, nil
}

// OpenReplay returns a guarded Transport over a replay of r, and the Replay so
// the caller can check that it was followed to the end.
func OpenReplay(r io.Reader, g *Guard) (Transport, *Replay, error) {
	rp, err := NewReplay(r)
	if err != nil {
		return nil, nil, err
	}
	return Guarded(rp, g), rp, nil
}

// advance delivers entries up to the next write and returns the error of a
// failed write among them.
func (rp *Replay) advance() error {
	var werr error
	for ; rp.pos < len(rp.entries); rp.pos++ {
		e := rp.entries[rp.pos]
		switch e.Dir {
		case DirOut:
			return werr
		case DirIn:
			rp.in.push(Report{ID: e.ID, Data: fill(e.ID, e.Data), At: time.Now()})
		case DirOutError:
			werr = replayedError(e.Err)
		case DirInError:
			rp.err.set(replayedError(e.Err))
			rp.in.close()
		}
	}
	return werr
}

// WriteRawOnce is WriteRaw: a replay never resends.
func (rp *Replay) WriteRawOnce(p wire.Packet) error { return rp.WriteRaw(p) }

func (rp *Replay) WriteRaw(p wire.Packet) error {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	switch {
	case rp.closed:
		return ErrClosed
	case rp.div != nil:
		return rp.div
	case rp.pos >= len(rp.entries):
		rp.div = fmt.Errorf("%w: write %d (%s) comes after the end of the transcript", ErrDiverged, rp.writes+1, p)
		return rp.div
	}
	want := rp.entries[rp.pos].Data
	if diff := mismatch(want, p); len(diff) > 0 {
		rp.div = fmt.Errorf("%w: write %d: want %s, got %s, differs at bytes %v", ErrDiverged, rp.writes+1, want, p, diff)
		return rp.div
	}
	rp.writes++
	rp.pos++
	return rp.advance()
}

func (rp *Replay) Reports() <-chan Report { return rp.in.frames.ch }

func (rp *Replay) Others() <-chan Report { return rp.in.others.ch }

func (rp *Replay) Err() error { return rp.err.get() }

// Diverged returns the first write that did not match the transcript.
func (rp *Replay) Diverged() error {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.div
}

// Remaining counts the recorded writes not yet matched.
func (rp *Replay) Remaining() int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	n := 0
	for _, e := range rp.entries[rp.pos:] {
		if e.Dir == DirOut {
			n++
		}
	}
	return n
}

func (rp *Replay) Close() error {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.closed = true
	rp.in.close()
	return nil
}

func mismatch(want Frame, got wire.Packet) []int {
	ignore := want.Mask
	if got.Cmd() == wire.CmdHandshake {
		ignore |= 0xF<<dataShift | checksumBit
	}
	var diff []int
	for i := range wire.Size {
		if ignore&(1<<i) == 0 && (i >= len(want.Bytes) || want.Bytes[i] != got[i]) {
			diff = append(diff, i)
		}
	}
	return diff
}

func fill(id byte, f Frame) []byte {
	b := slices.Clone(f.Bytes)
	if f.Mask == 0 {
		return b
	}
	for i := range b {
		if !f.Redacted(i) {
			continue
		}
		b[i] = 0xFF
		if id == wire.ReportID && len(b) == wire.Size && b[0] == byte(wire.CmdOnline) && i >= 6 && i <= 8 {
			b[i] = placeholderAddr[i-6]
		}
	}
	if id == wire.ReportID && len(b) == wire.Size && f.Redacted(wire.Size-1) {
		var p wire.Packet
		copy(p[:], b)
		b[wire.Size-1] = p.Checksum()
	}
	return b
}

// replayedError carries a recorded error's text, so that IOReturn and Classify
// see the same code the live error had.
type replayedError string

func (e replayedError) Error() string { return string(e) }

func (e replayedError) Is(target error) bool {
	t := target.Error()
	return t != "" && strings.Contains(string(e), t)
}
