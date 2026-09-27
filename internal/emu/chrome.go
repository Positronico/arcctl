package emu

import (
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// ChromeProcess is the process name the competitor shows in Clients.
const ChromeProcess = "Google Chrome Helper"

// Competitor is another HID client on the answering interface, polling the
// way the vendor's web app does in a browser tab: cmd 3, then cmd 4 when the
// mouse is online. Whether it sees arcctl's replies, and arcctl its own,
// follows the device's Sharing. Faults never apply to its packets.
type Competitor struct {
	cl    *client
	every time.Duration
	timer Timer
	seen  []wire.Packet
	done  bool
}

// Chrome opens the answering interface as a competitor. With every > 0 it
// polls on the bus clock at that interval; Poll triggers one poll by hand.
func (d *Device) Chrome(every time.Duration) *Competitor {
	b := d.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &Competitor{every: every}
	c.cl = &client{dev: d, iface: d.answering, pid: b.nextPID, process: ChromeProcess, comp: c}
	b.nextPID++
	if d.gone || b.closed {
		c.done = true
		return c
	}
	d.clients = append(d.clients, c.cl)
	c.schedule()
	return c
}

func (c *Competitor) schedule() {
	if c.every <= 0 || c.done {
		return
	}
	b := c.cl.dev.bus
	c.timer = b.clock.AfterFunc(c.every, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.closed || c.done {
			return
		}
		c.poll()
		c.schedule()
	})
}

// Poll sends one cmd 3, and a cmd 4 if the mouse is awake.
func (c *Competitor) Poll() {
	b := c.cl.dev.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	c.poll()
}

func (c *Competitor) poll() {
	if c.done {
		return
	}
	d := c.cl.dev
	d.process(c.cl, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil), 0, nil)
	if d.mouse != nil && d.mouse.awake {
		d.process(c.cl, wire.MustBuild(wire.Mouse, wire.CmdBattery, 0, nil), 0, nil)
	}
}

// Seen returns the report-8 frames the competitor received.
func (c *Competitor) Seen() []wire.Packet {
	b := c.cl.dev.bus
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(c.seen)
}

// Close stops polling and closes the competitor's handle.
func (c *Competitor) Close() {
	d := c.cl.dev
	d.bus.mu.Lock()
	defer d.bus.mu.Unlock()
	c.stop()
	d.detach(c.cl)
}

func (c *Competitor) stop() {
	c.done = true
	if c.timer != nil {
		c.timer.Stop()
	}
}
