package safety

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
)

// Preflight failures. Each one the preflight reports wraps one of these, with
// the detail a person needs to clear it.
var (
	ErrNotReady      = errors.New("safety: the device is not ready for writes")
	ErrBlocked       = errors.New("safety: the device is locked, seized or stalled")
	ErrConflict      = errors.New("safety: another client is talking to the device")
	ErrStale         = errors.New("safety: the device changed since it was loaded")
	ErrForeignClient = errors.New("safety: another program has the receiver open")
	ErrNoLock        = errors.New("safety: this arcctl does not hold the single-instance lock")
	ErrScreenLocked  = errors.New("safety: the screen is locked")
	ErrSecureInput   = errors.New("safety: Secure Input is on")
	ErrTier          = errors.New("safety: the plan writes a field arcctl never writes")
	ErrUntested      = errors.New("safety: the plan writes untested features")
	ErrExperimental  = errors.New("safety: the plan writes experimental features")
	ErrConfirm       = errors.New("safety: the typed confirmation does not match")
	ErrNoBackup      = errors.New("safety: the loaded configuration is not backed up")
	ErrNoFullBackup  = errors.New("safety: the first write to this device needs a full backup")
	ErrPartialBackup = errors.New("safety: the full backup is partial")
)

// Client is a process other than arcctl that holds the receiver open.
type Client struct {
	PID    int
	Name   string
	Seized bool
}

func (c Client) String() string {
	s := c.Name
	if s == "" {
		s = "unknown process"
	}
	s += " (pid " + strconv.Itoa(c.PID)
	if c.Seized {
		s += ", exclusive"
	}
	return s + ")"
}

// Console is the state of the login session that blocks HID writes on macOS.
type Console struct {
	ScreenLocked bool
	SecureInput  string // the process holding Secure Input; "" when it is off
}

// Facts are what the session found out just before a write, most of it asked
// again for the occasion.
type Facts struct {
	// State is why the session cannot write now (not loaded and Ready, or a
	// Conflict or SuspectedConflict), nil when it can.
	State error
	// Online is the answer of a fresh cmd 3: nil when the mouse is online,
	// an error wrapping ErrOffline while it sleeps.
	Online error
	// Identity comes from a fresh handshake and cmd 3, Profile from a fresh
	// cmd 14 (nil when the mouse has no profiles).
	Identity plan.Identity
	Profile  *byte
	// Current holds every extent the plan writes, read again from the device.
	Current *flash.Image
	// Firmware is the version a fresh cmd 18 reports, asked before a
	// factory reset only.
	Firmware string

	Journal    *Status
	JournalErr error
	// Clients are the foreign clients the IORegistry scan found on the
	// receiver; ScanErr says why the scan failed. Both are empty where the
	// platform has no scan.
	Clients []Client
	ScanErr error
	// Lock is nil when this process holds the single-instance lock, or when
	// no other process can reach the device.
	Lock       error
	Console    Console
	ConsoleErr error
}

// Gates are what the user allowed for one write.
type Gates struct {
	// DryRun sends nothing: the write-only gates (tiers, foreign clients, a
	// clean journal and I1) are not checked.
	DryRun             bool
	AllowUntested      bool   // --allow-untested
	Experimental       bool   // --experimental
	Confirm            string // what the user typed; it must equal ConfirmPhrase
	AllowForeignClient bool   // --allow-foreign-client
	// AcceptPartial accepts a full backup that could not read every range,
	// after the user saw which ranges it lacks.
	AcceptPartial bool
}

// ConfirmPhrase is what the user types to write ops below Verified: "write
// untested", or "write experimental" when an Experimental op is among them.
// Ops that write captured bytes add every extent they write, so the phrase
// names each one: "write experimental and captured 6+2 84+12". It is ""
// when every op is Verified.
func ConfirmPhrase(ops []plan.Op) string {
	low := catalog.Verified
	var captured []string
	for _, op := range ops {
		if op.Tier >= catalog.Experimental {
			low = min(low, op.Tier)
		}
		if e := op.Extent.String(); op.Phase == plan.Captured && !slices.Contains(captured, e) {
			captured = append(captured, e)
		}
	}
	if low == catalog.Verified {
		return ""
	}
	phrase := "write " + low.String()
	if len(captured) > 0 {
		phrase += " and captured " + strings.Join(captured, " ")
	}
	return phrase
}

// PreflightError lists every check that blocked a write.
type PreflightError struct {
	Failures []error
}

func (e *PreflightError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = strings.TrimPrefix(f.Error(), "safety: ")
	}
	return "safety: write blocked: " + strings.Join(parts, "; ")
}

func (e *PreflightError) Unwrap() []error { return e.Failures }

func blocked(fs []error) error {
	if len(fs) == 0 {
		return nil
	}
	return &PreflightError{Failures: fs}
}

// Preflight checks a write before its first packet (§6.3) and returns a
// *PreflightError listing every check that failed. kind says what the write
// is: an apply is checked in full; a revert takes p's ops from the run it
// undoes and leaves the unchanged-extents check to the executor; a recovery
// settles ops the user already confirmed, so it skips the tier gates, and it
// runs on a journal that is not clean by definition. For a revert or a
// recovery, p carries the identity and profile the run was written with.
func Preflight(kind RunKind, p plan.Plan, f Facts, g Gates) error {
	var fs []error
	add := func(err error) {
		if err != nil {
			fs = append(fs, err)
		}
	}
	add(f.State)
	add(f.Online)
	if f.State == nil && f.Online == nil {
		if p.Device.Key() != f.Identity.Key() {
			add(fmt.Errorf("%w: the plan is for %s, the device answering is %s", ErrIdentity, p.Device.Key(), f.Identity.Key()))
		}
		if !sameProfile(p.Profile, f.Profile) {
			add(fmt.Errorf("%w: the plan was made on profile %s, the mouse is on profile %s; reload and plan again",
				ErrProfile, profileString(p.Profile), profileString(f.Profile)))
		}
		if kind == KindApply {
			add(stale(p, f.Current))
		}
	}
	for _, err := range tiers(kind, p, g) {
		add(err)
	}
	fs = append(fs, host(f, g)...)
	if g.DryRun {
		return blocked(fs)
	}
	switch {
	case f.JournalErr != nil:
		add(f.JournalErr)
	case f.Journal == nil:
		add(fmt.Errorf("%w: not read", ErrJournal))
	case kind != KindRecover && !f.Journal.Clean():
		if n := len(f.Journal.Open); n > 0 {
			ids := make([]string, n)
			for i, r := range f.Journal.Open {
				ids[i] = r.ID
			}
			add(fmt.Errorf("%w: %s; recover first", ErrNotClean, strings.Join(ids, ", ")))
		}
		for _, r := range f.Journal.Unsettled {
			add(fmt.Errorf("%w: the factory reset %s ended before arcctl checked what it did; arcctl checks it once the mouse is loaded",
				ErrNotClean, r.ID))
		}
	}
	return blocked(fs)
}

// Recheck is the part of the preflight a pause in the middle of a write
// makes stale, apart from the device's bytes, which the executor reads
// again itself: f.State, the lock, the console and, unless g allows them or
// runs dry, other clients on the receiver. It returns a *PreflightError.
func Recheck(f Facts, g Gates) error {
	var fs []error
	if f.State != nil {
		fs = append(fs, f.State)
	}
	return blocked(append(fs, host(f, g)...))
}

// host checks what the OS says: the lock, the console and, for a write that
// is not dry and does not allow them, the other clients of the receiver.
func host(f Facts, g Gates) []error {
	var fs []error
	if f.Lock != nil {
		fs = append(fs, fmt.Errorf("%w: %w", ErrNoLock, f.Lock))
	}
	switch {
	case f.ConsoleErr != nil:
		fs = append(fs, fmt.Errorf("%w: the console state could not be read: %w", ErrScreenLocked, f.ConsoleErr))
	case f.Console.ScreenLocked:
		fs = append(fs, fmt.Errorf("%w: unlock the Mac, then retry", ErrScreenLocked))
	case f.Console.SecureInput != "":
		fs = append(fs, fmt.Errorf("%w: %s holds it; close its password field or quit it, then retry", ErrSecureInput, f.Console.SecureInput))
	}
	if g.DryRun || g.AllowForeignClient {
		return fs
	}
	switch {
	case f.ScanErr != nil:
		fs = append(fs, fmt.Errorf("%w: the client scan failed (%w); pass --allow-foreign-client to write anyway", ErrForeignClient, f.ScanErr))
	case len(f.Clients) > 0:
		names := make([]string, len(f.Clients))
		for i, c := range f.Clients {
			names[i] = c.String()
		}
		fs = append(fs, fmt.Errorf("%w: %s; quit it or pass --allow-foreign-client", ErrForeignClient, strings.Join(names, ", ")))
	}
	return fs
}

// stale compares every extent p writes with the bytes read again just now:
// they must equal the old bytes of the first op on that extent.
func stale(p plan.Plan, cur *flash.Image) error {
	var seen []flash.Extent
	var bad []string
	for _, op := range p.Ops {
		if slices.Contains(seen, op.Extent) {
			continue
		}
		seen = append(seen, op.Extent)
		var now []byte
		ok := false
		if cur != nil {
			now, ok = cur.Get(op.Extent)
		}
		switch {
		case !ok:
			bad = append(bad, op.Extent.String()+" was not read again")
		case !bytes.Equal(now, op.Old):
			bad = append(bad, fmt.Sprintf("%s holds % x, the plan expects % x", op.Extent, now, op.Old))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s; reload and plan again", ErrStale, strings.Join(bad, ", "))
}

func tiers(kind RunKind, p plan.Plan, g Gates) []error {
	var refused []string
	untested, experimental := 0, 0
	for _, op := range p.Ops {
		switch op.Tier {
		case catalog.Verified:
		case catalog.Untested:
			untested++
		case catalog.Experimental:
			experimental++
		default:
			refused = append(refused, fmt.Sprintf("op %d (%s) is %s", op.Seq, op.Desc, op.Tier))
		}
	}
	var fs []error
	if len(refused) > 0 {
		fs = append(fs, fmt.Errorf("%w: %s", ErrTier, strings.Join(refused, ", ")))
	}
	if kind == KindRecover || g.DryRun {
		return fs
	}
	if untested > 0 && !g.AllowUntested {
		fs = append(fs, fmt.Errorf("%w: %s; pass --allow-untested", ErrUntested, count(untested, "record")))
	}
	if experimental > 0 && !g.Experimental {
		fs = append(fs, fmt.Errorf("%w: %s; pass --experimental", ErrExperimental, count(experimental, "record")))
	}
	if want := ConfirmPhrase(p.Ops); want != "" && strings.TrimSpace(g.Confirm) != want {
		fs = append(fs, fmt.Errorf("%w: type %q to write features no hardware test has verified", ErrConfirm, want))
	}
	return fs
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// BackupState is what I1 needs to know before a write.
type BackupState struct {
	// Session is where this session saved every byte it had loaded; "" when
	// it has not.
	Session string
	// Written reports whether the journal holds a run for the device: it
	// has been written before, so its first write is behind it.
	Written bool
	// Full is where this session saved a full backup of the device, and
	// Missing the ranges that backup could not read.
	Full    string
	Missing []flash.Extent
}

// PartialError is a full backup that lacks some ranges and was not accepted.
type PartialError struct {
	Path    string
	Missing []flash.Extent
}

func (e *PartialError) Error() string {
	ranges := make([]string, len(e.Missing))
	for i, m := range e.Missing {
		ranges[i] = m.String()
	}
	return fmt.Sprintf("%v: %s lacks %s; accept it as partial to write anyway", ErrPartialBackup, e.Path, strings.Join(ranges, ", "))
}

func (e *PartialError) Unwrap() error { return ErrPartialBackup }

// BackupGate enforces I1: a session writes only after it saved every byte it
// had loaded, and the first write ever to a device only after a full backup
// that completed or that the user accepted as partial.
func BackupGate(b BackupState, g Gates) error {
	var fs []error
	if b.Session == "" {
		fs = append(fs, fmt.Errorf("%w: this session saved no backup yet", ErrNoBackup))
	}
	if !b.Written {
		switch {
		case b.Full == "":
			fs = append(fs, ErrNoFullBackup)
		case len(b.Missing) > 0 && !g.AcceptPartial:
			fs = append(fs, &PartialError{Path: b.Full, Missing: slices.Clone(b.Missing)})
		}
	}
	return blocked(fs)
}
