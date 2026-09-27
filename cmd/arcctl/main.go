// Command arcctl configures ProtoArc mice over USB HID without the vendor's
// web app. With no arguments it starts the TUI; the subcommands are the CLI.
package main

import (
	"context"
	"os"

	"github.com/positronico/arcctl/internal/cli"
	"github.com/positronico/arcctl/internal/tui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	env := cli.DefaultEnv(version)
	env.TUI = runTUI
	os.Exit(cli.MainEnv(env))
}

func runTUI(ctx context.Context, t cli.TUI) error {
	mode := tui.ModeEdit
	switch {
	case t.ReadOnly:
		mode = tui.ModeReadOnly
	case t.DryRun:
		mode = tui.ModeDryRun
	}
	return tui.Run(ctx, tui.Options{
		Session: t.Session,
		Mode:    mode,
		Gates:   t.Gates,
		Source:  t.Source,
		OS:      t.OS,
		ASCII:   t.ASCII,
		NoColor: t.NoColor,
		Backups: t.Backups,
		DataDir: t.DataDir,
		Notes:   t.Notes,
		Host:    tui.Host{Permission: t.Permission, Console: t.Console, OpenSettings: t.OpenSettings},
	})
}
