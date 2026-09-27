package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/wire"
)

// resetLate is how long after a factory reset a cmd-9 reply counts as a late
// reply to it rather than a foreign one: the mouse may answer once it has
// restarted.
const resetLate = 30 * time.Second

// resetRanges are what the backup before a factory reset and the check after
// it read: the ranges of a full backup.
var resetRanges = []flash.Extent{fullSettings, fullCRC}

// Resetter sends the factory reset (D4); *Session implements it. It stands
// apart from API so that the fakes of API need not grow with it.
type Resetter interface {
	// PreflightReset runs the checks of a factory reset of dev on profile
	// and writes nothing: the gate first, with no I/O, then the preflight
	// with fresh answers from the mouse and the OS. Without the phrase in
	// g.Confirm its failures include one wrapping safety.ErrConfirm.
	PreflightReset(ctx context.Context, dev plan.Identity, profile *byte, g safety.Gates) error
	// Reset sends the factory reset to dev on profile; see Session.Reset.
	Reset(ctx context.Context, dev plan.Identity, profile *byte, g safety.Gates) (ResetOutcome, error)
}

// ResetOutcome is what a factory reset did and what the reload after it
// found.
type ResetOutcome struct {
	// Backup is the full backup saved just before the reset.
	Backup string
	// Run is the reset's journal run; "" when its packet never went out.
	Run string
	// Sent reports that the packet went out, or may have: it was journaled
	// as going out.
	Sent  bool
	Reply safety.Reply
	// Verdict is what the reload found compared with the backup. Changed
	// lists the byte runs that differ, Unread those it could not read.
	Verdict safety.ResetVerdict
	Changed []flash.Extent
	Unread  []flash.Extent
	// Before is what the backup holds, After what the reload read.
	Before, After *flash.Image
	DryRun        bool
	// Packets holds, for a dry run, the packet it would have sent.
	Packets []wire.Packet
}

// resetTask is the state of a factory reset between its passes.
type resetTask struct {
	dev      plan.Identity
	profile  *byte
	firmware string // the loaded firmware, which the gate opened for
	backup   string
	before   *flash.Image
	j        *safety.Journal
	sent     bool // the packet was journaled as going out
	out      ResetOutcome
}

// Reset sends the factory reset (cmd 9, D4) to the loaded mouse, which must
// be dev on profile. Unless hardware test H7's record of the reset covers the
// loaded model and firmware (safety.ResetGate), it is refused before any
// I/O. Otherwise, in order: the preflight with fresh answers, the typed
// phrase included; a full backup read afresh and saved as LabelBeforeReset,
// which must be complete; the preflight again; the reset journaled as its
// own run; its packet, let through once by the guard's Reset policy, sent
// once and never again, with Timing.ResetReply to answer; then, with or
// without a reply, the whole configuration read again and compared with the
// backup, which decides the verdict. No other command follows (D4).
//
// Cancelling ctx or Abort before the packet stops the reset with nothing
// sent; after it, they only stop the wait for the mouse, and the verdict is
// VerdictUnchecked with an error wrapping safety.ErrResetUnchecked. A dry
// run runs the checks and returns the packet it would send.
func (s *Session) Reset(ctx context.Context, dev plan.Identity, profile *byte, g safety.Gates) (ResetOutcome, error) {
	t := &writeTask{kind: safety.KindReset, gates: g, reset: &resetTask{dev: dev, profile: profile}}
	// The owner goroutine is done with t once write returns.
	_, err := s.write(ctx, t)
	return t.reset.out, err
}

func (s *Session) PreflightReset(ctx context.Context, dev plan.Identity, profile *byte, g safety.Gates) error {
	t := &writeTask{kind: safety.KindReset, gates: g, reset: &resetTask{dev: dev, profile: profile}}
	return s.call(ctx, request{kind: reqPreflight, task: t}).err
}

// openReset is the first check of a factory reset, before any I/O: the gate
// for the loaded model and firmware. It keeps the firmware the gate opened
// for, which the preflight compares with a fresh cmd 18.
func (s *Session) openReset(t *writeTask) error {
	if err := safety.ResetGate(s.model, s.versions.Mouse, s.verifiedStages()); err != nil {
		s.log.Warn("factory reset refused", "err", err)
		return &safety.PreflightError{Failures: []error{err}}
	}
	t.reset.firmware = s.versions.Mouse
	return nil
}

func (s *Session) verifiedStages() catalog.Verifications {
	if v := s.opt.Writes.verified; v != nil {
		return v
	}
	return catalog.VerifiedStages()
}

// resetPass is one pass of a factory reset: the gate and the checks, then
// the backup, or once it is saved, the packet.
func (s *Session) resetPass(ctx context.Context, t *writeTask) {
	r := t.reset
	if err := s.openReset(t); err != nil {
		s.endTask(err)
		return
	}
	c, err := s.check(ctx, t)
	if c.w != nil {
		defer s.release(c.w)
	}
	if err != nil {
		s.endTask(err)
		return
	}
	pkt, err := wire.Build(s.dev.target, wire.CmdClear, 0, nil)
	switch {
	case err != nil:
		s.endTask(err)
	case t.gates.DryRun:
		r.out.DryRun, r.out.Packets = true, []wire.Packet{pkt}
		s.endTask(nil)
	case r.backup == "":
		s.backUpBeforeReset(t)
	default:
		r.j = c.j
		s.sendReset(ctx, t, pkt)
	}
}

// backUpBeforeReset reads every backup range afresh, whatever is loaded, so
// the backup is what the mouse holds right before the reset. The task waits
// for it and then starts over from fresh checks.
func (s *Session) backUpBeforeReset(t *writeTask) {
	s.log.Info("factory reset: full backup first", "device", t.reset.dev.Key())
	j := &job{kind: jobBackup, full: true, fresh: true, want: resetRanges}
	j.waiters = []request{{ctx: t.req.ctx, then: func(res result) { s.resetBackedUp(t, res) }}}
	t.job = j
	s.addJob(j)
}

func (s *Session) resetBackedUp(t *writeTask, res result) {
	if s.task != t {
		return
	}
	t.job = nil
	r := t.reset
	if res.err != nil {
		s.endTask(&safety.PreflightError{Failures: []error{fmt.Errorf("%w: it did not finish: %w", safety.ErrNoFullBackup, res.err)}})
		return
	}
	path, err := s.saveBackup(res.capture, LabelBeforeReset)
	if err == nil {
		r.out.Backup = path
		t.out.Backups = append(t.out.Backups, path)
		err = safety.ResetBackupGate(path, res.capture.Missing)
	}
	if err != nil {
		s.endTask(&safety.PreflightError{Failures: []error{err}})
		return
	}
	r.backup, r.before = path, res.capture.Image
}

// sendReset sends the packet under the Reset policy, then starts the check.
func (s *Session) sendReset(ctx context.Context, t *writeTask, pkt wire.Packet) {
	r := t.reset
	grant := hidio.Grant{Model: s.model.Key, Firmware: r.firmware, Verified: s.verifiedStages()}
	s.writing = &writeProgress{kind: safety.KindReset, total: 1}
	s.enter(Applying, nil)
	run, reply, err := s.transmitReset(ctx, r, pkt, grant)
	s.writing = nil
	r.out.Run, r.out.Reply = run, reply
	if run == "" {
		s.afterWrite(err, false)
		s.endTask(err)
		return
	}
	r.sent, r.out.Sent = true, true
	s.resetAt = time.Now()
	s.overlay = nil
	s.forgetBackups("the mouse was reset")
	s.log.Info("factory reset sent", "run", run, "reply", reply, "err", err)
	s.afterWrite(err, false)
	s.checkAfterReset(t)
}

// transmitReset holds the Reset policy for exactly one transaction of one
// try, which the journal records before and after.
func (s *Session) transmitReset(ctx context.Context, r *resetTask, pkt wire.Packet, grant hidio.Grant) (string, safety.Reply, error) {
	if err := s.setPolicy(wire.Reset, "factory reset of "+r.dev.Key(), grant); err != nil {
		return "", safety.ReplyUnknown, err
	}
	defer s.setPolicy(wire.ReadOnly, "factory reset sent")
	return safety.SendReset(ctx, r.j, r.dev, r.profile, r.backup, pkt, func(ctx context.Context, p wire.Packet) (wire.Packet, error) {
		tx := s.transact(ctx, s.dev, p, 1, s.tm.ResetReply)
		if errors.Is(tx.err, ErrNoReply) {
			return tx.rep, fmt.Errorf("%w: %w", safety.ErrTimeout, tx.err)
		}
		return tx.rep, tx.err
	})
}

// checkAfterReset reads the whole configuration again, whether the reset
// was answered or not, to compare it with the backup from before.
func (s *Session) checkAfterReset(t *writeTask) {
	if s.dev == nil {
		s.resetUnchecked(t, ErrNotConnected)
		return
	}
	j := &job{kind: jobBackup, name: "reset check", full: true, fresh: true, want: resetRanges}
	j.waiters = []request{{ctx: t.req.ctx, then: func(res result) { s.resetChecked(t, res) }}}
	t.job = j
	s.addJob(j)
}

func (s *Session) resetChecked(t *writeTask, res result) {
	if s.task != t {
		return
	}
	t.job = nil
	if res.err != nil {
		s.resetUnchecked(t, res.err)
		return
	}
	r := t.reset
	changed, unread := safety.ResetDiff(r.before, res.capture.Image, resetRanges)
	v := safety.VerdictUnchanged
	if len(changed) > 0 {
		v = safety.VerdictChanged
	}
	r.out.Verdict, r.out.Changed, r.out.Unread = v, changed, unread
	r.out.Before, r.out.After = r.before, res.capture.Image
	s.log.Info("factory reset checked", "run", r.out.Run, "verdict", v, "changed", len(changed), "unread", len(unread))
	err := r.j.EndReset(r.out.Run, v, changed, unread, nil)
	s.reloadAfterReset()
	s.endTask(err)
}

// resetInterrupted ends a reset whose packet went out when the caller gave
// up or asked to stop while the check waited for the mouse.
func (s *Session) resetInterrupted(t *writeTask) {
	var cause error
	switch {
	case t.req.ctx.Err() != nil:
		cause = t.req.ctx.Err()
	case s.abort.Swap(false):
		cause = safety.ErrAborted
	case t.job != nil:
		return
	default:
		cause = errors.New("the reload did not start")
	}
	s.resetUnchecked(t, fmt.Errorf("the wait for the mouse stopped: %w", cause))
}

func (s *Session) resetUnchecked(t *writeTask, cause error) {
	r := t.reset
	r.out.Verdict, r.out.Before = safety.VerdictUnchecked, r.before
	s.log.Warn("factory reset not checked", "run", r.out.Run, "err", cause)
	err := fmt.Errorf("%w: %w", safety.ErrResetUnchecked, cause)
	if jerr := r.j.EndReset(r.out.Run, safety.VerdictUnchecked, nil, nil, cause); jerr != nil {
		err = errors.Join(err, jerr)
	}
	s.reloadAfterReset()
	s.endTask(err)
}

// reloadAfterReset loads the mouse again, which also checks the journal.
func (s *Session) reloadAfterReset() {
	if s.dev == nil || s.hs == nil || s.model == nil {
		return
	}
	if i := slices.IndexFunc(s.jobs, func(j *job) bool { return j.kind == jobLoad }); i >= 0 {
		s.jobs[i].own = true
		return
	}
	s.addJob(&job{kind: jobLoad, own: true})
}

// askFirmware sends cmd 18; a NAK leaves the version unknown.
func (s *Session) askFirmware(ctx context.Context) (string, error) {
	t := s.transact(ctx, s.dev, query(s.dev.target, wire.CmdFWVersion), s.tm.Tries, s.tm.Try)
	switch {
	case errors.Is(t.err, wire.ErrNAK):
		return "", nil
	case t.err != nil:
		return "", fmt.Errorf("cmd 18: %w", t.err)
	}
	return version(t.rep), nil
}

// settleResets checks the factory resets another process ended before it
// checked them: the whole configuration read afresh, compared with the
// backup each one names, and its end entry journaled. Until then the
// journal is not clean, so no write and no other reset starts.
func (s *Session) settleResets(j *safety.Journal, runs []*safety.Run) {
	if s.settling {
		return
	}
	s.settling = true
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	s.log.Warn("factory reset never checked: reading the mouse again", "runs", ids)
	jb := &job{kind: jobBackup, name: "reset check", full: true, fresh: true, want: resetRanges}
	jb.waiters = []request{{ctx: context.Background(), then: func(res result) { s.resetsSettled(j, runs, res) }}}
	s.addJob(jb)
}

func (s *Session) resetsSettled(j *safety.Journal, runs []*safety.Run, res result) {
	s.settling = false
	if res.err != nil {
		s.log.Warn("factory reset check did not finish; it runs again after the next load", "err", res.err)
		return
	}
	for _, r := range runs {
		v, cause := safety.VerdictUnchecked, error(nil)
		var changed, unread []flash.Extent
		var before *flash.Image
		err := fmt.Errorf("%w: no backup folder is configured", safety.ErrNoBackup)
		if b := s.opt.Writes.Backups; b != nil {
			before, err = b.Image(r.Reset.Backup)
		}
		if err != nil {
			cause = fmt.Errorf("the backup taken before it could not be read: %w", err)
		} else {
			changed, unread = safety.ResetDiff(before, res.capture.Image, resetRanges)
			v = safety.VerdictUnchanged
			if len(changed) > 0 {
				v = safety.VerdictChanged
			}
		}
		if err := j.EndReset(r.ID, v, changed, unread, cause); err != nil {
			s.log.Warn("factory reset check not journaled", "run", r.ID, "err", err)
			continue
		}
		s.log.Info("factory reset checked after the fact", "run", r.ID, "verdict", v, "changed", len(changed))
	}
	s.forgetBackups("a factory reset was checked")
	s.journalDue = true
}
