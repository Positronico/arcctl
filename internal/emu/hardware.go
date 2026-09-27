package emu

import (
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

// EM11ProSleep is how long the H0 unit stayed awake after its last input.
const EM11ProSleep = 13 * time.Second

// EM11Pro is the maintainer's EM11 Pro (7B04, mid 4) on its 260D:1282
// receiver as the read-only hardware stage H0 found it (mouse v1.26,
// receiver v1.0, wireless 1 kHz), holding im, or the model's defaults when
// im is nil. Interface 1 answers and interface 0 stays silent. The receiver
// answers cmd 3 itself, also while the mouse sleeps; cmd 29 crosses the
// radio, so the awake mouse NAKs it and nothing answers while it sleeps.
// cmds 14 and 23 get a NAK. The mouse sleeps EM11ProSleep after its last
// input or radio packet, and no frame marks sleep, wake or a screen lock.
// A radio request that reaches the receiver before the reply to the previous
// one drops that reply. The address is the placeholder 11 22 33, not the
// unit's. H0 could not tell whether a DPI press pushes StatusChanged, so the
// push stays on (Behavior.SilentDPI turns it off), and no try of a single
// client went unanswered, so Loss is 0. Replies come at once;
// EM11ProLatency adds the measured delays.
func EM11Pro(im *flash.Image) Config {
	m, _ := catalog.ByKey("7B04")
	return Config{
		VID: defaultVID,
		PID: defaultPID,
		Mouse: &Mouse{
			Model:      m,
			Image:      im,
			Firmware:   Version{Major: 1, Minor: 0x26},
			Battery:    Battery{Level: 100, MilliVolts: 4144},
			SleepAfter: EM11ProSleep,
		},
		Behavior: Behavior{RadioRxVersion: true, OneAtATime: true},
	}
}

// EM11ProLatency is the reply latency H0 measured with one client: the
// receiver's own cmd-3 replies in 2 to 6 ms (134 replies, max 10 ms), and
// radio replies around 12 ms with a tail to 56 ms (2,810 reads: p50
// 12.1 ms, p99 43.7 ms).
func EM11ProLatency() Latency {
	return Latency{
		ReceiverCurve: Curve{
			{0, ms(1.5)}, {0.05, ms(2)}, {0.25, ms(2.7)}, {0.5, ms(3.5)}, {0.75, ms(4.1)},
			{0.9, ms(5.1)}, {0.95, ms(5.5)}, {0.99, ms(6.5)}, {1, ms(10.1)},
		},
		MouseCurve: Curve{
			{0, ms(6)}, {0.05, ms(11.2)}, {0.25, ms(11.9)}, {0.5, ms(12.1)}, {0.75, ms(14.5)},
			{0.9, ms(16.4)}, {0.95, ms(20)}, {0.98, ms(32.6)}, {0.99, ms(43.7)}, {1, ms(55.8)},
		},
	}
}

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }
