package wire

import "strconv"

const (
	ReportID = 8
	Size     = 16
	MaxData  = 10
)

const (
	lenMask      = 0x0F
	reservedMask = 0x70
	flagMask     = 0x80
)

type Packet [Size]byte

type Target byte

const (
	Mouse    Target = 0x00
	Keyboard Target = 0x80
)

func (t Target) valid() bool { return t == Mouse || t == Keyboard }

func (t Target) String() string {
	switch t {
	case Mouse:
		return "mouse"
	case Keyboard:
		return "keyboard"
	}
	return "target 0x" + hexByte(byte(t))
}

type Status byte

const (
	StatusOK  Status = 0
	StatusNAK Status = 1
)

func (s Status) Err() error {
	switch s {
	case StatusOK:
		return nil
	case StatusNAK:
		return ErrNAK
	}
	return newError(ErrStatus, "status 0x"+hexByte(byte(s)))
}

func Build(t Target, c Cmd, addr uint16, data []byte) (Packet, error) {
	if !t.valid() {
		return Packet{}, newError(ErrTarget, c.String()+": invalid "+t.String())
	}
	if len(data) > MaxData {
		return Packet{}, newError(ErrFraming, c.String()+": "+strconv.Itoa(len(data))+" data bytes, max 10")
	}
	var p Packet
	p[0] = byte(c)
	p[2] = byte(addr >> 8)
	p[3] = byte(addr)
	p[4] = byte(t) | byte(len(data))
	copy(p[5:], data)
	if err := p.framing(); err != nil {
		return Packet{}, err
	}
	p[Size-1] = p.Checksum()
	return p, nil
}

func MustBuild(t Target, c Cmd, addr uint16, data []byte) Packet {
	p, err := Build(t, c, addr, data)
	if err != nil {
		panic(err)
	}
	return p
}

func BuildRead(t Target, addr uint16, n int) (Packet, error) {
	if n < 1 || n > MaxData {
		return Packet{}, newError(ErrFraming, CmdRead.String()+": length "+strconv.Itoa(n)+", want 1 to 10")
	}
	return Build(t, CmdRead, addr, make([]byte, n))
}

func (p Packet) Cmd() Cmd { return Cmd(p[0]) }

func (p Packet) Status() Status { return Status(p[1]) }

func (p Packet) Addr() uint16 { return uint16(p[2])<<8 | uint16(p[3]) }

func (p Packet) Target() Target { return Target(p[4] & flagMask) }

func (p Packet) Len() int { return int(p[4] & lenMask) }

func (p Packet) Data() []byte {
	n := min(p.Len(), MaxData)
	return p[5 : 5+n]
}

// Checksum returns the byte 15 that makes (report ID + all 16 bytes) & 0xFF == 0x55.
func (p Packet) Checksum() byte {
	s := byte(ReportID)
	for _, b := range p[:Size-1] {
		s += b
	}
	return 0x55 - s
}

func (p Packet) Valid() bool { return p[Size-1] == p.Checksum() }

func (p Packet) String() string {
	b := make([]byte, 0, Size*3-1)
	for i, x := range p {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, hexDigits[x>>4], hexDigits[x&0x0F])
	}
	return string(b)
}

// Match reports whether rep answers req. Only writes and reads carry an address;
// their replies echo it along with the length in the low nibble of byte 4.
func Match(req, rep Packet) bool {
	if rep[0] != req[0] {
		return false
	}
	if !req.Cmd().addressed() {
		return true
	}
	return rep[2] == req[2] && rep[3] == req[3] && rep[4]&lenMask == req[4]&lenMask
}

// framing accepts exactly the layouts Build produces: [cmd, 0, aHi, aLo, flag|len, data, 0 padding, cks].
func (p Packet) framing() error {
	c := p.Cmd()
	n := p.Len()
	switch {
	case p[1] != 0:
		return newError(ErrFraming, c.String()+": byte 1 is 0x"+hexByte(p[1])+", want 0")
	case p[4]&reservedMask != 0:
		return newError(ErrFraming, c.String()+": reserved bits set in byte 4 (0x"+hexByte(p[4])+")")
	case n > MaxData:
		return newError(ErrFraming, c.String()+": length "+strconv.Itoa(n)+", max 10")
	case !c.addressed() && (p[2] != 0 || p[3] != 0):
		return newError(ErrFraming, c.String()+": takes no address, got 0x"+hexByte(p[2])+hexByte(p[3]))
	case c.addressed() && n == 0:
		return newError(ErrFraming, c.String()+": length 0")
	}
	pad := p[5+n : Size-1]
	if c == CmdRead {
		pad = p[5 : Size-1]
	}
	for _, b := range pad {
		if b != 0 {
			return newError(ErrFraming, c.String()+": non-zero byte outside the data")
		}
	}
	return nil
}

const hexDigits = "0123456789abcdef"

func hexByte(b byte) string {
	return string([]byte{hexDigits[b>>4], hexDigits[b&0x0F]})
}
