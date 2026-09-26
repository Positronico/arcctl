package mouse

import (
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
)

type KeyType uint8

const (
	TypeDisable KeyType = iota
	TypeMouse
	TypeDPI
	TypeScroll
	TypeFire
	TypeShortcut
	TypeMacro
	TypeRateSwitch
	TypeDragScroll
	TypeProfile
	TypeDPILock
	TypeWheel
)

var keyTypeNames = [...]string{
	"disable", "mouse button", "dpi", "scroll left/right", "fire key", "shortcut",
	"macro", "rate switch", "drag scroll", "profile switch", "dpi lock", "scroll up/down",
}

func (t KeyType) String() string {
	if int(t) < len(keyTypeNames) {
		return keyTypeNames[t]
	}
	return "KeyType(" + strconv.Itoa(int(t)) + ")"
}

const (
	ParamLeft        uint16 = 0x0100
	ParamRight       uint16 = 0x0200
	ParamMiddle      uint16 = 0x0400
	ParamBack        uint16 = 0x0800
	ParamForward     uint16 = 0x1000
	ParamDPICycle    uint16 = 0x0100
	ParamDPIUp       uint16 = 0x0200
	ParamDPIDown     uint16 = 0x0300
	ParamScrollLeft  uint16 = 0x0100
	ParamScrollRight uint16 = 0x0200
	ParamScrollUp    uint16 = 0x0100
	ParamScrollDown  uint16 = 0x0200
	ParamDragScroll  uint16 = 0x0500
)

const (
	minFireInterval = 10
	maxFireTimes    = 3
	maxCycles       = 250
)

type KeyFn struct {
	Type  KeyType
	Param uint16
}

func EncodeKeyFn(k KeyFn) (flash.Record, error) {
	if err := k.Check(); err != nil {
		return nil, err
	}
	hi, lo := byte(k.Param>>8), byte(k.Param)
	if k.Type == TypeDPILock {
		return flash.NewRecord(byte(k.Type), lo, hi), nil
	}
	return flash.NewRecord(byte(k.Type), hi, lo), nil
}

func DecodeKeyFn(b []byte) (KeyFn, error) {
	r, err := record(b, recordSize)
	if err != nil {
		return KeyFn{}, err
	}
	k := KeyFn{Type: KeyType(r[0]), Param: uint16(r[1])<<8 | uint16(r[2])}
	if k.Type == TypeDPILock {
		k.Param = uint16(r[1]) | uint16(r[2])<<8
	}
	if p := k.problem(); p != "" {
		return KeyFn{}, newError(ErrInvalid, p)
	}
	return k, nil
}

func (k KeyFn) Check() error {
	if p := k.problem(); p != "" {
		return newError(ErrValue, p)
	}
	return nil
}

func (k KeyFn) problem() string {
	hi, lo := k.Param>>8, k.Param&0xFF
	ok := false
	switch k.Type {
	case TypeDisable, TypeShortcut, TypeRateSwitch, TypeProfile:
		ok = k.Param == 0
	case TypeMouse:
		ok = k.Param == ParamLeft || k.Param == ParamRight || k.Param == ParamMiddle ||
			k.Param == ParamBack || k.Param == ParamForward
	case TypeDPI:
		ok = k.Param == ParamDPICycle || k.Param == ParamDPIUp || k.Param == ParamDPIDown
	case TypeScroll, TypeWheel:
		ok = k.Param == 0x0100 || k.Param == 0x0200
	case TypeFire:
		ok = hi >= minFireInterval && lo <= maxFireTimes
	case TypeMacro:
		ok = hi < Slots && validCycle(int(lo))
	case TypeDragScroll:
		ok = k.Param == ParamDragScroll
	case TypeDPILock:
		ok = k.Param <= maxRaw
	default:
		return "unknown key type " + strconv.Itoa(int(k.Type))
	}
	if ok {
		return ""
	}
	return "key type " + k.Type.String() + " does not take param " + hex16(k.Param)
}

func FireKey(interval, times int) (KeyFn, error) {
	if interval < minFireInterval || interval > 0xFF || times < 0 || times > maxFireTimes {
		return KeyFn{}, newError(ErrValue, "fire key interval "+strconv.Itoa(interval)+", times "+strconv.Itoa(times))
	}
	return KeyFn{Type: TypeFire, Param: uint16(interval)<<8 | uint16(times)}, nil
}

func MacroBinding(slot, cycle int) (KeyFn, error) {
	if slot < 0 || slot >= Slots || !validCycle(cycle) {
		return KeyFn{}, newError(ErrValue, "macro binding slot "+strconv.Itoa(slot)+", cycle "+strconv.Itoa(cycle))
	}
	return KeyFn{Type: TypeMacro, Param: uint16(slot)<<8 | uint16(cycle)}, nil
}

func validCycle(c int) bool { return c >= 1 && c <= maxCycles || c >= 253 && c <= 255 }

func DPILock(s *catalog.Sensor, dpi int) (KeyFn, error) {
	raw, flags, err := encodeAxis(s, dpi)
	if err != nil {
		return KeyFn{}, err
	}
	if flags != 0 {
		return KeyFn{}, newError(ErrValue, "DPI lock "+strconv.Itoa(dpi)+" is above the first range of sensor "+s.ID)
	}
	return KeyFn{Type: TypeDPILock, Param: raw}, nil
}

func (k KeyFn) LockedDPI(s *catalog.Sensor) (int, bool) {
	if k.Type != TypeDPILock || k.Param > maxRaw || s == nil || len(s.Ranges) == 0 {
		return 0, false
	}
	return decodeAxis(s, k.Param, 0)
}
