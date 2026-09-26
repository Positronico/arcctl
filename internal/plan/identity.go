package plan

type Identity struct {
	CID, MID    byte
	Addr        [3]byte
	AddrTrusted bool
	VID, PID    uint16
}

func (id Identity) Key() string {
	if id.AddrTrusted {
		return hexBytes(id.CID, id.MID) + "-" + hexBytes(id.Addr[:]...)
	}
	return hexBytes(byte(id.VID>>8), byte(id.VID)) + "-" + hexBytes(byte(id.PID>>8), byte(id.PID)) +
		"-" + hexBytes(id.CID, id.MID)
}

func hexBytes(b ...byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&0x0F])
	}
	return string(out)
}
