package session_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

var _ session.Resetter = (*session.Session)(nil)

// h7 is a test-only record of hardware test H7 for the em11 fixture's model
// and firmware; verified.json holds none.
var h7 = catalog.Verification{Model: "7B04", Feature: hidio.ResetFeature, Firmware: "v1.05", Stage: "H7", Date: "2026-10-01"}

var (
	resetRanges = []flash.Extent{{Addr: 0, Len: mouse.AddrEndEeprom}, {Addr: 9504, Len: 256}}
	resetPacket = wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil)
)

const resetReply = 60 * time.Millisecond

// served is what the emulated mouse answers reads of im with: its bytes,
// and 0xFF where it holds none.
func served(t *testing.T, im *flash.Image) *flash.Image {
	t.Helper()
	out, err := flash.FromDump(0, im.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// newResetRig is an EM11 Pro whose writes hold records, and whose packets
// must each pass Edit, or be cmd 9 under Reset.
func newResetRig(t *testing.T, o emu.Options, tweak func(*emu.Config), records catalog.Verifications) *rig {
	t.Helper()
	c := receiver(em11(t))
	if tweak != nil {
		tweak(&c)
	}
	r := newRigOn(t, o, c, checkReset)
	session.SetVerified(&r.writes, records)
	return r
}

func checkReset(t *testing.T, ws []emu.Write) {
	t.Helper()
	for _, w := range ws {
		p := w.Packet
		if err := wire.Edit.Check(p, wire.Mouse); err != nil && (p.Cmd() != wire.CmdClear || wire.Reset.Check(p, wire.Mouse) != nil) {
			t.Errorf("packet %v reached the device: %v", p, err)
		}
	}
}

func fastReset(o *session.Options) { o.Timing.ResetReply = resetReply }

func resetGates() safety.Gates { return safety.Gates{Confirm: safety.ResetPhrase} }

// resets lists the cmd-9 packets that reached the device.
func resets(ws []emu.Write) []int {
	var out []int
	for i, w := range ws {
		if w.Packet.Cmd() == wire.CmdClear {
			if w.Packet != resetPacket {
				panic("a malformed cmd 9 reached the device")
			}
			out = append(out, i)
		}
	}
	return out
}

// Without H7's record of the reset for the mouse's model and firmware, the
// reset and its preflight are refused before any packet: with the records
// compiled from verified.json (none for the emulated firmware v0.42), and
// with records for another firmware or stage.
func TestResetLockedBeforeAnyIO(t *testing.T) {
	other := h7
	other.Firmware = "v1.06"
	h6 := h7
	h6.Stage = "H6"
	tests := []struct {
		name     string
		firmware emu.Version
		records  catalog.Verifications
	}{
		{"compiled records", emu.Version{Minor: 0x42}, nil},
		{"another firmware", emu.Version{Major: 1, Minor: 5}, catalog.Verifications{other}},
		{"another stage", emu.Version{Major: 1, Minor: 5}, catalog.Verifications{h6}},
		{"no records", emu.Version{Major: 1, Minor: 5}, catalog.Verifications{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResetRig(t, emu.Options{}, func(c *emu.Config) { c.Mouse.Firmware = tt.firmware }, tt.records)
			s, sn := r.ready(fastReset)
			n := len(r.dev.Writes())
			err := s.PreflightReset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
			var pe *safety.PreflightError
			if !errors.As(err, &pe) || !errors.Is(err, safety.ErrResetLocked) {
				t.Fatalf("PreflightReset = %v", err)
			}
			out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
			if !errors.Is(err, safety.ErrResetLocked) || out.Sent || out.Backup != "" || out.Run != "" {
				t.Fatalf("Reset = %+v, %v", out, err)
			}
			if got := len(r.dev.Writes()); got != n {
				t.Fatalf("%d packets went out after the refusal", got-n)
			}
			if list := backupsOf(t, r, sn); len(list) != 0 {
				t.Errorf("backups %v", list)
			}
			if st := journalOf(t, r, sn); len(st.Runs) != 0 {
				t.Errorf("journal runs %v", st.Runs)
			}
			if sn := s.Snapshot(); sn.Policy != wire.ReadOnly || sn.State != session.Ready {
				t.Errorf("after the refusal: %v, %v", sn.Policy, sn.State)
			}
		})
	}
}

// With the gate open, a reset takes a full backup read afresh, sends one
// cmd 9 and nothing else, reads the whole configuration again, and compares
// it with the backup; the journal records it as a reset run that no revert
// reaches past.
func TestResetSendsOnceThenReloadsAndDiffs(t *testing.T) {
	r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
	s, sn := r.ready(fastReset)
	before := served(t, r.dev.Image())
	out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Sent || out.Run == "" || out.Reply != safety.ReplyAck || out.Verdict != safety.VerdictChanged || len(out.Changed) == 0 ||
		len(out.Unread) != 0 || out.DryRun || len(out.Packets) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	after := served(t, r.dev.Image())
	if c, u := safety.ResetDiff(before, after, resetRanges); !slices.Equal(c, out.Changed) || len(u) != 0 {
		t.Fatalf("changed %v, the device changed %v", out.Changed, c)
	}
	sameBytes(t, out.Before, before, resetRanges...)
	sameBytes(t, out.After, after, resetRanges...)

	ws := r.dev.Writes()
	at := resets(ws)
	if len(at) != 1 {
		t.Fatalf("%d cmd-9 packets reached the device", len(at))
	}
	for _, w := range slices.Concat(ws[:at[0]], ws[at[0]+1:]) {
		if err := wire.ReadOnly.Check(w.Packet, wire.Mouse); err != nil {
			t.Errorf("besides cmd 9, %v reached the device", w.Packet)
		}
	}
	full := chunks(resetRanges...)
	if got := logical(ws[:at[0]]); len(got) < len(full) || !slices.Equal(got[len(got)-len(full):], full) {
		t.Error("the reads right before cmd 9 are not a full backup read afresh")
	}
	if got := logical(ws[at[0]+1:]); len(got) < len(full) || !slices.Equal(got[:len(full)], full) {
		t.Error("the reads right after cmd 9 are not the whole configuration")
	}

	f, err := backup.Load(out.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if f.Label != session.LabelBeforeReset || !f.Full || len(f.Missing()) != 0 {
		t.Fatalf("backup: label %q, full %v, missing %v", f.Label, f.Full, f.Missing())
	}
	sameBytes(t, f.Image(), before, resetRanges...)

	sn = await(t, s, "the reload after the reset", idle)
	if sn.Policy != wire.ReadOnly || sn.Stats.Foreign != 0 {
		t.Fatalf("after the reset: policy %v, %d foreign replies", sn.Policy, sn.Stats.Foreign)
	}
	sameBytes(t, sn.Image, after, flash.Extent{Addr: 0, Len: 256})
	st := journalOf(t, r, sn)
	run := st.Find(out.Run)
	if run == nil || run.Kind != safety.KindReset || !run.Complete || run.Reset.Backup != out.Backup || run.Reset.Reply != safety.ReplyAck ||
		run.Reset.Verdict != safety.VerdictChanged || !slices.Equal(run.Reset.Changed, out.Changed) || !st.Clean() || st.Last != nil {
		t.Fatalf("journal: run %+v, last %v", run, st.Last)
	}
	if sn.Journal == nil || sn.Journal.Last != nil || len(sn.Journal.Open) != 0 {
		t.Fatalf("journal state %+v", sn.Journal)
	}
}

// The reset is its own run in the journal, after which a revert has nothing
// to undo: the backup from before the reset is the way back.
func TestResetEndsWhatRevertUndoes(t *testing.T) {
	r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
	s, sn := r.ready(fastReset)
	p := planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1})
	if _, err := s.Apply(ctxT(t), p, allow(p), nil); err != nil {
		t.Fatal(err)
	}
	sn = await(t, s, "the reload after the apply", idle)
	if _, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates()); err != nil {
		t.Fatal(err)
	}
	await(t, s, "the reload after the reset", idle)
	if _, err := s.Revert(ctxT(t), safety.Gates{AllowUntested: true, Confirm: "write untested"}, nil); !errors.Is(err, safety.ErrNothing) {
		t.Fatalf("Revert after a reset = %v", err)
	}
	if n := len(resets(r.dev.Writes())); n != 1 {
		t.Fatalf("%d cmd-9 packets", n)
	}
}

// Without a reply the reset is not sent again: the session waits
// Timing.ResetReply, then reads the mouse and compares. The mouse may reset
// silently, or never get the packet.
func TestResetWithoutAReply(t *testing.T) {
	t.Run("silent reset", func(t *testing.T) {
		r := newResetRig(t, emu.Options{}, func(c *emu.Config) { c.Behavior.ClearSilent = true }, catalog.Verifications{h7})
		s, sn := r.ready(fastReset)
		var mu sync.Mutex
		var seen []session.Snapshot
		stop := make(chan struct{})
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			for {
				select {
				case <-stop:
					return
				case <-s.Changed():
					mu.Lock()
					seen = append(seen, *s.Snapshot())
					mu.Unlock()
				}
			}
		}()
		started := time.Now()
		out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
		close(stop)
		<-watched
		if err != nil || out.Reply != safety.ReplyNone || out.Verdict != safety.VerdictChanged {
			t.Fatalf("outcome %+v, %v", out, err)
		}
		if time.Since(started) < resetReply {
			t.Error("the reset did not wait for its reply")
		}
		if n := len(resets(r.dev.Writes())); n != 1 {
			t.Fatalf("%d cmd-9 packets", n)
		}
		mu.Lock()
		defer mu.Unlock()
		reset := false
		for _, x := range seen {
			if x.Policy == wire.Reset {
				reset = true
				if x.State != session.Applying || x.Progress.Job != "reset" {
					t.Errorf("under Reset: state %v, progress %+v", x.State, x.Progress)
				}
			}
			if x.Policy == wire.Edit {
				t.Error("the reset switched the guard to Edit")
			}
		}
		if !reset {
			t.Error("no snapshot showed the Reset policy while the reset waited for its reply")
		}
	})
	t.Run("the mouse slept", func(t *testing.T) {
		r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
		s, sn := r.ready(fastReset)
		r.dev.Inject(emu.Fault{Cmd: wire.CmdClear, Action: emu.Asleep})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitFor)
			defer cancel()
			if _, err := session.Await(ctx, s, in(session.Offline)); err == nil {
				r.dev.Wake()
			}
		}()
		out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
		if err != nil || out.Reply != safety.ReplyNone || out.Verdict != safety.VerdictUnchanged || len(out.Changed) != 0 {
			t.Fatalf("outcome %+v, %v", out, err)
		}
		if n := len(resets(r.dev.Writes())); n != 1 {
			t.Fatalf("%d cmd-9 packets", n)
		}
		if !slices.Equal(r.dev.Image().Bytes(), r.start.Bytes()) {
			t.Error("the mouse changed")
		}
	})
}

// Whatever happens to the packet or its reply, cmd 9 reaches the mouse at
// most once, the guard is read-only afterwards, and a late reply is not
// taken for another program's.
func TestResetNeverResent(t *testing.T) {
	tests := []struct {
		name      string
		fault     emu.Fault
		delivered int
		reply     safety.Reply
		verdict   safety.ResetVerdict
		unchecked bool
	}{
		{"no reply", emu.Fault{Cmd: wire.CmdClear, Action: emu.Drop}, 1, safety.ReplyNone, safety.VerdictChanged, false},
		{"NAK", emu.Fault{Cmd: wire.CmdClear, Action: emu.NAK}, 1, safety.ReplyNAK, safety.VerdictChanged, false},
		{"late reply", emu.Fault{Cmd: wire.CmdClear, Action: emu.Late, Delay: 3 * resetReply}, 1, safety.ReplyNone, safety.VerdictChanged, false},
		{"two replies", emu.Fault{Cmd: wire.CmdClear, Action: emu.Duplicate}, 1, safety.ReplyAck, safety.VerdictChanged, false},
		{"one OS error", emu.Fault{Cmd: wire.CmdClear, Action: emu.Fail, Times: 1}, 0, safety.ReplyError, safety.VerdictUnchanged, false},
		{"taken, then an OS error", emu.Fault{Cmd: wire.CmdClear, Action: emu.Taken}, 1, safety.ReplyError, safety.VerdictChanged, false},
		{"unplugged", emu.Fault{Cmd: wire.CmdClear, Action: emu.Unplug}, 0, safety.ReplyError, safety.VerdictUnchecked, true},
		{"write hangs", emu.Fault{Cmd: wire.CmdClear, Action: emu.Hang}, 0, safety.ReplyError, safety.VerdictUnchecked, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResetRig(t, emu.Options{Watchdog: 50 * time.Millisecond}, nil, catalog.Verifications{h7})
			s, sn := r.ready(fastReset)
			r.dev.Inject(tt.fault)
			out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
			if tt.unchecked != errors.Is(err, safety.ErrResetUnchecked) || !tt.unchecked && err != nil {
				t.Fatalf("Reset = %v", err)
			}
			if !out.Sent || out.Reply != tt.reply || out.Verdict != tt.verdict {
				t.Fatalf("outcome: sent %v, reply %v, verdict %v, err %v", out.Sent, out.Reply, out.Verdict, err)
			}
			if n := len(resets(r.dev.Writes())); n != tt.delivered {
				t.Fatalf("%d cmd-9 packets reached the device, want %d", n, tt.delivered)
			}
			if !tt.unchecked {
				await(t, s, "the reload after the reset", idle)
				time.Sleep(4 * resetReply)
				sn := s.Snapshot()
				if sn.State != session.Ready || sn.Stats.Foreign != 0 || sn.Policy != wire.ReadOnly {
					t.Fatalf("after the reset: %v, %d foreign replies, policy %v", sn.State, sn.Stats.Foreign, sn.Policy)
				}
				if tt.name == "late reply" && sn.Stats.Late == 0 {
					t.Error("the late reply was not counted as late")
				}
			}
			if n := len(resets(r.dev.Writes())); n != tt.delivered {
				t.Fatalf("%d cmd-9 packets reached the device later", n)
			}
			if s.Snapshot().Policy != wire.ReadOnly {
				t.Error("the guard is not read-only")
			}
		})
	}
}

// A dry run checks everything but the phrase and the journal, and returns
// the packet without sending it or taking a backup.
func TestResetDryRun(t *testing.T) {
	r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
	s, sn := r.ready(fastReset)
	out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), safety.Gates{DryRun: true})
	if err != nil || !out.DryRun || !slices.Equal(out.Packets, []wire.Packet{resetPacket}) || out.Sent || out.Backup != "" {
		t.Fatalf("dry run %+v, %v", out, err)
	}
	if len(resets(r.dev.Writes())) != 0 || len(backupsOf(t, r, sn)) != 0 || len(journalOf(t, r, sn).Runs) != 0 {
		t.Fatal("the dry run sent, saved or journaled something")
	}
}

// Each failed check refuses the reset before its backup and its packet.
func TestResetPreflightRefusals(t *testing.T) {
	other := func(sn *session.Snapshot) (session.Snapshot, *byte) {
		x := *sn
		x.Identity.MID = 6
		return x, profileOf(sn)
	}
	profile := func(sn *session.Snapshot) (session.Snapshot, *byte) { return *sn, ptr(byte(3)) }
	same := func(sn *session.Snapshot) (session.Snapshot, *byte) { return *sn, profileOf(sn) }
	tests := []struct {
		name   string
		target func(*session.Snapshot) (session.Snapshot, *byte)
		gates  safety.Gates
		setup  func(*rig)
		want   error
	}{
		{"no phrase", same, safety.Gates{Confirm: "write untested"}, nil, safety.ErrConfirm},
		{"another mouse", other, resetGates(), nil, safety.ErrIdentity},
		{"another profile", profile, resetGates(), nil, safety.ErrProfile},
		{"another program", same, resetGates(), func(r *rig) { r.dev.AddClient(emu.Client{PID: 4242, Process: "Google Chrome Helper"}) }, safety.ErrForeignClient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
			s, sn := r.ready(fastReset)
			if tt.setup != nil {
				tt.setup(r)
			}
			x, p := tt.target(sn)
			if err := s.PreflightReset(ctxT(t), x.Identity, p, tt.gates); !errors.Is(err, tt.want) {
				t.Fatalf("PreflightReset = %v", err)
			}
			out, err := s.Reset(ctxT(t), x.Identity, p, tt.gates)
			if !errors.Is(err, tt.want) || out.Sent || out.Backup != "" {
				t.Fatalf("Reset = %+v, %v", out, err)
			}
			if len(resets(r.dev.Writes())) != 0 || len(backupsOf(t, r, sn)) != 0 {
				t.Fatal("a refused reset sent or saved something")
			}
		})
	}
}

// A backup that could not read every range is saved but refuses the reset.
func TestResetNeedsACompleteBackup(t *testing.T) {
	r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
	s, sn := r.ready(fastReset)
	r.dev.Inject(emu.Fault{Cmd: wire.CmdRead, Match: func(p wire.Packet) bool { return p.Addr() == 9604 }, Action: emu.NAK})
	out, err := s.Reset(ctxT(t), sn.Identity, profileOf(sn), resetGates())
	if !errors.Is(err, safety.ErrPartialBackup) || out.Sent || out.Backup == "" {
		t.Fatalf("Reset = %+v, %v", out, err)
	}
	if len(resets(r.dev.Writes())) != 0 {
		t.Fatal("cmd 9 went out after a partial backup")
	}
	if list := backupsOf(t, r, sn); len(list) != 1 || list[0].Path != out.Backup || len(list[0].File.Missing()) == 0 {
		t.Fatalf("backups %+v", list)
	}
}

// Once cmd 9 went out, cancelling or stopping only ends the wait for the
// mouse: the reset is recorded as unchecked, and the session loads the
// mouse when it wakes.
func TestResetStoppedWhileTheMouseSleeps(t *testing.T) {
	for _, how := range []string{"cancel", "abort"} {
		t.Run(how, func(t *testing.T) {
			r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
			s, sn := r.ready(fastReset)
			sent := false
			r.dev.Inject(emu.Fault{Action: emu.Asleep, Times: 1, Match: func(p wire.Packet) bool {
				if p.Cmd() == wire.CmdClear {
					sent = true
				}
				return sent && p.Cmd() == wire.CmdRead
			}})
			ctx, cancel := context.WithCancel(ctxT(t))
			defer cancel()
			go func() {
				wctx, wcancel := context.WithTimeout(context.Background(), waitFor)
				defer wcancel()
				if _, err := session.Await(wctx, s, in(session.Offline)); err == nil {
					if how == "cancel" {
						cancel()
					} else {
						s.Abort()
					}
				}
			}()
			out, err := s.Reset(ctx, sn.Identity, profileOf(sn), resetGates())
			if !errors.Is(err, safety.ErrResetUnchecked) || !out.Sent || out.Verdict != safety.VerdictUnchecked || out.Run == "" {
				t.Fatalf("Reset = %+v, %v", out, err)
			}
			if how == "cancel" && !errors.Is(err, context.Canceled) || how == "abort" && !errors.Is(err, safety.ErrAborted) {
				t.Errorf("the error does not say why: %v", err)
			}
			r.dev.Wake()
			sn = await(t, s, "the load once the mouse woke", idle)
			run := journalOf(t, r, sn).Find(out.Run)
			if run == nil || !run.Ended || run.Complete || run.Reset.Verdict != safety.VerdictUnchecked || run.Err == "" {
				t.Fatalf("journal run %+v", run)
			}
			if n := len(resets(r.dev.Writes())); n != 1 {
				t.Fatalf("%d cmd-9 packets", n)
			}
		})
	}
}

// A process that ended between the reset's packet and its check leaves the
// run unsettled. The next session to load the mouse refuses writes and
// resets until it has read the mouse again, compared it with the backup
// the run names and journaled the verdict.
func TestInterruptedResetIsCheckedOnTheNextLoad(t *testing.T) {
	r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
	s, sn := r.ready(fastReset)
	cp, err := s.Backup(ctxT(t), true)
	if err != nil {
		t.Fatal(err)
	}
	path, err := r.store.Save(cp, session.LabelBeforeReset)
	if err != nil {
		t.Fatal(err)
	}
	j, err := safety.OpenJournal(r.writes.Journal, sn.Identity)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	run, _, err := safety.SendReset(ctxT(t), j, sn.Identity, profileOf(sn), path, resetPacket, func(context.Context, wire.Packet) (wire.Packet, error) {
		return resetPacket, r.dev.Store(mouse.AddrCurrentDPI, []byte{0x00, 0x55})
	})
	if err != nil {
		t.Fatal(err)
	}

	s2, stop := r.run(fastReset)
	defer stop()
	blocked := await(t, s2, "the unsettled reset", func(sn *session.Snapshot) bool {
		return sn.Journal != nil && len(sn.Journal.Resets) == 1
	})
	if blocked.Journal.Resets[0].ID != run {
		t.Fatalf("unsettled %v", blocked.Journal.Resets)
	}
	sn = await(t, s2, "the check of the reset", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Journal != nil && len(sn.Journal.Resets) == 0
	})
	got := journalOf(t, r, sn).Find(run)
	if got == nil || !got.Ended || got.Reset.Verdict != safety.VerdictChanged || !slices.Contains(got.Reset.Changed, flash.Extent{Addr: mouse.AddrCurrentDPI, Len: 2}) {
		t.Fatalf("journal run %+v, reset %+v", got, got.Reset)
	}
	if !journalOf(t, r, sn).Clean() {
		t.Error("the journal is not clean after the check")
	}
	if n := len(resets(r.dev.Writes())); n != 0 {
		t.Fatalf("%d cmd-9 packets", n)
	}
}

// While the check has not run, the preflight of a write and of a reset
// names the unsettled reset.
func TestUnsettledResetBlocksWrites(t *testing.T) {
	r := newResetRig(t, emu.Options{}, nil, catalog.Verifications{h7})
	_, sn := r.ready(fastReset)
	j, err := safety.OpenJournal(r.writes.Journal, sn.Identity)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, _, err := safety.SendReset(ctxT(t), j, sn.Identity, profileOf(sn), "/no/such/backup.json", resetPacket,
		func(context.Context, wire.Packet) (wire.Packet, error) { return resetPacket, nil }); err != nil {
		t.Fatal(err)
	}
	st, err := j.Status()
	if err != nil {
		t.Fatal(err)
	}
	f := safety.Facts{Journal: st}
	if err := safety.Preflight(safety.KindApply, planOn(t, sn, sn.Image, mouse.SetCurrent{Stage: 1}), f, safety.Gates{AllowUntested: true}); !errors.Is(err, safety.ErrNotClean) {
		t.Errorf("apply preflight: %v", err)
	}
	s2, stop := r.run(fastReset)
	defer stop()
	sn = await(t, s2, "the check of the reset", func(sn *session.Snapshot) bool {
		return idle(sn) && sn.Journal != nil && len(sn.Journal.Resets) == 0
	})
	st = journalOf(t, r, sn)
	got := st.Runs[0]
	if !got.Ended || got.Reset.Verdict != safety.VerdictUnchecked || !strings.Contains(got.Err, "the backup taken before it could not be read") || !st.Clean() {
		t.Fatalf("a reset whose backup is gone: %+v", got)
	}
}
