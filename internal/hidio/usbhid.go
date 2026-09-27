package hidio

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/third_party/usbhid"
	"github.com/positronico/arcctl/internal/wire"
)

func init() { backends["usbhid"] = usbhidBackend{} }

type usbhidBackend struct{}

func (usbhidBackend) enumerate() ([]Candidate, error) {
	devs, err := usbhid.Enumerate(func(d *usbhid.Device) bool {
		return catalog.Classify(d.VendorId(), d.ProductId()) != catalog.ClassUnknown &&
			vendorChannel(d.ReportDescriptor(), runtime.GOOS) == nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(devs))
	for _, d := range devs {
		out = append(out, Candidate{
			Backend:      "usbhid",
			Path:         d.Path(),
			VID:          d.VendorId(),
			PID:          d.ProductId(),
			Interface:    interfaceOf(runtime.GOOS, d.Path()),
			UsagePage:    d.UsagePage(),
			Usage:        d.Usage(),
			Manufacturer: d.Manufacturer(),
			Product:      d.Product(),
		})
	}
	return out, nil
}

func (usbhidBackend) open(c Candidate) (Raw, error) {
	devs, err := usbhid.Enumerate(func(d *usbhid.Device) bool {
		return d.Path() == c.Path && d.VendorId() == c.VID && d.ProductId() == c.PID
	})
	if err != nil {
		return nil, err
	}
	if len(devs) == 0 {
		return nil, ErrNotFound
	}
	d := devs[0]
	if err := vendorChannel(d.ReportDescriptor(), runtime.GOOS); err != nil {
		return nil, err
	}
	if err := d.Open(false); err != nil {
		return nil, err
	}
	return newDeviceRaw(d), nil
}

// hidDevice is the part of *usbhid.Device the backend uses.
type hidDevice interface {
	GetInputReport() (byte, []byte, error)
	SetOutputReport(id byte, data []byte) error
	Close() error
	DroppedInputReports() uint64
}

type deviceRaw struct {
	dev        hidDevice
	in         inbox
	w          writePath
	readerDone chan struct{}
	err        errBox
	closing    atomic.Bool
	closeOnce  sync.Once
	closeErr   error
}

func newDeviceRaw(dev hidDevice) *deviceRaw {
	r := &deviceRaw{dev: dev, in: newInbox(), readerDone: make(chan struct{})}
	go r.read()
	return r
}

func (r *deviceRaw) read() {
	defer close(r.readerDone)
	defer r.in.close()
	for {
		id, data, err := r.dev.GetInputReport()
		if err != nil {
			if !r.closing.Load() {
				r.err.set(err)
			}
			return
		}
		r.in.push(Report{ID: id, Data: data, At: time.Now()})
	}
}

func (r *deviceRaw) WriteRaw(p wire.Packet) error {
	return r.w.write(func() error { return r.dev.SetOutputReport(wire.ReportID, p[:]) }, true)
}

func (r *deviceRaw) WriteRawOnce(p wire.Packet) error {
	return r.w.write(func() error { return r.dev.SetOutputReport(wire.ReportID, p[:]) }, false)
}

func (r *deviceRaw) Reports() <-chan Report { return r.in.frames.ch }

func (r *deviceRaw) Others() <-chan Report { return r.in.others.ch }

func (r *deviceRaw) Err() error { return r.err.get() }

// Dropped adds the reports usbhid dropped before they reached the reader; it
// cannot tell their report IDs.
func (r *deviceRaw) Dropped() uint64 {
	return r.in.frames.dropped.Load() + r.dev.DroppedInputReports()
}

// Close closes the device first, which ends a blocked GetInputReport, and then
// joins the reader. After a stall the close runs detached, since it may wait
// behind the stuck write; it still ends the reader first.
func (r *deviceRaw) Close() error {
	r.closeOnce.Do(func() {
		r.closing.Store(true)
		r.w.closed.Store(true)
		if r.w.stalled.Load() {
			go r.dev.Close()
		} else if err := r.dev.Close(); err != nil && !errors.Is(err, usbhid.ErrDeviceIsClosed) {
			r.closeErr = err
		}
		join(r.readerDone)
	})
	return r.closeErr
}
