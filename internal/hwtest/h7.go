//go:build hwtest

package hwtest

import (
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

// resetPhrase is typed before H7 runs, as before the release build's reset.
const resetPhrase = safety.ResetPhrase

// resetLate is how long after the reset a cmd-9 frame counts as its late
// reply, kept from every session: the mouse may answer once it restarted.
const resetLate = 30 * time.Second

// h7Prepare is the preparation §11 asks for before the reset (D4, D6, D9).
var h7Prepare = []question{
	{"h7.prep-repair", "The reset may unpair the mouse. Have you written down the re-pair path (docs/safety.md: hold left, right and " +
		"middle for about 3 s until the light flashes, then the offline web app's \"Pairing a New Receiver\") and rehearsed it once?"},
	{"h7.prep-pointer", "Is another pointing device at hand: the trackpad, or this mouse on its Bluetooth channel?"},
	{"h7.prep-webapp", "Is every ProtoArc web app tab closed?"},
	{"h7.prep-drill", "The reset may erase every setting of the mouse; the stage writes them back from the full backup it takes first. " +
		"Run the factory-reset drill (D9)?"},
}

// h7State is what H7 learns while it runs. id and profile are the fresh
// backup's, B1's; lr is the cmd-23 reply before the reset.
type h7State struct {
	src     *backup.Source
	id      plan.Identity
	profile *byte
	lr      wire.Packet
	lrOK    bool
	changed bool
	paired  bool
	// other are the overrides a write-back needs when the mouse answers
	// after the reset as another device or on another onboard profile;
	// otherOK is the user's answer to writing back with them.
	other, asked, otherOK bool
	// extra answers whether the records the release restore leaves out go
	// back too, and unknown whether the captured bytes of the records arcctl
	// knows no valid value for do (--include-unknown); declined lists those
	// the user left changed.
	extraAsked, extraOK     bool
	unknownAsked, unknownOK bool
	declined                []string
}

// H7: the factory reset, sent once through the raw path; a full read from
// a new session, whose diff with the fresh backup decides what the reset
// did; cmd 23 and the pairing; the fresh backup written back; a full read
// that must equal it in every record a plan can write.
func buildH7(b *builder) error {
	if b.src == nil {
		return errors.New("H7 needs its fresh backup as the restore source")
	}
	st := &h7State{src: b.src, id: b.dev, profile: b.profile}
	if f := b.src.File; f != nil {
		st.id, st.profile = f.Identity(), fileProfile(f)
	}
	b.h7 = st
	b.custom("factory reset: cmd 9, sent once through the raw path", &custom{
		raw: []wire.Packet{resetPacket()},
		lines: []string{
			"Sent once and never again, whatever comes back; the stage waits up to 3 s for its reply.",
			"Then, with or without a reply, a new session reads everything a full backup covers, and the",
			"difference with the fresh backup decides what the reset did. cmd 23 and cmd 3 follow.",
		},
		run: func(ctx context.Context, r *runner, c *conn) error { return r.runReset(ctx, c, st) },
	})
	b.custom("write the fresh backup back", &custom{
		lines: []string{
			"Planned after the reset from what it changed (backup.PlanRestore), plus the records of the",
			"layout the release restore leaves out, at the experimental tier; its ops and dry run print first.",
			"If the user agrees, the backup's captured bytes also go back where arcctl knows no valid value",
			"(--include-unknown), at the experimental tier.",
		},
		need: []catalog.Tier{catalog.Untested, catalog.Experimental},
		run:  func(ctx context.Context, r *runner, c *conn) error { return r.restoreB1(ctx, c, st) },
	})
	b.ask("buttons", "Press every button, turn the wheel and move the pointer. Does the mouse work as it did before the stage?", true)
	return nil
}

func fileProfile(f *backup.File) *byte {
	if p := f.Device.Profile; p != nil && p.Supported {
		v := p.Value
		return &v
	}
	return nil
}

const resetTitle = "factory reset: cmd 9 sent once"

// runReset sends the reset once and finds out what it did. A checkpoint
// synced before the packet makes the next run of the stage go on after the
// reset rather than send it again, until the fresh backup is back on the
// mouse.
func (r *runner) runReset(ctx context.Context, c *conn, st *h7State) error {
	title := resetTitle
	sn := c.s.Snapshot()
	if err := c.s.Preflight(ctx, plan.Plan{Device: sn.Identity, Profile: profileOf(sn)}, r.gates(nil)); err != nil {
		r.step(title, false, err.Error())
		return err
	}
	rp, err := r.rawPath(c)
	if err != nil {
		return err
	}
	st.lr, st.lrOK, err = rp.query(ctx, wire.CmdGetLongRange)
	if err != nil {
		return err
	}
	cp := r.newCheckpoint(checkpointReset)
	if st.lrOK {
		cp.LongRange = st.lr[:]
	}
	if err := r.saveCheckpoint(cp); err != nil {
		r.step(title, false, "not sent: the checkpoint that keeps a next run from sending it again could not be saved: "+err.Error())
		return err
	}
	w, err := rp.hwReset(ctx)
	if !w.at.IsZero() {
		r.devs.expect(isCmd(wire.CmdClear), resetLate)
	}
	if r.cfg.afterReset != nil {
		r.cfg.afterReset()
	}
	detail := append([]string{"sent " + w.packet.String()}, replies(w)...)
	if err != nil {
		if w.at.IsZero() {
			r.step(title, false, append(detail, "not sent: "+err.Error())...)
			return errors.Join(err, removeCheckpoint(r.cfg.Logs, r.def.name))
		}
		r.step(title, false, append(detail, err.Error(), "it is never sent again; the next run of the stage reads the mouse and writes the fresh backup back")...)
		return err
	}
	r.step(title, true, detail...)
	switch len(w.replies) {
	case 0:
		r.finding("cmd 9: no reply within %s", resetWait)
	case 1:
		r.finding("cmd 9: one reply after %s: %s (status %d)", w.replies[0].at.Sub(w.at).Round(time.Millisecond), w.replies[0].p, w.replies[0].p.Status())
	default:
		r.finding("cmd 9: %d replies, the first %s (status %d)", len(w.replies), w.replies[0].p, w.replies[0].p.Status())
	}

	return r.afterReset(ctx, c, st)
}

// afterReset finds out what the reset did: a full read from a new session,
// its difference with the fresh backup, cmd 23 and the pairing. When the
// reset changed nothing, the mouse holds the fresh backup and the
// checkpoint goes.
func (r *runner) afterReset(ctx context.Context, c *conn, st *h7State) error {
	after, err := r.readAfterReset(ctx, c, st)
	if err != nil {
		return err
	}
	r.sameDevice(c, st)
	st.changed = r.resetScope(c, st, after)
	r.longRange(ctx, c, st.lr, st.lrOK)
	r.pairing(c, st)
	if !st.changed {
		return removeCheckpoint(r.cfg.Logs, r.def.name)
	}
	return nil
}

// sameDevice records whether the mouse answers after the reset as the
// device and on the onboard profile of the fresh backup. When it does not,
// writing the backup back needs an override the user must allow (I11), and
// the stage does not pass.
func (r *runner) sameDevice(c *conn, st *h7State) {
	sn := c.s.Snapshot()
	o := r.restoreOptions(sn, st)
	st.other = o.OtherDevice || o.OtherProfile
	if !st.other {
		r.step("the mouse answers as the device and profile of the fresh backup", true)
		return
	}
	var what []string
	if o.OtherDevice {
		what = append(what, "as another device")
	}
	if o.OtherProfile {
		what = append(what, fmt.Sprintf("on %s (the fresh backup: %s)", profileText(profileOf(sn)), profileText(st.profile)))
	}
	r.finding("reset scope: after the reset the mouse answers %s", strings.Join(what, " and "))
	r.step("the mouse answers as the device and profile of the fresh backup", false, "it answers "+strings.Join(what, " and "))
}

func profileText(p *byte) string {
	if p == nil {
		return "no onboard profile"
	}
	return fmt.Sprintf("onboard profile %d", *p)
}

// resumeReset goes on with H7 after a run that stopped once its reset went
// out: never another reset, but the full read, the difference with the
// fresh backup the checkpoint names, the write-back and the steps after it.
// Until the analysis starts, a failure keeps the checkpoint and records
// nothing.
func (r *runner) resumeReset(ctx context.Context, cp *checkpoint) error {
	r.say(fmt.Sprintf("Stage %s stopped after its factory reset went out (the run started %s). The reset is never sent again: "+
		"this run reads the mouse, compares it with the fresh backup of that run and writes the backup back.", r.def.name, cp.Started.Format(time.RFC3339)))
	c, sn, err := r.connect(ctx, connectOpts{rec: r.rec.rec, writes: true})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	defer c.close()
	r.describe(sn)
	if d := r.res.Device; d.Model != cp.Device.Model || d.MID != cp.Device.MID || d.Mouse != cp.Device.Mouse {
		return fmt.Errorf("%w: the reset went to a %s (mid %d, %s), this is a %s (mid %d, %s)", ErrResume,
			cp.Device.Model, cp.Device.MID, cp.Device.Mouse, d.Model, d.MID, d.Mouse)
	}
	if err := r.writable(sn); err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	f, err := backup.Load(cp.Backup)
	if err != nil {
		return fmt.Errorf("%w: the fresh backup: %w", ErrResume, err)
	}
	img := f.Image()
	b := r.builder(sn, img)
	b.src = &backup.Source{Kind: backup.KindBackup, Path: cp.Backup, File: f, Image: img}
	if err := r.def.build(b); err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	if fingerprint(b.steps) != cp.Plan || cp.Step >= len(b.steps) || b.steps[cp.Step].kind != stepCustom || b.h7 == nil {
		return fmt.Errorf("%w: the stage lays out other steps than the run that sent the reset", ErrResume)
	}
	if err := r.checkFlags(b.steps[cp.Step+1:]); err != nil {
		return fmt.Errorf("%w: %w", ErrResume, err)
	}
	ok, err := r.cfg.Prompt.Ask(stageID(r.def.name)+".resume", "Read the mouse now and write the fresh backup of that run back?")
	switch {
	case err != nil:
		return fmt.Errorf("%w: %w", ErrResume, err)
	case !ok:
		return fmt.Errorf("%w: %w", ErrResume, ErrDeclined)
	}
	r.res.Started, r.res.Steps, r.res.Answers, r.res.Findings = cp.Started, cp.Steps, cp.Answers, cp.Findings
	r.res.Backups = append(slices.Clone(cp.Backups), r.res.Backups...)
	r.fresh = f
	if !cp.Recorded {
		r.rec.extra = append(r.rec.extra, &recording{name: r.rec.name, path: cp.Transcript})
	}
	r.fingerprint, r.begun, r.at = cp.Plan, true, cp.Step
	st := b.h7
	if len(cp.LongRange) == wire.Size {
		st.lr, st.lrOK = wire.Packet(cp.LongRange), true
	}
	r.step(resetTitle, true, "sent by the run that stopped; never sent again")
	r.finding("the stage resumed after a run that stopped once the reset had gone out")
	stillPaired, err := r.ask(stageID(r.def.name)+".repaired", "Did you re-pair the mouse since the reset?", false)
	if err != nil {
		return err
	}
	if !stillPaired {
		r.finding("the mouse was re-paired between the runs, so whether the reset unpaired it is not known (D6)")
	}
	if err := r.afterReset(ctx, c, st); err != nil {
		return fmt.Errorf("step %d (%s): %w", cp.Step+1, b.steps[cp.Step].title, err)
	}
	return r.execute(ctx, c, b.steps, img, cp.Step+1)
}

// readAfterReset reads everything again from a new session. When the
// mouse no longer answers, it may have lost its pairing: the user re-pairs
// it along the documented path, and the stage reads again.
func (r *runner) readAfterReset(ctx context.Context, c *conn, st *h7State) (*backup.File, error) {
	title := "a full read after the reset"
	f, err := r.fullRead(ctx, c, "hwtest H7 after the reset")
	st.paired = err == nil
	if err != nil && errors.Is(err, ErrNotReady) {
		r.finding("after the reset the mouse did not answer: %v", err)
		c.close()
		if err := r.wait("h7.repair", "The mouse does not answer after the reset, so it may have lost its pairing. Re-pair it: hold left, "+
			"right and middle for about 3 s until the light flashes, then use the offline web app's \"Pairing a New Receiver\". Close "+
			"the web app afterwards, move the mouse, and press Enter."); err != nil {
			return nil, err
		}
		f, err = r.fullRead(ctx, c, "hwtest H7 after the reset")
	}
	if err != nil {
		r.step(title, false, err.Error(), "the fresh backup holds everything from before the reset; restore it with 'arcctl restore'")
		return nil, err
	}
	r.step(title, true, fmt.Sprintf("%d bytes from a new session, saved as a backup", f.Known()))
	return f, nil
}

// resetScope compares the full read after the reset with the fresh backup
// and records what the reset changed, by record name. It reports whether
// the reset changed anything.
func (r *runner) resetScope(c *conn, st *h7State, after *backup.File) bool {
	title := "the reset took effect"
	b1, b2 := st.src.Image, after.Image()
	var runs []flash.Extent
	for _, e := range b1.KnownExtents() {
		runs = append(runs, diffRuns(b1, b2, e)...)
	}
	sn := c.s.Snapshot()
	t := r.restoreTarget(sn)
	t.Image = b2
	rs, err := backup.PlanRestore(st.src, t, r.restoreOptions(sn, st))
	if err != nil {
		r.step(title, false, err.Error())
		return len(runs) > 0
	}
	var names []string
	for _, x := range rs.Records {
		names = append(names, x.Name)
	}
	other := outside(b1, b2, rs)
	if len(runs) == 0 {
		r.finding("reset scope: nothing the full read covers changed")
		r.step(title, false, "every byte of the full read equals the fresh backup: the reset did nothing, or the mouse held its factory settings")
		return false
	}
	r.finding("reset scope: %d records changed: %s", len(names), strings.Join(names, ", "))
	if len(other) > 0 {
		r.finding("reset scope: bytes outside those records changed: %s", strings.Join(other, ", "))
	}
	if slices.ContainsFunc(rs.Records, func(x backup.RestoreRecord) bool { return x.Extent.Addr == mouse.AddrSensorMode }) {
		r.finding("reset scope: the pair at %d, flagged invalid on this unit, changed", mouse.AddrSensorMode)
	}
	detail := []string{fmt.Sprintf("%d records and %d byte runs differ from the fresh backup", len(names), len(runs)), "records: " + strings.Join(names, ", ")}
	if len(other) > 0 {
		detail = append(detail, "bytes outside them: "+strings.Join(other, ", "))
	}
	r.step(title, true, detail...)
	return true
}

// outside lists, by region, the byte runs where a and b differ that no
// record of rs covers.
func outside(a, b *flash.Image, rs *backup.Restore) []string {
	var out []string
	for _, e := range a.KnownExtents() {
		for _, d := range diffRuns(a, b, e) {
			if !slices.ContainsFunc(rs.Records, func(x backup.RestoreRecord) bool { return x.Extent.Overlaps(d) }) {
				out = append(out, regionOf(d))
			}
		}
	}
	return out
}

// regionOf names the part of the flash e lies in.
func regionOf(e flash.Extent) string {
	for k := range mouse.Slots {
		if s, _ := mouse.ShortcutExtent(k); s.Contains(e) {
			return fmt.Sprintf("%s (shortcut slot %d)", e, k)
		}
		if s, _ := mouse.MacroExtent(k); s.Contains(e) {
			return fmt.Sprintf("%s (macro slot %d)", e, k)
		}
	}
	if e.Addr >= mouse.AddrSensor3955DPI && e.End() <= mouse.AddrEndEeprom {
		return e.String() + " (extended block)"
	}
	switch {
	case e.End() <= 256:
		return e.String() + " (settings page)"
	case e.Addr >= crcBlock.Addr && e.End() <= crcBlock.End():
		return e.String() + " (the 9504..9760 block arcctl never writes)"
	}
	return e.String()
}

// crcBlock is the mouse range full backups read past the map (§5).
var crcBlock = flash.Extent{Addr: 9504, Len: 256}

// markRecorded notes in a reset checkpoint that the run that left it
// recorded itself, so the run that resumes it does not commit its
// transcript again.
func (r *runner) markRecorded() error {
	cp, err := loadCheckpoint(r.cfg.Logs, r.def.name)
	if err != nil || cp == nil || cp.Kind != checkpointReset || cp.Recorded {
		return err
	}
	cp.Recorded = true
	return r.saveCheckpoint(*cp)
}

// restoreOptions are the overrides a restore of the fresh backup to the
// mouse as sn shows it needs.
func (r *runner) restoreOptions(sn *session.Snapshot, st *h7State) backup.RestoreOptions {
	a, b := sn.Identity.Addr, st.id.Addr
	other := sn.Identity.Key() != st.id.Key() || a != [3]byte{} && b != [3]byte{} && a != b
	return backup.RestoreOptions{OtherDevice: other, OtherProfile: !sameProfile(profileOf(sn), st.profile)}
}

func sameProfile(a, b *byte) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// longRange records cmd 23 before and after the reset.
func (r *runner) longRange(ctx context.Context, c *conn, before wire.Packet, beforeOK bool) {
	rp, err := r.rawPath(c)
	if err != nil {
		r.finding("cmd 23 after the reset: %v", err)
		return
	}
	after, ok, err := rp.query(ctx, wire.CmdGetLongRange)
	if err != nil {
		r.finding("cmd 23 after the reset: %v", err)
		return
	}
	text := func(p wire.Packet, ok bool) string {
		switch {
		case !ok:
			return "no reply"
		case p.Status() == wire.StatusNAK:
			return "a NAK"
		}
		return fmt.Sprintf("status %d, value %d", p.Status(), p[5])
	}
	r.finding("cmd 23 before the reset: %s; after it: %s", text(before, beforeOK), text(after, ok))
}

// pairing records whether the mouse stayed paired: after the reset a new
// session found it online, and its handshake named the same model.
func (r *runner) pairing(c *conn, st *h7State) {
	sn := c.s.Snapshot()
	same := sn.Handshake != nil && sn.Identity.CID == st.id.CID && sn.Identity.MID == st.id.MID
	switch {
	case st.paired && same && sn.Online:
		r.finding("pairing survived the reset: cmd 3 reports the mouse online and its handshake answers (%02X/%02X)", sn.Identity.CID, sn.Identity.MID)
		r.step("the mouse stayed paired", true, "cmd 3 online, handshake answered")
	case !st.paired:
		r.finding("the reset unpaired the mouse, or it stayed silent until re-paired (D6: revisit pairing before the reset is enabled)")
		r.step("the mouse stayed paired", false, "it answered only after the re-pair")
	default:
		r.step("the mouse stayed paired", false, fmt.Sprintf("online %v, handshake %v", sn.Online, sn.Handshake))
	}
	if sn.Identity.Key() != st.id.Key() {
		r.finding("the reset changed the device identity: %s before, %s after", st.id.Key(), sn.Identity.Key())
	}
}

// restoreB1 writes the fresh backup back, and lets the user try again
// after a stop instead of resetting the mouse again.
func (r *runner) restoreB1(ctx context.Context, c *conn, st *h7State) error {
	if !st.changed {
		r.step("write the fresh backup back", true, "nothing to write: the reset changed nothing")
		return nil
	}
	if st.other && !st.asked {
		st.asked = true
		ok, err := r.cfg.Prompt.Ask(stageID(r.def.name)+".write-back-other", "After the reset the mouse answers as another device or on "+
			"another onboard profile than the fresh backup's. Write the fresh backup back to it anyway, as 'arcctl restore' with "+
			"--other-device or --other-profile would?")
		if err != nil {
			return err
		}
		st.otherOK = ok
		r.res.Answers = append(r.res.Answers, Answer{ID: stageID(r.def.name) + ".write-back-other", Question: "write back with overrides", Answer: yesNo(ok), OK: true})
	}
	if st.other && !st.otherOK {
		r.say("The fresh backup stays on disk at " + r.res.Backups[0] + "; restore it with 'arcctl restore' and the override it names.")
		err := errors.New("not written back: the mouse answers as another device or profile, and the user did not allow the override")
		r.step("write the fresh backup back", false, err.Error())
		return err
	}
	for try := 1; ; try++ {
		err := r.writeBack(ctx, c, st)
		if err == nil || try >= writeBackTries || ctx.Err() != nil {
			return err
		}
		again, aerr := r.ask("h7.write-back-again", fmt.Sprintf("The write-back stopped: %v. Wake the mouse, or clear what the message names, "+
			"and try the write-back again?", err), true)
		if aerr != nil || !again {
			return errors.Join(err, aerr)
		}
		if _, err := r.reconnect(ctx, c); err != nil {
			return err
		}
		if _, err := r.settleOpen(ctx, c, "the stopped write-back"); err != nil {
			return err
		}
	}
}

// writeBackTries bounds the tries of H7's write-back, which the user can
// repeat after a stop rather than reset the mouse again.
const writeBackTries = 3

// writeBack writes the fresh backup back: the release restore's plan, plus
// the records of the layout it leaves out (the report rate, the DPI
// colours, hidden slots without a hardware record), which the stage writes
// at the experimental tier as D10 allows; never slots 0 and 1, and never a
// frozen or unmapped record. It then reads everything again from a new
// session, which must equal the backup in every record a plan can write.
func (r *runner) writeBack(ctx context.Context, c *conn, st *h7State) error {
	title := "write the fresh backup back"
	rs, im, err := r.planRestore2(ctx, c, st, !st.unknownAsked || st.unknownOK)
	if err != nil {
		r.step(title, false, err.Error())
		return err
	}
	sn := c.s.Snapshot()
	l := mouse.Layout(sn.Model)
	if !st.extraAsked {
		if _, extra, _, _, err := h7Plan(sn, im, rs, true, false); err == nil && len(extra) > 0 {
			st.extraAsked = true
			q := "The release restore leaves out " + strings.Join(extra, ", ") + ": the report rate and the DPI colours have no " +
				"feature yet, and D5 keeps hidden slots and settings read-only until their own H8. Write their bytes from before the " +
				"reset back at the experimental tier? If not, they stay as the reset left them."
			ok, err := r.cfg.Prompt.Ask(stageID(r.def.name)+".write-back-extra", q)
			if err != nil {
				return err
			}
			st.extraOK = ok
			r.res.Answers = append(r.res.Answers, Answer{ID: stageID(r.def.name) + ".write-back-extra", Question: q, Answer: yesNo(ok), OK: true})
			if !ok {
				st.declined = extra
			}
		}
	}
	if captured := h7Captured(l, rs); !st.unknownAsked && len(captured) > 0 {
		st.unknownAsked = true
		var names, where []string
		for _, x := range captured {
			names = append(names, x.Name)
			where = append(where, x.Name+" ("+x.Extent.String()+")")
		}
		q := "The reset also changed " + strings.Join(where, ", ") + ", where arcctl knows no valid value. Write back the bytes " +
			"the fresh backup captured there, as they are, at the experimental tier (--include-unknown)? If not, they stay as the reset left them."
		ok, err := r.cfg.Prompt.Ask(stageID(r.def.name)+".write-back-unknown", q)
		if err != nil {
			return err
		}
		st.unknownOK = ok
		r.res.Answers = append(r.res.Answers, Answer{ID: stageID(r.def.name) + ".write-back-unknown", Question: q, Answer: yesNo(ok), OK: true})
		if !ok {
			st.declined = append(st.declined, names...)
			if rs, im, err = r.planRestore2(ctx, c, st, false); err != nil {
				r.step(title, false, err.Error())
				return err
			}
			sn = c.s.Snapshot()
		}
	}
	p, extra, captured, left, err := h7Plan(sn, im, rs, st.extraOK, st.unknownOK)
	if err != nil {
		r.step(title, false, err.Error())
		return err
	}
	if len(extra) > 0 {
		r.finding("the release restore leaves out %s after a reset; the stage wrote them back at the experimental tier", strings.Join(extra, ", "))
	}
	if len(captured) > 0 {
		r.finding("the stage wrote back the captured bytes of %s at the experimental tier (--include-unknown)", strings.Join(captured, ", "))
	}
	if len(st.declined) > 0 {
		r.finding("left as the reset changed them, at the user's choice: %s", strings.Join(st.declined, ", "))
	}
	if len(left) > 0 {
		r.finding("not written back, arcctl never writes them: %s", strings.Join(left, ", "))
	}
	if len(p.Ops) > 0 {
		r.say("\nThe write-back:")
		if _, err := r.dryRun1(ctx, c, r.at, p); err != nil {
			r.step(title, false, err.Error())
			return err
		}
		if _, err := r.write(ctx, c, title, p, r.event); err != nil {
			return err
		}
	}
	f, err := r.fullRead(ctx, c, "hwtest H7 after the write-back")
	if err != nil {
		r.step("every record equals the fresh backup", false, err.Error())
		return err
	}
	sn = c.s.Snapshot()
	t := r.restoreTarget(sn)
	t.Image = f.Image()
	again, err := backup.PlanRestore(st.src, t, r.restoreOptions(sn, st))
	if err != nil {
		r.step("every record equals the fresh backup", false, err.Error())
		return err
	}
	var bad, unknown []string
	for _, x := range again.Records {
		name := fmt.Sprintf("%s at %s (%s)", x.Name, x.Extent, x.Fate)
		switch {
		case (x.Fate == backup.FateRefused || x.Fate == backup.FateUnknown) && slices.Contains(st.declined, x.Name):
		case x.Fate == backup.FateWrite || x.Fate == backup.FateRefused && writable(mouse.Layout(sn.Model), x.Extent):
			bad = append(bad, name)
		default:
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		r.finding("still different after the write-back, and never written by arcctl: %s", strings.Join(unknown, ", "))
	}
	if other := outside(st.src.Image, f.Image(), again); len(other) > 0 {
		r.finding("bytes outside the records that still differ from the fresh backup: %s", strings.Join(other, ", "))
	}
	if len(bad) > 0 {
		err := fmt.Errorf("still different: %s", strings.Join(bad, ", "))
		r.step("every record equals the fresh backup", false, err.Error())
		return err
	}
	detail := []string{fmt.Sprintf("a full read from a new session; %d records differ that arcctl never writes", len(unknown))}
	if len(unknown) > 0 {
		detail = append(detail, "they are: "+strings.Join(unknown, ", "), "the fresh backup keeps them")
	}
	r.step("every record equals the fresh backup", true, detail...)
	return removeCheckpoint(r.cfg.Logs, r.def.name)
}

// planRestore2 is planRestore with the options the reset may call for and,
// with unknown, --include-unknown; it also returns the image it planned on.
// The records that option adds change which others the restore refuses, so
// the write-back is planned with it only when the user agreed to it.
func (r *runner) planRestore2(ctx context.Context, c *conn, st *h7State, unknown bool) (*backup.Restore, *flash.Image, error) {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	o := r.restoreOptions(sn, st)
	if (o.OtherDevice || o.OtherProfile) && !st.otherOK {
		return nil, nil, fmt.Errorf("%w: the mouse answers as another device or profile than the fresh backup's", backup.ErrOtherProfile)
	}
	t := r.restoreTarget(sn)
	for range 3 {
		need := backup.RestoreReads(st.src.Image, t.Image)
		if len(need) == 0 {
			break
		}
		cp, err := c.s.Read(ctx, need...)
		if err != nil {
			return nil, nil, err
		}
		t.Image = cp.Image
	}
	o.IncludeUnknown = unknown
	rs, err := backup.PlanRestore(st.src, t, o)
	return rs, t.Image, err
}

// h7Captured lists the records H7 writes back as their captured bytes, only
// after asking: those --include-unknown writes that way, and the hidden
// settings D5 keeps out of it until their H8, which H7 writes back under
// T211.
func h7Captured(l plan.Layout, rs *backup.Restore) []backup.RestoreRecord {
	var out []backup.RestoreRecord
	for _, x := range rs.Records {
		if h7Captures(l, x) {
			out = append(out, x)
		}
	}
	return out
}

func h7Captures(l plan.Layout, x backup.RestoreRecord) bool {
	switch {
	case x.Captured:
		return true
	case x.Fate != backup.FateUnknown || x.Source == nil:
		return false
	}
	return x.Eligible || slices.Contains(l.Records, x.Extent) && l.Capturable(x.Extent)
}

// stagePair reports whether e is the stage count or the current stage. The
// release restore refuses them only when their write would leave the
// current stage past the count, so H7 never adds them.
func stagePair(e flash.Extent) bool {
	return e == flash.Extent{Addr: mouse.AddrMaxDpiStage, Len: 2} || e == flash.Extent{Addr: mouse.AddrCurrentDPI, Len: 2}
}

// h7Plan is the write-back: every record the restore writes and, with
// extra, every one but the stage pair it refuses that a plan may write, at
// the experimental tier; with unknown, the captured bytes h7Captured lists
// too. It lists by name the records it added, those it writes as captured
// bytes and those it leaves; captured records it does not write are the
// caller's to name. When the planner refuses the added records, it writes
// the restore's alone.
func h7Plan(sn *session.Snapshot, im *flash.Image, rs *backup.Restore, extra, unknown bool) (plan.Plan, []string, []string, []string, error) {
	l := mouse.Layout(sn.Model)
	var base, more []plan.Change
	var added, captured, left []string
	for _, x := range rs.Records {
		switch {
		case touchesPrimary(x.Extent):
			left = append(left, x.Name+" (slots 0 and 1 are never written by a hardware test; use 'arcctl restore')")
		case h7Captures(l, x):
			if !unknown {
				continue
			}
			more = append(more, plan.Change{Addr: x.Extent.Addr, New: x.Source, Tier: catalog.Experimental, Captured: true,
				Desc: "restore " + x.Name + " (captured bytes)"})
			captured = append(captured, x.Name)
		case x.Fate == backup.FateWrite:
			base = append(base, plan.Change{Addr: x.Extent.Addr, New: x.Source, Tier: x.Tier, Desc: "restore " + x.Name})
		case x.Fate == backup.FateRefused && stagePair(x.Extent):
			left = append(left, x.Name+" ("+x.Why+")")
		case x.Fate == backup.FateRefused && writable(l, x.Extent) && x.Source != nil:
			if !extra {
				continue
			}
			more = append(more, plan.Change{Addr: x.Extent.Addr, New: x.Source, Tier: catalog.Experimental, Desc: "restore " + x.Name + " (hardware test)"})
			added = append(added, x.Name)
		default:
			left = append(left, fmt.Sprintf("%s (%s)", x.Name, x.Fate))
		}
	}
	p, err := plan.New(sn.Identity, profileOf(sn), im, l, slices.Concat(base, more))
	if err == nil || len(more) == 0 {
		return p, added, captured, left, err
	}
	p, err2 := plan.New(sn.Identity, profileOf(sn), im, l, base)
	if err2 != nil {
		return p, nil, nil, left, err2
	}
	for _, x := range slices.Concat(added, captured) {
		left = append(left, x+" (the planner refused it with the others: "+strings.TrimPrefix(err.Error(), "plan: ")+")")
	}
	return p, nil, nil, left, nil
}

// writable reports whether a plan may write e as a whole record of l.
func writable(l plan.Layout, e flash.Extent) bool {
	if slices.ContainsFunc(l.Frozen, e.Overlaps) {
		return false
	}
	if slices.Contains(l.Records, e) {
		return true
	}
	for k := range mouse.Slots {
		if x, _ := mouse.KeyFnExtent(k); x == e {
			return true
		}
	}
	_, ok := bodySlot(e)
	return ok
}
