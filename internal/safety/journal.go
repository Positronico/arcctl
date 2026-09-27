package safety

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

const journalVersion = 1

// RunKind is why a run was written.
type RunKind uint8

const (
	KindApply   RunKind = iota + 1
	KindRevert          // undoes the run named by Of
	KindRecover         // finishes or rolls back the run named by Of
	KindReset           // one factory reset (cmd 9); it has no ops
)

var kindNames = [...]string{"", "apply", "revert", "recover", "reset"}

func (k RunKind) String() string {
	if k >= KindApply && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind " + strconv.Itoa(int(k))
}

// State is where an op got to. Planned is journaled for every op of a run
// before its first packet, the others as the op goes.
type State uint8

const (
	StatePlanned State = iota + 1
	StateSending
	StateSent
	StateVerified
	StateFailed
)

var stateNames = [...]string{"", "planned", "sending", "sent", "verified", "failed"}

func (s State) String() string {
	if s >= StatePlanned && int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "state " + strconv.Itoa(int(s))
}

// Strategy is how recovery settles an unfinished run.
type Strategy uint8

const (
	Forward Strategy = iota + 1 // every extent gets the bytes the run meant to leave
	Back                        // every extent gets the bytes it held before the run
	Leave                       // nothing is written; refused while an extent is torn
)

var strategyNames = [...]string{"", "forward", "back", "leave"}

func (s Strategy) String() string {
	if s >= Forward && int(s) < len(strategyNames) {
		return strategyNames[s]
	}
	return "strategy " + strconv.Itoa(int(s))
}

// Journal is the append-only record of the writes to one device. Each session
// that writes gets its own file, <root>/<identity key>/<session>.jsonl. Every
// entry is fsynced before the call that wrote it returns, and a run's ops are
// all journaled before its first packet. After a failed append the journal
// refuses to write again, so a torn line can only ever be the last one of a
// file.
type Journal struct {
	dir  string
	key  string
	sync func(*os.File) error

	mu     sync.Mutex
	name   string
	f      *os.File
	runs   int
	broken error
}

// OpenJournal opens the journal of dev under root. The session file is
// created by the first run.
func OpenJournal(root string, dev plan.Identity) (*Journal, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: no journal folder", ErrJournal)
	}
	dir := filepath.Join(root, dev.Key())
	if err := mkdirSynced(dir, syncDir); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJournal, err)
	}
	return &Journal{dir: dir, key: dev.Key(), sync: (*os.File).Sync}, nil
}

// mkdirSynced creates dir and the folders above it that are missing, and
// syncs the parent of each folder it creates, so that the new entries
// survive a power loss.
func mkdirSynced(dir string, sync func(string) error) error {
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		_, err := os.Stat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, d := range slices.Backward(missing) {
		if err := sync(filepath.Dir(d)); err != nil {
			return err
		}
	}
	return nil
}

// Dir is the folder holding the journal files of this device.
func (j *Journal) Dir() string { return j.dir }

// Path is this session's file, or "" before the first run.
func (j *Journal) Path() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return ""
	}
	return j.f.Name()
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	j.broken = errors.New("closed")
	return err
}

// Status reads every file of this device's journal.
func (j *Journal) Status() (*Status, error) { return Load(j.dir) }

func (j *Journal) begin(kind RunKind, of string, p plan.Plan) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.open(); err != nil {
		return "", err
	}
	j.runs++
	id := j.name + "/" + strconv.Itoa(j.runs)
	now := stamp()
	es := []entry{{V: journalVersion, Type: typeRun, Run: id, Time: now, Kind: kind.String(), Of: of,
		Device: deviceOf(p.Device), Profile: p.Profile, NOps: len(p.Ops)}}
	for _, op := range p.Ops {
		e := op.Extent
		es = append(es, entry{Type: typeOp, Run: id, Time: now, Seq: op.Seq, State: StatePlanned.String(),
			Phase: op.Phase.String(), Extent: &e, Old: op.Old, New: op.New, Tier: op.Tier.String(), Desc: op.Desc})
	}
	if err := j.append(es...); err != nil {
		return "", err
	}
	return id, nil
}

func (j *Journal) state(run string, seq int, s State, st *StopError) error {
	e := entry{Type: typeOp, Run: run, Time: stamp(), Seq: seq, State: s.String()}
	if st != nil {
		e.Error = st.Err.Error()
		e.Found = st.Found
		if st.Class != ClassUnknown {
			e.Class = st.Class.String()
		}
	}
	return j.write(e)
}

func (j *Journal) end(run string, cause error) error {
	e := entry{Type: typeEnd, Run: run, Time: stamp(), Result: resultComplete}
	if cause != nil {
		e.Result, e.Error = resultStopped, cause.Error()
	}
	return j.write(e)
}

// resolve records how r was settled; kept lists, for Leave, the ops whose
// bytes the device holds.
func (j *Journal) resolve(r *Run, how Strategy, by string, kept []int) error {
	es := []entry{{Type: typeResolve, Run: r.ID, Time: stamp(), Result: how.String(), By: by, Kept: kept}}
	for _, x := range r.Recoveries {
		es = append(es, entry{Type: typeResolve, Run: x.ID, Time: stamp(), Result: how.String(), By: by})
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.open(); err != nil {
		return err
	}
	return j.append(es...)
}

func (j *Journal) write(e entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.broken != nil:
		return fmt.Errorf("%w: %w", ErrJournal, j.broken)
	case j.f == nil:
		return fmt.Errorf("%w: no run was started", ErrJournal)
	}
	return j.append(e)
}

func (j *Journal) open() error {
	if j.broken != nil {
		return fmt.Errorf("%w: %w", ErrJournal, j.broken)
	}
	if j.f != nil {
		return nil
	}
	base := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + strconv.Itoa(os.Getpid())
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		f, err := os.OpenFile(filepath.Join(j.dir, name+fileExt), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
		switch {
		case err == nil:
			if err := syncDir(j.dir); err != nil {
				f.Close()
				return fmt.Errorf("%w: %w", ErrJournal, err)
			}
			j.f, j.name = f, name
			return nil
		case !errors.Is(err, fs.ErrExist) || i >= 100:
			return fmt.Errorf("%w: %w", ErrJournal, err)
		}
	}
}

func (j *Journal) append(es ...entry) error {
	var buf bytes.Buffer
	for _, e := range es {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrJournal, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	_, err := j.f.Write(buf.Bytes())
	if err == nil {
		err = j.sync(j.f)
	}
	if err != nil {
		j.broken = err
		return fmt.Errorf("%w: %w", ErrJournal, err)
	}
	return nil
}

func stamp() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

const (
	fileExt = ".jsonl"

	typeRun     = "run"
	typeOp      = "op"
	typeSend    = "send"
	typeEnd     = "end"
	typeResolve = "resolve"

	resultComplete = "complete"
	resultStopped  = "stopped"
)

type entry struct {
	V       int            `json:"v,omitempty"`
	Type    string         `json:"type"`
	Run     string         `json:"run"`
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind,omitempty"`
	Of      string         `json:"of,omitempty"`
	Device  *device        `json:"device,omitempty"`
	Profile *byte          `json:"profile,omitempty"`
	NOps    int            `json:"ops,omitempty"`
	Seq     int            `json:"seq,omitempty"`
	State   string         `json:"state,omitempty"`
	Phase   string         `json:"phase,omitempty"`
	Extent  *flash.Extent  `json:"extent,omitempty"`
	Old     hexBytes       `json:"old,omitempty"`
	New     hexBytes       `json:"new,omitempty"`
	Tier    string         `json:"tier,omitempty"`
	Desc    string         `json:"desc,omitempty"`
	Found   hexBytes       `json:"found,omitempty"`
	Class   string         `json:"class,omitempty"`
	Result  string         `json:"result,omitempty"`
	By      string         `json:"by,omitempty"`
	Kept    []int          `json:"kept,omitempty"`
	Backup  string         `json:"backup,omitempty"`
	Packet  hexBytes       `json:"packet,omitempty"`
	Reply   string         `json:"reply,omitempty"`
	Verdict string         `json:"verdict,omitempty"`
	Changed []flash.Extent `json:"changed,omitempty"`
	Unread  []flash.Extent `json:"unread,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type device struct {
	Key         string `json:"key"`
	VID         string `json:"vid"`
	PID         string `json:"pid"`
	CID         string `json:"cid"`
	MID         byte   `json:"mid"`
	Addr        string `json:"addr,omitempty"`
	AddrTrusted bool   `json:"addr_trusted,omitempty"`
}

func deviceOf(id plan.Identity) *device {
	d := &device{Key: id.Key(), VID: fmt.Sprintf("%04x", id.VID), PID: fmt.Sprintf("%04x", id.PID),
		CID: fmt.Sprintf("%02x", id.CID), MID: id.MID, AddrTrusted: id.AddrTrusted}
	if id.Addr != [3]byte{} {
		d.Addr = hex.EncodeToString(id.Addr[:])
	}
	return d
}

func (d *device) identity() (plan.Identity, error) {
	var id plan.Identity
	num := func(s string, bits int) (uint64, error) { return strconv.ParseUint(s, 16, bits) }
	vid, err1 := num(d.VID, 16)
	pid, err2 := num(d.PID, 16)
	cid, err3 := num(d.CID, 8)
	if err := errors.Join(err1, err2, err3); err != nil {
		return id, err
	}
	id = plan.Identity{VID: uint16(vid), PID: uint16(pid), CID: byte(cid), MID: d.MID, AddrTrusted: d.AddrTrusted}
	if d.Addr != "" {
		a, err := hex.DecodeString(d.Addr)
		if err != nil || len(a) != len(id.Addr) {
			return id, fmt.Errorf("address %q", d.Addr)
		}
		copy(id.Addr[:], a)
	}
	if id.Key() != d.Key {
		return id, fmt.Errorf("key %q does not match its fields (%s)", d.Key, id.Key())
	}
	return id, nil
}

type hexBytes []byte

func (h hexBytes) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(h)), nil }

func (h *hexBytes) UnmarshalText(b []byte) error {
	out, err := hex.DecodeString(string(b))
	*h = out
	return err
}

// Run is one plan as the journal recorded it.
type Run struct {
	ID      string
	Kind    RunKind
	Of      string // the run a revert or recovery works on
	Device  plan.Identity
	Profile *byte
	Started time.Time
	Ops     []OpRecord
	// Truncated means the process died while journaling the run's ops, so no
	// packet of it was sent.
	Truncated bool
	Ended     bool // an end entry was written
	Complete  bool // every op was verified
	Err       string
	Resolved  Strategy // how recovery settled the run; 0 when it did not
	By        string   // the recovery run that did it
	// Recoveries are the unfinished recovery runs of this run; they are
	// settled with it.
	Recoveries []*Run
	// Kept are, for a run settled with Leave, the ops whose bytes its
	// extents held then.
	Kept []int
	// Reset is what a factory reset run recorded; nil for other kinds.
	Reset *ResetRecord
	nops  int
}

// OpRecord is one op of a run and the last state journaled for it.
type OpRecord struct {
	plan.Op
	State State
	Class Class  // StateFailed: what its extent held after the failure
	Found []byte // StateFailed: the bytes read back, when they could be
	Err   string
}

// Open reports whether the run still needs recovery: it neither completed nor
// was settled, and a packet of it may have gone out. An op is journaled as
// sending before its first packet, so a run whose ops are all planned sent
// nothing.
func (r *Run) Open() bool {
	return !r.Truncated && !r.Complete && r.Resolved == 0 &&
		slices.ContainsFunc(r.Ops, func(o OpRecord) bool { return o.State != StatePlanned })
}

// Matches checks that the device and its active profile are the ones the run
// was written to (I11).
func (r *Run) Matches(dev plan.Identity, profile *byte) error {
	if r.Device.Key() != dev.Key() {
		return fmt.Errorf("%w: run %s was written to %s, this is %s", ErrIdentity, r.ID, r.Device.Key(), dev.Key())
	}
	if !sameProfile(r.Profile, profile) {
		return fmt.Errorf("%w: run %s was written to profile %s, the active one is %s", ErrProfile, r.ID, profileString(r.Profile), profileString(profile))
	}
	return nil
}

// effective lists the ops whose bytes the device holds because of the run:
// all of them once the run completed or was finished by recovery, the ones
// the inspection found when it was left, the verified ones otherwise.
func (r *Run) effective() []plan.Op {
	var out []plan.Op
	for _, o := range r.Ops {
		switch {
		case r.Resolved == Back:
		case r.Resolved == Leave:
			if slices.Contains(r.Kept, o.Seq) {
				out = append(out, o.Op)
			}
		case r.Complete || r.Resolved == Forward || o.State == StateVerified:
			out = append(out, o.Op)
		}
	}
	return out
}

func sameProfile(a, b *byte) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func profileString(p *byte) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(int(*p))
}

// Status is a device's journal: every run, the ones still open and the one a
// revert would undo.
type Status struct {
	Runs []*Run // in the order they were started
	// Open are the runs that need recovery, oldest first. Unfinished recovery
	// runs are not listed on their own: they are in Recoveries of the run
	// they work on.
	Open []*Run
	// Last is the newest apply or revert run that changed the device, or
	// nil. A factory reset whose packet may have gone out ends what the
	// journal can undo: no run before it is Last.
	Last *Run
	// Unsettled are the factory reset runs whose packet may have gone out
	// and that have no end entry: the process ended before the reload that
	// says what the reset did. A session settles them once it has loaded
	// the mouse (EndReset).
	Unsettled []*Run
}

// Clean reports whether nothing needs recovery or a check.
func (s *Status) Clean() bool { return len(s.Open) == 0 && len(s.Unsettled) == 0 }

// Find returns the run with the given ID, or nil.
func (s *Status) Find(id string) *Run {
	for _, r := range s.Runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Load reads every journal file in dir, a device's journal folder. A torn
// last line in a file is the tail of an append the process did not finish and
// is ignored; any other unreadable line makes the journal corrupt.
func Load(dir string) (*Status, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*"+fileExt))
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	p := parser{key: filepath.Base(dir), runs: map[string]*Run{}}
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrJournal, err)
		}
		if err := p.file(filepath.Base(name), b); err != nil {
			return nil, err
		}
	}
	return p.status()
}

type parser struct {
	key      string
	runs     map[string]*Run
	order    []*Run
	resolves []resolution
}

// resolution is a resolve entry. It may name a run of any file, so it is
// applied once every file is read.
type resolution struct {
	run, by, where string
	how            Strategy
	kept           []int
}

func (p *parser) file(name string, b []byte) error {
	lines := bytes.Split(b, []byte{'\n'})
	lines = lines[:len(lines)-1]
	for i, line := range lines {
		where := name + " line " + strconv.Itoa(i+1)
		if err := p.line(line, where); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrCorrupt, where, err)
		}
	}
	return nil
}

func (p *parser) line(line []byte, where string) error {
	var e entry
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return err
	}
	switch e.Type {
	case typeRun:
		return p.run(e)
	case typeResolve:
		how, ok := parseName(e.Result, strategyNames[:], Forward)
		if !ok {
			return fmt.Errorf("resolution %q", e.Result)
		}
		p.resolves = append(p.resolves, resolution{run: e.Run, by: e.By, where: where, how: how, kept: e.Kept})
		return nil
	}
	r, ok := p.runs[e.Run]
	if !ok {
		return fmt.Errorf("%s entry for unknown run %q", e.Type, e.Run)
	}
	switch e.Type {
	case typeOp:
		return p.op(r, e)
	case typeSend:
		return p.send(r, e)
	case typeEnd:
		r.Ended = true
		r.Complete = e.Result == resultComplete
		r.Err = e.Error
		if r.Complete && slices.ContainsFunc(r.Ops, func(o OpRecord) bool { return o.State != StateVerified }) {
			return fmt.Errorf("run %s ended complete with an op not verified", r.ID)
		}
		if r.Reset != nil {
			return p.resetEnd(r, e)
		}
	default:
		return fmt.Errorf("entry type %q", e.Type)
	}
	return nil
}

func (p *parser) run(e entry) error {
	switch {
	case e.V != journalVersion:
		return fmt.Errorf("version %d", e.V)
	case e.Run == "" || p.runs[e.Run] != nil:
		return fmt.Errorf("run id %q is empty or repeated", e.Run)
	case e.Device == nil:
		return errors.New("run without a device")
	}
	kind, ok := parseName(e.Kind, kindNames[:], KindApply)
	switch {
	case !ok:
		return fmt.Errorf("run kind %q", e.Kind)
	case kind == KindReset && (e.NOps != 0 || e.Backup == "" || len(e.Packet) != wire.Size):
		return errors.New("reset run without its backup and packet, or with ops")
	case kind != KindReset && e.NOps < 1:
		return errors.New("run without ops")
	case kind != KindApply && kind != KindReset && e.Of == "":
		return fmt.Errorf("%s run without the run it works on", kind)
	}
	id, err := e.Device.identity()
	if err != nil {
		return err
	}
	if id.Key() != p.key {
		return fmt.Errorf("run for device %s in the journal of %s", id.Key(), p.key)
	}
	r := &Run{ID: e.Run, Kind: kind, Of: e.Of, Device: id, Profile: e.Profile, Started: e.Time, nops: e.NOps}
	if kind == KindReset {
		r.Reset = &ResetRecord{Backup: e.Backup, Packet: wire.Packet(e.Packet)}
	}
	p.runs[r.ID] = r
	p.order = append(p.order, r)
	return nil
}

func (p *parser) op(r *Run, e entry) error {
	s, ok := parseName(e.State, stateNames[:], StatePlanned)
	if !ok {
		return fmt.Errorf("op state %q", e.State)
	}
	if s == StatePlanned {
		op, err := plannedOp(e)
		if err != nil {
			return err
		}
		if op.Seq != len(r.Ops)+1 || op.Seq > r.nops {
			return fmt.Errorf("run %s: planned op %d out of order", r.ID, op.Seq)
		}
		r.Ops = append(r.Ops, OpRecord{Op: op, State: s})
		return nil
	}
	if e.Seq < 1 || e.Seq > len(r.Ops) || len(r.Ops) != r.nops {
		return fmt.Errorf("run %s: state for op %d before every op was planned", r.ID, e.Seq)
	}
	o := &r.Ops[e.Seq-1]
	o.State, o.Err = s, e.Error
	if s == StateFailed {
		o.Found = e.Found
		if o.Class, ok = parseClass(e.Class); !ok && e.Class != "" {
			return fmt.Errorf("class %q", e.Class)
		}
	}
	return nil
}

func plannedOp(e entry) (plan.Op, error) {
	phases := []string{""}
	for p := plan.Neutralise; p <= plan.Captured; p++ {
		phases = append(phases, p.String())
	}
	phase, ok := parseName(e.Phase, phases, plan.Neutralise)
	if !ok {
		return plan.Op{}, fmt.Errorf("phase %q", e.Phase)
	}
	var tiers []string
	for t := catalog.Off; t <= catalog.Verified; t++ {
		tiers = append(tiers, t.String())
	}
	tier, ok := parseName(e.Tier, tiers, catalog.Off)
	if !ok {
		return plan.Op{}, fmt.Errorf("tier %q", e.Tier)
	}
	if e.Extent == nil || e.Extent.Len < 1 || len(e.Old) != e.Extent.Len || len(e.New) != e.Extent.Len {
		return plan.Op{}, errors.New("planned op without whole extent, old and new bytes")
	}
	return plan.Op{Seq: e.Seq, Extent: *e.Extent, Old: e.Old, New: e.New, Phase: plan.Phase(phase), Desc: e.Desc, Tier: tier}, nil
}

func (p *parser) status() (*Status, error) {
	for _, res := range p.resolves {
		r, ok := p.runs[res.run]
		if !ok {
			return nil, fmt.Errorf("%w: %s: resolve entry for unknown run %q", ErrCorrupt, res.where, res.run)
		}
		r.Resolved, r.By, r.Kept = res.how, res.by, res.kept
	}
	st := &Status{Runs: p.order}
	for _, r := range p.order {
		r.Truncated = len(r.Ops) < r.nops
	}
	for _, r := range p.order {
		if !r.Open() {
			continue
		}
		if r.Kind == KindRecover {
			if t := p.runs[r.Of]; t != nil && t.Open() {
				t.Recoveries = append(t.Recoveries, r)
			}
			continue
		}
		st.Open = append(st.Open, r)
	}
	for _, r := range p.order {
		if r.Kind == KindReset && r.Reset.Sending && !r.Ended {
			st.Unsettled = append(st.Unsettled, r)
		}
	}
	for _, r := range slices.Backward(p.order) {
		if r.Kind == KindReset && r.Reset.Sending {
			break
		}
		if r.Kind != KindRecover && r.Kind != KindReset && !r.Open() && len(r.effective()) > 0 {
			st.Last = r
			break
		}
	}
	return st, nil
}
