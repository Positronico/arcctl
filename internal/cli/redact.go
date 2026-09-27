package cli

import (
	"bytes"
	"fmt"
	"os"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/hidio"
)

// runRedact copies a recorded transcript with everything private masked, so
// that the copy can be shared or committed.
func runRedact(r *runner, args []string) error {
	fs := r.flagSet("redact", synopsis("redact"))
	out := fs.String("o", "", "write the redacted copy to `file` (required; an existing file is kept)")
	pos, err := r.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	if *out == "" {
		return usageError("redact: -o is required")
	}
	in, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	var b bytes.Buffer
	if err := hidio.RedactTranscript(&b, bytes.NewReader(in)); err != nil {
		return fmt.Errorf("%s: %w", pos[0], err)
	}
	if err := backup.WriteNew(*out, b.Bytes()); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "Wrote %s: a redacted copy of %s\n", *out, pos[0])
	fmt.Fprintln(r.out, fill("  Masked: the receiver's address, the shortcut and macro bytes, the data of every keyboard, consumer and mouse report, and the time zone. Free-text notes are kept as written.", "", 80))
	return nil
}
