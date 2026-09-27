package session

import (
	"bytes"
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
)

// writeState says why the session cannot write now, or nil.
func (s *Session) writeState() error {
	switch {
	case s.base == Locked, s.base == Seized, s.base == Stalled:
		return fmt.Errorf("%w: the session is %s: %v", safety.ErrBlocked, s.base, s.err)
	case s.dev == nil:
		return fmt.Errorf("%w: no device is attached", safety.ErrNotReady)
	}
	if err := s.conflictState(); err != nil {
		return err
	}
	switch {
	case s.base == Offline:
		return fmt.Errorf("%w: wake it (move it or press a button), then retry", safety.ErrOffline)
	case s.base != Ready:
		return fmt.Errorf("%w: the session is %s", safety.ErrNotReady, s.base)
	case s.model == nil || s.model.Family != catalog.FamilyMouse:
		return fmt.Errorf("%w: arcctl writes only to mice it knows", safety.ErrNotReady)
	case s.image == nil:
		return fmt.Errorf("%w: the configuration is not loaded", safety.ErrNotReady)
	}
	return nil
}

// conflictState is the Conflict or SuspectedConflict that blocks writes, or
// nil.
func (s *Session) conflictState() error {
	switch s.conflict {
	case Conflict:
		return fmt.Errorf("%w: a reply arcctl did not ask for arrived; close the other program and clear the conflict", safety.ErrConflict)
	case SuspectedConflict:
		return fmt.Errorf("%w: replies went missing; wait until the suspicion clears", safety.ErrConflict)
	}
	return nil
}

// hostFacts asks the OS whether this process holds the lock, and for the
// console state.
func (s *Session) hostFacts() (lock error, console safety.Console, consoleErr error) {
	lock = errors.New("no single-instance lock is configured")
	if s.opt.Writes.Lock != nil {
		lock = s.opt.Writes.Lock()
	}
	if s.opt.Writes.Console != nil {
		console, consoleErr = s.opt.Writes.Console()
	}
	return lock, console, consoleErr
}

// gather finds out, fresh from the device and the OS, what Preflight checks.
// An error means a check could not run at all.
func (s *Session) gather(ctx context.Context, t *writeTask, p plan.Plan, w *writeLink, loaded plan.Identity) (safety.Facts, error) {
	var f safety.Facts
	f.State = s.writeState()
	if f.State == nil {
		switch on, err := s.checkOnline(ctx); {
		case blocking(err):
			f.Online = fmt.Errorf("%w: cmd 3 failed: %w", safety.ErrBlocked, err)
		case err != nil:
			f.Online = fmt.Errorf("%w: cmd 3 failed: %w", safety.ErrNotReady, err)
		case !on:
			f.Online = fmt.Errorf("%w: wake it (move it or press a button), then retry", safety.ErrOffline)
		}
	}
	if f.State == nil && f.Online == nil {
		tx := s.transact(ctx, s.dev, handshake(s.dev.target), s.tm.Tries, s.tm.Try)
		if tx.err != nil {
			return f, fmt.Errorf("handshake: %w", tx.err)
		}
		h := parseHandshake(tx.rep)
		f.Identity = plan.Identity{CID: h.CID, MID: h.MID, Addr: s.addr, AddrTrusted: s.opt.TrustAddress, VID: s.dev.c.VID, PID: s.dev.c.PID}
		profile, err := s.askProfile(ctx)
		if err != nil {
			return f, err
		}
		f.Profile = profile
		if t.kind == safety.KindApply {
			if f.Current, err = s.reread(ctx, w, p, loaded, t.gates.DryRun); err != nil {
				return f, err
			}
		}
		s.noticeChange(loaded, f)
	}
	f.Clients, f.ScanErr = s.foreignClients()
	f.Lock, f.Console, f.ConsoleErr = s.hostFacts()
	return f, nil
}

// blocking reports whether err is one of a device that is locked, seized or
// stalled.
func blocking(err error) bool {
	switch hidio.Classify(err) {
	case hidio.ClassLocked, hidio.ClassSeized, hidio.ClassStalled:
		return true
	}
	return false
}

// reread reads every extent p writes again. The fresh bytes go into the
// loaded image, and into the published one when they differ, so the plan can
// be made again; a dry run compares them with its own writes on top.
func (s *Session) reread(ctx context.Context, w *writeLink, p plan.Plan, id plan.Identity, dry bool) (*flash.Image, error) {
	var extents []flash.Extent
	for _, op := range p.Ops {
		if !slices.Contains(extents, op.Extent) {
			extents = append(extents, op.Extent)
		}
	}
	cur, err := readExtents(ctx, w, extents)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, e := range extents {
		b, _ := cur.Get(e)
		if was, ok := s.image.Get(e); !ok || !bytes.Equal(was, b) {
			changed = true
		}
		s.store(e.Addr, b)
	}
	if changed {
		s.shown, s.dirty = s.image.Clone(), true
	}
	if dry && s.overlay != nil && s.overlayKey == id.Key() {
		cur = s.overlay.Apply(cur)
	}
	return cur, nil
}

// noticeChange starts over when the fresh answers show that the loaded
// configuration belongs to another mouse or profile.
func (s *Session) noticeChange(loaded plan.Identity, f safety.Facts) {
	switch {
	case f.Identity.Key() != loaded.Key():
		s.log.Warn("a different device answers the handshake", "loaded", loaded.Key(), "now", f.Identity.Key())
		s.online = false
		s.enter(Offline, ErrDeviceChanged)
		s.due.online = time.Now()
	case !sameProfile(f.Profile, s.profilePtr()):
		s.profileSwitched()
	}
}

// foreignClients scans every interface with the device's VID and PID, since
// another program may hold the receiver through one arcctl does not use.
func (s *Session) foreignClients() ([]safety.Client, error) {
	if s.opt.Clients == nil || s.dev == nil {
		return nil, nil
	}
	cands, err := s.opt.Devices.Enumerate()
	if err != nil {
		return nil, err
	}
	cands = slices.DeleteFunc(cands, func(c hidio.Candidate) bool { return c.VID != s.dev.c.VID || c.PID != s.dev.c.PID })
	if !slices.ContainsFunc(cands, func(c hidio.Candidate) bool { return c.Path == s.dev.c.Path }) {
		cands = append(cands, s.dev.c)
	}
	var out []safety.Client
	for _, c := range cands {
		cs, err := s.opt.Clients(c)
		if err != nil {
			return nil, err
		}
		if c.Path == s.dev.c.Path {
			s.clients, s.dirty = cs, true
		}
		for _, x := range cs {
			if !slices.ContainsFunc(out, func(o safety.Client) bool { return o.PID == x.PID }) {
				out = append(out, safety.Client{PID: x.PID, Name: x.Name, Seized: x.Seized})
			}
		}
	}
	return out, nil
}

func (s *Session) profilePtr() *byte {
	if !s.profile.Supported {
		return nil
	}
	v := s.profile.Value
	return &v
}
