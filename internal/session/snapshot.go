package session

import (
	"fmt"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// Snapshot is the session as it was at one moment. It is never changed after
// it is published; Image is shared and read-only.
type Snapshot struct {
	Seq   uint64
	State State
	// Link is the device state under a Conflict, a SuspectedConflict or
	// Recovering; it equals State otherwise.
	Link  State
	Since time.Time
	Err   error // why the session is in State, when that is an error

	Device  *hidio.Candidate // the chosen interface
	Answers []Answer         // Choosing: every interface that answered

	Online    bool
	Handshake *Handshake
	Model     *catalog.Model
	Identity  plan.Identity
	Versions  Versions
	Battery   *Battery
	Profile   Probe // cmd 14
	LongRange Probe // cmd 23; Value 1 means on

	Image    *flash.Image   // the last complete load, with later re-reads; nil before the first
	Unread   []flash.Extent // chunks the last load could not read
	Progress Progress
	// DryRun is Image with every dry-run write of this session on top; nil
	// when no dry run wrote anything.
	DryRun *flash.Image
	// Journal is the device's journal at the last check, after each load;
	// nil before the first check or when the session does not write.
	Journal *JournalState

	ConflictSince time.Time
	LastForeign   time.Time
	Clients       []Client // foreign clients at the last scan
	Stalls        int
	Policy        wire.Policy
	Stats         Stats
}

// Answer is an interface that answered the probe.
type Answer struct {
	Candidate hidio.Candidate
	Online    bool
	Handshake *Handshake
	Model     *catalog.Model
}

type Handshake struct {
	CID, MID byte
	Conn     byte // connection type, reply byte 11
}

var connNames = [...]string{"wireless 1 kHz", "wireless 4 kHz", "wired 1 kHz", "wired 8 kHz", "wireless 2 kHz", "wireless 8 kHz", "charging base"}

func (h Handshake) ConnString() string {
	if int(h.Conn) < len(connNames) {
		return connNames[h.Conn]
	}
	return fmt.Sprintf("type %d", h.Conn)
}

func (h Handshake) Wired() bool { return h.Conn == 2 || h.Conn == 3 }

const connChargingBase = 6

type Versions struct {
	Receiver string // cmd 29; "v1.0" when the receiver rejects it
	Mouse    string // cmd 18
}

// Battery is the raw cmd-4 reply: Level is byte 5, or byte 10 when Direct.
type Battery struct {
	Level      byte
	Charging   bool
	MilliVolts uint16
	Direct     bool
	At         time.Time
}

// Probe is the answer to a query the device may reject.
type Probe struct {
	Asked     bool // a reply or a NAK came back
	Supported bool
	Value     byte
}

type Progress struct {
	Job    string // "load", "reread", "backup", "read", "journal" (the check after a load), "reset check" (the read after a factory reset), or "apply", "revert", "recover" and "reset" while Applying; empty when idle
	Done   int    // transactions finished, or ops verified while Applying
	Total  int    // transactions known so far (a load adds more as it learns the bindings), or the ops of the run
	Paused bool   // waiting for the mouse to wake, or for the screen to unlock while Applying
}

// JournalState is what the device's journal holds: the runs a crash or a
// failed write left unfinished, each read again from the device, and the run
// Revert would undo.
type JournalState struct {
	Open []OpenRun
	Last *safety.Run
	// Resets are the factory resets a process ended before it checked what
	// they did; the session checks them after the load (the "reset check"
	// job) and journals what it finds.
	Resets []*safety.Run
	Err    error // the journal could not be read
}

// OpenRun is an unfinished run and what its extents hold now. Inspection is
// nil when they could not be read; Err says why.
type OpenRun struct {
	Run        *safety.Run
	Inspection *safety.Inspection
	Err        error
}

type Stats struct {
	Transactions   int
	FailedTries    int // tries whose request was sent and went unanswered
	WriteErrors    int // tries whose request the OS or the device refused
	NAKs           int
	Pushes         int
	PossiblePushes int // cmd 3 reports nobody asked for
	Duplicates     int
	Late           int
	Foreign        int
	OddStatus      int
	BadChecksums   int
	Dropped        uint64
}

// Capture is what a backup is made from: the bytes read so far or for it,
// and the identity and profile they belong to.
type Capture struct {
	Device    plan.Identity
	Model     *catalog.Model
	Profile   Probe
	Versions  Versions
	Image     *flash.Image
	Full      bool
	Missing   []flash.Extent // chunks that could not be read
	Handshake *Handshake     // the handshake of the mouse the bytes came from
	Started   time.Time
	Finished  time.Time
}

func (s *Session) snapshot() *Snapshot {
	s.seq++
	sn := &Snapshot{
		Seq:           s.seq,
		State:         s.state(),
		Link:          s.base,
		Since:         s.since,
		Err:           s.err,
		Online:        s.online,
		Model:         s.model,
		Versions:      s.versions,
		Profile:       s.profile,
		LongRange:     s.longRange,
		Image:         s.shown,
		Unread:        slices.Clone(s.unread),
		Progress:      s.progress(),
		Journal:       s.jstate,
		ConflictSince: s.conflictSince,
		LastForeign:   s.lastForeign,
		Clients:       slices.Clone(s.clients),
		Stalls:        s.stalls,
		Policy:        s.guard.Policy(),
		Stats:         s.stats,
	}
	if s.dev != nil {
		c := s.dev.c
		sn.Device = &c
		sn.Stats.Dropped = s.dev.tr.Dropped()
	}
	if s.hs != nil {
		h := *s.hs
		sn.Handshake = &h
		sn.Identity = s.identity()
	}
	if s.battery != nil {
		b := *s.battery
		sn.Battery = &b
	}
	if s.overlay != nil && s.shown != nil && s.overlayKey == sn.Identity.Key() {
		sn.DryRun = s.overlay.Apply(s.shown)
	}
	for _, l := range s.choices {
		a := Answer{Candidate: l.c, Online: l.online, Model: l.model}
		if l.hs != nil {
			h := *l.hs
			a.Handshake = &h
		}
		sn.Answers = append(sn.Answers, a)
	}
	return sn
}

func (s *Session) state() State {
	switch {
	case s.conflict != 0 && s.base.attached():
		return s.conflict
	case s.base == Ready && s.jstate != nil && len(s.jstate.Open) > 0:
		return Recovering
	}
	return s.base
}

func (s *Session) identity() plan.Identity {
	id := plan.Identity{AddrTrusted: s.opt.TrustAddress, Addr: s.addr}
	if s.hs != nil {
		id.CID, id.MID = s.hs.CID, s.hs.MID
	}
	if s.dev != nil {
		id.VID, id.PID = s.dev.c.VID, s.dev.c.PID
	}
	return id
}
