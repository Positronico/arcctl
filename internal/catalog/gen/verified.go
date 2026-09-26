package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type verification struct {
	Model    string `json:"model"`
	Feature  string `json:"feature"`
	Firmware string `json:"firmware"`
	Stage    string `json:"stage"`
	Date     string `json:"date"`
}

var (
	featurePattern  = regexp.MustCompile(`^[a-z][a-z0-9]*([.-][a-z0-9]+)*$`)
	firmwarePattern = regexp.MustCompile(`^v[0-9]+\.[0-9a-f]{2}$`)
	stagePattern    = regexp.MustCompile(`^H[0-9]+[a-z]?$`)
	datePattern     = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$`)
)

// parseVerified reads verified.json, the list of hardware stages passed, with at most
// one entry per model, feature and firmware.
func parseVerified(data []byte, f *facts) ([]verification, error) {
	var vs []verification
	if err := decodeStrict(data, &vs); err != nil {
		return nil, fmt.Errorf("verified.json: %w", err)
	}
	var keys []string
	for _, m := range f.Models.Models {
		keys = append(keys, modelKey(m))
	}
	type id struct{ model, feature, firmware string }
	seen := map[id]bool{}
	var errs []error
	for i, v := range vs {
		bad := func(field, value string) {
			errs = append(errs, fmt.Errorf("verified.json: [%d].%s: invalid value %q", i, field, value))
		}
		if !slices.Contains(keys, v.Model) {
			bad("model", v.Model)
		}
		if !featurePattern.MatchString(v.Feature) {
			bad("feature", v.Feature)
		}
		if !firmwarePattern.MatchString(v.Firmware) {
			bad("firmware", v.Firmware)
		}
		if !stagePattern.MatchString(v.Stage) {
			bad("stage", v.Stage)
		}
		if !datePattern.MatchString(v.Date) {
			bad("date", v.Date)
		}
		k := id{v.Model, v.Feature, v.Firmware}
		if seen[k] {
			errs = append(errs, fmt.Errorf("verified.json: [%d]: %s %s %s is listed twice", i, v.Model, v.Feature, v.Firmware))
		}
		seen[k] = true
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return vs, nil
}

const verifiedPath = "internal/catalog/zz_verified.go"

func renderVerified(vs []verification) ([]byte, error) {
	g := newGoFileBy(verifiedBy, "catalog")
	g.p("")
	if len(vs) == 0 {
		g.p("var verified = Verifications{}")
		return g.source()
	}
	g.p("var verified = Verifications{")
	for _, v := range vs {
		fields := []string{"Model: " + q(v.Model), "Feature: " + q(v.Feature), "Firmware: " + q(v.Firmware),
			"Stage: " + q(v.Stage), "Date: " + q(v.Date)}
		g.p("{%s},", strings.Join(fields, ", "))
	}
	g.p("}")
	return g.source()
}
