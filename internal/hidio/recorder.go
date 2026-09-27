package hidio

import (
	"encoding/json"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// Recorder writes a transcript of every packet written and every input report
// read through the Raws it wraps. With Header.Redacted set, each entry is
// passed through Redact before it is written.
type Recorder struct {
	mu     sync.Mutex
	enc    *json.Encoder
	redact bool
	err    error
}

// NewRecorder writes h as the transcript's first line.
func NewRecorder(w io.Writer, h Header) (*Recorder, error) {
	h.Version = transcriptVersion
	if h.Started.IsZero() {
		h.Started = time.Now()
	}
	if h.Redacted {
		h.Started = h.Started.UTC()
	}
	r := &Recorder{enc: newEncoder(w), redact: h.Redacted}
	if err := r.enc.Encode(h); err != nil {
		return nil, err
	}
	return r, nil
}

// Note adds a line of free text, such as a hardware-test step.
func (r *Recorder) Note(text string) {
	r.record(Entry{At: time.Now(), Dir: DirNote, Text: text})
}

// Err returns the first error writing the transcript; recording stops there.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Recorder) record(e Entry) {
	if r.redact {
		e = Redact(e)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = r.enc.Encode(e)
	}
}

// Wrap returns a Raw that records raw's traffic. A write is recorded before it
// is sent, so its reply can never precede it in the transcript.
func (r *Recorder) Wrap(raw Raw) Raw {
	x := &recorded{raw: raw, rec: r, in: newInbox(), stop: make(chan struct{}), done: make(chan struct{})}
	go x.forward()
	return x
}

type recorded struct {
	raw       Raw
	rec       *Recorder
	in        inbox
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (x *recorded) WriteRaw(p wire.Packet) error {
	x.rec.record(Entry{At: time.Now(), Dir: DirOut, ID: wire.ReportID, Data: Frame{Bytes: slices.Clone(p[:])}})
	err := x.raw.WriteRaw(p)
	if err != nil {
		x.rec.record(Entry{At: time.Now(), Dir: DirOutError, Err: err.Error()})
	}
	return err
}

func (x *recorded) Reports() <-chan Report { return x.in.frames.ch }

func (x *recorded) Others() <-chan Report { return x.in.others.ch }

func (x *recorded) Wake() <-chan struct{} { return rawWake(x.raw) }

func (x *recorded) Err() error { return rawErr(x.raw) }

func (x *recorded) Dropped() uint64 { return x.in.frames.dropped.Load() + rawDropped(x.raw) }

func (x *recorded) Close() error {
	x.closeOnce.Do(func() {
		x.closeErr = x.raw.Close()
		close(x.stop)
		<-x.done
	})
	return x.closeErr
}

func (x *recorded) forward() {
	defer close(x.done)
	defer x.in.close()
	in, others := x.raw.Reports(), rawOthers(x.raw)
	for {
		select {
		case <-x.stop:
			return
		case rep, ok := <-in:
			if !ok {
				if err := rawErr(x.raw); err != nil {
					x.rec.record(Entry{At: time.Now(), Dir: DirInError, Err: err.Error()})
				}
				return
			}
			x.input(rep)
		case rep, ok := <-others:
			if !ok {
				others = nil
				continue
			}
			x.input(rep)
		}
	}
}

func (x *recorded) input(rep Report) {
	x.rec.record(Entry{At: rep.At, Dir: DirIn, ID: rep.ID, Data: Frame{Bytes: rep.Data}})
	x.in.push(rep)
}
