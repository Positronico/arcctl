package safety_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// readOnlyLink is a link whose guard was never switched from ReadOnly, so a
// cmd 7 that got past the overlay would be refused before any I/O.
func readOnlyLink(t *testing.T, f *fixture) *link {
	t.Helper()
	tr, err := f.bus.Open(f.dev.Candidates()[1], hidio.NewGuard(wire.Mouse), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return &link{tr: tr, dev: f.dev, replies: map[uint16]wire.Packet{}}
}

// packetsOf is every cmd 7 p sends, in order: each op in chunks of at most
// 10 bytes.
func packetsOf(p plan.Plan) []wire.Packet {
	var out []wire.Packet
	for _, op := range p.Ops {
		for off := 0; off < len(op.New); off += wire.MaxData {
			out = append(out, wire.MustBuild(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr+off), op.New[off:min(off+wire.MaxData, len(op.New))]))
		}
	}
	return out
}

// A dry run goes through the executor as an apply does, from the fresh cmd 3
// to the verified read-back, and leaves the exact packets in the overlay and
// the log. The device gets reads and cmd 3 only, and its flash is unchanged.
func TestDryRunSendsNothingAndRecordsThePackets(t *testing.T) {
	f := newFixture(t, setup{seed: boundMacro, noSync: true})
	var log bytes.Buffer
	ov := safety.NewOverlay(slog.New(slog.NewTextHandler(&log, nil)))
	x := safety.NewExecutor(ov.Link(readOnlyLink(t, f)), f.j, fastOptions())
	p := f.plan(mixedChanges(t, f))
	before := len(f.dev.Writes())

	res, err := x.Apply(context.Background(), p, f.device(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified != len(p.Ops) || res.Echoes != 0 {
		t.Fatalf("result %+v for %d ops", res, len(p.Ops))
	}
	want := packetsOf(p)
	if got := ov.Packets(); !slices.Equal(got, want) {
		t.Fatalf("packets\n got %v\nwant %v", got, want)
	}
	for _, pk := range want {
		if !strings.Contains(log.String(), pk.String()) {
			t.Errorf("packet %v is not in the log", pk)
		}
	}
	checks, reads := 0, 0
	for _, w := range f.dev.Writes()[before:] {
		switch w.Packet.Cmd() {
		case wire.CmdOnline:
			checks++
		case wire.CmdRead:
			reads++
		default:
			t.Errorf("%v reached the device", w.Packet)
		}
	}
	if checks < len(p.Ops) || reads == 0 {
		t.Errorf("%d cmd-3 checks and %d reads for %d ops; the dry run skipped the executor's checks", checks, reads, len(p.Ops))
	}
	if !bytes.Equal(f.image().Bytes(), f.start.Bytes()) {
		t.Fatal("the dry run changed the device")
	}
	if d := differ(ov.Apply(f.image()), after(t, f.image(), p), p); len(d) > 0 {
		t.Fatalf("the overlay differs from the plan at %v", d)
	}
	if got := ov.Written(); len(got) == 0 {
		t.Error("Written is empty")
	}
}

// Dry runs build on each other: a plan made on the overlay's image runs
// against the overlay's bytes, and the device still never changes.
func TestDryRunsAccumulate(t *testing.T) {
	f := newFixture(t, setup{noSync: true})
	ov := safety.NewOverlay(nil)
	l := ov.Link(readOnlyLink(t, f))
	x := safety.NewExecutor(l, f.j, fastOptions())
	first := f.plan(pairChange(t))
	if _, err := x.Apply(context.Background(), first, f.device(), nil); err != nil {
		t.Fatal(err)
	}
	d := f.device()
	d.Image = ov.Apply(d.Image)
	second, err := plan.New(identity, ptr(f.profile), d.Image, d.Layout, dpiChange(t, f.model))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Apply(context.Background(), second, d, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := ov.Packets(), slices.Concat(packetsOf(first), packetsOf(second)); !slices.Equal(got, want) {
		t.Fatalf("packets %v, want %v", got, want)
	}
	if !bytes.Equal(f.image().Bytes(), f.start.Bytes()) {
		t.Fatal("the dry runs changed the device")
	}
}

// Reads through the overlay see its bytes; everything else comes from the
// device.
func TestOverlayPatchesReads(t *testing.T) {
	f := newFixture(t, setup{noSync: true})
	raw := readOnlyLink(t, f)
	ov := safety.NewOverlay(nil)
	l := ov.Link(raw)
	ctx := context.Background()
	w := wire.MustBuild(wire.Mouse, wire.CmdWrite, 4, []byte{0x01, 0x54})
	echo, err := l.Transact(ctx, w)
	if err != nil || !wire.Match(w, echo) {
		t.Fatalf("write = %v, %v", echo, err)
	}
	read := wire.MustBuild(wire.Mouse, wire.CmdRead, 0, make([]byte, 10))
	got, err := l.Transact(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := raw.Transact(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(dev.Data())
	want[4], want[5] = 0x01, 0x54
	if !bytes.Equal(got.Data(), want) || !got.Valid() {
		t.Fatalf("read through the overlay % x (valid %v), want % x", got.Data(), got.Valid(), want)
	}
	if bytes.Equal(dev.Data(), want) {
		t.Fatal("the device holds the overlay's bytes")
	}
	im := ov.Apply(nil)
	if b, _ := im.Get(flash.Extent{Addr: 4, Len: 2}); !bytes.Equal(b, []byte{0x01, 0x54}) || im.Known(flash.Extent{Addr: 0, Len: 1}) {
		t.Fatalf("Apply(nil) holds % x and more", b)
	}
}

// The overlay refuses what the Edit policy refuses, and records nothing.
func TestOverlayChecksPackets(t *testing.T) {
	f := newFixture(t, setup{noSync: true})
	ov := safety.NewOverlay(nil)
	l := ov.Link(readOnlyLink(t, f))
	bad := wire.MustBuild(wire.Mouse, wire.CmdWrite, 4, []byte{0x01, 0x54})
	bad[wire.Size-1]++
	keyboard := wire.MustBuild(wire.Keyboard, wire.CmdWrite, 4, []byte{0x01, 0x54})
	for _, p := range []wire.Packet{bad, keyboard} {
		if _, err := l.Transact(context.Background(), p); !errors.Is(err, hidio.ErrForbidden) || hidio.Classify(err) != hidio.ClassRefused {
			t.Errorf("%v: err = %v, want a refusal", p, err)
		}
	}
	if len(ov.Packets()) != 0 || len(ov.Written()) != 0 {
		t.Fatal("a refused packet was recorded")
	}
}
