// Command arcctl configures ProtoArc mice over USB HID without the vendor's
// web app. With no arguments it will start the TUI; the subcommands are the
// CLI.
package main

import (
	"os"

	"github.com/positronico/arcctl/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(cli.Main(version)) }
