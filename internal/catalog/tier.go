package catalog

import "strconv"

type Tier uint8

const (
	Off Tier = iota
	ReadOnly
	Experimental
	Untested
	Verified
)

var tierNames = [...]string{"off", "read-only", "experimental", "untested", "verified"}

func (t Tier) String() string {
	if int(t) < len(tierNames) {
		return tierNames[t]
	}
	return "Tier(" + strconv.Itoa(int(t)) + ")"
}
