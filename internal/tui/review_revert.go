package tui

import (
	"errors"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/safety"
)

var errNoRevert = errors.New("nothing to revert: the journal holds no write to this mouse")

type reviewRevertMsg struct{}

// ReviewRevert opens the review of a revert of the journal's last change:
// the plan safety.RevertPlan builds from the loaded image, each record
// before and after in words and bytes, its web-compat warnings and tier
// gates, then the session's preflight and the confirmation. Going on calls
// the session's Revert, which reads the records again, plans the same way
// and writes through the preflight, the journal and the read-back.
func ReviewRevert() tea.Msg { return reviewRevertMsg{} }

func (a *App) reviewRevert(c *Context) tea.Cmd {
	switch {
	case c.Mode == ModeReadOnly:
		return Problem(ErrReadOnlyMode)
	case c.Writing():
		return Problem(ErrWriting)
	case journalLast(c) == nil:
		return Problem(errNoRevert)
	}
	a.push(newReview(c, a.api, safety.KindRevert))
	return nil
}

// replanRevert plans the undo of the journal's last run on the loaded image.
// A different last run blocks it, since Revert always undoes the newest.
func (d *reviewDialog) replanRevert(c *Context) {
	last := journalLast(c)
	switch {
	case last == nil:
		d.planErr = errNoRevert
		return
	case d.target != nil && last.ID != d.target.ID:
		d.planErr = fmt.Errorf("the journal's last change is now %s; close this and review again", last.ID)
		return
	}
	d.target = last
	for _, o := range last.Ops {
		d.gateOps = append(d.gateOps, o.Op)
	}
	m := c.Model()
	if m == nil || m.Family != catalog.FamilyMouse || c.Image() == nil {
		d.planErr = errNoMouse
		return
	}
	dev := safety.Device{Identity: c.Snapshot.Identity, Profile: c.MouseOptions().Profile, Image: c.Image(), Layout: mouse.Layout(m)}
	d.plan, d.planErr = safety.RevertPlan(last, dev)
	if errors.Is(d.planErr, plan.ErrUnread) {
		d.preview = true
		d.rows = reviewRows(c, journalUndo(last))
	}
}

// journalUndo is what undoing r writes by the journal alone: each record it
// changed, from the bytes it left back to the bytes before it.
func journalUndo(r *safety.Run) plan.Plan {
	var p plan.Plan
	for _, o := range r.Ops {
		i := slices.IndexFunc(p.Ops, func(x plan.Op) bool { return x.Extent == o.Extent })
		if i < 0 {
			p.Ops = append(p.Ops, plan.Op{Seq: len(p.Ops) + 1, Extent: o.Extent, New: o.Old, Old: o.New, Phase: o.Phase, Tier: o.Tier,
				Desc: "revert: " + o.Desc})
			continue
		}
		x := &p.Ops[i]
		x.Old, x.Phase, x.Tier, x.Desc = o.New, o.Phase, min(x.Tier, o.Tier), "revert: "+o.Desc
	}
	return p
}

// revertLines say which run the revert undoes and what it changed.
func (d *reviewDialog) revertLines(c *Context, w int) []string {
	r := d.target
	if r == nil {
		return nil
	}
	var extents []flash.Extent
	for _, o := range r.Ops {
		if !slices.Contains(extents, o.Extent) {
			extents = append(extents, o.Extent)
		}
	}
	text := fmt.Sprintf("It undoes %s %s, started %s, which changed %s. The revert writes back what they held before it.",
		r.Kind, r.ID, r.Started.UTC().Format("2006-01-02 15:04 UTC"), plural(len(extents), "record", "records"))
	if r.Kind == safety.KindRevert {
		text = fmt.Sprintf("The last change is itself revert %s, of %s: undoing it puts back what %s wrote. It changed %s.",
			r.ID, r.Of, r.Of, plural(len(extents), "record", "records"))
	}
	out := wrap(text, w, "", "")
	if d.preview {
		out = append(out, wrap("No preview from the loaded configuration ("+reviewLower(d.planErr)+
			"): the rows below come from the journal, and the session reads each record again before it writes.", w, "", "")...)
	}
	return out
}

// revertName is what U does now: revert the last apply, or undo the last
// revert, which puts back the apply before it.
func revertName(c *Context) string {
	if r := journalLast(c); r != nil && r.Kind == safety.KindRevert {
		return "undo last revert"
	}
	return "revert last apply"
}
