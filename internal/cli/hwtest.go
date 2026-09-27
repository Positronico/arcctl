//go:build hwtest

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/hwtest"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// This init runs after the one in cli.go, which builds the table: a package
// initializes its files in name order.
func init() {
	commands = append(commands, command{"hwtest", "--stage name [--steps names] [--dry-run] [--repo dir] [--trace-for d] [--debug-abort-after-chunk n]",
		"run a hardware test stage, with you at the mouse", runHWTest})
}

func (r *runner) hwUsage(fs *flag.FlagSet) {
	fmt.Fprint(r.out, "Usage: arcctl hwtest --stage name [--steps names] [--dry-run] [--repo dir]\n")
	fmt.Fprint(r.out, "                     [--trace-for d] [--debug-abort-after-chunk n]\n\n")
	fmt.Fprintln(r.out, fill("Run a hardware test stage, with you at the mouse. A write stage takes a fresh full backup, "+
		"prints every step with its exact packets, and writes only after you confirm.", "", 80))
	fmt.Fprintln(r.out, "\nFlags:")
	fs.VisitAll(func(f *flag.Flag) {
		if !isGlobal(f.Name) {
			printFlag(r.out, f)
		}
	})
	fmt.Fprintln(r.out, "\n"+fill("With --dry-run, a write stage (every stage but H0) prints only that preview and exits: "+
		"it takes no backup, asks nothing, writes nothing and records nothing, and needs no tier flag. H0 writes nothing "+
		"and has no dry run.", "", 80))
	fmt.Fprintln(r.out, "\n"+fill("With --steps, H0 runs only the steps named, in the stage's order: "+strings.Join(hwtest.Steps("H0"), ", ")+
		" (dump needs backups in the same run). The run gets its own log entry, \"H0 (steps: ...)\", and its own transcript. "+
		"The log counts H0 as passed once each of its steps has passed in some recorded run on the same mouse firmware.", "", 80))
	fmt.Fprintln(r.out, "\n"+fill("A stage that ended after H5's torn-write drill or after H7's factory reset leaves a "+
		"checkpoint in the logs folder, and its next run goes on from there: the reset is never sent again.", "", 80))
	fmt.Fprintln(r.out, "\n"+fill("Global flags: see 'arcctl help'. The write flags --allow-untested, --experimental "+
		"and --allow-foreign-client apply to the stage's writes; --emulate runs it as a rehearsal.", "", 80))
}

func runHWTest(r *runner, args []string) error {
	fs := r.flagSet("hwtest", synopsis("hwtest"))
	fs.Usage = func() { r.hwUsage(fs) }
	stage := fs.String("stage", "", "the `stage` to run: "+strings.Join(hwtest.Stages(), ", "))
	steps := fs.String("steps", "", "run only these steps of H0, comma-separated (`names` as below)")
	repo := fs.String("repo", "", "the arcctl checkout that gets the transcripts, the log entry and verified.json (default: the one around the current folder; a rehearsal with --emulate records to a temporary folder)")
	traceFor := fs.Duration("trace-for", hwtest.DefaultTraceFor, "how long H0 listens while the web app is connected")
	abort := fs.Int("debug-abort-after-chunk", 0, "end the process right after chunk `n` of a record is acknowledged, as a crash would; "+
		"the journal settles the write on the next start. H5 needs it, with n from 1 to 10, and applies it only to its torn-write drill")
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	switch {
	case !slices.ContainsFunc(hwtest.Stages(), func(s string) bool { return strings.EqualFold(s, *stage) }):
		return usageError("hwtest: --stage must be one of %s", strings.Join(hwtest.Stages(), ", "))
	case r.g.replay != "":
		return usageError("hwtest: a replay cannot run a hardware test")
	case r.g.record != "":
		return usageError("hwtest: the stage records its own transcript; drop --record")
	case *abort < 0:
		return usageError("hwtest: --debug-abort-after-chunk must be positive")
	}
	var picked []string
	for _, s := range strings.Split(*steps, ",") {
		if s = strings.TrimSpace(s); s != "" {
			picked = append(picked, s)
		}
	}
	if err := hwtest.CheckSteps(*stage, picked); err != nil {
		return usageError("%v", err)
	}
	paths, err := r.env.Paths()
	if err != nil {
		return err
	}
	w, err := r.world()
	if err != nil {
		return err
	}
	defer w.close()
	raw, source := hwtest.HID(r.g.backend), backup.SourceDevice
	root, logs, journal, backups := *repo, paths.Logs, paths.Journal, paths.Backups
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root, _ = hwtest.FindRepo(wd)
		}
	}
	dump := ""
	if root != "" {
		dump = filepath.Join(root, "testdata", "flash-dump.bin")
	}
	if bus, ok := w.devices.(*emu.Bus); ok {
		raw, source = bus, backup.SourceEmulator
		tmp, err := os.MkdirTemp("", "arcctl-hwtest-")
		if err != nil {
			return err
		}
		if r.g.dryRun {
			defer os.RemoveAll(tmp)
		} else {
			fmt.Fprintf(r.errw, "arcctl: a rehearsal on the emulator; its records, backups and journal go to %s\n", tmp)
		}
		root = filepath.Join(tmp, "records")
		logs, journal, backups = filepath.Join(tmp, "logs"), filepath.Join(tmp, "journal"), filepath.Join(tmp, "backups")
	} else if root == "" && !r.g.dryRun {
		return usageError("hwtest: run it inside the arcctl checkout, or pass --repo")
	}
	if w.locks {
		release, err := r.lock()
		if err != nil {
			return err
		}
		defer release()
	}
	r.writes = &session.Writes{
		Journal: journal,
		Backups: backup.Store{Root: backups, Tool: "arcctl " + r.env.Version, Source: source, OS: r.keyOS(), Now: r.env.Now},
		// The stage's sessions run only after this process took the lock.
		Lock:     func() error { return nil },
		Executor: r.env.Executor,
	}
	opt := r.options(w)
	logClose, err := r.logger(&opt)
	if err != nil {
		return err
	}
	defer logClose()
	wait := hwtest.DefaultWait
	if r.g.wait != defaultWait {
		wait = r.g.wait
	}
	cfg := hwtest.Config{
		Raw:             raw,
		Session:         opt,
		Host:            hwHost{r: r, w: w},
		Prompt:          hwtest.Terminal(os.Stdin, r.out),
		Repo:            root,
		Logs:            logs,
		Backups:         backups,
		Dump:            dump,
		Source:          source,
		Tool:            "arcctl " + r.env.Version,
		OS:              r.keyOS(),
		Steps:           picked,
		Gates:           r.g.gates(),
		Wait:            wait,
		TraceFor:        *traceFor,
		AbortAfterChunk: *abort,
		Now:             r.env.Now,
	}
	res, err := hwtest.Run(r.ctx, cfg, *stage)
	switch {
	case errors.Is(err, hwtest.ErrNoDryRun):
		return usageError("%v; drop --dry-run", err)
	case res == nil:
		return err
	}
	if res.Recorded {
		r.hwSummary(res, root)
	}
	switch {
	case err != nil:
		return fmt.Errorf("the stage's records are incomplete: %w", err)
	case errors.Is(res.Err, context.Canceled):
		return res.Err
	case res.DryRun && res.Err != nil:
		return fail(ExitFailure, fmt.Sprintf("the dry run of stage %s stopped: %v", res.Stage, res.Err), "")
	case res.DryRun:
		return nil
	case errors.Is(res.Err, hwtest.ErrFlags):
		return usageError("%v", res.Err)
	case errors.Is(res.Err, hwtest.ErrDeclined), errors.Is(res.Err, hwtest.ErrConfirm):
		return fail(ExitAborted, fmt.Sprintf("stage %s did not run: %v", res.Stage, res.Err), "")
	case !res.Recorded:
		return fail(ExitFailure, fmt.Sprintf("stage %s did not start: %v", res.Stage, res.Err), "")
	case !res.Passed && res.Err != nil:
		return fail(ExitVerify, fmt.Sprintf("stage %s failed: %v", res.Stage, res.Err),
			"The log entry lists each step. After a write stage, 'arcctl journal status' shows whether a write was left unfinished.")
	case !res.Passed:
		return fail(ExitVerify, fmt.Sprintf("stage %s failed", res.Stage), "The log entry lists the steps and answers that did not pass.")
	}
	return nil
}

func (r *runner) hwSummary(res *hwtest.Result, root string) {
	verdict := "failed"
	if res.Passed {
		verdict = "passed"
	}
	stage := res.Stage
	if len(res.Selected) > 0 {
		stage += " (steps: " + strings.Join(res.Selected, ", ") + ")"
	}
	fmt.Fprintf(r.out, "\nStage %s %s.\n", stage, verdict)
	if res.Status != "" {
		fmt.Fprintf(r.out, "  Stage status: %s\n", res.Status)
	}
	fmt.Fprintf(r.out, "  Log: %s\n", filepath.Join(root, "docs", "hardware-tests.md"))
	for _, t := range res.Transcripts {
		fmt.Fprintf(r.out, "  Transcript: %s\n", filepath.Join(root, filepath.FromSlash(t)))
	}
	for _, b := range res.Backups {
		fmt.Fprintf(r.out, "  Backup: %s\n", b)
	}
	for _, v := range res.Promoted {
		fmt.Fprintf(r.out, "  Verified: %s on %s %s\n", v.Feature, v.Model, v.Firmware)
	}
}

// hwHost answers H0's questions from the OS, or from the emulator.
type hwHost struct {
	r *runner
	w *world
}

func (h hwHost) Doctor(out io.Writer) (hwtest.Doctor, error) {
	sub := &runner{ctx: h.r.ctx, env: h.r.env, out: out, errw: out, g: h.r.g, cmd: "doctor"}
	_ = runDoctor(sub, nil)
	var d hwtest.Doctor
	p, err := h.w.host.Permission()
	if err != nil {
		return d, err
	}
	d.Access, d.App = p.Access.String(), p.App.Name
	devs, err := h.w.host.Clients()
	if errors.Is(err, errors.ErrUnsupported) {
		return d, nil
	}
	for _, dev := range devs {
		for _, c := range dev.Clients {
			if !c.Self && !slices.Contains(d.Clients, orName(c.Name)) {
				d.Clients = append(d.Clients, orName(c.Name))
			}
		}
	}
	return d, err
}

func (h hwHost) Console() (safety.Console, error) { return consoleOf(h.w.host) }

func (h hwHost) Clients() ([]safety.Client, error) {
	devs, err := h.w.host.Clients()
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var out []safety.Client
	for _, d := range devs {
		for _, c := range platform.Foreign(d.Clients, nil) {
			if !slices.ContainsFunc(out, func(x safety.Client) bool { return x.PID == c.PID }) {
				out = append(out, safety.Client{PID: c.PID, Name: orName(c.Name), Seized: c.Seized})
			}
		}
	}
	return out, nil
}

func (h hwHost) Permission() (string, error) {
	p, err := h.w.host.Permission()
	if err != nil {
		return "", err
	}
	s := p.Access.String()
	if p.App.Name != "" {
		s += " for " + p.App.Name
	}
	return s, nil
}
