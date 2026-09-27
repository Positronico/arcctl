//go:build hwtest

package hwtest

import (
	"io"

	"github.com/positronico/arcctl/internal/safety"
)

// Host answers what H0 asks the OS. The CLI answers from the platform
// package; the emulator answers for itself.
type Host interface {
	// Doctor prints the doctor report to w and returns what the log keeps.
	Doctor(w io.Writer) (Doctor, error)
	// Console is the screen lock and the Secure Input holder.
	Console() (safety.Console, error)
	// Clients are the other processes holding the receiver open.
	Clients() ([]safety.Client, error)
	// Permission describes the Input Monitoring grant as the OS reports it.
	Permission() (string, error)
}

// Doctor is the part of the doctor report the log keeps: nothing that names
// a device path.
type Doctor struct {
	Access  string   // Input Monitoring: granted, denied, not needed
	App     string   // the app the grant belongs to
	Clients []string // other processes holding the receiver open
}
