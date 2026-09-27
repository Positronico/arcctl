package emu

import (
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/wire"
)

// respond gives the replies the receiver and mouse make to p.
func (d *Device) respond(p wire.Packet) []reply {
	if p.Target() != wire.Mouse {
		return nil
	}
	if !p.Valid() {
		switch d.behavior.BadChecksum {
		case AnswerNAK:
			return []reply{{nak(p, d.behavior.ShortNAK), false}}
		case AnswerSilence:
			return nil
		}
	}
	switch p.Cmd() {
	case wire.CmdOnline:
		return []reply{{d.online(), false}}
	case wire.CmdRxVersion:
		radio := d.behavior.RadioRxVersion
		if radio {
			if m := d.mouse; m == nil || !m.awake {
				return nil
			}
			d.keepAwake()
		}
		if d.rxVersion == nil {
			return []reply{{nak(p, d.behavior.ShortNAK), radio}}
		}
		return []reply{{frame(wire.CmdRxVersion, 0, []byte{d.rxVersion.Major, d.rxVersion.Minor}), radio}}
	}
	if !radioCmds[p.Cmd()] {
		if d.behavior.Unknown == AnswerNAK {
			return []reply{{nak(p, d.behavior.ShortNAK), false}}
		}
		return nil
	}
	m := d.mouse
	if m == nil || !m.awake {
		return nil
	}
	d.keepAwake()
	var out []reply
	for _, r := range m.respond(p, d.behavior) {
		out = append(out, reply{r, true})
	}
	return out
}

var radioCmds = map[wire.Cmd]bool{
	wire.CmdHandshake:    true,
	wire.CmdBattery:      true,
	wire.CmdWrite:        true,
	wire.CmdRead:         true,
	wire.CmdClear:        true,
	wire.CmdGetProfile:   true,
	wire.CmdFWVersion:    true,
	wire.CmdSetLongRange: true,
	wire.CmdGetLongRange: true,
}

func (m *mouseState) respond(p wire.Packet, bh Behavior) []wire.Packet {
	c := p.Cmd()
	switch c {
	case wire.CmdHandshake:
		return one(frame(c, 0, []byte{p[5], p[6], p[7], p[8], m.cid, m.mid, m.conn, 0}))
	case wire.CmdBattery:
		b := m.battery
		data := []byte{b.Level, flag(b.Charging), byte(b.MilliVolts >> 8), byte(b.MilliVolts)}
		if b.Direct {
			data = append(data, 1, b.Level)
		}
		return one(frame(c, 0, data))
	case wire.CmdRead:
		e, ok := extent(p)
		if !ok {
			return one(nak(p, bh.ShortNAK))
		}
		data, _ := m.image.Get(e)
		return one(frame(c, p.Addr(), data))
	case wire.CmdWrite:
		e, ok := extent(p)
		if !ok {
			return one(nak(p, bh.ShortNAK))
		}
		_ = m.image.Set(e.Addr, p[5:5+e.Len])
		var echo wire.Packet
		switch bh.Echo {
		case EchoFull:
			echo = p
		case EchoHeader:
			echo = frame(c, p.Addr(), nil)
			echo[4] = p[4]
			echo[wire.Size-1] = echo.Checksum()
		default:
			return nil
		}
		if bh.DoubleWrite {
			return []wire.Packet{echo, echo}
		}
		return one(echo)
	case wire.CmdClear:
		m.image = m.factory.Clone()
		if bh.ClearSilent {
			return nil
		}
		return one(frame(c, 0, nil))
	case wire.CmdGetProfile:
		if m.profile == nil {
			return one(nak(p, bh.ShortNAK))
		}
		return one(frame(c, 0, []byte{*m.profile}))
	case wire.CmdFWVersion:
		return one(frame(c, 0, []byte{m.firmware.Major, m.firmware.Minor}))
	case wire.CmdGetLongRange:
		if m.longRange == nil {
			return one(nak(p, bh.ShortNAK))
		}
		return one(frame(c, 0, []byte{flag(*m.longRange)}))
	case wire.CmdSetLongRange:
		if m.longRange == nil || p.Len() != wire.MaxData || p[5] > 1 {
			return one(nak(p, bh.ShortNAK))
		}
		*m.longRange = p[5] == 1
		return one(frame(c, 0, []byte{p[5]}))
	}
	return nil
}

func extent(p wire.Packet) (flash.Extent, bool) {
	e := flash.Extent{Addr: int(p.Addr()), Len: p.Len()}
	return e, e.Len > 0 && e.Len <= wire.MaxData && e.End() <= flash.Size
}

func one(p wire.Packet) []wire.Packet { return []wire.Packet{p} }

func flag(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// frame builds a reply as the device frames it: wire.Build builds requests
// only, and a read request carries no data.
func frame(c wire.Cmd, addr uint16, data []byte) wire.Packet {
	var p wire.Packet
	p[0] = byte(c)
	p[2], p[3] = byte(addr>>8), byte(addr)
	p[4] = byte(len(data))
	copy(p[5:wire.Size-1], data)
	p[wire.Size-1] = p.Checksum()
	return p
}

// nak is the status-1 reply to p: its header with no data, without the
// length when short.
func nak(p wire.Packet, short bool) wire.Packet {
	var q wire.Packet
	copy(q[:5], p[:5])
	q[1] = byte(wire.StatusNAK)
	if short {
		q[4] &^= 0x0F
	}
	q[wire.Size-1] = q.Checksum()
	return q
}

// statusChanged is a push as the EM11 Pro sends it: 10 data bytes, the two
// flag bytes first.
func statusChanged(f1, f2 byte) wire.Packet {
	return frame(wire.CmdStatusChanged, 0, []byte{f1, f2, 0, 0, 0, 0, 0, 0, 0, 0})
}
