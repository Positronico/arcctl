//go:build hidapi

package hidio

import (
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	hid "github.com/sstallion/go-hid"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/wire"
)

// The hidapi backend needs cgo. Every hidapi call runs on a locked OS thread
// with SIGURG blocked, because the Go runtime's preemption signal interrupts
// IOHIDDeviceSetReport with kIOReturnError.

const (
	hidapiReadTimeout = 250 * time.Millisecond
	maxDescriptor     = 4096 // HID_API_MAX_REPORT_DESCRIPTOR_SIZE
)

func init() { backends["hidapi"] = hidapiBackend{} }

type hidapiBackend struct{}

// worker runs functions one at a time on its own locked thread. The thread is
// never unlocked, so it is discarded with its signal mask when the worker stops.
type worker chan func()

func newWorker() worker {
	w := make(worker)
	go func() {
		runtime.LockOSThread()
		blockSIGURG()
		for fn := range w {
			fn()
		}
	}()
	return w
}

func (w worker) do(fn func() error) error {
	res := make(chan error, 1)
	w <- func() { res <- fn() }
	return <-res
}

var hidapiMain = sync.OnceValue(newWorker)

// hidapiInit must run before any other hidapi call: the first hid_init turns
// exclusive opens back on, so the shared mode is set right after it.
var hidapiInit = sync.OnceValue(func() error {
	return hidapiMain().do(func() error {
		if err := hid.Init(); err != nil {
			return err
		}
		openShared()
		return nil
	})
})

func vendorIDs() []uint16 {
	var vids []uint16
	for _, id := range catalog.USBIDs() {
		if !slices.Contains(vids, id.VID) {
			vids = append(vids, id.VID)
		}
	}
	return vids
}

func (hidapiBackend) enumerate() ([]Candidate, error) {
	if err := hidapiInit(); err != nil {
		return nil, err
	}
	var out []Candidate
	err := hidapiMain().do(func() error {
		for _, vid := range vendorIDs() {
			err := hid.Enumerate(vid, hid.ProductIDAny, func(info *hid.DeviceInfo) error {
				if info.BusType == hid.BusBluetooth {
					return nil
				}
				out = append(out, Candidate{
					Backend:      "hidapi",
					Path:         info.Path,
					VID:          info.VendorID,
					PID:          info.ProductID,
					Interface:    info.InterfaceNbr,
					UsagePage:    info.UsagePage,
					Usage:        info.Usage,
					Manufacturer: info.MfrStr,
					Product:      info.ProductStr,
				})
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (hidapiBackend) open(c Candidate) (Raw, error) {
	if err := hidapiInit(); err != nil {
		return nil, err
	}
	var dev *hid.Device
	err := hidapiMain().do(func() error {
		d, err := hid.OpenPath(c.Path)
		if err != nil {
			return err
		}
		info, err := d.GetDeviceInfo()
		if err != nil || info.VendorID != c.VID || info.ProductID != c.PID {
			d.Close()
			return ErrNotFound
		}
		desc := make([]byte, maxDescriptor)
		n, err := d.GetReportDescriptor(desc)
		if err != nil {
			n = 0
		}
		if err := vendorChannel(desc[:n], runtime.GOOS); err != nil {
			d.Close()
			return err
		}
		dev = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	r := &hidapiRaw{dev: dev, io: newWorker(), in: newInbox(), readerDone: make(chan struct{})}
	go r.read()
	return r, nil
}

type hidapiRaw struct {
	dev        *hid.Device
	io         worker
	in         inbox
	w          writePath
	stop       atomic.Bool
	readerDone chan struct{}
	err        errBox
	closeOnce  sync.Once
	closeErr   error
}

func (r *hidapiRaw) read() {
	runtime.LockOSThread()
	blockSIGURG()
	defer close(r.readerDone)
	defer r.in.close()
	buf := make([]byte, maxFrame+1)
	for !r.stop.Load() {
		n, err := r.dev.ReadWithTimeout(buf, hidapiReadTimeout)
		switch {
		case errors.Is(err, hid.ErrTimeout):
			continue
		case err != nil:
			r.err.set(err)
			return
		case n < 1:
			continue
		}
		r.in.push(Report{ID: buf[0], Data: append([]byte(nil), buf[1:n]...), At: time.Now()})
	}
}

func (r *hidapiRaw) WriteRaw(p wire.Packet) error { return r.write(p, true) }

func (r *hidapiRaw) WriteRawOnce(p wire.Packet) error { return r.write(p, false) }

func (r *hidapiRaw) write(p wire.Packet, resend bool) error {
	buf := append([]byte{wire.ReportID}, p[:]...)
	return r.w.write(func() error {
		return r.io.do(func() error {
			_, err := r.dev.Write(buf)
			return err
		})
	}, resend)
}

func (r *hidapiRaw) Reports() <-chan Report { return r.in.frames.ch }

func (r *hidapiRaw) Others() <-chan Report { return r.in.others.ch }

func (r *hidapiRaw) Err() error { return r.err.get() }

func (r *hidapiRaw) Dropped() uint64 { return r.in.frames.dropped.Load() }

// Close stops and joins the reader before hid_close, which frees the lock the
// reader waits on. A stalled or unjoined device is left open: closing it
// under a stuck call would free memory that call still uses.
func (r *hidapiRaw) Close() error {
	r.closeOnce.Do(func() {
		r.w.closed.Store(true)
		r.stop.Store(true)
		if !join(r.readerDone) || r.w.stalled.Load() {
			r.closeErr = ErrStalled
			return
		}
		r.closeErr = r.io.do(r.dev.Close)
		close(r.io)
	})
	return r.closeErr
}
