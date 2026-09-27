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
}

// Version is a firmware version, printed as "v%d.%02x".
type Version struct{ Major, Minor byte }

func (v Version) String() string { return fmt.Sprintf("v%d.%02x", v.Major, v.Minor) }

// Battery is what cmd 4 reports. With Direct set, reply byte 9 is 1 and the
// level is repeated in byte 10.
type Battery struct {
	Level      byte
	Charging   bool
	MilliVolts uint16
	Direct     bool
}

// Behavior settles what the hardware tests have not: how the device answers
// writes, unknown commands and bad checksums, and how it shares replies
// between clients.
type Behavior struct {
	Echo        Echo
	DoubleWrite bool   // every cmd 7 is answered twice
	Unknown     Answer // AnswerNormally counts as AnswerSilence here
	BadChecksum Answer
	ClearSilent bool // cmd 9 resets the flash but sends no reply
	PushOnline  bool // sleep and wake push an unsolicited cmd-3 report
	ShortNAK    bool // a NAK echoes the command and address but not the length
	Sharing     Sharing
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
// that much more, drawn from the bus seed. Zero delivers a reply before the
// write that caused it returns.
type Latency struct {
	Receiver time.Duration
	Mouse    time.Duration
	Jitter   time.Duration
}
