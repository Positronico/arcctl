package backup

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/positronico/arcctl/internal/flash"
)

// Kind is what a configuration file turned out to be.
type Kind uint8

const (
	KindBackup Kind = iota + 1 // an arcctl backup
	KindBin                    // the web app's .bin export
	KindDump                   // raw flash bytes from address 0, such as a 256-byte settings page
)

var kindNames = [...]string{KindBackup: "backup", KindBin: "web .bin", KindDump: "dump"}

func (k Kind) String() string {
	if int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return fmt.Sprintf("kind %d", k)
}

var ErrUnknownFile = errors.New("backup: not a backup, web .bin or flash dump")

// Source is a configuration read from a file of any Kind. Image holds the
// bytes the file captured; everything else is unknown.
type Source struct {
	Kind  Kind
	Path  string
	File  *File // KindBackup
	Bin   *Bin  // KindBin
	Image *flash.Image
}

// Open reads a backup, a web .bin or a raw dump.
func Open(path string) (*Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Path = path
	return s, nil
}

func Parse(b []byte) (*Source, error) {
	if t := bytes.TrimLeft(b, " \t\r\n"); len(t) > 0 && t[0] == '{' {
		f, err := Decode(b)
		if err != nil {
			return nil, err
		}
		return &Source{Kind: KindBackup, File: f, Image: f.Image()}, nil
	}
	if len(b) == BinSize {
		bin, err := ParseBin(b)
		if err != nil {
			return nil, err
		}
		return &Source{Kind: KindBin, Bin: bin, Image: bin.Image}, nil
	}
	if len(b) == 0 || len(b) > flash.Size {
		return nil, fmt.Errorf("%w (%d bytes)", ErrUnknownFile, len(b))
	}
	im, err := flash.FromDump(0, b)
	if err != nil {
		return nil, err
	}
	return &Source{Kind: KindDump, Image: im}, nil
}
