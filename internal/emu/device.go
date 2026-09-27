package emu

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

const (
	defaultVID = 0x260D
	defaultPID = 0x1282
	noiseID    = 7
)

// placeholderAddr is the mouse address a Mouse gets when it names none; the
// test vectors use the same one.
var placeholderAddr = [3]byte{0x11, 0x22, 0x33}

var ErrNotPaired = errors.New("emu: no mouse is paired")

// Device is one emulated USB device on a Bus: a receiver and the mouse paired
// to it. Its methods act the way the hardware, the user or the OS would.
type Device struct {
	bus       *Bus
	id        int
	vid, pid  uint16
	ifaces    int
	answering int
	rxVersion *Version
	behavior  Behavior
	latency   Latency
	mouse     *mouseState
	clients   []*client
	extra     []Client
	faults    []*fault
	writes    []Write
	locked    bool
	seized    bool
	denied    bool
	gone      bool
	release   chan struct{}
}

// Write is a packet one of arcctl's handles delivered to the device.
type Write struct {
	Interface int
	Packet    wire.Packet
}

type mouseState struct {
	cid, mid  byte
	conn      byte
	addr      [3]byte
	image     *flash.Image
	factory   *flash.Image
	firmware  Version
	battery   Battery
	profile   *byte
	longRange *bool
	awake     bool
}

func newDevice(b *Bus, c Config) (*Device, error) {
	d := &Device{
		bus:       b,
		vid:       cmp0(c.VID, defaultVID),
		pid:       cmp0(c.PID, defaultPID),
		ifaces:    c.Interfaces,
		answering: c.Answering,
		behavior:  c.Behavior,
		latency:   c.Latency,
		release:   make(chan struct{}),
	}
	if d.ifaces == 0 {
		d.ifaces, d.answering = 2, 1
	}
	if d.answering < 0 || d.answering >= d.ifaces {
		return nil, fmt.Errorf("emu: answering interface %d of %d", d.answering, d.ifaces)
	}
	if c.RxVersion != nil {
		v := *c.RxVersion
		d.rxVersion = &v
	}
	if c.Mouse != nil {
		m, err := newMouse(*c.Mouse)
		if err != nil {
			return nil, err
		}
		d.mouse = m
	}
	return d, nil
}

func newMouse(c Mouse) (*mouseState, error) {
	m := &mouseState{
		cid:      c.CID,
		mid:      c.MID,
		conn:     c.Conn,
		addr:     c.Addr,
		firmware: c.Firmware,
		battery:  c.Battery,
		awake:    !c.Asleep,
	}
	if c.Model != nil {
		m.cid = cmp0(m.cid, c.Model.CID)
		if len(c.Model.MIDs) > 0 {
			m.mid = cmp0(m.mid, c.Model.MIDs[0])
		}
	}
	if m.addr == ([3]byte{}) {
		m.addr = placeholderAddr
	}
	if c.Profile != nil {
		p := *c.Profile
		m.profile = &p
	}
	if c.LongRange != nil {
		v := *c.LongRange
		m.longRange = &v
	}
	defaults, err := modelDefaults(c.Model)
	if err != nil {
		return nil, err
	}
	switch {
	case c.Image != nil:
		m.image = c.Image.Clone()
	case defaults != nil:
		m.image = defaults.Clone()
	default:
		m.image = flash.New()
	}
	switch {
	case c.Factory != nil:
		m.factory = c.Factory.Clone()
	case defaults != nil:
		m.factory = defaults
	default:
		m.factory = m.image.Clone()
	}
	return m, nil
}

func modelDefaults(m *catalog.Model) (*flash.Image, error) {
	if m == nil || m.Defaults == nil {
		return nil, nil
	}
	return Defaults(m)
}

func cmp0[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

func (d *Device) path(iface int) string {
	return fmt.Sprintf("emu:%d/IOUSBHostInterface@%d", d.id, iface)
}

func (d *Device) candidates() []hidio.Candidate {
	product := "Emulated receiver"
	if d.mouse != nil {
		if m, ok := catalog.Resolve(d.mouse.cid, d.mouse.mid); ok {
			product = "Emulated receiver (" + m.Name + ")"
		}
	}
	out := make([]hidio.Candidate, d.ifaces)
	for i := range out {
		out[i] = hidio.Candidate{
			Backend:      Backend,
			Path:         d.path(i),
			VID:          d.vid,
			PID:          d.pid,
			Class:        catalog.Classify(d.vid, d.pid),
			Interface:    i,
			UsagePage:    vendorUsagePage,
			Usage:        vendorUsage,
			Manufacturer: "arcctl",
			Product:      product,
		}
	}
	return out
}

// Candidates lists this device's interfaces as Enumerate would.
func (d *Device) Candidates() []hidio.Candidate {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	return d.candidates()
}

// refuse is why the device cannot take a write now.
func (d *Device) refuse(cl *client) error {
	switch {
	case d.bus.closed, d.gone, !slices.Contains(d.clients, cl):
		return ErrGone
	case d.denied:
		return ErrDenied
	case d.seized:
		return ErrSeized
	case d.locked:
		return ErrLocked
	}
	return nil
}

// write is the device side of a handle's WriteRaw.
func (d *Device) write(cl *client, p wire.Packet) error {
	b := d.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := d.refuse(cl); err != nil {
		return err
	}
	f := d.fault(p)
	var act Action
	if f != nil {
		act = f.Action
	}
	switch act {
	case Fail:
		return f.err()
	case Unplug:
		d.unplug()
		return ErrGone
	case Hang:
		rel := d.release
		b.mu.Unlock()
		<-rel
		b.mu.Lock()
		if err := d.refuse(cl); err != nil {
			return err
		}
		f, act = nil, 0
	}
	d.writes = append(d.writes, Write{Interface: cl.iface, Packet: p})
	d.process(cl, p, act, f)
	return nil
}

func (d *Device) fault(p wire.Packet) *fault {
	var hit *fault
	for _, f := range d.faults {
		if f.count(p) && hit == nil {
			hit = f
		}
	}
	return hit
}

type reply struct {
	p     wire.Packet
	radio bool
}

// process answers p as the device would and sends the replies to the clients
// of cl's interface.
func (d *Device) process(cl *client, p wire.Packet, act Action, f *fault) {
	if act == Asleep {
		d.doze()
	}
	if cl.iface != d.answering {
		return
	}
	undo := d.misstore(p, act)
	replies := d.respond(p)
	undo()
	switch act {
	case Drop:
		replies = nil
	case NAK:
		replies = []reply{{nak(p, d.behavior.ShortNAK), false}}
	case Duplicate:
		replies = append(replies, replies...)
	}
	for _, r := range replies {
		delay := d.latency.Receiver
		if r.radio {
			delay = d.latency.Mouse
		}
		delay += d.bus.jitter(d.latency.Jitter)
		if act == Late {
			delay = f.Delay
		}
		d.send(cl.iface, r.p, delay)
	}
}

// misstore arms Ignore and Corrupt for a cmd 7 the mouse takes: the returned
// func, run after the mouse answered, puts in the bytes the fault keeps.
func (d *Device) misstore(p wire.Packet, act Action) func() {
	m := d.mouse
	e, ok := extent(p)
	if act != Ignore && act != Corrupt || p.Cmd() != wire.CmdWrite || m == nil || !m.awake || !ok {
		return func() {}
	}
	keep, _ := m.image.Get(e)
	if act == Corrupt {
		keep = slices.Clone(p[5 : 5+e.Len])
		keep[0] ^= 0x01
	}
	return func() { _ = m.image.Set(e.Addr, keep) }
}

func (d *Device) send(iface int, p wire.Packet, delay time.Duration) {
	if delay <= 0 {
		d.route(iface, p)
		return
	}
	d.bus.after(delay, func() {
		if !d.gone {
			d.route(iface, p)
		}
	})
}

// route hands a report-8 frame to the clients of iface: all of them, or one
// picked by the bus seed when the device steals.
func (d *Device) route(iface int, p wire.Packet) {
	targets := d.clientsOn(iface)
	if len(targets) > 1 && d.behavior.Sharing == Steal {
		targets = targets[d.bus.rng.IntN(len(targets)):][:1]
	}
	for _, c := range targets {
		c.deliver(wire.ReportID, p[:])
	}
}

func (d *Device) clientsOn(iface int) []*client {
	var out []*client
	for _, c := range d.clients {
		if c.iface == iface {
			out = append(out, c)
		}
	}
	return out
}

func (d *Device) detach(cl *client) {
	d.clients = slices.DeleteFunc(d.clients, func(c *client) bool { return c == cl })
}

func (d *Device) unplug() {
	if d.gone {
		return
	}
	d.gone = true
	for _, c := range d.clients {
		if c.pipe != nil {
			c.pipe.Fail(ErrGone)
		}
		if c.comp != nil {
			c.comp.stop()
		}
	}
	d.clients = nil
	d.releaseHangs()
}

func (d *Device) releaseHangs() {
	close(d.release)
	d.release = make(chan struct{})
}

func (d *Device) doze() {
	if m := d.mouse; m != nil && m.awake {
		m.awake = false
		d.pushOnline()
	}
}

func (d *Device) pushOnline() {
	if d.behavior.PushOnline {
		d.route(d.answering, d.online())
	}
}

func (d *Device) online() wire.Packet {
	var online byte
	var a [3]byte
	if m := d.mouse; m != nil {
		a = m.addr
		if m.awake {
			online = 1
		}
	}
	return frame(wire.CmdOnline, 0, []byte{online, a[2], a[1], a[0]})
}
