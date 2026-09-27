package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/session"
)

var settingsPage = flash.Extent{Addr: 0, Len: 256}

func runDump(r *runner, args []string) error {
	fs := r.flagSet("dump", synopsis("dump"))
	rng := fs.String("range", "", "read `a:b` (end exclusive) or a+n, decimal or 0x hex; default: what the load read")
	asBin := fs.Bool("bin", false, "write raw bytes, unread ones as 0xFF (default range 0:256)")
	asJSON := fs.Bool("json", false, "print the decoded configuration as JSON")
	out := fs.String("o", "", "write to `file` instead of standard output")
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if *asBin && *asJSON {
		return usageError("dump: --bin and --json exclude each other")
	}
	var want []flash.Extent
	if *rng != "" {
		e, err := parseRange(*rng)
		if err != nil {
			return usageError("dump: %v", err)
		}
		want = []flash.Extent{e}
	}
	c, sn, err := r.connect(loaded)
	if err != nil {
		return err
	}
	defer c.stop()
	if err := loadable(sn); err != nil {
		return err
	}
	im, missing := sn.Image, sn.Unread
	if want != nil {
		var cp session.Capture
		err := r.do(c, func(ctx context.Context) error {
			var err error
			cp, err = c.s.Read(ctx, want...)
			return err
		})
		if err != nil {
			return readError(err)
		}
		im, missing = cp.Image, cp.Missing
	}
	switch {
	case *asJSON:
		return r.writeOut(*out, func(w io.Writer) error {
			enc := json.NewEncoder(w)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			return enc.Encode(backup.Summarize(sn.Model, im, r.keyOS()))
		})
	case *asBin:
		e := settingsPage
		if want != nil {
			e = want[0]
		}
		b, _ := im.Get(e)
		err = r.writeRaw(*out, b)
	default:
		extents := want
		if extents == nil {
			extents = im.KnownExtents()
		}
		err = r.writeOut(*out, func(w io.Writer) error { hexdump(w, im, extents); return nil })
	}
	if err == nil && len(missing) > 0 {
		words := make([]string, len(missing))
		for i, e := range missing {
			words[i] = e.String()
		}
		fmt.Fprintln(r.errw, wrap("arcctl: could not read ", "  ", words, ", ", 80))
	}
	return err
}

// loadable refuses devices whose flash arcctl does not load.
func loadable(sn *session.Snapshot) error {
	switch {
	case sn.State == session.Unknown:
		msg := "this device is not supported, so nothing was read from it"
		if sn.Err != nil {
			msg = strings.TrimPrefix(sn.Err.Error(), "session: ") + "; nothing was read from it"
		}
		return fail(ExitFailure, msg, "")
	case sn.Image == nil || sn.Model == nil:
		return fail(ExitFailure, "this device's flash is not read in this build (keyboards arrive in M8)", "")
	}
	return nil
}

func readError(err error) error {
	switch {
	case errors.Is(err, session.ErrUnsupported):
		return fail(ExitFailure, "this device's flash is not read in this build", "")
	case errors.Is(err, session.ErrProfileChanged):
		return fail(ExitFailure, "the onboard profile changed during the read", "Run the command again.")
	case errors.Is(err, session.ErrDeviceChanged):
		return fail(ExitFailure, "a different mouse was paired during the read", "Run the command again.")
	}
	return err
}
