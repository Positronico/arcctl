package safety_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// opKind is one shape of plan the fault matrix writes (PLAN §10 item 6).
type opKind struct {
	name    string
	seed    func(testing.TB, *flash.Image)
	changes func(testing.TB, *catalog.Model) []plan.Change
}

// Slots 12 to 15 are Disable in the dump, so their bodies are inert.
var matrixKinds = []opKind{
	{"pair", nil, func(t testing.TB, _ *catalog.Model) []plan.Change { return pairChange(t) }},
	{"record", nil, dpiChange},
	{"shortcut 2 chunks", nil, shortcutBody(13, 3)},
	{"shortcut 3 chunks", nil, shortcutBody(14, 4)},
	{"shortcut 4 chunks", nil, shortcutBody(15, 5)},
	{"macro 39 chunks", nil, macroBody(15, 70)},
	{"macro and binding", nil, func(t testing.TB, _ *catalog.Model) []plan.Change { return macroChanges(t, 3, "new", 70) }},
	{"shortcut trio", nil, func(t testing.TB, _ *catalog.Model) []plan.Change { return shortcutChange(t) }},
	{"macro trio", boundMacro, func(t testing.TB, _ *catalog.Model) []plan.Change { return macroChanges(t, 5, "new", 70) }},
}

func kindNamed(t testing.TB, name string) opKind {
	t.Helper()
	i := slices.IndexFunc(matrixKinds, func(k opKind) bool { return k.name == name })
	if i < 0 {
		t.Fatalf("no op kind %q", name)
	}
	return matrixKinds[i]
}

// shortcutBody writes a combo of n strokes into the body of an unbound slot:
// 3 strokes take 2 chunks, 4 take 3 and 5 take 4.
func shortcutBody(slot, n int) func(testing.TB, *catalog.Model) []plan.Change {
	return func(t testing.TB, _ *catalog.Model) []plan.Change {
		mods := keys.Combo{keys.LCtrl.Stroke(), keys.LShift.Stroke(), keys.LAlt.Stroke(), keys.LMeta.Stroke()}
		body := must[flash.Record](t)(mouse.EncodeShortcut(append(mods[:n-1:n-1], key(0x2B))))
		return []plan.Change{{Addr: extentOf(t, mouse.ShortcutExtent, slot).Addr, New: body, Tier: catalog.Untested,
			Desc: fmt.Sprintf("shortcut %d: %d strokes", slot, n)}}
	}
}

// macroBody writes a macro of n events into the body of an unbound slot; 70
// events fill 39 chunks.
func macroBody(slot, n int) func(testing.TB, *catalog.Model) []plan.Change {
	return func(t testing.TB, _ *catalog.Model) []plan.Change {
		body := must[[]byte](t)(mouse.EncodeMacro(macro("body", n)))
		return []plan.Change{{Addr: extentOf(t, mouse.MacroExtent, slot).Addr, New: body, Tier: catalog.Untested,
			Desc: fmt.Sprintf("macro %d: %d events", slot, n)}}
	}
}

// chunkOp locates the k-th cmd 7 (from 1) of p: the index of its op and its
// chunk within that op (from 1).
func chunkOp(p plan.Plan, k int) (op, chunk int) {
	for i, o := range p.Ops {
		n := (o.Extent.Len + wire.MaxData - 1) / wire.MaxData
		if k <= n {
			return i, k
		}
		k -= n
	}
	return len(p.Ops), 0
}

// sampleChunks is every chunk of p when all is set, else the first two, the
// middle and the last two chunks of each op.
func sampleChunks(p plan.Plan, all bool) []int {
	var out []int
	base := 0
	for _, o := range p.Ops {
		n := (o.Extent.Len + wire.MaxData - 1) / wire.MaxData
		for c := 1; c <= n; c++ {
			if all || c <= 2 || c >= n-1 || c == (n+1)/2 {
				out = append(out, base+c)
			}
		}
		base += n
	}
	return out
}

// sameImage fails unless got and want hold the same 16 KiB, naming the first
// bytes that differ.
func sameImage(t testing.TB, what string, got, want *flash.Image) {
	t.Helper()
	if d := diffBytes(got, want); len(d) > 0 {
		t.Fatalf("%s differs from the expected image at %v", what, d)
	}
}

func diffBytes(got, want *flash.Image) []string {
	a, b := got.Bytes(), want.Bytes()
	var out []string
	for i := range a {
		if a[i] != b[i] {
			out = append(out, fmt.Sprintf("%d: %02x, want %02x", i, a[i], b[i]))
			if len(out) == 8 {
				break
			}
		}
	}
	return out
}

// checkRecords fails when a record p writes does not hold a valid value: a
// pair or a 4-byte record whose checksum is wrong, a binding that does not
// decode, or a body that is neither empty nor valid.
func checkRecords(t testing.TB, m *catalog.Model, im *flash.Image, p plan.Plan) {
	t.Helper()
	c := mouse.Decode(m, im)
	for _, op := range p.Ops {
		b, _ := im.Get(op.Extent)
		ok := true
		switch slot, kind := slotOf(op.Extent); kind {
		case "binding":
			_, err := mouse.DecodeKeyFn(b)
			ok = err == nil
		case "shortcut":
			ok = c.ShortcutClass[slot] != flash.SlotInvalid
		case "macro":
			ok = c.MacroClass[slot] != flash.SlotInvalid
		default:
			ok = flash.Record(b).Verify()
		}
		if !ok {
			t.Fatalf("%s holds % x, which is not a valid record", op.Extent, b)
		}
	}
}

// slotOf names the table an op's extent belongs to.
func slotOf(e flash.Extent) (int, string) {
	for k := range mouse.Slots {
		switch {
		case e == must2(mouse.KeyFnExtent(k)):
			return k, "binding"
		case e.Addr == must2(mouse.ShortcutExtent(k)).Addr:
			return k, "shortcut"
		case e.Addr == must2(mouse.MacroExtent(k)).Addr:
			return k, "macro"
		}
	}
	return -1, "record"
}

func must2(e flash.Extent, _ bool) flash.Extent { return e }

// checkBound fails when a binding runs a body that is not valid: a shortcut
// binding its own slot's shortcut, a macro binding its own slot's macro and
// the one its byte 1 names (PLAN §6.2). A binding in skip may hold bytes that
// do not decode, as a write the device garbled leaves it.
func checkBound(t testing.TB, m *catalog.Model, im *flash.Image, skip ...flash.Extent) {
	t.Helper()
	c := mouse.Decode(m, im)
	for k, b := range c.Keys {
		if !b.Field.State.Valid() {
			if slices.Contains(skip, must2(mouse.KeyFnExtent(k))) {
				continue
			}
			t.Fatalf("binding %d holds % x: %v", k, b.Field.Raw, b.Field.Err)
		}
		switch b.Fn.Type {
		case mouse.TypeShortcut:
			if c.ShortcutClass[k] != flash.SlotValid {
				t.Fatalf("binding %d runs shortcut %d, which is %v", k, k, c.ShortcutClass[k])
			}
		case mouse.TypeMacro:
			for _, s := range []int{k, int(b.Fn.Param >> 8)} {
				if s >= mouse.Slots || c.MacroClass[s] != flash.SlotValid {
					t.Fatalf("binding %d (% x) runs macro %d, which is not valid", k, b.Field.Raw, s)
				}
			}
		}
	}
}

// checkMidway fails when an extent of p, other than the one of the op the
// run stopped in and those in skip, holds bytes no op of p leaves there.
func checkMidway(t testing.TB, im *flash.Image, p plan.Plan, stopped int, skip ...flash.Extent) {
	t.Helper()
	for _, op := range p.Ops {
		if stopped >= 0 && stopped < len(p.Ops) && op.Extent == p.Ops[stopped].Extent || slices.Contains(skip, op.Extent) {
			continue
		}
		found, _ := im.Get(op.Extent)
		if !slices.ContainsFunc(p.Ops, func(o plan.Op) bool {
			return o.Extent == op.Extent && (bytes.Equal(found, o.Old) || bytes.Equal(found, o.New))
		}) {
			t.Fatalf("extent %s holds % x, which no op left there", op.Extent, found)
		}
	}
}

// garbled is the extent of the stopped op, whose binding may not decode
// after a fault that garbles writes.
func garbled(garbles bool, p plan.Plan, stopped int) []flash.Extent {
	if !garbles || stopped < 0 || stopped >= len(p.Ops) {
		return nil
	}
	return []flash.Extent{p.Ops[stopped].Extent}
}

// chunksSent lists the chunks of p (from 1) that reached the device.
func chunksSent(ws []emu.Write, p plan.Plan) []int {
	var out []int
	for k := 1; k <= chunksOf(p); k++ {
		c := chunkPacket(p, k, false)
		if slices.ContainsFunc(ws, func(w emu.Write) bool { return w.Packet == c }) {
			out = append(out, k)
		}
	}
	return out
}

// noChunkAfter fails when a chunk of p after the k-th reached the device.
func noChunkAfter(t testing.TB, ws []emu.Write, p plan.Plan, k int) {
	t.Helper()
	if s := chunksSent(ws, p); len(s) > 0 && s[len(s)-1] > k {
		t.Fatalf("chunk %d reached the device; the run had to stop by chunk %d (sent %v)", s[len(s)-1], k, s)
	}
}

// checkFreshOnline fails when the first chunk of an op reached the device
// with no cmd 3 since the cmd 7 before it (I7).
func checkFreshOnline(t testing.TB, ws []emu.Write, p plan.Plan) {
	t.Helper()
	k := 1
	for _, op := range p.Ops {
		first := chunkPacket(p, k, false)
		k += (op.Extent.Len + wire.MaxData - 1) / wire.MaxData
		i := slices.IndexFunc(ws, func(w emu.Write) bool { return w.Packet == first })
		fresh := i < 0
		for j := i - 1; j >= 0 && !fresh && ws[j].Packet.Cmd() != wire.CmdWrite; j-- {
			fresh = ws[j].Packet.Cmd() == wire.CmdOnline
		}
		if !fresh {
			t.Fatalf("op %d's first chunk went out with no cmd 3 since the cmd 7 before it", op.Seq)
		}
	}
}

// checkPackets fails when a packet that reached the device is one the Edit
// policy refuses, or a cmd 7 falls outside p's extents.
func checkPackets(t testing.TB, ws []emu.Write, p plan.Plan) {
	t.Helper()
	for _, w := range ws {
		if err := wire.Edit.Check(w.Packet, wire.Mouse); err != nil {
			t.Fatalf("packet %v reached the device: %v", w.Packet, err)
		}
		if w.Packet.Cmd() != wire.CmdWrite {
			continue
		}
		e := flash.Extent{Addr: int(w.Packet.Addr()), Len: w.Packet.Len()}
		if !slices.ContainsFunc(p.Ops, func(o plan.Op) bool { return o.Extent.Contains(e) }) {
			t.Fatalf("cmd 7 to %s is outside the plan", e)
		}
	}
}

// srig is an emulated EM11 Pro driven through the session, the way arcctl
// writes: its journal and backup folders outlive the sessions a test starts,
// as they outlive arcctl processes.
type srig struct {
	t      *testing.T
	bus    *emu.Bus
	dev    *emu.Device
	model  *catalog.Model
	writes session.Writes
	start  *flash.Image
	hang   hangWatch

	s    *session.Session
	stop func()
}

func newSRig(t *testing.T, seed func(testing.TB, *flash.Image), o emu.Options, bh emu.Behavior) *srig {
	t.Helper()
	m, ok := catalog.ByKey("7B04")
	if !ok {
		t.Fatal("no EM11 Pro in the catalog")
	}
	im := dumpImage(t)
	if seed != nil {
		seed(t, im)
	}
	b := emu.New(o)
	t.Cleanup(b.Close)
	d, err := b.Add(emu.Config{
		RxVersion: &emu.Version{Major: 1, Minor: 2},
		Behavior:  bh,
		Mouse: &emu.Mouse{
			Model: m, Image: im, Firmware: emu.Version{Major: 1, Minor: 5},
			Battery: emu.Battery{Level: 80, MilliVolts: 3900}, Profile: ptr(byte(0)), LongRange: ptr(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &srig{t: t, bus: b, dev: d, model: m, start: d.Image()}
	r.writes = session.Writes{
		Journal:  t.TempDir(),
		Backups:  backup.Store{Root: t.TempDir(), Tool: "arcctl test", Source: backup.SourceEmulator},
		Lock:     func() error { return nil },
		Executor: safety.Options{OfflineWait: time.Second, LockWait: time.Second, Poll: 5 * time.Millisecond},
	}
	return r
}

func sessionTiming() session.Timing {
	return session.Timing{
		Try:           40 * time.Millisecond,
		ProbeTry:      25 * time.Millisecond,
		Window:        time.Second,
		Debounce:      5 * time.Millisecond,
		Rescan:        20 * time.Millisecond,
		RescanMax:     50 * time.Millisecond,
		Retry:         20 * time.Millisecond,
		RetryMax:      50 * time.Millisecond,
		Offline:       40 * time.Millisecond,
		Online:        time.Hour,
		Battery:       time.Hour,
		Suspect:       30 * time.Millisecond,
		ConflictQuiet: 100 * time.Millisecond,
		LoadWatchdog:  10 * time.Second,
	}
}

// run starts a session, as starting arcctl does; the previous one must have
// stopped.
func (r *srig) run() (*session.Session, context.CancelFunc) {
	r.t.Helper()
	w := r.writes
	s := session.New(session.Options{Devices: watchedBus{r.bus, &r.hang}, Clients: r.clients, Timing: sessionTiming(), Writes: &w})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != context.Canceled {
				r.t.Errorf("Run = %v, want context.Canceled", err)
			}
		})
	}
	r.t.Cleanup(stop)
	r.s, r.stop = s, stop
	return s, cancel
}

// hangWatch is the write watchdog of a stall case: limit on the write the
// case makes hang, and hidio's default, as on a real device, on every other
// write, so that a write a busy machine slows down never counts as a second
// stall.
type hangWatch struct {
	mu    sync.Mutex
	match func(wire.Packet) bool
	limit time.Duration
}

// arm puts limit on the write match picks; match sees every write of the
// transports w watches, in order.
func (w *hangWatch) arm(match func(wire.Packet) bool, limit time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.match, w.limit = match, limit
}

func (w *hangWatch) watch(tr hidio.Transport) hidio.Transport { return &watched{Transport: tr, w: w} }

// watched sends the write the watch picks, and every later one, through a
// pipe with the watch's limit, so that hidio's own write path times it and
// stays stalled after it.
type watched struct {
	hidio.Transport
	w    *hangWatch
	pipe *hidio.Pipe
}

func (t *watched) Write(p wire.Packet) error {
	if pipe := t.w.pipeFor(t, p); pipe != nil {
		return pipe.WriteRawOnce(p)
	}
	return t.Transport.Write(p)
}

func (w *hangWatch) pipeFor(t *watched, p wire.Packet) *hidio.Pipe {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.pipe == nil && w.match != nil && w.match(p) {
		t.pipe = hidio.NewPipe(t.Transport.Write)
		t.pipe.SetWatchdog(w.limit)
	}
	return t.pipe
}

// watchedBus opens the bus's devices under a hangWatch.
type watchedBus struct {
	*emu.Bus
	hang *hangWatch
}

func (b watchedBus) Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error) {
	tr, err := b.Bus.Open(c, g, rec)
	if err != nil {
		return nil, err
	}
	return b.hang.watch(tr), nil
}

func (r *srig) clients(c hidio.Candidate) ([]session.Client, error) {
	var out []session.Client
	for _, x := range r.bus.Clients() {
		if x.Path == c.Path && x.PID != os.Getpid() {
			out = append(out, session.Client{PID: x.PID, Name: x.Process, Seized: x.Seized})
		}
	}
	return out, nil
}

func (r *srig) await(what string, pred func(*session.Snapshot) bool) *session.Snapshot {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sn, err := session.Await(ctx, r.s, pred)
	if err != nil {
		r.t.Fatalf("waiting for %s: %v (state %v, link %v, err %v, progress %+v, journal %+v)",
			what, err, sn.State, sn.Link, sn.Err, sn.Progress, sn.Journal)
	}
	return sn
}

// settled waits until the session checked the journal again after it
// showed before, with the device loaded and every open run inspected.
func (r *srig) settled(before *session.JournalState) *session.Snapshot {
	r.t.Helper()
	return r.await("the journal checked again", func(sn *session.Snapshot) bool {
		js := sn.Journal
		if js == nil || js == before || js.Err != nil || sn.Link != session.Ready || sn.Progress.Job != "" {
			return false
		}
		if sn.State != session.Ready && sn.State != session.Recovering {
			return false
		}
		return !slices.ContainsFunc(js.Open, func(o session.OpenRun) bool { return o.Err != nil || o.Inspection == nil })
	})
}

// plan builds a plan from changes over the device's whole image, with the
// identity and profile the session loaded.
func (r *srig) plan(sn *session.Snapshot, changes []plan.Change) plan.Plan {
	r.t.Helper()
	im := must[*flash.Image](r.t)(flash.FromDump(0, r.dev.Image().Bytes()))
	var profile *byte
	if sn.Profile.Supported {
		profile = ptr(sn.Profile.Value)
	}
	p, err := plan.New(sn.Identity, profile, im, mouse.Layout(r.model), changes)
	if err != nil {
		r.t.Fatal(err)
	}
	if len(p.Ops) == 0 {
		r.t.Fatal("the plan writes nothing")
	}
	return p
}

func (r *srig) journal(id plan.Identity) *safety.Status {
	r.t.Helper()
	j, err := safety.OpenJournal(r.writes.Journal, id)
	if err != nil {
		r.t.Fatal(err)
	}
	defer j.Close()
	st, err := j.Status()
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

// competitor opens the receiver the way a browser tab running the vendor's
// web app does, and polls once.
func (r *srig) competitor() *emu.Competitor {
	c := r.dev.Chrome(0)
	r.t.Cleanup(c.Close)
	c.Poll()
	return c
}

// clearConflict closes what a competitor left: a Conflict needs the user to
// clear it once the device is quiet, a SuspectedConflict clears itself.
func (r *srig) clearConflict() {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sn := r.s.Snapshot()
		if sn.State != session.Conflict && sn.State != session.SuspectedConflict {
			return
		}
		if sn.State == session.Conflict {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := r.s.ClearConflict(ctx)
			cancel()
			if err != nil && !errors.Is(err, session.ErrConflict) {
				r.t.Fatalf("clear conflict: %v", err)
			}
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("the session stays %v", sn.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func allowAll(p plan.Plan) safety.Gates {
	return safety.Gates{AllowUntested: true, Experimental: true, Confirm: safety.ConfirmPhrase(p.Ops)}
}

func cmdCount(ws []emu.Write, c wire.Cmd) int {
	n := 0
	for _, w := range ws {
		if w.Packet.Cmd() == c {
			n++
		}
	}
	return n
}
