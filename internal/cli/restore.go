package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// restoreReadRounds bounds the reads a restore asks for: the headers of the
// bodies, then their records.
const restoreReadRounds = 3

// runRestore writes a backup or a web .bin back to the mouse: the records
// the two hold differently, planned record by record (backup.PlanRestore),
// previewed, confirmed, and written through the session's Apply, so the
// preflight, the I1 backup, the journal and the read-back all run.
func runRestore(r *runner, args []string) error {
	fs := r.flagSet("restore", synopsis("restore"))
	var o backup.RestoreOptions
	fs.BoolVar(&o.IncludeUnknown, "include-unknown", false, "also write back, as the source captured them, the settings-page records arcctl knows no valid value for; experimental, never a byte it did not capture")
	fs.BoolVar(&o.OtherDevice, "other-device", false, "restore a backup of another mouse of the same model and sensor, or a web .bin, which names no mouse")
	fs.BoolVar(&o.OtherProfile, "other-profile", false, "restore a source read on another onboard profile, or on an unknown one")
	yes := fs.Bool("yes", false, "do not ask before writing; a plan with untested records still needs its phrase typed")
	pos, err := r.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	if r.g.replay != "" {
		return usageError("restore: --replay is read-only; restore to the mouse, or to the emulator with --emulate")
	}
	src, err := backup.Open(pos[0])
	if err != nil {
		return err
	}
	if src.Kind == backup.KindDump {
		return fail(ExitFailure, "a raw dump records no device, and arcctl does not restore from one",
			"Restore from an arcctl backup, or from a web .bin.")
	}
	w, err := r.world()
	if err != nil {
		return err
	}
	c, sn, err := r.restoreSession(w)
	if err != nil {
		return err
	}
	defer c.stop()
	if err := loadable(sn); err != nil {
		return err
	}
	if sn.Model.Family != catalog.FamilyMouse {
		return fail(ExitFailure, sn.Model.Name+" is not a mouse; arcctl restores only mice in this build", "")
	}
	t := restoreTarget(sn, w.source)
	if _, err := backup.CheckSource(src, t, o); err != nil {
		return sourceError(err)
	}
	t.Image, err = r.restoreReads(c, src.Image, sn.Image)
	if err != nil {
		return err
	}
	rs, err := backup.PlanRestore(src, t, o)
	if err != nil {
		return fail(ExitFailure, "the restore cannot be planned: "+plainText(err.Error()), "")
	}
	r.printRestore(src, sn, t, rs)
	p := rs.Plan
	if len(p.Ops) == 0 {
		fmt.Fprintln(r.out, "Nothing to write: the mouse already holds every record the source restores.")
		return nil
	}
	g := r.g.gates()
	if err := restoreGates(p, g); err != nil {
		return err
	}
	if !g.DryRun {
		typed, err := r.confirmRestore(p, *yes)
		if err != nil {
			return err
		}
		g.Confirm = typed
	}
	out, err := r.write(func(ctx context.Context, on func(safety.OpEvent)) (session.Outcome, error) {
		return c.s.Apply(ctx, p, g, on)
	})
	if len(out.Backups) > 0 {
		fmt.Fprintln(r.out, "Backup saved before the write:")
		for _, b := range out.Backups {
			fmt.Fprintln(r.out, "  "+b)
		}
	}
	if err != nil {
		return writeError(err, "restore", nil)
	}
	if g.DryRun {
		fmt.Fprintf(r.out, "Dry run: nothing was written. The restore would send %s:\n", count(len(out.Packets), "packet", "packets"))
		for _, pk := range out.Packets {
			fmt.Fprintln(r.out, "  "+pk.String())
		}
		t.Image = out.Image
		return r.restoreCheck(src, t, o, "after it the mouse would hold")
	}
	fmt.Fprintf(r.out, "Restore run %s: %s verified.\n", out.Run, count(out.Verified, "write", "writes"))
	if out.Overlap {
		fmt.Fprintln(r.out, fill("The mouse reported a change to the last record while it was written; arcctl read it again.", "", 80))
	}
	sn, err = r.await(c, c.s.Snapshot().Seq, loaded)
	if err != nil {
		return err
	}
	if t.Image, err = r.restoreReads(c, src.Image, sn.Image); err != nil {
		return err
	}
	return r.restoreCheck(src, t, o, "the mouse now holds")
}

// restoreSession runs a session on w with writes enabled. The emulator's
// journal and backups go to a new temporary folder, never to the real
// device's (T120).
func (r *runner) restoreSession(w *world) (*conn, *session.Snapshot, error) {
	data, journal, backups, _, done, err := r.writeFolders(w)
	if err != nil {
		w.close()
		return nil, nil, err
	}
	if w.source == backup.SourceEmulator {
		fmt.Fprintf(r.errw, "arcctl: the emulated mouse's journal and backups go to %s, which is removed on exit\n", data)
	}
	r.writes = &session.Writes{
		Journal: journal,
		Backups: backup.Store{Root: backups, Tool: "arcctl " + r.env.Version, Source: w.source, OS: r.keyOS(), Now: r.env.Now},
		// start takes the lock for a real device before the session runs.
		Lock:     func() error { return nil },
		Executor: r.env.Executor,
	}
	c, sn, err := r.attach(w, loaded)
	if c != nil {
		c.stop = chain(c.stop, w.close, done)
	} else {
		w.close()
		done()
	}
	return c, sn, err
}

func restoreTarget(sn *session.Snapshot, source string) backup.Target {
	t := backup.Target{
		Model: sn.Model,
		Image: sn.Image,
		Options: mouse.Options{Device: sn.Identity, Profile: profileOf(sn), Firmware: sn.Versions.Mouse,
			Verified: catalog.VerifiedStages()},
		Source: source,
	}
	if h := sn.Handshake; h != nil {
		conn := h.Conn
		t.Conn = &conn
	}
	return t
}

func sourceError(err error) error {
	msg := strings.TrimPrefix(err.Error(), "backup: ")
	switch {
	case errors.Is(err, backup.ErrOtherDevice):
		return fail(ExitFailure, msg, "Pass --other-device to restore it to this mouse anyway.")
	case errors.Is(err, backup.ErrOtherProfile):
		return fail(ExitFailure, msg, "Switch the mouse to the profile the source was read on, or pass --other-profile.")
	}
	return fail(ExitFailure, msg, "")
}

// restoreReads reads what the plan compares that the mouse's image lacks,
// round by round, and returns the image with it.
func (r *runner) restoreReads(c *conn, src, dev *flash.Image) (*flash.Image, error) {
	for range restoreReadRounds {
		need := backup.RestoreReads(src, dev)
		if len(need) == 0 {
			break
		}
		var cp session.Capture
		err := r.do(c, func(ctx context.Context) error {
			var err error
			cp, err = c.s.Read(ctx, need...)
			return err
		})
		if err != nil {
			return nil, readError(err)
		}
		if cp.Image == nil {
			break
		}
		dev = cp.Image
		if len(cp.Missing) > 0 {
			break
		}
	}
	return dev, nil
}

// restoreCheck plans the restore again on what the mouse holds now, which
// must write nothing.
func (r *runner) restoreCheck(src *backup.Source, t backup.Target, o backup.RestoreOptions, what string) error {
	rs, err := backup.PlanRestore(src, t, o)
	if err != nil {
		return fail(ExitFailure, "the restore could not be checked: "+plainText(err.Error()), "")
	}
	if n := len(rs.Plan.Ops); n > 0 {
		return fail(ExitVerify, fmt.Sprintf("%s still differs from the source", count(n, "record", "records")),
			"Run 'arcctl restore' again to see which; 'arcctl journal status' shows the run.")
	}
	fmt.Fprintln(r.out, fill("Checked: "+what+" what the source holds in every record it restores.", "", 80))
	return nil
}

// restoreGates refuses early a plan the flags do not allow to be written, as
// the preflight would, before asking for a confirmation.
func restoreGates(p plan.Plan, g safety.Gates) error {
	if g.DryRun {
		return nil
	}
	var untested, experimental int
	for _, op := range p.Ops {
		switch op.Tier {
		case catalog.Untested:
			untested++
		case catalog.Experimental:
			experimental++
		}
	}
	switch {
	case untested > 0 && !g.AllowUntested:
		return fail(ExitFailure, fmt.Sprintf("%s untested on this firmware", count(untested, "record is", "records are")),
			"Pass --allow-untested to write them, or --dry-run to see the packets first.")
	case experimental > 0 && !g.Experimental:
		return fail(ExitFailure, fmt.Sprintf("%s experimental", count(experimental, "record is", "records are")),
			"Pass --experimental to write them, or --dry-run to see the packets first.")
	}
	return nil
}

// confirmRestore asks before writing: the phrase the tiers call for, typed
// out and read with echo on, or y unless --yes.
func (r *runner) confirmRestore(p plan.Plan, yes bool) (string, error) {
	phrase := safety.ConfirmPhrase(p.Ops)
	if phrase == "" && yes {
		return "", nil
	}
	in := bufio.NewReader(orEmpty(r.env.Stdin))
	what := count(len(p.Ops), "write", "writes")
	if phrase != "" {
		fmt.Fprintf(r.errw, "Type %q and press enter to make %s to the mouse: ", phrase, what)
	} else {
		fmt.Fprintf(r.errw, "Make %s to the mouse? [y/N] ", what)
	}
	line, err := in.ReadString('\n')
	fmt.Fprintln(r.errw)
	answer := strings.TrimSpace(line)
	switch {
	case err != nil && answer == "":
		return "", fail(ExitAborted, "no confirmation: nothing was written", "Run it from a terminal, or pipe the answer to standard input.")
	case phrase != "" && answer != phrase:
		return "", fail(ExitAborted, fmt.Sprintf("the confirmation was not %q: nothing was written", phrase), "")
	case phrase == "" && !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes"):
		return "", fail(ExitAborted, "not confirmed: nothing was written", "")
	}
	return answer, nil
}

func orEmpty(rd io.Reader) io.Reader {
	if rd == nil {
		return strings.NewReader("")
	}
	return rd
}

func (r *runner) printRestore(src *backup.Source, sn *session.Snapshot, t backup.Target, rs *backup.Restore) {
	line := func(label, text string) {
		fmt.Fprintln(r.out, wrap(fmt.Sprintf("%-9s ", label), strings.Repeat(" ", 10), strings.Fields(text), " ", 80))
	}
	line("Source", src.Path)
	switch f := src.File; src.Kind {
	case backup.KindBackup:
		kind := "loaded bytes"
		if f.Full {
			kind = "full"
		}
		line("", fmt.Sprintf("backup of %s, %s UTC, %s", f.Key(), f.Created.UTC().Format(time.DateTime), kind))
	case backup.KindBin:
		line("", fmt.Sprintf("web .bin, sensor %s, the regions the web app reads", src.Bin.Sensor))
	}
	line("Mouse", fmt.Sprintf("%s (%s), identity %s, %s", sn.Model.Name, sn.Model.Key, sn.Identity.Key(), profileWords(profileOf(sn))))
	for _, n := range rs.Notes {
		line("Note", n)
	}
	for _, cl := range rs.Clamps {
		line("Clamped", fmt.Sprintf("%s % x to % x: the .bin's value is past what this mouse takes", cl.Name, cl.From, cl.To))
	}
	fmt.Fprintln(r.out)
	if len(rs.Records) == 0 {
		fmt.Fprintf(r.out, "No record differs; %s equal.\n", count(rs.Equal, "record is", "records are"))
		return
	}
	fmt.Fprintln(r.out, "Records that differ (the mouse now -> the source):")
	width := 0
	for _, x := range rs.Records {
		width = max(width, len(x.Name))
	}
	m, os := sn.Model, r.keyOS()
	srcImage, _ := backup.RestoreImage(src, t)
	for _, x := range rs.Records {
		words := restoreWords(m, t.Image, x.Extent, x.Device, os) + " -> " + restoreWords(m, srcImage, x.Extent, x.Source, os)
		first := fmt.Sprintf("  %-*s  ", width, x.Name)
		pad := strings.Repeat(" ", len(first))
		fmt.Fprintln(r.out, wrap(first, pad, strings.Fields(words), " ", 80))
		fmt.Fprintln(r.out, wrap(pad, pad, strings.Fields(fateText(x)), " ", 80))
	}
	fmt.Fprintln(r.out)
	packets := 0
	for _, op := range rs.Plan.Ops {
		packets += (op.Extent.Len + 9) / 10
	}
	left := len(rs.Records) - rs.Count(backup.FateWrite)
	fmt.Fprintf(r.out, "%s to write in %s; %s left alone; %s equal.\n",
		count(rs.Count(backup.FateWrite), "record", "records"), count(packets, "packet", "packets"),
		count(left, "record", "records"), count(rs.Equal, "record", "records"))
	if len(rs.Plan.Ops) > 0 {
		fmt.Fprintln(r.out, "\nPlan, in the order it is written:")
		fmt.Fprintf(r.out, "  %3s  %-9s  %-10s  %-12s  %s\n", "Op", "Record", "Phase", "Tier", "What")
		for _, op := range rs.Plan.Ops {
			first := fmt.Sprintf("  %3d  %-9s  %-10s  %-12s  ", op.Seq, op.Extent, op.Phase, op.Tier)
			fmt.Fprintln(r.out, wrap(first, strings.Repeat(" ", len(first)), strings.Fields(op.Desc), " ", 80))
		}
	}
	fmt.Fprintln(r.out)
}

// restoreWords says in words what a record holds; b is nil when it is not
// known.
func restoreWords(m *catalog.Model, im *flash.Image, e flash.Extent, b []byte, os keys.OS) string {
	if b == nil || im == nil {
		return "(not known)"
	}
	return backup.Words(m, im, e, os)
}

func fateText(x backup.RestoreRecord) string {
	switch {
	case x.Captured:
		return "write, " + x.Tier.String() + ", the source's bytes as captured (--include-unknown): " + x.Why
	case x.Fate == backup.FateWrite:
		return "write, " + x.Tier.String()
	case x.Eligible:
		return x.Fate.String() + ": " + x.Why + " (--include-unknown writes it back)"
	}
	return x.Fate.String() + ": " + x.Why
}

func profileWords(p *byte) string {
	if p == nil {
		return "no onboard profiles"
	}
	return fmt.Sprintf("onboard profile %d", *p)
}
