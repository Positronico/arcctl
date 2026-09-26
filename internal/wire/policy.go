package wire

import "strconv"

// Policy is the set of commands a session may send. The zero value is ReadOnly.
type Policy uint8

const (
	ReadOnly Policy = iota
	Edit
	Reset
	Experimental
)

func (p Policy) valid() bool { return p <= Experimental }

func (p Policy) String() string {
	switch p {
	case ReadOnly:
		return "read-only"
	case Edit:
		return "edit"
	case Reset:
		return "reset"
	case Experimental:
		return "experimental"
	}
	return "policy " + strconv.Itoa(int(p))
}

// Allows reports whether p lets c go to t. Keyboards only get the read commands (D7).
func (p Policy) Allows(c Cmd, t Target) bool {
	if !p.valid() || !t.valid() {
		return false
	}
	switch c {
	case CmdHandshake, CmdOnline, CmdBattery, CmdRead, CmdGetProfile, CmdFWVersion, CmdGetLongRange, CmdRxVersion:
		return true
	}
	if t != Mouse {
		return false
	}
	switch p {
	case Edit:
		return c == CmdWrite
	case Reset:
		return c == CmdClear
	case Experimental:
		return c == CmdWrite || c == CmdSetLongRange
	}
	return false
}

func (p Policy) Check(pk Packet, t Target) error {
	c := pk.Cmd()
	switch {
	case !t.valid():
		return newError(ErrTarget, c.String()+": invalid session "+t.String())
	case !p.Allows(c, t):
		return newError(ErrForbidden, c.String()+" under "+p.String()+" for the "+t.String())
	case pk.Target() != t:
		return newError(ErrTarget, c.String()+": packet flag says "+pk.Target().String()+", session target is "+t.String())
	}
	if err := pk.framing(); err != nil {
		return err
	}
	if err := pk.requestShape(); err != nil {
		return err
	}
	if !pk.Valid() {
		return newError(ErrChecksum, c.String()+": byte 15 is 0x"+hexByte(pk[Size-1])+", want 0x"+hexByte(pk.Checksum()))
	}
	return nil
}

// requestShape checks the fixed request layouts in docs/protocol.md on top of the
// framing that replies share: no data for the plain queries and cmd 9, the 8-byte
// handshake, and cmd 22 as [0 or 1, then 9 zero bytes].
func (p Packet) requestShape() error {
	c, n := p.Cmd(), p.Len()
	want := -1
	switch c {
	case CmdOnline, CmdBattery, CmdClear, CmdGetProfile, CmdFWVersion, CmdGetLongRange, CmdRxVersion:
		want = 0
	case CmdHandshake:
		want = 8
	case CmdSetLongRange:
		want = MaxData
		if n == MaxData && (p[5] > 1 || !zero(p[6:5+MaxData])) {
			return newError(ErrFraming, c.String()+": data must be [0 or 1, then zeros]")
		}
	}
	if want >= 0 && n != want {
		return newError(ErrFraming, c.String()+": length "+strconv.Itoa(n)+", want "+strconv.Itoa(want))
	}
	return nil
}

func zero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
