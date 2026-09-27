package usbhid

import (
	"bytes"
	"testing"
)

func TestReportDescriptorReturnsACopy(t *testing.T) {
	want := []byte{0x06, 0x02, 0xff}
	d := &Device{reportDescriptor: append([]byte{}, want...)}
	got := d.ReportDescriptor()
	got[0] = 0
	if !bytes.Equal(d.ReportDescriptor(), want) {
		t.Fatalf("ReportDescriptor = % x after changing the returned slice, want % x", d.ReportDescriptor(), want)
	}
	if (&Device{}).ReportDescriptor() != nil {
		t.Fatal("ReportDescriptor of a device without one is not nil")
	}
}
