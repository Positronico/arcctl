//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// H0 limits: how long the mouse may take to fall asleep, and the receiver to
// be seen gone after an unplug.
const (
	sleepWait   = 5 * time.Minute
	unplugWait  = 15 * time.Second
	latencyRuns = 200
	loadRuns    = 20
	cycleRuns   = 50
	probeTry    = 150 * time.Millisecond
	closeLimit  = 2 * time.Second
	confirmWord = "confirm H0"
)

// latencyExtent is read for the latency figures: a whole packet of the
// binding table that no push re-reads and no load chunk starts at.
var latencyExtent = flash.Extent{Addr: 0x60, Len: 10}

// h0 is the read-only stage. It never enables writes: its sessions have no
// Writes, and the raw path only sends cmd 3 and cmd 8.
type h0 struct {
	r    *runner
	main *conn
	path string
	full [2]*session.Capture

	identical, dumpOK bool
	// measured is set once the latency or the load step measured reads, and
	// lost counts those lost beyond the retries.
	measured bool
	lost     int
}

// h0Step is one step of H0; its name is what --steps takes.
type h0Step struct {
	stepInfo
	run func(*h0, context.Context) (string, bool, []string, error)
}

// The titles stay as the first runs logged them: the log's status of H0
// matches the steps of older entries by title.
var h0Steps = []h0Step{
	{stepInfo{name: "doctor", title: "doctor"}, (*h0).doctor},
	{stepInfo{name: "trace", title: "trace: DPI button, sleep and wake, screen lock"}, (*h0).trace},
	{stepInfo{name: "info", title: "info"}, (*h0).info},
	{stepInfo{name: "latency", title: "latency of cmd-8 reads"}, (*h0).latency},
	{stepInfo{name: "loads", title: "working loads while the mouse moves"}, (*h0).loads},
	{stepInfo{name: "backups", title: "two full backups"}, (*h0).backups},
	{stepInfo{name: "dump", title: "0..256 against flash-dump.bin", needs: "backups"}, (*h0).dump},
	{stepInfo{name: "interfaces", title: "interfaces that answer"}, (*h0).interfaces},
	{stepInfo{name: "cycles", title: "open and close cycles"}, (*h0).cycles},
	{stepInfo{name: "sleep-wake", title: "address across sleep and wake"}, (*h0).sleepWake},
	{stepInfo{name: "unplug", title: "unplug while idle, address across a replug"}, (*h0).unplug},
	{stepInfo{name: "coexist", title: "coexistence with the web app"}, (*h0).coexist},
	{stepInfo{name: "cable", title: "USB-C cable"}, (*h0).cable},
	{stepInfo{name: "typed", title: "typed confirmation and Secure Input"}, (*h0).typed},
	{stepInfo{name: "revoke", title: "Input Monitoring revoked"}, (*h0).revoke},
}

func h0Infos() []stepInfo {
	out := make([]stepInfo, len(h0Steps))
	for i, s := range h0Steps {
		out[i] = s.stepInfo
	}
	return out
}

func runH0(ctx context.Context, r *runner) error {
	h := &h0{r: r}
	defer h.stop()
	sel := r.res.Selected
	steps := slices.DeleteFunc(slices.Clone(h0Steps), func(s h0Step) bool { return len(sel) > 0 && !slices.Contains(sel, s.name) })
	what := "It asks you to press buttons, let the mouse sleep, unplug the receiver, open the web app and plug in the USB-C cable."
	if len(sel) > 0 {
		what = "This run takes only its steps " + strings.Join(sel, ", ") + "."
	}
	ok, err := r.ask("h0.run", "Stage H0 writes nothing. "+what+" Start?", true)
	switch {
	case err != nil:
		return err
	case !ok:
		return ErrDeclined
	}
	r.begun = true
	if len(sel) > 0 && !slices.Contains(sel, "info") {
		if _, _, err := h.session(ctx); err != nil {
			return err
		}
		h.stop()
	}
	for _, s := range steps {
		r.say("\n" + s.title)
		r.note("step: " + s.title)
		finding, ok, detail, err := s.run(h, ctx)
		stop := err != nil && (errors.Is(err, ErrNoInput) || ctx.Err() != nil)
		switch {
		case stop:
			detail = []string{err.Error()}
			ok = false
		case err != nil:
			detail = append(detail, err.Error())
			ok = false
		}
		r.step(s.title, ok, detail...)
		r.res.Steps[len(r.res.Steps)-1].Name = s.name
		if stop {
			return err
		}
		if finding != "" {
			r.finding("%s", finding)
		}
	}
	ran := func(name string) bool { return len(sel) == 0 || slices.Contains(sel, name) }
	crit := func(v bool, names ...string) string {
		if !slices.ContainsFunc(names, ran) {
			return "not run"
		}
		return yesNo(v)
	}
	if slices.ContainsFunc([]string{"backups", "latency", "loads"}, ran) {
		r.finding("exit criteria: two full backups identical: %s; 0..256 equals flash-dump.bin or explained: %s; no reply lost beyond the retries: %s",
			crit(h.identical, "backups"), crit(h.dumpOK, "dump"), crit(h.measured && h.lost == 0, "latency", "loads"))
	}
	if len(sel) == 0 {
		r.finding("to do by hand: update the conflict thresholds, the retry budget and the emulator's reply layouts from these findings")
	}
	return nil
}

// session returns the main session, started when needed. The first one
// names the device the stage runs on, when no info step did.
func (h *h0) session(ctx context.Context) (*conn, *session.Snapshot, error) {
	if h.main != nil {
		sn, err := h.r.ready(ctx, h.main)
		return h.main, sn, err
	}
	c, sn, err := h.r.connect(ctx, connectOpts{rec: h.r.rec.rec, device: h.path})
	if err != nil {
		return nil, sn, err
	}
	h.main = c
	if h.path == "" && sn.Device != nil {
		h.path = sn.Device.Path
	}
	if h.r.res.Device.Key == "" {
		h.r.describe(sn)
	}
	return c, sn, nil
}

func (h *h0) stop() {
	if h.main != nil {
		h.main.close()
		h.main = nil
	}
}

func (h *h0) doctor(context.Context) (string, bool, []string, error) {
	if h.r.cfg.Host == nil {
		return "", true, []string{"no OS checks in this run"}, nil
	}
	var b bytes.Buffer
	d, err := h.r.cfg.Host.Doctor(&b)
	h.r.say(strings.TrimRight(b.String(), "\n"))
	if err != nil {
		return "", false, nil, err
	}
	clients := "none"
	if len(d.Clients) > 0 {
		clients = strings.Join(d.Clients, ", ")
	}
	detail := []string{fmt.Sprintf("Input Monitoring %s, held by %s", d.Access, orNone(d.App)), "other HID clients on the receiver: " + clients}
	return "baseline HID clients on the receiver: " + clients, true, detail, nil
}

// traced is one input report the trace saw, on an interface.
type traced struct {
	iface int
	rep   hidio.Report
}

// tracer opens every interface through the raw path and keeps what arrives.
// It sends nothing.
type tracer struct {
	mu   sync.Mutex
	got  []traced
	raws []hidio.Raw
	wg   sync.WaitGroup
}

func (h *h0) startTrace() (*tracer, error) {
	cands, err := h.r.cfg.Raw.Enumerate()
	if err != nil {
		return nil, err
	}
	t := &tracer{}
	for _, c := range cands {
		raw, err := h.r.cfg.Raw.OpenRaw(c)
		if err != nil {
			h.r.say(fmt.Sprintf("    interface %d: %v", c.Interface, err))
			continue
		}
		if h.r.rec != nil {
			raw = h.r.rec.rec.Wrap(raw)
		}
		t.raws = append(t.raws, raw)
		chans := []<-chan hidio.Report{raw.Reports()}
		if o, ok := raw.(interface{ Others() <-chan hidio.Report }); ok {
			chans = append(chans, o.Others())
		}
		for _, ch := range chans {
			t.wg.Add(1)
			go func() {
				defer t.wg.Done()
				for rep := range ch {
					t.mu.Lock()
					t.got = append(t.got, traced{c.Interface, rep})
					t.mu.Unlock()
				}
			}()
		}
	}
	if len(t.raws) == 0 {
		return nil, errors.New("no interface could be opened")
	}
	return t, nil
}

// cut returns what arrived since the last cut.
func (t *tracer) cut() []traced {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.got
	t.got = nil
	return out
}

func (t *tracer) close() {
	for _, r := range t.raws {
		r.Close()
	}
	t.wg.Wait()
}

// summary names the report-8 frames and counts the other reports.
func summary(got []traced) string {
	var frames []string
	others := map[byte]int{}
	for _, g := range got {
		p, ok := g.rep.Packet()
		if !ok {
			others[g.rep.ID]++
			continue
		}
		switch p.Cmd() {
		case wire.CmdStatusChanged:
			frames = append(frames, fmt.Sprintf("cmd 10 flags %02x %02x on interface %d", p[5], p[6], g.iface))
		case wire.CmdOnline:
			frames = append(frames, fmt.Sprintf("cmd 3 online=%d on interface %d", p[5], g.iface))
		default:
			frames = append(frames, fmt.Sprintf("%v status %d on interface %d", p.Cmd(), p.Status(), g.iface))
		}
	}
	var parts []string
	if len(frames) == 0 {
		parts = append(parts, "no report-8 frame")
	} else {
		parts = append(parts, strings.Join(frames, "; "))
	}
	if len(others) > 0 {
		ids := make([]int, 0, len(others))
		for id := range others {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		var s []string
		for _, id := range ids {
			s = append(s, fmt.Sprintf("id %d x%d", id, others[byte(id)]))
		}
		parts = append(parts, "other reports: "+strings.Join(s, ", "))
	}
	return strings.Join(parts, "; ")
}

// notTestable is the DPI line of a trace on a mouse whose buttons run no
// DPI function: a press sends whatever the button runs, which says nothing
// about pushes.
const notTestable = "not testable: no button is bound to a DPI function"

// dpiButton names a visible button that runs a DPI function (KeyFn type 2)
// in the loaded configuration, and the function.
func dpiButton(sn *session.Snapshot, os keys.OS) (label, action string, ok bool) {
	if sn == nil || sn.Model == nil || sn.Image == nil {
		return "", "", false
	}
	for _, x := range sn.Model.Buttons {
		if !x.Visible {
			continue
		}
		e, _ := mouse.KeyFnExtent(x.Slot)
		if cur, known := sn.Image.Get(e); !known || mouse.KeyType(cur[0]) != mouse.TypeDPI {
			continue
		}
		label = x.Label
		if label == "" {
			label = fmt.Sprintf("slot %d", x.Slot)
		}
		cfg := mouse.Decode(sn.Model, sn.Image)
		return label, backup.Action(sn.Model, &cfg, x.Slot, os), true
	}
	return "", "", false
}

func (h *h0) trace(ctx context.Context) (string, bool, []string, error) {
	_, sn, err := h.session(ctx)
	if err != nil {
		return "", false, nil, err
	}
	label, action, bound := dpiButton(sn, h.r.cfg.OS)
	h.stop()
	t, err := h.startTrace()
	if err != nil {
		return "", false, nil, err
	}
	defer t.close()
	t.cut()
	type act struct{ id, what, text string }
	var actions []act
	var detail, finding []string
	if bound {
		actions = append(actions, act{"h0.trace-dpi", fmt.Sprintf("DPI button (%s, %s)", label, action),
			fmt.Sprintf("Press the %s button (%s) once, then press Enter.", label, action)})
	} else {
		h.r.say("No button runs a DPI function, so the trace skips the DPI press.")
		detail = append(detail, "DPI button: "+notTestable)
		finding = append(finding, "DPI button -> "+notTestable)
	}
	actions = append(actions,
		act{"h0.trace-sleep", "sleep", "Leave the mouse untouched until it sleeps (a minute or two), then press Enter."},
		act{"h0.trace-wake", "wake", "Move the mouse to wake it, then press Enter."},
		act{"h0.trace-lock", "screen lock", "Lock the screen, wait a few seconds, unlock it, then press Enter."},
	)
	for _, a := range actions {
		if err := h.r.wait(a.id, a.text); err != nil {
			return "", false, detail, err
		}
		settle(ctx, h.r.cfg.Poll)
		s := summary(t.cut())
		detail = append(detail, a.what+": "+s)
		finding = append(finding, a.what+" -> "+s)
	}
	return "trace: " + strings.Join(finding, " | "), true, detail, nil
}

func (h *h0) info(ctx context.Context) (string, bool, []string, error) {
	h.stop()
	c, sn, err := h.r.connect(ctx, connectOpts{})
	if err != nil {
		return "", false, nil, err
	}
	h.path = sn.Device.Path
	c.close()
	rc, err := h.r.startRecording("h0-info")
	if err != nil {
		return "", false, nil, err
	}
	h.r.rec.extra = append(h.r.rec.extra, rc)
	c, sn, err = h.r.connect(ctx, connectOpts{rec: rc.rec, device: h.path})
	if err != nil {
		return "", false, nil, err
	}
	c.close()
	h.r.describe(sn)
	d := h.r.res.Device
	detail := []string{
		fmt.Sprintf("%s (%s), cid %02x mid %d, %s", d.Model, d.Key, sn.Identity.CID, d.MID, d.Conn),
		fmt.Sprintf("firmware: mouse %s, receiver %s", orNone(d.Mouse), orNone(d.Receiver)),
		"cmd 14 (profiles): " + probeText(sn.Profile),
		"cmd 23 (long range): " + probeText(sn.LongRange),
	}
	if b := sn.Battery; b != nil {
		detail = append(detail, fmt.Sprintf("battery: level %d, charging %v, %d mV, byte 9 set %v", b.Level, b.Charging, b.MilliVolts, b.Direct))
	}
	st := sn.Stats
	detail = append(detail, fmt.Sprintf("%d transactions; inbound checksums bad: %d; NAKs %d", st.Transactions, st.BadChecksums, st.NAKs))
	finding := fmt.Sprintf("info: mid %d, %s; cmd 14 %s; cmd 23 %s; inbound checksums bad %d of %d replies",
		d.MID, d.Conn, probeText(sn.Profile), probeText(sn.LongRange), st.BadChecksums, st.Transactions)
	return finding, true, detail, nil
}

func probeText(p session.Probe) string {
	switch {
	case !p.Asked:
		return "not asked"
	case !p.Supported:
		return "NAK"
	}
	return fmt.Sprintf("status 0, value %d", p.Value)
}

func (h *h0) latency(ctx context.Context) (string, bool, []string, error) {
	c, _, err := h.session(ctx)
	if err != nil {
		return "", false, nil, err
	}
	rp, err := h.r.rawPath(c)
	if err != nil {
		return "", false, nil, err
	}
	p, err := wire.BuildRead(wire.Mouse, uint16(latencyExtent.Addr), latencyExtent.Len)
	if err != nil {
		return "", false, nil, err
	}
	var times []time.Duration
	missed, lost := 0, 0
	for range latencyRuns {
		answered := false
		for range rawTries {
			_, d, ok, err := rp.once(ctx, p)
			if err != nil {
				return "", false, nil, err
			}
			if ok {
				times = append(times, d)
				answered = true
				break
			}
			missed++
		}
		if !answered {
			lost++
		}
	}
	h.measured, h.lost = true, h.lost+lost
	if len(times) == 0 {
		return "", false, nil, fmt.Errorf("%w: none of %d reads was answered", ErrNoReply, latencyRuns)
	}
	slices.Sort(times)
	q := func(f float64) time.Duration { return times[min(len(times)-1, int(f*float64(len(times))))] }
	s := fmt.Sprintf("%d reads of %s: p50 %s, p99 %s, max %s; %d tries unanswered, %d reads lost after %d tries",
		latencyRuns, latencyExtent, q(0.5).Round(time.Microsecond*100), q(0.99).Round(time.Microsecond*100), times[len(times)-1].Round(time.Microsecond*100), missed, lost, rawTries)
	return "latency: " + s, lost == 0, []string{s}, nil
}

func (h *h0) loads(ctx context.Context) (string, bool, []string, error) {
	c, sn, err := h.session(ctx)
	if err != nil {
		return "", false, nil, err
	}
	if err := h.r.wait("h0.move", "Keep moving the mouse, without a pause, until arcctl says stop. Press Enter to start."); err != nil {
		return "", false, nil, err
	}
	before := sn.Stats
	var times []time.Duration
	unread := 0
	for range loadRuns {
		t0 := time.Now()
		seq := c.s.Snapshot().Seq
		if err := h.r.watch(ctx, c, c.s.Reload); err != nil {
			return "", false, nil, err
		}
		times = append(times, time.Since(t0))
		sn, err := h.r.readyAfter(ctx, c, seq)
		if err != nil {
			return "", false, nil, err
		}
		if len(sn.Unread) > 0 {
			unread++
		}
	}
	h.r.say("You can stop moving the mouse.")
	after := c.s.Snapshot().Stats
	slices.Sort(times)
	s := fmt.Sprintf("%d loads: median %s, max %s; %d left bytes unread; tries unanswered %d, frames dropped %d, foreign %d",
		loadRuns, times[len(times)/2].Round(time.Millisecond), times[len(times)-1].Round(time.Millisecond), unread,
		after.FailedTries-before.FailedTries, after.Dropped-before.Dropped, after.Foreign-before.Foreign)
	h.measured, h.lost = true, h.lost+unread
	return "loads while moving: " + s, unread == 0, []string{s}, nil
}

func (h *h0) backups(ctx context.Context) (string, bool, []string, error) {
	h.stop()
	if err := h.r.wait("h0.backups", "Two full backups come next, each from a new session. Do not touch the mouse until both are done. Press Enter to start."); err != nil {
		return "", false, nil, err
	}
	var detail, took []string
	h.r.pauses = 0
	for i := range h.full {
		c, _, err := h.r.connect(ctx, connectOpts{rec: h.r.rec.rec, device: h.path})
		if err != nil {
			return "", false, detail, err
		}
		t0 := time.Now()
		var cp session.Capture
		err = h.r.watch(ctx, c, func(ctx context.Context) error {
			var err error
			cp, err = c.s.Backup(ctx, true)
			return err
		})
		d := time.Since(t0)
		c.close()
		if err != nil {
			return "", false, detail, err
		}
		if _, _, err := h.r.saveBackup(cp, fmt.Sprintf("hwtest H0 full %d", i+1)); err != nil {
			return "", false, detail, err
		}
		h.full[i] = &cp
		took = append(took, d.Round(time.Millisecond).String())
		detail = append(detail, fmt.Sprintf("backup %d: %d bytes in %s, unreadable: %s", i+1, known(cp.Image), d.Round(time.Millisecond), extents(cp.Missing)))
	}
	a, b := h.full[0], h.full[1]
	h.identical = bytes.Equal(a.Image.Bytes(), b.Image.Bytes()) && slices.Equal(a.Image.KnownExtents(), b.Image.KnownExtents()) && slices.Equal(a.Missing, b.Missing)
	detail = append(detail, "identical: "+yesNo(h.identical))
	awake := "yes"
	if h.r.pauses > 0 {
		awake = fmt.Sprintf("no, the backups paused %d times for a sleeping mouse", h.r.pauses)
	}
	detail = append(detail, "host traffic alone kept the mouse awake: "+awake)
	finding := fmt.Sprintf("full backups took %s; unreadable ranges: %s; host traffic kept the mouse awake: %s", strings.Join(took, " and "), extents(a.Missing), awake)
	return finding, h.identical, detail, nil
}

func known(im *flash.Image) int {
	n := 0
	for _, e := range im.KnownExtents() {
		n += e.Len
	}
	return n
}

func extents(es []flash.Extent) string {
	if len(es) == 0 {
		return "none"
	}
	s := make([]string, len(es))
	for i, e := range es {
		s[i] = e.String()
	}
	return strings.Join(s, ", ")
}

func (h *h0) dump(context.Context) (string, bool, []string, error) {
	if h.full[0] == nil {
		return "", false, nil, errors.New("no full backup to compare")
	}
	b, err := os.ReadFile(h.r.cfg.Dump)
	if errors.Is(err, fs.ErrNotExist) {
		return "", true, []string{"no flash-dump.bin to compare with"}, nil
	}
	if err != nil {
		return "", false, nil, err
	}
	dump, err := flash.FromDump(0, b[:min(len(b), 256)])
	if err != nil {
		return "", false, nil, err
	}
	now := h.full[0].Image
	settings := flash.Extent{Addr: 0, Len: 256}
	if !now.Known(settings) {
		return "", false, nil, errors.New("the backup lacks part of 0..256")
	}
	diffs := diffRuns(dump, now, settings)
	if len(diffs) == 0 {
		h.dumpOK = true
		return "0..256 equals flash-dump.bin", true, []string{"no difference"}, nil
	}
	var detail []string
	for _, e := range diffs {
		was, _ := dump.Get(e)
		is, _ := now.Get(e)
		detail = append(detail, fmt.Sprintf("%s: dump % x, now % x", e, was, is))
	}
	h.r.say(strings.Join(detail, "\n"))
	why, err := h.r.line("h0.dump", "0..256 differs from flash-dump.bin as shown. What explains it (settings you changed since the dump)? Type unexplained if nothing does:")
	if err != nil {
		return "", false, detail, err
	}
	h.dumpOK = why != "" && !strings.EqualFold(why, "unexplained")
	return fmt.Sprintf("0..256 differs from flash-dump.bin at %s: %s", extents(diffs), why), h.dumpOK, detail, nil
}

// probeAll sends cmd 3 to every interface, through a read-only guard, and
// lists those that answer.
func (h *h0) probeAll(ctx context.Context) ([]hidio.Candidate, []string, error) {
	cands, err := h.r.cfg.Raw.Enumerate()
	if err != nil {
		return nil, nil, err
	}
	var answered []hidio.Candidate
	var detail []string
	for _, c := range cands {
		ok, err := h.probe(ctx, c)
		state := "silent"
		switch {
		case err != nil:
			state = err.Error()
		case ok:
			state = "answers"
			answered = append(answered, c)
		}
		detail = append(detail, fmt.Sprintf("%04x:%04x interface %d: %s", c.VID, c.PID, c.Interface, state))
	}
	return answered, detail, nil
}

// open opens c through the raw path under a guard of its own, which stays
// read-only.
func (h *h0) open(c hidio.Candidate) (hidio.Transport, error) {
	raw, err := h.r.cfg.Raw.OpenRaw(c)
	if err != nil {
		return nil, err
	}
	if h.r.rec != nil {
		raw = h.r.rec.rec.Wrap(raw)
	}
	return hidio.Guarded(raw, hidio.NewGuard(wire.Mouse)), nil
}

func (h *h0) probe(ctx context.Context, c hidio.Candidate) (bool, error) {
	tr, err := h.open(c)
	if err != nil {
		return false, err
	}
	defer tr.Close()
	return ask(ctx, tr, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil), 3, probeTry)
}

// ask sends p up to tries times and reports whether a frame answered it.
func ask(ctx context.Context, tr hidio.Transport, p wire.Packet, tries int, each time.Duration) (bool, error) {
	for range tries {
		if err := tr.Write(p); err != nil {
			return false, err
		}
		t := time.NewTimer(each)
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				t.Stop()
				return false, ctx.Err()
			case <-t.C:
				waiting = false
			case rep, ok := <-tr.Reports():
				if !ok {
					t.Stop()
					return false, tr.Err()
				}
				if f, ok := rep.Packet(); ok && wire.Match(p, f) {
					t.Stop()
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (h *h0) interfaces(ctx context.Context) (string, bool, []string, error) {
	h.stop()
	answered, detail, err := h.probeAll(ctx)
	if err != nil {
		return "", false, nil, err
	}
	var which []string
	for _, c := range answered {
		which = append(which, fmt.Sprint(c.Interface))
	}
	return "interfaces answering cmd 3: " + strings.Join(which, ", "), len(answered) == 1, detail, nil
}

func (h *h0) cycles(ctx context.Context) (string, bool, []string, error) {
	h.stop()
	c, err := h.candidate()
	if err != nil {
		return "", false, nil, err
	}
	var slowest time.Duration
	failed := 0
	for range cycleRuns {
		tr, err := h.open(c)
		if err != nil {
			return "", false, nil, err
		}
		ok, err := ask(ctx, tr, wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil), 1, rawTry)
		if err != nil || !ok {
			failed++
		}
		t0 := time.Now()
		tr.Close()
		slowest = max(slowest, time.Since(t0))
	}
	tr, err := h.open(c)
	if err != nil {
		return "", false, nil, err
	}
	p, _ := wire.BuildRead(wire.Mouse, uint16(latencyExtent.Addr), latencyExtent.Len)
	werr := tr.Write(p)
	t0 := time.Now()
	tr.Close()
	during := time.Since(t0)
	ok := failed == 0 && slowest < closeLimit && during < closeLimit && werr == nil
	s := fmt.Sprintf("%d open/close cycles: %d without an answer to cmd 3, slowest close %s; a close during a read returned after %s",
		cycleRuns, failed, slowest.Round(time.Millisecond), during.Round(time.Millisecond))
	return s, ok, []string{s}, nil
}

func (h *h0) candidate() (hidio.Candidate, error) {
	cands, err := h.r.cfg.Raw.Enumerate()
	if err != nil {
		return hidio.Candidate{}, err
	}
	i := slices.IndexFunc(cands, func(c hidio.Candidate) bool { return c.Path == h.path })
	if i < 0 {
		return hidio.Candidate{}, fmt.Errorf("%w: the interface that answered is gone", ErrNotReady)
	}
	return cands[i], nil
}

// until polls the mouse's online flag through the raw path until it equals
// want, for up to limit.
func (h *h0) until(ctx context.Context, c *conn, want bool, limit time.Duration) ([3]byte, time.Duration, error) {
	t0 := time.Now()
	for {
		rp, err := h.r.rawPath(c)
		if err == nil {
			var on bool
			var addr [3]byte
			on, addr, err = rp.online(ctx)
			if err == nil && on == want {
				return addr, time.Since(t0), nil
			}
		}
		if time.Since(t0) > limit {
			return [3]byte{}, time.Since(t0), fmt.Errorf("the mouse is still %s after %s", map[bool]string{true: "asleep", false: "awake"}[want], limit)
		}
		select {
		case <-ctx.Done():
			return [3]byte{}, 0, ctx.Err()
		case <-time.After(h.r.cfg.Poll):
		}
	}
}

func (h *h0) sleepWake(ctx context.Context) (string, bool, []string, error) {
	c, _, err := h.session(ctx)
	if err != nil {
		return "", false, nil, err
	}
	a1, _, err := h.until(ctx, c, true, h.r.cfg.Wait)
	if err != nil {
		return "", false, nil, err
	}
	if err := h.r.wait("h0.sleep", "Leave the mouse untouched so that it falls asleep. Press Enter, then wait; arcctl says when it sleeps."); err != nil {
		return "", false, nil, err
	}
	_, slept, err := h.until(ctx, c, false, sleepWait)
	if err != nil {
		return "", false, nil, err
	}
	h.r.say(fmt.Sprintf("The mouse fell asleep after %s.", slept.Round(time.Second)))
	if err := h.r.wait("h0.wake", "Move the mouse to wake it, then press Enter."); err != nil {
		return "", false, nil, err
	}
	a2, _, err := h.until(ctx, c, true, h.r.cfg.Wait)
	if err != nil {
		return "", false, nil, err
	}
	same := a1 == a2
	s := fmt.Sprintf("asleep %s after the last input; cmd-3 address across sleep and wake: %s", slept.Round(time.Second), map[bool]string{true: "unchanged", false: "changed"}[same])
	return s, true, []string{s}, nil
}

// address is the receiver's cmd-3 address, which it gives whether the
// mouse is awake or not.
func (h *h0) address(ctx context.Context, c *conn) ([3]byte, bool, error) {
	rp, err := h.r.rawPath(c)
	if err != nil {
		return [3]byte{}, false, err
	}
	on, a, err := rp.online(ctx)
	return a, on, err
}

// hint is said once when the receiver is back after a replug and the mouse
// still sleeps.
const hint = "The receiver is back; the mouse is asleep. Move it."

// replug is what the session and the receiver showed after a replug.
type replug struct {
	known  bool // the receiver answered cmd 3
	addr   [3]byte
	back   time.Duration // until it answered
	asleep bool          // the mouse slept when the receiver was back
	ready  bool
	after  time.Duration // until the session was Ready again
	state  session.State
}

// attached reports whether the session has an interface open whose
// receiver answered its probe.
func attached(sn *session.Snapshot) bool {
	switch sn.Link {
	case session.Offline, session.Handshaking, session.Loading, session.Ready, session.Unknown:
		return sn.Device != nil
	}
	return false
}

// awaitReplug follows the session after the receiver was plugged back in,
// for up to Config.Wait: it asks the receiver for its cmd-3 address as soon
// as the session has it open, whether the mouse is awake or not, says the
// hint once while the mouse sleeps, and waits for Ready.
func (h *h0) awaitReplug(ctx context.Context, c *conn) (replug, error) {
	var out replug
	t0 := time.Now()
	deadline := t0.Add(h.r.cfg.Wait)
	hinted := false
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		sn := c.s.Snapshot()
		if !out.known && attached(sn) {
			if a, on, err := h.address(ctx, c); err == nil {
				out.known, out.addr, out.back, out.asleep = true, a, time.Since(t0), !on
			}
			sn = c.s.Snapshot()
		}
		if !hinted && sn.Link == session.Offline && !sn.Online {
			hinted, out.asleep = true, true
			h.r.say(hint)
			h.r.note("the receiver is back and the mouse asleep")
		}
		out.state = sn.State
		if out.known && sn.State == session.Ready && sn.Progress.Job == "" {
			out.ready, out.after = true, time.Since(t0)
			return out, nil
		}
		if time.Now().After(deadline) {
			out.after = time.Since(t0)
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-c.s.Changed():
		case <-tick.C:
		}
	}
}

// unplug passes when the session saw the receiver go and come back, the
// receiver gave its address after the replug, and the session was Ready
// again once the user moved the mouse.
func (h *h0) unplug(ctx context.Context) (string, bool, []string, error) {
	c, _, err := h.session(ctx)
	if err != nil {
		return "", false, nil, err
	}
	before, _, err := h.address(ctx, c)
	if err != nil {
		return "", false, nil, err
	}
	if err := h.r.wait("h0.unplug", "Unplug the receiver, wait five seconds, then press Enter."); err != nil {
		return "", false, nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, unplugWait)
	sn, err := session.Await(wctx, c.s, func(sn *session.Snapshot) bool { return sn.State == session.NoReceiver })
	cancel()
	gone := err == nil
	detail := []string{"the session saw the receiver go: " + yesNo(gone) + " (state " + sn.State.String() + ")"}
	if err := h.r.wait("h0.plug", "Plug the receiver back in, then press Enter."); err != nil {
		return "", false, detail, err
	}
	h.r.say("Now move the mouse to wake it; arcctl waits until the session is ready again.")
	rp, err := h.awaitReplug(ctx, c)
	if err != nil {
		return "", false, detail, err
	}
	parts := []string{"unplug while idle: seen " + yesNo(gone)}
	addr := "unknown"
	if rp.known {
		addr = map[bool]string{true: "unchanged", false: "changed"}[rp.addr == before]
		was := map[bool]string{true: "asleep", false: "awake"}[rp.asleep]
		detail = append(detail, fmt.Sprintf("the receiver answered cmd 3 %s after the Enter, the mouse %s", rp.back.Round(time.Millisecond), was))
		parts = append(parts, "address across a replug "+addr, "the mouse "+was+" after the replug")
	} else {
		detail = append(detail, fmt.Sprintf("the receiver did not answer cmd 3 within %s of the Enter", rp.after.Round(time.Second)))
		parts = append(parts, "address across a replug unknown: the receiver did not answer")
	}
	if rp.asleep {
		detail = append(detail, "the mouse slept after the replug; arcctl asked for it to be moved")
	}
	if rp.ready {
		detail = append(detail, fmt.Sprintf("back to Ready %s after the Enter", rp.after.Round(time.Millisecond)))
		parts = append(parts, "Ready again after "+rp.after.Round(time.Millisecond).String())
	} else {
		not := fmt.Sprintf("not Ready again: still %s after %s", rp.state, rp.after.Round(time.Second))
		detail = append(detail, not)
		parts = append(parts, not)
	}
	detail = append(detail, "cmd-3 address across the replug: "+addr)
	return strings.Join(parts, "; "), gone && rp.known && rp.ready, detail, nil
}

func (h *h0) coexist(ctx context.Context) (string, bool, []string, error) {
	h.stop()
	if err := h.r.wait("h0.hub", "Open the ProtoArc HUB page in Chrome and connect the mouse there. Press Enter when the page shows the mouse."); err != nil {
		return "", false, nil, err
	}
	t, err := h.startTrace()
	if err != nil {
		return "", false, nil, err
	}
	t.cut()
	settle(ctx, h.r.cfg.TraceFor)
	got := t.cut()
	t.close()
	replies := 0
	for _, g := range got {
		if p, ok := g.rep.Packet(); ok && p.Cmd() != wire.CmdStatusChanged {
			replies++
		}
	}
	detail := []string{fmt.Sprintf("in %s a listener that sends nothing saw %d replies meant for another client", h.r.cfg.TraceFor, replies)}
	named := "no scan"
	if host := h.r.cfg.Host; host != nil {
		cs, err := host.Clients()
		switch {
		case err != nil:
			named = "scan failed: " + err.Error()
		case len(cs) == 0:
			named = "nobody"
		default:
			var names []string
			for _, c := range cs {
				names = append(names, c.Name)
			}
			named = strings.Join(names, ", ")
		}
	}
	detail = append(detail, "the IORegistry scan names: "+named)
	c, sn, err := h.r.connect(ctx, connectOpts{rec: h.r.rec.rec, device: h.path})
	var state string
	switch {
	case err != nil && sn != nil:
		state = sn.State.String()
	case err != nil:
		state = err.Error()
	default:
		settle(ctx, h.r.cfg.TraceFor)
		sn = c.s.Snapshot()
		state = sn.State.String()
		c.close()
	}
	if sn != nil {
		detail = append(detail, fmt.Sprintf("a session next to it: %s; foreign replies %d, unanswered tries %d", state, sn.Stats.Foreign, sn.Stats.FailedTries))
	}
	if err := h.r.wait("h0.hub-close", "Close the HUB tab (or quit Chrome), then press Enter."); err != nil {
		return "", false, detail, err
	}
	mode := "broadcast (every client sees every reply)"
	if replies == 0 {
		mode = "not broadcast (no reply for another client reached arcctl)"
	}
	return fmt.Sprintf("with the web app connected, replies are %s; the scan names %s; a session ends up %s", mode, named, state), true, detail, nil
}

// settle gives input that is on its way time to arrive.
func settle(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (h *h0) cable(ctx context.Context) (string, bool, []string, error) {
	h.stop()
	if err := h.r.wait("h0.cable", "Connect the mouse to this computer with its USB-C cable, keep the receiver plugged in, then press Enter."); err != nil {
		return "", false, nil, err
	}
	answered, detail, err := h.probeAll(ctx)
	if err != nil {
		return "", false, nil, err
	}
	if err := h.r.wait("h0.cable-off", "Unplug the cable, then press Enter."); err != nil {
		return "", false, detail, err
	}
	return fmt.Sprintf("with the cable in, %d interfaces answer cmd 3", len(answered)), true, detail, nil
}

func (h *h0) typed(context.Context) (string, bool, []string, error) {
	if h.r.cfg.Host == nil {
		return "", true, []string{"no OS checks in this run"}, nil
	}
	got, err := h.r.line("h0.typed", fmt.Sprintf("Type %q and press Enter:", confirmWord))
	if err != nil {
		return "", false, nil, err
	}
	cons, err := h.r.cfg.Host.Console()
	if err != nil {
		return "", false, nil, err
	}
	secure := "off"
	if cons.SecureInput != "" {
		secure = "on, held by " + cons.SecureInput
	}
	ok := got == confirmWord && cons.SecureInput == ""
	detail := []string{"CLI: Secure Input after the typed confirmation: " + secure, "TUI: not built yet; repeat this check in M4"}
	return "a typed confirmation in the CLI leaves Secure Input " + secure, ok, detail, nil
}

func (h *h0) revoke(ctx context.Context) (string, bool, []string, error) {
	if h.r.cfg.Host == nil {
		return "", true, []string{"no OS checks in this run"}, nil
	}
	yes, err := h.r.cfg.Prompt.Ask("h0.revoke", "Optional: revoke the Input Monitoring grant for a moment to record the error arcctl gets?")
	if err != nil {
		return "", false, nil, err
	}
	h.r.res.Answers = append(h.r.res.Answers, Answer{ID: "h0.revoke", Question: "Revoke the Input Monitoring grant?", Answer: yesNo(yes), OK: true})
	if !yes {
		return "", true, []string{"skipped"}, nil
	}
	if err := h.r.wait("h0.revoked", "Turn the terminal app off in System Settings > Privacy & Security > Input Monitoring, then press Enter."); err != nil {
		return "", false, nil, err
	}
	perm, perr := h.r.cfg.Host.Permission()
	detail := []string{"permission: " + perm}
	if perr != nil {
		detail = append(detail, "permission check: "+perr.Error())
	}
	opened := "the interface still opens"
	if c, err := h.candidate(); err == nil {
		raw, oerr := h.r.cfg.Raw.OpenRaw(c)
		if oerr != nil {
			opened = "open fails: " + oerr.Error()
			if code, ok := hidio.IOReturn(oerr); ok {
				opened += fmt.Sprintf(" (IOReturn 0x%08X)", code)
			}
		} else {
			raw.Close()
		}
	}
	detail = append(detail, opened)
	if err := h.r.wait("h0.regrant", "Turn the grant back on (restart the terminal if macOS asks), then press Enter."); err != nil {
		return "", false, detail, err
	}
	return "without Input Monitoring: " + perm + "; " + opened, true, detail, nil
}
