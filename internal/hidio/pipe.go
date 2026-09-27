package hidio

import (
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// Pipe is a Raw whose device side is code, such as the emulator. Packets
// written to it go to the function given to NewPipe, and the device side
// answers with Deliver. Writes get the same retries, watchdog and stall
// handling as a real device.
type Pipe struct {
	send func(wire.Packet) error
	in   inbox
	w    writePath
	err  errBox
}

func NewPipe(send func(p wire.Packet) error) *Pipe {
	return &Pipe{send: send, in: newInbox()}
}

// SetWatchdog changes how long a write may block before it counts as stalled.
// Call it before the first write.
func (p *Pipe) SetWatchdog(d time.Duration) { p.w.watchdog = d }

func (p *Pipe) WriteRaw(pk wire.Packet) error {
	return p.w.write(func() error { return p.send(pk) }, true)
}

func (p *Pipe) WriteRawOnce(pk wire.Packet) error {
	return p.w.write(func() error { return p.send(pk) }, false)
}

// Deliver queues an input report from the device side. It never blocks.
func (p *Pipe) Deliver(id byte, data []byte) {
	p.in.push(Report{ID: id, Data: slices.Clone(data), At: time.Now()})
}

// Fail ends the input side with err, as a device that went away would.
func (p *Pipe) Fail(err error) {
	p.err.set(err)
	p.in.close()
}

func (p *Pipe) Reports() <-chan Report { return p.in.frames.ch }

func (p *Pipe) Others() <-chan Report { return p.in.others.ch }

func (p *Pipe) Err() error { return p.err.get() }

func (p *Pipe) Dropped() uint64 { return p.in.frames.dropped.Load() }

func (p *Pipe) Close() error {
	p.w.closed.Store(true)
	p.in.close()
	return nil
}
