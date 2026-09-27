// Package emu emulates ProtoArc receivers and the mice paired to them, so the
// session, the CLI and the TUI can run without hardware. A Bus holds the
// emulated USB devices. Its Enumerate and Open mirror hidio's, and Open hands
// out only guarded Transports. Each Device can be made to misbehave the ways
// real hardware and other HID clients do.
package emu

import (
	"cmp"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

// Backend is the Candidate.Backend of emulated interfaces. hidio.Open does not
// know it, so only Bus.Open can open them.
const Backend = "emu"

const (
	vendorUsagePage = 0xFF02
	vendorUsage     = 2
	arcctlProcess   = "arcctl"
)

type Options struct {
	Seed     uint64        // drives latency jitter and reply stealing
	Clock    Clock         // nil: real time
	Watchdog time.Duration // write watchdog of opened handles; 0: hidio's default
	PID      int           // process ID of arcctl's own clients in Clients; 0: this process
}

// Bus is a set of emulated USB devices. All of its state sits behind one
// lock, so a run that uses a ManualClock and no latency replays exactly.
type Bus struct {
	mu      sync.Mutex
	opt     Options
	clock   Clock
	rng     *rand.Rand
	devices []*Device
	nextPID int
	closed  bool
}

func New(o Options) *Bus {
	b := &Bus{opt: o, clock: o.Clock, rng: rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15)), nextPID: 4000}
	if b.clock == nil {
		b.clock = realClock{}
	}
	if b.opt.PID == 0 {
		b.opt.PID = os.Getpid()
	}
	return b
}

// Add plugs in a device described by c.
func (b *Bus) Add(c Config) (*Device, error) {
	d, err := newDevice(b, c)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	d.id = len(b.devices) + 1
	b.devices = append(b.devices, d)
	return d, nil
}

// Enumerate lists every interface of every plugged device whose VID and PID
// the catalog knows, ordered like hidio.Enumerate.
func (b *Bus) Enumerate() ([]hidio.Candidate, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []hidio.Candidate
	for _, d := range b.devices {
		if !d.gone && catalog.Classify(d.vid, d.pid) != catalog.ClassUnknown {
			out = append(out, d.candidates()...)
		}
	}
	slices.SortFunc(out, func(a, b hidio.Candidate) int {
		return cmp.Or(cmp.Compare(a.VID, b.VID), cmp.Compare(a.PID, b.PID), cmp.Compare(a.Interface, b.Interface), strings.Compare(a.Path, b.Path))
	})
	return out, nil
}

// Open opens c shared, records its traffic when rec is not nil, and returns it
// guarded by g, like hidio.Open.
func (b *Bus) Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error) {
	h, err := b.open(c)
	if err != nil {
		return nil, err
	}
	var raw hidio.Raw = h
	if rec != nil {
		raw = rec.Wrap(raw)
	}
	return hidio.Guarded(raw, g), nil
}

func (b *Bus) open(c hidio.Candidate) (*handle, error) {
	if c.Backend != Backend {
		return nil, fmt.Errorf("%w %q: emu opens only its own candidates", hidio.ErrBackend, c.Backend)
	}
	if catalog.Classify(c.VID, c.PID) == catalog.ClassUnknown {
		return nil, fmt.Errorf("%w: %04x:%04x is not a device arcctl knows", hidio.ErrNotFound, c.VID, c.PID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	d, iface, ok := b.find(c.Path)
	switch {
	case b.closed || !ok || d.gone || d.vid != c.VID || d.pid != c.PID:
		return nil, fmt.Errorf("%w: %s", hidio.ErrNotFound, c.Path)
	case d.denied:
		return nil, ErrDenied
	case d.seized:
		return nil, ErrSeized
	}
	cl := &client{dev: d, iface: iface, pid: b.opt.PID, process: arcctlProcess, own: true}
	cl.pipe = hidio.NewPipe(func(p wire.Packet) error { return d.write(cl, p) })
	if b.opt.Watchdog > 0 {
		cl.pipe.SetWatchdog(b.opt.Watchdog)
	}
	d.clients = append(d.clients, cl)
	return &handle{Pipe: cl.pipe, cl: cl}, nil
}

func (b *Bus) find(path string) (*Device, int, bool) {
	for _, d := range b.devices {
		for i := range d.ifaces {
			if d.path(i) == path {
				return d, i, true
			}
		}
	}
	return nil, 0, false
}

// Client is an entry of the fake IORegistry: a process holding an interface
// open.
type Client struct {
	Path    string
	PID     int
	Process string
	Seized  bool
}

// Clients lists the processes that hold an interface of a plugged device
// open, arcctl's own handles included, as the IORegistry scan would.
func (b *Bus) Clients() []Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Client
	for _, d := range b.devices {
		if d.gone {
			continue
		}
		for _, c := range d.clients {
			out = append(out, Client{Path: d.path(c.iface), PID: c.pid, Process: c.process})
		}
		out = append(out, d.extra...)
	}
	return out
}

// Close unplugs every device and stops every timer the bus runs. Hung writes
// return.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, d := range b.devices {
		d.unplug()
	}
}

func (b *Bus) after(d time.Duration, f func()) {
	b.clock.AfterFunc(d, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.closed {
			f()
		}
	})
}

func (b *Bus) jitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	return time.Duration(b.rng.Int64N(int64(limit) + 1))
}

// client is one open handle on an interface: arcctl's, through a Pipe, or the
// competitor's.
type client struct {
	dev     *Device
	iface   int
	pid     int
	process string
	own     bool
	pipe    *hidio.Pipe
	comp    *Competitor
}

func (c *client) deliver(id byte, data []byte) {
	if c.pipe != nil {
		c.pipe.Deliver(id, data)
		return
	}
	if p, ok := (hidio.Report{ID: id, Data: data}).Packet(); ok {
		c.comp.seen = append(c.comp.seen, p)
	}
}

// handle is the Raw behind an opened Transport. Closing it removes its client
// from the device.
type handle struct {
	*hidio.Pipe
	cl *client
}

func (h *handle) Close() error {
	d := h.cl.dev
	d.bus.mu.Lock()
	d.detach(h.cl)
	d.bus.mu.Unlock()
	return h.Pipe.Close()
}
