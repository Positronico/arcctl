package session

import (
	"time"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/wire"
)

// A StatusChanged push (cmd 10) carries two flag bytes; each mouse flag names
// the flash range to read again. The keyboard flags mean other things.
const (
	pushProfile = 0x04 // byte 5: the onboard profile changed; reload everything
	pushBattery = 0x40 // byte 5: poll the battery
)

var pushRereads = [2][]struct {
	bit byte
	e   flash.Extent
}{
	{{0x01, flash.Extent{Addr: 4, Len: 2}}, {0x02, flash.Extent{Addr: 0, Len: 2}}, {0x08, flash.Extent{Addr: 76, Len: 8}}, {0x20, flash.Extent{Addr: 160, Len: 7}}},
	{{0x01, flash.Extent{Addr: 10, Len: 2}}, {0x02, flash.Extent{Addr: 169, Len: 2}}, {0x04, flash.Extent{Addr: 171, Len: 2}}, {0x08, flash.Extent{Addr: 233, Len: 6}}, {0x10, flash.Extent{Addr: 225, Len: 2}}},
}

func rereads(f1, f2 byte) []flash.Extent {
	var out []flash.Extent
	for i, f := range [2]byte{f1, f2} {
		for _, r := range pushRereads[i] {
			if f&r.bit != 0 {
				out = append(out, r.e)
			}
		}
	}
	return out
}

func (s *Session) onPush(l *link, p wire.Packet) {
	s.stats.Pushes++
	f1, f2 := p[5], p[6]
	s.log.Info("status changed", "flags", []byte{f1, f2}, "path", l.c.Path)
	if l != s.dev || p.Status() != wire.StatusOK {
		return
	}
	if s.base == Offline {
		s.wakeHint = true
	}
	if s.model == nil || s.model.Family != catalog.FamilyMouse {
		return
	}
	if f1&pushBattery != 0 {
		s.due.battery = time.Now()
	}
	if f1&pushProfile != 0 {
		s.profileSwitched()
		return
	}
	if e := rereads(f1, f2); len(e) > 0 {
		s.addJob(&job{kind: jobReread, want: e})
	}
}
