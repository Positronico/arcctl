package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/library"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/session"
)

type macroReadMsg struct {
	extents []flash.Extent
	err     error
}

// macroSlotsRead is c with the unread bytes of the macro slots that e and
// the pending macros write taken as erased flash, for previews only: the
// slots are read before the review plans them.
func macroSlotsRead(c *Context, e mouse.SetMacro) (*Context, bool) {
	im := c.Image()
	if im == nil {
		return c, false
	}
	slots := []int{e.Slot}
	for _, s := range c.Pending.List() {
		if p, ok := s.Edit.(mouse.SetMacro); ok {
			slots = append(slots, p.Slot)
		}
	}
	var filled *flash.Image
	unread := false
	for _, k := range slots {
		ext, _ := mouse.MacroExtent(k)
		if im.Known(ext) {
			continue
		}
		unread = unread || k == e.Slot
		if filled == nil {
			filled = im.Clone()
		}
		for a := ext.Addr; a < ext.End(); a++ {
			if _, ok := filled.Byte(a); !ok {
				_ = filled.Set(a, []byte{0xFF})
			}
		}
	}
	if filled == nil {
		return c, false
	}
	sn := *c.Snapshot
	sn.Image, sn.DryRun = filled, nil
	cc := *c
	cc.Snapshot = &sn
	return &cc, unread
}

// macroPreview plans binding e as the review will, with the other pending
// edits and the namesakes staged with it, and keeps what concerns its slot.
func macroPreview(c *Context, e mouse.SetMacro, with ...mouse.SetMacro) slotPreview {
	cc, unread := macroSlotsRead(withStaged(c, with...), e)
	pv := previewSlot(cc, e.Slot, e)
	if unread {
		ext, _ := mouse.MacroExtent(e.Slot)
		pv.unread = &ext
	}
	return pv
}

var codecDomain = regexp.MustCompile(`^(?i:value outside the codec domain: )`)

// macroRefusal is why the planner refused binding a macro, in words.
func macroRefusal(err error) string {
	s := codecDomain.ReplaceAllString(editRefusal(err), "")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// withStaged is c with es staged on a copy of its pending edits.
func withStaged(c *Context, es ...mouse.SetMacro) *Context {
	if len(es) == 0 {
		return c
	}
	p := &Pending{edits: c.Pending.List()}
	for _, e := range es {
		p.Stage(Staged{Key: SlotKey(e.Slot), Edit: e})
	}
	cc := *c
	cc.Pending = p
	return &cc
}

// namesakes are the macros besides e's slot that carry e's name with other
// events, pending or on the mouse, which the planner refuses next to e: the
// web app keeps one macro per name. join are those a button runs from its
// own slot, which can take e's events too, each with its own repeat mode;
// blocked are the slots of the others, which only a new name gets past.
func namesakes(c *Context, e mouse.SetMacro) (join []mouse.SetMacro, blocked []int) {
	cfg := c.Config()
	for k := range mouse.Slots {
		if k == e.Slot {
			continue
		}
		if s, ok := c.Pending.Get(SlotKey(k)); ok {
			if p, ok := s.Edit.(mouse.SetMacro); ok {
				if p.Macro.Name == e.Macro.Name && !slices.Equal(p.Macro.Events, e.Macro.Events) {
					join = append(join, mouse.SetMacro{Slot: k, Macro: e.Macro, Cycle: p.Cycle})
				}
				continue
			}
		}
		if cfg == nil {
			continue
		}
		m := cfg.Macros[k]
		if cfg.MacroClass[k] != flash.SlotValid || m == nil || m.Name != e.Macro.Name || slices.Equal(m.Events, e.Macro.Events) {
			continue
		}
		fn := cfg.Keys[k].Fn
		if cfg.Keys[k].Field.State == flash.OK && fn.Type == mouse.TypeMacro && int(fn.Param>>8) == k {
			join = append(join, mouse.SetMacro{Slot: k, Macro: e.Macro, Cycle: int(fn.Param & 0xFF)})
			continue
		}
		blocked = append(blocked, k)
	}
	return join, blocked
}

// errNamesakes is why a macro whose name another macro holds cannot be
// staged without a new name.
func errNamesakes(c *Context, e mouse.SetMacro, blocked []int) error {
	return fmt.Errorf("macro slot %d holds another macro named %q that no button runs from its own slot; the web app keeps one "+
		"macro per name, so rename this one in the editor (enter)", blocked[0], shownName(e.Macro.Name))
}

// errOddName is why a macro whose name the web app's sanitiser would change
// is not staged (§7.7).
func errOddName(name string) error {
	return fmt.Errorf("the name %q holds characters the web app leaves out of names; rename it in the editor (enter) first", shownName(name))
}

// stage makes binding e the pending edit of its slot, or drops the slot's
// pending edit when the button runs that macro already, and reads the
// macro slot when it was never read. with are e's namesakes, staged with
// it. The error is why it cannot.
func (t *Macros) stage(c *Context, e mouse.SetMacro, with ...mouse.SetMacro) (tea.Cmd, error) {
	switch {
	case c.Mode == ModeReadOnly:
		return nil, ErrReadOnlyMode
	case c.Writing():
		return nil, ErrWriting
	case c.Config() == nil:
		return nil, errButtonsUnloaded
	case sanitizeName(e.Macro.Name) != e.Macro.Name:
		return nil, errOddName(e.Macro.Name)
	}
	if err := (library.Entry{Macro: e.Macro, Cycle: e.Cycle}).Check(); err != nil {
		msg := macroRefusal(err)
		if len(shortDelays(e.Macro.Events)) > 0 {
			msg += "; t in the editor raises every such delay to 10 ms"
		}
		return nil, errors.New(msg)
	}
	if fn, ok := editFn(e); ok && losesLeftClick(c, e.Slot, fn) {
		return nil, errLeftClick
	}
	pv := macroPreview(c, e, with...)
	where := buttonWhere(c.Model(), e.Slot)
	text := editText(c.Model(), e, c.OS)
	switch {
	case pv.err != nil:
		return nil, errors.New(macroRefusal(pv.err))
	case pv.ops == 0 && len(with) == 0:
		c.Pending.Drop(SlotKey(e.Slot))
		return Notice(where + " already runs " + text + "; nothing is pending for it."), nil
	}
	c.Pending.Stage(Staged{Key: SlotKey(e.Slot), Desc: where + ": " + text, Edit: e})
	notice := "Pending: " + where + " " + c.Glyphs.Arrow + " " + text
	var also []string
	for _, w := range with {
		ww := buttonWhere(c.Model(), w.Slot)
		c.Pending.Stage(Staged{Key: SlotKey(w.Slot), Desc: ww + ": " + editText(c.Model(), w, c.OS), Edit: w})
		also = append(also, ww)
	}
	if len(also) > 0 {
		notice += ", and the same events for " + strings.Join(also, ", ")
	}
	notice += ". a reviews and applies."
	if pv.unread != nil && c.Snapshot.State != session.Ready {
		notice += " Its macro slot is read once the mouse is ready."
	}
	return tea.Batch(Notice(notice), t.readBodies(c)), nil
}

// readBodies reads the macro slots that pending macros rewrite and that
// were never read in full, while the session is ready and no such read
// runs. A whole slot is read, so that both the new body and the record the
// old one declares are known.
func (t *Macros) readBodies(c *Context) tea.Cmd {
	im := c.Image()
	if t.reading || im == nil || c.Snapshot.State != session.Ready {
		return nil
	}
	var want []flash.Extent
	for _, s := range c.Pending.List() {
		if e, ok := s.Edit.(mouse.SetMacro); ok {
			if ext, _ := mouse.MacroExtent(e.Slot); !im.Known(ext) {
				want = append(want, ext)
			}
		}
	}
	switch {
	case len(want) == 0:
		return nil
	case t.read == nil:
		return Problem(fmt.Errorf("the macro slot at %v was never read; the review cannot plan its macro until it is", want[0]))
	}
	t.reading = true
	read := t.read
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), bodyReadTimeout)
		defer cancel()
		_, err := read(ctx, want...)
		return macroReadMsg{want, err}
	}
}
