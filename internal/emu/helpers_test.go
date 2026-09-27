package emu_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/vectors"
	"github.com/positronico/arcctl/internal/wire"
)

const (
	waitFor = time.Second
	quiet   = 30 * time.Millisecond
)

func newBus(t *testing.T, o emu.Options) *emu.Bus {
	t.Helper()
	b := emu.New(o)
	t.Cleanup(b.Close)
	return b
}

func add(t *testing.T, b *emu.Bus, c emu.Config) *emu.Device {
	t.Helper()
	d, err := b.Add(c)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func model(t testing.TB, key string) *catalog.Model {
	t.Helper()
	m, ok := catalog.ByKey(key)
	if !ok {
		t.Fatalf("no model %s", key)
	}
	return m
}

func testdata(t testing.TB, name string) []byte {
	t.Helper()
	root, err := vectors.ModuleRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// dumpImage is the committed settings page with placeholder bodies in the
// slots it binds.
func dumpImage(t testing.TB) *flash.Image {
	t.Helper()
	im, err := flash.FromDump(0, testdata(t, "flash-dump.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if im, err = emu.WithBodies(im); err != nil {
		t.Fatal(err)
	}
	return im
}

func ptr[T any](v T) *T { return &v }

// em11 is an EM11 Pro paired to the default receiver, awake.
func em11(t testing.TB) *emu.Mouse {
	return &emu.Mouse{
		Model:     model(t, "7B04"),
		Image:     dumpImage(t),
		Firmware:  emu.Version{Major: 1, Minor: 5},
		Battery:   emu.Battery{Level: 80, MilliVolts: 3900},
		Profile:   ptr(byte(0)),
		LongRange: ptr(true),
	}
}

func receiver(m *emu.Mouse) emu.Config {
	return emu.Config{RxVersion: &emu.Version{Major: 1, Minor: 2}, Mouse: m}
}

// open opens interface iface of d under a read-only mouse guard.
func open(t *testing.T, b *emu.Bus, d *emu.Device, iface int) hidio.Transport {
	t.Helper()
	tr, err := b.Open(d.Candidates()[iface], hidio.NewGuard(wire.Mouse), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func req(c wire.Cmd, data ...byte) wire.Packet { return wire.MustBuild(wire.Mouse, c, 0, data) }

func handshake() wire.Packet { return req(wire.CmdHandshake, 0xa1, 0xb2, 0xc3, 0xd4, 0, 0, 0, 0) }

func read(t testing.TB, addr, n int) wire.Packet {
	t.Helper()
	p, err := wire.BuildRead(wire.Mouse, uint16(addr), n)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// next returns the next report-8 frame, failing when none comes.
func next(t *testing.T, tr hidio.Transport) wire.Packet {
	t.Helper()
	select {
	case r, ok := <-tr.Reports():
		if !ok {
			t.Fatal("Reports closed")
		}
		p, _ := r.Packet()
		return p
	case <-time.After(waitFor):
		t.Fatal("no report")
	}
	return wire.Packet{}
}

// none fails when a report-8 frame arrives within the quiet period.
func none(t *testing.T, tr hidio.Transport) {
	t.Helper()
	select {
	case r, ok := <-tr.Reports():
		if ok {
			t.Fatalf("unexpected report % x", r.Data)
		}
	case <-time.After(quiet):
	}
}

func write(t *testing.T, tr hidio.Transport, p wire.Packet) {
	t.Helper()
	if err := tr.Write(p); err != nil {
		t.Fatalf("Write(%v): %v", p, err)
	}
}

// transact writes p and returns its reply.
func transact(t *testing.T, tr hidio.Transport, p wire.Packet) wire.Packet {
	t.Helper()
	write(t, tr, p)
	rep := next(t, tr)
	if !wire.Match(p, rep) {
		t.Fatalf("reply %v does not answer %v", rep, p)
	}
	return rep
}

func vector(t testing.TB, name string) []byte {
	t.Helper()
	path, err := vectors.DefaultPath(".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vectors.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Vectors {
		if v.Name == name {
			b, err := v.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("no vector %q", name)
	return nil
}
