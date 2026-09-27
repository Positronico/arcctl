package hidio_test

import (
	"errors"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

var errTaken = errors.New("IOHIDDeviceSetReport: (iokit/common) general error (0xe00002bc)")

// counting is a Raw whose device takes every packet and then reports a
// transient error for those fail matches.
func counting(fail func(wire.Packet) bool) (*hidio.Pipe, map[wire.Cmd]int) {
	n := map[wire.Cmd]int{}
	return hidio.NewPipe(func(p wire.Packet) error {
		n[p.Cmd()]++
		if fail(p) {
			return errTaken
		}
		return nil
	}), n
}

// The receiver may take a report and IOKit still answer kIOReturnError: the
// reset let through by the guard must not reach the mouse again, while the
// other packets under the same policy keep their retries.
func TestGatedPacketIsNeverResent(t *testing.T) {
	online := 0
	p, n := counting(func(pk wire.Packet) bool {
		if pk.Cmd() == wire.CmdOnline {
			online++
			return online == 1
		}
		return pk.Cmd() == wire.CmdClear
	})
	g := hidio.NewGuard(wire.Mouse)
	tr := hidio.Guarded(p, g)
	defer tr.Close()
	vs := catalog.Verifications{{Model: "7B04", Feature: hidio.ResetFeature, Firmware: "v1.05", Stage: "H7"}}
	if err := g.Set(wire.Reset, "reset", hidio.Grant{Model: "7B04", Firmware: "v1.05", Verified: vs}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil)); hidio.Classify(err) != hidio.ClassRetry {
		t.Fatalf("Write = %v, want the transient error", err)
	}
	if n[wire.CmdClear] != 1 {
		t.Fatalf("cmd 9 reached the device %d times under one guard pass", n[wire.CmdClear])
	}
	if err := tr.Write(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); err != nil || n[wire.CmdOnline] != 2 {
		t.Fatalf("cmd 3: %v after %d tries, want a retry", err, n[wire.CmdOnline])
	}
}

func TestWriteRawOnceIsNeverResent(t *testing.T) {
	p, n := counting(func(wire.Packet) bool { return true })
	defer p.Close()
	pk := wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil)
	if err := p.WriteRawOnce(pk); !errors.Is(err, errTaken) || n[wire.CmdClear] != 1 {
		t.Fatalf("WriteRawOnce = %v after %d tries", err, n[wire.CmdClear])
	}
	if err := p.WriteRaw(pk); !errors.Is(err, errTaken) || n[wire.CmdClear] != 4 {
		t.Fatalf("WriteRaw = %v after %d tries, want 3 more", err, n[wire.CmdClear]-1)
	}
}
