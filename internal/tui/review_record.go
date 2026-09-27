package tui

import (
	"fmt"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/wire"
)

// reviewRow is one op of the plan as the review shows it: the record before
// and after the op, in words.
type reviewRow struct {
	op       plan.Op
	old, new string
	packets  int
}

// reviewRows decodes each op's old bytes over the image the plan was made
// on, and its new bytes over that image with the whole plan applied, so a
// binding reads the body it will run.
func reviewRows(c *Context, p plan.Plan) []reviewRow {
	m, before := c.Model(), c.Image()
	if before == nil {
		before = flash.New()
	}
	after := before.Clone()
	for _, op := range p.Ops {
		_ = after.Set(op.Extent.Addr, op.New)
	}
	rows := make([]reviewRow, len(p.Ops))
	for i, op := range p.Ops {
		rows[i] = reviewRow{
			op:      op,
			old:     reviewRecordText(m, before, op.Extent, op.Old, c.OS),
			new:     reviewRecordText(m, after, op.Extent, op.New, c.OS),
			packets: reviewPackets(op),
		}
	}
	return rows
}

// reviewDesc is what the op does, in the planner's words.
func reviewDesc(op plan.Op) string {
	if op.Desc == "" {
		return op.Phase.String() + " " + op.Extent.String()
	}
	return op.Desc
}

// reviewPackets is the number of cmd-7 packets an op takes.
func reviewPackets(op plan.Op) int { return (op.Extent.Len + wire.MaxData - 1) / wire.MaxData }

// reviewRecordText says in words what the record at e holds once b is
// written over base; "" when arcctl has no words for it.
func reviewRecordText(m *catalog.Model, base *flash.Image, e flash.Extent, b []byte, os keys.OS) string {
	if m == nil || m.Family != catalog.FamilyMouse {
		return ""
	}
	im := base.Clone()
	if err := im.Set(e.Addr, b); err != nil {
		return ""
	}
	cfg := mouse.Decode(m, im)
	for k := range mouse.Slots {
		if x, _ := mouse.KeyFnExtent(k); x == e {
			name, _ := slotFunction(m, &cfg, k, os)
			return name
		}
		if x, _ := mouse.ShortcutExtent(k); x.Addr == e.Addr {
			if cfg.ShortcutClass[k] != flash.SlotValid {
				return reviewClass(cfg.ShortcutClass[k])
			}
			return comboText(cfg.Shortcuts[k], os)
		}
		if x, _ := mouse.MacroExtent(k); x.Addr == e.Addr {
			if mac := cfg.Macros[k]; mac != nil && cfg.MacroClass[k] == flash.SlotValid {
				return strconv.Quote(mac.Name) + ", " + plural(len(mac.Events), "event", "events")
			}
			return reviewClass(cfg.MacroClass[k])
		}
	}
	for i := range mouse.MaxStages {
		s := cfg.DPI[i]
		switch {
		case s.DPIField.Extent == e:
			if s.DPIField.State != flash.OK {
				return reviewState(s.DPIField.State)
			}
			if s.DPI.Y != s.DPI.X {
				return fmt.Sprintf("%d × %d", s.DPI.X, s.DPI.Y)
			}
			return strconv.Itoa(s.DPI.X)
		case s.ColorField.Extent == e:
			if s.ColorField.State != flash.OK {
				return reviewState(s.ColorField.State)
			}
			return fmt.Sprintf("#%02x%02x%02x", s.Color[0], s.Color[1], s.Color[2])
		}
	}
	switch f := cfg.Rate; {
	case f.Extent == e:
		return reviewField(f, func(v int) string { return strconv.Itoa(v) + " Hz" })
	case cfg.Stages.Extent == e:
		return reviewField(cfg.Stages, func(v int) string { return plural(v, "stage", "stages") })
	case cfg.Current.Extent == e:
		return reviewField(cfg.Current, func(v int) string { return "stage " + strconv.Itoa(v+1) })
	}
	for _, f := range cfg.Hidden {
		if f.Extent == e {
			return reviewField(f, strconv.Itoa)
		}
	}
	return ""
}

func reviewField(f mouse.Field, text func(int) string) string {
	if f.State != flash.OK {
		return reviewState(f.State)
	}
	return text(f.Value)
}

func reviewState(s flash.FieldState) string {
	if s == flash.Unknown {
		return "not read"
	}
	return s.String()
}

func reviewClass(c flash.SlotClass) string {
	switch c {
	case flash.SlotEmpty:
		return "empty"
	case flash.SlotInvalid:
		return "invalid"
	case flash.SlotUnknown:
		return "not read"
	}
	return c.String()
}

// reviewBytes shows up to n bytes of b, and how many there are when it cuts.
func reviewBytes(c *Context, b []byte, n int) string {
	s := fmt.Sprintf("% x", b[:min(len(b), n)])
	if len(b) > n {
		s += fmt.Sprintf(" %s (%d bytes)", c.Glyphs.Ellipsis, len(b))
	}
	return s
}

// reviewProfile names an onboard profile; nil is a mouse without profiles.
func reviewProfile(p *byte) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(int(*p))
}
