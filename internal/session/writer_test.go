package session_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// rig is an emulated EM11 Pro with a journal folder and a backup store that
// outlive the sessions a test starts on it, as they outlive arcctl runs.
type rig struct {
	t      *testing.T
	bus    *emu.Bus
	dev    *emu.Device
	store  backup.Store
	writes session.Writes
	start  *flash.Image
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigOn(t, emu.Options{}, receiver(em11(t)), checkEdit)
}

// newRigOn is a rig on c, whose packets check vets when the test ends.
func newRigOn(t *testing.T, o emu.Options, c emu.Config, check func(*testing.T, []emu.Write)) *rig {
	t.Helper()
	b := newBus(t, o)
	d, err := b.Add(c)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, bus: b, dev: d, start: d.Image()}
	r.store = backup.Store{Root: t.TempDir(), Tool: "arcctl test", Source: backup.SourceEmulator}
	r.writes = session.Writes{
		Journal:  t.TempDir(),
		Backups:  r.store,
		Lock:     func() error { return nil },
		Executor: safety.Options{OfflineWait: 500 * time.Millisecond, LockWait: 500 * time.Millisecond, Poll: 5 * time.Millisecond},
	}
	t.Cleanup(func() { check(t, d.Writes()) })
	return r
}

// checkEdit fails on any packet the Edit policy refuses for the mouse.
func checkEdit(t *testing.T, ws []emu.Write) {
	t.Helper()
	for _, w := range ws {
		if err := wire.Edit.Check(w.Packet, wire.Mouse); err != nil {
			t.Errorf("packet %v reached the device: %v", w.Packet, err)
		}
	}
}

// run starts a session on the rig. stop ends it, as quitting arcctl does.
func (r *rig) run(tweak func(*session.Options)) (s *session.Session, stop func()) {
	r.t.Helper()
	w := r.writes
	opt := session.Options{Devices: r.bus, Clients: clientsOf(r.bus), Timing: fast(), Writes: &w}
	if tweak != nil {
		tweak(&opt)
	}
	s = session.New(opt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != context.Canceled {
				r.t.Errorf("Run = %v, want context.Canceled", err)
			}
		})
	}
	r.t.Cleanup(stop)
	return s, stop
}

// ready starts a session and waits for its load.
func (r *rig) ready(tweak func(*session.Options)) (*session.Session, *session.Snapshot) {
	r.t.Helper()
	s, _ := r.run(tweak)
	return s, await(r.t, s, "ready", idle)
}

// cmd7 lists the cmd-7 packets that reached the device.
func (r *rig) cmd7() []wire.Packet { return cmd7Of(r.dev.Writes()) }

// logicalCmd7 is cmd7 with each resend left out.
func (r *rig) logicalCmd7() []wire.Packet { return cmd7Of(emu.Logical(r.dev.Writes())) }

func cmd7Of(ws []emu.Write) []wire.Packet {
	var out []wire.Packet
	for _, w := range ws {
		if w.Packet.Cmd() == wire.CmdWrite {
			out = append(out, w.Packet)
		}
	}
	return out
}

func profileOf(sn *session.Snapshot) *byte {
	if !sn.Profile.Supported {
		return nil
	}
	v := sn.Profile.Value
	return &v
}

// planOn plans edits on im the way the TUI does, from the snapshot's
// identity, profile and firmware.
func planOn(t *testing.T, sn *session.Snapshot, im *flash.Image, edits ...mouse.Edit) plan.Plan {
	t.Helper()
	p, err := mouse.PlanEdits(sn.Model, im, edits, mouse.Options{
		Device: sn.Identity, Profile: profileOf(sn), Firmware: sn.Versions.Mouse, Verified: catalog.VerifiedStages(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Ops) == 0 {
		t.Fatal("the plan writes nothing")
	}
	return p
}

var cmdAltTab = keys.Combo{keys.LMeta.Stroke(), keys.LAlt.Stroke(), {Kind: keys.KindKey, Value: 0x2B}}

// mixed changes DPI stage 1 and rewrites the shortcut bound to slot 4, which
// takes the two-phase path.
func mixed(t *testing.T, sn *session.Snapshot) plan.Plan {
	p := planOn(t, sn, sn.Image, mouse.SetDPI{Stage: 0, DPI: 900}, mouse.SetShortcut{Slot: 4, Combo: cmdAltTab})
	if len(p.Ops) != 4 {
		t.Fatalf("%d ops, want the DPI record and the two-phase trio", len(p.Ops))
	}
	return p
}

// allow opens every gate p needs.
func allow(p plan.Plan) safety.Gates {
	return safety.Gates{AllowUntested: true, Experimental: true, Confirm: safety.ConfirmPhrase(p.Ops)}
}

// afterPlan is im with every op of p written.
func afterPlan(t *testing.T, im *flash.Image, p plan.Plan) *flash.Image {
	t.Helper()
	out := im.Clone()
	for _, op := range p.Ops {
		must(t, out.Set(op.Extent.Addr, op.New))
	}
	return out
}

// holds fails unless im holds, over every extent of p, the bytes of want.
func holds(t *testing.T, what string, im, want *flash.Image, p plan.Plan) {
	t.Helper()
	for _, op := range p.Ops {
		g, _ := im.Get(op.Extent)
		w, _ := want.Get(op.Extent)
		if !slices.Equal(g, w) {
			t.Errorf("%s: %v holds % x, want % x", what, op.Extent, g, w)
		}
	}
}

// chunksOf is every cmd 7 p sends, in order.
func chunksOf(p plan.Plan) []wire.Packet {
	var out []wire.Packet
	for _, op := range p.Ops {
		for off := 0; off < len(op.New); off += wire.MaxData {
			out = append(out, wire.MustBuild(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr+off), op.New[off:min(off+wire.MaxData, len(op.New))]))
		}
	}
	return out
}

func journalOf(t *testing.T, r *rig, sn *session.Snapshot) *safety.Status {
	t.Helper()
	j, err := safety.OpenJournal(r.writes.Journal, sn.Identity)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	st, err := j.Status()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func backupsOf(t *testing.T, r *rig, sn *session.Snapshot) []backup.Listing {
	t.Helper()
	list, err := r.store.List(sn.Identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if l.Err != nil {
			t.Fatalf("%s: %v", l.Path, l.Err)
		}
	}
	return list
}
