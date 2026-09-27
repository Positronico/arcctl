package safety_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// preflightPlan writes the current stage (Untested) and DPI stage 1 at the
// given tier, on the committed dump.
func preflightPlan(t *testing.T, dpiTier catalog.Tier) (plan.Plan, *flash.Image) {
	t.Helper()
	im := dumpImage(t)
	m, _ := catalog.ByKey("7B04")
	changes := slices.Concat(pairChange(t), dpiChange(t, m))
	changes[1].Tier = dpiTier
	p, err := plan.New(identity, ptr(byte(0)), im, mouse.Layout(m), changes)
	if err != nil {
		t.Fatal(err)
	}
	return p, im
}

func goodFacts(im *flash.Image) safety.Facts {
	return safety.Facts{Identity: identity, Profile: ptr(byte(0)), Current: im.Clone(), Journal: &safety.Status{}}
}

func allowed(p plan.Plan) safety.Gates {
	return safety.Gates{AllowUntested: true, Experimental: true, Confirm: safety.ConfirmPhrase(p.Ops)}
}

func TestPreflightPassesWhenEveryCheckDoes(t *testing.T) {
	p, im := preflightPlan(t, catalog.Untested)
	if err := safety.Preflight(safety.KindApply, p, goodFacts(im), allowed(p)); err != nil {
		t.Fatal(err)
	}
	verified, im := preflightPlan(t, catalog.Verified)
	for i := range verified.Ops {
		verified.Ops[i].Tier = catalog.Verified
	}
	if err := safety.Preflight(safety.KindApply, verified, goodFacts(im), safety.Gates{}); err != nil {
		t.Fatalf("a verified plan needs no flags: %v", err)
	}
}

// Each check blocks the apply on its own, with an error that says which.
func TestPreflightFailures(t *testing.T) {
	p, im := preflightPlan(t, catalog.Untested)
	other := identity
	other.PID = 0x1283
	changed := im.Clone()
	if err := changed.Set(mouse.AddrCurrentDPI, []byte{0x02, 0x53}); err != nil {
		t.Fatal(err)
	}
	unread := flash.New()
	experimental, _ := preflightPlan(t, catalog.Experimental)
	readOnly, _ := preflightPlan(t, catalog.Untested)
	readOnly.Ops[0].Tier = catalog.ReadOnly
	open := &safety.Status{Open: []*safety.Run{{ID: "20260926T120000.000000000Z-1/1"}}}

	tests := []struct {
		name  string
		p     plan.Plan
		facts func(*safety.Facts)
		gates func(*safety.Gates)
		want  error
		text  string
	}{
		{name: "state", facts: func(f *safety.Facts) { f.State = safety.ErrConflict }, want: safety.ErrConflict},
		{name: "offline", facts: func(f *safety.Facts) { f.Online = safety.ErrOffline }, want: safety.ErrOffline},
		{name: "identity", facts: func(f *safety.Facts) { f.Identity = other }, want: safety.ErrIdentity, text: "260d-1283-7b04"},
		{name: "profile changed", facts: func(f *safety.Facts) { f.Profile = ptr(byte(1)) }, want: safety.ErrProfile},
		{name: "profile gone", facts: func(f *safety.Facts) { f.Profile = nil }, want: safety.ErrProfile},
		{name: "stale", facts: func(f *safety.Facts) { f.Current = changed }, want: safety.ErrStale, text: "02 53"},
		{name: "not read again", facts: func(f *safety.Facts) { f.Current = unread }, want: safety.ErrStale, text: "not read again"},
		{name: "foreign client", facts: func(f *safety.Facts) {
			f.Clients = []safety.Client{{PID: 812, Name: "Google Chrome Helper"}}
		}, want: safety.ErrForeignClient, text: "Google Chrome Helper (pid 812)"},
		{name: "scan failed", facts: func(f *safety.Facts) { f.ScanErr = errors.New("no registry") }, want: safety.ErrForeignClient},
		{name: "lock", facts: func(f *safety.Facts) { f.Lock = errors.New("another arcctl (pid 9) holds it") }, want: safety.ErrNoLock},
		{name: "screen locked", facts: func(f *safety.Facts) { f.Console.ScreenLocked = true }, want: safety.ErrScreenLocked},
		{name: "secure input", facts: func(f *safety.Facts) { f.Console.SecureInput = "Terminal (pid 77)" }, want: safety.ErrSecureInput, text: "Terminal (pid 77)"},
		{name: "console unknown", facts: func(f *safety.Facts) { f.ConsoleErr = errors.New("no session") }, want: safety.ErrScreenLocked},
		{name: "journal unread", facts: func(f *safety.Facts) { f.Journal = nil }, want: safety.ErrJournal},
		{name: "journal corrupt", facts: func(f *safety.Facts) { f.Journal, f.JournalErr = nil, safety.ErrCorrupt }, want: safety.ErrCorrupt},
		{name: "journal not clean", facts: func(f *safety.Facts) { f.Journal = open }, want: safety.ErrNotClean, text: "20260926T120000.000000000Z-1/1"},
		{name: "untested", gates: func(g *safety.Gates) { g.AllowUntested = false }, want: safety.ErrUntested},
		{name: "unconfirmed", gates: func(g *safety.Gates) { g.Confirm = "yes" }, want: safety.ErrConfirm, text: `"write untested"`},
		{name: "experimental", p: experimental, gates: func(g *safety.Gates) { g.Experimental = false }, want: safety.ErrExperimental},
		{name: "experimental confirmation", p: experimental, gates: func(g *safety.Gates) { g.Confirm = "write untested" }, want: safety.ErrConfirm, text: `"write experimental"`},
		{name: "read-only field", p: readOnly, want: safety.ErrTier},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pl := p
			if tt.p.Ops != nil {
				pl = tt.p
			}
			f, g := goodFacts(im), allowed(pl)
			if tt.facts != nil {
				tt.facts(&f)
			}
			if tt.gates != nil {
				tt.gates(&g)
			}
			err := safety.Preflight(safety.KindApply, pl, f, g)
			var pe *safety.PreflightError
			if !errors.As(err, &pe) || !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want a *PreflightError with %v", err, tt.want)
			}
			if len(pe.Failures) != 1 {
				t.Errorf("%d failures, want 1: %v", len(pe.Failures), err)
			}
			if !strings.Contains(err.Error(), tt.text) {
				t.Errorf("err %q does not say %q", err, tt.text)
			}
		})
	}
}

func TestPreflightListsEveryFailure(t *testing.T) {
	p, im := preflightPlan(t, catalog.Untested)
	f := goodFacts(im)
	f.Clients = []safety.Client{{PID: 1, Name: "chrome"}}
	f.Console.ScreenLocked = true
	f.Lock = errors.New("held elsewhere")
	err := safety.Preflight(safety.KindApply, p, f, safety.Gates{})
	for _, want := range []error{safety.ErrForeignClient, safety.ErrScreenLocked, safety.ErrNoLock, safety.ErrUntested, safety.ErrConfirm} {
		if !errors.Is(err, want) {
			t.Errorf("%v does not list %v", err, want)
		}
	}
	if strings.Count(err.Error(), "safety:") != 1 {
		t.Errorf("message %q repeats the package prefix", err)
	}
}

// A sleeping mouse cannot be identified, so only the sleep is reported.
func TestPreflightOfflineSkipsTheDeviceChecks(t *testing.T) {
	p, _ := preflightPlan(t, catalog.Untested)
	f := safety.Facts{Online: safety.ErrOffline, Journal: &safety.Status{}}
	err := safety.Preflight(safety.KindApply, p, f, allowed(p))
	var pe *safety.PreflightError
	if !errors.As(err, &pe) || len(pe.Failures) != 1 || !errors.Is(err, safety.ErrOffline) {
		t.Fatalf("err = %v, want only ErrOffline", err)
	}
}

// A dry run writes nothing, so the write-only gates stay open; the device
// still has to be the one the plan was made for.
func TestPreflightDryRun(t *testing.T) {
	p, im := preflightPlan(t, catalog.Experimental)
	f := goodFacts(im)
	f.Clients = []safety.Client{{PID: 1, Name: "chrome"}}
	f.Journal = &safety.Status{Open: []*safety.Run{{ID: "r/1"}}}
	if err := safety.Preflight(safety.KindApply, p, f, safety.Gates{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	f.Profile = ptr(byte(2))
	if err := safety.Preflight(safety.KindApply, p, f, safety.Gates{DryRun: true}); !errors.Is(err, safety.ErrProfile) {
		t.Fatalf("dry run on another profile: %v", err)
	}
}

// A recovery settles ops the user already confirmed on a journal that is
// open by definition; a revert re-reads its extents itself but is gated by
// the tiers of the run it undoes.
func TestPreflightRecoverAndRevert(t *testing.T) {
	p, im := preflightPlan(t, catalog.Untested)
	f := goodFacts(im)
	f.Current = nil
	f.Journal = &safety.Status{Open: []*safety.Run{{ID: "r/1"}}}
	if err := safety.Preflight(safety.KindRecover, p, f, safety.Gates{}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	err := safety.Preflight(safety.KindRevert, p, f, safety.Gates{})
	for _, want := range []error{safety.ErrNotClean, safety.ErrUntested, safety.ErrConfirm} {
		if !errors.Is(err, want) {
			t.Errorf("revert: %v does not list %v", err, want)
		}
	}
	if errors.Is(err, safety.ErrStale) {
		t.Errorf("revert checked the extents: %v", err)
	}
	f.Journal = &safety.Status{}
	if err := safety.Preflight(safety.KindRevert, p, f, allowed(p)); err != nil {
		t.Fatalf("revert with the gates open: %v", err)
	}
}

func TestConfirmPhrase(t *testing.T) {
	op := func(t catalog.Tier) plan.Op { return plan.Op{Tier: t} }
	tests := []struct {
		ops  []plan.Op
		want string
	}{
		{nil, ""},
		{[]plan.Op{op(catalog.Verified)}, ""},
		{[]plan.Op{op(catalog.Verified), op(catalog.Untested)}, "write untested"},
		{[]plan.Op{op(catalog.Untested), op(catalog.Experimental)}, "write experimental"},
	}
	for _, tt := range tests {
		if got := safety.ConfirmPhrase(tt.ops); got != tt.want {
			t.Errorf("ConfirmPhrase(%v) = %q, want %q", tt.ops, got, tt.want)
		}
	}
}

func TestBackupGate(t *testing.T) {
	missing := []flash.Extent{{Addr: 9504, Len: 256}}
	tests := []struct {
		name string
		b    safety.BackupState
		g    safety.Gates
		want []error
	}{
		{name: "nothing saved, never written", want: []error{safety.ErrNoBackup, safety.ErrNoFullBackup}},
		{name: "loaded bytes saved, never written", b: safety.BackupState{Session: "s.json"}, want: []error{safety.ErrNoFullBackup}},
		{name: "written before, nothing saved", b: safety.BackupState{Written: true}, want: []error{safety.ErrNoBackup}},
		{name: "written before", b: safety.BackupState{Session: "s.json", Written: true}},
		{name: "full backup", b: safety.BackupState{Session: "f.json", Full: "f.json"}},
		{name: "partial", b: safety.BackupState{Session: "f.json", Full: "f.json", Missing: missing}, want: []error{safety.ErrPartialBackup}},
		{name: "partial accepted", b: safety.BackupState{Session: "f.json", Full: "f.json", Missing: missing}, g: safety.Gates{AcceptPartial: true}},
		{name: "partial after the first write", b: safety.BackupState{Session: "s.json", Written: true, Full: "f.json", Missing: missing}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := safety.BackupGate(tt.b, tt.g)
			if len(tt.want) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var pe *safety.PreflightError
			if !errors.As(err, &pe) || len(pe.Failures) != len(tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			for _, w := range tt.want {
				if !errors.Is(err, w) {
					t.Errorf("%v does not list %v", err, w)
				}
			}
		})
	}
	var partial *safety.PartialError
	err := safety.BackupGate(safety.BackupState{Session: "f.json", Full: "f.json", Missing: missing}, safety.Gates{})
	if !errors.As(err, &partial) || partial.Path != "f.json" || !slices.Equal(partial.Missing, missing) {
		t.Fatalf("err = %v, want a *PartialError naming the file and its gaps", err)
	}
	if !strings.Contains(err.Error(), "9504+256") {
		t.Errorf("%q does not list the missing range", err)
	}
}
