// Local patch for arcctl: see NOTICE and patches/0001-setreport-timeout.patch.

package usbhid

import (
	"errors"
	"testing"
	"time"
	"unsafe"
)

func stubReportIO(t *testing.T, setReport func(context uintptr, typ _IOHIDReportType, reportId _CFIndex, report unsafe.Pointer, reportLength _CFIndex, timeout _CFTimeInterval) _IOReturn, dispatch bool) *Device {
	t.Helper()
	originalSetReport := _IOHIDDeviceSetReportWithCallback
	originalSignal := _CFRunLoopSourceSignal
	originalWakeUp := _CFRunLoopWakeUp
	originalStop := _CFRunLoopStop
	originalClose := _IOHIDDeviceClose
	originalRelease := _CFRelease
	originalPush := _objc_autoreleasePoolPush
	originalPop := _objc_autoreleasePoolPop
	t.Cleanup(func() {
		_IOHIDDeviceSetReportWithCallback = originalSetReport
		_CFRunLoopSourceSignal = originalSignal
		_CFRunLoopWakeUp = originalWakeUp
		_CFRunLoopStop = originalStop
		_IOHIDDeviceClose = originalClose
		_CFRelease = originalRelease
		_objc_autoreleasePoolPush = originalPush
		_objc_autoreleasePoolPop = originalPop
	})

	_IOHIDDeviceSetReportWithCallback = func(_ _IOHIDDeviceRef, typ _IOHIDReportType, reportId _CFIndex, report unsafe.Pointer, reportLength _CFIndex, timeout _CFTimeInterval, _ uintptr, context uintptr) _IOReturn {
		return setReport(context, typ, reportId, report, reportLength, timeout)
	}
	var d *Device
	_CFRunLoopSourceSignal = func(_ _CFRunLoopSourceRef) {
		if !dispatch {
			return
		}
		select {
		case fn := <-d.extra.ioCh:
			go fn()
		default:
		}
	}
	_CFRunLoopWakeUp = func(_ _CFRunLoopRef) {}
	_CFRunLoopStop = func(_ _CFRunLoopRef) {}
	_IOHIDDeviceClose = func(_ _IOHIDDeviceRef, _ _IOOptionBits) _IOReturn { return kIOReturnSuccess }
	_CFRelease = func(_ _CFTypeRef) {}
	_objc_autoreleasePoolPush = func() uintptr { return 1 }
	_objc_autoreleasePoolPop = func(_ uintptr) {}

	d = &Device{reportWithId: true}
	d.extra.state = deviceOpen
	d.extra.file = 1
	d.extra.ioSource = 1
	d.extra.ioCh = make(chan func(), 1)
	d.extra.reportCh = make(chan _IOReturn, 1)
	d.extra.done = make(chan struct{})
	d.extra.runloopDone = make(chan struct{})
	close(d.extra.runloopDone)
	d.extra.callbackHandle = registerCallbackDevice(d)
	t.Cleanup(func() { unregisterCallbackDevice(d.extra.callbackHandle) })
	return d
}

func TestDarwinSetReportPassesTimeout(t *testing.T) {
	var got _CFTimeInterval
	d := stubReportIO(t, func(context uintptr, typ _IOHIDReportType, reportId _CFIndex, report unsafe.Pointer, reportLength _CFIndex, timeout _CFTimeInterval) _IOReturn {
		got = timeout
		reportCallback(context, kIOReturnSuccess, 0, typ, uint32(reportId), report, reportLength)
		return kIOReturnSuccess
	}, true)
	if err := d.setOutputReport(8, []byte{3}); err != nil {
		t.Fatalf("set output report failed: %v", err)
	}
	if got != 1000 {
		t.Fatalf("timeout passed to IOKit = %v ms, want 1000 ms", float64(got))
	}
}

func TestDarwinSetReportTimeoutIsReported(t *testing.T) {
	const kIOReturnTimeout _IOReturn = -0x1ffffd2a
	d := stubReportIO(t, func(context uintptr, typ _IOHIDReportType, reportId _CFIndex, report unsafe.Pointer, reportLength _CFIndex, _ _CFTimeInterval) _IOReturn {
		reportCallback(context, kIOReturnTimeout, 0, typ, uint32(reportId), report, reportLength)
		return kIOReturnSuccess
	}, true)
	err := d.setOutputReport(8, []byte{3})
	var ioErr ioReturnError
	if !errors.As(err, &ioErr) || _IOReturn(ioErr) != kIOReturnTimeout {
		t.Fatalf("set output report returned %v, want kIOReturnTimeout", err)
	}
}

func TestDarwinCloseAbandonsHungReport(t *testing.T) {
	grace := setReportGrace
	setReportGrace = 50 * time.Millisecond
	t.Cleanup(func() { setReportGrace = grace })

	started := make(chan struct{})
	d := stubReportIO(t, func(uintptr, _IOHIDReportType, _CFIndex, unsafe.Pointer, _CFIndex, _CFTimeInterval) _IOReturn {
		close(started)
		return kIOReturnSuccess
	}, true)

	ioResult := make(chan error, 1)
	go func() { ioResult <- d.setOutputReport(8, []byte{3}) }()
	<-started

	closeResult := make(chan error, 1)
	go func() { closeResult <- d.close() }()
	select {
	case err := <-ioResult:
		if !errors.Is(err, ErrDeviceIsClosed) {
			t.Fatalf("hung report returned %v, want ErrDeviceIsClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hung report kept blocking after close")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("close failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close stayed blocked behind a hung report")
	}
}

func TestDarwinCloseFreesUndispatchedReport(t *testing.T) {
	grace := setReportGrace
	setReportGrace = 50 * time.Millisecond
	t.Cleanup(func() { setReportGrace = grace })

	originalDeallocate := _CFAllocatorDeallocate
	freed := make(chan struct{}, 1)
	_CFAllocatorDeallocate = func(allocator _CFAllocatorRef, ptr unsafe.Pointer) {
		originalDeallocate(allocator, ptr)
		freed <- struct{}{}
	}
	t.Cleanup(func() { _CFAllocatorDeallocate = originalDeallocate })

	d := stubReportIO(t, func(uintptr, _IOHIDReportType, _CFIndex, unsafe.Pointer, _CFIndex, _CFTimeInterval) _IOReturn {
		t.Error("report reached IOKit")
		return kIOReturnSuccess
	}, false)

	ioResult := make(chan error, 1)
	go func() { ioResult <- d.setOutputReport(8, []byte{3}) }()
	deadline := time.After(time.Second)
	for len(d.extra.ioCh) == 0 {
		select {
		case <-deadline:
			t.Fatal("report was never queued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := d.close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := <-ioResult; !errors.Is(err, ErrDeviceIsClosed) {
		t.Fatalf("undispatched report returned %v, want ErrDeviceIsClosed", err)
	}
	select {
	case <-freed:
	default:
		t.Fatal("buffer of a report that never reached IOKit was not freed")
	}
}
