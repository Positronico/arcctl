package wire

import "strconv"

type Cmd byte

const (
	CmdHandshake     Cmd = 1
	CmdDriverStatus  Cmd = 2
	CmdOnline        Cmd = 3
	CmdBattery       Cmd = 4
	CmdPair          Cmd = 5
	CmdPairState     Cmd = 6
	CmdWrite         Cmd = 7
	CmdRead          Cmd = 8
	CmdClear         Cmd = 9
	CmdStatusChanged Cmd = 10
	CmdGetProfile    Cmd = 14
	CmdSetProfile    Cmd = 15
	CmdFWVersion     Cmd = 18
	CmdSetRxLED4K    Cmd = 20
	CmdGetRxLED4K    Cmd = 21
	CmdSetLongRange  Cmd = 22
	CmdGetLongRange  Cmd = 23
	CmdSetRxLEDBar   Cmd = 24
	CmdGetRxLEDBar   Cmd = 25
	CmdRxVersion     Cmd = 29
	CmdSetRxLED3     Cmd = 44
	CmdGetRxLED3     Cmd = 45
)

var cmdNames = map[Cmd]string{
	CmdHandshake:     "handshake",
	CmdDriverStatus:  "driver-status",
	CmdOnline:        "online",
	CmdBattery:       "battery",
	CmdPair:          "pair",
	CmdPairState:     "pair-state",
	CmdWrite:         "write",
	CmdRead:          "read",
	CmdClear:         "clear",
	CmdStatusChanged: "status-changed",
	CmdGetProfile:    "get-profile",
	CmdSetProfile:    "set-profile",
	CmdFWVersion:     "fw-version",
	CmdSetRxLED4K:    "set-rx-led-4k",
	CmdGetRxLED4K:    "get-rx-led-4k",
	CmdSetLongRange:  "set-long-range",
	CmdGetLongRange:  "get-long-range",
	CmdSetRxLEDBar:   "set-rx-led-bar",
	CmdGetRxLEDBar:   "get-rx-led-bar",
	CmdRxVersion:     "rx-version",
	CmdSetRxLED3:     "set-rx-led-3",
	CmdGetRxLED3:     "get-rx-led-3",
}

func (c Cmd) String() string {
	s := "cmd " + strconv.Itoa(int(c))
	if name, ok := cmdNames[c]; ok {
		s += " (" + name + ")"
	}
	return s
}

func (c Cmd) addressed() bool {
	return c == CmdWrite || c == CmdRead
}
