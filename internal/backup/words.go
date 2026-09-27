package backup

import (
	"fmt"
	"strconv"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
)

// Words says what the record at e holds in im, in words: a button's action,
// a shortcut's keys, a macro's name, a DPI or a setting's value. It falls
// back to the state of the record, or its bytes.
func Words(m *catalog.Model, im *flash.Image, e flash.Extent, os keys.OS) string {
	if !im.Known(flash.Extent{Addr: e.Addr, Len: 1}) {
		return "not read"
	}
	c := mouse.Decode(m, im)
	for k := range mouse.Slots {
		if x, _ := mouse.KeyFnExtent(k); x == e {
			return Action(m, &c, k, os)
		}
		if x, _ := mouse.ShortcutExtent(k); x.Addr == e.Addr {
			if c.ShortcutClass[k] != flash.SlotValid {
				return c.ShortcutClass[k].String()
			}
			combo, preset := ComboLabel(c.Shortcuts[k], os)
			if preset != "" {
				return preset + " (" + combo + ")"
			}
			return combo
		}
		if x, _ := mouse.MacroExtent(k); x.Addr == e.Addr {
			if mac := c.Macros[k]; mac != nil && c.MacroClass[k] == flash.SlotValid {
				return strconv.Quote(mac.Name) + ", " + plural(len(mac.Events), "event", "events")
			}
			return c.MacroClass[k].String()
		}
	}
	for i, s := range c.DPI {
		switch {
		case s.DPIField.Extent == e:
			return fieldWords(s.DPIField, func(int) string {
				if s.DPI.X != s.DPI.Y {
					return fmt.Sprintf("%d x %d", s.DPI.X, s.DPI.Y)
				}
				return strconv.Itoa(s.DPI.X)
			})
		case s.ColorField.Extent == e:
			col := c.DPI[i].Color
			return fieldWords(s.ColorField, func(int) string { return fmt.Sprintf("#%02x%02x%02x", col[0], col[1], col[2]) })
		}
	}
	switch e {
	case c.Rate.Extent:
		return fieldWords(c.Rate, func(v int) string { return strconv.Itoa(v) + " Hz" })
	case c.Stages.Extent:
		return fieldWords(c.Stages, func(v int) string { return plural(v, "stage", "stages") })
	case c.Current.Extent:
		return fieldWords(c.Current, func(v int) string { return "stage " + strconv.Itoa(v+1) })
	}
	for _, f := range c.Hidden {
		if f.Extent == e && len(f.Raw) == 2 && f.State == flash.OK {
			return strconv.Itoa(f.Value)
		}
	}
	b, ok := im.Get(e)
	if !ok {
		return "partly read"
	}
	return rawHex(b)
}

func fieldWords(f mouse.Field, text func(int) string) string {
	switch f.State {
	case flash.OK:
		return text(f.Value)
	case flash.Unknown:
		return "not read"
	case flash.Invalid:
		return "invalid (" + rawHex(f.Raw) + ")"
	}
	return f.State.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
