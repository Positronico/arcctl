package safety

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/wire"
)

// Overlay is the image dry runs write to. A Link it wraps never sends a
// cmd 7: the packet is checked as the Edit policy would check it, recorded,
// logged and answered with an echo of itself, and its bytes land in the
// overlay. Reads still go to the device, and the bytes the overlay holds
// replace what came back, so a read-back sees what the write would have left.
// Everything else passes through, so the executor runs as it does for a
// real apply.
type Overlay struct {
	log *slog.Logger

	mu      sync.Mutex
	written *flash.Image
	packets []wire.Packet
}

func NewOverlay(log *slog.Logger) *Overlay {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Overlay{log: log, written: flash.New()}
}

// Link wraps l so that its writes go to the overlay.
func (o *Overlay) Link(l Link) Link { return &overlayLink{o: o, l: l} }

// Packets are the cmd-7 packets the dry runs would have sent, in order.
func (o *Overlay) Packets() []wire.Packet {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.packets)
}

// Written lists the extents the dry runs wrote.
func (o *Overlay) Written() []flash.Extent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.written.KnownExtents()
}

// Apply returns a copy of im with the overlay's bytes on top.
func (o *Overlay) Apply(im *flash.Image) *flash.Image {
	out := flash.New()
	if im != nil {
		out = im.Clone()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, e := range o.written.KnownExtents() {
		b, _ := o.written.Get(e)
		_ = out.Set(e.Addr, b)
	}
	return out
}

func (o *Overlay) write(p wire.Packet) error {
	if err := wire.Edit.Check(p, wire.Mouse); err != nil {
		return fmt.Errorf("%w: %w", hidio.ErrForbidden, err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.written.Set(int(p.Addr()), p.Data()); err != nil {
		return err
	}
	o.packets = append(o.packets, p)
	o.log.Info("dry run: not sent", "packet", p.String())
	return nil
}

// patch puts the overlay's bytes over a read reply.
func (o *Overlay) patch(rep wire.Packet) wire.Packet {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := rep
	for i := range rep.Data() {
		if b, ok := o.written.Byte(int(rep.Addr()) + i); ok {
			out[5+i] = b
		}
	}
	if out != rep {
		out[wire.Size-1] = out.Checksum()
	}
	return out
}

type overlayLink struct {
	o *Overlay
	l Link
}

func (w *overlayLink) Transact(ctx context.Context, p wire.Packet) (wire.Packet, error) {
	switch p.Cmd() {
	case wire.CmdWrite:
		if err := ctx.Err(); err != nil {
			return wire.Packet{}, err
		}
		if err := w.o.write(p); err != nil {
			return wire.Packet{}, err
		}
		return p, nil
	case wire.CmdRead:
		rep, err := w.l.Transact(ctx, p)
		if err != nil {
			return rep, err
		}
		return w.o.patch(rep), nil
	}
	return w.l.Transact(ctx, p)
}

func (w *overlayLink) OnlineCheck(ctx context.Context) error { return w.l.OnlineCheck(ctx) }

func (w *overlayLink) Recheck(ctx context.Context) error { return w.l.Recheck(ctx) }

func (w *overlayLink) Pending() []Signal { return w.l.Pending() }
