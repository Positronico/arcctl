package cli

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/positronico/arcctl/internal/backup"
	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/keys"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

func runDiff(r *runner, args []string) error {
	fs := r.flagSet("diff", synopsis("diff"))
	pos, err := r.parse(fs, args, 1, 2)
	if err != nil {
		return err
	}
	a, err := backup.Open(pos[0])
	if err != nil {
		return err
	}
	m, err := r.modelOf(a)
	if err != nil {
		return err
	}
	if m.Family != catalog.FamilyMouse {
		return fail(ExitFailure, "keyboard backups are compared from M8", "")
	}
	var b *flash.Image
	var bName string
	if len(pos) == 2 {
		src, err := backup.Open(pos[1])
		if err != nil {
			return err
		}
		mb, err := r.modelOf(src)
		if err != nil {
			return err
		}
		if mb.Key != m.Key {
			fmt.Fprintf(r.errw, "arcctl: the files are for different models (%s, %s); comparing as %s\n", m.Key, mb.Key, m.Key)
		}
		b, bName = src.Image, describeSource(src)
	} else {
		c, sn, err := r.connect(loaded)
		if err != nil {
			return err
		}
		defer c.stop()
		if err := loadable(sn); err != nil {
			return err
		}
		if sn.Model.Key != m.Key {
			fmt.Fprintf(r.errw, "arcctl: the device is a %s, the file is for %s; comparing as %s\n", sn.Model.Key, m.Key, m.Key)
		}
		var cp session.Capture
		err = r.do(c, func(ctx context.Context) error {
			var err error
			cp, err = c.s.Read(ctx, a.Image.KnownExtents()...)
			return err
		})
		if err != nil {
			return readError(err)
		}
		b, bName = cp.Image, "the device ("+sn.Identity.Key()+")"
	}
	fmt.Fprintf(r.out, "a: %s\nb: %s\n\n", describeSource(a), bName)
	res := compare(m, a.Image, b, r.keyOS())
	var rows [][]string
	for _, d := range res.changed {
		rows = append(rows, []string{d.name, d.from, "->", d.to})
	}
	if len(rows) == 0 {
		fmt.Fprintln(r.out, "No differences.")
	} else {
		table(r.out, "  ", rows)
		fmt.Fprintln(r.out)
	}
	fmt.Fprintf(r.out, "%d differ, %d equal, %d not compared (read on one side only)\n", len(res.changed), res.equal, res.skipped)
	return nil
}

func describeSource(s *backup.Source) string {
	switch s.Kind {
	case backup.KindBackup:
		return s.Path + " (" + s.File.Created.UTC().Format(time.DateTime) + " UTC)"
	case backup.KindBin:
		return s.Path + " (web .bin)"
	}
	return s.Path + " (dump)"
}

type change struct{ name, from, to string }

type comparison struct {
	changed []change
	equal   int
	skipped int
}

// compare goes record by record over the settings page, the shortcut and
// macro slots, and then byte by byte over everything else both images know.
func compare(m *catalog.Model, a, b *flash.Image, os keys.OS) comparison {
	ca, cb := mouse.Decode(m, a), mouse.Decode(m, b)
	var res comparison
	field := func(name string, fa, fb mouse.Field, text func(*mouse.Config) string) {
		switch {
		case fa.Raw == nil || fb.Raw == nil:
			if fa.Raw != nil || fb.Raw != nil {
				res.skipped++
			}
		case bytes.Equal(fa.Raw, fb.Raw):
			res.equal++
		default:
			from, to := fieldText(fa), fieldText(fb)
			if text != nil && fa.State == flash.OK && fb.State == flash.OK {
				from, to = text(&ca), text(&cb)
			}
			res.changed = append(res.changed, change{name, from, to})
		}
	}
	field("Report rate", ca.Rate, cb.Rate, func(c *mouse.Config) string { return strconv.Itoa(c.Rate.Value) + " Hz" })
	field("DPI stages", ca.Stages, cb.Stages, func(c *mouse.Config) string { return strconv.Itoa(c.Stages.Value) })
	field("Current stage", ca.Current, cb.Current, func(c *mouse.Config) string { return strconv.Itoa(c.Current.Value + 1) })
	for i := range ca.DPI {
		field(fmt.Sprintf("DPI stage %d", i+1), ca.DPI[i].DPIField, cb.DPI[i].DPIField, func(c *mouse.Config) string {
			d := c.DPI[i].DPI
			if d.X == d.Y {
				return strconv.Itoa(d.X)
			}
			return fmt.Sprintf("%d x %d", d.X, d.Y)
		})
		field(fmt.Sprintf("Colour %d", i+1), ca.DPI[i].ColorField, cb.DPI[i].ColorField, func(c *mouse.Config) string {
			col := c.DPI[i].Color
			return fmt.Sprintf("#%02x%02x%02x", col[0], col[1], col[2])
		})
	}
	for k := range ca.Keys {
		field(fmt.Sprintf("Button slot %d", k), ca.Keys[k].Field, cb.Keys[k].Field, func(c *mouse.Config) string {
			return backup.Action(m, c, k, os)
		})
	}
	for k := range mouse.Slots {
		res.body(fmt.Sprintf("Shortcut %d", k), ca.ShortcutClass[k], cb.ShortcutClass[k],
			shortcutText(ca.Shortcuts[k], os), shortcutText(cb.Shortcuts[k], os),
			reflect.DeepEqual(ca.Shortcuts[k], cb.Shortcuts[k]), slotBytes(a, mouse.ShortcutExtent, k), slotBytes(b, mouse.ShortcutExtent, k))
	}
	for k := range mouse.Slots {
		res.body(fmt.Sprintf("Macro %d", k), ca.MacroClass[k], cb.MacroClass[k],
			macroText(ca.Macros[k]), macroText(cb.Macros[k]),
			reflect.DeepEqual(ca.Macros[k], cb.Macros[k]), slotBytes(a, mouse.MacroExtent, k), slotBytes(b, mouse.MacroExtent, k))
	}
	for i := range ca.Hidden {
		fa, fb := ca.Hidden[i], cb.Hidden[i]
		field(fmt.Sprintf("%s @%d", fa.Name, fa.Extent.Addr), fa, fb, nil)
	}
	res.rest(a, b)
	return res
}

func (res *comparison) body(name string, ka, kb flash.SlotClass, ta, tb string, same bool, ra, rb []byte) {
	switch {
	case ka == flash.SlotUnknown || kb == flash.SlotUnknown:
		if ka != kb {
			res.skipped++
		}
	case ka == flash.SlotValid && kb == flash.SlotValid && same,
		ka == flash.SlotEmpty && kb == flash.SlotEmpty,
		ka == flash.SlotInvalid && kb == flash.SlotInvalid && bytes.Equal(ra, rb):
		res.equal++
	default:
		if ka != flash.SlotValid {
			ta = ka.String()
		}
		if kb != flash.SlotValid {
			tb = kb.String()
		}
		if ta == tb {
			tb += ", other events"
		}
		res.changed = append(res.changed, change{name, ta, tb})
	}
}

// rest compares what lies past the slots (the extended block, the keyboard
// CRC area and anything else both images know) run by run: each run of bytes
// known on both sides counts as one record, and a run known on one side only
// is not compared.
func (res *comparison) rest(a, b *flash.Image) {
	last, _ := mouse.MacroExtent(mouse.Slots - 1)
	status := func(addr int) (x, y byte, okA, okB bool) {
		x, okA = a.Byte(addr)
		y, okB = b.Byte(addr)
		return
	}
	for addr := last.End(); addr < flash.Size; {
		_, _, okA, okB := status(addr)
		start := addr
		for addr < flash.Size {
			_, _, a2, b2 := status(addr)
			if a2 != okA || b2 != okB {
				break
			}
			addr++
		}
		switch {
		case okA && okB:
			res.run(a, b, flash.Extent{Addr: start, Len: addr - start})
		case okA || okB:
			res.skipped++
		}
	}
}

// run compares one run both images know and reports each stretch that differs.
func (res *comparison) run(a, b *flash.Image, e flash.Extent) {
	ra, _ := a.Get(e)
	rb, _ := b.Get(e)
	if bytes.Equal(ra, rb) {
		res.equal++
		return
	}
	for i := 0; i < len(ra); i++ {
		if ra[i] == rb[i] {
			continue
		}
		j := i
		for j < len(ra) && ra[j] != rb[j] {
			j++
		}
		d := flash.Extent{Addr: e.Addr + i, Len: j - i}
		res.changed = append(res.changed, change{"Bytes " + d.String(), clip(ra[i:j]), clip(rb[i:j])})
		i = j
	}
}

func clip(b []byte) string {
	const n = 8
	s := fmt.Sprintf("% x", b[:min(len(b), n)])
	if len(b) > n {
		s += " ..."
	}
	return s
}

func fieldText(f mouse.Field) string {
	if f.State == flash.OK {
		return clip(f.Raw)
	}
	return f.State.String() + " (" + clip(f.Raw) + ")"
}

func slotBytes(im *flash.Image, table func(int) (flash.Extent, bool), k int) []byte {
	e, _ := table(k)
	var out []byte
	for a := e.Addr; a < e.End(); a++ {
		b, ok := im.Byte(a)
		if !ok {
			break
		}
		out = append(out, b)
	}
	return out
}

func shortcutText(c keys.Combo, os keys.OS) string {
	if c == nil {
		return ""
	}
	combo, preset := backup.ComboLabel(c, os)
	if preset != "" {
		return combo + " (" + preset + ")"
	}
	return combo
}

func macroText(m *mouse.Macro) string {
	if m == nil {
		return ""
	}
	return strconv.Quote(m.Name) + ", " + strconv.Itoa(len(m.Events)) + " events"
}
