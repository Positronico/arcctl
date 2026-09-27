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
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func runJournal(r *runner, args []string) error {
	fs := r.flagSet("journal", synopsis("journal"))
	fs.Usage = func() { r.journalUsage(r.out) }
	rest, err := parseArgs(fs, args, true)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return journalUsageError("journal: missing subcommand; usage: arcctl journal %s", synopsis("journal"))
	}
	switch rest[0] {
	case "status":
		return runJournalStatus(r, rest[1:])
	case "recover":
		return runJournalRecover(r, rest[1:])
	}
	return journalUsageError("journal: unknown subcommand %q; use status or recover", rest[0])
}

type recoverFlags struct {
	run                  *string
	forward, back, leave *bool
}

func defineRecover(fs *flag.FlagSet) recoverFlags {
	return recoverFlags{
		run:     fs.String("run", "", "settle the unfinished run with this `id`, as 'journal status' names it; needed when several are unfinished"),
		forward: fs.Bool("forward", false, "finish the run: write what it meant to leave"),
		back:    fs.Bool("back", false, "roll the run back: write what its records held before it"),
		leave:   fs.Bool("leave", false, "record the run as settled and write nothing; refused while a record is torn"),
	}
}

// strategy is the choice the flags make, or 0 when they make none.
func (f recoverFlags) strategy() (safety.Strategy, error) {
	var picked []safety.Strategy
	for _, c := range []struct {
		how safety.Strategy
		set bool
	}{{safety.Forward, *f.forward}, {safety.Back, *f.back}, {safety.Leave, *f.leave}} {
		if c.set {
			picked = append(picked, c.how)
		}
	}
	switch len(picked) {
	case 0:
		return 0, nil
	case 1:
		return picked[0], nil
	}
	return 0, journalUsageError("journal recover: choose one of --forward, --back and --leave")
}

func (r *runner) journalUsage(w io.Writer) {
	fmt.Fprint(w, "Usage: arcctl journal status\n       arcctl journal recover [--run id] [--forward|--back|--leave]\n\n")
	fmt.Fprint(w, "List writes a crash left unfinished, and settle them.\n\n")
	sub := func(name, text string) {
		fmt.Fprintln(w, wrap(fmt.Sprintf("%-9s", name), strings.Repeat(" ", 9), strings.Fields(text), " ", 80))
	}
	sub("status", "lists, for each device arcctl wrote to, the runs that need recovery and the last run that changed it. It reads only the journal files.")
	sub("recover", "reads the records of the mouse's unfinished run again and shows what each one holds; with --forward, --back or --leave it settles the run. Runs whose records all hold the old or all the new bytes are settled without asking.")
	fmt.Fprintln(w, "\nFlags of recover:")
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	defineRecover(fs)
	fs.VisitAll(func(f *flag.Flag) { printFlag(w, f) })
	fmt.Fprintln(w, "\n"+fill("Global flags: see 'arcctl help'. Of the write flags, --dry-run and --allow-foreign-client apply to recover.", "", 80))
}

func journalUsageError(format string, a ...any) error {
	return fail(ExitUsage, fmt.Sprintf(format, a...), "Run 'arcctl help journal' for its subcommands and flags.")
}

// realDevices refuses the emulator and replays: only real devices have a
// journal.
func (r *runner) realDevices(cmd string) error {
	if r.g.emulate != "" || r.g.replay != "" {
		return journalUsageError("%s: only real devices have a journal; --emulate and --replay keep none", cmd)
	}
	return nil
}

// deviceJournal is the journal of one device, by its identity key.
type deviceJournal struct {
	key    string
	status *safety.Status
	err    error
}

// readJournals reads the journal of every device under root, leaving out
// devices that have no run.
func readJournals(root string) ([]deviceJournal, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []deviceJournal
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		st, err := safety.Load(filepath.Join(root, e.Name()))
		if err == nil && len(st.Runs) == 0 {
			continue
		}
		out = append(out, deviceJournal{key: e.Name(), status: st, err: err})
	}
	return out, nil
}

func runJournalStatus(r *runner, args []string) error {
	fs := r.flagSet("journal status", "")
	fs.Usage = func() { r.journalUsage(r.out) }
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if err := r.realDevices("journal status"); err != nil {
		return err
	}
	paths, err := r.env.Paths()
	if err != nil {
		return err
	}
	devs, err := readJournals(paths.Journal)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "Journal %s\n", paths.Journal)
	if len(devs) == 0 {
		fmt.Fprintln(r.out, "Empty: arcctl has not written to any device.")
		return nil
	}
	if who := lockHolder(paths.Lock); who != "" {
		fmt.Fprintln(r.out, fill(who+" is running: a run it is still writing shows here as unfinished.", "", 80))
	}
	open, unchecked, bad := 0, 0, 0
	for _, d := range devs {
		fmt.Fprintln(r.out)
		if d.err != nil {
			bad++
			fmt.Fprintln(r.out, "Identity "+d.key)
			fmt.Fprintln(r.out, fill("  unreadable: "+plain(d.err), "  ", 80))
			continue
		}
		open += len(d.status.Open)
		unchecked += len(d.status.Unsettled)
		r.printDeviceJournal(d)
	}
	switch {
	case bad > 0:
		return fail(ExitFailure, count(bad, "device journal", "device journals")+" could not be read",
			"arcctl does not write to a device whose journal it cannot read. Keep the files: they hold the bytes of every write.")
	case open > 0:
		return fail(ExitVerify, count(open, "unfinished run needs", "unfinished runs need")+" recovery",
			"Connect the mouse and run 'arcctl journal recover': it reads the records again and shows the choices.")
	case unchecked > 0:
		return fail(ExitVerify, count(unchecked, "factory reset was", "factory resets were")+" never checked",
			"Connect the mouse and run 'arcctl journal recover': it reads the mouse again and records what the reset did.")
	}
	return nil
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// lockHolder names another arcctl that holds the single-instance lock, or
// returns "".
func lockHolder(path string) string {
	l, err := platform.AcquireLock(path)
	var le *platform.LockedError
	switch {
	case err == nil:
		_ = l.Release()
	case errors.As(err, &le) && le.PID != 0:
		return fmt.Sprintf("Another arcctl (pid %d)", le.PID)
	case errors.As(err, &le):
		return "Another arcctl"
	}
	return ""
}

func (r *runner) printDeviceJournal(d deviceJournal) {
	st := d.status
	fmt.Fprintf(r.out, "%s, identity %s\n", deviceName(st.Runs[0].Device), d.key)
	for _, run := range st.Open {
		r.printRun("Unfinished", run)
		for _, x := range run.Recoveries {
			fmt.Fprintf(r.out, "    recovery %s\n", x.ID)
			fmt.Fprintln(r.out, fill("      started "+when(x.Started)+"; "+runState(x), "  ", 80))
		}
	}
	for _, run := range st.Unsettled {
		fmt.Fprintf(r.out, "  Unchecked %s\n", runName(run))
		r.printRunState("    ", run)
		fmt.Fprintln(r.out, fill("    arcctl ended after the packet and before it read the mouse again, so what the reset did "+
			"is not known. The next session that loads the mouse reads it again and compares it with the backup taken before "+
			"the reset: "+run.Reset.Backup, "      ", 80))
	}
	if st.Clean() {
		fmt.Fprintln(r.out, "  Clean: nothing to recover.")
	}
	if st.Last == nil {
		fmt.Fprintln(r.out, "  Last change: none")
		return
	}
	r.printRun("Last change:", st.Last)
}

func (r *runner) printRun(label string, run *safety.Run) {
	fmt.Fprintf(r.out, "  %s %s\n", label, runName(run))
	r.printRunState("    ", run)
	fmt.Fprintln(r.out, opHeader("    "))
	for _, o := range run.Ops {
		fmt.Fprintln(r.out, opRow("    ", o.Op, opState(o)))
	}
}

func runName(run *safety.Run) string { return run.Kind.String() + " " + run.ID }

// resetVerdict says what the check of a factory reset found.
func resetVerdict(x *safety.ResetRecord) string {
	switch x.Verdict {
	case safety.VerdictChanged:
		parts := make([]string, len(x.Changed))
		for i, e := range x.Changed {
			parts[i] = e.String()
		}
		return "it changed " + strings.Join(parts, ", ")
	case safety.VerdictUnchanged:
		return "it changed nothing arcctl reads"
	}
	return "it could not be checked"
}

// printRunState says when a run started, on which profile, and how it
// ended. Run IDs vary in length, so no line holds more than one.
func (r *runner) printRunState(indent string, run *safety.Run) {
	if run.Kind == safety.KindRevert {
		fmt.Fprintln(r.out, indent+"undoes "+run.Of)
	}
	s := "started " + when(run.Started)
	if run.Profile != nil {
		s += fmt.Sprintf(" on profile %d", *run.Profile)
	}
	fmt.Fprintln(r.out, indent+s)
	fmt.Fprintln(r.out, fill(indent+runState(run), "  ", 80))
}

func when(t time.Time) string { return t.UTC().Format(time.DateTime) + " UTC" }

func runState(run *safety.Run) string {
	switch {
	case run.Complete:
		return "complete"
	case run.Resolved == safety.Leave:
		return "stopped, then left as it was"
	case run.Resolved != 0 && run.By != "":
		return fmt.Sprintf("stopped, then settled %s by %s", run.Resolved, run.By)
	case run.Resolved != 0:
		return fmt.Sprintf("stopped, then settled %s without writing", run.Resolved)
	case run.Ended:
		return "stopped: " + plainText(run.Err)
	}
	return "did not end: arcctl stopped during the write"
}

func opState(o safety.OpRecord) string {
	s := o.State.String()
	if o.State == safety.StateFailed && o.Class != safety.ClassUnknown {
		s += ", " + o.Class.String()
	}
	return s
}

func opHeader(indent string) string {
	return fmt.Sprintf("%s%3s  %-9s  %-12s  %s", indent, "Op", "Record", "State", "What")
}

func opRow(indent string, op plan.Op, state string) string {
	first := fmt.Sprintf("%s%3d  %-9s  %-12s  ", indent, op.Seq, op.Extent, state)
	return wrap(first, strings.Repeat(" ", len(first)), strings.Fields(op.Desc), " ", 80)
}

func deviceName(id plan.Identity) string {
	if m, ok := catalog.Resolve(id.CID, id.MID); ok {
		return m.Name
	}
	return fmt.Sprintf("Unknown device cid 0x%02x mid %d", id.CID, id.MID)
}

// plain drops the package prefixes from an error's text.
func plain(err error) string { return plainText(err.Error()) }

func plainText(s string) string {
	return strings.NewReplacer("safety: ", "", "session: ", "", "wire: ", "", "hidio: ", "").Replace(s)
}

func runJournalRecover(r *runner, args []string) error {
	fs := r.flagSet("journal recover", "")
	fs.Usage = func() { r.journalUsage(r.out) }
	f := defineRecover(fs)
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	how, err := f.strategy()
	if err != nil {
		return err
	}
	if err := r.realDevices("journal recover"); err != nil {
		return err
	}
	paths, err := r.env.Paths()
	if err != nil {
		return err
	}
	before, _ := readJournals(paths.Journal)
	r.writes = &session.Writes{
		Journal: paths.Journal,
		Backups: backup.Store{Root: paths.Backups, Tool: "arcctl " + r.env.Version, Source: backup.SourceDevice, OS: r.keyOS(), Now: r.env.Now},
		// The session runs only after this process took the lock.
		Lock:     func() error { return nil },
		Executor: r.env.Executor,
	}
	c, sn, err := r.connect(loaded)
	if err != nil {
		return err
	}
	defer c.stop()
	if err := loadable(sn); err != nil {
		return err
	}
	js := sn.Journal
	switch {
	case js == nil:
		return fail(ExitFailure, "the journal of this mouse was not checked", "")
	case js.Err != nil:
		return fail(ExitFailure, "the journal of this mouse could not be read: "+plain(js.Err),
			"arcctl does not write to a device whose journal it cannot read. Keep the files: they hold the bytes of every write.")
	}
	key := sn.Identity.Key()
	fmt.Fprintf(r.out, "%s, identity %s\n", sn.Model.Name, key)
	settled := settledOnLoad(before, key, filepath.Join(paths.Journal, key))
	for _, run := range settled {
		r.printSettled(run)
	}
	open := js.Open
	if *f.run != "" {
		i := slices.IndexFunc(open, func(o session.OpenRun) bool { return o.Run.ID == *f.run })
		switch {
		case i >= 0:
			open = open[i : i+1]
		case slices.ContainsFunc(settled, func(run *safety.Run) bool { return run.ID == *f.run }):
			open = nil
		default:
			return journalUsageError("journal recover: %q is not an unfinished run of this mouse; 'arcctl journal status' lists them", *f.run)
		}
	}
	dev := safety.Device{Identity: sn.Identity, Profile: profileOf(sn), Layout: mouse.Layout(sn.Model)}
	switch {
	case len(open) == 0:
		fmt.Fprintln(r.out, "Nothing to recover: the journal of this mouse is clean.")
		return nil
	case len(open) > 1:
		ids := make([]string, len(open))
		for i, o := range open {
			r.printOpen(o, dev, false)
			ids[i] = o.Run.ID
		}
		return journalUsageError("journal recover: %d runs are unfinished; pass --run with one of %s", len(open), strings.Join(ids, ", "))
	case how == 0:
		r.printOpen(open[0], dev, true)
		if open[0].Inspection == nil {
			return fail(ExitVerify, "the records of the run could not be read again",
				"Clear the cause shown above, then run 'arcctl journal recover' again.")
		}
		return fail(ExitVerify, "run "+open[0].Run.ID+" needs a decision",
			"Run 'arcctl journal recover' again with --forward, --back or --leave. Add --dry-run to see the packets first.")
	}
	return r.settle(c, open[0].Run, how, filepath.Join(paths.Journal, key))
}

// lastDesc describes what the run meant to leave in e: the description of
// its last op there.
func lastDesc(run *safety.Run, e flash.Extent) string {
	desc := ""
	for _, o := range run.Ops {
		if o.Extent == e {
			desc = o.Desc
		}
	}
	return desc
}

func profileOf(sn *session.Snapshot) *byte {
	if !sn.Profile.Supported {
		return nil
	}
	v := sn.Profile.Value
	return &v
}

// settledOnLoad lists the runs that were open before the session started and
// that its check after the load settled without writing.
func settledOnLoad(before []deviceJournal, key, dir string) []*safety.Run {
	i := slices.IndexFunc(before, func(d deviceJournal) bool { return d.key == key })
	if i < 0 || before[i].status == nil {
		return nil
	}
	after, err := safety.Load(dir)
	if err != nil {
		return nil
	}
	var out []*safety.Run
	for _, run := range before[i].status.Open {
		if now := after.Find(run.ID); now != nil && now.Resolved != 0 {
			out = append(out, now)
		}
	}
	for _, run := range before[i].status.Unsettled {
		if now := after.Find(run.ID); now != nil && now.Ended {
			out = append(out, now)
		}
	}
	return out
}

func (r *runner) printSettled(run *safety.Run) {
	if run.Kind == safety.KindReset {
		fmt.Fprintf(r.out, "Checked %s: %s\n", runName(run), resetVerdict(run.Reset))
		fmt.Fprintln(r.out, fill("  the backup from before it: "+run.Reset.Backup, "    ", 80))
		return
	}
	what := "every record already held what the run meant to leave"
	if run.Resolved == safety.Back {
		what = "every record still held what it held before the run"
	}
	fmt.Fprintf(r.out, "Settled %s %s:\n", runName(run), run.Resolved)
	fmt.Fprintln(r.out, fill("  "+what+"; nothing was written.", "  ", 80))
}

// printOpen shows an unfinished run and what its records hold now, and with
// choices what each strategy would write.
func (r *runner) printOpen(o session.OpenRun, dev safety.Device, choices bool) {
	fmt.Fprintf(r.out, "\nUnfinished %s\n", runName(o.Run))
	r.printRunState("  ", o.Run)
	in := o.Inspection
	if in == nil {
		msg := "its records could not be read again"
		if o.Err != nil {
			msg += ": " + plain(o.Err)
		}
		fmt.Fprintln(r.out, fill("  "+msg, "  ", 80))
		return
	}
	fmt.Fprintf(r.out, "\n  %-9s  %-4s  %s\n", "Record", "Now", "What")
	for _, e := range in.Extents {
		first := fmt.Sprintf("  %-9s  %-4s  ", e.Extent, e.Class)
		fmt.Fprintln(r.out, wrap(first, strings.Repeat(" ", len(first)), strings.Fields(lastDesc(o.Run, e.Extent)), " ", 80))
	}
	fmt.Fprintln(r.out, "\n"+fill("  old: as before the run; new: as the run meant to leave; mid: a step in between, such as a binding disabled while its body is rewritten; torn: neither, the write was cut short.", "", 80))
	if !choices {
		return
	}
	dev.Image = in.Image
	fmt.Fprintln(r.out)
	for _, c := range []struct {
		how  safety.Strategy
		what string
	}{{safety.Forward, "finish it: write what the run meant to leave"}, {safety.Back, "roll it back: write what the records held before it"}} {
		p, err := safety.RecoveryPlan(in, c.how, dev)
		text := c.what
		switch {
		case err != nil:
			text = "not possible: " + plain(err)
		case len(p.Ops) == 0:
			text += " (nothing to write)"
		default:
			text += fmt.Sprintf(" (%s)", count(len(p.Ops), "write", "writes"))
		}
		fmt.Fprintln(r.out, wrap(fmt.Sprintf("  --%-8s ", c.how), strings.Repeat(" ", 13), strings.Fields(text), " ", 80))
	}
	leave := "record the run as settled; nothing is written"
	if in.Torn() {
		leave = "refused while a record is torn"
	}
	fmt.Fprintf(r.out, "  --%-8s %s\n", safety.Leave, leave)
}

// settle writes the recovery of run and reports it; dir is the device's
// journal folder.
func (r *runner) settle(c *conn, run *safety.Run, how safety.Strategy, dir string) error {
	g := r.g.gates()
	if g.DryRun {
		fmt.Fprintln(r.out, fill("Dry run: the packets go to an overlay and the read-back reads from it; nothing reaches the mouse.", "", 80))
	}
	switch how {
	case safety.Forward:
		fmt.Fprintf(r.out, "Finishing %s\n", runName(run))
	case safety.Back:
		fmt.Fprintf(r.out, "Rolling back %s\n", runName(run))
	default:
		fmt.Fprintf(r.out, "Leaving %s as it is\n", runName(run))
	}
	out, err := r.write(func(ctx context.Context, on func(safety.OpEvent)) (session.Outcome, error) {
		return c.s.Recover(ctx, run.ID, how, g, on)
	})
	if len(out.Backups) > 0 {
		fmt.Fprintln(r.out, "Backup saved before the write:")
		for _, b := range out.Backups {
			fmt.Fprintln(r.out, "  "+b)
		}
	}
	if err != nil {
		return writeError(err, "recovery", run)
	}
	switch {
	case g.DryRun && how == safety.Leave:
		fmt.Fprintf(r.out, "Dry run: run %s would be left as it is.\n", run.ID)
	case g.DryRun:
		fmt.Fprintf(r.out, "Dry run: nothing was written. The recovery would send %s:\n", count(len(out.Packets), "packet", "packets"))
		for _, p := range out.Packets {
			fmt.Fprintln(r.out, "  "+p.String())
		}
	case how == safety.Leave:
		fmt.Fprintf(r.out, "Run %s is settled: left as it is.\nNothing was written.\n", run.ID)
	case out.Run == "":
		fmt.Fprintf(r.out, "Run %s is settled %s.\nEvery record already held those bytes.\n", run.ID, how)
	default:
		fmt.Fprintf(r.out, "Run %s is settled %s.\nRecovery run %s: %s verified.\n", run.ID, how, out.Run, count(out.Verified, "write", "writes"))
	}
	if out.Echoes > 0 {
		fmt.Fprintf(r.out, "%s differed from the request; the read-back decided.\n", count(out.Echoes, "cmd-7 reply", "cmd-7 replies"))
	}
	if out.Overlap {
		fmt.Fprintln(r.out, fill("The mouse reported a change to the last record while it was written; arcctl read it again.", "", 80))
	}
	if g.DryRun {
		return nil
	}
	st, err := safety.Load(dir)
	switch {
	case err != nil:
		return fail(ExitFailure, "the journal could not be read again: "+plain(err), "")
	case !st.Clean():
		return fail(ExitVerify, count(len(st.Open), "unfinished run remains", "unfinished runs remain"),
			"Run 'arcctl journal recover' again to settle it.")
	}
	fmt.Fprintln(r.out, "The journal of this mouse is clean.")
	return nil
}

// write runs a write and prints its progress: a row for each record verified
// or failed, and a notice while it waits for the mouse or the screen. An
// interrupt asks the write to stop after its current record; the outcome
// still comes back.
func (r *runner) write(call func(context.Context, func(safety.OpEvent)) (session.Outcome, error)) (session.Outcome, error) {
	p := &progressPrinter{r: r}
	type result struct {
		out session.Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := call(r.ctx, p.event)
		done <- result{out, err}
	}()
	select {
	case res := <-done:
		return res.out, res.err
	case <-r.ctx.Done():
		p.notice("stopping after the current record. Press Ctrl-C again to quit at once; the journal then keeps the write for 'arcctl journal recover'.")
	}
	res := <-done
	return res.out, res.err
}

// progressPrinter prints a write's events, which arrive on the session's
// goroutine.
type progressPrinter struct {
	r      *runner
	mu     sync.Mutex
	header bool
}

func (p *progressPrinter) event(e safety.OpEvent) {
	switch e.Kind {
	case safety.EventVerified:
		p.row(e.Op, "verified")
	case safety.EventFailed:
		p.row(e.Op, failedState(e))
	case safety.EventPaused:
		p.notice(pauseText(e.Err, p.r.env.Executor))
	case safety.EventResumed:
		p.notice("the mouse answers again; the write goes on")
	case safety.EventRestart:
		p.notice(fmt.Sprintf("writing %s again from its first chunk", e.Op.Extent))
	}
}

func failedState(e safety.OpEvent) string {
	switch {
	case betweenOps(e.Err):
		return "not written"
	case e.Class != safety.ClassUnknown:
		return "failed, " + e.Class.String()
	}
	return "failed"
}

// betweenOps reports whether a run stopped before an op because the user or
// a push asked it to.
func betweenOps(err error) bool {
	return errors.Is(err, safety.ErrAborted) || errors.Is(err, safety.ErrOverlap)
}

func (p *progressPrinter) row(op plan.Op, state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.header {
		fmt.Fprintln(p.r.out, opHeader("  "))
		p.header = true
	}
	fmt.Fprintln(p.r.out, opRow("  ", op, state))
}

func (p *progressPrinter) notice(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.r.errw, fill("arcctl: "+text, "  ", 80))
}

func pauseText(err error, o safety.Options) string {
	if hidio.Classify(err) == hidio.ClassLocked {
		wait := cmp0(o.LockWait, safety.DefaultLockWait)
		return fmt.Sprintf("the screen is locked or Secure Input is on; waiting up to %s for it to clear", wait)
	}
	wait := cmp0(o.OfflineWait, safety.DefaultOfflineWait)
	return fmt.Sprintf("the mouse went to sleep; waiting up to %s for it to wake (move it)", wait)
}

func cmp0(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// writeError explains why a write (what names it) of run did not finish.
func writeError(err error, what string, run *safety.Run) error {
	var pe *safety.PreflightError
	var se *safety.StopError
	const again = "The journal keeps the old and new bytes of every record. Run 'arcctl journal recover' again once the cause is cleared."
	switch {
	case errors.As(err, &pe):
		return blockedWrite(pe, what, run)
	case errors.As(err, &se) && errors.Is(se.Err, safety.ErrAborted):
		return fail(ExitAborted, fmt.Sprintf("the %s was aborted before %s (%s)", what, se.Op.Extent, se.Op.Desc),
			"The records before it are written and verified. Run 'arcctl journal recover' to finish or roll back.")
	case errors.As(err, &se):
		msg := fmt.Sprintf("the %s stopped at %s (%s)", what, se.Op.Extent, se.Op.Desc)
		if se.Written {
			chunks := (se.Op.Extent.Len + wire.MaxData - 1) / wire.MaxData
			msg += fmt.Sprintf(" with %d of %s acknowledged", se.Chunks, count(chunks, "chunk", "chunks"))
			if se.Class != safety.ClassUnknown {
				msg += "; the record is " + se.Class.String()
			}
		}
		msg += ": " + plain(se.Err)
		code := ExitVerify
		switch {
		case errors.Is(se.Err, context.Canceled):
			code = ExitAborted
		case blocks(se.Err):
			code = ExitBlocked
		case errors.Is(se.Err, safety.ErrOffline):
			code = ExitOffline
		}
		return fail(code, msg, again)
	case errors.Is(err, safety.ErrAborted):
		return fail(ExitAborted, plain(err), "")
	case errors.Is(err, safety.ErrTorn):
		return fail(ExitVerify, "a record is torn, so the run cannot be left as it is", "Choose --forward or --back.")
	case errors.Is(err, safety.ErrIdentity), errors.Is(err, session.ErrDeviceChanged):
		return fail(ExitFailure, "a different mouse answers now: "+plain(err),
			"Pair or wake the mouse the run was written to, then retry.")
	case errors.Is(err, session.ErrPanic):
		return fail(ExitFailure, plain(err), again)
	}
	return fail(ExitFailure, plain(err), "")
}

// blocks reports whether err says the device or the receiver is taken or
// blocked: locked, seized, stalled, in a conflict, held by another client or
// arcctl, or behind the screen lock or Secure Input.
func blocks(err error) bool {
	for _, target := range []error{safety.ErrBlocked, safety.ErrConflict, safety.ErrForeignClient, safety.ErrNoLock,
		safety.ErrScreenLocked, safety.ErrSecureInput} {
		if errors.Is(err, target) {
			return true
		}
	}
	switch hidio.Classify(err) {
	case hidio.ClassLocked, hidio.ClassSeized, hidio.ClassStalled:
		return true
	}
	return false
}

// blockedWrite lists every check that blocked a write. The exit code is
// that of a blocked device when any check says so, else that of a sleeping
// mouse when it sleeps.
func blockedWrite(pe *safety.PreflightError, what string, run *safety.Run) error {
	code := ExitFailure
	var lines []string
	for _, f := range pe.Failures {
		switch {
		case blocks(f):
			code = ExitBlocked
		case errors.Is(f, safety.ErrOffline) && code == ExitFailure:
			code = ExitOffline
		}
		text := plain(f)
		if errors.Is(f, safety.ErrProfile) && run != nil && run.Profile != nil {
			text = fmt.Sprintf("the run was written on onboard profile %d, and the mouse is on another one now; switch it back to %d, then retry", *run.Profile, *run.Profile)
		}
		lines = append(lines, wrap("- ", "  ", strings.Fields(text), " ", 78))
	}
	return fail(code, "the "+what+" is blocked", strings.Join(lines, "\n"))
}
