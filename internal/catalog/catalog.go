package catalog

//go:generate go run ./gen -facts facts -verified verified.json -root ../..

import (
	"slices"
	"strconv"
)

const (
	GroupMouse       = "mouse"
	GroupOfficeMouse = "officeMouse"
	GroupKeyboard    = "keyboard"
)

type Family uint8

const (
	FamilyMouse Family = iota + 1
	FamilyKeyboard
)

func (f Family) String() string {
	switch f {
	case FamilyMouse:
		return "mouse"
	case FamilyKeyboard:
		return "keyboard"
	}
	return "Family(" + strconv.Itoa(int(f)) + ")"
}

type Model struct {
	Key              string
	Name             string
	Alias            string
	Group            string
	CID              byte
	MIDs             []byte
	Family           Family
	PairCID          byte
	Sensor           *Sensor
	MaxDPI           int
	Stages           int
	Buttons          []Button
	UI               UIFlags
	LongRangeDefault *bool
	Defaults         *MouseDefaults
	Keyboard         *KeyboardSpec
}

type Button struct {
	Slot    int
	Type    byte
	Param   uint16
	Media   uint16
	Visible bool
	Label   string
}

type UIFlags struct {
	Office         bool
	GameRoller     bool
	Flywheel       bool
	ProfilesUI     bool
	DPILight       bool
	RatePanel      bool
	OSSwitchLocked bool
}

type MouseDefaults struct {
	DPIs             []DPIStage
	CurrentDPI       int
	Debounce         int
	TipsDebounce     int
	MaxDebounce      int
	ReportRate       int
	SensorMode       int
	LOD              *int
	PerformanceState bool
	Performance      int
	Ripple           bool
	Angle            bool
	MotionSync       bool
	SleepTime        int
	DPIEffect        DPIEffect
	LightEffect      LightEffect
	Firmware         map[string]string
}

type DPIStage struct {
	DPI   int
	Color [3]byte
}

type DPIEffect struct {
	Mode       int
	Brightness int
	Speed      int
	Show       bool
}

type LightEffect struct {
	Mode           int
	Brightness     int
	Speed          int
	MovingOffState bool
}

type KeyboardSpec struct {
	Systems []string
	Layouts []string
	Width   int
	Height  int
	Rects   []Rect
	Maps    []Layer
}

type Rect struct {
	Left, Top, Width, Height int
}

type Layer struct {
	System string
	Layout string
	Slots  []KeySlot
}

type KeySlot struct {
	Type  byte
	Value uint16
}

func (k *KeyboardSpec) Layer(system, layout string) (*Layer, bool) {
	for i := range k.Maps {
		if l := &k.Maps[i]; l.System == system && l.Layout == layout {
			return l, true
		}
	}
	return nil, false
}

func (k *KeyboardSpec) OnImage(slot int) bool {
	if slot < 0 || slot >= len(k.Rects) {
		return false
	}
	r := k.Rects[slot]
	return r.Left < k.Width && r.Top < k.Height && r.Left+r.Width > 0 && r.Top+r.Height > 0
}

var models = applyOverlay(factModels)

func Models() []*Model { return slices.Clone(models) }

func ByKey(key string) (*Model, bool) {
	i := slices.IndexFunc(models, func(m *Model) bool { return m.Key == key })
	if i < 0 {
		return nil, false
	}
	return models[i], true
}

func Resolve(cid, mid byte) (*Model, bool) {
	for _, m := range models {
		if m.CID == cid && slices.Contains(m.MIDs, mid) {
			return m, true
		}
	}
	return nil, false
}

func ref[T any](v T) *T { return &v }
