package session

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/wire"
)

// The working load and the full backup (docs/protocol.md). Ends are
// exclusive: the web app never reads byte 6987.
var (
	loadSettings = flash.Extent{Addr: 0, Len: 256}
	loadExtended = flash.Extent{Addr: mouse.AddrSensor3955DPI, Len: mouse.AddrEndEeprom - mouse.AddrSensor3955DPI}
	fullSettings = flash.Extent{Addr: 0, Len: mouse.AddrEndEeprom}
	fullCRC      = flash.Extent{Addr: 9504, Len: 256}
)

const (
	macroHeader = 32
	opAttempts  = 2 // transactions per op while the mouse stays online
)

type jobKind uint8

const (
	jobReread jobKind = iota
	jobLoad
	jobBackup
	jobRead
)

var jobNames = [...]string{"reread", "load", "backup", "read"}

func (k jobKind) priority() int { return min(int(k), int(jobBackup)) }

// op is one transaction of a job: a read of at most 10 bytes, or a query.
type op struct {
	e     flash.Extent
	cmd   wire.Cmd
	tries int
}

// job is work that takes many transactions. It keeps its place across a
// sleeping mouse or a lock: the failed op is retried when the session can go
// on, and everything read so far is kept.
type job struct {
	kind    jobKind
	name    string // shown in Progress in place of the kind's name
	full    bool
	fresh   bool // a backup that reads every byte again, known or not
	want    []flash.Extent
	im      *flash.Image
	ops     []op
	stage   int
	started bool
	gen     int
	failed  []flash.Extent
	waiters []request
	done    int
	total   int
	begun   time.Time
	moved   time.Time    // the last reply, for the load watchdog
	own     bool         // a load after the session's own write
	was     *flash.Image // the working image when the load started
}

func (s *Session) addJob(j *job) {
	s.jobs = append(s.jobs, j)
	s.dirty = true
}

func (s *Session) nextJob() *job {
	if len(s.jobs) == 0 {
		return nil
	}
	return slices.MinFunc(s.jobs, func(a, b *job) int { return cmp.Compare(a.kind.priority(), b.kind.priority()) })
}

func (s *Session) removeJob(j *job) {
	s.jobs = slices.DeleteFunc(s.jobs, func(x *job) bool { return x == j })
	s.dirty = true
}

func (s *Session) failJobs(err error) {
	for _, j := range s.jobs {
		for _, w := range j.waiters {
			w.answer(result{err: err})
		}
	}
	s.jobs = nil
	s.dirty = true
}

// resumeJobs restarts the watchdog of every job, after a reply or a pause.
func (s *Session) resumeJobs() {
	now := time.Now()
	for _, j := range s.jobs {
		j.moved = now
	}
}

func (j *job) abandoned() bool {
	return len(j.waiters) > 0 && !slices.ContainsFunc(j.waiters, func(r request) bool { return r.ctx.Err() == nil })
}

func (s *Session) progress() Progress {
	if w := s.writing; w != nil {
		return Progress{Job: w.kind.String(), Done: w.done, Total: w.total, Paused: w.paused}
	}
	j := s.nextJob()
	if j == nil {
		if s.journalDue {
			return Progress{Job: "journal", Paused: s.base != Ready}
		}
		return Progress{}
	}
	return Progress{Job: j.label(), Done: j.done, Total: j.total, Paused: s.base != Loading && s.base != Ready}
}

func (s *Session) reload(r request) {
	switch {
	case s.dev == nil || s.hs == nil:
		r.answer(result{err: ErrNotConnected})
		return
	case s.model == nil || !s.pollsBattery():
		r.answer(result{err: ErrUnsupported})
		return
	}
	if i := slices.IndexFunc(s.jobs, func(j *job) bool { return j.kind == jobLoad }); i >= 0 {
		s.jobs[i].waiters = append(s.jobs[i].waiters, r)
		return
	}
	s.addJob(&job{kind: jobLoad, waiters: []request{r}})
}

// step runs one transaction of the most urgent job.
func (s *Session) step(ctx context.Context) bool {
	j := s.nextJob()
	if j == nil {
		if s.base == Loading {
			s.ready()
		}
		return false
	}
	if j.kind != jobLoad && j.abandoned() {
		s.removeJob(j)
		return true
	}
	if !j.started && !s.start(j) {
		return true
	}
	if j.kind == jobLoad && s.base == Ready {
		s.enter(Loading, nil)
	}
	if len(j.ops) == 0 {
		if !s.expand(j) {
			s.finish(j, nil)
		}
		return true
	}
	if time.Since(j.moved) > s.tm.LoadWatchdog {
		s.giveUp(j)
		return true
	}
	s.runOp(ctx, j)
	return true
}

func (s *Session) start(j *job) bool {
	j.started, j.begun, j.moved = true, time.Now(), time.Now()
	switch {
	case j.kind == jobLoad:
		j.im = flash.New()
		j.ops = reads(loadSettings, loadExtended)
		if s.image != nil {
			j.was = s.image.Clone()
		}
	case j.kind == jobReread:
		j.ops = reads(j.want...)
	case j.fresh:
		j.im = flash.New()
		j.ops = reads(j.want...)
	default:
		if s.image == nil {
			s.finish(j, ErrNotLoaded)
			return false
		}
		j.im = s.image.Clone()
		j.ops = reads(unknown(j.im, j.want)...)
	}
	j.total = len(j.ops)
	s.dirty = true
	return true
}

// expand adds the next stage of a load: the bound bodies, then the macro
// events their headers announce, then the queries.
func (s *Session) expand(j *job) bool {
	if j.kind != jobLoad {
		return false
	}
	for j.stage < 3 {
		j.stage++
		switch j.stage {
		case 1:
			j.ops = reads(bodies(j.im)...)
		case 2:
			j.ops = reads(macroEvents(j.im)...)
		case 3:
			j.ops = s.queries()
		}
		if len(j.ops) > 0 {
			j.total += len(j.ops)
			s.dirty = true
			return true
		}
	}
	return false
}

func (s *Session) queries() []op {
	out := []op{{cmd: wire.CmdGetProfile}, {cmd: wire.CmdFWVersion}, {cmd: wire.CmdBattery}}
	if s.hs != nil && !s.hs.Wired() {
		out = append(out, op{cmd: wire.CmdGetLongRange})
	}
	return out
}

func (s *Session) runOp(ctx context.Context, j *job) {
	o := j.ops[0]
	req := query(s.dev.target, o.cmd)
	if o.e.Len > 0 {
		req, _ = wire.BuildRead(s.dev.target, uint16(o.e.Addr), o.e.Len)
	}
	gen := j.gen
	t := s.mouseCmd(ctx, req)
	if j.gen != gen || !slices.Contains(s.jobs, j) {
		return
	}
	nak := errors.Is(t.err, wire.ErrNAK)
	if t.err == nil || nak {
		s.resumeJobs()
	}
	switch {
	case t.err == nil && o.e.Len > 0:
		s.store(o.e.Addr, t.rep.Data())
		j.pop()
	case t.err == nil || nak && o.e.Len == 0:
		s.answerQuery(o.cmd, t.rep, nak)
		j.pop()
	case nak:
		j.failed = append(j.failed, o.e)
		j.pop()
	case errors.Is(t.err, ErrOffline), ctx.Err() != nil:
	case errors.Is(t.err, ErrNoReply) && s.othersOpen():
		s.log.Info("another program holds the receiver and took the replies; reading the chunk again", "job", jobNames[j.kind], "extent", o.e)
	case errors.Is(t.err, ErrNoReply), transient(t.err):
		if transient(t.err) {
			if s.writeFailed(t.err); !slices.Contains(s.jobs, j) {
				return
			}
		}
		if j.ops[0].tries++; j.ops[0].tries >= opAttempts {
			s.log.Warn("giving up", "job", jobNames[j.kind], "err", t.err)
			if o.e.Len > 0 {
				j.failed = append(j.failed, o.e)
			}
			j.pop()
		}
	default:
		s.fail(t.err)
	}
	s.dirty = true
}

func (j *job) label() string {
	if j.name != "" {
		return j.name
	}
	return jobNames[j.kind]
}

func (j *job) pop() {
	j.ops = j.ops[1:]
	j.done++
}

// giveUp ends a job that has had no reply for Timing.LoadWatchdog while the
// mouse seemed online.
func (s *Session) giveUp(j *job) {
	s.log.Warn("no progress; giving up the remaining reads", "job", jobNames[j.kind], "ops", len(j.ops))
	for _, o := range j.ops {
		if o.e.Len > 0 {
			j.failed = append(j.failed, o.e)
		}
	}
	j.ops, j.stage = nil, 3
	s.dirty = true
}

// store puts freshly read bytes into the working image and into every job's
// image, so no job keeps a stale copy of them.
func (s *Session) store(addr int, b []byte) {
	for _, j := range s.jobs {
		if j.im != nil {
			_ = j.im.Set(addr, b)
		}
	}
	if s.image != nil {
		_ = s.image.Set(addr, b)
	}
}

func (s *Session) answerQuery(c wire.Cmd, p wire.Packet, nak bool) {
	switch c {
	case wire.CmdGetProfile:
		s.profile = Probe{Asked: true, Supported: !nak, Value: p[5] * flag(!nak)}
	case wire.CmdGetLongRange:
		s.longRange = Probe{Asked: true, Supported: !nak, Value: p[5] * flag(!nak)}
	case wire.CmdFWVersion:
		if !nak {
			s.versions.Mouse = version(p)
		}
	case wire.CmdBattery:
		if !nak {
			s.setBattery(p)
		}
	}
}

func flag(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func (s *Session) finish(j *job, err error) {
	s.removeJob(j)
	res := result{err: err}
	if err == nil {
		switch j.kind {
		case jobLoad:
			if !j.own && j.was != nil && drifted(j.was, j.im) {
				s.forgetBackups("the reload found bytes the backups before a write do not hold")
			}
			s.image, s.unread = j.im, j.failed
			s.log.Info("loaded", "unread", len(j.failed))
			s.ready()
			s.journalDue = s.opt.Writes != nil
		case jobBackup, jobRead:
			res.capture = s.capture(j)
		}
		if s.image != nil {
			s.shown = s.image.Clone()
		}
	}
	for _, w := range j.waiters {
		w.answer(res)
	}
}

func (s *Session) capture(j *job) Capture {
	return s.captureOf(j.im, j.full, j.failed, j.begun)
}

func (s *Session) captureOf(im *flash.Image, full bool, missing []flash.Extent, started time.Time) Capture {
	c := Capture{
		Device:   s.identity(),
		Model:    s.model,
		Profile:  s.profile,
		Versions: s.versions,
		Image:    im,
		Full:     full,
		Missing:  slices.Clone(missing),
		Started:  started,
		Finished: time.Now(),
	}
	if s.hs != nil {
		h := *s.hs
		c.Handshake = &h
	}
	return c
}

// drifted reports whether now knows a byte that was does not, or differs
// from it.
func drifted(was, now *flash.Image) bool {
	for _, e := range now.KnownExtents() {
		a, _ := now.Get(e)
		for i, x := range a {
			if y, ok := was.Byte(e.Addr + i); !ok || x != y {
				return true
			}
		}
	}
	return false
}

// othersOpen reports whether the client scan finds another program on the
// device's interface: a read it left unanswered was most likely taken by
// that program, so the read is not given up (§10 item 6).
func (s *Session) othersOpen() bool {
	if s.opt.Clients == nil || s.dev == nil {
		return false
	}
	cs, err := s.opt.Clients(s.dev.c)
	if err != nil {
		return false
	}
	s.clients, s.dirty = cs, true
	return len(cs) > 0
}

// profileSwitched restarts the load, since every byte may have changed, and
// fails backups, which would mix two profiles.
func (s *Session) profileSwitched() {
	s.log.Info("onboard profile switched")
	s.overlay = nil
	s.forgetBackups("the onboard profile switched")
	var load *job
	s.jobs = slices.DeleteFunc(s.jobs, func(j *job) bool {
		switch j.kind {
		case jobLoad:
			load = j
			return false
		case jobBackup, jobRead:
			for _, w := range j.waiters {
				w.answer(result{err: ErrProfileChanged})
			}
			return true
		}
		return false
	})
	if load == nil {
		s.addJob(&job{kind: jobLoad})
		return
	}
	*load = job{kind: jobLoad, gen: load.gen + 1, waiters: load.waiters}
	s.dirty = true
}

// reads splits extents into reads of at most 10 bytes.
func reads(extents ...flash.Extent) []op {
	var out []op
	for _, e := range extents {
		for a := e.Addr; a < e.End(); a += wire.MaxData {
			out = append(out, op{e: flash.Extent{Addr: a, Len: min(wire.MaxData, e.End()-a)}})
		}
	}
	return out
}

// unknown returns the parts of extents im does not know.
func unknown(im *flash.Image, extents []flash.Extent) []flash.Extent {
	var out []flash.Extent
	for _, e := range extents {
		start := -1
		for a := e.Addr; a <= e.End(); a++ {
			_, known := im.Byte(a)
			missing := a < e.End() && !known
			switch {
			case missing && start < 0:
				start = a
			case !missing && start >= 0:
				out = append(out, flash.Extent{Addr: start, Len: a - start})
				start = -1
			}
		}
	}
	return out
}

// bodies lists what the bindings point at: each shortcut slot whole, and the
// 32-byte header of each macro, both the slot's own and the one its binding
// names.
func bodies(im *flash.Image) []flash.Extent {
	var out []flash.Extent
	for slot := range mouse.Slots {
		e, _ := mouse.KeyFnExtent(slot)
		r, ok := im.Get(e)
		if !ok {
			continue
		}
		switch mouse.KeyType(r[0]) {
		case mouse.TypeShortcut:
			b, _ := mouse.ShortcutExtent(slot)
			out = append(out, b)
		case mouse.TypeMacro:
			for _, k := range []int{slot, int(r[1])} {
				if b, ok := mouse.MacroExtent(k); ok {
					out = append(out, flash.Extent{Addr: b.Addr, Len: macroHeader})
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b flash.Extent) int { return cmp.Compare(a.Addr, b.Addr) })
	return slices.Compact(out)
}

// macroEvents lists the rest of each macro whose header is loaded: the events
// and the checksum after the count at +31, when the count is valid.
func macroEvents(im *flash.Image) []flash.Extent {
	var out []flash.Extent
	for slot := range mouse.Slots {
		e, _ := mouse.MacroExtent(slot)
		h, ok := im.Get(flash.Extent{Addr: e.Addr, Len: macroHeader})
		if !ok {
			continue
		}
		n := int(h[macroHeader-1])
		if n < 1 || n > mouse.MaxMacroEvents {
			continue
		}
		out = append(out, flash.Extent{Addr: e.Addr + macroHeader, Len: 1 + 5*n})
	}
	return out
}
