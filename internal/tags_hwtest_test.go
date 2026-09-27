//go:build hwtest

package internal_test

import "testing"

var listTags = []string{"-tags=hwtest"}

// rawPath lists, by package, the exported functions besides hidio's own that
// may return a hidio.Raw: the device end of the hwtest raw path (D10).
var rawPath = map[string][]string{
	"hidio": {"OpenRaw"},
	"emu":   {"Bus.OpenRaw"},
}

func TestWiringSeesHwtestCode(t *testing.T) {
	for _, p := range listPackages(t) {
		if p.ImportPath == module+"/internal/hwtest" && len(p.GoFiles) > 0 {
			return
		}
	}
	t.Fatal("the package list holds no file of internal/hwtest under -tags hwtest")
}
