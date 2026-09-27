package plan

import (
	"bytes"
	"slices"
	"strconv"

	"github.com/positronico/arcctl/internal/flash"
)

type Table struct{ Base, Stride, Count int }

type Layout struct {
	Bindings  Table
	Shortcuts Table
	Macros    Table
	// Buttons are the binding slots wired to physical buttons; only they count as a remaining Left Click.
	Buttons []int
	Frozen  []flash.Extent
	// Records are the only extents outside the tables a plan may write, each as a
	// whole, except for captured bytes (Capturable).
	Records []flash.Extent
}

type kind uint8

const (
	kindRecord kind = iota
	kindBinding
	kindShortcut
	kindMacro
	kindCaptured
)

var kindNames = [...]string{"record", "binding", "shortcut", "macro", "captured bytes"}

type ref struct {
	kind kind
	slot int
}

func (r ref) String() string {
	if r.kind == kindRecord || r.kind == kindCaptured {
		return kindNames[r.kind]
	}
	return kindNames[r.kind] + " " + strconv.Itoa(r.slot)
}

var (
	disable   = []byte{0x00, 0x00, 0x00, 0x55}
	leftClick = []byte{0x01, 0x01, 0x00, 0x53}
)

const (
	typeShortcut = 5
	typeMacro    = 6
)

// frame returns the bytes before a body's event count and the size of one event.
// The checksum runs from the count to the end of the record.
func frame(k kind) (head, event int) {
	if k == kindMacro {
		return 31, 5
	}
	return 0, 3
}

func (t Table) slot(i int) flash.Extent {
	return flash.Extent{Addr: t.Base + i*t.Stride, Len: t.Stride}
}

func (t Table) region() flash.Extent { return flash.Extent{Addr: t.Base, Len: t.Stride * t.Count} }

func (l Layout) table(k kind) Table {
	return [...]Table{kindBinding: l.Bindings, kindShortcut: l.Shortcuts, kindMacro: l.Macros}[k]
}

func (l Layout) check() error {
	bad := func(detail string) error { return newError(ErrMalformed, "layout: "+detail) }
	if l.Bindings.Stride != len(disable) {
		return bad("bindings stride must be 4")
	}
	regions := make([]flash.Extent, 0, 3)
	for _, k := range []kind{kindBinding, kindShortcut, kindMacro} {
		t := l.table(k)
		head, event := frame(k)
		switch {
		case t.Count < 1 || t.Stride < 1 || t.Base < 0 || t.Base > flash.Size || t.Count > (flash.Size-t.Base)/t.Stride:
			return bad(kindNames[k] + " table outside the image")
		case k != kindBinding && t.Stride < head+2+event:
			return bad(kindNames[k] + " slots too small for one event")
		case k != kindBinding && t.Count < l.Bindings.Count:
			return bad(kindNames[k] + " table has fewer slots than bindings")
		}
		for _, r := range regions {
			if r.Overlaps(t.region()) {
				return bad(kindNames[k] + " table overlaps another table")
			}
		}
		regions = append(regions, t.region())
	}
	if len(l.Buttons) == 0 {
		return bad("no buttons")
	}
	for i, b := range l.Buttons {
		if b < 0 || b >= l.Bindings.Count || slices.Contains(l.Buttons[:i], b) {
			return bad("button " + strconv.Itoa(b) + " is out of range or repeated")
		}
	}
	if len(l.Frozen) == 0 {
		return bad("no frozen extents")
	}
	for _, f := range l.Frozen {
		if !inImage(f) {
			return bad("frozen extent " + f.String() + " outside the image")
		}
	}
	if len(l.Records) == 0 {
		return bad("no records")
	}
	for i, r := range l.Records {
		switch {
		case !inImage(r) || r.Len < 2:
			return bad("record " + r.String() + " is outside the image or shorter than a pair")
		case slices.ContainsFunc(regions, r.Overlaps):
			return bad("record " + r.String() + " overlaps a table")
		case slices.ContainsFunc(l.Frozen, r.Overlaps):
			return bad("record " + r.String() + " overlaps a frozen extent")
		case slices.ContainsFunc(l.Records[:i], r.Overlaps):
			return bad("record " + r.String() + " overlaps another record")
		}
	}
	return nil
}

func (l Layout) classify(e flash.Extent) (ref, bool) {
	for _, k := range []kind{kindBinding, kindShortcut, kindMacro} {
		t := l.table(k)
		if !t.region().Overlaps(e) {
			continue
		}
		if e.Addr < t.Base {
			return ref{}, false
		}
		i := (e.Addr - t.Base) / t.Stride
		s := t.slot(i)
		if e.Addr != s.Addr || !s.Contains(e) || (k == kindBinding && e != s) {
			return ref{}, false
		}
		return ref{k, i}, true
	}
	return ref{kind: kindRecord}, slices.Contains(l.Records, e)
}

// Capturable reports whether a plan may write captured bytes over e: e lies
// in the settings page, before the body tables, clear of every table and
// frozen extent, and is either one of Records or clear of all of them.
func (l Layout) Capturable(e flash.Extent) bool {
	if !inImage(e) || e.End() > min(l.Shortcuts.Base, l.Macros.Base) || slices.ContainsFunc(l.Frozen, e.Overlaps) {
		return false
	}
	for _, k := range []kind{kindBinding, kindShortcut, kindMacro} {
		if l.table(k).region().Overlaps(e) {
			return false
		}
	}
	return slices.Contains(l.Records, e) || !slices.ContainsFunc(l.Records, e.Overlaps)
}

func targets(slot int, b []byte) []ref {
	switch b[0] {
	case typeShortcut:
		return []ref{{kindShortcut, slot}}
	case typeMacro:
		if int(b[1]) == slot {
			return []ref{{kindMacro, slot}}
		}
		return []ref{{kindMacro, slot}, {kindMacro, int(b[1])}}
	}
	return nil
}

// Unread returns what im lacks before a binding at slot holding b can be
// checked: the event count of a body b points at, then the whole record that
// count declares. ok is false once every such body is known, or holds no
// valid record whatever its unread bytes.
func (l Layout) Unread(im *flash.Image, slot int, b []byte) (e flash.Extent, ok bool) {
	if slot < 0 || slot >= l.Bindings.Count || len(b) != len(disable) {
		return flash.Extent{}, false
	}
	for _, t := range targets(slot, b) {
		tb := l.table(t.kind)
		if t.slot < 0 || t.slot >= tb.Count {
			continue
		}
		head, event := frame(t.kind)
		s := tb.slot(t.slot)
		n, known := im.Byte(s.Addr + head)
		if !known {
			return flash.Extent{Addr: s.Addr, Len: head + 1}, true
		}
		size := head + 2 + event*int(n)
		if n == 0 || size > s.Len {
			continue
		}
		if rec := (flash.Extent{Addr: s.Addr, Len: size}); !im.Known(rec) {
			return rec, true
		}
	}
	return flash.Extent{}, false
}

func (l Layout) body(im *flash.Image, r ref) (known, valid bool) {
	t := l.table(r.kind)
	if r.slot < 0 || r.slot >= t.Count {
		return true, false
	}
	head, event := frame(r.kind)
	s := t.slot(r.slot)
	n, ok := im.Byte(s.Addr + head)
	if !ok {
		return false, false
	}
	size := head + 2 + event*int(n)
	if n == 0 || size > s.Len {
		return true, false
	}
	b, ok := im.Get(flash.Extent{Addr: s.Addr, Len: size})
	if !ok {
		return false, false
	}
	return true, sum(b[head:]) == 0x55
}

func (l Layout) leftClicks(im *flash.Image) int {
	n := 0
	for _, s := range l.Buttons {
		if b, ok := im.Get(l.Bindings.slot(s)); ok && bytes.Equal(b, leftClick) {
			n++
		}
	}
	return n
}

func inImage(e flash.Extent) bool {
	return e.Addr >= 0 && e.Len > 0 && e.Addr <= flash.Size-e.Len
}

func empty(b []byte) bool {
	return len(b) > 0 && (bytes.Count(b, []byte{0x00}) == len(b) || bytes.Count(b, []byte{0xFF}) == len(b))
}

func sum(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return s
}
