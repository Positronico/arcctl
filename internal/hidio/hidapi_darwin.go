//go:build hidapi

package hidio

import hid "github.com/sstallion/go-hid"

func openShared() { hid.SetOpenExclusive(false) }
