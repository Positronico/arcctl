package hidio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/positronico/arcctl/internal/wire"
)

// A transcript is JSON Lines: one Header, then one Entry per line.
const transcriptVersion = 1

type Header struct {
	Version  int       `json:"transcript"`
	Source   string    `json:"source,omitempty"`
	Redacted bool      `json:"redacted,omitempty"`
	Started  time.Time `json:"started"`
}

type Dir string

const (
	DirOut      Dir = "out"       // a packet written as output report 8
	DirIn       Dir = "in"        // an input report
	DirOutError Dir = "out-error" // the previous write failed with Err
	DirInError  Dir = "in-error"  // the input side stopped with Err
	DirNote     Dir = "note"      // free text; replay skips it
)

type Entry struct {
	At   time.Time `json:"t"`
	Dir  Dir       `json:"dir"`
	ID   byte      `json:"id,omitzero"`
	Data Frame     `json:"data,omitzero"`
	Err  string    `json:"err,omitempty"`
	Text string    `json:"text,omitempty"`
}

const maxFrame = 64

// Frame is the bytes of a packet or report. Bytes whose bit is set in Mask were
// redacted: they read as zero here and as "xx" in a transcript.
type Frame struct {
	Bytes []byte
	Mask  uint64
}

func (f Frame) IsZero() bool { return len(f.Bytes) == 0 }

func (f Frame) Redacted(i int) bool { return i < maxFrame && f.Mask&(1<<i) != 0 }

func (f Frame) String() string {
	var b strings.Builder
	for i, x := range f.Bytes {
		if i > 0 {
			b.WriteByte(' ')
		}
		if f.Redacted(i) {
			b.WriteString("xx")
		} else {
			fmt.Fprintf(&b, "%02x", x)
		}
	}
	return b.String()
}

func (f Frame) MarshalText() ([]byte, error) {
	if len(f.Bytes) > maxFrame {
		return nil, fmt.Errorf("hidio: frame of %d bytes, max %d", len(f.Bytes), maxFrame)
	}
	return []byte(f.String()), nil
}

func (f *Frame) UnmarshalText(text []byte) error {
	fields := strings.Fields(string(text))
	if len(fields) > maxFrame {
		return fmt.Errorf("hidio: frame of %d bytes, max %d", len(fields), maxFrame)
	}
	out := Frame{Bytes: make([]byte, len(fields))}
	for i, s := range fields {
		if s == "xx" {
			out.Mask |= 1 << i
			continue
		}
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil || len(s) != 2 {
			return fmt.Errorf("hidio: frame byte %d is %q, want two hex digits or xx", i, s)
		}
		out.Bytes[i] = byte(v)
	}
	*f = out
	return nil
}

var errTranscript = errors.New("hidio: bad transcript")

// ReadTranscript parses a transcript written by a Recorder.
func ReadTranscript(r io.Reader) (Header, []Entry, error) {
	var h Header
	var entries []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if h.Version == 0 {
			if err := dec.Decode(&h); err != nil {
				return h, nil, fmt.Errorf("%w: line %d: %w", errTranscript, line, err)
			}
			if h.Version != transcriptVersion {
				return h, nil, fmt.Errorf("%w: line %d: version %d, want %d", errTranscript, line, h.Version, transcriptVersion)
			}
			continue
		}
		var e Entry
		if err := dec.Decode(&e); err != nil {
			return h, nil, fmt.Errorf("%w: line %d: %w", errTranscript, line, err)
		}
		if err := e.validate(); err != nil {
			return h, nil, fmt.Errorf("%w: line %d: %w", errTranscript, line, err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return h, nil, err
	}
	if h.Version == 0 {
		return h, nil, fmt.Errorf("%w: no header", errTranscript)
	}
	return h, entries, nil
}

func (e Entry) validate() error {
	switch e.Dir {
	case DirOut:
		if e.ID != wire.ReportID || len(e.Data.Bytes) != wire.Size {
			return errors.New("an out entry must be report 8 with 16 bytes")
		}
	case DirIn:
	case DirOutError, DirInError:
		if e.Err == "" {
			return fmt.Errorf("%s entry without err", e.Dir)
		}
	case DirNote:
	default:
		return fmt.Errorf("unknown dir %q", e.Dir)
	}
	return nil
}

func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}
