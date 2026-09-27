package emu

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/wire"
)

// The receiver takes one radio request at a time: one that reaches it before
// the reply to the previous one went out drops that reply. cmd 3 does not.
func TestOneRadioRequestAtATime(t *testing.T) {
	read, err := wire.BuildRead(wire.Mouse, 0x60, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, one := range []bool{true, false} {
		clock := NewManualClock()
		c := Config{Mouse: em11(), Latency: Latency{Receiver: 3 * time.Millisecond, Mouse: 12 * time.Millisecond}}
		c.Behavior.OneAtATime = one
		b, d, h := rawOpen(t, Options{Clock: clock}, c)
		other, err := b.open(d.Candidates()[d.answering])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { other.Close() })
		send(t, h, read)
		clock.Advance(5 * time.Millisecond)
		send(t, other, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil))
		clock.Advance(time.Millisecond)
		send(t, other, wire.MustBuild(wire.Mouse, wire.CmdBattery, 0, nil))
		clock.Advance(50 * time.Millisecond)
		var got []wire.Cmd
		for _, p := range drain(h) {
			got = append(got, p.Cmd())
		}
		want := []wire.Cmd{wire.CmdOnline, wire.CmdBattery}
		if !one {
			want = []wire.Cmd{wire.CmdOnline, wire.CmdRead, wire.CmdBattery}
		}
		if !slices.Equal(got, want) {
			t.Errorf("one at a time %v: %v reached the first client, want %v", one, got, want)
		}
		drain(other)
		for range 2 {
			if err := h.WriteRaw(read); err != nil {
				t.Fatal(err)
			}
			clock.Advance(50 * time.Millisecond)
		}
		if n := len(drain(h)); n != 2 {
			t.Errorf("one at a time %v: %d replies to two reads in turn", one, n)
		}
	}
}

// Loss drops radio replies at its rate, the same ones for the same seed. The
// requests still reach the mouse, and cmd 3 is never lost.
func TestLoss(t *testing.T) {
	const n = 4000
	run := func(seed uint64) []int {
		c := Config{Mouse: em11()}
		c.Behavior.Loss = 0.025
		_, d, h := rawOpen(t, Options{Seed: seed}, c)
		p := wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 1, 0, 0x53})
		var lost []int
		for i := range n {
			if len(send(t, h, p)) == 0 {
				lost = append(lost, i)
			}
		}
		_ = d.mouse.image.Set(0x60, []byte{0, 0, 0, 0x55})
		send(t, h, p)
		for range 200 {
			if len(send(t, h, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil))) != 1 {
				t.Fatal("cmd 3 lost")
			}
		}
		if got, _ := d.mouse.image.Get(extentOf(p)); !slices.Equal(got, p[5:9]) {
			t.Errorf("the mouse holds % x after a write whose reply may be lost", got)
		}
		return lost
	}
	a := run(11)
	if len(a) < 60 || len(a) > 140 {
		t.Errorf("%d of %d replies lost, want about 100", len(a), n)
	}
	if b := run(11); !slices.Equal(a, b) {
		t.Error("the same seed lost other replies")
	}
}

func extentOf(p wire.Packet) flash.Extent {
	e, _ := extent(p)
	return e
}

func TestCurve(t *testing.T) {
	c := Curve{{0, 10 * time.Millisecond}, {0.5, 20 * time.Millisecond}, {1, 60 * time.Millisecond}}
	for _, tt := range []struct {
		u    float64
		want time.Duration
	}{{0, 10 * time.Millisecond}, {0.25, 15 * time.Millisecond}, {0.5, 20 * time.Millisecond}, {0.75, 40 * time.Millisecond}, {1, 60 * time.Millisecond}} {
		if got := c.at(tt.u); got != tt.want {
			t.Errorf("at(%v) = %v, want %v", tt.u, got, tt.want)
		}
	}
	if got := (Curve{}).at(0.5); got != 0 {
		t.Errorf("empty curve gives %v", got)
	}
}

// The EM11 Pro curves give back the quantiles H0 measured.
func TestEM11ProLatency(t *testing.T) {
	l := EM11ProLatency()
	rng := rand.New(rand.NewPCG(1, 2))
	for _, tt := range []struct {
		name     string
		c        Curve
		p50, p99 [2]time.Duration
	}{
		{"receiver", l.ReceiverCurve, [2]time.Duration{ms(3), ms(4)}, [2]time.Duration{ms(6), ms(8)}},
		{"radio", l.MouseCurve, [2]time.Duration{ms(11.5), ms(13.5)}, [2]time.Duration{ms(38), ms(50)}},
	} {
		d := make([]time.Duration, 20000)
		for i := range d {
			d[i] = tt.c.at(rng.Float64())
		}
		slices.Sort(d)
		p50, p99 := d[len(d)/2], d[len(d)*99/100]
		if p50 < tt.p50[0] || p50 > tt.p50[1] || p99 < tt.p99[0] || p99 > tt.p99[1] {
			t.Errorf("%s: p50 %v, p99 %v", tt.name, p50, p99)
		}
	}
}
