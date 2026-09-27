package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

func host(o *Options) {
	o.Host = Host{
		Permission: func() string {
			return "Allow Input Monitoring for Ghostty in System Settings > Privacy & Security > Input Monitoring, then quit and reopen Ghostty."
		},
		OpenSettings: func() error { return nil },
	}
}

func console(c safety.Console) func(*Options) {
	return func(o *Options) {
		o.Host.Console = func() (safety.Console, error) { return c, nil }
	}
}

func mode(m Mode) func(*Options) { return func(o *Options) { o.Mode = m } }

var chrome = session.Client{PID: 4242, Name: "Google Chrome Helper"}

func TestBanners(t *testing.T) {
	cable := receiver
	cable.Path, cable.PID = "emu:2/IOUSBHostInterface@0", 0x1283
	tests := []struct {
		name string
		sn   func(t *testing.T) *session.Snapshot
		opts []func(*Options)
		want string
	}{
		{"no-receiver", func(*testing.T) *session.Snapshot { return bare(session.NoReceiver) }, nil,
			"No ProtoArc receiver found"},
		{"no-answer", func(*testing.T) *session.Snapshot {
			sn := bare(session.NoReceiver)
			sn.Err = session.ErrNoAnswer
			return sn
		}, nil, "none answered"},
		{"needs-permission", func(*testing.T) *session.Snapshot {
			sn := bare(session.NeedsPermission)
			sn.Err = errors.New("device access is denied to Ghostty")
			return sn
		}, []func(*Options){host}, "Input Monitoring for Ghostty"},
		{"probing", func(*testing.T) *session.Snapshot { return bare(session.Probing) }, nil, "which one answers"},
		{"choosing", func(*testing.T) *session.Snapshot {
			sn := bare(session.Choosing)
			sn.Answers = []session.Answer{
				{Candidate: receiver, Online: true, Handshake: &session.Handshake{CID: 0x7B, MID: 4}, Model: em11()},
				{Candidate: cable},
			}
			return sn
		}, nil, "Pick one"},
		{"locked-secure-input", func(*testing.T) *session.Snapshot { return attached(session.Locked) },
			[]func(*Options){console(safety.Console{SecureInput: "Ghostty (pid 812)"})}, "held by Ghostty (pid 812)"},
		{"locked-screen", func(*testing.T) *session.Snapshot { return attached(session.Locked) },
			[]func(*Options){console(safety.Console{ScreenLocked: true})}, "Screen locked"},
		{"seized", func(*testing.T) *session.Snapshot {
			sn := attached(session.Seized)
			sn.Clients = []session.Client{{PID: 402, Name: "karabiner_grabber", Seized: true}}
			return sn
		}, nil, "karabiner_grabber (pid 402) exclusively"},
		{"stalled", func(*testing.T) *session.Snapshot {
			sn := attached(session.Stalled)
			sn.Stalls = 2
			return sn
		}, nil, "Replug it"},
		{"offline", func(t *testing.T) *session.Snapshot {
			sn := withState(ready(t), session.Offline)
			sn.Link, sn.Online = session.Offline, false
			sn.Progress = session.Progress{Job: "backup", Done: 120, Total: 725, Paused: true}
			return sn
		}, nil, "Backing up paused at 120 of 725"},
		{"offline-never-loaded", func(*testing.T) *session.Snapshot { return attached(session.Offline) }, nil,
			"2.4 GHz channel"},
		{"handshaking", func(*testing.T) *session.Snapshot {
			sn := attached(session.Handshaking)
			sn.Online = true
			return sn
		}, nil, "which model it is"},
		{"loading", func(t *testing.T) *session.Snapshot {
			sn := withState(ready(t), session.Loading)
			sn.Image, sn.Journal = nil, nil
			sn.Progress = session.Progress{Job: "load", Done: 34, Total: 120}
			return sn
		}, nil, "34 of 120"},
		{"ready", ready, nil, "Buttons tab body"},
		{"ready-warnings", func(t *testing.T) *session.Snapshot {
			sn := ready(t)
			sn.Clients = []session.Client{chrome}
			sn.Unread = sn.Image.KnownExtents()[:1]
			return sn
		}, nil, "has the receiver open"},
		{"ready-dry-run", ready, []func(*Options){mode(ModeDryRun), func(o *Options) { o.Source = "emulator" }}, "[DRY-RUN]"},
		{"ready-replay", ready, []func(*Options){mode(ModeReadOnly), func(o *Options) { o.Source = "replay" }}, "[READ-ONLY]"},
		{"ready-backup", func(t *testing.T) *session.Snapshot {
			sn := ready(t)
			sn.Progress = session.Progress{Job: "backup", Done: 360, Total: 725}
			return sn
		}, nil, "360 of 725"},
		{"conflict", func(t *testing.T) *session.Snapshot {
			sn := withState(ready(t), session.Conflict)
			sn.Clients = []session.Client{chrome}
			sn.ConflictSince, sn.LastForeign = now.Add(-time.Minute), now.Add(-3*time.Second)
			return sn
		}, nil, "last foreign reply came 3 s ago"},
		{"suspected-conflict", func(t *testing.T) *session.Snapshot {
			sn := withState(ready(t), session.SuspectedConflict)
			sn.Clients = []session.Client{chrome}
			return sn
		}, nil, "Replies are going missing"},
		{"recovering", unfinished, nil, "Unfinished write"},
		{"unknown", func(*testing.T) *session.Snapshot {
			sn := attached(session.Unknown)
			sn.Online = true
			sn.Handshake = &session.Handshake{CID: 0x7B, MID: 9}
			sn.Err = session.ErrUnknownModel
			return sn
		}, nil, "Unknown device 0x7B/0x09: read-only"},
		{"charging-base", func(*testing.T) *session.Snapshot {
			sn := attached(session.Unknown)
			sn.Handshake = &session.Handshake{CID: 0x7B, MID: 4, Conn: 6}
			sn.Err = session.ErrChargingBase
			return sn
		}, nil, "charging base"},
		{"keyboard", func(t *testing.T) *session.Snapshot {
			sn := ready(t)
			m, _ := catalog.ByKey("0301")
			sn.Model = m
			sn.Handshake = &session.Handshake{CID: 0x03, MID: 1}
			sn.Image = nil
			return sn
		}, nil, "Keyboards are shown read-only"},
		{"ascii", ready, []func(*Options){func(o *Options) { o.ASCII = true }}, "[1 Buttons]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.sn(t), tc.opts...)
			if s := h.screen(120, 40); !strings.Contains(s, tc.want) {
				t.Errorf("the screen lacks %q:\n%s", tc.want, s)
			}
			h.golden("banner-" + tc.name)
		})
	}
}

// unfinished is a Ready mouse whose journal holds an apply that stopped with
// its only record torn.
func unfinished(t *testing.T) *session.Snapshot {
	sn := withState(ready(t), session.Recovering)
	p, err := mouse.PlanEdits(sn.Model, sn.Image, []mouse.Edit{mouse.SetDPI{Stage: 1, DPI: 1600}}, mouse.Options{Device: sn.Identity})
	if err != nil {
		t.Fatal(err)
	}
	run := &safety.Run{ID: "20260926T115900Z-4242", Kind: safety.KindApply, Device: sn.Identity,
		Started: now.Add(-time.Minute), Err: "no reply"}
	in := &safety.Inspection{Run: run, Image: sn.Image.Clone()}
	for _, op := range p.Ops {
		run.Ops = append(run.Ops, safety.OpRecord{Op: op, State: safety.StateSending})
		found := make([]byte, op.Extent.Len)
		copy(found, []byte{0x12, 0x34, 0x56, 0x78})
		if err := in.Image.Set(op.Extent.Addr, found); err != nil {
			t.Fatal(err)
		}
		in.Extents = append(in.Extents, safety.ExtentState{Extent: op.Extent, Before: op.Old, After: op.New,
			Found: found, Class: safety.ClassTorn, Tier: op.Tier, Desc: op.Desc})
	}
	sn.Journal = &session.JournalState{Open: []session.OpenRun{{Run: run, Inspection: in}}}
	return sn
}

// Two receivers on the same model differ at the end of their paths, which
// the picker keeps.
func TestChoosingKeepsThePathTails(t *testing.T) {
	sn := bare(session.Choosing)
	base := "IOService:/x/arm-io@00000000/x/usb@00000000/XHC@00000000/port@00000000/hub@00000000/"
	a, b := receiver, receiver
	a.Path, b.Path = base+"USB Receiver@01100000/IOUSBHostInterface@1", base+"USB Receiver@01200000/IOUSBHostInterface@1"
	sn.Answers = []session.Answer{{Candidate: a}, {Candidate: b}}
	h := newHarness(t, sn)
	s := h.screen(80, 24)
	for _, tail := range []string{"1100000/IOUSBHostInterface@1", "1200000/IOUSBHostInterface@1"} {
		if !strings.Contains(s, tail) {
			t.Errorf("the picker cut %q:\n%s", tail, s)
		}
	}
}

// A narrow header drops the source badge before the tier badge, and a
// write that is still backing up says so in the state badge.
func TestHeaderSafetyBadges(t *testing.T) {
	h := newHarness(t, ready(t), func(o *Options) { o.Source = "emulator" })
	head := strings.Split(h.screen(60, 20), "\n")[0]
	if !strings.Contains(head, "[UNTESTED]") {
		t.Errorf("60 columns: %q", head)
	}
	stage(h, 1, 1600)
	p := stagedPlan(t, h)
	release := holdWrite(h, p)
	defer release()
	backingUp := ready(t)
	backingUp.Progress = session.Progress{Job: "backup", Done: 48, Total: 680}
	h.snap(backingUp)
	h.startWrite(applyRequest(p, safety.Gates{}))
	h.snap(backingUp)
	s := h.screen(80, 24)
	if head := strings.Split(s, "\n")[0]; strings.Contains(head, "ONLINE") || !strings.Contains(head, "BACKING UP") {
		t.Errorf("header during the first backup: %q", head)
	}
	if strings.Contains(s, "BUSY") {
		t.Errorf("the write's backup shows as BUSY:\n%s", s)
	}
}
