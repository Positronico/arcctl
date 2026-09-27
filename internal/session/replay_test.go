package session_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/positronico/arcctl/internal/emu"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/session"
	"github.com/positronico/arcctl/internal/wire"
)

// replayed is Devices over one recorded interface.
type replayed struct {
	mu         sync.Mutex
	c          hidio.Candidate
	transcript []byte
	rp         *hidio.Replay
}

func (r *replayed) Enumerate() ([]hidio.Candidate, error) { return []hidio.Candidate{r.c}, nil }

func (r *replayed) Open(c hidio.Candidate, g *hidio.Guard, rec *hidio.Recorder) (hidio.Transport, error) {
	tr, rp, err := hidio.OpenReplay(bytes.NewReader(r.transcript), g)
	r.mu.Lock()
	r.rp = rp
	r.mu.Unlock()
	return tr, err
}

func (r *replayed) replay() *hidio.Replay {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rp
}

func TestRecordedSessionReplays(t *testing.T) {
	for _, redact := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw", true: "redacted"}[redact], func(t *testing.T) {
			var buf bytes.Buffer
			rec, err := hidio.NewRecorder(&buf, hidio.Header{Source: "session test", Redacted: redact})
			must(t, err)
			b := newBus(t, emu.Options{})
			m := em11(t)
			m.Image = dumpImage(t)
			d := add(t, b, receiver(m))
			c := d.Candidates()[1]
			s := session.New(session.Options{Devices: b, Device: c.Path, Recorder: rec, Timing: fast()})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s.Run(ctx) }()
			live := await(t, s, "ready", idle)
			cancel()
			<-done
			must(t, rec.Err())

			r := &replayed{c: c, transcript: buf.Bytes()}
			r.c.Backend = "replay"
			rs := start(t, nil, session.Options{Devices: r, Device: c.Path})
			got := await(t, rs, "ready from the replay", idle)
			if err := r.replay().Diverged(); err != nil {
				t.Fatal(err)
			}
			if n := r.replay().Remaining(); n != 0 {
				t.Errorf("%d recorded writes not replayed", n)
			}
			if got.Identity.Key() != live.Identity.Key() || got.Versions != live.Versions || got.Model != live.Model {
				t.Errorf("replayed %+v %+v, live %+v %+v", got.Identity, got.Versions, live.Identity, live.Versions)
			}
			settings := flash.Extent{Addr: 0, Len: 256}
			sameBytes(t, got.Image, live.Image, settings)
			if !redact {
				sameBytes(t, got.Image, live.Image, live.Image.KnownExtents()...)
			}
			if got.Stats.Foreign != 0 || got.Policy != wire.ReadOnly {
				t.Errorf("stats %+v, policy %v", got.Stats, got.Policy)
			}
		})
	}
}
