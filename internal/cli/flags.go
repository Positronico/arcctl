package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// runner is one invocation: the environment, the global flags and the
// command being run.
type runner struct {
	ctx  context.Context
	env  Env
	out  io.Writer
	errw io.Writer
	g    globals
	cmd  string
	// writes enables the session's writes, for the commands that write.
	writes *session.Writes
}

type globals struct {
	device  string
	backend string
	log     string
	emulate string
	replay  string
	record  string
	model   string
	os      string
	wait    time.Duration
	ascii   bool
	noColor bool

	dryRun             bool
	allowUntested      bool
	experimental       bool
	allowForeignClient bool
}

const defaultWait = 10 * time.Second

func newGlobals() globals { return globals{wait: defaultWait} }

func (g *globals) define(fs *flag.FlagSet) {
	fs.StringVar(&g.device, "device", g.device, "use the interface at this `path` (required when several answer)")
	fs.StringVar(&g.backend, "backend", g.backend, "HID `backend`: usbhid, or hidapi in builds with that tag")
	fs.StringVar(&g.log, "log", g.log, "append the session log to `file`")
	fs.StringVar(&g.emulate, "emulate", g.emulate, "no hardware: emulate a mouse holding this backup, web .bin or dump `file`")
	fs.StringVar(&g.replay, "replay", g.replay, "no hardware: play back this recorded transcript `file`")
	fs.StringVar(&g.record, "record", g.record, "record the HID traffic as a transcript to `file`; a bare name goes in the logs folder")
	fs.StringVar(&g.model, "model", g.model, "model `key` of a dump or .bin, as in 'arcctl info' (default 7B04)")
	fs.StringVar(&g.os, "os", g.os, "key names for `os`: mac or win (default: this computer's)")
	fs.DurationVar(&g.wait, "wait", g.wait, "how long to wait for a sleeping mouse, or while replies go missing")
	fs.BoolVar(&g.ascii, "ascii", g.ascii, "print only ASCII")
	fs.BoolVar(&g.noColor, "no-color", g.noColor, "no colour in the TUI (the CLI prints none); NO_COLOR works too")
	fs.BoolVar(&g.dryRun, "dry-run", g.dryRun, "writes go to an in-memory overlay and their exact packets are printed; nothing reaches the mouse")
	fs.BoolVar(&g.allowUntested, "allow-untested", g.allowUntested, "let a write include features no hardware test has verified yet; a typed confirmation is still needed")
	fs.BoolVar(&g.experimental, "experimental", g.experimental, "let a write include experimental features that have a hardware-test record; a typed confirmation is still needed")
	fs.BoolVar(&g.allowForeignClient, "allow-foreign-client", g.allowForeignClient, "write even while another program has the receiver open")
}

// gates are what the global flags allow a write.
func (g *globals) gates() safety.Gates {
	return safety.Gates{DryRun: g.dryRun, AllowUntested: g.allowUntested, Experimental: g.experimental, AllowForeignClient: g.allowForeignClient}
}

// flagSet makes a flag set that also takes the global flags, so they can come
// after the command too.
func (r *runner) flagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	r.g.define(fs)
	fs.Usage = func() {
		fmt.Fprintf(r.out, "Usage: arcctl %s %s\n", name, synopsis)
		if c, ok := lookup(name); ok {
			fmt.Fprintf(r.out, "\n%s.\n", upperFirst(c.summary))
		}
		fmt.Fprintln(r.out, "\nFlags:")
		fs.VisitAll(func(f *flag.Flag) {
			if !isGlobal(f.Name) {
				printFlag(r.out, f)
			}
		})
		fmt.Fprintln(r.out, "\nGlobal flags: see 'arcctl help'.")
	}
	return fs
}

func isGlobal(name string) bool {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	g := newGlobals()
	g.define(fs)
	return fs.Lookup(name) != nil
}

func printFlag(w io.Writer, f *flag.Flag) {
	name, usage := flag.UnquoteUsage(f)
	line := "  -" + f.Name
	if name != "" {
		line += " " + name
	}
	if d := f.DefValue; d != "" && d != "false" && d != "0" && d != "0s" {
		usage += " (default " + d + ")"
	}
	fmt.Fprintf(w, "%s\n%s\n", line, fill("      "+usage, "", 80))
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// parseArgs parses flags that may come before, between or after the
// positional arguments. With stopAtFirst it stops at the first positional
// argument, which is how the command name is found.
func parseArgs(fs *flag.FlagSet, args []string, stopAtFirst bool) ([]string, error) {
	usage := fs.Usage
	fs.Usage = func() {}
	defer func() { fs.Usage = usage }()
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				usage()
				return nil, err
			}
			return nil, usageError("%v", err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if stopAtFirst {
			return append(pos, args...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
		if len(args) > 0 && args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
	}
}

// parse parses a command's flags and checks the number of positional
// arguments.
func (r *runner) parse(fs *flag.FlagSet, args []string, min, max int) ([]string, error) {
	pos, err := parseArgs(fs, args, false)
	if err != nil {
		return nil, err
	}
	switch {
	case len(pos) < min:
		return nil, usageError("%s: missing argument; usage: arcctl %s %s", fs.Name(), fs.Name(), synopsis(fs.Name()))
	case max >= 0 && len(pos) > max:
		return nil, usageError("%s: unexpected argument %q", fs.Name(), pos[max])
	}
	if err := r.checkGlobals(); err != nil {
		return nil, err
	}
	if r.g.ascii {
		r.out, r.errw = asciiWriter{r.env.Stdout}, asciiWriter{r.env.Stderr}
	}
	return pos, nil
}

func synopsis(name string) string {
	c, _ := lookup(name)
	return c.args
}

func (r *runner) checkGlobals() error {
	g := &r.g
	switch {
	case g.emulate != "" && g.replay != "":
		return usageError("--emulate and --replay cannot be used together")
	case g.os != "" && g.os != "mac" && g.os != "win":
		return usageError("--os must be mac or win")
	case g.wait <= 0:
		return usageError("--wait must be positive")
	}
	return nil
}

func (r *runner) keyOS() keys.OS {
	switch {
	case r.g.os == "mac":
		return keys.Mac
	case r.g.os == "win":
		return keys.Win
	case runtime.GOOS == "darwin":
		return keys.Mac
	}
	return keys.Win
}

// parseRange reads "a:b" (end exclusive) or "a+n", in decimal or 0x hex.
func parseRange(s string) (flash.Extent, error) {
	bad := fmt.Errorf("bad range %q: want start:end or start+length, such as 0:256 or 0x60+10", s)
	var e flash.Extent
	if a, b, ok := strings.Cut(s, ":"); ok {
		start, err1 := parseInt(a)
		end, err2 := parseInt(b)
		if err1 != nil || err2 != nil || end <= start {
			return e, bad
		}
		e = flash.Extent{Addr: start, Len: end - start}
	} else if a, n, ok := strings.Cut(s, "+"); ok {
		start, err1 := parseInt(a)
		l, err2 := parseInt(n)
		if err1 != nil || err2 != nil || l <= 0 {
			return e, bad
		}
		e = flash.Extent{Addr: start, Len: l}
	} else {
		return e, bad
	}
	if !(flash.Extent{Addr: 0, Len: flash.Size}).Contains(e) {
		return e, fmt.Errorf("range %v is outside the %d-byte flash", e, flash.Size)
	}
	return e, nil
}

func parseInt(s string) (int, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 0, 32)
	return int(v), err
}
