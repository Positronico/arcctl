package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxLabel          = 40
	maxFlashAddress   = 16383
	maxSensorRawValue = 1023
)

var (
	labelKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+){1,3}$`)
	identPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	presetIDPattern = regexp.MustCompile(`^diy[0-9]+$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sensorIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	groups          = []string{"mouse", "officeMouse", "keyboard"}
	systems         = []string{"win", "mac", "iOS", "android"}
	layouts         = []string{"normal", "fn", "fn2"}
	repeatModes     = []string{"count", "untilPressedAgain", "untilReleased", "untilAnyKey"}
)

type checker struct {
	errs []error
}

func (c *checker) errorf(path, format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
}

func (c *checker) atLeast(path string, v, min int) {
	if v < min {
		c.errorf(path, "%d is below the minimum %d", v, min)
	}
}

func (c *checker) between(path string, v, min, max int) {
	if v < min || v > max {
		c.errorf(path, "%d is outside %d..%d", v, min, max)
	}
}

func (c *checker) nonEmpty(path, s string) {
	if s == "" {
		c.errorf(path, "must not be empty")
	}
}

func (c *checker) oneOf(path, s string, allowed []string) {
	if !slices.Contains(allowed, s) {
		c.errorf(path, "%q is not one of %v", s, allowed)
	}
}

func (c *checker) match(path, s string, re *regexp.Regexp) {
	if !re.MatchString(s) {
		c.errorf(path, "%q does not match %s", s, re)
	}
}

func (c *checker) text(path, s string) {
	n := utf8.RuneCountInString(s)
	switch {
	case n == 0 || n > maxLabel:
		c.errorf(path, "label %q has %d characters, want 1..%d", s, n, maxLabel)
	case strings.TrimSpace(s) != s:
		c.errorf(path, "label %q has leading or trailing space", s)
	case strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
		c.errorf(path, "label %q has a non-printable character", s)
	}
}

func (c *checker) labelKey(path, k string) {
	c.match(path, k, labelKeyPattern)
	if n := utf8.RuneCountInString(k); n > maxLabel {
		c.errorf(path, "label key %q has %d characters, want at most %d", k, n, maxLabel)
	}
}

func (c *checker) labelRef(path, k string, labels map[string]string) {
	c.labelKey(path, k)
	if _, ok := labels[k]; !ok {
		c.errorf(path, "label key %q is not in labels.json", k)
	}
}

func unique[T comparable](c *checker, path string, items []T) {
	seen := make(map[T]bool, len(items))
	for i, it := range items {
		if seen[it] {
			c.errorf(fmt.Sprintf("%s[%d]", path, i), "duplicate %v", it)
		}
		seen[it] = true
	}
}

func minItems[T any](c *checker, path string, items []T, min int) {
	if len(items) < min {
		c.errorf(path, "has %d items, want at least %d", len(items), min)
	}
}

func validate(f *facts) error {
	c := &checker{}
	labels := f.Labels.Labels
	validateLabels(c, labels)
	sensorIDs := validateSensors(c, &f.Sensors)
	usages := validateMedia(c, &f.Media)
	validateModels(c, &f.Models, labels, sensorIDs, usages)
	validateIDs(c, &f.IDs)
	keyByCode := validateKeys(c, &f.Keys)
	validatePresets(c, &f.Presets, labels, keyByCode)
	validateOffsets(c, &f.Offsets)
	validateBrightness(c, &f.Brightness)
	validateSources(c, &f.Sources)
	return errors.Join(c.errs...)
}

func validateLabels(c *checker, labels map[string]string) {
	for _, k := range sortedKeys(labels) {
		p := "labels.json: labels[" + k + "]"
		c.labelKey(p, k)
		c.text(p, labels[k])
	}
}

func validateSensors(c *checker, s *sensorsFile) map[string]bool {
	ids := map[string]bool{}
	minItems(c, "sensors.json: sensors", s.Sensors, 1)
	for i, sn := range s.Sensors {
		p := fmt.Sprintf("sensors.json: sensors[%d]", i)
		c.match(p+".id", sn.ID, sensorIDPattern)
		if ids[sn.ID] {
			c.errorf(p+".id", "duplicate sensor %q", sn.ID)
		}
		ids[sn.ID] = true
		minItems(c, p+".ranges", sn.Ranges, 1)
		for j, r := range sn.Ranges {
			rp := fmt.Sprintf("%s.ranges[%d]", p, j)
			c.atLeast(rp+".min", r.Min, 1)
			c.atLeast(rp+".max", r.Max, 1)
			c.atLeast(rp+".step", r.Step, 1)
			if r.Min > r.Max || r.Step > 0 && (r.Max-r.Min)%r.Step != 0 {
				c.errorf(rp, "min %d, max %d and step %d do not form a range", r.Min, r.Max, r.Step)
			}
			if j > 0 && r.Min <= sn.Ranges[j-1].Max {
				c.errorf(rp, "overlaps the previous range")
			}
		}
		if sn.Values != nil {
			minItems(c, p+".values", sn.Values, 1)
			for j, v := range sn.Values {
				c.between(fmt.Sprintf("%s.values[%d]", p, j), v, 0, maxSensorRawValue)
			}
			if len(sn.Ranges) > 0 && sn.Ranges[0].Step > 0 {
				r := sn.Ranges[0]
				if n := (r.Max-r.Min)/r.Step + 1; len(sn.Values) != n {
					c.errorf(p+".values", "has %d items, the first range has %d steps", len(sn.Values), n)
				}
			}
		}
	}
	for i, g := range s.Gates {
		p := fmt.Sprintf("sensors.json: gates[%d]", i)
		c.nonEmpty(p+".name", g.Name)
		unique(c, p+".sensors", g.Sensors)
		for j, id := range g.Sensors {
			if !ids[id] {
				c.errorf(fmt.Sprintf("%s.sensors[%d]", p, j), "unknown sensor %q", id)
			}
		}
	}
	unique(c, "sensors.json: gates.name", gateNames(s.Gates))
	return ids
}

func gateNames(gs []gate) []string {
	names := make([]string, len(gs))
	for i, g := range gs {
		names[i] = g.Name
	}
	return names
}

func validateMedia(c *checker, m *mediaFile) map[uint16]bool {
	codes := map[uint16]bool{}
	for i, u := range m.Usages {
		p := fmt.Sprintf("media.json: usages[%d]", i)
		if u.Code == 0 {
			c.errorf(p+".code", "usage 0 is reserved for no usage")
		}
		if codes[u.Code] {
			c.errorf(p+".code", "duplicate usage %#04x", u.Code)
		} else if i > 0 && u.Code < m.Usages[i-1].Code {
			c.errorf(p+".code", "usage %#04x is out of order after %#04x", u.Code, m.Usages[i-1].Code)
		}
		codes[u.Code] = true
		c.nonEmpty(p+".name", u.Name)
		c.text(p+".label", u.Label)
	}
	for _, l := range []struct {
		name  string
		codes []uint16
	}{{"mouse", m.Mouse}, {"keyboard", m.Keyboard}} {
		p := "media.json: " + l.name
		unique(c, p, l.codes)
		for i, code := range l.codes {
			if !codes[code] {
				c.errorf(fmt.Sprintf("%s[%d]", p, i), "usage %#04x is not in usages", code)
			}
		}
	}
	return codes
}

func validateModels(c *checker, mf *modelsFile, labels map[string]string, sensors map[string]bool, usages map[uint16]bool) {
	minItems(c, "models.json: models", mf.Models, 1)
	type id struct{ cid, mid uint8 }
	seen := map[id]int{}
	keys := map[string]bool{}
	for i, m := range mf.Models {
		p := fmt.Sprintf("models.json: models[%d]", i)
		c.oneOf(p+".group", m.Group, groups)
		minItems(c, p+".mids", m.MIDs, 1)
		unique(c, p+".mids", m.MIDs)
		for _, mid := range m.MIDs {
			if prev, dup := seen[id{m.CID, mid}]; dup {
				c.errorf(p, "cid %d mid %d is also in models[%d]", m.CID, mid, prev)
			}
			seen[id{m.CID, mid}] = i
		}
		if len(m.MIDs) > 0 {
			k := modelKey(m)
			if keys[k] {
				c.errorf(p, "duplicate model key %s", k)
			}
			keys[k] = true
		}
		if m.Name != nil {
			c.nonEmpty(p+".name", *m.Name)
		}
		if m.Group == "keyboard" {
			if m.Keyboard == nil || m.Mouse != nil {
				c.errorf(p, "a keyboard entry needs keyboard and no mouse")
			}
		} else if m.Mouse == nil || m.Keyboard != nil {
			c.errorf(p, "a mouse entry needs mouse and no keyboard")
		}
		if m.Mouse != nil {
			validateMouse(c, p+".mouse", m.Mouse, labels, sensors, usages)
		}
		if m.Keyboard != nil {
			validateKeyboard(c, p+".keyboard", m.Keyboard, usages)
		}
	}
}

func validateMouse(c *checker, p string, m *mouse, labels map[string]string, sensors map[string]bool, usages map[uint16]bool) {
	if !sensors[m.Sensor] {
		c.errorf(p+".sensor", "unknown sensor %q", m.Sensor)
	}
	c.atLeast(p+".maxDpi", m.MaxDPI, 1)
	if len(m.DPIs) < 1 || len(m.DPIs) > 8 {
		c.errorf(p+".dpis", "has %d stages, want 1..8", len(m.DPIs))
	}
	for i, d := range m.DPIs {
		c.atLeast(fmt.Sprintf("%s.dpis[%d].dpi", p, i), d.DPI, 1)
	}
	c.between(p+".currentDpi", m.CurrentDPI, 0, 7)
	if m.CurrentDPI >= len(m.DPIs) {
		c.errorf(p+".currentDpi", "%d is not a stage index", m.CurrentDPI)
	}
	if len(m.Keys) > 16 {
		c.errorf(p+".keys", "has %d keys, want at most 16", len(m.Keys))
	}
	slots := make([]int, len(m.Keys))
	for i, k := range m.Keys {
		kp := fmt.Sprintf("%s.keys[%d]", p, i)
		slots[i] = k.Index
		c.between(kp+".index", k.Index, 0, 15)
		switch {
		case k.Media != nil:
			if k.Type != 5 || k.Param != 0 {
				c.errorf(kp, "a media default needs type 5 and param 0")
			}
			if k.Label != nil {
				c.errorf(kp+".label", "a media default takes its label from media.json")
			}
			if !usages[*k.Media] {
				c.errorf(kp+".media", "usage %#04x is not in media.json", *k.Media)
			}
		case k.Label == nil:
			c.errorf(kp+".label", "required when media is absent")
		default:
			c.labelRef(kp+".label", *k.Label, labels)
		}
	}
	unique(c, p+".keys.index", slots)
	for _, f := range []struct {
		name string
		v    int
	}{
		{"debounce", m.Debounce}, {"tipsDebounce", m.TipsDebounce}, {"maxDebounce", m.MaxDebounce},
		{"sensorMode", m.SensorMode}, {"performance", m.Performance}, {"sleepTime", m.SleepTime},
		{"dpiEffect.mode", m.DPIEffect.Mode}, {"dpiEffect.brightness", m.DPIEffect.Brightness},
		{"dpiEffect.speed", m.DPIEffect.Speed}, {"lightEffect.mode", m.LightEffect.Mode},
		{"lightEffect.brightness", m.LightEffect.Brightness}, {"lightEffect.speed", m.LightEffect.Speed},
	} {
		c.atLeast(p+"."+f.name, f.v, 0)
	}
	c.atLeast(p+".reportRate", m.ReportRate, 1)
	if m.LOD != nil {
		c.atLeast(p+".lod", *m.LOD, 0)
	}
}

func validateKeyboard(c *checker, p string, k *keyboard, usages map[uint16]bool) {
	minItems(c, p+".systems", k.Systems, 1)
	unique(c, p+".systems", k.Systems)
	for i, s := range k.Systems {
		c.oneOf(fmt.Sprintf("%s.systems[%d]", p, i), s, systems)
	}
	minItems(c, p+".layouts", k.Layouts, 1)
	unique(c, p+".layouts", k.Layouts)
	for i, l := range k.Layouts {
		c.oneOf(fmt.Sprintf("%s.layouts[%d]", p, i), l, layouts)
	}
	c.atLeast(p+".image.width", k.Image.Width, 1)
	c.atLeast(p+".image.height", k.Image.Height, 1)
	minItems(c, p+".rects", k.Rects, 1)
	for i, r := range k.Rects {
		c.atLeast(fmt.Sprintf("%s.rects[%d].width", p, i), r.Width, 0)
		c.atLeast(fmt.Sprintf("%s.rects[%d].height", p, i), r.Height, 0)
	}
	minItems(c, p+".maps", k.Maps, 1)
	if want := len(k.Systems) * len(k.Layouts); len(k.Maps) != want {
		c.errorf(p+".maps", "has %d layers, want %d (systems x layouts)", len(k.Maps), want)
	}
	for i, l := range k.Maps {
		lp := fmt.Sprintf("%s.maps[%d]", p, i)
		c.oneOf(lp+".system", l.System, systems)
		c.oneOf(lp+".layout", l.Layout, layouts)
		if len(k.Layouts) > 0 && i/len(k.Layouts) < len(k.Systems) {
			if ws, wl := k.Systems[i/len(k.Layouts)], k.Layouts[i%len(k.Layouts)]; l.System != ws || l.Layout != wl {
				c.errorf(lp, "is %s/%s, want %s/%s (systems x layouts order)", l.System, l.Layout, ws, wl)
			}
		}
		minItems(c, lp+".slots", l.Slots, 1)
		if len(l.Slots) != len(k.Rects) {
			c.errorf(lp+".slots", "has %d slots, rects has %d", len(l.Slots), len(k.Rects))
		}
		for j, s := range l.Slots {
			if s.Type&0x0F == 3 && !usages[s.Value] {
				c.errorf(fmt.Sprintf("%s.slots[%d]", lp, j), "consumer usage %#04x is not in media.json", s.Value)
			}
		}
	}
}

func validateIDs(c *checker, ids *idsFile) {
	minItems(c, "ids.json: vids", ids.VIDs, 1)
	unique(c, "ids.json: vids", ids.VIDs)
	var all []uint16
	for _, cl := range pidClasses(ids) {
		p := "ids.json: pids." + cl.name
		unique(c, p+".wireless", cl.class.Wireless)
		unique(c, p+".wired", cl.class.Wired)
		all = append(all, cl.class.Wireless...)
		all = append(all, cl.class.Wired...)
	}
	unique(c, "ids.json: pids (all classes)", all)
}

type namedClass struct {
	name  string
	class pidClass
}

func pidClasses(ids *idsFile) []namedClass {
	return []namedClass{
		{"mouse", ids.PIDs.Mouse},
		{"keyboard", ids.PIDs.Keyboard},
		{"composite", ids.PIDs.Composite},
	}
}

func validateKeys(c *checker, kf *keysFile) map[string]key {
	byCode := map[string]key{}
	minItems(c, "keys.json: keys", kf.Keys, 1)
	for i, k := range kf.Keys {
		p := fmt.Sprintf("keys.json: keys[%d]", i)
		c.match(p+".code", k.Code, identPattern)
		if _, dup := byCode[k.Code]; dup {
			c.errorf(p+".code", "duplicate code %q", k.Code)
		} else {
			byCode[k.Code] = k
		}
		c.between(p+".kind", k.Kind, 0, 15)
		c.text(p+".win", k.Win)
		c.text(p+".mac", k.Mac)
	}
	return byCode
}

func validatePresets(c *checker, pf *presetsFile, labels map[string]string, keys map[string]key) {
	for _, set := range presetSets(pf) {
		ids := make([]string, len(set.presets))
		for i, pr := range set.presets {
			p := fmt.Sprintf("presets.json: shortcuts.%s[%d]", set.os, i)
			ids[i] = pr.ID
			c.match(p+".id", pr.ID, presetIDPattern)
			c.labelRef(p+".label", pr.Label, labels)
			if len(pr.Keys) < 1 || len(pr.Keys) > 5 {
				c.errorf(p+".keys", "has %d keys, want 1..5", len(pr.Keys))
			}
			for j, pk := range pr.Keys {
				kp := fmt.Sprintf("%s.keys[%d]", p, j)
				c.match(kp+".code", pk.Code, identPattern)
				c.between(kp+".kind", pk.Kind, 0, 15)
				k, ok := keys[pk.Code]
				switch {
				case !ok:
					c.errorf(kp, "code %q is not in keys.json", pk.Code)
				case k.Kind != pk.Kind || k.Value != pk.Value:
					c.errorf(kp, "%s is kind %d value %d, keys.json has kind %d value %d", pk.Code, pk.Kind, pk.Value, k.Kind, k.Value)
				}
			}
		}
		unique(c, "presets.json: shortcuts."+set.os+".id", ids)
	}
	values := make([]uint8, len(pf.MacroRepeat))
	for i, r := range pf.MacroRepeat {
		p := fmt.Sprintf("presets.json: macroRepeat[%d]", i)
		values[i] = r.Value
		c.oneOf(p+".mode", r.Mode, repeatModes)
		c.labelRef(p+".label", r.Label, labels)
	}
	unique(c, "presets.json: macroRepeat.value", values)
}

func validateOffsets(c *checker, of *offsetsFile) {
	for _, t := range offsetTables(of) {
		p := "offsets.json: " + t.name
		minItems(c, p, t.entries, 1)
		idents := make([]string, len(t.entries))
		for i, o := range t.entries {
			ep := fmt.Sprintf("%s[%d]", p, i)
			c.match(ep+".name", o.Name, identPattern)
			idents[i] = exported(o.Name)
			if o.Value != nil {
				c.between(ep+".value", *o.Value, 0, maxFlashAddress)
			} else if o.Vendor == nil {
				c.errorf(ep, "a null value needs the vendor value")
			}
			if o.Vendor != nil {
				c.between(ep+".vendor", *o.Vendor, 0, maxFlashAddress)
			}
		}
		unique(c, p+".name", idents)
	}
}

type presetSet struct {
	os      string
	presets []preset
}

func presetSets(pf *presetsFile) []presetSet {
	return []presetSet{{"win", pf.Shortcuts.Win}, {"mac", pf.Shortcuts.Mac}}
}

type offsetTable struct {
	name    string
	entries []offset
}

func offsetTables(of *offsetsFile) []offsetTable {
	return []offsetTable{{"mouse", of.Mouse}, {"keyboard", of.Keyboard}, {"officeKeyboard", of.OfficeKeyboard}}
}

func validateBrightness(c *checker, b *brightnessFile) {
	minItems(c, "brightness.json: levels", b.Levels, 1)
	levels := make([]int, len(b.Levels))
	values := make([]uint8, len(b.Levels))
	for i, l := range b.Levels {
		c.atLeast(fmt.Sprintf("brightness.json: levels[%d].level", i), l.Level, 1)
		levels[i], values[i] = l.Level, l.Value
	}
	unique(c, "brightness.json: levels.level", levels)
	unique(c, "brightness.json: levels.value", values)
}

func validateSources(c *checker, s *sourcesFile) {
	if s.Schema != 1 {
		c.errorf("sources.json: schema", "format version %d, this generator reads 1", s.Schema)
	}
	c.nonEmpty("sources.json: cfgVersion", s.CfgVersion)
	c.nonEmpty("sources.json: bundle", s.Bundle)
	minItems(c, "sources.json: tools", s.Tools, 1)
	names := make([]string, len(s.Tools))
	for i, t := range s.Tools {
		p := fmt.Sprintf("sources.json: tools[%d]", i)
		names[i] = t.Name
		c.nonEmpty(p+".name", t.Name)
		if t.Version == nil && t.SHA256 == nil {
			c.errorf(p, "needs a version or a sha256")
		}
		if t.Version != nil {
			c.nonEmpty(p+".version", *t.Version)
		}
		if t.SHA256 != nil {
			c.match(p+".sha256", *t.SHA256, sha256Pattern)
		}
	}
	unique(c, "sources.json: tools.name", names)
	minItems(c, "sources.json: inputs", s.Inputs, 1)
	paths := make([]string, len(s.Inputs))
	for i, in := range s.Inputs {
		p := fmt.Sprintf("sources.json: inputs[%d]", i)
		paths[i] = in.Path
		c.nonEmpty(p+".path", in.Path)
		c.match(p+".sha256", in.SHA256, sha256Pattern)
	}
	unique(c, "sources.json: inputs.path", paths)
}
