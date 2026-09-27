// Package cli is arcctl's command line: commands that inspect the receiver
// and the mouse, back them up and decode backups, against the real device,
// the emulator (--emulate) or a recorded transcript (--replay), and the
// journal commands that settle writes a crash left unfinished.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// Exit codes.
const (
	ExitOK         = 0
	ExitFailure    = 1 // anything the codes below do not cover
	ExitUsage      = 2
	ExitNoReceiver = 3
	ExitOffline    = 4 // the receiver answers, the mouse does not
	ExitPermission = 5
	ExitBlocked    = 6 // locked, seized, another client, suspected conflict or stalled
	ExitVerify     = 7 // a write stopped, or the journal holds an unfinished run
	ExitAborted    = 8
	ExitChoose     = 9 // several devices answer; pass --device
)

// Env is everything a command takes from outside the process.
type Env struct {
	Stdout, Stderr io.Writer
	Version        string
	Now            func() time.Time
	Paths          func() (platform.Paths, error)
	Host           Host
	// HID opens the real devices of a backend.
	HID    func(backend string) (session.Devices, error)
	Timing session.Timing
	// Executor tunes the writes; zero fields take the executor's defaults.
	Executor safety.Options
	// Interactive shows progress on Stderr.
	Interactive bool
	// Terminal means stdin and stdout are a terminal the TUI can take.
	Terminal bool
	// TUI runs the terminal UI, which arcctl starts with no command; nil
	// in builds and tests without one.
	TUI func(ctx context.Context, t TUI) error
}

// Main runs arcctl with the process's arguments and returns its exit code.
// The first interrupt cancels the command, which lets a write stop after its
// current record; a second one ends the process at once, as a crash would,
// and the journal settles the write later.
func Main(version string) int { return MainEnv(DefaultEnv(version)) }

// MainEnv is Main in env.
func MainEnv(env Env) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	return Run(ctx, os.Args[1:], env)
}

// DefaultEnv is the environment of a real run.
func DefaultEnv(version string) Env {
	return Env{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Version:     version,
		Now:         time.Now,
		Paths:       platform.DefaultPaths,
		Host:        systemHost{},
		HID:         hidDevices,
		Interactive: isTerminal(os.Stderr),
		Terminal:    isTerminal(os.Stdin) && isTerminal(os.Stdout),
	}
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// exitError ends a command with a code, a message and what to do about it.
type exitError struct {
	code int
	msg  string
	hint string
}

func (e *exitError) Error() string { return e.msg }

func fail(code int, msg, hint string) error { return &exitError{code: code, msg: msg, hint: hint} }

func usageError(format string, a ...any) error {
	return &exitError{code: ExitUsage, msg: fmt.Sprintf(format, a...), hint: "Run 'arcctl help' for the commands and their flags."}
}

type command struct {
	name    string
	args    string
	summary string
	run     func(r *runner, args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"doctor", "[--request]", "check permissions, the receiver, other HID clients and the lock", runDoctor},
		{"info", "[--json]", "show the device: online state, versions, battery, model, tiers", runInfo},
		{"trace", "[--for 60s] [--raw]", "print every input report of every interface; sends nothing", runTrace},
		{"dump", "[--range a:b] [--bin|--json] [-o file]", "read flash and print it as hex, raw bytes or decoded", runDump},
		{"backup", "[--full] [-o file] [--label text]", "save a backup of the mouse configuration", runBackup},
		{"backups", "", "list the saved backups", runBackups},
		{"show", "[--json] <backup|.bin|dump>", "decode a backup, web .bin or dump; no device needed", runShow},
		{"diff", "<backup> [<backup2>]", "compare two backups, or a backup with the device, per record", runDiff},
		{"export-bin", "<backup|.bin|dump> -o file.bin [--allow-partial]", "write a web app compatible .bin (16448 bytes)", runExportBin},
		{"journal", "status | recover [--run id] [--forward|--back|--leave]", "list writes a crash left unfinished, and settle them", runJournal},
		{"redact", "<transcript> -o file", "copy a --record transcript with private bytes masked, for sharing", runRedact},
		{"version", "", "show the version, catalog inputs, verified stages, usbhid patches", runVersion},
		{"help", "[command]", "show help", runHelp},
	}
}

func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// Run executes one command line and returns the exit code.
func Run(ctx context.Context, args []string, env Env) int {
	r := &runner{ctx: ctx, env: env, out: env.Stdout, errw: env.Stderr, g: newGlobals()}
	if r.env.Now == nil {
		r.env.Now = time.Now
	}
	err := r.main(args)
	return r.report(err)
}

func (r *runner) main(args []string) error {
	fs := r.flagSet("arcctl", "")
	fs.Usage = func() { r.usage(r.out) }
	rest, err := parseArgs(fs, args, true)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return r.runTUI()
	}
	c, ok := lookup(rest[0])
	if !ok {
		return usageError("unknown command %q", rest[0])
	}
	r.cmd = c.name
	return c.run(r, rest[1:])
}

func (r *runner) report(err error) int {
	var ee *exitError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.Is(err, context.Canceled) && r.ctx.Err() != nil:
		fmt.Fprintln(r.errw, "arcctl: aborted")
		return ExitAborted
	case errors.As(err, &ee):
		fmt.Fprintln(r.errw, fill("arcctl: "+ee.msg, "  ", 80))
		if ee.hint != "" {
			fmt.Fprintln(r.errw, fill(indent(ee.hint, "  "), "", 80))
		}
		return ee.code
	}
	fmt.Fprintf(r.errw, "arcctl: %v\n", err)
	return ExitFailure
}

func runHelp(r *runner, args []string) error {
	if len(args) == 0 {
		r.usage(r.out)
		return nil
	}
	c, ok := lookup(args[0])
	if !ok || c.name == "help" {
		return usageError("unknown command %q", args[0])
	}
	return c.run(r, []string{"-h"})
}

func (r *runner) usage(w io.Writer) {
	fmt.Fprint(w, "Usage: arcctl [global flags] <command> [flags] [args]\n\n")
	fmt.Fprintln(w, "Commands:")
	r.commandList(w)
	fmt.Fprint(w, "\nGlobal flags (before or after the command):\n")
	fs := flag.NewFlagSet("arcctl", flag.ContinueOnError)
	g := newGlobals()
	g.define(fs)
	fs.VisitAll(func(f *flag.Flag) { printFlag(w, f) })
	fmt.Fprint(w, "\nExit codes: 0 ok, 1 error, 2 usage, 3 no receiver, 4 mouse offline,\n"+
		"5 permission, 6 locked, seized, other client or stalled, 7 a write stopped\n"+
		"or the journal needs recovery, 8 aborted, 9 several devices answer\n"+
		"(pass --device).\n")
}

func (r *runner) commandList(w io.Writer) {
	for _, c := range commands {
		fmt.Fprintf(w, "  %-11s %s\n", c.name, c.summary)
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
