package emu_test

import (
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
)

func TestManualClock(t *testing.T) {
	c := emu.NewManualClock()
	start := c.Now()
	var got []string
	at := func(name string) func() {
		return func() { got = append(got, name+"@"+c.Now().Sub(start).String()) }
	}
	c.AfterFunc(20*time.Millisecond, at("b"))
	c.AfterFunc(10*time.Millisecond, at("a"))
	c.AfterFunc(20*time.Millisecond, at("c"))
	stopped := c.AfterFunc(15*time.Millisecond, at("x"))
	c.AfterFunc(5*time.Millisecond, func() {
		at("d")()
		c.AfterFunc(10*time.Millisecond, at("e"))
	})
	if !stopped.Stop() || stopped.Stop() {
		t.Fatal("Stop should succeed once")
	}
	c.Advance(18 * time.Millisecond)
	if want := []string{"d@5ms", "a@10ms", "e@15ms"}; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if c.Pending() != 2 || c.Now().Sub(start) != 18*time.Millisecond {
		t.Fatalf("pending %d at %v", c.Pending(), c.Now().Sub(start))
	}
	c.Advance(2 * time.Millisecond)
	if want := []string{"d@5ms", "a@10ms", "e@15ms", "b@20ms", "c@20ms"}; !slices.Equal(got, want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	if c.Pending() != 0 {
		t.Fatalf("pending %d", c.Pending())
	}
}
