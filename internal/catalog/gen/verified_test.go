package main

import (
	"os"
	"strings"
	"testing"

	arcmouse "github.com/positronico/arcctl/internal/mouse"
)

func TestParseVerified(t *testing.T) {
	f, err := loadFacts(os.DirFS(factsDir))
	if err != nil {
		t.Fatal(err)
	}
	good := `[
		{"model": "7B04", "feature": "dpi.value", "firmware": "v1.07", "stage": "H2", "date": "2026-10-01"},
		{"model": "7B04", "feature": "dpi.value", "firmware": "v1.08", "stage": "H2", "date": "2026-11-12"},
		{"model": "7B06", "feature": "button.system", "firmware": "v1.07", "stage": "H3b", "date": "2026-10-02"}
	]`
	vs, err := parseVerified([]byte(good), f)
	if err != nil {
		t.Fatal(err)
	}
	src, err := renderVerified(vs)
	if err != nil {
		t.Fatal(err)
	}
	want := `{Model: "7B06", Feature: "button.system", Firmware: "v1.07", Stage: "H3b", Date: "2026-10-02"},`
	if len(vs) != 3 || !strings.Contains(string(src), want) {
		t.Errorf("parsed %v, rendered:\n%s", vs, src)
	}

	entry := func(model, feature, firmware, stage, date string) string {
		return `{"model": "` + model + `", "feature": "` + feature + `", "firmware": "` + firmware +
			`", "stage": "` + stage + `", "date": "` + date + `"}`
	}
	ok := entry("7B04", "dpi.value", "v1.07", "H2", "2026-10-01")
	bad := []struct {
		name, data, want string
	}{
		{"null", `null`, "null is not allowed"},
		{"object", `{}`, "cannot unmarshal"},
		{"trailing data", `[] []`, "trailing data"},
		{"unknown field", `[{"model": "7B04", "feature": "dpi.value", "firmware": "v1.07", "stage": "H2", "date": "2026-10-01", "by": "x"}]`, "unknown field"},
		{"missing field", `[{"model": "7B04", "feature": "dpi.value", "firmware": "v1.07", "stage": "H2"}]`, "date"},
		{"unknown model", "[" + entry("7B09", "dpi.value", "v1.07", "H2", "2026-10-01") + "]", "model"},
		{"bad feature", "[" + entry("7B04", "DPI Values", "v1.07", "H2", "2026-10-01") + "]", "feature"},
		{"bad firmware", "[" + entry("7B04", "dpi.value", "1.07", "H2", "2026-10-01") + "]", "firmware"},
		{"bad stage", "[" + entry("7B04", "dpi.value", "v1.07", "stage 2", "2026-10-01") + "]", "stage"},
		{"bad date", "[" + entry("7B04", "dpi.value", "v1.07", "H2", "2026-13-01") + "]", "date"},
		{"duplicate", "[" + ok + "," + ok + "]", "listed twice"},
	}
	for _, tc := range bad {
		_, err := parseVerified([]byte(tc.data), f)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one mentioning %q", tc.name, err, tc.want)
		}
		if _, err := build(os.DirFS(factsDir), []byte(tc.data)); err == nil {
			t.Errorf("%s: build accepted it", tc.name)
		}
	}
}

func TestFeatureNamesFitVerified(t *testing.T) {
	for _, f := range arcmouse.Features() {
		if !featurePattern.MatchString(string(f)) {
			t.Errorf("feature %q cannot be written in verified.json", f)
		}
	}
}
