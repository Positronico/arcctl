//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

func stageID(name string) string { return strings.ToLower(name) }

// writeStage runs a stage that writes: a fresh full backup, the steps laid
// out on it, a dry-run preview of every write, the user's confirmation, the
// steps, and a reload that must find every touched extent as it was.
func (r *runner) writeStage(ctx context.Context) error {
	c, sn, err := r.connect(ctx, connectOpts{rec: r.rec.rec, writes: true})
	if err != nil {
		return err
	}
	defer c.close()
	r.describe(sn)
	if err := r.writable(sn); err != nil {
		return err
	}
	img, err := r.freshBackup(ctx, c)
	if err != nil {
		return err
	}
	b := r.builder(c.s.Snapshot(), img)
	if err := r.def.build(b); err != nil {
		return err
	}
	if err := r.checkFlags(b.steps); err != nil {
		return err
	}
	if err := r.preview(ctx, c, b.steps); err != nil {
		return err
	}
	if err := r.confirm(b.steps); err != nil {
		return err
	}
	return r.execute(ctx, c, b.steps, img)
}

// dryRun shows a write stage's preview and stops: no backup, no question,
// no write and no record. The steps are laid out on the image the session
// loaded, and each write's dry run goes only to the session's overlay.
func (r *runner) dryRun(ctx context.Context) (*Result, error) {
	if r.def.build == nil {
		return nil, fmt.Errorf("%w: %s writes nothing", ErrNoDryRun, r.def.name)
	}
	r.say(fmt.Sprintf("Stage %s: %s (dry run)", r.def.name, r.def.title))
	r.res.Err = r.previewStage(ctx)
	r.res.Ended = r.cfg.Now()
	if r.res.Err != nil {
		r.say("Stopped: " + r.res.Err.Error())
	}
	return r.res, nil
}

func (r *runner) previewStage(ctx context.Context) error {
	c, sn, err := r.connect(ctx, connectOpts{writes: true})
	if err != nil {
		return err
	}
	defer c.close()
	r.describe(sn)
	if err := r.writable(sn); err != nil {
		return err
	}
	b := r.builder(sn, sn.Image)
	if err := r.def.build(b); err != nil {
		return err
	}
	if err := r.preview(ctx, c, b.steps); err != nil {
		return err
	}
	if need := r.missingFlags(b.steps); len(need) > 0 {
		r.say(fmt.Sprintf("\nThe stage itself needs %s.", strings.Join(need, " and ")))
	}
	r.say("\nDry run: no backup was taken and nothing was written.")
	return nil
}

func (r *runner) writable(sn *session.Snapshot) error {
	switch {
	case sn.Model == nil || sn.Model.Key != em11:
		name := "an unknown device"
		if sn.Model != nil {
			name = sn.Model.Name
		}
		return fmt.Errorf("%w; this is %s", ErrModel, name)
	case sn.Journal == nil:
		return fmt.Errorf("%w: the session did not read the journal", ErrJournal)
	case sn.Journal.Err != nil:
		return fmt.Errorf("%w: %w", ErrJournal, sn.Journal.Err)
	case len(sn.Journal.Open) > 0:
		return fmt.Errorf("%w; settle them with 'arcctl journal recover' first", ErrJournal)
	}
	return nil
}

// freshBackup reads everything a full backup covers, saves it, reads the
// file back and checks it. A write stage needs it complete.
func (r *runner) freshBackup(ctx context.Context, c *conn) (*flash.Image, error) {
	var cp session.Capture
	err := r.watch(ctx, c, func(ctx context.Context) error {
		var err error
		cp, err = c.s.Backup(ctx, true)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("the fresh backup: %w", err)
	}
	if len(cp.Missing) > 0 {
		return nil, fmt.Errorf("%w: it could not read %v", ErrBackup, cp.Missing)
	}
	path, f, err := r.saveBackup(cp, "hwtest "+r.def.name+" before")
	if err != nil {
		return nil, err
	}
	r.step("fresh full backup", true, fmt.Sprintf("%d bytes, complete, read back from its file", f.Known()))
	r.say("    saved to " + path)
	return cp.Image.Clone(), nil
}

func (r *runner) saveBackup(cp session.Capture, label string) (string, *backup.File, error) {
	meta := backup.Meta{Tool: r.cfg.Tool, Source: r.cfg.Source, Label: label, Created: r.cfg.Now(), OS: r.cfg.OS}
	if h := cp.Handshake; h != nil {
		conn := h.Conn
		meta.Conn = &conn
	}
	f, err := backup.New(cp, meta)
	if err != nil {
		return "", nil, err
	}
	path, err := backup.Save(r.cfg.Backups, f)
	if err != nil {
		return "", nil, err
	}
	back, err := backup.Load(path)
	if err != nil {
		return "", nil, fmt.Errorf("the backup at %s does not read back: %w", path, err)
	}
	if !bytes.Equal(back.Image().Bytes(), cp.Image.Bytes()) || !slices.Equal(back.Image().KnownExtents(), cp.Image.KnownExtents()) {
		return "", nil, fmt.Errorf("the backup at %s differs from what was read", path)
	}
	r.res.Backups = append(r.res.Backups, path)
	return path, f, nil
}

// watch runs call while it follows the session: it tells the user when the
// mouse sleeps, and gives up when the session loses the device.
func (r *runner) watch(ctx context.Context, c *conn, call func(ctx context.Context) error) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- call(cctx) }()
	told := false
	for {
		select {
		case err := <-res:
			return err
		case <-ctx.Done():
			<-res
			return ctx.Err()
		case <-c.s.Changed():
		}
		sn := c.s.Snapshot()
		switch sn.State {
		case session.NoReceiver, session.NeedsPermission, session.Locked, session.Seized, session.Stalled, session.Conflict:
			cancel()
			<-res
			return fmt.Errorf("%w: it is %s: %v", ErrUnhealthy, sn.State, sn.Err)
		}
		switch {
		case sn.Progress.Paused && !told:
			r.say("The mouse fell asleep. Move it to go on.")
			r.note("the mouse fell asleep during " + sn.Progress.Job)
			r.pauses++
			told = true
		case !sn.Progress.Paused:
			told = false
		}
	}
}

func (r *runner) builder(sn *session.Snapshot, img *flash.Image) *builder {
	return &builder{
		stage:   r.def.name,
		m:       sn.Model,
		dev:     sn.Identity,
		profile: profileOf(sn),
		opt:     mouse.Options{Device: sn.Identity, Profile: profileOf(sn), Firmware: sn.Versions.Mouse, Verified: catalog.VerifiedStages()},
		os:      r.cfg.OS,
		img:     img.Clone(),
		layout:  mouse.Layout(sn.Model),
	}
}

func profileOf(sn *session.Snapshot) *byte {
	if !sn.Profile.Supported {
		return nil
	}
	v := sn.Profile.Value
	return &v
}

func stageOps(steps []step) []plan.Op {
	var ops []plan.Op
	for _, s := range steps {
		ops = append(ops, s.ops()...)
	}
	return ops
}

// checkFlags refuses a stage whose writes the user's flags do not allow,
// before anything is written.
func (r *runner) checkFlags(steps []step) error {
	if need := r.missingFlags(steps); len(need) > 0 {
		return fmt.Errorf("%w: pass %s", ErrFlags, strings.Join(need, " and "))
	}
	return nil
}

func (r *runner) missingFlags(steps []step) []string {
	var need []string
	ops := stageOps(steps)
	if slices.ContainsFunc(ops, func(o plan.Op) bool { return o.Tier == catalog.Untested }) && !r.cfg.Gates.AllowUntested {
		need = append(need, "--allow-untested")
	}
	if slices.ContainsFunc(ops, func(o plan.Op) bool { return o.Tier == catalog.Experimental }) && !r.cfg.Gates.Experimental {
		need = append(need, "--experimental")
	}
	return need
}

// packets are the cmd-7 packets the executor sends for p.
func packets(p plan.Plan) []wire.Packet {
	var out []wire.Packet
	for _, op := range p.Ops {
		for off := 0; off < op.Extent.Len; off += wire.MaxData {
			out = append(out, wire.MustBuild(wire.Mouse, wire.CmdWrite, uint16(op.Extent.Addr+off), op.New[off:min(off+wire.MaxData, op.Extent.Len)]))
		}
	}
	return out
}

func identityPackets(s step) []wire.Packet {
	return packets(plan.Plan{Ops: []plan.Op{{Extent: s.extent, New: s.bytes}}})
}

// preview shows every step with its exact packets. Each write is also run
// as a dry run, which must produce exactly the packets shown.
func (r *runner) preview(ctx context.Context, c *conn, steps []step) error {
	r.say("\nWhat the stage does:")
	n := 0
	for i, s := range steps {
		r.say(fmt.Sprintf("%2d. %s", i+1, s.describe()))
		switch s.kind {
		case stepIdentity:
			for _, p := range identityPackets(s) {
				r.say("      raw  " + p.String())
				n++
			}
		case stepProbe:
			p, _ := probePacket(s.extent, s.bytes)
			r.say(fmt.Sprintf("      raw  %s (byte 15 should be %02x)", p, p.Checksum()))
			n++
		case stepApply, stepRevert:
			for _, op := range s.plan.Ops {
				r.say(fmt.Sprintf("      op %d %s %s: % x -> % x (%s) %s", op.Seq, op.Phase, op.Extent, op.Old, op.New, op.Tier, op.Desc))
			}
			out, err := c.s.Apply(ctx, s.plan, safety.Gates{DryRun: true}, nil)
			if err != nil {
				return fmt.Errorf("the dry run of step %d: %w", i+1, err)
			}
			want := packets(s.plan)
			if !slices.Equal(out.Packets, want) {
				return fmt.Errorf("the dry run of step %d sent %v, the plan has %v", i+1, out.Packets, want)
			}
			for _, p := range out.Packets {
				r.say("      cmd7 " + p.String())
				n++
			}
		}
	}
	if phrase := safety.ConfirmPhrase(stageOps(steps)); phrase != "" {
		r.say(fmt.Sprintf("\nThe stage writes features no hardware test has verified yet; you will type %q to go on.", phrase))
	}
	r.step("dry-run preview", true, fmt.Sprintf("%d steps, %d packets, every write's dry run matched its plan", len(steps), n))
	return nil
}

func (s step) describe() string {
	switch s.kind {
	case stepIdentity, stepProbe:
		return fmt.Sprintf("%s (raw path; % x stays % x)", s.title, s.bytes, s.bytes)
	case stepApply:
		return s.title
	case stepRevert:
		return fmt.Sprintf("%s (revert of step %d)", s.title, s.of+1)
	case stepCheck:
		return fmt.Sprintf("check: %s (%s holds % x)", s.title, s.extent, s.bytes)
	}
	return "question: " + s.text
}

func (r *runner) confirm(steps []step) error {
	writes := 0
	for _, s := range steps {
		switch s.kind {
		case stepIdentity, stepProbe:
			writes++
		case stepApply, stepRevert:
			writes += len(s.plan.Ops)
		}
	}
	ok, err := r.ask(stageID(r.def.name)+".run", fmt.Sprintf("\nRun stage %s now? It writes %d records to the mouse.", r.def.name, writes), true)
	switch {
	case err != nil:
		return err
	case !ok:
		return ErrDeclined
	}
	phrase := safety.ConfirmPhrase(stageOps(steps))
	if phrase == "" {
		r.begun = true
		return nil
	}
	typed, err := r.line(stageID(r.def.name)+".confirm", fmt.Sprintf("Type %q to write them:", phrase))
	if err != nil {
		return err
	}
	if typed != phrase {
		return fmt.Errorf("%w: typed %q, want %q", ErrConfirm, typed, phrase)
	}
	r.begun = true
	return nil
}

// execute runs the steps. A question answered the unexpected way fails the
// stage but the steps go on, so every change is still reverted; an error
// stops them, and the reverts still owed then run while the session is
// healthy.
func (r *runner) execute(ctx context.Context, c *conn, steps []step, img *flash.Image) error {
	runs := make([]string, len(steps))
	done := make([]bool, len(steps))
	var err error
	for i, s := range steps {
		r.note(fmt.Sprintf("step %d: %s", i+1, s.describe()))
		switch s.kind {
		case stepIdentity:
			err = r.doIdentity(ctx, c, s)
		case stepProbe:
			err = r.doProbe(ctx, c, s)
		case stepApply:
			runs[i], err = r.doApply(ctx, c, s)
		case stepRevert:
			_, err = r.doRevert(ctx, c, s, runs[s.of], steps[s.of].plan.Ops)
		case stepAsk:
			_, err = r.ask(s.id, s.text, s.want)
		case stepName:
			_, err = r.line(s.id, s.text)
		case stepCheck:
			err = r.doCheck(ctx, c, s)
		}
		if err != nil {
			err = fmt.Errorf("step %d (%s): %w", i+1, s.title, err)
			r.restore(ctx, c, steps, runs, done, i)
			return err
		}
		done[i] = true
	}
	if rp, err := r.rawPath(c); err == nil {
		if n := rp.t.lateReplies(); n > 0 {
			r.finding("%d replies to the raw path came after it stopped listening; the session never saw them", n)
		}
	}
	return r.verify(ctx, c, steps, img)
}

// restore runs the reverts owed after step failed: those of applies that
// finished and whose own revert did not run, the failed step's included. An
// interrupt does not skip them: they run on a context of their own, bounded
// by Config.Wait, and a second interrupt ends the process as a crash would.
// What cannot be undone is named.
func (r *runner) restore(ctx context.Context, c *conn, steps []step, runs []string, done []bool, failed int) {
	if ctx.Err() != nil {
		r.say("Interrupted: undoing the steps still applied. Press Ctrl-C again to quit at once.")
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), r.cfg.Wait)
		defer cancel()
	}
	var owed []int
	for i := len(steps) - 1; i >= failed; i-- {
		if s := steps[i]; s.kind == stepRevert && !done[i] && runs[s.of] != "" && done[s.of] {
			owed = append(owed, i)
		}
	}
	for n, i := range owed {
		s := steps[i]
		err := healthy(c.s.Snapshot())
		if err == nil {
			r.say(fmt.Sprintf("Undoing step %d after the failure.", s.of+1))
			_, err = r.doRevert(ctx, c, s, runs[s.of], steps[s.of].plan.Ops)
		}
		if err != nil {
			r.stillApplied(steps, owed[n:], err)
			return
		}
		done[i] = true
	}
}

// stillApplied names the steps whose changes stay on the mouse.
func (r *runner) stillApplied(steps []step, owed []int, err error) {
	var left []string
	for _, i := range owed {
		of := steps[i].of
		left = append(left, fmt.Sprintf("step %d (%s)", of+1, steps[of].title))
		r.note(fmt.Sprintf("still applied: step %d", of+1))
	}
	r.say(fmt.Sprintf("Cannot undo %s: %v.", strings.Join(left, ", "), err))
	r.say("The mouse keeps those changes. The backup from the start of the stage holds the bytes from before it; " +
		"settle the journal with 'arcctl journal recover' if it names a run, then run the stage again once the device is back to them.")
}

func (r *runner) gates(ops []plan.Op) safety.Gates {
	g := r.cfg.Gates
	g.DryRun = false
	g.Confirm = safety.ConfirmPhrase(ops)
	return g
}

// writeChecks runs the session's preflight for a raw write of s, as for any
// write: the same device and profile, the bytes s writes still on it, no
// other client on the receiver unless allowed, the lock, the console, a
// clean journal and the tier gates (I10, §6.3).
func (r *runner) writeChecks(ctx context.Context, c *conn, s step) error {
	sn := c.s.Snapshot()
	p := plan.Plan{Device: sn.Identity, Profile: profileOf(sn), Ops: s.ops()}
	return c.s.Preflight(ctx, p, r.gates(p.Ops))
}

func (r *runner) doIdentity(ctx context.Context, c *conn, s step) error {
	if err := r.writeChecks(ctx, c, s); err != nil {
		r.step(s.title, false, err.Error())
		return err
	}
	rp, err := r.rawPath(c)
	if err != nil {
		return err
	}
	res, err := rp.identity(ctx, s.extent, s.bytes)
	var detail []string
	for _, w := range res.writes {
		detail = append(detail, "sent "+w.packet.String())
		detail = append(detail, replies(w)...)
		r.echo(w)
	}
	if err != nil {
		r.step(s.title, false, append(detail, err.Error())...)
		return err
	}
	r.step(s.title, true, append(detail, fmt.Sprintf("read-back % x, equal", res.readBack))...)
	return nil
}

// replies describes what came back for one raw write.
func replies(w written) []string {
	if len(w.replies) == 0 {
		return []string{"no cmd-7 reply"}
	}
	var out []string
	for i, rep := range w.replies {
		kind := "reply"
		if i > 0 {
			kind = "another reply"
		}
		out = append(out, fmt.Sprintf("%s after %s: %s (%s)", kind, rep.at.Sub(w.at).Round(time.Millisecond), rep.p, echoKind(w.packet, rep.p)))
	}
	if n := len(w.seen) - len(w.replies); n > 0 {
		out = append(out, fmt.Sprintf("%d other report-8 frames arrived meanwhile", n))
	}
	return out
}

// echoKind classes a cmd-7 reply against the request it answers.
func echoKind(req, rep wire.Packet) string {
	switch {
	case rep == req:
		return "the request itself"
	case rep.Status() == wire.StatusNAK:
		return "status 1, a NAK"
	case rep.Status() != wire.StatusOK:
		return fmt.Sprintf("status %d", rep.Status())
	case rep.Addr() != req.Addr() || rep.Len() != req.Len():
		return "a different address or length"
	case bytes.Equal(rep.Data(), req.Data()):
		return "the request's header and data, checksum " + map[bool]string{true: "valid", false: "invalid"}[rep.Valid()]
	case bytes.Count(rep.Data(), []byte{0}) == len(rep.Data()):
		return "the request's header, no data"
	}
	return "the request's header, other data"
}

// echo records what the identity writes show about cmd 7's replies.
func (r *runner) echo(w written) {
	switch len(w.replies) {
	case 0:
		r.finding("cmd 7 at %d: no reply", w.packet.Addr())
	case 1:
		r.finding("cmd 7 at %d: one reply, %s; no second reply within %s", w.packet.Addr(), echoKind(w.packet, w.replies[0].p), r.cfg.Listen)
	default:
		r.finding("cmd 7 at %d: %d replies (%s, then %s)", w.packet.Addr(), len(w.replies), echoKind(w.packet, w.replies[0].p), echoKind(w.packet, w.replies[1].p))
	}
}

func (r *runner) doProbe(ctx context.Context, c *conn, s step) error {
	if err := r.writeChecks(ctx, c, s); err != nil {
		r.step(s.title, false, err.Error())
		return err
	}
	rp, err := r.rawPath(c)
	if err != nil {
		return err
	}
	res, err := rp.probe(ctx, s.extent, s.bytes)
	detail := append([]string{"sent " + res.write.packet.String()}, replies(res.write)...)
	if err != nil {
		r.step(s.title, false, append(detail, err.Error())...)
		return err
	}
	outcome := "silence: no reply"
	if len(res.write.replies) > 0 {
		rep := res.write.replies[0].p
		switch rep.Status() {
		case wire.StatusNAK:
			outcome = "a NAK (status 1): " + rep.String()
		case wire.StatusOK:
			outcome = "accepted (status 0): " + rep.String()
		default:
			outcome = fmt.Sprintf("status %d: %s", rep.Status(), rep)
		}
		if len(res.write.replies) > 1 {
			outcome += fmt.Sprintf(", and %d more", len(res.write.replies)-1)
		}
	}
	r.finding("a cmd 7 with a wrong packet checksum gets %s; the flash is unchanged", outcome)
	r.step(s.title, true, append(detail, fmt.Sprintf("read-back % x, unchanged", res.readBack))...)
	return nil
}

func (r *runner) doApply(ctx context.Context, c *conn, s step) (string, error) {
	out, err := c.s.Apply(ctx, s.plan, r.gates(s.plan.Ops), r.event)
	if err != nil {
		r.step(s.title, false, fmt.Sprintf("run %q: %d of %d records verified", out.Run, out.Verified, out.Ops), err.Error())
		return out.Run, err
	}
	if err := r.settled(ctx, c, out.Run); err != nil {
		r.step(s.title, false, err.Error())
		return out.Run, err
	}
	r.step(s.title, true, r.outcome(out)...)
	return out.Run, nil
}

func (r *runner) outcome(out session.Outcome) []string {
	d := []string{fmt.Sprintf("%d of %d records written and verified by read-back", out.Verified, out.Ops)}
	if out.Echoes > 0 {
		d = append(d, fmt.Sprintf("%d cmd-7 replies differed from their request", out.Echoes))
		r.finding("the executor saw %d cmd-7 replies whose data differ from the request", out.Echoes)
	}
	return d
}

// settled waits for the reload after a write and checks that the journal
// took the run as the last one.
func (r *runner) settled(ctx context.Context, c *conn, run string) error {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return err
	}
	if err := healthy(sn); err != nil {
		return err
	}
	if sn.Journal == nil || sn.Journal.Last == nil || sn.Journal.Last.ID != run {
		return fmt.Errorf("%w: the journal's last run is not %q", ErrJournal, run)
	}
	return nil
}

// doRevert reverts the run of step of through the session, after a dry run
// of the revert showed exactly the packets of the preview.
func (r *runner) doRevert(ctx context.Context, c *conn, s step, run string, ops []plan.Op) (string, error) {
	sn := c.s.Snapshot()
	if sn.Journal == nil || sn.Journal.Last == nil || sn.Journal.Last.ID != run {
		err := fmt.Errorf("%w: the last run is not step %d's", ErrJournal, s.of+1)
		r.step(s.title, false, err.Error())
		return "", err
	}
	dry, err := c.s.Revert(ctx, safety.Gates{DryRun: true}, nil)
	if want := packets(s.plan); err == nil && !slices.Equal(dry.Packets, want) {
		err = fmt.Errorf("the revert would send %v, the preview showed %v", dry.Packets, want)
	}
	if err != nil {
		r.step(s.title, false, err.Error())
		return "", err
	}
	out, err := c.s.Revert(ctx, r.gates(ops), r.event)
	if err == nil {
		err = r.settled(ctx, c, out.Run)
	}
	if err != nil {
		r.step(s.title, false, err.Error())
		return out.Run, err
	}
	r.step(s.title, true, r.outcome(out)...)
	return out.Run, nil
}

func (r *runner) doCheck(ctx context.Context, c *conn, s step) error {
	rp, err := r.rawPath(c)
	if err != nil {
		return err
	}
	got, _, err := rp.read(ctx, s.extent)
	if err != nil {
		r.step(s.title, false, err.Error())
		return err
	}
	ok := bytes.Equal(got, s.bytes)
	r.step(s.title, ok, fmt.Sprintf("%s holds % x, want % x", s.extent, got, s.bytes))
	return nil
}

// event follows a write: progress for the user, and the debug abort that
// ends the process after a chunk, as a crash would.
func (r *runner) event(e safety.OpEvent) {
	switch e.Kind {
	case safety.EventChunk:
		if n := r.cfg.AbortAfterChunk; n > 0 && e.Chunk == n {
			msg := fmt.Sprintf("debug abort after chunk %d of %d of op %d (%s), as --debug-abort-after-chunk asked", n, e.Chunks, e.Op.Seq, e.Op.Extent)
			r.note(msg)
			r.say(msg + "; the journal settles the write on the next start")
			r.cfg.Exit(abortCode)
		}
	case safety.EventVerified:
		r.say(fmt.Sprintf("    op %d verified: %s", e.Op.Seq, e.Op.Desc))
	case safety.EventFailed:
		r.say(fmt.Sprintf("    op %d failed (%s): %v", e.Op.Seq, e.Class, e.Err))
	case safety.EventPaused:
		r.say(fmt.Sprintf("    paused: %v; wake the mouse or unlock the screen", e.Err))
	case safety.EventResumed:
		r.say("    resumed")
	}
}

// verify reloads and checks that every extent the stage touched holds what
// the fresh backup holds. Other changes to the settings page, such as the
// current stage after a press of the DPI button, are reported.
func (r *runner) verify(ctx context.Context, c *conn, steps []step, img *flash.Image) error {
	seq := c.s.Snapshot().Seq
	if err := r.watch(ctx, c, c.s.Reload); err != nil {
		return err
	}
	sn, err := r.readyAfter(ctx, c, seq)
	if err != nil {
		return err
	}
	var touched []flash.Extent
	for _, s := range steps {
		switch s.kind {
		case stepIdentity, stepProbe, stepCheck:
			touched = append(touched, s.extent)
		case stepApply, stepRevert:
			for _, op := range s.plan.Ops {
				touched = append(touched, op.Extent)
			}
		}
	}
	var bad []string
	for _, e := range touched {
		got, ok := sn.Image.Get(e)
		want, _ := img.Get(e)
		if !ok {
			cp, err := c.s.Read(ctx, e)
			if err != nil {
				return err
			}
			got, _ = cp.Image.Get(e)
		}
		if !bytes.Equal(got, want) {
			bad = append(bad, fmt.Sprintf("%s holds % x, the backup % x", e, got, want))
		}
	}
	settings := flash.Extent{Addr: 0, Len: 256}
	for _, e := range diffRuns(img, sn.Image, settings) {
		if slices.ContainsFunc(touched, e.Overlaps) {
			continue
		}
		was, _ := img.Get(e)
		now, _ := sn.Image.Get(e)
		r.finding("%s changed during the stage without a write of it: % x -> % x", e, was, now)
	}
	if err := healthy(sn); err != nil {
		bad = append(bad, err.Error())
	}
	if len(bad) > 0 {
		r.step("the device holds its starting bytes", false, bad...)
		return errors.New("hwtest: the device does not hold its starting bytes")
	}
	r.step("the device holds its starting bytes", true, fmt.Sprintf("%d extents read again after a reload, journal clean", len(touched)))
	return nil
}

// diffRuns lists the runs of bytes in e that both images know and that
// differ.
func diffRuns(a, b *flash.Image, e flash.Extent) []flash.Extent {
	var out []flash.Extent
	start := -1
	for i := e.Addr; i <= e.End(); i++ {
		diff := false
		if i < e.End() {
			x, okA := a.Byte(i)
			y, okB := b.Byte(i)
			diff = okA && okB && x != y
		}
		switch {
		case diff && start < 0:
			start = i
		case !diff && start >= 0:
			out = append(out, flash.Extent{Addr: start, Len: i - start})
			start = -1
		}
	}
	return out
}
