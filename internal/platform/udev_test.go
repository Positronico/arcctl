package platform

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func ruleLines(s string) []string {
	var out []string
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestUdevRulesMatchPackaging(t *testing.T) {
	file, err := os.ReadFile("../../packaging/linux/70-arcctl.rules")
	if err != nil {
		t.Fatal(err)
	}
	want, got := ruleLines(string(file)), ruleLines(UdevRules())
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("UdevRules has %d rules, packaging/linux/70-arcctl.rules has %d; they differ", len(got), len(want))
	}
}

func TestUdevHint(t *testing.T) {
	h := UdevHint()
	for _, s := range []string{
		"sudo tee " + UdevRulesPath + " >/dev/null <<'EOF'\n",
		UdevRules() + "EOF\n",
		"sudo udevadm control --reload-rules\n",
		"sudo udevadm trigger\n",
	} {
		if !strings.Contains(h, s) {
			t.Errorf("UdevHint lacks %q", s)
		}
	}
	for _, l := range ruleLines(UdevRules()) {
		if !strings.Contains(l, `SUBSYSTEM=="hidraw"`) || !strings.Contains(l, `ATTRS{idProduct}==`) || !strings.HasSuffix(l, `TAG+="uaccess"`) {
			t.Errorf("rule %q is not a VID+PID hidraw uaccess rule", l)
		}
	}
}
