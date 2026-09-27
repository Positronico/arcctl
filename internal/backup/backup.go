// Package backup reads and writes arcctl backups: the bytes read from a
// device's flash, which of them are known, the device they came from and a
// decoded summary a person can re-enter by hand. It also writes the web app's
// .bin export and reads .bin files and raw dumps for display.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

const Format = "arcctl-backup/1"

// Where the bytes of a backup came from.
const (
	SourceDevice   = "device"
	SourceEmulator = "emulator"
	SourceReplay   = "replay"
)

const (
	statusOK      = "ok"
	statusMissing = "missing"
)

var (
	ErrFormat   = errors.New("backup: not an arcctl backup")
	ErrInvalid  = errors.New("backup: invalid backup")
	ErrChecksum = errors.New("backup: sha256 does not match the ranges")
	ErrNoImage  = errors.New("backup: the capture has no image")
)

// File is one backup as it is stored. The image is kept as ranges of known
// bytes; bytes in no "ok" range were never read.
type File struct {
	Format   string    `json:"format"`
	Created  time.Time `json:"created"`
	Tool     string    `json:"tool"`
	Source   string    `json:"source"`
	Label    string    `json:"label,omitempty"`
	Device   Device    `json:"device"`
	Model    *Model    `json:"model"`
	Full     bool      `json:"full"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Ranges   []Range   `json:"ranges"`
	Summary  *Summary  `json:"summary"`
	SHA256   string    `json:"sha256"`
}

type Device struct {
	VID         Hex16    `json:"vid"`
	PID         Hex16    `json:"pid"`
	CID         Hex8     `json:"cid"`
	MID         byte     `json:"mid"`
	Conn        *byte    `json:"conn"`
	Addr        string   `json:"addr,omitempty"`
	AddrTrusted bool     `json:"addr_trusted"`
	Key         string   `json:"key"`
	FWMouse     string   `json:"fw_mouse,omitempty"`
	FWReceiver  string   `json:"fw_receiver,omitempty"`
	Profile     *Profile `json:"profile"`
}

// Profile is the cmd-14 answer at capture time; a nil Profile means it was
// never asked.
type Profile struct {
	Supported bool `json:"supported"`
	Value     byte `json:"value"`
}

type Model struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Sensor string `json:"sensor,omitempty"`
}

// Range is a run of known bytes ("ok", with their hex) or of bytes whose read
// failed ("missing").
type Range struct {
	Addr   int    `json:"addr"`
	Len    int    `json:"len"`
	Status string `json:"status"`
	Hex    string `json:"hex,omitempty"`
}

func (r Range) Extent() flash.Extent { return flash.Extent{Addr: r.Addr, Len: r.Len} }

// Meta is what a backup records beyond the capture itself.
type Meta struct {
	Tool    string
	Source  string // SourceDevice when empty
	Label   string
	Created time.Time
	Conn    *byte   // handshake connection type
	OS      keys.OS // key names in the summary
}

// New builds a backup from a session capture.
func New(c session.Capture, meta Meta) (*File, error) {
	if c.Image == nil {
		return nil, ErrNoImage
	}
	src := meta.Source
	if src == "" {
		src = SourceDevice
	}
	id := c.Device
	f := &File{
		Format:   Format,
		Created:  meta.Created.UTC().Truncate(time.Second),
		Tool:     meta.Tool,
		Source:   src,
		Label:    meta.Label,
		Full:     c.Full,
		Started:  c.Started.UTC().Truncate(time.Millisecond),
		Finished: c.Finished.UTC().Truncate(time.Millisecond),
		Device: Device{
			VID: Hex16(id.VID), PID: Hex16(id.PID), CID: Hex8(id.CID), MID: id.MID,
			AddrTrusted: id.AddrTrusted, Key: id.Key(),
			FWMouse: c.Versions.Mouse, FWReceiver: c.Versions.Receiver,
		},
	}
	if id.Addr != [3]byte{} {
		f.Device.Addr = formatAddr(id.Addr)
	}
	if meta.Conn != nil {
		v := *meta.Conn
		f.Device.Conn = &v
	}
	if c.Profile.Asked {
		f.Device.Profile = &Profile{Supported: c.Profile.Supported, Value: c.Profile.Value}
	}
	if m := c.Model; m != nil {
		f.Model = &Model{Key: m.Key, Name: m.Name}
		if m.Sensor != nil {
			f.Model.Sensor = m.Sensor.ID
		}
		if m.Family == catalog.FamilyMouse {
			f.Summary = Summarize(m, c.Image, meta.OS)
		}
	}
	f.Ranges = ranges(c.Image, c.Missing)
	sum, err := digest(f.Ranges)
	if err != nil {
		return nil, err
	}
	f.SHA256 = sum
	return f, nil
}

func ranges(im *flash.Image, missing []flash.Extent) []Range {
	var out []Range
	for _, e := range im.KnownExtents() {
		b, _ := im.Get(e)
		out = append(out, Range{Addr: e.Addr, Len: e.Len, Status: statusOK, Hex: hex.EncodeToString(b)})
	}
	var lost [flash.Size]bool
	for _, e := range missing {
		for a := max(e.Addr, 0); a < min(e.End(), flash.Size); a++ {
			if _, known := im.Byte(a); !known {
				lost[a] = true
			}
		}
	}
	for a := 0; a < flash.Size; a++ {
		if !lost[a] {
			continue
		}
		start := a
		for a < flash.Size && lost[a] {
			a++
		}
		out = append(out, Range{Addr: start, Len: a - start, Status: statusMissing})
	}
	slices.SortFunc(out, func(x, y Range) int { return x.Addr - y.Addr })
	return out
}

// digest is the SHA-256 over every known range in order: its address and
// length as big-endian uint32, then its bytes.
func digest(rs []Range) (string, error) {
	h := sha256.New()
	var hdr [8]byte
	for _, r := range rs {
		if r.Status != statusOK {
			continue
		}
		b, err := hex.DecodeString(r.Hex)
		if err != nil {
			return "", err
		}
		binary.BigEndian.PutUint32(hdr[:4], uint32(r.Addr))
		binary.BigEndian.PutUint32(hdr[4:], uint32(r.Len))
		h.Write(hdr[:])
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Read decodes and validates a backup.
func Read(r io.Reader) (*File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

func Decode(data []byte) (*File, error) {
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || !strings.HasPrefix(probe.Format, "arcctl-backup/") {
		return nil, ErrFormat
	}
	if probe.Format != Format {
		return nil, fmt.Errorf("%w: format %q, this arcctl reads %q", ErrFormat, probe.Format, Format)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Validate checks the ranges, the checksum and the identity key.
func (f *File) Validate() error {
	if f.Format != Format {
		return ErrFormat
	}
	switch f.Source {
	case SourceDevice, SourceEmulator, SourceReplay:
	default:
		return fmt.Errorf("%w: source %q", ErrInvalid, f.Source)
	}
	end := 0
	for i, r := range f.Ranges {
		e := r.Extent()
		switch {
		case r.Len <= 0 || !(flash.Extent{Addr: 0, Len: flash.Size}).Contains(e):
			return fmt.Errorf("%w: range %d (%v) is outside the image", ErrInvalid, i, e)
		case r.Addr < end:
			return fmt.Errorf("%w: range %d (%v) overlaps or is out of order", ErrInvalid, i, e)
		case r.Status == statusOK && len(r.Hex) != 2*r.Len:
			return fmt.Errorf("%w: range %d (%v) has %d hex digits", ErrInvalid, i, e, len(r.Hex))
		case r.Status == statusMissing && r.Hex != "":
			return fmt.Errorf("%w: missing range %d (%v) has bytes", ErrInvalid, i, e)
		case r.Status != statusOK && r.Status != statusMissing:
			return fmt.Errorf("%w: range %d status %q", ErrInvalid, i, r.Status)
		}
		end = e.End()
	}
	sum, err := digest(f.Ranges)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if sum != f.SHA256 {
		return ErrChecksum
	}
	if _, err := parseAddr(f.Device.Addr); err != nil {
		return err
	}
	if k := f.Identity().Key(); k != f.Device.Key {
		return fmt.Errorf("%w: device key %q, the device fields give %q", ErrInvalid, f.Device.Key, k)
	}
	return nil
}

// Image rebuilds the captured flash: the bytes of every "ok" range are known.
func (f *File) Image() *flash.Image {
	im := flash.New()
	for _, r := range f.Ranges {
		if r.Status != statusOK {
			continue
		}
		if b, err := hex.DecodeString(r.Hex); err == nil {
			_ = im.Set(r.Addr, b)
		}
	}
	return im
}

// Missing lists the ranges whose reads failed.
func (f *File) Missing() []flash.Extent {
	var out []flash.Extent
	for _, r := range f.Ranges {
		if r.Status == statusMissing {
			out = append(out, r.Extent())
		}
	}
	return out
}

// Known counts the captured bytes.
func (f *File) Known() int {
	n := 0
	for _, r := range f.Ranges {
		if r.Status == statusOK {
			n += r.Len
		}
	}
	return n
}

func (f *File) Identity() plan.Identity {
	addr, _ := parseAddr(f.Device.Addr)
	return plan.Identity{
		CID: byte(f.Device.CID), MID: f.Device.MID, Addr: addr, AddrTrusted: f.Device.AddrTrusted,
		VID: uint16(f.Device.VID), PID: uint16(f.Device.PID),
	}
}

// Key is the device identity the backup belongs to (plan.Identity.Key).
func (f *File) Key() string { return f.Identity().Key() }

// CatalogModel looks up the backup's model in this build's catalog.
func (f *File) CatalogModel() (*catalog.Model, bool) {
	if f.Model == nil {
		return nil, false
	}
	return catalog.ByKey(f.Model.Key)
}

// Encode writes the backup as indented JSON.
func (f *File) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(f)
}

func formatAddr(a [3]byte) string { return fmt.Sprintf("%02x:%02x:%02x", a[0], a[1], a[2]) }

func parseAddr(s string) ([3]byte, error) {
	var a [3]byte
	if s == "" {
		return a, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != len(a) {
		return a, fmt.Errorf("%w: address %q", ErrInvalid, s)
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil || len(p) != 2 {
			return a, fmt.Errorf("%w: address %q", ErrInvalid, s)
		}
		a[i] = byte(v)
	}
	return a, nil
}

// Hex16 is a USB ID, written as four upper-case hex digits.
type Hex16 uint16

func (h Hex16) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "%04X", uint16(h)), nil }

func (h *Hex16) UnmarshalText(b []byte) error {
	v, err := parseHex(b, 4)
	*h = Hex16(v)
	return err
}

// Hex8 is a byte written as two upper-case hex digits.
type Hex8 byte

func (h Hex8) MarshalText() ([]byte, error) { return fmt.Appendf(nil, "%02X", byte(h)), nil }

func (h *Hex8) UnmarshalText(b []byte) error {
	v, err := parseHex(b, 2)
	*h = Hex8(v)
	return err
}

func parseHex(b []byte, digits int) (uint64, error) {
	v, err := strconv.ParseUint(string(b), 16, 4*digits)
	if err != nil || len(b) != digits {
		return 0, fmt.Errorf("%w: %q is not %d hex digits", ErrInvalid, b, digits)
	}
	return v, nil
}
