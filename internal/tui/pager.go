package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
)

// pager scrolls a body of lines above lines pinned to the bottom, such as a
// prompt; when the body is cut, its last row says how much and where.
type pager struct {
	scroll int
	follow bool // bring the focus line into view at the next frame
	over   bool // the last frame cut lines
}

const pagerStep = 10

// key scrolls on s and reports whether it used it.
func (p *pager) key(s string) bool {
	switch s {
	case "pgdown", "space":
		p.scroll += pagerStep
	case "pgup":
		p.scroll = max(0, p.scroll-pagerStep)
	case "home":
		p.scroll = 0
	case "end":
		p.scroll = 1 << 20
	default:
		return false
	}
	p.follow = false
	return true
}

// frame fits body into h rows above pinned, scrolled so that the focus line
// shows after the cursor moved; focus is -1 when there is none.
func (p *pager) frame(c *Context, body, pinned []string, focus, h int) []string {
	room := h - len(pinned)
	if len(body) <= room || room < 3 {
		p.scroll, p.over = 0, false
		return append(body, pinned...)
	}
	p.over = true
	view := room - 1
	if p.follow && focus >= 0 {
		switch {
		case focus < p.scroll:
			p.scroll = focus
		case focus >= p.scroll+view:
			p.scroll = focus - view + 1
		}
	}
	p.follow = false
	p.scroll = max(0, min(p.scroll, len(body)-view))
	out := append([]string(nil), body[p.scroll:p.scroll+view]...)
	above, below := p.scroll, len(body)-p.scroll-view
	up, down := dilArrows(c, "↑", "^"), dilArrows(c, "↓", "v")
	var more string
	switch {
	case above == 0:
		more = fmt.Sprintf("%s %s below: pgdn", down, plural(below, "line", "lines"))
	case below == 0:
		more = fmt.Sprintf("%s %s above: pgup", up, plural(above, "line", "lines"))
	default:
		more = fmt.Sprintf("%s%s %d above, %d below: pgup, pgdn", up, down, above, below)
	}
	out = append(out, c.Styles.Faint.Render(more))
	return append(out, pinned...)
}

// hints is the scroll key, while the last frame cut lines.
func (p *pager) hints() []key.Binding {
	if p.over {
		return []key.Binding{hint("pgdn/pgup", "scroll")}
	}
	return nil
}
