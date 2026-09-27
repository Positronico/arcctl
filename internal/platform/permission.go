package platform

import (
	"fmt"
	"runtime"
	"strings"
)

const inputMonitoringPane = "System Settings > Privacy & Security > Input Monitoring"

// Hint says what the user can do when access is not granted; "" otherwise.
func (p Permission) Hint() string { return permissionHint(runtime.GOOS, p) }

func permissionHint(goos string, p Permission) string {
	if p.Access == AccessGranted || p.Access == AccessNotNeeded {
		return ""
	}
	switch goos {
	case "darwin":
		return inputMonitoringHint(p.App)
	case "linux":
		if p.Access == AccessDenied {
			return "Install the udev rules, then replug the receiver:\n" + UdevHint()
		}
	}
	return ""
}

func inputMonitoringHint(app App) string {
	var b strings.Builder
	switch {
	case app.Via == ViaSelf:
		fmt.Fprintf(&b, "Allow Input Monitoring for %s in %s.", app.Proc.Path, inputMonitoringPane)
	case app.Name != "":
		fmt.Fprintf(&b, "Allow Input Monitoring for %s in %s, then quit and reopen %s.", app.Name, inputMonitoringPane, app.Name)
	default:
		fmt.Fprintf(&b, "Allow Input Monitoring for the terminal app that runs arcctl in %s, then quit and reopen that app.", inputMonitoringPane)
	}
	if app.Tmux {
		b.WriteString(" Inside tmux the grant belongs to the app that started the tmux server")
		if app.Name != "" {
			fmt.Fprintf(&b, " (%s)", app.Name)
		}
		b.WriteString(", which may not be the terminal in front of you.")
	}
	return b.String()
}
