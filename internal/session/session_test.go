package session_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func TestConnectLoadsTheWorkingSet(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	sn := await(t, s, "ready", idle)

	if sn.Device == nil || sn.Device.Interface != 1 {
		t.Errorf("Device = %+v, want interface 1", sn.Device)
	}
	if sn.Model == nil || sn.Model.Key != "7B04" {
		t.Errorf("Model = %v, want 7B04", sn.Model)
	}
	if want := (session.Handshake{CID: 0x7B, MID: 4}); sn.Handshake == nil || *sn.Handshake != want {
		t.Errorf("Handshake = %+v, want %+v", sn.Handshake, want)
	}
	if want := (session.Versions{Receiver: "v1.02", Mouse: "v1.05"}); sn.Versions != want {
		t.Errorf("Versions = %+v, want %+v", sn.Versions, want)
	}
	if sn.Battery == nil || sn.Battery.Level != 80 || sn.Battery.MilliVolts != 3900 || sn.Battery.Charging {
		t.Errorf("Battery = %+v", sn.Battery)
	}
	if want := (session.Probe{Asked: true, Supported: true}); sn.Profile != want {
		t.Errorf("Profile = %+v, want %+v", sn.Profile, want)
	}
	if want := (session.Probe{Asked: true, Supported: true, Value: 1}); sn.LongRange != want {
		t.Errorf("LongRange = %+v, want %+v", sn.LongRange, want)
	}
	wantID := plan.Identity{CID: 0x7B, MID: 4, Addr: [3]byte{0x11, 0x22, 0x33}, VID: 0x260D, PID: 0x1282}
	if sn.Identity != wantID || sn.Identity.Key() != "260d-1282-7b04" {
		t.Errorf("Identity = %+v (%s), want %+v", sn.Identity, sn.Identity.Key(), wantID)
	}
	if !sn.Online || len(sn.Unread) != 0 || sn.Policy != wire.ReadOnly || sn.Stats.Foreign != 0 {
		t.Errorf("Online %v, Unread %v, Policy %v, Foreign %d", sn.Online, sn.Unread, sn.Policy, sn.Stats.Foreign)
	}

	ws := d.Writes()
	var probed []int
	for _, w := range ws {
		if w.Packet.Cmd() == wire.CmdOnline && !slices.Contains(probed, w.Interface) {
			probed = append(probed, w.Interface)
		}
		if w.Interface == 0 && w.Packet.Cmd() != wire.CmdOnline {
			t.Errorf("interface 0 got %v", w.Packet)
		}
	}
	if len(probed) != 2 {
		t.Errorf("probed interfaces %v, want both", probed)
	}
	if got, want := reads(ws), workingSet(); !slices.Equal(got, want) {
		t.Errorf("reads\n got %v\nwant %v", got, want)
	}
	for c, n := range map[wire.Cmd]int{wire.CmdHandshake: 1, wire.CmdRxVersion: 1, wire.CmdGetProfile: 1, wire.CmdFWVersion: 1, wire.CmdBattery: 1, wire.CmdGetLongRange: 1} {
		if got := count(ws, c); got != n {
			t.Errorf("%v sent %d times, want %d", c, got, n)
		}
	}

	sameBytes(t, sn.Image, d.Image(), workingSet()...)
	if sn.Image.Known(flash.Extent{Addr: 6987, Len: 1}) {
		t.Error("byte 6987 was read")
	}
	if e, _ := mouse.ShortcutExtent(12); sn.Image.Known(e) {
		t.Error("an unbound shortcut slot was read")
	}
	cfg := mouse.Decode(sn.Model, sn.Image)
	if cfg.Macros[macroSlot] == nil || cfg.Macros[macroSlot].Name != testMacro.Name || cfg.MacroClass[macroBound] != flash.SlotEmpty {
		t.Errorf("macros: slot %d %+v, slot %d class %v", macroSlot, cfg.Macros[macroSlot], macroBound, cfg.MacroClass[macroBound])
	}
	for slot := 2; slot <= 5; slot++ {
		if cfg.ShortcutClass[slot] != flash.SlotValid {
			t.Errorf("shortcut %d is %v", slot, cfg.ShortcutClass[slot])
		}
	}
}

func TestStartsOfflineAndLoadsOnWake(t *testing.T) {
	b := newBus(t, emu.Options{})
	m := em11(t)
	m.Asleep = true
	d := add(t, b, receiver(m))
	s := start(t, b, session.Options{})
	sn := await(t, s, "offline", in(session.Offline))
	if sn.Online || sn.Handshake != nil || sn.Versions.Receiver != "v1.02" {
		t.Errorf("offline snapshot: online %v, handshake %v, versions %+v", sn.Online, sn.Handshake, sn.Versions)
	}
	time.Sleep(3 * fast().Offline)
	if n := count(d.Writes(), wire.CmdHandshake); n != 0 {
		t.Errorf("%d handshakes sent to a sleeping mouse", n)
	}
	d.Wake()
	sn = await(t, s, "ready", idle)
	if sn.Handshake == nil || sn.Image == nil {
		t.Fatal("no handshake or image after the wake")
	}
	sameBytes(t, sn.Image, d.Image(), workingSet()...)
}

func TestNoReceiverUntilPlugged(t *testing.T) {
	b := newBus(t, emu.Options{})
	s := start(t, b, session.Options{})
	await(t, s, "no receiver", in(session.NoReceiver))
	add(t, b, receiver(em11(t)))
	await(t, s, "ready", idle)
}

func TestUnplugAndReplug(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	d.Unplug()
	sn := await(t, s, "no receiver", in(session.NoReceiver))
	if sn.Image != nil || sn.Handshake != nil || sn.Device != nil {
		t.Errorf("state kept after the unplug: %+v", sn)
	}
	if _, err := s.Backup(ctxT(t), false); !errors.Is(err, session.ErrNotConnected) {
		t.Errorf("Backup while unplugged = %v, want ErrNotConnected", err)
	}
	d.Plug()
	await(t, s, "ready again", idle)
}

func TestChoosing(t *testing.T) {
	b := newBus(t, emu.Options{})
	first := add(t, b, receiver(em11(t)))
	other := em11(t)
	other.Model = model(t, "7B05")
	other.Image = nil
	second := add(t, b, receiver(other))
	s := start(t, b, session.Options{})
	sn := await(t, s, "choosing", in(session.Choosing))
	if len(sn.Answers) != 2 {
		t.Fatalf("Answers = %+v, want both receivers", sn.Answers)
	}
	for i, want := range []string{"7B04", "7B05"} {
		a := sn.Answers[i]
		if !a.Online || a.Handshake == nil || a.Model == nil || a.Model.Key != want || a.Candidate.Interface != 1 {
			t.Errorf("answer %d = %+v, want an online %s on interface 1", i, a, want)
		}
	}
	if err := s.Choose(ctxT(t), "emu:9/IOUSBHostInterface@1"); !errors.Is(err, session.ErrNoSuchDevice) {
		t.Errorf("Choose(unknown) = %v", err)
	}
	if err := s.Choose(ctxT(t), sn.Answers[1].Candidate.Path); err != nil {
		t.Fatal(err)
	}
	sn = await(t, s, "ready", idle)
	if sn.Model.Key != "7B05" || sn.Device.Path != second.Candidates()[1].Path {
		t.Errorf("attached %v on %v", sn.Model.Key, sn.Device)
	}
	if len(sn.Answers) != 0 {
		t.Errorf("Answers still listed: %v", sn.Answers)
	}
	for _, c := range b.Clients() {
		if c.Path == first.Candidates()[1].Path {
			t.Errorf("the unchosen receiver is still open: %+v", c)
		}
	}
	if err := s.Choose(ctxT(t), sn.Device.Path); !errors.Is(err, session.ErrNotChoosing) {
		t.Errorf("Choose when ready = %v", err)
	}
}

func TestDeviceOptionSkipsTheChoice(t *testing.T) {
	b := newBus(t, emu.Options{})
	add(t, b, receiver(em11(t)))
	d := add(t, b, receiver(em11(t)))
	path := d.Candidates()[1].Path
	s := start(t, b, session.Options{Device: path})
	sn := await(t, s, "ready", idle)
	if sn.Device.Path != path {
		t.Errorf("attached %s, want %s", sn.Device.Path, path)
	}
}

func TestBlockedAtStart(t *testing.T) {
	tests := []struct {
		name  string
		set   func(*emu.Device, bool)
		state session.State
	}{
		{"locked", (*emu.Device).SetLocked, session.Locked},
		{"denied", (*emu.Device).SetDenied, session.NeedsPermission},
		{"seized", (*emu.Device).SetSeized, session.Seized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBus(t, emu.Options{})
			d := add(t, b, receiver(em11(t)))
			tt.set(d, true)
			s := start(t, b, session.Options{})
			sn := await(t, s, tt.state.String(), in(tt.state))
			if sn.Err == nil {
				t.Error("no error in the snapshot")
			}
			tt.set(d, false)
			await(t, s, "ready", idle)
		})
	}
}

func TestBlockedWhileReady(t *testing.T) {
	tests := []struct {
		name  string
		set   func(*emu.Device, bool)
		state session.State
	}{
		{"locked", (*emu.Device).SetLocked, session.Locked},
		{"denied", (*emu.Device).SetDenied, session.NeedsPermission},
		{"seized", (*emu.Device).SetSeized, session.Seized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBus(t, emu.Options{})
			d := add(t, b, receiver(em11(t)))
			tm := fast()
			tm.Online = 20 * time.Millisecond
			s := start(t, b, session.Options{Timing: tm})
			before := await(t, s, "ready", idle)
			tt.set(d, true)
			await(t, s, tt.state.String(), in(tt.state))
			tt.set(d, false)
			sn := await(t, s, "ready", in(session.Ready))
			if sn.Image != before.Image || sn.Device.Path != before.Device.Path {
				t.Error("the session did not keep its device and image")
			}
		})
	}
}

func TestNothingAnswers(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdOnline, Times: 6, Action: emu.Drop})
	s := start(t, b, session.Options{})
	sn := await(t, s, "no receiver", func(sn *session.Snapshot) bool { return errors.Is(sn.Err, session.ErrNoAnswer) })
	if sn.State != session.NoReceiver {
		t.Errorf("State = %v", sn.State)
	}
	await(t, s, "ready", idle)
}

func TestAsleepAtTheHandshake(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdHandshake, Times: 1, Action: emu.Asleep})
	s := start(t, b, session.Options{})
	await(t, s, "offline", func(sn *session.Snapshot) bool {
		return sn.State == session.Offline && count(d.Writes(), wire.CmdHandshake) > 0
	})
	if sn := s.Snapshot(); sn.Handshake != nil || sn.Stats.FailedTries < 5 {
		t.Errorf("handshake %v, failed tries %d", sn.Handshake, sn.Stats.FailedTries)
	}
	d.Wake()
	await(t, s, "ready", idle)
}

func TestTrustedAddressKeysTheIdentity(t *testing.T) {
	b := newBus(t, emu.Options{})
	add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{TrustAddress: true})
	if sn := await(t, s, "ready", idle); sn.Identity.Key() != "7b04-112233" {
		t.Errorf("Key = %s", sn.Identity.Key())
	}
}

func TestPreflight(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	denied := make(chan bool, 1)
	denied <- true
	errDenied := errors.New("input monitoring denied")
	s := start(t, b, session.Options{Preflight: func() error {
		select {
		case v := <-denied:
			if v {
				return errDenied
			}
		default:
		}
		return nil
	}})
	sn := await(t, s, "needs permission", in(session.NeedsPermission))
	if !errors.Is(sn.Err, errDenied) {
		t.Errorf("Err = %v", sn.Err)
	}
	await(t, s, "ready", idle)
	if n := count(d.Writes(), wire.CmdOnline); n == 0 {
		t.Error("never probed after the grant")
	}
}

func TestHandshakeOutcomes(t *testing.T) {
	tests := []struct {
		name  string
		mouse func(*emu.Mouse)
		state session.State
		err   error
	}{
		{"no mouse known", func(m *emu.Mouse) { m.Model, m.CID, m.MID = nil, 0, 0 }, session.Offline, nil},
		{"unknown model", func(m *emu.Mouse) { m.Model, m.CID, m.MID = nil, 0x55, 9 }, session.Unknown, session.ErrUnknownModel},
		{"charging base", func(m *emu.Mouse) { m.Conn = 6 }, session.Unknown, session.ErrChargingBase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBus(t, emu.Options{})
			m := em11(t)
			tt.mouse(m)
			d := add(t, b, receiver(m))
			s := start(t, b, session.Options{})
			await(t, s, "a handshake", func(sn *session.Snapshot) bool { return count(d.Writes(), wire.CmdHandshake) > 0 })
			sn := await(t, s, tt.state.String(), func(sn *session.Snapshot) bool {
				return sn.State == tt.state && sn.Link != session.Handshaking
			})
			if !errors.Is(sn.Err, tt.err) && !(tt.err == nil && sn.Err == nil) {
				t.Errorf("Err = %v, want %v", sn.Err, tt.err)
			}
			if sn.Image != nil || count(d.Writes(), wire.CmdRead) != 0 {
				t.Error("flash was read")
			}
			if tt.state != session.Unknown {
				return
			}
			if _, err := s.Backup(ctxT(t), true); !errors.Is(err, session.ErrUnsupported) {
				t.Errorf("Backup = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestKeyboardProbeRetriesWithoutTheFlag(t *testing.T) {
	b := newBus(t, emu.Options{})
	c := receiver(em11(t))
	c.PID = 0xFA0A
	d := add(t, b, c)
	if cl := d.Candidates()[0].Class; cl != catalog.ClassKeyboard {
		t.Fatalf("PID class %v", cl)
	}
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	var flagged, plain int
	for _, w := range d.Writes() {
		if w.Packet.Target() == wire.Keyboard {
			flagged++
		} else {
			plain++
		}
	}
	if flagged == 0 || plain == 0 {
		t.Errorf("%d flagged and %d plain packets, want both", flagged, plain)
	}
}

func TestStallRescans(t *testing.T) {
	b := newBus(t, emu.Options{Watchdog: 30 * time.Millisecond})
	d := add(t, b, receiver(em11(t)))
	d.Inject(emu.Fault{Cmd: wire.CmdRxVersion, Times: 1, Action: emu.Hang})
	t.Cleanup(d.Release)
	s := start(t, b, session.Options{})
	sn := await(t, s, "stalled", in(session.Stalled))
	if sn.Stalls != 1 {
		t.Errorf("Stalls = %d", sn.Stalls)
	}
	sn = await(t, s, "ready", idle)
	if sn.Stalls != 1 {
		t.Errorf("Stalls = %d after the rescan", sn.Stalls)
	}
}

func TestDifferentMouseAfterWake(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	tm := fast()
	tm.Online = 20 * time.Millisecond
	s := start(t, b, session.Options{Timing: tm})
	await(t, s, "ready", idle)
	d.Sleep()
	await(t, s, "offline", in(session.Offline))
	other := em11(t)
	other.Model, other.Image, other.Asleep = model(t, "7B05"), nil, true
	must(t, d.Pair(other))
	d.Wake()
	sn := await(t, s, "the other mouse", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Model != nil && sn.Model.Key == "7B05"
	})
	sameBytes(t, sn.Image, d.Image(), flash.Extent{Addr: 0, Len: 256})
}

func TestReloadAndApply(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)
	before := len(reads(d.Writes()))
	if err := s.Reload(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if got := len(reads(d.Writes())) - before; got != len(workingSet()) {
		t.Errorf("reload read %d chunks, want %d", got, len(workingSet()))
	}
	n := len(d.Writes())
	if err := s.Apply(ctxT(t), plan.Plan{}, nil); !errors.Is(err, session.ErrReadOnly) {
		t.Errorf("Apply = %v", err)
	}
	if len(d.Writes()) != n {
		t.Error("Apply wrote")
	}
}

func TestBackupAndRead(t *testing.T) {
	b := newBus(t, emu.Options{})
	d := add(t, b, receiver(em11(t)))
	s := start(t, b, session.Options{})
	await(t, s, "ready", idle)

	quick, err := s.Backup(ctxT(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if quick.Full || len(quick.Missing) != 0 || quick.Device.CID != 0x7B {
		t.Errorf("session backup %+v", quick)
	}
	sameBytes(t, quick.Image, d.Image(), workingSet()...)

	before := len(d.Writes())
	full, err := s.Backup(ctxT(t), true)
	if err != nil {
		t.Fatal(err)
	}
	ranges := []flash.Extent{{Addr: 0, Len: 6987}, {Addr: 9504, Len: 256}}
	if !full.Full || len(full.Missing) != 0 {
		t.Errorf("full backup: full %v, missing %v", full.Full, full.Missing)
	}
	sameBytes(t, full.Image, d.Image(), ranges...)
	got := reads(d.Writes()[before:])
	loaded := workingSet()
	for _, e := range got {
		if slices.ContainsFunc(loaded, e.Overlaps) {
			t.Errorf("the full backup read %v again", e)
		}
	}
	sn := s.Snapshot()
	sameBytes(t, sn.Image, d.Image(), ranges...)

	before = len(d.Writes())
	c, err := s.Read(ctxT(t), flash.Extent{Addr: 10000, Len: 25}, flash.Extent{Addr: 0, Len: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := chunks(flash.Extent{Addr: 10000, Len: 25}); !slices.Equal(reads(d.Writes()[before:]), want) {
		t.Errorf("Read sent %v, want %v", reads(d.Writes()[before:]), want)
	}
	sameBytes(t, c.Image, d.Image(), flash.Extent{Addr: 10000, Len: 25})
	if _, err := s.Read(ctxT(t), flash.Extent{Addr: flash.Size - 1, Len: 2}); !errors.Is(err, flash.ErrRange) {
		t.Errorf("Read past the end = %v", err)
	}
}
