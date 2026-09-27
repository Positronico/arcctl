package emu

import (
	"errors"
	"slices"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// Inject adds a fault to the packets arcctl writes from now on.
func (d *Device) Inject(f Fault) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.faults = append(d.faults, &fault{Fault: f})
}

// Release lets every hung write through.
func (d *Device) Release() {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.releaseHangs()
}

// Sleep puts the mouse to sleep: the receiver still answers, the mouse does not.
func (d *Device) Sleep() {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.doze()
}

// Wake wakes the mouse the way moving it does: it answers again, and its
// movement reaches the host as report-7 input.
func (d *Device) Wake() {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	m := d.mouse
	if m == nil || m.awake {
		return
	}
	m.awake = true
	d.noise(1)
	d.pushOnline()
}

// Awake reports whether a paired mouse is awake.
func (d *Device) Awake() bool {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	return d.mouse != nil && d.mouse.awake
}

// Noise sends n mouse-movement reports (report ID 7) to every client of the
// answering interface.
func (d *Device) Noise(n int) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.noise(n)
}

func (d *Device) noise(n int) {
	for range n {
		for _, c := range d.clientsOn(d.answering) {
			c.deliver(noiseID, []byte{0, 1, 0, 0xff, 0xff, 0, 0})
		}
	}
}

// Push sends an unsolicited StatusChanged (cmd 10) with flag bytes f1 and f2.
func (d *Device) Push(f1, f2 byte) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.route(d.answering, statusChanged(f1, f2))
}

// Deliver sends p, as it is, to every client of the answering interface: a
// late, duplicate or foreign reply.
func (d *Device) Deliver(p wire.Packet) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	for _, c := range d.clientsOn(d.answering) {
		c.deliver(wire.ReportID, p[:])
	}
}

// PressDPI presses the mouse's DPI button: the current stage moves to the next
// one in flash, and the mouse pushes StatusChanged 0x01.
func (d *Device) PressDPI() error {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	m := d.mouse
	if m == nil {
		return ErrNotPaired
	}
	count, err := mouse.DecodeStageCount(pairAt(m.image, mouse.AddrMaxDpiStage))
	if err != nil {
		return err
	}
	cur, err := mouse.DecodeCurrentStage(pairAt(m.image, mouse.AddrCurrentDPI))
	if err != nil {
		return err
	}
	next, err := mouse.EncodeCurrentStage((cur + 1) % count)
	if err != nil {
		return err
	}
	_ = m.image.Set(mouse.AddrCurrentDPI, next[:])
	if !m.awake {
		m.awake = true
		d.pushOnline()
	}
	d.route(d.answering, statusChanged(0x01, 0))
	return nil
}

func pairAt(im *flash.Image, addr int) flash.Pair {
	p, _ := im.Pair(addr)
	return p
}

// SwitchProfile makes the mouse change to onboard profile p and push
// StatusChanged 0x04. It fails when the mouse has no profiles.
func (d *Device) SwitchProfile(p byte) error {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	m := d.mouse
	switch {
	case m == nil:
		return ErrNotPaired
	case m.profile == nil:
		return errors.New("emu: the mouse has no profiles")
	}
	*m.profile = p
	d.route(d.answering, statusChanged(0x04, 0))
	return nil
}

// SetProfile changes the onboard profile without a push, as a switch the
// host never hears of would: one made while the mouse sleeps, say.
func (d *Device) SetProfile(p byte) error {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	m := d.mouse
	switch {
	case m == nil:
		return ErrNotPaired
	case m.profile == nil:
		return errors.New("emu: the mouse has no profiles")
	}
	*m.profile = p
	return nil
}

// Store changes the mouse's flash at addr without a push, as a write by
// another program would.
func (d *Device) Store(addr int, b []byte) error {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	if d.mouse == nil {
		return ErrNotPaired
	}
	return d.mouse.image.Set(addr, b)
}

// SetBattery changes what cmd 4 reports.
func (d *Device) SetBattery(b Battery) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	if d.mouse != nil {
		d.mouse.battery = b
	}
}

// Pair replaces the paired mouse, as pairing another one would; nil unpairs.
func (d *Device) Pair(m *Mouse) error {
	var ms *mouseState
	if m != nil {
		var err error
		if ms, err = newMouse(*m); err != nil {
			return err
		}
	}
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.mouse = ms
	return nil
}

// SetLocked makes every write fail with ErrLocked, as a locked screen or
// Secure Input does, until it is called with false.
func (d *Device) SetLocked(v bool) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.locked = v
}

// SetSeized makes opens and writes fail with ErrSeized, as when another
// process opens the device exclusively.
func (d *Device) SetSeized(v bool) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.seized = v
}

// SetDenied makes opens and writes fail with ErrDenied, as when the terminal
// lacks the Input Monitoring grant.
func (d *Device) SetDenied(v bool) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.denied = v
}

// Unplug removes the device: open handles end with ErrGone, and Enumerate no
// longer lists it.
func (d *Device) Unplug() {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	d.unplug()
}

// Plug brings an unplugged device back with the same paths; it has to be
// opened again.
func (d *Device) Plug() {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	if !d.bus.closed {
		d.gone = false
	}
}

// AddClient lists another process in the fake IORegistry, on every interface
// when c.Path is empty.
func (d *Device) AddClient(c Client) {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	if c.Path != "" {
		d.extra = append(d.extra, c)
		return
	}
	for i := range d.ifaces {
		c.Path = d.path(i)
		d.extra = append(d.extra, c)
	}
}

// Image returns a copy of the mouse's flash, or nil when nothing is paired.
func (d *Device) Image() *flash.Image {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	if d.mouse == nil {
		return nil
	}
	return d.mouse.image.Clone()
}

// Writes lists, in order, every packet arcctl's handles delivered to the
// device. Failed writes are not in it.
func (d *Device) Writes() []Write {
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	return slices.Clone(d.writes)
}

// Logical is ws with every resend left out: a write equal to the one right
// before it is the same packet sent again after a try went unanswered.
func Logical(ws []Write) []Write {
	return slices.Compact(slices.Clone(ws))
}
