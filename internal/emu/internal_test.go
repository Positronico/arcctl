package emu

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// These tests reach the device through an unguarded handle, the only way to
// exercise the commands the read-only guard refuses.

func rawOpen(t *testing.T, o Options, c Config) (*Bus, *Device, *handle) {
	t.Helper()
	b := New(o)
	t.Cleanup(b.Close)
	d, err := b.Add(c)
	if err != nil {
		t.Fatal(err)
	}
	h, err := b.open(d.Candidates()[d.answering])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return b, d, h
}

func em11() *Mouse {
	m, _ := catalog.ByKey("7B04")
	return &Mouse{Model: m, LongRange: new(bool)}
}

// drain returns the report-8 frames queued on h. Replies without latency are
// queued before WriteRaw returns.
func drain(h *handle) []wire.Packet {
	var out []wire.Packet
	for {
		select {
		case r, open := <-h.Reports():
			if !open {
				return out
			}
			if p, ok := r.Packet(); ok {
				out = append(out, p)
			}
		default:
			return out
		}
	}
}

func send(t *testing.T, h *handle, p wire.Packet) []wire.Packet {
	t.Helper()
	if err := h.WriteRaw(p); err != nil {
		t.Fatalf("WriteRaw(%v): %v", p, err)
	}
	return drain(h)
}

func withChecksum(p wire.Packet) wire.Packet {
	p[wire.Size-1] = p.Checksum()
	return p
}

func TestWriteEcho(t *testing.T) {
	p := wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 2, 0, 0x52})
	header := withChecksum(wire.Packet{7, 0, 0, 0x60, 4})
	tests := []struct {
		name string
		bh   Behavior
		want []wire.Packet
	}{
		{"full", Behavior{}, []wire.Packet{p}},
		{"header", Behavior{Echo: EchoHeader}, []wire.Packet{header}},
		{"none", Behavior{Echo: EchoNone}, nil},
		{"double", Behavior{DoubleWrite: true}, []wire.Packet{p, p}},
		{"double header", Behavior{Echo: EchoHeader, DoubleWrite: true}, []wire.Packet{header, header}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, h := rawOpen(t, Options{}, Config{Mouse: em11(), Behavior: tt.bh})
			if got := send(t, h, p); !slices.Equal(got, tt.want) {
				t.Fatalf("replies %v, want %v", got, tt.want)
			}
			if got, _ := d.Image().Get(flash.Extent{Addr: 0x60, Len: 4}); !bytes.Equal(got, []byte{1, 2, 0, 0x52}) {
				t.Fatalf("flash at 0x60 = % x", got)
			}
		})
	}
}

func TestClear(t *testing.T) {
	for _, silent := range []bool{false, true} {
		_, d, h := rawOpen(t, Options{}, Config{Mouse: em11(), Behavior: Behavior{ClearSilent: silent}})
		send(t, h, wire.MustBuild(wire.Mouse, wire.CmdWrite, mouse.AddrReportRate, []byte{1, 0x54}))
		got := send(t, h, wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil))
		if want := !silent; (len(got) == 1 && got[0].Cmd() == wire.CmdClear) != want {
			t.Fatalf("silent %v: replies %v", silent, got)
		}
		m, _ := catalog.ByKey("7B04")
		want, _ := Defaults(m)
		if !bytes.Equal(d.Image().Bytes(), want.Bytes()) {
			t.Fatalf("silent %v: flash after cmd 9 is not the factory image", silent)
		}
	}
}

func TestLongRange(t *testing.T) {
	_, _, h := rawOpen(t, Options{}, Config{Mouse: em11()})
	on := wire.MustBuild(wire.Mouse, wire.CmdSetLongRange, 0, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if got := send(t, h, on); len(got) != 1 || got[0].Status() != wire.StatusOK {
		t.Fatalf("cmd 22 replies %v", got)
	}
	if got := send(t, h, wire.MustBuild(wire.Mouse, wire.CmdGetLongRange, 0, nil)); len(got) != 1 || got[0][5] != 1 {
		t.Fatalf("cmd 23 after setting: %v", got)
	}
}

func TestPacketsTheReceiverRefuses(t *testing.T) {
	unknown := wire.MustBuild(wire.Mouse, 46, 0, nil)
	bad := wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{9, 9, 0, 0x43})
	bad[wire.Size-1]--
	past := withChecksum(wire.Packet{8, 0, 0x3f, 0xfc, 10})
	keyboard := wire.MustBuild(wire.Keyboard, wire.CmdOnline, 0, nil)
	tests := []struct {
		name    string
		bh      Behavior
		p       wire.Packet
		nak     bool
		applied bool
	}{
		{"unknown: NAK", Behavior{}, unknown, true, false},
		{"unknown: silence", Behavior{Unknown: AnswerSilence}, unknown, false, false},
		{"bad checksum: NAK", Behavior{}, bad, true, false},
		{"bad checksum: silence", Behavior{BadChecksum: AnswerSilence}, bad, false, false},
		{"bad checksum: accepted", Behavior{BadChecksum: AnswerNormally}, bad, false, true},
		{"read past the flash", Behavior{}, past, true, false},
		{"keyboard flag", Behavior{}, keyboard, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, h := rawOpen(t, Options{}, Config{Mouse: em11(), Behavior: tt.bh})
			got := send(t, h, tt.p)
			switch {
			case tt.nak:
				if len(got) != 1 || got[0].Status() != wire.StatusNAK || !got[0].Valid() || !bytes.Equal(got[0][2:5], tt.p[2:5]) {
					t.Fatalf("replies %v, want one NAK with the request's header", got)
				}
			case tt.applied:
				if len(got) != 1 || got[0] != tt.p {
					t.Fatalf("replies %v, want the echo", got)
				}
			default:
				if len(got) != 0 {
					t.Fatalf("replies %v, want none", got)
				}
			}
			written, _ := d.Image().Byte(0x60)
			if (written == 9) != tt.applied {
				t.Fatalf("flash at 0x60 = %#x", written)
			}
		})
	}
}

func TestShortNAK(t *testing.T) {
	read := withChecksum(wire.Packet{8, 0, 0x3f, 0xfc, 10})
	for _, short := range []bool{false, true} {
		_, _, h := rawOpen(t, Options{}, Config{Mouse: em11(), Behavior: Behavior{ShortNAK: short}})
		got := send(t, h, read)
		if len(got) != 1 || got[0].Status() != wire.StatusNAK || !got[0].Valid() || !bytes.Equal(got[0][:4], []byte{8, 1, 0x3f, 0xfc}) {
			t.Fatalf("short %v: replies %v, want one NAK with the command and address", short, got)
		}
		want := byte(10)
		if short {
			want = 0
		}
		if got[0][4] != want {
			t.Fatalf("short %v: the NAK echoes length %d, want %d", short, got[0][4], want)
		}
	}
}

func TestLatency(t *testing.T) {
	arrivals := func(seed uint64) []time.Duration {
		clock := NewManualClock()
		lat := Latency{Receiver: 3 * time.Millisecond, Mouse: 12 * time.Millisecond, Jitter: 4 * time.Millisecond}
		_, _, h := rawOpen(t, Options{Seed: seed, Clock: clock}, Config{Mouse: em11(), Latency: lat})
		var out []time.Duration
		for _, p := range []wire.Packet{
			wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil),
			wire.MustBuild(wire.Mouse, wire.CmdFWVersion, 0, nil),
			wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil),
		} {
			if got := send(t, h, p); len(got) != 0 {
				t.Fatalf("reply %v before any time passed", got)
			}
			var waited time.Duration
			for len(drain(h)) == 0 {
				if waited > time.Second {
					t.Fatalf("no reply to %v", p)
				}
				clock.Advance(time.Millisecond)
				waited += time.Millisecond
			}
			out = append(out, waited)
		}
		return out
	}
	a := arrivals(3)
	for i, lo := range []time.Duration{3, 12, 3} {
		lo *= time.Millisecond
		if a[i] < lo || a[i] > lo+4*time.Millisecond {
			t.Errorf("reply %d after %v, want %v plus up to 4ms", i, a[i], lo)
		}
	}
	if b := arrivals(3); !slices.Equal(a, b) {
		t.Errorf("the same seed gave %v and %v", a, b)
	}
}

func TestLateReplyAfterUnplugIsDropped(t *testing.T) {
	clock := NewManualClock()
	_, d, h := rawOpen(t, Options{Clock: clock}, Config{Mouse: em11(), Latency: Latency{Mouse: 10 * time.Millisecond}})
	send(t, h, wire.MustBuild(wire.Mouse, wire.CmdFWVersion, 0, nil))
	d.Unplug()
	clock.Advance(time.Second)
	if got := drain(h); len(got) != 0 {
		t.Fatalf("replies %v after unplug", got)
	}
}

// FuzzRespond checks that whatever arcctl writes, every reply is a valid frame
// that answers it.
func FuzzRespond(f *testing.F) {
	f.Add([]byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x4a})
	f.Add([]byte{8, 0, 0, 0x60, 0x0a, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xdb})
	f.Add([]byte{7, 0, 0, 0, 2, 4, 0x51, 0, 0, 0, 0, 0, 0, 0, 0, 0xef})
	b := New(Options{})
	defer b.Close()
	d, err := b.Add(Config{Mouse: em11(), Behavior: Behavior{DoubleWrite: true}})
	if err != nil {
		f.Fatal(err)
	}
	h, err := b.open(d.Candidates()[1])
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var p wire.Packet
		copy(p[:], raw)
		for _, rep := range send(t, h, p) {
			if !rep.Valid() || !wire.Match(p, rep) {
				t.Fatalf("reply %v to %v", rep, p)
			}
		}
	})
}
