package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/platform"
	"github.com/positronico/arcctl/internal/wire"
)

// runTrace opens every interface shared and prints each input report. It
// never writes: no Transport it opens is ever given a packet.
func runTrace(r *runner, args []string) error {
	fs := r.flagSet("trace", synopsis("trace"))
	dur := fs.Duration("for", time.Minute, "how long to listen")
	raw := fs.Bool("raw", false, "also print the data of keyboard, consumer and mouse reports, which show what is typed")
	if _, err := r.parse(fs, args, 0, 0); err != nil {
		return err
	}
	if *dur <= 0 {
		return usageError("trace: --for must be positive")
	}
	w, err := r.world()
	if err != nil {
		return err
	}
	defer w.close()
	if w.locks {
		if p, _ := w.host.Permission(); p.Access == platform.AccessDenied {
			return fail(ExitPermission, "device access is denied", p.Hint())
		}
		release, err := r.lock()
		if err != nil {
			return err
		}
		defer release()
	}
	cands, err := w.devices.Enumerate()
	if err != nil {
		return err
	}
	if w.device != "" {
		cands = slices.DeleteFunc(cands, func(c hidio.Candidate) bool { return c.Path != w.device })
	}
	if len(cands) == 0 {
		return fail(ExitNoReceiver, "no ProtoArc receiver or mouse found", "Plug in the receiver, or the mouse with its USB cable.")
	}
	t := &tracer{out: r.out, start: time.Now(), counts: map[string]map[byte]int{}, raw: *raw}
	names := ifNames(cands)
	var open []hidio.Transport
	var errs []error
	for i, c := range cands {
		tr, err := t.open(w, c, names[i])
		if err != nil {
			t.say("%s  cannot open: %v\n", names[i], err)
			errs = append(errs, err)
			continue
		}
		open = append(open, tr)
	}
	if len(open) == 0 {
		return r.openFailure(w, errs)
	}
	t.say("Listening on %d interface(s) for %s; nothing is sent. Ctrl-C stops.\n", len(open), *dur)
	timer := time.NewTimer(*dur)
	defer timer.Stop()
	drain := make(chan struct{})
	var wg sync.WaitGroup
	for _, tr := range open {
		wg.Go(func() {
			for {
				select {
				case _, ok := <-tr.Reports():
					if !ok {
						return
					}
				case <-tr.Wake():
				case <-drain:
					return
				}
			}
		})
	}
	select {
	case <-timer.C:
	case <-r.ctx.Done():
	}
	close(drain)
	for _, tr := range open {
		tr.Close()
	}
	wg.Wait()
	t.wait()
	t.summary()
	return nil
}

// ifNames names each interface by its number, or by its path when the
// numbers do not tell the interfaces apart.
func ifNames(cands []hidio.Candidate) []string {
	names := make([]string, len(cands))
	seen := map[string]bool{}
	unique := true
	for i, c := range cands {
		names[i] = fmt.Sprintf("if%d", c.Interface)
		if c.Interface < 0 || seen[names[i]] {
			unique = false
		}
		seen[names[i]] = true
	}
	if !unique {
		for i, c := range cands {
			names[i] = c.Path
		}
	}
	return names
}

func (r *runner) openFailure(w *world, errs []error) error {
	for _, err := range errs {
		d := w.host.Diagnose(err)
		switch d.Class {
		case hidio.ClassPermission:
			return fail(ExitPermission, d.Summary, d.Hint)
		case hidio.ClassLocked, hidio.ClassSeized:
			return fail(ExitBlocked, d.Summary, d.Hint)
		}
	}
	return fail(ExitNoReceiver, "no interface could be opened", "")
}

// tracer prints the entries a Recorder writes for each opened interface.
type tracer struct {
	mu     sync.Mutex
	out    io.Writer
	start  time.Time
	wg     sync.WaitGroup
	counts map[string]map[byte]int
	names  []string
	raw    bool
}

func (t *tracer) open(w *world, c hidio.Candidate, name string) (hidio.Transport, error) {
	target := wire.Mouse
	if c.Class == catalog.ClassKeyboard {
		target = wire.Keyboard
	}
	pr, pw := io.Pipe()
	t.wg.Go(func() { t.follow(name, pr) })
	rec, err := hidio.NewRecorder(pw, hidio.Header{Source: "arcctl trace"})
	if err != nil {
		pw.Close()
		return nil, err
	}
	tr, err := w.devices.Open(c, hidio.NewGuard(target), rec)
	if err != nil {
		pw.Close()
		return nil, err
	}
	t.mu.Lock()
	t.names = append(t.names, name)
	t.counts[name] = map[byte]int{}
	t.mu.Unlock()
	return closer{tr, pw}, nil
}

// closer closes the transcript pipe after the transport, so the follower
// sees every entry.
type closer struct {
	hidio.Transport
	pw *io.PipeWriter
}

func (c closer) Close() error {
	err := c.Transport.Close()
	c.pw.Close()
	return err
}

func (t *tracer) say(format string, a ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.out, format, a...)
}

func (t *tracer) follow(name string, r io.Reader) {
	dec := json.NewDecoder(r)
	var h hidio.Header
	if dec.Decode(&h) != nil {
		io.Copy(io.Discard, r)
		return
	}
	for {
		var e hidio.Entry
		if err := dec.Decode(&e); err != nil {
			if !errors.Is(err, io.EOF) {
				io.Copy(io.Discard, r)
			}
			return
		}
		t.print(name, e)
	}
}

func (t *tracer) print(name string, e hidio.Entry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	at := e.At.Sub(t.start).Seconds()
	switch e.Dir {
	case hidio.DirIn:
		t.counts[name][e.ID]++
		data := e.Data.String()
		if _, ok := (hidio.Report{ID: e.ID, Data: e.Data.Bytes}).Packet(); !ok && !t.raw {
			data = fmt.Sprintf("%d bytes", len(e.Data.Bytes))
		}
		fmt.Fprintf(t.out, "%8.3f  %s  id %-3d %s%s\n", at, name, e.ID, data, describe(e))
	case hidio.DirInError:
		fmt.Fprintf(t.out, "%8.3f  %s  read stopped: %s\n", at, name, e.Err)
	}
}

func describe(e hidio.Entry) string {
	p, ok := hidio.Report{ID: e.ID, Data: e.Data.Bytes}.Packet()
	if !ok {
		return ""
	}
	return "  " + strings.TrimPrefix(p.Cmd().String(), "cmd ")
}

func (t *tracer) wait() { t.wg.Wait() }

func (t *tracer) summary() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range t.names {
		c := t.counts[name]
		total := 0
		var parts []string
		for _, id := range slices.Sorted(maps.Keys(c)) {
			total += c[id]
			parts = append(parts, fmt.Sprintf("id %d: %d", id, c[id]))
		}
		text := fmt.Sprintf("%s: %d report(s)", name, total)
		if len(parts) > 0 {
			text += " (" + strings.Join(parts, ", ") + ")"
		}
		fmt.Fprintln(t.out, text)
	}
}
