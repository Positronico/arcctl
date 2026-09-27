package catalog_test

import (
	"slices"
	"testing"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/mouse"
)

// verified.json fills as the hardware stages pass; whatever it holds, each
// entry covers its own model, feature and firmware and nothing else.
func TestVerifiedCoversOnlyWhatItNames(t *testing.T) {
	v := catalog.VerifiedStages()
	if v == nil {
		t.Fatal("VerifiedStages is nil, want at least an empty list")
	}
	for _, x := range v {
		if !v.Covers(x.Model, x.Feature, x.Firmware) || v.Covers(x.Model, x.Feature, "v0.00") {
			t.Errorf("%+v", x)
		}
	}
	if (catalog.Verifications{}).Covers("7B04", "dpi.value", "v1.07") {
		t.Error("an empty list covers a feature")
	}
}

func TestVerifiedNamesKnownFeatures(t *testing.T) {
	for _, v := range catalog.VerifiedStages() {
		if !slices.Contains(mouse.Features(), mouse.Feature(v.Feature)) {
			t.Errorf("verified.json records %q, which is no feature", v.Feature)
		}
	}
}

func TestVerifications(t *testing.T) {
	v := catalog.Verifications{
		{Model: "7B04", Feature: "dpi.value", Firmware: "v1.07", Stage: "H2", Date: "2026-10-01"},
		{Model: "7B04", Feature: "dpi.value", Firmware: "v1.08", Stage: "H2", Date: "2026-11-12"},
		{Model: "7B06", Feature: "button.system", Firmware: "v1.07", Stage: "H3b", Date: "2026-10-02"},
	}
	if !v.Covers("7B04", "dpi.value", "v1.08") || v.Covers("7B04", "dpi.value", "v1.09") || v.Covers("7B06", "dpi.value", "v1.07") {
		t.Error("Covers does not match on model, feature and firmware")
	}
	if got := v.Find("7B04", "dpi.value"); len(got) != 2 || got[1].Firmware != "v1.08" {
		t.Errorf("Find = %v", got)
	}
}
