//go:build hwtest

package hwtest

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
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
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
)

// imageWith waits until the session is idle and returns its image with es
// read. A load reads only the working set afresh, so unbound bodies are
// unknown after it, and Read fetches only what is unknown: the bytes are
// never older than the last load.
func (r *runner) imageWith(ctx context.Context, c *conn, es ...flash.Extent) (*flash.Image, *session.Snapshot, error) {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return nil, sn, err
	}
	if len(es) == 0 {
		return sn.Image.Clone(), sn, nil
	}
	cp, err := c.s.Read(ctx, es...)
	if err != nil {
		return nil, sn, err
	}
	if len(cp.Missing) > 0 {
		return nil, sn, fmt.Errorf("%w: could not read %v", ErrNotReady, cp.Missing)
	}
	return cp.Image, sn, nil
}

// planOn plans changes on what the device holds now, read for the occasion.
func (r *runner) planOn(ctx context.Context, c *conn, changes []plan.Change) (plan.Plan, error) {
	var es []flash.Extent
	for _, ch := range changes {
		es = append(es, flash.Extent{Addr: ch.Addr, Len: len(ch.New)})
	}
	cur, sn, err := r.imageWith(ctx, c, es...)
	if err != nil {
		return plan.Plan{}, err
	}
	return plan.New(sn.Identity, profileOf(sn), cur, mouse.Layout(sn.Model), changes)
}

// write applies p through the session, as an apply step does, and waits
// for the reload after it.
func (r *runner) write(ctx context.Context, c *conn, title string, p plan.Plan, on func(safety.OpEvent)) (session.Outcome, error) {
	if err := spares(p); err != nil {
		r.step(title, false, err.Error())
		return session.Outcome{}, err
	}
	out, err := c.s.Apply(ctx, p, r.gates(p.Ops), on)
	if err == nil {
		err = r.settled(ctx, c, out.Run)
	}
	if err != nil {
		r.step(title, false, fmt.Sprintf("run %q: %d of %d records verified", out.Run, out.Verified, out.Ops), err.Error())
		return out, err
	}
	r.step(title, true, r.outcome(out)...)
	return out, nil
}

// spares refuses a plan made while a stage runs that would write slot 0 or
// 1, which the builder refuses for the plans it lays out.
func spares(p plan.Plan) error {
	for _, op := range p.Ops {
		if touchesPrimary(op.Extent) {
			return fmt.Errorf("%w: op %d writes %s", errPrimary, op.Seq, op.Extent)
		}
	}
	return nil
}

// putBack writes want's bytes back to every extent of es the device holds
// differently, as one plan through the session. A body extent grows to the
// longer of the record want declares and the one the device holds, so no
// tail of either outlives it.
func (r *runner) putBack(ctx context.Context, c *conn, es []flash.Extent, want *flash.Image, title string) error {
	es = mergeExtents(es)
	var slots []flash.Extent
	for _, e := range es {
		if s, ok := bodySlot(e); ok {
			slots = append(slots, s)
		}
	}
	cur, sn, err := r.imageWith(ctx, c, append(slices.Clone(es), slots...)...)
	if err != nil {
		return err
	}
	if err := healthy(sn); err != nil {
		return err
	}
	var changes []plan.Change
	for _, e := range es {
		e = bodySpan(e, want, cur)
		was, ok := want.Get(e)
		if !ok {
			return fmt.Errorf("the fresh backup lacks %s", e)
		}
		if now, _ := cur.Get(e); bytes.Equal(now, was) {
			continue
		}
		t, err := extentTier(sn, e, was)
		if err != nil {
			return err
		}
		changes = append(changes, plan.Change{Addr: e.Addr, New: was, Desc: "put back " + e.String(), Tier: t})
	}
	if len(changes) == 0 {
		return nil
	}
	p, err := plan.New(sn.Identity, profileOf(sn), cur, mouse.Layout(sn.Model), changes)
	if err != nil {
		return err
	}
	_, err = r.write(ctx, c, title, p, r.event)
	return err
}

// mergeExtents sorts es and drops repeats; of two body extents that start a
// slot, the longer stays.
func mergeExtents(es []flash.Extent) []flash.Extent {
	out := slices.Clone(es)
	slices.SortFunc(out, func(a, b flash.Extent) int { return cmp.Or(cmp.Compare(a.Addr, b.Addr), cmp.Compare(b.Len, a.Len)) })
	return slices.CompactFunc(out, func(a, b flash.Extent) bool { return a.Addr == b.Addr })
}

// bodySlot is the shortcut or macro slot a body extent starts.
func bodySlot(e flash.Extent) (flash.Extent, bool) {
	for k := range mouse.Slots {
		for _, table := range []func(int) (flash.Extent, bool){mouse.ShortcutExtent, mouse.MacroExtent} {
			if s, _ := table(k); s.Addr == e.Addr && s.Contains(e) {
				return s, true
			}
		}
	}
	return flash.Extent{}, false
}

// bodySpan grows a body extent to the records want and cur declare in its
// slot; other extents come back as they are.
func bodySpan(e flash.Extent, want, cur *flash.Image) flash.Extent {
	s, ok := bodySlot(e)
	if !ok {
		return e
	}
	head, event := 0, 3
	if m, _ := mouse.MacroExtent(0); s.Len == m.Len {
		head, event = mouse.MaxNameLen+1, 5
	}
	for _, im := range []*flash.Image{want, cur} {
		if n, ok := im.Byte(s.Addr + head); ok && n > 0 {
			if size := head + 2 + event*int(n); size <= s.Len {
				e.Len = max(e.Len, size)
			}
		}
	}
	return e
}

// extentTier is the tier of writing b at e, from the features the planner
// takes for that record.
func extentTier(sn *session.Snapshot, e flash.Extent, b []byte) (catalog.Tier, error) {
	m := sn.Model
	opt := mouse.Options{Device: sn.Identity, Profile: profileOf(sn), Firmware: sn.Versions.Mouse, Verified: catalog.VerifiedStages()}
	tier := func(slot int, fs ...mouse.Feature) catalog.Tier {
		if slot >= 0 {
			fs = append(fs, mouse.SlotFeatures(m, slot)...)
		}
		t := catalog.Verified
		for _, f := range fs {
			ft, _ := f.Tier(m, opt)
			t = min(t, ft)
		}
		return t
	}
	for k := range mouse.Slots {
		if x, _ := mouse.KeyFnExtent(k); x == e {
			return tier(k, keyFnFeature(k, b)), nil
		}
		if x, _ := mouse.ShortcutExtent(k); x.Addr == e.Addr {
			return tier(k, mouse.FeatureShortcut), nil
		}
		if x, _ := mouse.MacroExtent(k); x.Addr == e.Addr {
			return tier(k, mouse.FeatureMacro), nil
		}
	}
	switch e {
	case pairExtent(mouse.AddrCurrentDPI):
		return tier(-1, mouse.FeatureCurrent), nil
	case pairExtent(mouse.AddrMaxDpiStage):
		return tier(-1, mouse.FeatureStages), nil
	}
	for i := range mouse.MaxStages {
		if d, _ := mouse.DPIExtent(i); d == e {
			return tier(-1, mouse.FeatureDPI), nil
		}
	}
	return catalog.Off, fmt.Errorf("hwtest: no tier for writing %s", e)
}

func keyFnFeature(slot int, b []byte) mouse.Feature {
	switch mouse.KeyType(b[0]) {
	case mouse.TypeShortcut:
		return mouse.FeatureShortcut
	case mouse.TypeMacro:
		if int(b[1]) != slot {
			return mouse.FeatureMacroForeign
		}
		return mouse.FeatureMacro
	}
	return mouse.FeatureSystem
}

// reconnect ends c's session and starts a new one on the same devices, so
// nothing the old one loaded is reused; c holds the new one.
func (r *runner) reconnect(ctx context.Context, c *conn) (*session.Snapshot, error) {
	c.close()
	var rec *hidio.Recorder
	if r.rec != nil {
		rec = r.rec.rec
	}
	n, sn, err := r.connect(ctx, connectOpts{rec: rec, writes: true})
	if err != nil {
		return sn, err
	}
	*c = *n
	return sn, nil
}

// fullRead reads everything a full backup covers from a new session and
// saves it under label.
func (r *runner) fullRead(ctx context.Context, c *conn, label string) (*backup.File, error) {
	if _, err := r.reconnect(ctx, c); err != nil {
		return nil, err
	}
	var cp session.Capture
	err := r.watch(ctx, c, func(ctx context.Context) error {
		var err error
		cp, err = c.s.Backup(ctx, true)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("the full read: %w", err)
	}
	if len(cp.Missing) > 0 {
		return nil, fmt.Errorf("%w: it could not read %v", ErrBackup, cp.Missing)
	}
	_, f, err := r.saveBackup(cp, label)
	return f, err
}

// restoreTarget is the mouse a restore writes to, as the session loaded it.
func (r *runner) restoreTarget(sn *session.Snapshot) backup.Target {
	t := backup.Target{
		Model: sn.Model,
		Image: sn.Image,
		Options: mouse.Options{Device: sn.Identity, Profile: profileOf(sn), Firmware: sn.Versions.Mouse,
			Verified: catalog.VerifiedStages()},
		Source: r.cfg.Source,
	}
	if h := sn.Handshake; h != nil {
		conn := h.Conn
		t.Conn = &conn
	}
	return t
}

// planRestore lays out a restore of src on what the mouse holds now, after
// reading what the restore compares (backup.RestoreReads).
func (r *runner) planRestore(ctx context.Context, c *conn, src *backup.Source) (*backup.Restore, error) {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return nil, err
	}
	t := r.restoreTarget(sn)
	for range 3 {
		need := backup.RestoreReads(src.Image, t.Image)
		if len(need) == 0 {
			break
		}
		cp, err := c.s.Read(ctx, need...)
		if err != nil {
			return nil, err
		}
		if len(cp.Missing) > 0 {
			return nil, fmt.Errorf("%w: could not read %v", ErrNotReady, cp.Missing)
		}
		t.Image = cp.Image
	}
	return backup.PlanRestore(src, t, backup.RestoreOptions{})
}

// recordNames lists the records of a restore by name, fate and extent: no
// bytes, which may be the user's keys.
func recordNames(rs *backup.Restore) []string {
	var out []string
	for _, x := range rs.Records {
		s := fmt.Sprintf("%s at %s: %s", x.Name, x.Extent, x.Fate)
		if x.Why != "" && x.Fate != backup.FateWrite {
			s += " (" + x.Why + ")"
		}
		out = append(out, s)
	}
	return out
}

// openRun is the unfinished run id of the journal, as the session inspected
// it after its last load.
func openRun(sn *session.Snapshot, id string) *session.OpenRun {
	if sn.Journal == nil {
		return nil
	}
	for i := range sn.Journal.Open {
		if sn.Journal.Open[i].Run.ID == id {
			return &sn.Journal.Open[i]
		}
	}
	return nil
}

// runStatus reads the journal of the loaded device from its folder.
func (r *runner) runStatus(sn *session.Snapshot, id string) (*safety.Run, error) {
	st, err := safety.Load(filepath.Join(r.cfg.Session.Writes.Journal, sn.Identity.Key()))
	if err != nil {
		return nil, err
	}
	if run := st.Find(id); run != nil {
		return run, nil
	}
	return nil, fmt.Errorf("%w: no run %q", ErrJournal, id)
}

// classes says what each extent of an inspected run holds.
func classes(in *safety.Inspection) string {
	var out []string
	for _, e := range in.Extents {
		s := fmt.Sprintf("%s %s", e.Extent, e.Class)
		if e.Class == safety.ClassTorn {
			s += fmt.Sprintf(" (%d of its %d bytes differ from the old record, %d from the new one)", differ(e.Found, e.Before), e.Extent.Len, differ(e.Found, e.After))
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

func differ(a, b []byte) int {
	n := 0
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			n++
		}
	}
	return n + max(len(a), len(b)) - min(len(a), len(b))
}

// recoverBack settles the open run id by writing back the bytes from before
// it, and checks the journal is clean after.
func (r *runner) recoverBack(ctx context.Context, c *conn, id, title string) error {
	out, err := c.s.Recover(ctx, id, safety.Back, r.gates(nil), r.event)
	if err == nil {
		var sn *session.Snapshot
		if sn, err = r.ready(ctx, c); err == nil && sn.Journal != nil && len(sn.Journal.Open) > 0 {
			err = fmt.Errorf("%w: %d runs still open", ErrJournal, len(sn.Journal.Open))
		}
	}
	if err != nil {
		r.step(title, false, fmt.Sprintf("run %q: %d of %d records verified", out.Run, out.Verified, out.Ops), err.Error())
		return err
	}
	r.step(title, true, fmt.Sprintf("%d of %d records written back and verified; the journal is clean", out.Verified, out.Ops))
	return nil
}

// settleOpen recovers every run the journal holds open, writing back the
// bytes from before each, and says what their extents held.
func (r *runner) settleOpen(ctx context.Context, c *conn, why string) ([]string, error) {
	sn, err := r.ready(ctx, c)
	if err != nil {
		return nil, err
	}
	if sn.Journal == nil {
		return nil, fmt.Errorf("%w: the session did not read the journal", ErrJournal)
	}
	var held []string
	for _, o := range sn.Journal.Open {
		if o.Inspection != nil {
			held = append(held, classes(o.Inspection))
		}
		if err := r.recoverBack(ctx, c, o.Run.ID, "journal recovery after "+why); err != nil {
			return held, err
		}
	}
	return held, nil
}

// trace follows the op events of a drill's write: what paused it, when, and
// how it went on. pace slows its chunks down, so the user has time to act
// while it writes; it stops once the drill saw what it waits for.
type trace struct {
	mu       sync.Mutex
	pace     time.Duration
	pacing   func(safety.OpEvent) bool
	chunk    int // chunks of the current op acknowledged in this attempt
	chunks   int
	pauses   []pause
	restarts int
}

type pause struct {
	op     plan.Op
	after  int // chunks acknowledged before it
	of     int
	locked bool
	err    error
}

func (t *trace) on(ctx context.Context, r *runner) func(safety.OpEvent) {
	return func(e safety.OpEvent) {
		r.event(e)
		t.mu.Lock()
		switch e.Kind {
		case safety.EventStart, safety.EventRestart:
			t.chunk, t.chunks = 0, e.Chunks
			if e.Kind == safety.EventRestart {
				t.restarts++
			}
		case safety.EventChunk:
			t.chunk = e.Chunk
		case safety.EventPaused:
			t.pauses = append(t.pauses, pause{op: e.Op, after: t.chunk, of: t.chunks, locked: hidio.Classify(e.Err) == hidio.ClassLocked, err: e.Err})
		}
		wait := t.pace > 0 && e.Kind == safety.EventChunk && (t.pacing == nil || t.pacing(e)) && len(t.pauses) == 0
		pace := t.pace
		t.mu.Unlock()
		if wait {
			sleep(ctx, pace)
		}
	}
}

// midRecord is the first pause that came after a chunk of its op went out,
// whose cause locked says; nil when there was none.
func (t *trace) midRecord(locked bool) *pause {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, p := range t.pauses {
		if p.after > 0 && p.locked == locked {
			return &t.pauses[i]
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// awaitDevice waits until the session is loaded again after the device went
// away or blocked, asking the user once with id and text while it is gone.
func (r *runner) awaitDevice(ctx context.Context, c *conn, id, text string, limit time.Duration) (*session.Snapshot, error) {
	asked := false
	deadline := time.Now().Add(limit)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		sn := c.s.Snapshot()
		switch sn.State {
		case session.Ready, session.Recovering:
			if sn.Progress.Job == "" && sn.Journal != nil {
				return sn, nil
			}
		case session.NoReceiver, session.Locked, session.Offline:
			if !asked && text != "" {
				asked = true
				if err := r.wait(id, text); err != nil {
					return sn, err
				}
				deadline = time.Now().Add(limit)
				continue
			}
		case session.NeedsPermission, session.Seized, session.Stalled, session.Conflict, session.Choosing:
			return sn, fmt.Errorf("%w: the session is %s: %v", ErrNotReady, sn.State, sn.Err)
		}
		if time.Now().After(deadline) {
			return sn, fmt.Errorf("%w: still %s after %s", ErrNotReady, sn.State, limit)
		}
		select {
		case <-ctx.Done():
			return sn, ctx.Err()
		case <-c.s.Changed():
		case <-tick.C:
		}
	}
}

// stopped reports whether err is the executor's stop of a run, and returns
// it.
func stopped(err error) (*safety.StopError, bool) {
	var st *safety.StopError
	if errors.As(err, &st) {
		return st, true
	}
	return nil, false
}
