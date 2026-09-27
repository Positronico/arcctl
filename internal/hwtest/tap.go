//go:build hwtest

package hwtest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

// RawDevices finds interfaces and opens them without a guard: hidio.OpenRaw
// for real devices, the emulator's Bus.OpenRaw for rehearsals and tests.
type RawDevices interface {
	Enumerate() ([]hidio.Candidate, error)
	OpenRaw(c hidio.Candidate) (hidio.Raw, error)
}

// HID is RawDevices for a hidio backend; "" is the default one.
func HID(backend string) RawDevices { return hidRaw(backend) }

type hidRaw string

func (b hidRaw) Enumerate() ([]hidio.Candidate, error) { return hidio.Enumerate(string(b)) }

func (hidRaw) OpenRaw(c hidio.Candidate) (hidio.Raw, error) { return hidio.OpenRaw(c) }

var errNoTap = errors.New("hwtest: the interface is not open")

// devices is the session's Devices over RawDevices. Every interface a session
// opens gets a tap between its Raw and the session's guard.
type devices struct {
	raw  RawDevices
	mu   sync.Mutex
	taps map[string]*tap
	late []lateClaim
}

// lateClaim keeps frames match accepts from every session until until: the
// late replies to a raw packet, which may reach a tap opened after it.
type lateClaim struct {
	match func(wire.Packet) bool
	until time.Time
}

func newDevices(r RawDevices) *devices {
	return &devices{raw: r, taps: map[string]*tap{}}
}

func (d *devices) Enumerate() ([]hidio.Candidate, error) { return d.raw.Enumerate() }

func (d *devices) Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error) {
	r, err := d.raw.OpenRaw(c)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		r = rec.Wrap(r)
	}
	t := newTap(r)
	d.mu.Lock()
	d.taps[c.Path] = t
	for _, l := range d.late {
		t.keep(l)
	}
	d.mu.Unlock()
	return hidio.Guarded(t, g), nil
}

// expect keeps the frames match accepts from the sessions of every tap,
// open now or later, for d: they are late replies to the raw path.
func (d *devices) expect(match func(wire.Packet) bool, dur time.Duration) {
	l := lateClaim{match: match, until: time.Now().Add(dur)}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.late = append(d.late, l)
	for _, t := range d.taps {
		t.keep(l)
	}
}

// tap returns the open tap of the interface at path.
func (d *devices) tap(path string) (*tap, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.taps[path]
	if t == nil || t.closed.Load() {
		return nil, errNoTap
	}
	return t, nil
}

const tapQueue = 256

// tap passes the session's packets and reports through unchanged. The raw
// path writes its own packets straight to the Raw beneath it and claims their
// replies, which then never reach the session.
type tap struct {
	raw    hidio.Raw
	out    chan hidio.Report
	stop   chan struct{}
	done   chan struct{}
	closed atomic.Bool
	once   sync.Once
	err    error

	mu     sync.Mutex
	claims []*claim
	late   int
}

func newTap(r hidio.Raw) *tap {
	t := &tap{raw: r, out: make(chan hidio.Report, tapQueue), stop: make(chan struct{}), done: make(chan struct{})}
	go t.forward()
	return t
}

// WriteRaw is the session's write. Replies of the shape it asks for belong
// to the session from now on: claims that would take them give them up.
func (t *tap) WriteRaw(p wire.Packet) error {
	t.yield(p)
	return t.raw.WriteRaw(p)
}

func (t *tap) WriteRawOnce(p wire.Packet) error {
	t.yield(p)
	return t.raw.WriteRawOnce(p)
}

func (t *tap) yield(p wire.Packet) {
	t.mu.Lock()
	now := time.Now()
	for _, c := range t.claims {
		if c.swallow && c.match(p) {
			c.swallow = false
			if !c.watch {
				c.expires = now
			}
		}
	}
	t.mu.Unlock()
}

func (t *tap) Reports() <-chan hidio.Report { return t.out }

func (t *tap) Others() <-chan hidio.Report {
	if o, ok := t.raw.(interface{ Others() <-chan hidio.Report }); ok {
		return o.Others()
	}
	return nil
}

func (t *tap) Wake() <-chan struct{} {
	if w, ok := t.raw.(interface{ Wake() <-chan struct{} }); ok {
		return w.Wake()
	}
	return nil
}

func (t *tap) Err() error {
	if e, ok := t.raw.(interface{ Err() error }); ok {
		return e.Err()
	}
	return nil
}

func (t *tap) Dropped() uint64 {
	if d, ok := t.raw.(interface{ Dropped() uint64 }); ok {
		return d.Dropped()
	}
	return 0
}

func (t *tap) Close() error {
	t.once.Do(func() {
		t.closed.Store(true)
		t.err = t.raw.Close()
		close(t.stop)
		<-t.done
	})
	return t.err
}

func (t *tap) forward() {
	defer close(t.done)
	defer close(t.out)
	in := t.raw.Reports()
	for {
		select {
		case <-t.stop:
			return
		case r, ok := <-in:
			if !ok {
				return
			}
			if t.take(r) {
				continue
			}
			select {
			case t.out <- r:
			case <-t.stop:
				return
			}
		}
	}
}

// reply is a report-8 frame and when it arrived.
type reply struct {
	p  wire.Packet
	at time.Time
}

// claim is a raw request waiting for its answers. It sees every frame while
// its exchange runs; a swallowing claim keeps the frames it matches from the
// session, until it expires after its exchange.
type claim struct {
	match   func(wire.Packet) bool
	swallow bool
	watch   bool
	got     []reply
	seen    []reply
	signal  chan struct{}
	expires time.Time
}

func (t *tap) take(r hidio.Report) bool {
	p, ok := r.Packet()
	if !ok {
		return false
	}
	at := r.At
	if at.IsZero() {
		at = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	taken, watched := false, false
	live := t.claims[:0]
	for _, c := range t.claims {
		if !c.expires.IsZero() && at.After(c.expires) {
			continue
		}
		live = append(live, c)
		if c.watch {
			c.seen = append(c.seen, reply{p, at})
		}
		if !c.match(p) {
			continue
		}
		if c.watch {
			watched = true
			c.got = append(c.got, reply{p, at})
			select {
			case c.signal <- struct{}{}:
			default:
			}
		}
		taken = taken || c.swallow
	}
	clear(t.claims[len(live):])
	t.claims = live
	if taken && !watched {
		t.late++
	}
	return taken
}

// exchangeOpts shape one raw exchange: first is how long it waits for the
// first answer, listen how long it keeps listening after it for more, and
// linger how long afterwards a swallowing claim still keeps late answers from
// the session. once sends the packet with no resend, even after an error the
// OS calls transient.
type exchangeOpts struct {
	match   func(wire.Packet) bool
	swallow bool
	once    bool
	first   time.Duration
	listen  time.Duration
	linger  time.Duration
}

type exchange struct {
	sent    wire.Packet
	at      time.Time
	replies []reply
	seen    []reply
}

func (x exchange) latency() time.Duration {
	if len(x.replies) == 0 {
		return 0
	}
	return x.replies[0].at.Sub(x.at)
}

// exchange sends p to the Raw, past the guard, and collects the frames o.match
// accepts.
func (t *tap) exchange(ctx context.Context, p wire.Packet, o exchangeOpts) (x exchange, err error) {
	x.sent = p
	if t.closed.Load() {
		return x, errNoTap
	}
	c := &claim{match: o.match, swallow: o.swallow, watch: true, signal: make(chan struct{}, 1)}
	t.mu.Lock()
	t.claims = append(t.claims, c)
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		c.watch = false
		c.expires = time.Now().Add(o.linger)
		x.replies, x.seen = c.got, c.seen
		t.mu.Unlock()
	}()
	x.at = time.Now()
	send := t.raw.WriteRaw
	if o.once {
		send = t.raw.WriteRawOnce
	}
	if err := send(p); err != nil {
		return x, err
	}
	wait := time.NewTimer(o.first)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return x, ctx.Err()
	case <-wait.C:
		return x, nil
	case <-c.signal:
	}
	if o.listen <= 0 {
		return x, nil
	}
	wait.Reset(o.listen)
	select {
	case <-ctx.Done():
		return x, ctx.Err()
	case <-wait.C:
	}
	return x, nil
}

// keep adds a claim that swallows the frames of l until it expires, and
// counts them as late replies.
func (t *tap) keep(l lateClaim) {
	if time.Now().After(l.until) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.claims = append(t.claims, &claim{match: l.match, swallow: true, expires: l.until})
}

// lateReplies counts the frames only claims whose exchange had ended took:
// answers that came after the raw path stopped listening.
func (t *tap) lateReplies() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.late
}
