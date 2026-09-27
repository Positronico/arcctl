package session_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func TestPushRereads(t *testing.T) {
	tests := []struct {
		f1, f2 byte
		want   []flash.Extent
	}{
		{0x01, 0, []flash.Extent{{Addr: 4, Len: 2}}},
		{0x02, 0, []flash.Extent{{Addr: 0, Len: 2}}},
		{0x08, 0, []flash.Extent{{Addr: 76, Len: 8}}},
		{0x20, 0, []flash.Extent{{Addr: 160, Len: 7}}},
		{0, 0x01, []flash.Extent{{Addr: 10, Len: 2}}},
		{0, 0x02, []flash.Extent{{Addr: 169, Len: 2}}},
		{0, 0x04, []flash.Extent{{Addr: 171, Len: 2}}},
		{0, 0x08, []flash.Extent{{Addr: 233, Len: 6}}},
		{0, 0x10, []flash.Extent{{Addr: 225, Len: 2}}},
		{0x29, 0x18, []flash.Extent{{Addr: 4, Len: 2}, {Addr: 76, Len: 8}, {Addr: 160, Len: 7}, {Addr: 233, Len: 6}, {Addr: 225, Len: 2}}},
		{0x90, 0xE0, nil},
	}
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	for i, tt := range tests {
		before := len(d.Writes())
		d.Push(tt.f1, tt.f2)
		await(t, s, "the re-reads", func(sn *session.Snapshot) bool { return sn.Stats.Pushes == i+1 && idle(sn) })
		if got := reads(d.Writes()[before:]); !slices.Equal(got, tt.want) {
			t.Errorf("push %02x %02x read %v, want %v", tt.f1, tt.f2, got, tt.want)
		}
	}
}

func TestDPIButtonPushUpdatesTheImage(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	sn := await(t, s, "ready", idle)
	was := mouse.Decode(sn.Model, sn.Image).Current.Value
	must(t, d.PressDPI())
	sn = await(t, s, "the new stage", func(sn *session.Snapshot) bool {
		return idle(sn) && mouse.Decode(sn.Model, sn.Image).Current.Value != was
	})
	sameBytes(t, sn.Image, d.Image(), flash.Extent{Addr: mouse.AddrCurrentDPI, Len: 2})
}

func TestBatteryPush(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	d.SetBattery(emu.Battery{Level: 42, Charging: true, MilliVolts: 4000})
	d.Push(0x40, 0)
	sn := await(t, s, "the new level", func(sn *session.Snapshot) bool { return sn.Battery != nil && sn.Battery.Level == 42 })
	if !sn.Battery.Charging || sn.Battery.MilliVolts != 4000 {
		t.Errorf("Battery = %+v", sn.Battery)
	}
}

func TestProfileSwitchReloads(t *testing.T) {
	b := newBus(t, emu.Options{})
	c := receiver(em11(t))
	c.Latency.Mouse = time.Millisecond
	d := add(t, b, c)
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)

	backup := make(chan error, 1)
	go func() {
		_, err := s.Backup(ctxT(t), true)
		backup <- err
	}()
	await(t, s, "the backup", func(sn *session.Snapshot) bool { return sn.Progress.Job == "backup" && sn.Progress.Done > 5 })
	before := len(d.Writes())
	must(t, d.SwitchProfile(1))
	if err := <-backup; !errors.Is(err, session.ErrProfileChanged) {
		t.Errorf("backup across a profile switch = %v", err)
	}
	sn := await(t, s, "the reload", func(sn *session.Snapshot) bool { return idle(sn) && sn.Profile.Value == 1 })
	after := d.Writes()[before:]
	if n := count(after, wire.CmdGetProfile); n != 1 {
		t.Errorf("cmd 14 sent %d times after the switch", n)
	}
	if got := reads(after); !slices.Equal(got[len(got)-len(workingSet()):], workingSet()) {
		t.Errorf("the reload did not read the working set: %v", got)
	}
	sameBytes(t, sn.Image, d.Image(), workingSet()...)
}
