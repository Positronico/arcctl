package safety

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/hidio"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

// ResetPhrase is what the user types to send a factory reset (D4).
const ResetPhrase = "reset"

var (
	ErrResetLocked    = errors.New("safety: factory reset is available after hardware test H7")
	ErrResetUnchecked = errors.New("safety: the factory reset went out, but the mouse was not read again")
	ErrFirmware       = errors.New("safety: the mouse reports another firmware than the one loaded")
)

// ResetVerification is the hardware record that opens the factory reset for
// model m on firmware: H7's record of the reset for exactly that model and
// firmware (D4).
func ResetVerification(m *catalog.Model, firmware string, vs catalog.Verifications) (catalog.Verification, bool) {
	gate, _ := hidio.GateOf(wire.Reset)
	if m == nil || m.Family != catalog.FamilyMouse || firmware == "" {
		return catalog.Verification{}, false
	}
	for _, v := range vs.Find(m.Key, gate.Feature) {
		if v.Firmware == firmware && v.Stage == gate.Stage {
			return v, true
		}
	}
	return catalog.Verification{}, false
}

// ResetGate returns nil when a factory reset may be sent to model m on
// firmware, and otherwise an error wrapping ErrResetLocked that says why.
// The guard applies the same gate again when the session asks it for the
// Reset policy.
func ResetGate(m *catalog.Model, firmware string, vs catalog.Verifications) error {
	switch {
	case m == nil:
		return fmt.Errorf("%w: the mouse is not identified yet", ErrResetLocked)
	case m.Family != catalog.FamilyMouse:
		return fmt.Errorf("%w: arcctl resets only mice", ErrResetLocked)
	case firmware == "":
		return fmt.Errorf("%w: the mouse's firmware version is not known yet", ErrResetLocked)
	}
	if _, ok := ResetVerification(m, firmware, vs); !ok {
		return fmt.Errorf("%w: no record of it for the %s (%s) on firmware %s", ErrResetLocked, m.Name, m.Key, firmware)
	}
	return nil
}

// PreflightReset checks a factory reset before its packet (§6.3, D4): the
// checks of an apply that do not compare records (the session's state, a
// fresh online check, the mouse and profile it was asked for, the lock, the
// console, other clients and a clean journal), the firmware the mouse
// reports now against firmware, the one the gate was opened for, and the
// typed phrase. A dry run sends nothing, so it skips the phrase, other
// clients and the journal, as an apply's does. The gate itself is the
// caller's first check, before any I/O.
func PreflightReset(dev plan.Identity, profile *byte, firmware string, f Facts, g Gates) error {
	var fs []error
	var pe *PreflightError
	if err := Preflight(KindReset, plan.Plan{Device: dev, Profile: profile}, f, g); errors.As(err, &pe) {
		fs = pe.Failures
	}
	if f.State == nil && f.Online == nil && f.Firmware != firmware {
		fs = append(fs, fmt.Errorf("%w: it reports %s, arcctl loaded %s; reload first", ErrFirmware, orNone(f.Firmware), orNone(firmware)))
	}
	if !g.DryRun && strings.TrimSpace(g.Confirm) != ResetPhrase {
		fs = append(fs, fmt.Errorf("%w: type %q to reset the mouse to its factory settings", ErrConfirm, ResetPhrase))
	}
	return blocked(fs)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// ResetBackupGate is I1 for a factory reset: the full backup taken just
// before it must be saved and complete, since the reset may change any byte.
func ResetBackupGate(path string, missing []flash.Extent) error {
	switch {
	case path == "":
		return fmt.Errorf("%w: a factory reset needs a full backup first", ErrNoFullBackup)
	case len(missing) > 0:
		ranges := make([]string, len(missing))
		for i, m := range missing {
			ranges[i] = m.String()
		}
		return fmt.Errorf("%w: %s lacks %s; a factory reset needs a complete one", ErrPartialBackup, path, strings.Join(ranges, ", "))
	}
	return nil
}

// Reply is how the mouse answered a factory reset.
type Reply uint8

const (
	ReplyUnknown Reply = iota // not sent, or the wait was cut short
	ReplyAck                  // a reply with status 0
	ReplyNAK                  // a reply with status 1
	ReplyNone                 // no reply within the wait
	ReplyError                // the OS reported a write error: the packet may or may not have gone out
)

var replyNames = [...]string{"unknown", "ack", "nak", "none", "error"}

func (r Reply) String() string {
	if int(r) < len(replyNames) {
		return replyNames[r]
	}
	return "reply " + strconv.Itoa(int(r))
}

// ResetVerdict is what the reload after a factory reset found, compared with
// the full backup taken just before it.
type ResetVerdict uint8

const (
	VerdictUnchecked ResetVerdict = iota // the mouse was not read again
	VerdictChanged                       // bytes differ from the backup: the reset took effect
	VerdictUnchanged                     // every byte read again equals the backup: the reset did nothing, or the mouse held its factory settings already
)

var verdictNames = [...]string{"unchecked", "changed", "unchanged"}

func (v ResetVerdict) String() string {
	if int(v) < len(verdictNames) {
		return verdictNames[v]
	}
	return "verdict " + strconv.Itoa(int(v))
}

// ResetRecord is what the journal holds of a factory reset: the full backup
// taken just before it and the packet; whether the packet may have gone out
// and how the mouse answered; and what the reload after it found.
type ResetRecord struct {
	Backup  string
	Packet  wire.Packet
	Sending bool // journaled before the packet: it may have gone out
	Reply   Reply
	SendErr string
	Verdict ResetVerdict
	Changed []flash.Extent
	Unread  []flash.Extent
}

// SendReset journals a factory reset of dev and sends its packet p through
// send, once: send must not send it again whatever happens, and SendReset
// never calls it twice. The run and the fact that the packet is going out are
// fsynced before send is called, and the reply after. backup is the full
// backup taken just before. run is "" when nothing was sent. A NAK or no
// reply is only a Reply; err is any other error of the send, or the
// journal's after the packet.
func SendReset(ctx context.Context, j *Journal, dev plan.Identity, profile *byte, backup string, p wire.Packet,
	send func(context.Context, wire.Packet) (wire.Packet, error)) (run string, reply Reply, err error) {
	if p.Cmd() != wire.CmdClear {
		return "", ReplyUnknown, fmt.Errorf("%w: %v is not the factory reset", ErrReply, p.Cmd())
	}
	if run, err = j.beginReset(dev, profile, backup, p); err != nil {
		return "", ReplyUnknown, err
	}
	_, serr := send(ctx, p)
	switch {
	case serr == nil:
		reply = ReplyAck
	case errors.Is(serr, wire.ErrNAK):
		reply = ReplyNAK
	case errors.Is(serr, ErrTimeout):
		reply = ReplyNone
	case ctx.Err() != nil:
		reply = ReplyUnknown
	default:
		reply = ReplyError
	}
	if jerr := j.resetSent(run, reply, serr); jerr != nil {
		return run, reply, errors.Join(serr, jerr)
	}
	if reply == ReplyNAK || reply == ReplyNone {
		serr = nil
	}
	return run, reply, serr
}

// EndReset journals what the reload after the factory reset run found; cause
// says why it was not read again, for VerdictUnchecked. The run may be one
// another process started and never ended: this journal's file then takes
// the end entry, which settles it.
func (j *Journal) EndReset(run string, v ResetVerdict, changed, unread []flash.Extent, cause error) error {
	e := entry{Type: typeEnd, Run: run, Time: stamp(), Result: resultComplete, Verdict: v.String(), Changed: changed, Unread: unread}
	if v == VerdictUnchecked {
		e.Result = resultStopped
		if cause != nil {
			e.Error = cause.Error()
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.open(); err != nil {
		return err
	}
	return j.append(e)
}

func (j *Journal) beginReset(dev plan.Identity, profile *byte, backup string, p wire.Packet) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.open(); err != nil {
		return "", err
	}
	j.runs++
	id := j.name + "/" + strconv.Itoa(j.runs)
	now := stamp()
	err := j.append(
		entry{V: journalVersion, Type: typeRun, Run: id, Time: now, Kind: KindReset.String(), Device: deviceOf(dev),
			Profile: profile, Backup: backup, Packet: p[:]},
		entry{Type: typeSend, Run: id, Time: now, State: sendSending},
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (j *Journal) resetSent(run string, r Reply, err error) error {
	e := entry{Type: typeSend, Run: run, Time: stamp(), State: sendSent, Reply: r.String()}
	if err != nil {
		e.Error = err.Error()
	}
	return j.write(e)
}

const (
	sendSending = "sending"
	sendSent    = "sent"
)

func (p *parser) send(r *Run, e entry) error {
	if r.Reset == nil {
		return fmt.Errorf("send entry for %s run %s", r.Kind, r.ID)
	}
	switch e.State {
	case sendSending:
		r.Reset.Sending = true
	case sendSent:
		reply, ok := parseName(e.Reply, replyNames[:], ReplyUnknown)
		if !ok || !r.Reset.Sending {
			return fmt.Errorf("run %s: reply %q before the packet", r.ID, e.Reply)
		}
		r.Reset.Reply, r.Reset.SendErr = reply, e.Error
	default:
		return fmt.Errorf("send state %q", e.State)
	}
	return nil
}

func (p *parser) resetEnd(r *Run, e entry) error {
	v, ok := parseName(e.Verdict, verdictNames[:], VerdictUnchecked)
	if !ok {
		return fmt.Errorf("verdict %q", e.Verdict)
	}
	r.Reset.Verdict, r.Reset.Changed, r.Reset.Unread = v, e.Changed, e.Unread
	return nil
}

// ResetDiff compares the backup taken before a factory reset with the
// reload after it, over ranges: the byte runs both know that differ, and
// the runs the backup knows that the reload lacks.
func ResetDiff(before, after *flash.Image, ranges []flash.Extent) (changed, unread []flash.Extent) {
	add := func(to *[]flash.Extent, a int) {
		if n := len(*to); n > 0 && (*to)[n-1].End() == a {
			(*to)[n-1].Len++
			return
		}
		*to = append(*to, flash.Extent{Addr: a, Len: 1})
	}
	for _, e := range ranges {
		for a := e.Addr; a < e.End(); a++ {
			b, ok := before.Byte(a)
			if !ok {
				continue
			}
			switch x, known := after.Byte(a); {
			case !known:
				add(&unread, a)
			case x != b:
				add(&changed, a)
			}
		}
	}
	return changed, unread
}
