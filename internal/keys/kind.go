package keys

import (
	"strconv"
	"strings"
)

const (
	KindModifier Kind = 0
	KindKey      Kind = 1
	KindConsumer Kind = 2
	KindMouse    Kind = 4
	KindMenu     Kind = 7
)

var kindNames = map[Kind]string{
	KindModifier: "modifier",
	KindKey:      "key",
	KindConsumer: "consumer",
	KindMouse:    "mouse",
	KindMenu:     "menu",
}

func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

type OS uint8

const (
	Win OS = iota
	Mac
)

func (o OS) String() string {
	switch o {
	case Win:
		return "win"
	case Mac:
		return "mac"
	}
	return "OS(" + strconv.Itoa(int(o)) + ")"
}

type Modifier uint8

const (
	LCtrl Modifier = 1 << iota
	LShift
	LAlt
	LMeta
	RCtrl
	RShift
	RAlt
	RMeta
)

var modifierNames = [8]struct{ id, win, mac string }{
	{"LCtrl", "Ctrl", "Ctrl"},
	{"LShift", "Shift", "Shift"},
	{"LAlt", "Alt", "Option"},
	{"LMeta", "Win", "Cmd"},
	{"RCtrl", "RCtrl", "RCtrl"},
	{"RShift", "RShift", "RShift"},
	{"RAlt", "RAlt", "ROption"},
	{"RMeta", "RWin", "RCmd"},
}

func (m Modifier) Right() bool { return m&(RCtrl|RShift|RAlt|RMeta) != 0 }

func (m Modifier) Stroke() Stroke { return Stroke{KindModifier, uint16(m)} }

func (m Modifier) Name(os OS) string {
	return m.join("+", func(i int) string {
		if os == Mac {
			return modifierNames[i].mac
		}
		return modifierNames[i].win
	})
}

func (m Modifier) String() string {
	if m == 0 {
		return "Modifier(0)"
	}
	return m.join("|", func(i int) string { return modifierNames[i].id })
}

func (m Modifier) join(sep string, name func(int) string) string {
	var parts []string
	for i := range modifierNames {
		if m&(1<<i) != 0 {
			parts = append(parts, name(i))
		}
	}
	return strings.Join(parts, sep)
}
