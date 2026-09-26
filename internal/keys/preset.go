package keys

import (
	"cmp"
	"slices"
)

func Presets(os OS) []Preset {
	switch os {
	case Win:
		return WinPresets()
	case Mac:
		return MacPresets()
	}
	return nil
}

func PresetByID(os OS, id string) (Preset, bool) {
	ps := Presets(os)
	i := slices.IndexFunc(ps, func(p Preset) bool { return p.ID == id })
	if i < 0 {
		return Preset{}, false
	}
	return ps[i], true
}

func MatchPreset(os OS, c Combo) (Preset, bool) {
	want := sorted(c)
	for _, p := range Presets(os) {
		if slices.Equal(sorted(p.Combo()), want) {
			return p, true
		}
	}
	return Preset{}, false
}

func (p Preset) Combo() Combo {
	c := make(Combo, len(p.Keys))
	for i, k := range p.Keys {
		c[i] = k.Stroke()
	}
	return c
}

func sorted(c Combo) Combo {
	return slices.SortedFunc(slices.Values(c), func(a, b Stroke) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Value, b.Value))
	})
}
