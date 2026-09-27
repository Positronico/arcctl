package emu

import (
	"fmt"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

// Config describes one emulated USB device. The zero value is an EM11 Pro
// style receiver (260D:1282) with nothing paired: two interfaces, of which
// only interface 1 answers, and a NAK for cmd 29.
type Config struct {
	VID, PID uint16
	// Interfaces is the number of HID interfaces and Answering the one that
	// answers; the others take packets and never reply. When Interfaces is 0
	// the receiver layout applies and Answering is ignored.
	Interfaces int
	Answering  int
	RxVersion  *Version // nil: cmd 29 gets a NAK
	Mouse      *Mouse   // nil: nothing paired, so the receiver reports offline
	Behavior   Behavior
	Latency    Latency
}

// Mouse is the device paired to a receiver.
type Mouse struct {
	// Model gives the cid and mid; CID and MID replace them when set, and are
	// all there is for a model the catalog does not know.
	Model    *catalog.Model
	CID, MID byte
	Conn     byte    // handshake connection type, reply byte 11
	Addr     [3]byte // zero: the placeholder 11 22 33
	Image    *flash.Image
	Factory  *flash.Image // what cmd 9 restores; nil: the model's defaults, else Image
	Firmware Version
	Battery  Battery
	Profile  *byte // nil: cmd 14 gets a NAK
	// LongRange is the extended-range flag of cmds 22 and 23; nil: both get a NAK.
	LongRange *bool
	Asleep    bool
	// SleepAfter puts the mouse to sleep that long after its last input or
	// the last packet that reached it over the radio; 0: only Sleep does.
	SleepAfter time.Duration
}

// Version is a firmware version, printed as "v%d.%02x".
type Version struct{ Major, Minor byte }

func (v Version) String() string { return fmt.Sprintf("v%d.%02x", v.Major, v.Minor) }

// Battery is what cmd 4 reports: level, charging and millivolts in 4 bytes.
// With Direct set, the reply has 6: byte 9 is 1 and byte 10 repeats the level.
type Battery struct {
	Level      byte
	Charging   bool
	MilliVolts uint16
	Direct     bool
}

// Behavior is how the device answers beyond its flash: writes, unknown
// commands and bad checksums (open until H1), pushes, the radio, and how it
// shares replies between clients. EM11Pro sets what H0 measured.
type Behavior struct {
	Echo        Echo
	DoubleWrite bool   // every cmd 7 is answered twice
	Unknown     Answer // AnswerNormally counts as AnswerSilence here
	BadChecksum Answer
	ClearSilent bool // cmd 9 resets the flash but sends no reply
	PushOnline  bool // sleep and wake push an unsolicited cmd-3 report
	SilentDPI   bool // the DPI button changes the stage without a StatusChanged push
	ShortNAK    bool // a NAK echoes the command and address but not the length
	// RadioRxVersion sends cmd 29 over the radio, so the mouse answers it
	// (a NAK when RxVersion is nil) and nothing does while it sleeps.
	RadioRxVersion bool
	// OneAtATime drops the pending reply of a radio request when another
	// radio request reaches the receiver before that reply went out.
	OneAtATime bool
	// Loss is the share of radio replies that never arrive, drawn from the
	// bus seed. The request still reaches the mouse.
	Loss    float64
	Sharing Sharing
}

// Echo is the reply to cmd 7.
type Echo uint8

const (
	EchoFull   Echo = iota // the request itself
	EchoHeader             // command, address and length, no data
	EchoNone               // no reply
)

// Answer is how the receiver treats a packet it cannot take.
type Answer uint8

const (
	AnswerNAK      Answer = iota // status 1, with the request's header
	AnswerSilence                // no reply
	AnswerNormally               // handled as if it were valid
)

// Sharing is how replies reach the clients that have an interface open.
type Sharing uint8

const (
	Broadcast Sharing = iota // every client sees every report
	Steal                    // each report goes to one client, picked by the bus seed
)

// Latency delays replies: Receiver for those the receiver makes itself (cmds 3
// and 29 and NAKs), Mouse for those that cross the radio. Jitter adds up to
// that much more, drawn from the bus seed. A curve, when set, replaces the
// fixed delay and the jitter of its side. Zero delivers a reply before the
// write that caused it returns.
type Latency struct {
	Receiver      time.Duration
	Mouse         time.Duration
	Jitter        time.Duration
	ReceiverCurve Curve
	MouseCurve    Curve
}

// Curve is a delay distribution given by points of its cumulative
// distribution, P rising from 0 to 1: a draw picks a uniform P and
// interpolates the delay between the points around it.
type Curve []Quantile

type Quantile struct {
	P float64
	D time.Duration
}

func (c Curve) at(u float64) time.Duration {
	if len(c) == 0 {
		return 0
	}
	for i := 1; i < len(c); i++ {
		lo, hi := c[i-1], c[i]
		if u > hi.P {
			continue
		}
		if hi.P <= lo.P {
			return hi.D
		}
		f := (u - lo.P) / (hi.P - lo.P)
		return lo.D + time.Duration(f*float64(hi.D-lo.D))
	}
	return c[len(c)-1].D
}
