package session

import (
	"log/slog"
	"time"

	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/safety"
)

// Devices finds and opens HID interfaces. hidio provides one per backend
// (HID); the emulator's Bus is another.
type Devices interface {
	Enumerate() ([]hidio.Candidate, error)
	Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error)
}

// HID returns the Devices of a hidio backend; "" is the default one.
func HID(backend string) Devices { return hidDevices(backend) }

type hidDevices string

func (b hidDevices) Enumerate() ([]hidio.Candidate, error) { return hidio.Enumerate(string(b)) }

func (hidDevices) Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error) {
	return hidio.Open(c, g, rec)
}

// Client is a process other than arcctl that holds an interface open.
type Client struct {
	PID    int
	Name   string
	Seized bool
}

type Options struct {
	Devices Devices // required
	// Device is a candidate path (--device). When set, only that interface is
	// probed and a single answer is taken without a choice.
	Device string
	// Preflight checks the OS permission before any device is opened; an
	// error puts the session in NeedsPermission.
	Preflight func() error
	// Clients lists the foreign HID clients on an interface (the IORegistry
	// scan); nil when the platform has no scan.
	Clients func(c hidio.Candidate) ([]Client, error)
	// Recorder records the traffic of every interface the session opens.
	Recorder *hidio.Recorder
	// TrustAddress keys the identity on the cmd-3 address; leave it off until
	// the hardware tests show the address is stable.
	TrustAddress bool
	// Writes enables Apply, Recover and Revert; without it they return
	// ErrReadOnly.
	Writes *Writes
	Timing Timing
	Log    *slog.Logger
}

// Writes is what the session needs before it writes to a device.
type Writes struct {
	Journal string  // the journal folder, <data>/journal
	Backups Backups // where the backups I1 requires are saved
	// Lock reports whether this process holds the single-instance lock: nil
	// when it does, or when no other process can reach the device (the
	// emulator). A nil Lock blocks every write.
	Lock func() error
	// Console reports the screen lock and the Secure Input holder; nil where
	// the OS has neither.
	Console func() (safety.Console, error)
	// Executor tunes the executor; its Log defaults to the session's.
	Executor safety.Options
}

// Backups saves the backups I1 requires before a write and returns where
// each went; backup.Store implements it.
type Backups interface {
	Save(c Capture, label string) (string, error)
}

// Timing holds every delay and cadence the session uses. Zero fields take
// the values of DefaultTiming.
type Timing struct {
	Try           time.Duration // wait for a reply, per try
	Tries         int
	ProbeTry      time.Duration // the same for the interface probe
	ProbeTries    int
	Window        time.Duration // how long completed and timed-out requests explain a reply
	Debounce      time.Duration // minimum gap between wake-triggered cmd-3 checks
	Rescan        time.Duration // NoReceiver rescan, doubling up to RescanMax
	RescanMax     time.Duration
	Retry         time.Duration // Locked, Seized and NeedsPermission retry, doubling up to RetryMax
	RetryMax      time.Duration
	Offline       time.Duration // cmd-3 poll while Offline
	Online        time.Duration // cmd-3 poll while Ready
	Battery       time.Duration // cmd-4 poll while Ready
	Suspect       time.Duration // cmd 3 and client scan while SuspectedConflict
	ConflictQuiet time.Duration // silence needed before a Conflict can be cleared
	LoadWatchdog  time.Duration // a job with no successful read this long gives up its remaining reads
}

func DefaultTiming() Timing {
	return Timing{
		Try:           200 * time.Millisecond,
		Tries:         5,
		ProbeTry:      150 * time.Millisecond,
		ProbeTries:    3,
		Window:        2 * time.Second,
		Debounce:      200 * time.Millisecond,
		Rescan:        time.Second,
		RescanMax:     5 * time.Second,
		Retry:         2 * time.Second,
		RetryMax:      10 * time.Second,
		Offline:       1500 * time.Millisecond,
		Online:        5 * time.Second,
		Battery:       30 * time.Second,
		Suspect:       5 * time.Second,
		ConflictQuiet: 10 * time.Second,
		LoadWatchdog:  30 * time.Second,
	}
}

func (t Timing) withDefaults() Timing {
	d := DefaultTiming()
	durations := []struct{ v, def *time.Duration }{
		{&t.Try, &d.Try}, {&t.ProbeTry, &d.ProbeTry}, {&t.Window, &d.Window}, {&t.Debounce, &d.Debounce},
		{&t.Rescan, &d.Rescan}, {&t.RescanMax, &d.RescanMax}, {&t.Retry, &d.Retry}, {&t.RetryMax, &d.RetryMax},
		{&t.Offline, &d.Offline}, {&t.Online, &d.Online}, {&t.Battery, &d.Battery}, {&t.Suspect, &d.Suspect},
		{&t.ConflictQuiet, &d.ConflictQuiet}, {&t.LoadWatchdog, &d.LoadWatchdog},
	}
	for _, x := range durations {
		if *x.v <= 0 {
			*x.v = *x.def
		}
	}
	if t.Tries <= 0 {
		t.Tries = d.Tries
	}
	if t.ProbeTries <= 0 {
		t.ProbeTries = d.ProbeTries
	}
	return t
}
