//go:build hwtest

package hwtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/wire"
)

var (
	ErrAsleep  = errors.New("hwtest: the mouse is asleep")
	ErrNoReply = errors.New("hwtest: no reply")
	ErrChanged = errors.New("hwtest: the device no longer holds the bytes the preview showed")
	ErrVerify  = errors.New("hwtest: the read-back differs")
)

// Raw path timing: the receiver answers cmd 3 itself within milliseconds, and
// the session treats a reply as a duplicate for 2 s after its request.
const (
	rawTry     = 500 * time.Millisecond
	rawTries   = 3
	listenFor  = 2 * time.Second
	lingerFor  = 3 * time.Second
	probeFirst = 1500 * time.Millisecond
)

// rawPath talks to the attached interface past the session's guard. Reads
// and online checks are read-only packets; the only writes are identity
// writes and the probe, which put back the bytes the device already holds.
type rawPath struct {
	t      *tap
	note   func(string)
	listen time.Duration
}

// online sends cmd 3 and lets the session see the reply too, as it would a
// cmd-3 push. It returns the online flag and the address bytes as sent.
func (r *rawPath) online(ctx context.Context) (bool, [3]byte, error) {
	p := wire.MustBuild(wire.Mouse, wire.CmdOnline, 0, nil)
	for range rawTries {
		x, err := r.t.exchange(ctx, p, exchangeOpts{match: isCmd(wire.CmdOnline), first: rawTry})
		if err != nil {
			return false, [3]byte{}, err
		}
		if len(x.replies) > 0 {
			rep := x.replies[0].p
			return rep[5] == 1, [3]byte{rep[6], rep[7], rep[8]}, nil
		}
	}
	return false, [3]byte{}, fmt.Errorf("%w to cmd 3", ErrNoReply)
}

// read reads e with cmd 8, at most 10 bytes a packet, and returns the bytes
// and each packet's round trip.
func (r *rawPath) read(ctx context.Context, e flash.Extent) ([]byte, []time.Duration, error) {
	var out []byte
	var times []time.Duration
	for a := e.Addr; a < e.End(); a += wire.MaxData {
		n := min(wire.MaxData, e.End()-a)
		p, err := wire.BuildRead(wire.Mouse, uint16(a), n)
		if err != nil {
			return nil, nil, err
		}
		if err := wire.ReadOnly.Check(p, wire.Mouse); err != nil {
			return nil, nil, err
		}
		rep, d, err := r.transact(ctx, p)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", e, err)
		}
		out = append(out, rep.Data()...)
		times = append(times, d)
	}
	return out, times, nil
}

func (r *rawPath) transact(ctx context.Context, p wire.Packet) (wire.Packet, time.Duration, error) {
	for range rawTries {
		rep, d, ok, err := r.once(ctx, p)
		if err != nil || ok {
			return rep, d, err
		}
	}
	return wire.Packet{}, 0, fmt.Errorf("%w to %v after %d tries", ErrNoReply, p.Cmd(), rawTries)
}

// once sends a read once; ok is false when nothing answered it in time.
func (r *rawPath) once(ctx context.Context, p wire.Packet) (rep wire.Packet, d time.Duration, ok bool, err error) {
	match := func(rep wire.Packet) bool { return answers(p, rep) }
	x, err := r.t.exchange(ctx, p, exchangeOpts{match: match, swallow: true, first: rawTry, linger: lingerFor})
	if err != nil || len(x.replies) == 0 {
		return wire.Packet{}, 0, false, err
	}
	rep = x.replies[0].p
	if err := rep.Status().Err(); err != nil {
		return rep, x.latency(), true, err
	}
	if rep.Len() != p.Len() {
		return rep, x.latency(), true, fmt.Errorf("%d bytes for a read of %d", rep.Len(), p.Len())
	}
	return rep, x.latency(), true, nil
}

// answers accepts what the session would take as the reply to req, and a
// status-1 frame with its command and address whatever length it echoes.
func answers(req, rep wire.Packet) bool {
	if rep.Status() == wire.StatusNAK {
		return rep.Cmd() == req.Cmd() && rep.Addr() == req.Addr()
	}
	return wire.Match(req, rep)
}

func isCmd(c wire.Cmd) func(wire.Packet) bool {
	return func(p wire.Packet) bool { return p.Cmd() == c }
}

// written is what one cmd-7 packet of the raw path brought back.
type written struct {
	packet  wire.Packet
	at      time.Time
	replies []reply
	seen    []reply
}

// write sends a cmd-7 packet and takes every cmd-7 frame that comes back
// within the listening window: the echo, a second reply, a NAK. Nothing else
// sends cmd 7 while the raw path runs.
func (r *rawPath) write(ctx context.Context, p wire.Packet, first time.Duration) (written, error) {
	x, err := r.t.exchange(ctx, p, exchangeOpts{match: isCmd(wire.CmdWrite), swallow: true, first: first, listen: r.listen, linger: lingerFor})
	return written{packet: p, at: x.at, replies: x.replies, seen: x.seen}, err
}

// identityResult is an identity write: its packets, what came back, and the
// read-back.
type identityResult struct {
	writes   []written
	readBack []byte
}

// identity writes want back to e through the raw path, one packet per 10
// bytes, and reads e back. The mouse must be online and e must still hold
// want: an identity write never changes a byte.
func (r *rawPath) identity(ctx context.Context, e flash.Extent, want []byte) (identityResult, error) {
	var res identityResult
	if err := r.ready(ctx, e, want); err != nil {
		return res, err
	}
	for off := 0; off < e.Len; off += wire.MaxData {
		p, err := wire.Build(wire.Mouse, wire.CmdWrite, uint16(e.Addr+off), want[off:min(off+wire.MaxData, e.Len)])
		if err != nil {
			return res, err
		}
		if err := wire.Edit.Check(p, wire.Mouse); err != nil {
			return res, err
		}
		r.note("raw path: identity write " + p.String())
		w, err := r.write(ctx, p, rawTry)
		res.writes = append(res.writes, w)
		if err != nil {
			return res, err
		}
	}
	return res, r.readBack(ctx, e, want, &res.readBack)
}

// probeResult is the NAK probe: the packet with the wrong checksum, what came
// back, and the read-back.
type probeResult struct {
	write    written
	readBack []byte
}

// probe sends the identity write of e, one packet, with its checksum off by
// one. Its data equal the flash, so a device that took it changes nothing.
func (r *rawPath) probe(ctx context.Context, e flash.Extent, want []byte) (probeResult, error) {
	var res probeResult
	if e.Len > wire.MaxData {
		return res, fmt.Errorf("the probe takes one packet, %s needs more", e)
	}
	if err := r.ready(ctx, e, want); err != nil {
		return res, err
	}
	p, err := probePacket(e, want)
	if err != nil {
		return res, err
	}
	r.note("raw path: NAK probe " + p.String())
	res.write, err = r.write(ctx, p, probeFirst)
	if err != nil {
		return res, err
	}
	return res, r.readBack(ctx, e, want, &res.readBack)
}

// probePacket is the identity write of e with byte 15 one below its checksum,
// and nothing else wrong with it.
func probePacket(e flash.Extent, want []byte) (wire.Packet, error) {
	p, err := wire.Build(wire.Mouse, wire.CmdWrite, uint16(e.Addr), want)
	if err != nil {
		return p, err
	}
	p[wire.Size-1]--
	if err := wire.Edit.Check(p, wire.Mouse); !errors.Is(err, wire.ErrChecksum) {
		return p, fmt.Errorf("the probe packet %v must fail only its checksum: %v", p, err)
	}
	return p, nil
}

// ready is the check before a raw write: the mouse is online and e holds want.
func (r *rawPath) ready(ctx context.Context, e flash.Extent, want []byte) error {
	if touchesPrimary(e) {
		return fmt.Errorf("%w: %s is a binding of the left or right button", errPrimary, e)
	}
	on, _, err := r.online(ctx)
	switch {
	case err != nil:
		return err
	case !on:
		return ErrAsleep
	}
	cur, _, err := r.read(ctx, e)
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, want) {
		return fmt.Errorf("%w: %s holds % x, want % x", ErrChanged, e, cur, want)
	}
	return nil
}

func (r *rawPath) readBack(ctx context.Context, e flash.Extent, want []byte, into *[]byte) error {
	got, _, err := r.read(ctx, e)
	*into = got
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%w: %s holds % x, want % x", ErrVerify, e, got, want)
	}
	return nil
}
