package hidio

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/third_party/usbhid"
	"github.com/positronico/arcctl/internal/wire"
)

func TestQueueDropsOldest(t *testing.T) {
	q := newQueue()
	for i := range queueLen + 44 {
		q.push(Report{ID: byte(i)})
	}
	if got := q.dropped.Load(); got != 44 {
		t.Fatalf("dropped %d, want 44", got)
	}
	if r := <-q.ch; r.ID != 44 {
		t.Fatalf("oldest queued report is %d, want 44", r.ID)
	}
	q.close()
	q.push(Report{})
	q.close()
}

var errTransient = fmt.Errorf("IOHIDDeviceSetReport failed: (0xE00002BC) (iokit/common) general error")

func TestRetry(t *testing.T) {
	locked := errors.New("set usb hid output report failed [rid=8]: (iokit/common) not permitted (0xe00002e2)")
	tests := []struct {
		name  string
		errs  []error
		want  error
		tries int
	}{
		{"ok", []error{nil}, nil, 1},
		{"transient then ok", []error{errTransient, errTransient, nil}, nil, 3},
		{"transient every time", []error{errTransient, errTransient, errTransient, nil}, errTransient, writeTries},
		{"locked is not retried", []error{locked, nil}, locked, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 0
			err := retry(func() error { n++; return tt.errs[n-1] })
			if !errors.Is(err, tt.want) || n != tt.tries {
				t.Fatalf("retry = %v after %d tries, want %v after %d", err, n, tt.want, tt.tries)
			}
		})
	}
}

// fakeDevice stands in for *usbhid.Device.
type fakeDevice struct {
	mu      sync.Mutex
	input   chan []byte
	done    chan struct{}
	once    sync.Once
	writes  [][]byte
	write   func() error
	closed  atomic.Int32
	readErr error
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{input: make(chan []byte, 8), done: make(chan struct{})}
}

func (d *fakeDevice) GetInputReport() (byte, []byte, error) {
	select {
	case b, ok := <-d.input:
		if !ok {
			return 0, nil, d.readErr
		}
		return b[0], b[1:], nil
	case <-d.done:
		return 0, nil, fmt.Errorf("get usb hid input report failed: %w", usbhid.ErrDeviceIsClosed)
	}
}

func (d *fakeDevice) SetOutputReport(id byte, data []byte) error {
	d.mu.Lock()
	d.writes = append(d.writes, append([]byte{id}, data...))
	write := d.write
	d.mu.Unlock()
	if write != nil {
		return write()
	}
	return nil
}

func (d *fakeDevice) Close() error {
	d.closed.Add(1)
	d.once.Do(func() { close(d.done) })
	return nil
}

func (d *fakeDevice) DroppedInputReports() uint64 { return 3 }

func (d *fakeDevice) attempts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.writes)
}

func TestDeviceRawReadsAndCloses(t *testing.T) {
	dev := newFakeDevice()
	r := newDeviceRaw(dev)
	p := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	dev.input <- []byte{7, 1, 2, 3}
	dev.input <- append([]byte{wire.ReportID}, p[:]...)
	rep := <-r.Others()
	if rep.ID != 7 || !slices.Equal(rep.Data, []byte{1, 2, 3}) || rep.At.IsZero() {
		t.Fatalf("other report = %+v", rep)
	}
	if rep = <-r.Reports(); rep.ID != wire.ReportID || !slices.Equal(rep.Data, p[:]) {
		t.Fatalf("frame = %+v", rep)
	}
	if err := r.WriteRaw(p); err != nil {
		t.Fatal(err)
	}
	if want := append([]byte{wire.ReportID}, p[:]...); !slices.Equal(dev.writes[0], want) {
		t.Fatalf("wrote %x, want %x", dev.writes[0], want)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.readerDone:
	default:
		t.Fatal("Close returned before the reader exited")
	}
	if _, ok := <-r.Reports(); ok {
		t.Fatal("Reports open after Close")
	}
	if _, ok := <-r.Others(); ok {
		t.Fatal("Others open after Close")
	}
	if r.Err() != nil || dev.closed.Load() != 1 || r.Dropped() != 3 {
		t.Fatalf("Err %v, closes %d, dropped %d", r.Err(), dev.closed.Load(), r.Dropped())
	}
	if err := r.WriteRaw(p); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteRaw after Close = %v", err)
	}
}

func TestDeviceRawGone(t *testing.T) {
	dev := newFakeDevice()
	dev.readErr = errors.New("(iokit/common) no such device (0xe00002c0)")
	r := newDeviceRaw(dev)
	close(dev.input)
	if _, ok := <-r.Reports(); ok {
		t.Fatal("Reports open after a read error")
	}
	if Classify(r.Err()) != ClassGone {
		t.Fatalf("Err = %v, class %s, want gone", r.Err(), Classify(r.Err()))
	}
	r.Close()
}

func TestDeviceRawRetriesTransientWrites(t *testing.T) {
	dev := newFakeDevice()
	n := 0
	dev.write = func() error {
		if n++; n < 3 {
			return errTransient
		}
		return nil
	}
	r := newDeviceRaw(dev)
	defer r.Close()
	if err := r.WriteRaw(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); err != nil {
		t.Fatal(err)
	}
	if dev.attempts() != 3 {
		t.Fatalf("%d attempts, want 3", dev.attempts())
	}
}

func TestDeviceRawStallDetachesClose(t *testing.T) {
	dev := newFakeDevice()
	release := make(chan struct{})
	dev.write = func() error { <-release; return nil }
	r := newDeviceRaw(dev)
	r.w.watchdog = 20 * time.Millisecond
	p := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	if err := r.WriteRaw(p); !errors.Is(err, ErrStalled) {
		t.Fatalf("WriteRaw = %v, want ErrStalled", err)
	}
	if err := r.WriteRaw(p); !errors.Is(err, ErrStalled) || dev.attempts() != 1 {
		t.Fatalf("WriteRaw after a stall = %v with %d attempts", err, dev.attempts())
	}
	start := time.Now()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("Close took %v", time.Since(start))
	}
	deadline := time.Now().Add(time.Second)
	for dev.closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dev.closed.Load() != 1 {
		t.Fatal("stalled device was never closed")
	}
	close(release)
}

func TestCandidates(t *testing.T) {
	const receiver = 0x1282
	mk := func(backend, path string, vid, pid uint16, iface int, page uint16) Candidate {
		return Candidate{Backend: backend, Path: path, VID: vid, PID: pid, Interface: iface, UsagePage: page}
	}
	all := []Candidate{
		mk("usbhid", "p/if1", 0x260D, receiver, 1, 0xFF04),
		mk("usbhid", "p/if0", 0x260D, receiver, 0, 0x0001),
		mk("usbhid", "other", 0x046D, 0xC52B, 2, 0xFF00),
		mk("usbhid", "q", 0x062A, 0x9999, 0, 0xFF02),
		mk("hidapi", "dev1", 0x3554, 0xF818, 1, 0x0001),
		mk("hidapi", "dev1", 0x3554, 0xF818, 1, 0xFF02),
		mk("hidapi", "dev1", 0x3554, 0xF818, 1, 0x000C),
		mk("usbhid", "win-col05", 0x260D, receiver, 1, 0xFF02),
	}
	paths := func(cs []Candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, fmt.Sprintf("%s:%04x", c.Path, c.UsagePage))
		}
		return out
	}
	tests := []struct {
		goos string
		want []string
	}{
		{"darwin", []string{"p/if0:0001", "p/if1:ff04", "win-col05:ff02", "dev1:ff02"}},
		{"linux", []string{"p/if0:0001", "p/if1:ff04", "win-col05:ff02", "dev1:ff02"}},
		{"windows", []string{"win-col05:ff02", "dev1:ff02"}},
	}
	for _, tt := range tests {
		got := candidates(all, tt.goos)
		if !slices.Equal(paths(got), tt.want) {
			t.Errorf("%s: candidates = %v, want %v", tt.goos, paths(got), tt.want)
		}
		for _, c := range got {
			if c.Class != catalog.Classify(c.VID, c.PID) || c.Class == catalog.ClassUnknown {
				t.Errorf("%s: %s has class %s", tt.goos, c.Path, c.Class)
			}
		}
	}
}

func TestInterfaceFromPath(t *testing.T) {
	tests := []struct {
		name string
		goos string
		path string
		want int
	}{
		{"darwin interface 1", "darwin", "IOService:/x/XHC@00000000/port@00000000/Receiver@00000000/IOUSBHostInterface@1/AppleUserUSBHostHIDDevice", 1},
		{"darwin interface 0", "darwin", "IOService:/x/IOUSBHostInterface@0/AppleUserUSBHostHIDDevice", 0},
		{"darwin without interface", "darwin", "IOService:/x/AppleUserUSBHostHIDDevice", -1},
		{"windows mi_01", "windows", `\\?\hid#vid_260d&pid_1282&mi_01&col05#8&2c1a2b3c&0&0004#{4d1e55b2-f16f-11cf-88cb-001111000030}`, 1},
		{"windows mi_0a", "windows", `\\?\HID#VID_260D&PID_1282&MI_0A#x`, 10},
		{"freebsd", "freebsd", "/dev/uhid0", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := interfaceOf(tt.goos, tt.path); got != tt.want {
				t.Fatalf("interfaceOf = %d, want %d", got, tt.want)
			}
		})
	}
	sysfs := "/sys/devices/pci0000:00/0000:00:14.0/usb1/1-2/1-2:1.1/0003:260D:1282.0005"
	if got := interfaceMatch(sysfsInterface, sysfs, 10); got != 1 {
		t.Fatalf("sysfs interface = %d, want 1", got)
	}
}
