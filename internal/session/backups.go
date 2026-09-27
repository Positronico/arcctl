package session

import (
	"fmt"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

// Labels of the backups a write saves first (I1).
const (
	LabelBeforeWrite = "auto before write"
	LabelFirstWrite  = "auto first write"
)

// savedBackups are the I1 backups this session saved for one device.
type savedBackups struct {
	session string
	full    string
	missing []flash.Extent
}

// backsUp reports whether t writes to the device, so I1 applies.
func (s *Session) backsUp(t *writeTask) bool {
	return !t.gates.DryRun && !(t.kind == safety.KindRecover && t.how == safety.Leave)
}

// savedKey keys the backups a session saved by device and onboard profile
// (I11): a backup holds only the active profile.
func savedKey(id plan.Identity, profile *byte) string {
	return id.Key() + "/" + profileName(profile)
}

// forgetBackups drops the record of the I1 backups this session saved, once
// what they hold may no longer be what the device holds: the next write
// saves again.
func (s *Session) forgetBackups(why string) {
	if len(s.saved) == 0 {
		return
	}
	s.log.Info("the next write saves its backups again", "why", why)
	clear(s.saved)
}

// backupState is what I1 needs for the loaded device and profile. The device
// counts as written to once the journal holds a run on this profile.
func (s *Session) backupState(id plan.Identity, st *safety.Status) safety.BackupState {
	profile := s.profilePtr()
	var b safety.BackupState
	if st != nil {
		b.Written = slices.ContainsFunc(st.Runs, func(r *safety.Run) bool { return sameProfile(r.Profile, profile) })
	}
	if sv := s.saved[savedKey(id, profile)]; sv != nil {
		b.Session, b.Full, b.Missing = sv.session, sv.full, sv.missing
	}
	return b
}

// backUpLoaded saves every byte loaded so far, before the session's first
// write to the device and profile.
func (s *Session) backUpLoaded(t *writeTask, id plan.Identity) error {
	now := time.Now()
	path, err := s.saveBackup(s.captureOf(s.image.Clone(), false, s.unread, now), LabelBeforeWrite)
	if err != nil {
		return err
	}
	key := savedKey(id, s.profilePtr())
	sv := s.saved[key]
	if sv == nil {
		sv = &savedBackups{}
		s.saved[key] = sv
	}
	sv.session = path
	t.out.Backups = append(t.out.Backups, path)
	return nil
}

// backUpFull starts the full backup that precedes the first write ever to
// the device. The task waits for it and then starts over.
func (s *Session) backUpFull(t *writeTask) {
	s.log.Info("first write to this device: full backup first", "device", s.identity().Key())
	j := &job{kind: jobBackup, full: true, want: []flash.Extent{fullSettings, fullCRC}}
	j.waiters = []request{{ctx: t.req.ctx, then: func(res result) { s.fullBackupDone(t, res) }}}
	t.job = j
	s.addJob(j)
}

func (s *Session) fullBackupDone(t *writeTask, res result) {
	if s.task != t {
		return
	}
	t.job = nil
	if res.err != nil {
		s.endTask(&safety.PreflightError{Failures: []error{fmt.Errorf("%w: it did not finish: %w", safety.ErrNoFullBackup, res.err)}})
		return
	}
	path, err := s.saveBackup(res.capture, LabelFirstWrite)
	if err != nil {
		s.endTask(&safety.PreflightError{Failures: []error{err}})
		return
	}
	var profile *byte
	if p := res.capture.Profile; p.Supported {
		profile = &p.Value
	}
	s.saved[savedKey(res.capture.Device, profile)] = &savedBackups{session: path, full: path, missing: res.capture.Missing}
	t.out.Backups = append(t.out.Backups, path)
}

func (s *Session) saveBackup(c Capture, label string) (string, error) {
	if s.opt.Writes.Backups == nil {
		return "", fmt.Errorf("%w: no backup folder is configured", safety.ErrNoBackup)
	}
	path, err := s.opt.Writes.Backups.Save(c, label)
	if err != nil {
		return "", fmt.Errorf("%w: saving it failed: %w", safety.ErrNoBackup, err)
	}
	s.log.Info("backup saved", "label", label, "path", path, "full", c.Full, "missing", len(c.Missing))
	return path, nil
}
