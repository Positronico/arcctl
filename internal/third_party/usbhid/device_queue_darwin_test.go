// Local patch for arcctl: see NOTICE and patches/0002-buffered-input.patch.

package usbhid

import (
	"testing"
	"unsafe"
)

func TestDarwinInputCallbackQueuesAndCountsDrops(t *testing.T) {
	d := &Device{}
	d.extra.state = deviceOpen
	d.extra.inputBufferLen = 2
	d.extra.inputCh = make(chan inputCtx, inputQueueLen)
	handle := registerCallbackDevice(d)
	defer unregisterCallbackDevice(handle)

	for i := 0; i < inputQueueLen+3; i++ {
		report := []byte{8, byte(i)}
		inputCallback(handle, kIOReturnSuccess, 0, 0, 8, unsafe.Pointer(&report[0]), _CFIndex(len(report)))
	}
	if got := len(d.extra.inputCh); got != inputQueueLen {
		t.Fatalf("queued %d reports, want %d", got, inputQueueLen)
	}
	if got := d.DroppedInputReports(); got != 3 {
		t.Fatalf("dropped %d reports, want 3", got)
	}
	if first := <-d.extra.inputCh; first.buf[1] != 0 {
		t.Fatalf("first queued report is %v, want the oldest", first.buf)
	}
}
