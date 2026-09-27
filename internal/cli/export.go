package cli

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
)

// runExportBin writes a .bin the web app can import. The import writes the
// settings page, the 4 bytes at 6984 and every bound shortcut and macro slot
// back to the mouse, so a file that lacks any of those bytes is refused unless
// --allow-partial says to write them as 0xFF.
func runExportBin(r *runner, args []string) error {
	fs := r.flagSet("export-bin", synopsis("export-bin"))
	out := fs.String("o", "", "write the .bin to `file` (required)")
	partial := fs.Bool("allow-partial", false, "export even when the file lacks bytes the web app's import writes back; they go in as 0xFF")
	pos, err := r.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	if *out == "" {
		return usageError("export-bin: -o is required")
	}
	src, err := backup.Open(pos[0])
	if err != nil {
		return err
	}
	if f := src.File; f != nil && f.Source != backup.SourceDevice {
		return fail(ExitFailure, fmt.Sprintf("%s was made by the %s, not read from a mouse", src.Path, f.Source),
			"The web app would write its bytes to the mouse. Export a backup of the mouse itself.")
	}
	m, err := r.modelOf(src)
	if err != nil {
		return err
	}
	export := backup.ExportBin
	if *partial {
		export = backup.ExportPartialBin
	}
	b, err := export(m, src.Image)
	var pe *backup.PartialError
	switch {
	case errors.As(err, &pe):
		return fail(ExitFailure, fmt.Sprintf("%s lacks bytes the web app's import writes back to the mouse: %s", src.Path, extentList(pe.Missing)),
			"The import would overwrite them with 0xFF. Export a full backup (arcctl backup --full) instead, or pass --allow-partial to write the file anyway.")
	case errors.Is(err, backup.ErrUnsupported):
		return fail(ExitFailure, m.Name+" has no web .bin export", "")
	case err != nil:
		return err
	}
	if err := backup.WriteFile(*out, b); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "Wrote %s: %d bytes for the web app (%s, sensor %s)\n", *out, len(b), m.Name, m.Sensor.ID)
	text := "  Importing it, the web app writes the settings page, the 4 bytes at 6984 and every bound shortcut and macro slot that differs from what it read from the mouse."
	if missing := backup.BinMissing(src.Image); len(missing) > 0 {
		text += fmt.Sprintf(" %s lacks %s, which the import writes as 0xFF.", src.Path, extentList(missing))
	}
	fmt.Fprintln(r.out, fill(text, "", 80))
	return nil
}

func extentList(es []flash.Extent) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.String()
	}
	return strings.Join(parts, ", ")
}

// The vendored usbhid: the upstream module and version it was copied from,
// and the local patches in its patches folder, in the order they apply.
const (
	usbhidModule  = "rafaelmartins.com/p/usbhid"
	usbhidVersion = "v0.0.0-20260903160318-2edd824d3b06"
	usbhidDir     = "internal/third_party/usbhid"
)

var usbhidPatches = []string{"0001-setreport-timeout", "0002-buffered-input", "0003-report-descriptor", "0004-lint"}

func runVersion(r *runner, args []string) error {
	fs := r.flagSet("version", synopsis("version"))
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	w := r.out
	line := func(label, text string) { fmt.Fprintf(w, "%-9s %s\n", label, text) }
	fmt.Fprintf(w, "arcctl %s\n", r.env.Version)
	line("Go", runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH)
	backends := hidio.Backends()
	for i, b := range backends {
		if b == hidio.DefaultBackend {
			backends[i] += " (default)"
		}
	}
	line("Backends", strings.Join(backends, ", "))
	line("usbhid", usbhidModule+" "+usbhidVersion)
	line("", "vendored in "+usbhidDir)
	fmt.Fprintln(w, wrap("Patches   ", strings.Repeat(" ", 10), usbhidPatches, ", ", 80))
	line("Catalog", fmt.Sprintf("facts schema %d, web app cfg %s, bundle %s", catalog.FactsSchema, catalog.CfgVersion, catalog.Bundle))
	for _, t := range catalog.Tools() {
		text := t.Name
		if t.Version != "" {
			text += " version " + t.Version
		}
		if t.SHA256 != "" {
			text += " sha256 " + t.SHA256[:16]
		}
		line("Tool", text)
	}
	fmt.Fprintln(w, "Inputs (sha256)")
	var rows [][]string
	for _, in := range catalog.Inputs() {
		rows = append(rows, []string{in.Path, in.SHA256[:16]})
	}
	table(w, "  ", rows)
	vs := catalog.VerifiedStages()
	if len(vs) == 0 {
		line("Verified", "no hardware stage recorded yet")
		return nil
	}
	fmt.Fprintln(w, "Verified")
	rows = rows[:0]
	for _, v := range vs {
		rows = append(rows, []string{v.Model, v.Feature, v.Firmware, v.Stage, v.Date})
	}
	table(w, "  ", rows)
	return nil
}
