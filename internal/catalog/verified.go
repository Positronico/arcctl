package catalog

import "slices"

// Verification records a hardware stage a feature passed on one model and firmware.
// The list is generated from verified.json.
type Verification struct {
	Model    string
	Feature  string
	Firmware string
	Stage    string
	Date     string
}

type Verifications []Verification

func VerifiedStages() Verifications { return slices.Clone(verified) }

func (vs Verifications) Covers(model, feature, firmware string) bool {
	for _, v := range vs {
		if v.Model == model && v.Feature == feature && v.Firmware == firmware {
			return true
		}
	}
	return false
}

func (vs Verifications) Find(model, feature string) []Verification {
	var out []Verification
	for _, v := range vs {
		if v.Model == model && v.Feature == feature {
			out = append(out, v)
		}
	}
	return out
}
