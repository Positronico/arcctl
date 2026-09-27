package safety_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

var resetPacket = wire.MustBuild(wire.Mouse, wire.CmdClear, 0, nil)

func h7(model, firmware string) catalog.Verification {
	return catalog.Verification{Model: model, Feature: hidio.ResetFeature, Firmware: firmware, Stage: "H7", Date: "2026-10-01"}
}

// The gate opens only on H7's record of the reset for exactly the model and
// the firmware.
func TestResetGate(t *testing.T) {
	em11, _ := catalog.ByKey("7B04")
	em25, _ := catalog.ByKey("7B01")
	var kb *catalog.Model
	for _, m := range catalog.Models() {
		if m.Family == catalog.FamilyKeyboard {
			kb = m
			break
		}
	}
	other := h7("7B04", "v1.05")
	other.Stage = "H6"
	feature := h7("7B04", "v1.05")
	feature.Feature = "dpi.value"
	tests := []struct {
		name     string
		m        *catalog.Model
		firmware string
		vs       catalog.Verifications
		open     bool
	}{
		{"no model", nil, "v1.05", catalog.Verifications{h7("7B04", "v1.05")}, false},
		{"keyboard", kb, "v1.05", catalog.Verifications{h7(kb.Key, "v1.05")}, false},
		{"firmware unknown", em11, "", catalog.Verifications{h7("7B04", "")}, false},
		{"no records", em11, "v1.05", nil, false},
		{"another firmware", em11, "v1.06", catalog.Verifications{h7("7B04", "v1.05")}, false},
		{"another model", em25, "v1.05", catalog.Verifications{h7("7B04", "v1.05")}, false},
		{"another stage", em11, "v1.05", catalog.Verifications{other}, false},
		{"another feature", em11, "v1.05", catalog.Verifications{feature}, false},
		{"H7 record", em11, "v1.05", catalog.Verifications{feature, h7("7B04", "v1.05")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := safety.ResetGate(tt.m, tt.firmware, tt.vs)
			if tt.open != (err == nil) || !tt.open && !errors.Is(err, safety.ErrResetLocked) {
				t.Fatalf("ResetGate = %v, want open %v", err, tt.open)
			}
			if v, ok := safety.ResetVerification(tt.m, tt.firmware, tt.vs); ok != tt.open || ok && v != h7("7B04", "v1.05") {
				t.Fatalf("ResetVerification = %+v, %v", v, ok)
			}
		})
	}
}

// Release builds open the reset only where verified.json holds H7's record
// of it; the firmware the emulator tests run never has one, and on the day
// this was written no firmware had one, so the reset stays unreachable
// until H7 passes and records it.
func TestResetLockedUntilH7Records(t *testing.T) {
	vs := catalog.VerifiedStages()
	records := 0
	firmwares := []string{"v0.42", "v1.00", "v1.05", "v1.07"}
	for _, v := range vs {
		firmwares = append(firmwares, v.Firmware)
		if v.Feature == hidio.ResetFeature && v.Stage == "H7" {
			records++
		}
	}
	for _, m := range catalog.Models() {
		for _, fw := range firmwares {
			_, recorded := safety.ResetVerification(m, fw, vs)
			err := safety.ResetGate(m, fw, vs)
			if (err == nil) != (recorded && m.Family == catalog.FamilyMouse) {
				t.Errorf("%s %s: gate %v, H7 record %v", m.Key, fw, err, recorded)
			}
		}
		if err := safety.ResetGate(m, "v0.42", vs); !errors.Is(err, safety.ErrResetLocked) {
			t.Errorf("%s v0.42: %v", m.Key, err)
		}
	}
	t.Logf("verified.json holds %d H7 record(s) of the factory reset", records)
}

// The guard switches to Reset only with a grant its gate admits, and then
// lets exactly one cmd 9 through; cmd 7 never passes under Reset.
func TestGuardResetGate(t *testing.T) {
	g := hidio.NewGuard(wire.Mouse)
	vs := catalog.Verifications{h7("7B04", "v1.05")}
	refused := []struct {
		name  string
		grant []hidio.Grant
	}{
		{"no grant", nil},
		{"two grants", []hidio.Grant{{Model: "7B04", Firmware: "v1.05", Verified: vs}, {Model: "7B04", Firmware: "v1.05", Verified: vs}}},
		{"no record", []hidio.Grant{{Model: "7B04", Firmware: "v1.05"}}},
		{"release records", []hidio.Grant{{Model: "7B04", Firmware: "v0.42", Verified: catalog.VerifiedStages()}}},
		{"another firmware", []hidio.Grant{{Model: "7B04", Firmware: "v1.06", Verified: vs}}},
		{"another model", []hidio.Grant{{Model: "7B06", Firmware: "v1.05", Verified: vs}}},
	}
	for _, tt := range refused {
		if err := g.Set(wire.Reset, tt.name, tt.grant...); !errors.Is(err, hidio.ErrForbidden) || g.Policy() != wire.ReadOnly {
			t.Fatalf("%s: Set(Reset) = %v, policy %v", tt.name, err, g.Policy())
		}
		if err := g.Check(resetPacket); !errors.Is(err, hidio.ErrForbidden) {
			t.Fatalf("%s: cmd 9 passed a refused Reset", tt.name)
		}
	}
	for round := range 2 {
		if err := g.Set(wire.Reset, "reset", hidio.Grant{Model: "7B04", Firmware: "v1.05", Verified: vs}); err != nil {
			t.Fatal(err)
		}
		if err := g.Check(wire.MustBuild(wire.Mouse, wire.CmdWrite, 0x60, []byte{1, 1, 0, 0x53})); !errors.Is(err, hidio.ErrForbidden) {
			t.Fatalf("round %d: cmd 7 under Reset = %v", round, err)
		}
		if err := g.Check(wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)); err != nil {
			t.Fatalf("round %d: cmd 3 under Reset = %v", round, err)
		}
		if err := g.Check(resetPacket); err != nil {
			t.Fatalf("round %d: the first cmd 9 = %v", round, err)
		}
		if err := g.Check(resetPacket); !errors.Is(err, hidio.ErrForbidden) {
			t.Fatalf("round %d: a second cmd 9 = %v", round, err)
		}
		if err := g.Set(wire.ReadOnly, "done"); err != nil {
			t.Fatal(err)
		}
		if err := g.Check(resetPacket); !errors.Is(err, hidio.ErrForbidden) {
			t.Fatalf("round %d: cmd 9 under ReadOnly = %v", round, err)
		}
	}
}

func resetJournal(t *testing.T) (*safety.Journal, string) {
	t.Helper()
	root := t.TempDir()
	j, err := safety.OpenJournal(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j, root
}

// SendReset fsyncs the run and "sending" before its one call to send, then
// the reply; a NAK or no reply is a reply, not an error.
func TestSendReset(t *testing.T) {
	nak := fmt.Errorf("cmd 9: %w", wire.ErrNAK)
	tests := []struct {
		name  string
		err   error
		reply safety.Reply
		fails bool
	}{
		{"ack", nil, safety.ReplyAck, false},
		{"nak", nak, safety.ReplyNAK, false},
		{"none", fmt.Errorf("%w: nothing", safety.ErrTimeout), safety.ReplyNone, false},
		{"write error", errors.New("general error (0xe00002bc)"), safety.ReplyError, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, _ := resetJournal(t)
			calls := 0
			run, reply, err := safety.SendReset(context.Background(), j, identity, ptr(byte(0)), "/b/before.json", resetPacket,
				func(_ context.Context, p wire.Packet) (wire.Packet, error) {
					calls++
					lines := journalLines(t, j.Path())
					if n := len(lines); n != 2 || lines[0]["kind"] != "reset" || lines[1]["type"] != "send" || lines[1]["state"] != "sending" {
						t.Errorf("journal before the packet: %v", lines)
					}
					if p != resetPacket {
						t.Errorf("sent %v", p)
					}
					return resetPacket, tt.err
				})
			if calls != 1 || run == "" || reply != tt.reply || (err != nil) != tt.fails {
				t.Fatalf("calls %d, run %q, reply %v, err %v", calls, run, reply, err)
			}
			lines := journalLines(t, j.Path())
			if last := lines[len(lines)-1]; last["state"] != "sent" || last["reply"] != tt.reply.String() {
				t.Fatalf("last line %v", last)
			}
			st, err := j.Status()
			if err != nil {
				t.Fatal(err)
			}
			r := st.Find(run)
			if r == nil || r.Kind != safety.KindReset || r.Reset == nil || !r.Reset.Sending || r.Reset.Reply != tt.reply ||
				r.Reset.Backup != "/b/before.json" || r.Reset.Packet != resetPacket || r.Open() || len(st.Open) != 0 ||
				len(st.Unsettled) != 1 || st.Clean() {
				t.Fatalf("run %+v, reset %+v", r, r.Reset)
			}
		})
	}
}

// Nothing goes out when the journal cannot record the reset first, and a
// packet other than cmd 9 is refused.
func TestSendResetNeedsTheJournal(t *testing.T) {
	j, _ := resetJournal(t)
	safety.SetSync(j, func(*os.File) error { return errors.New("disk full") })
	send := func(context.Context, wire.Packet) (wire.Packet, error) {
		t.Fatal("sent without a journal")
		return wire.Packet{}, nil
	}
	if run, _, err := safety.SendReset(context.Background(), j, identity, nil, "b", resetPacket, send); run != "" || !errors.Is(err, safety.ErrJournal) {
		t.Fatalf("run %q, err %v", run, err)
	}
	j2, _ := resetJournal(t)
	if run, _, err := safety.SendReset(context.Background(), j2, identity, nil, "b", wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil), send); run != "" || err == nil {
		t.Fatalf("cmd 3 as a reset: run %q, err %v", run, err)
	}
}

// The end of a reset records the verdict; a reset that went out ends what
// revert can undo, and it is never an unfinished run.
func TestResetRunInTheJournal(t *testing.T) {
	f := newFixture(t, setup{})
	p := f.plan(pairChange(t))
	if _, err := f.apply(p, nil); err != nil {
		t.Fatal(err)
	}
	st, err := f.j.Status()
	if err != nil || st.Last == nil {
		t.Fatalf("status %+v, %v", st, err)
	}
	send := func(context.Context, wire.Packet) (wire.Packet, error) { return resetPacket, nil }
	run, _, err := safety.SendReset(context.Background(), f.j, identity, ptr(byte(0)), "b", resetPacket, send)
	if err != nil {
		t.Fatal(err)
	}
	changed := []flash.Extent{{Addr: 4, Len: 2}, {Addr: 96, Len: 4}}
	unread := []flash.Extent{{Addr: 9504, Len: 10}}
	if err := f.j.EndReset(run, safety.VerdictChanged, changed, unread, nil); err != nil {
		t.Fatal(err)
	}
	st, err = f.j.Status()
	if err != nil {
		t.Fatal(err)
	}
	r := st.Find(run)
	if st.Last != nil || !st.Clean() || !r.Ended || !r.Complete || r.Reset.Verdict != safety.VerdictChanged ||
		!slices.Equal(r.Reset.Changed, changed) || !slices.Equal(r.Reset.Unread, unread) {
		t.Fatalf("last %v, clean %v, run %+v, reset %+v", st.Last, st.Clean(), r, r.Reset)
	}

	run2, _, err := safety.SendReset(context.Background(), f.j, identity, ptr(byte(0)), "b2", resetPacket, send)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.j.EndReset(run2, safety.VerdictUnchecked, nil, nil, errors.New("the mouse slept")); err != nil {
		t.Fatal(err)
	}
	st, err = f.j.Status()
	if err != nil {
		t.Fatal(err)
	}
	if r := st.Find(run2); r.Complete || r.Reset.Verdict != safety.VerdictUnchecked || !strings.Contains(r.Err, "slept") {
		t.Fatalf("unchecked reset %+v, %+v", r, r.Reset)
	}
}

// A reset line the parser cannot place makes the journal corrupt; a reset
// run whose "sending" never reached the disk sent nothing and bars nothing.
func TestResetJournalParse(t *testing.T) {
	key := identity.Key()
	dev := fmt.Sprintf(`{"key":%q,"vid":"260d","pid":"1282","cid":"7b","mid":4}`, key)
	run := `{"v":1,"type":"run","run":"r/1","time":"2026-09-27T00:00:00Z","kind":"reset","device":` + dev + `,"backup":"b","packet":"` + fmt.Sprintf("%x", resetPacket[:]) + `"}`
	bad := map[string]string{
		"reset with ops":    strings.Replace(run, `"backup"`, `"ops":1,"backup"`, 1) + "\n",
		"reset, no backup":  strings.Replace(run, `"backup":"b",`, "", 1) + "\n",
		"reset, no packet":  strings.Replace(run, `,"packet":"`+fmt.Sprintf("%x", resetPacket[:])+`"`, "", 1) + "\n",
		"reply before send": run + "\n" + `{"type":"send","run":"r/1","time":"2026-09-27T00:00:00Z","state":"sent","reply":"ack"}` + "\n",
		"unknown reply":     run + "\n" + `{"type":"send","run":"r/1","time":"2026-09-27T00:00:00Z","state":"sending"}` + "\n" + `{"type":"send","run":"r/1","time":"2026-09-27T00:00:00Z","state":"sent","reply":"maybe"}` + "\n",
		"bad verdict":       run + "\n" + `{"type":"end","run":"r/1","time":"2026-09-27T00:00:00Z","result":"complete","verdict":"so-so"}` + "\n",
	}
	for name, b := range bad {
		if _, err := safety.Parse(key, []byte(b)); !errors.Is(err, safety.ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	st, err := safety.Parse(key, []byte(run+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := st.Find("r/1"); r == nil || r.Reset.Sending || r.Open() {
		t.Fatalf("run %+v", r)
	}
}

func TestResetDiff(t *testing.T) {
	before, after := flash.New(), flash.New()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(before.Set(0, []byte{1, 2, 3, 4, 5, 6, 7, 8}))
	must(after.Set(0, []byte{1, 9, 9, 4, 5, 6}))
	must(before.Set(20, []byte{1}))
	must(after.Set(20, []byte{2}))
	must(after.Set(30, []byte{7}))
	changed, unread := safety.ResetDiff(before, after, []flash.Extent{{Addr: 0, Len: 21}, {Addr: 30, Len: 1}})
	if want := []flash.Extent{{Addr: 1, Len: 2}, {Addr: 20, Len: 1}}; !slices.Equal(changed, want) {
		t.Errorf("changed %v, want %v", changed, want)
	}
	if want := []flash.Extent{{Addr: 6, Len: 2}}; !slices.Equal(unread, want) {
		t.Errorf("unread %v, want %v", unread, want)
	}
	if c, u := safety.ResetDiff(before, before, []flash.Extent{{Addr: 0, Len: 64}}); len(c)+len(u) != 0 {
		t.Errorf("a backup differs from itself: %v %v", c, u)
	}
}

// The preflight of a reset is an apply's without the record checks, plus
// the firmware and the phrase.
func TestPreflightReset(t *testing.T) {
	facts := func() safety.Facts {
		return safety.Facts{Identity: identity, Profile: ptr(byte(0)), Firmware: "v1.05", Journal: &safety.Status{}}
	}
	g := safety.Gates{Confirm: " reset "}
	if err := safety.PreflightReset(identity, ptr(byte(0)), "v1.05", facts(), g); err != nil {
		t.Fatal(err)
	}
	other := identity
	other.MID = 6
	open := &safety.Status{Open: []*safety.Run{{ID: "x/1"}}}
	tests := []struct {
		name  string
		dev   func() safety.Facts
		gates safety.Gates
		want  error
	}{
		{"no phrase", facts, safety.Gates{Confirm: "write untested"}, safety.ErrConfirm},
		{"firmware", func() safety.Facts { f := facts(); f.Firmware = "v1.06"; return f }, g, safety.ErrFirmware},
		{"identity", func() safety.Facts { f := facts(); f.Identity = other; return f }, g, safety.ErrIdentity},
		{"profile", func() safety.Facts { f := facts(); f.Profile = ptr(byte(1)); return f }, g, safety.ErrProfile},
		{"journal", func() safety.Facts { f := facts(); f.Journal = open; return f }, g, safety.ErrNotClean},
		{"client", func() safety.Facts { f := facts(); f.Clients = []safety.Client{{PID: 7, Name: "Chrome"}}; return f }, g, safety.ErrForeignClient},
		{"offline", func() safety.Facts { f := facts(); f.Online = safety.ErrOffline; return f }, g, safety.ErrOffline},
	}
	for _, tt := range tests {
		err := safety.PreflightReset(identity, ptr(byte(0)), "v1.05", tt.dev(), tt.gates)
		var pe *safety.PreflightError
		if !errors.As(err, &pe) || !errors.Is(err, tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	dry := facts()
	dry.Clients, dry.Journal = []safety.Client{{PID: 7, Name: "Chrome"}}, open
	if err := safety.PreflightReset(identity, ptr(byte(0)), "v1.05", dry, safety.Gates{DryRun: true}); err != nil {
		t.Errorf("dry run: %v", err)
	}
}

func TestResetBackupGate(t *testing.T) {
	if err := safety.ResetBackupGate("b.json", nil); err != nil {
		t.Fatal(err)
	}
	if err := safety.ResetBackupGate("", nil); !errors.Is(err, safety.ErrNoFullBackup) {
		t.Errorf("no backup: %v", err)
	}
	err := safety.ResetBackupGate("b.json", []flash.Extent{{Addr: 9504, Len: 10}})
	if !errors.Is(err, safety.ErrPartialBackup) || !strings.Contains(err.Error(), "9504") {
		t.Errorf("partial: %v", err)
	}
}

// A process that ends between the packet and EndReset leaves the run
// unsettled: the journal is not clean, every write and reset is refused,
// and the run settles with its end entry, which any later process may
// write.
func TestInterruptedResetIsUnsettled(t *testing.T) {
	j, root := resetJournal(t)
	run, _, err := safety.SendReset(context.Background(), j, identity, ptr(byte(0)), "/b/before.json", resetPacket,
		func(context.Context, wire.Packet) (wire.Packet, error) { return resetPacket, nil })
	if err != nil || run == "" {
		t.Fatalf("setup: run %q, err %v", run, err)
	}
	st, err := safety.Load(j.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if st.Clean() || len(st.Unsettled) != 1 || st.Unsettled[0].ID != run || len(st.Open) != 0 {
		t.Fatalf("clean %v, unsettled %v, open %d", st.Clean(), st.Unsettled, len(st.Open))
	}
	f := safety.Facts{Journal: st}
	err = safety.PreflightReset(identity, ptr(byte(0)), "", f, safety.Gates{Confirm: safety.ResetPhrase})
	if !errors.Is(err, safety.ErrNotClean) || !strings.Contains(err.Error(), "ended before arcctl checked what it did") {
		t.Errorf("preflight: %v", err)
	}
	later, err := safety.OpenJournal(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer later.Close()
	if err := later.EndReset(run, safety.VerdictChanged, []flash.Extent{{Addr: 4, Len: 2}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err = safety.Load(j.Dir())
	if err != nil || !st.Clean() || st.Runs[0].Reset.Verdict != safety.VerdictChanged {
		t.Fatalf("after the end entry: %v, clean %v", err, st != nil && st.Clean())
	}
}
