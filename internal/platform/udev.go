package platform

import (
	"fmt"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
)

// UdevRulesPath is where UdevHint installs the rules.
const UdevRulesPath = "/etc/udev/rules.d/70-arcctl.rules"

// UdevRules gives the logged-in user access to the hidraw nodes of every device
// the catalog knows. The rule lines match packaging/linux/70-arcctl.rules.
func UdevRules() string {
	var b strings.Builder
	b.WriteString("# arcctl: hidraw access for the logged-in user\n")
	for _, id := range catalog.USBIDs() {
		fmt.Fprintf(&b, "SUBSYSTEM==\"hidraw\", ATTRS{idVendor}==\"%04x\", ATTRS{idProduct}==\"%04x\", TAG+=\"uaccess\"\n", id.VID, id.PID)
	}
	return b.String()
}

// UdevHint is the shell commands that install UdevRules.
func UdevHint() string {
	return "sudo tee " + UdevRulesPath + " >/dev/null <<'EOF'\n" + UdevRules() + "EOF\n" +
		"sudo udevadm control --reload-rules\n" +
		"sudo udevadm trigger\n"
}
