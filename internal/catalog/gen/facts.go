package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strings"
)

type facts struct {
	Models     modelsFile     `json:"models.json"`
	IDs        idsFile        `json:"ids.json"`
	Sensors    sensorsFile    `json:"sensors.json"`
	Media      mediaFile      `json:"media.json"`
	Keys       keysFile       `json:"keys.json"`
	Presets    presetsFile    `json:"presets.json"`
	Offsets    offsetsFile    `json:"offsets.json"`
	Brightness brightnessFile `json:"brightness.json"`
	Labels     labelsFile     `json:"labels.json"`
	Sources    sourcesFile    `json:"sources.json"`
}

type modelsFile struct {
	Models []model `json:"models"`
}

type model struct {
	Group    string    `json:"group"`
	CID      uint8     `json:"cid"`
	MIDs     []uint8   `json:"mids"`
	Name     *string   `json:"name"`
	Mouse    *mouse    `json:"mouse,omitempty"`
	Keyboard *keyboard `json:"keyboard,omitempty"`
}

type mouse struct {
	Sensor           string            `json:"sensor"`
	MaxDPI           int               `json:"maxDpi"`
	DPIs             []dpiStage        `json:"dpis"`
	CurrentDPI       int               `json:"currentDpi"`
	Keys             []mouseKey        `json:"keys"`
	Debounce         int               `json:"debounce"`
	TipsDebounce     int               `json:"tipsDebounce"`
	MaxDebounce      int               `json:"maxDebounce"`
	ReportRate       int               `json:"reportRate"`
	SensorMode       int               `json:"sensorMode"`
	LOD              *int              `json:"lod"`
	PerformanceState bool              `json:"performanceState"`
	Performance      int               `json:"performance"`
	Ripple           bool              `json:"ripple"`
	Angle            bool              `json:"angle"`
	MotionSync       bool              `json:"motionSync"`
	SleepTime        int               `json:"sleepTime"`
	DPIEffect        dpiEffect         `json:"dpiEffect"`
	LightEffect      lightEffect       `json:"lightEffect"`
	LongDistance     bool              `json:"longDistance"`
	Firmware         map[string]string `json:"firmware,omitempty"`
}

type dpiEffect struct {
	Mode       int  `json:"mode"`
	Brightness int  `json:"brightness"`
	Speed      int  `json:"speed"`
	Show       bool `json:"show"`
}

type lightEffect struct {
	Mode           int  `json:"mode"`
	Brightness     int  `json:"brightness"`
	Speed          int  `json:"speed"`
	MovingOffState bool `json:"movingOffState"`
}

type dpiStage struct {
	DPI   int      `json:"dpi"`
	Color [3]uint8 `json:"color"`
}

type mouseKey struct {
	Index   int     `json:"index"`
	Type    uint8   `json:"type"`
	Param   uint16  `json:"param"`
	Media   *uint16 `json:"media,omitempty"`
	Visible bool    `json:"visible"`
	Label   *string `json:"label,omitempty"`
}

type keyboard struct {
	Systems []string  `json:"systems"`
	Layouts []string  `json:"layouts"`
	Image   imageSize `json:"image"`
	Rects   []rect    `json:"rects"`
	Maps    []layer   `json:"maps"`
}

type imageSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type rect struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type layer struct {
	System string    `json:"system"`
	Layout string    `json:"layout"`
	Slots  []keySlot `json:"slots"`
}

type keySlot struct {
	Type  uint8  `json:"type"`
	Value uint16 `json:"value"`
}

type idsFile struct {
	VIDs []uint16 `json:"vids"`
	PIDs pidTable `json:"pids"`
}

type pidTable struct {
	Mouse     pidClass `json:"mouse"`
	Keyboard  pidClass `json:"keyboard"`
	Composite pidClass `json:"composite"`
}

type pidClass struct {
	Wireless []uint16 `json:"wireless"`
	Wired    []uint16 `json:"wired"`
}

type sensorsFile struct {
	Sensors []sensor `json:"sensors"`
	Gates   []gate   `json:"gates"`
}

type sensor struct {
	ID     string     `json:"id"`
	Ranges []dpiRange `json:"ranges"`
	Values []int      `json:"values,omitempty"`
}

type dpiRange struct {
	Min   int   `json:"min"`
	Max   int   `json:"max"`
	Step  int   `json:"step"`
	DPIEx uint8 `json:"dpiEx"`
}

type gate struct {
	Name    string   `json:"name"`
	Sensors []string `json:"sensors"`
}

type mediaFile struct {
	Usages   []usage  `json:"usages"`
	Mouse    []uint16 `json:"mouse"`
	Keyboard []uint16 `json:"keyboard"`
}

type usage struct {
	Code  uint16 `json:"code"`
	Name  string `json:"name"`
	Label string `json:"label"`
}

type keysFile struct {
	Keys []key `json:"keys"`
}

type key struct {
	Code  string `json:"code"`
	Value uint8  `json:"value"`
	Kind  int    `json:"kind"`
	Win   string `json:"win"`
	Mac   string `json:"mac"`
}

type presetsFile struct {
	Shortcuts   shortcutSets   `json:"shortcuts"`
	MacroRepeat []repeatOption `json:"macroRepeat"`
}

type shortcutSets struct {
	Win []preset `json:"win"`
	Mac []preset `json:"mac"`
}

type preset struct {
	ID    string      `json:"id"`
	Label string      `json:"label"`
	Keys  []presetKey `json:"keys"`
}

type presetKey struct {
	Code  string `json:"code"`
	Kind  int    `json:"kind"`
	Value uint8  `json:"value"`
}

type repeatOption struct {
	Value uint8  `json:"value"`
	Mode  string `json:"mode"`
	Label string `json:"label"`
}

type offsetsFile struct {
	Mouse          []offset `json:"mouse"`
	Keyboard       []offset `json:"keyboard"`
	OfficeKeyboard []offset `json:"officeKeyboard"`
}

type offset struct {
	Name   string `json:"name"`
	Value  *int   `json:"value"`
	Vendor *int   `json:"vendor,omitempty"`
}

type brightnessFile struct {
	Levels  []brightnessLevel `json:"levels"`
	Default uint8             `json:"default"`
}

type brightnessLevel struct {
	Level int   `json:"level"`
	Value uint8 `json:"value"`
}

type labelsFile struct {
	Labels map[string]string `json:"labels"`
}

type sourcesFile struct {
	Schema     int     `json:"schema"`
	CfgVersion string  `json:"cfgVersion"`
	Bundle     string  `json:"bundle"`
	Tools      []tool  `json:"tools"`
	Inputs     []input `json:"inputs"`
}

type tool struct {
	Name    string  `json:"name"`
	Version *string `json:"version,omitempty"`
	SHA256  *string `json:"sha256,omitempty"`
}

type input struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

const schemaFile = "schema.json"

func loadFacts(fsys fs.FS) (*facts, error) {
	var f facts
	v := reflect.ValueOf(&f).Elem()
	known := map[string]bool{schemaFile: true}
	var errs []error
	for i := range v.NumField() {
		name, _ := jsonTag(v.Type().Field(i))
		known[name] = true
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := decodeStrict(data, v.Field(i).Addr().Interface()); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		errs = append(errs, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !known[e.Name()] {
			errs = append(errs, fmt.Errorf("%s: unexpected file in the facts directory", e.Name()))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &f, nil
}

func decodeStrict(data []byte, v any) error {
	if err := checkDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the top-level value")
	}
	raw := json.NewDecoder(bytes.NewReader(data))
	raw.UseNumber()
	var tree any
	if err := raw.Decode(&tree); err != nil {
		return err
	}
	return checkShape(tree, reflect.TypeOf(v).Elem(), "", false)
}

func jsonTag(f reflect.StructField) (name string, optional bool) {
	name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name, opts == "omitempty"
}

func checkShape(raw any, t reflect.Type, path string, nullable bool) error {
	if raw == nil {
		if nullable {
			return nil
		}
		return fmt.Errorf("%s: null is not allowed", pathOrRoot(path))
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var errs []error
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object", pathOrRoot(path))
		}
		for i := range t.NumField() {
			sf := t.Field(i)
			name, optional := jsonTag(sf)
			p := joinPath(path, name)
			val, present := obj[name]
			switch {
			case !present && !optional:
				errs = append(errs, fmt.Errorf("%s: required field is missing", p))
			case present:
				nullable := sf.Type.Kind() == reflect.Pointer && !optional
				errs = append(errs, checkShape(val, sf.Type, p, nullable))
			}
		}
	case reflect.Slice, reflect.Array:
		arr, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s: want an array", pathOrRoot(path))
		}
		if t.Kind() == reflect.Array && len(arr) != t.Len() {
			return fmt.Errorf("%s: want %d items, have %d", pathOrRoot(path), t.Len(), len(arr))
		}
		for i, e := range arr {
			errs = append(errs, checkShape(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i), false))
		}
	case reflect.Map:
		obj, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object", pathOrRoot(path))
		}
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			errs = append(errs, checkShape(obj[k], t.Elem(), joinPath(path, k), false))
		}
	}
	return errors.Join(errs...)
}

func checkDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type frame struct {
		keys      map[string]bool
		expectKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{keys: map[string]bool{}, expectKey: true})
			continue
		case json.Delim('['):
			stack = append(stack, &frame{})
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		default:
			if top != nil && top.expectKey {
				k := tok.(string)
				if top.keys[k] {
					return fmt.Errorf("duplicate object key %q", k)
				}
				top.keys[k] = true
				top.expectKey = false
				continue
			}
		}
		if len(stack) > 0 && stack[len(stack)-1].keys != nil {
			stack[len(stack)-1].expectKey = true
		}
	}
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func pathOrRoot(path string) string {
	if path == "" {
		return "top level"
	}
	return path
}
