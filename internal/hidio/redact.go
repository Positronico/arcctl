package hidio

import (
	"io"
	"slices"

	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// The mouse's shortcut and macro slots, whose bytes are the user's own
// content and never go into a committed transcript, except the event count
// of each macro header: the session reads the events it announces, so a
// replay needs it.
const (
	privateStart = mouse.AddrShortcutKey
	privateEnd   = mouse.AddrMacro + mouse.Slots*mouse.MacroSize
	macroCount   = 31
)

const (
	onlineAddrMask = 1<<6 | 1<<7 | 1<<8
	checksumBit    = 1 << (wire.Size - 1)
	dataShift      = 5
)

// Redact masks what a committed transcript must not reveal:
//   - the address in cmd-3 replies;
//   - the shortcut and macro bytes in cmd-7 packets and cmd-8 replies, or all
//     their data when the packet carries the keyboard flag;
//   - every data byte of an input report that is not a report-8 frame, since
//     keyboard, consumer and mouse reports carry what the user typed, the keys
//     a shortcut button sends and the keystrokes a macro plays.
//
// The checksum of a redacted packet is masked too, since it gives away the
// sum of the hidden bytes. Times become UTC, so that the transcript does not
// tell the local time zone.
func Redact(e Entry) Entry {
	e.At = e.At.UTC()
	if e.ID != wire.ReportID || len(e.Data.Bytes) != wire.Size {
		if e.Dir == DirIn {
			e.Data = maskAll(e.Data)
		}
		return e
	}
	var p wire.Packet
	copy(p[:], e.Data.Bytes)
	var m uint64
	switch p.Cmd() {
	case wire.CmdOnline:
		if e.Dir == DirIn {
			m = onlineAddrMask
		}
	case wire.CmdWrite:
		m = privateData(p)
	case wire.CmdRead:
		if e.Dir == DirIn {
			m = privateData(p)
		}
	}
	if m == 0 {
		return e
	}
	m |= checksumBit
	b := slices.Clone(e.Data.Bytes)
	for i := range b {
		if m&(1<<i) != 0 {
			b[i] = 0
		}
	}
	e.Data = Frame{Bytes: b, Mask: e.Data.Mask | m}
	return e
}

func privateData(p wire.Packet) uint64 {
	var m uint64
	for i := range min(p.Len(), wire.MaxData) {
		if p.Target() == wire.Keyboard || private(int(p.Addr())+i) {
			m |= 1 << (dataShift + i)
		}
	}
	return m
}

func private(a int) bool {
	if a < privateStart || a >= privateEnd {
		return false
	}
	return a < mouse.AddrMacro || (a-mouse.AddrMacro)%mouse.MacroSize != macroCount
}

func maskAll(f Frame) Frame {
	n := min(len(f.Bytes), maxFrame)
	m := uint64(1)<<n - 1
	if n == maxFrame {
		m = ^uint64(0)
	}
	b := make([]byte, len(f.Bytes))
	return Frame{Bytes: b, Mask: f.Mask | m}
}

// RedactTranscript copies a transcript with every entry passed through Redact.
func RedactTranscript(dst io.Writer, src io.Reader) error {
	h, entries, err := ReadTranscript(src)
	if err != nil {
		return err
	}
	h.Redacted = true
	h.Started = h.Started.UTC()
	enc := newEncoder(dst)
	if err := enc.Encode(h); err != nil {
		return err
	}
	for _, e := range entries {
		if err := enc.Encode(Redact(e)); err != nil {
			return err
		}
	}
	return nil
}
