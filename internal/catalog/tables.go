package catalog

import (
	"slices"
	"strconv"
)

type PIDClass uint8

const (
	ClassUnknown PIDClass = iota
	ClassMouse
	ClassKeyboard
	ClassComposite
)

var classNames = [...]string{"unknown", "mouse", "keyboard", "composite"}

func (c PIDClass) String() string {
	if int(c) < len(classNames) {
		return classNames[c]
	}
	return "PIDClass(" + strconv.Itoa(int(c)) + ")"
}

func (c PIDClass) Group() string {
	switch c {
	case ClassMouse:
		return GroupMouse
	case ClassKeyboard:
		return GroupKeyboard
	case ClassComposite:
		return GroupOfficeMouse
	}
	return ""
}

type productID struct {
	pid   uint16
	class PIDClass
}

type USBID struct {
	VID, PID uint16
	Class    PIDClass
}

func Classify(vid, pid uint16) PIDClass {
	if !slices.Contains(vendorIDs, vid) {
		return ClassUnknown
	}
	for _, p := range productIDs {
		if p.pid == pid {
			return p.class
		}
	}
	return ClassUnknown
}

func USBIDs() []USBID {
	ids := make([]USBID, 0, len(vendorIDs)*len(productIDs))
	for _, vid := range vendorIDs {
		for _, p := range productIDs {
			ids = append(ids, USBID{vid, p.pid, p.class})
		}
	}
	return ids
}

type Sensor struct {
	ID     string
	Ranges []Range
	Values []uint16
	Gates  []string
}

type Range struct {
	Min, Max, Step int
	DPIEx          byte
}

func (s *Sensor) HasGate(name string) bool { return slices.Contains(s.Gates, name) }

func Sensors() []*Sensor { return slices.Clone(sensors) }

func SensorByID(id string) (*Sensor, bool) {
	i := slices.IndexFunc(sensors, func(s *Sensor) bool { return s.ID == id })
	if i < 0 {
		return nil, false
	}
	return sensors[i], true
}

type Usage struct {
	Code  uint16
	Name  string
	Label string
}

func Media(code uint16) (Usage, bool) {
	i := slices.IndexFunc(usages, func(u Usage) bool { return u.Code == code })
	if i < 0 {
		return Usage{}, false
	}
	return usages[i], true
}

func MouseMedia() []Usage { return mediaList(mouseMedia) }

func KeyboardMedia() []Usage { return mediaList(keyboardMedia) }

func mediaList(codes []uint16) []Usage {
	out := make([]Usage, len(codes))
	for i, c := range codes {
		out[i], _ = Media(c)
	}
	return out
}

func Label(key string) (string, bool) {
	s, ok := labels[key]
	return s, ok
}

type Tool struct {
	Name    string
	Version string
	SHA256  string
}

type Input struct {
	Path   string
	SHA256 string
}

func Tools() []Tool { return slices.Clone(tools) }

func Inputs() []Input { return slices.Clone(inputs) }
