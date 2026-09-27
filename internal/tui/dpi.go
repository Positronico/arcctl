package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
)

// DPITab edits the DPI stages (§7.6): the stage count, the current stage and
// each stage's value. Its edits go to the pending store.
type DPITab struct {
	sel     int
	typing  bool
	typed   string
	details bool // raw records and, on office mice, the stage colours
	check   dpiCheck
	auto    *dpiAuto
}

// dpiAuto is the current stage a lower stage count moved, and the
// current-stage edit it replaced, nil when there was none.
type dpiAuto struct {
	stage int
	prev  *Staged
}

// dpiCheck caches the planner's verdict on the staged DPI edits.
type dpiCheck struct {
	rev int
	im  *flash.Image
	err error
	ok  bool
}

func NewDPITab() *DPITab { return &DPITab{} }

func (t *DPITab) Title() string { return "DPI" }

func (t *DPITab) Needs() []mouse.Feature {
	return []mouse.Feature{mouse.FeatureStages, mouse.FeatureCurrent, mouse.FeatureDPI}
}

func (t *DPITab) Capturing() bool { return t.typing }

const (
	dpiMaxDigits = 5
	dpiBarMax    = 48
)

var errNothingLoaded = errors.New("nothing is loaded yet")

// dpiState is what the tab shows: the device's DPI settings with the staged
// edits on top. A count or current of -1 is unknown.
type dpiState struct {
	m        *catalog.Model
	cfg      *mouse.Config
	legal    []int
	limit    int
	devCount int
	count    int
	devCur   int
	current  int
	staged   map[int]int
}

func dpiRead(c *Context) (dpiState, bool) {
	m, cfg := c.Model(), c.Config()
	if m == nil || cfg == nil || m.Sensor == nil || m.Stages < 1 {
		return dpiState{}, false
	}
	s := dpiState{m: m, cfg: cfg, legal: dpiLegal(m), limit: dpiLimit(m), staged: map[int]int{}}
	s.devCount, s.devCur = -1, -1
	if cfg.Stages.State == flash.OK {
		s.devCount = cfg.Stages.Value
	}
	if cfg.Current.State == flash.OK {
		s.devCur = cfg.Current.Value
	}
	s.count, s.current = s.devCount, s.devCur
	if st, ok := c.Pending.Get(StagesKey); ok {
		if e, ok := st.Edit.(mouse.SetStages); ok {
			s.count = e.Count
		}
	}
	if st, ok := c.Pending.Get(CurrentKey); ok {
		if e, ok := st.Edit.(mouse.SetCurrent); ok {
			s.current = e.Stage
		}
	}
	for i := range m.Stages {
		if st, ok := c.Pending.Get(DPIKey(i)); ok {
			if e, ok := st.Edit.(mouse.SetDPI); ok {
				s.staged[i] = e.DPI
			}
		}
	}
	return s, true
}

// dpiLimit is the model's highest DPI: its own maximum, capped by the sensor.
func dpiLimit(m *catalog.Model) int {
	limit := 0
	if s := m.Sensor; s != nil && len(s.Ranges) > 0 {
		limit = s.Ranges[len(s.Ranges)-1].Max
	}
	if m.MaxDPI > 0 && (limit == 0 || m.MaxDPI < limit) {
		limit = m.MaxDPI
	}
	return limit
}

// dpiLegal lists every DPI the model accepts, in order.
func dpiLegal(m *catalog.Model) []int {
	limit := dpiLimit(m)
	var out []int
	for _, d := range mouse.DPIs(m.Sensor) {
		if d <= limit {
			out = append(out, d)
		}
	}
	return out
}

// dpiSnap moves v up to the next legal value, as the web app does with a
// typed value; above the highest it gives the highest.
func dpiSnap(legal []int, v int) int {
	i, _ := slices.BinarySearch(legal, v)
	if i == len(legal) {
		return legal[len(legal)-1]
	}
	return legal[i]
}

// dpiNext is the legal value one step from v in direction dir, staying at
// the ends.
func dpiNext(legal []int, v, dir int) int {
	i, found := slices.BinarySearch(legal, v)
	switch {
	case dir > 0 && found:
		i++
	case dir < 0:
		i--
	}
	return legal[max(0, min(i, len(legal)-1))]
}

// dpiRanges describes the legal values up to the model's limit.
func dpiRanges(c *Context, m *catalog.Model) string {
	limit := dpiLimit(m)
	var parts []string
	for _, r := range m.Sensor.Ranges {
		if r.Min > limit || r.Step <= 0 {
			continue
		}
		hi := min(r.Max, limit)
		hi -= (hi - r.Min) % r.Step
		parts = append(parts, fmt.Sprintf("%d-%d step %d", r.Min, hi, r.Step))
	}
	return strings.Join(parts, " "+c.Glyphs.Bullet+" ")
}

func (t *DPITab) Update(c *Context, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if _, loaded := dpiRead(c); !loaded {
			t.typing = false
		}
		return nil
	}
	s, loaded := dpiRead(c)
	if loaded {
		t.sel = max(0, min(t.sel, s.m.Stages-1))
	}
	if t.typing {
		return t.typeKey(c, s, k)
	}
	switch k.String() {
	case "x":
		t.details = !t.details
		return nil
	case "up", "k":
		t.sel = max(0, t.sel-1)
		return nil
	case "down", "j":
		if loaded {
			t.sel = min(s.m.Stages-1, t.sel+1)
		}
		return nil
	}
	if !loaded {
		switch k.String() {
		case "left", "h", "right", "l", "enter", "space", "+", "=", "-", "_", "backspace", "delete":
			return Problem(errNothingLoaded)
		}
		return nil
	}
	switch k.String() {
	case "left", "h":
		return t.step(c, s, -1)
	case "right", "l":
		return t.step(c, s, 1)
	case "enter":
		if err := t.editable(c, s, mouse.FeatureDPI); err != nil {
			return Problem(err)
		}
		t.typing, t.typed = true, ""
	case "space":
		t.auto = nil
		return t.setCurrent(c, s, t.sel)
	case "+", "=":
		return t.setCount(c, s, 1)
	case "-", "_":
		return t.setCount(c, s, -1)
	case "backspace", "delete":
		if c.Pending.Drop(DPIKey(t.sel)) {
			return Notice(fmt.Sprintf("Stage %d is back to the value on the mouse.", t.sel+1))
		}
	}
	return nil
}

func (t *DPITab) typeKey(c *Context, s dpiState, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		t.typing = false
	case "backspace":
		if n := len(t.typed); n > 0 {
			t.typed = t.typed[:n-1]
		}
	case "enter":
		t.typing = false
		v, err := strconv.Atoi(t.typed)
		if err != nil || s.m == nil {
			return nil
		}
		got := dpiSnap(s.legal, v)
		cmd := t.setDPI(c, s, t.sel, got)
		switch {
		case cmd != nil:
			return cmd
		case got < v:
			return Notice(fmt.Sprintf("%d is above the %d DPI maximum; stage %d gets %d.", v, s.limit, t.sel+1, got))
		case got > v:
			return Notice(fmt.Sprintf("%d snaps up to %d, the next legal value.", v, got))
		}
	default:
		if r := k.Text; len(r) == 1 && r[0] >= '0' && r[0] <= '9' && len(t.typed) < dpiMaxDigits {
			t.typed += r
		}
	}
	return nil
}

// editable says why the tab cannot stage an edit of f now.
func (t *DPITab) editable(c *Context, s dpiState, f mouse.Feature) error {
	switch {
	case c.Mode == ModeReadOnly:
		return ErrReadOnlyMode
	case c.Writing():
		return ErrWriting
	}
	if tier, ok := c.Tier(f); !ok {
		return fmt.Errorf("%s is %s on the %s", f, tier, modelName(s.m))
	}
	return nil
}

func (t *DPITab) step(c *Context, s dpiState, dir int) tea.Cmd {
	if err := t.editable(c, s, mouse.FeatureDPI); err != nil {
		return Problem(err)
	}
	from := 0
	switch st := s.cfg.DPI[t.sel]; {
	case s.staged[t.sel] > 0:
		from = s.staged[t.sel]
	case st.DPIField.State == flash.OK:
		from = st.DPI.X
	case st.DPIField.State == flash.Unknown:
		return Problem(fmt.Errorf("stage %d was not read; reload with r", t.sel+1))
	default:
		dir = 0
	}
	return t.setDPI(c, s, t.sel, dpiNext(s.legal, from, dir))
}

// setDPI stages stage i at v, or drops its edit when the mouse holds v on
// both axes already.
func (t *DPITab) setDPI(c *Context, s dpiState, i, v int) tea.Cmd {
	if err := t.editable(c, s, mouse.FeatureDPI); err != nil {
		return Problem(err)
	}
	st := s.cfg.DPI[i]
	if st.DPIField.State == flash.Unknown {
		return Problem(fmt.Errorf("stage %d was not read; reload with r", i+1))
	}
	if st.DPIField.State == flash.OK && st.DPI.X == v && st.DPI.Y == v {
		c.Pending.Drop(DPIKey(i))
		return nil
	}
	c.Pending.Stage(Staged{Key: DPIKey(i), Desc: fmt.Sprintf("DPI stage %d: %d", i+1, v), Edit: mouse.SetDPI{Stage: i, DPI: v}})
	return nil
}

func (t *DPITab) setCurrent(c *Context, s dpiState, i int) tea.Cmd {
	if err := t.editable(c, s, mouse.FeatureCurrent); err != nil {
		return Problem(err)
	}
	if s.count >= 0 && i >= s.count {
		return Problem(fmt.Errorf("stage %d is inactive; raise the stage count with + first", i+1))
	}
	if i == s.devCur {
		c.Pending.Drop(CurrentKey)
		return nil
	}
	c.Pending.Stage(Staged{Key: CurrentKey, Desc: fmt.Sprintf("current stage: %d", i+1), Edit: mouse.SetCurrent{Stage: i}})
	return nil
}

func (t *DPITab) setCount(c *Context, s dpiState, delta int) tea.Cmd {
	if err := t.editable(c, s, mouse.FeatureStages); err != nil {
		return Problem(err)
	}
	from := s.count
	if from < 1 {
		from = s.m.Stages
	}
	n := from + delta
	if n < 1 || n > s.m.Stages {
		return Problem(fmt.Errorf("the %s has 1 to %d stages", modelName(s.m), s.m.Stages))
	}
	if n == s.devCount {
		c.Pending.Drop(StagesKey)
	} else {
		c.Pending.Stage(Staged{Key: StagesKey, Desc: fmt.Sprintf("stage count: %d", n), Edit: mouse.SetStages{Count: n}})
	}
	moved := t.moved(c)
	switch {
	case s.current >= n:
		if !moved {
			t.auto = &dpiAuto{}
			if prev, ok := c.Pending.Get(CurrentKey); ok {
				t.auto.prev = &prev
			}
		}
		t.auto.stage = n - 1
		s.count = n
		if cmd := t.setCurrent(c, s, n-1); cmd != nil {
			return cmd
		}
		return Notice(fmt.Sprintf("The current stage moves to %d, the last active one.", n))
	case moved:
		back := s.devCur
		if p := t.auto.prev; p != nil {
			if e, ok := p.Edit.(mouse.SetCurrent); ok {
				back = e.Stage
			}
		}
		if back < 0 || back >= n {
			return nil
		}
		if p := t.auto.prev; p != nil {
			c.Pending.Stage(*p)
		} else {
			c.Pending.Drop(CurrentKey)
		}
		t.auto = nil
		return Notice(fmt.Sprintf("The current stage is %d again.", back+1))
	}
	return nil
}

// moved reports whether the pending current stage is the one a lower
// stage count moved.
func (t *DPITab) moved(c *Context) bool {
	if t.auto == nil {
		return false
	}
	st, ok := c.Pending.Get(CurrentKey)
	return ok && st.Edit == mouse.SetCurrent{Stage: t.auto.stage}
}

func (t *DPITab) Hints(c *Context) []key.Binding {
	if t.typing {
		return []key.Binding{hint("0-9", "DPI"), hint("enter", "set"), hint("esc", "cancel")}
	}
	return []key.Binding{hint(dilArrows(c, "←/→", "left/right"), "step"), hint("enter", "type a value"), hint("space", "set current"),
		hint("+/-", "stage count"), hint(dilArrows(c, "↑/↓", "up/down"), "stage"), hint("del", "undo stage"), hint("x", "raw & colours")}
}

// dpiKeys are the pending keys the tab owns.
func dpiKeys() []string {
	out := []string{StagesKey, CurrentKey}
	for i := range mouse.MaxStages {
		out = append(out, DPIKey(i))
	}
	return out
}

// problem is the planner's objection to the staged DPI edits, if any.
func (t *DPITab) problem(c *Context) error {
	im := c.Image()
	if t.check.ok && t.check.rev == c.Pending.Rev() && t.check.im == im {
		return t.check.err
	}
	var edits []mouse.Edit
	for _, k := range dpiKeys() {
		if st, ok := c.Pending.Get(k); ok {
			edits = append(edits, st.Edit)
		}
	}
	var err error
	if len(edits) > 0 {
		_, err = mouse.PlanEdits(c.Model(), im, edits, c.MouseOptions())
	}
	t.check = dpiCheck{rev: c.Pending.Rev(), im: im, err: err, ok: true}
	return err
}

func (t *DPITab) View(c *Context, w, h int) string {
	s, ok := dpiRead(c)
	if !ok {
		return " " + errNothingLoaded.Error() + "."
	}
	t.sel = max(0, min(t.sel, s.m.Stages-1))
	g, st := c.Glyphs, c.Styles
	lines := []string{t.summary(c, s), " Legal DPI  " + dpiRanges(c, s.m) + "; typed values snap up", ""}
	lines = append(lines, t.rows(c, s, w)...)
	if s.m.Stages < mouse.MaxStages {
		lines = append(lines, st.Faint.Render(dpiUnused(s)))
	}
	lines = append(lines, "")
	lines = append(lines, t.detail(c, s, w)...)
	lines = append(lines, "")
	var pend []string
	for _, k := range dpiKeys() {
		if e, ok := c.Pending.Get(k); ok {
			pend = append(pend, e.Desc)
		}
	}
	if len(pend) == 0 {
		pend = []string{"none"}
	}
	lines = append(lines, wrap(strings.Join(pend, " "+g.Bullet+" "), w-1, " Pending    ", "            ")...)
	lines = append(lines, " Tiers      "+dpiTiers(c))
	if err := t.problem(c); err != nil {
		lines = append(lines, wrap("Cannot plan: "+plain(err), w-1, " "+st.Bad.Render("!")+" ", "   ")...)
	}
	return strings.Join(lines, "\n")
}

func (t *DPITab) summary(c *Context, s dpiState) string {
	g := c.Glyphs
	var count string
	switch {
	case s.count < 0:
		count = "stage count unreadable (" + spacedHex(s.cfg.Stages.Raw) + ")"
	case s.count != s.devCount && s.devCount >= 0:
		count = fmt.Sprintf("%d of %d stages active (was %d, pending)", s.count, s.m.Stages, s.devCount)
	default:
		count = fmt.Sprintf("%d of %d stages active", s.count, s.m.Stages)
	}
	var cur string
	switch {
	case s.current < 0:
		cur = "current stage unreadable (" + spacedHex(s.cfg.Current.Raw) + ")"
	case s.current != s.devCur && s.devCur >= 0:
		cur = fmt.Sprintf("current %s %d (was %d, pending)", g.Current, s.current+1, s.devCur+1)
	default:
		cur = fmt.Sprintf("current %s %d", g.Current, s.current+1)
	}
	return " " + c.Styles.Bold.Render(count) + " " + g.Bullet + " " + cur
}

// dpiRow is one stage line before it is laid out.
type dpiRow struct {
	head, value, colour string
	dpi                 int
	tags                []string
	faint               bool
}

func (t *DPITab) rows(c *Context, s dpiState, w int) []string {
	g, st := c.Glyphs, c.Styles
	rows := make([]dpiRow, s.m.Stages)
	valW, colW, tagW := 0, 0, 0
	for i := range rows {
		r := &rows[i]
		dev := s.cfg.DPI[i]
		cursor, mark := " ", " "
		if i == t.sel {
			cursor = g.Cursor
		}
		if i == s.current {
			mark = g.Current
		}
		r.head = fmt.Sprintf(" %s %s %d  ", cursor, mark, i+1)
		v, staged := s.staged[i]
		was := "?"
		switch dev.DPIField.State {
		case flash.OK:
			was = strconv.Itoa(dev.DPI.X)
			r.dpi = dev.DPI.X
			if dev.DPI.X != dev.DPI.Y && !staged {
				r.tags = append(r.tags, "X/Y differ")
			}
		case flash.Unknown:
			r.tags = append(r.tags, "not read")
		default:
			was = "bad"
			r.tags = append(r.tags, dev.DPIField.State.String())
		}
		r.value = was
		if staged {
			r.value, r.dpi = was+" "+g.Arrow+" "+strconv.Itoa(v), v
			r.tags = append(r.tags, "pending")
		}
		if t.typing && i == t.sel {
			r.value = "[" + t.typed + "_]"
			r.tags = []string{"enter sets it"}
			if v, err := strconv.Atoi(t.typed); err == nil {
				r.tags = []string{fmt.Sprintf("sets %d", dpiSnap(s.legal, v))}
			}
		}
		if s.count >= 0 && i >= s.count {
			r.tags, r.faint = append(r.tags, "inactive"), true
		}
		if t.details || !s.m.UI.Office {
			r.colour = "--"
			if dev.ColorField.State == flash.OK {
				r.colour = spacedHex(dev.Color[:])
			}
			colW = max(colW, ansi.StringWidth(r.colour))
		}
		valW = max(valW, ansi.StringWidth(r.value))
		tagW = max(tagW, ansi.StringWidth(strings.Join(r.tags, ", ")))
	}
	fixed := ansi.StringWidth(rows[0].head) + valW + 2 + tagW + 1
	if colW > 0 {
		fixed += colW + 2
	}
	barW := min(dpiBarMax, w-fixed-2)
	if barW < 4 {
		barW = 0
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		line := r.head + strings.Repeat(" ", valW-ansi.StringWidth(r.value)) + r.value + "  "
		if colW > 0 {
			line += fmt.Sprintf("%-*s  ", colW, r.colour)
		}
		if barW > 0 {
			n := 0
			if s.limit > 0 {
				n = min(barW, (r.dpi*barW+s.limit/2)/s.limit)
			}
			bar := strings.Repeat(g.BarFull, n) + strings.Repeat(" ", barW-n)
			if r.faint {
				bar = st.Faint.Render(bar)
			}
			line += bar + "  "
		}
		tags := strings.Join(r.tags, ", ")
		switch {
		case slices.Contains(r.tags, "pending"):
			tags = st.Warn.Render(tags)
		case r.faint:
			tags = st.Faint.Render(tags)
		}
		out[i] = line + tags
	}
	return out
}

// dpiUnused lists the DPI records past the model's stages, which it never
// uses.
func dpiUnused(s dpiState) string {
	var vals []string
	for i := s.m.Stages; i < mouse.MaxStages; i++ {
		v := "?"
		if f := s.cfg.DPI[i].DPIField; f.State == flash.OK {
			v = strconv.Itoa(s.cfg.DPI[i].DPI.X)
		} else if f.State != flash.Unknown {
			v = f.State.String()
		}
		vals = append(vals, fmt.Sprintf("%d  %s", i+1, v))
	}
	return fmt.Sprintf("     %s    unused by the %s, read-only", strings.Join(vals, "    "), modelName(s.m))
}

// detail describes the selected stage: its axes and the raw records.
func (t *DPITab) detail(c *Context, s dpiState, w int) []string {
	i := t.sel
	dev := s.cfg.DPI[i]
	head := fmt.Sprintf(" Stage %d  ", i+1)
	pad := strings.Repeat(" ", len(head))
	f := dev.DPIField
	var text string
	switch f.State {
	case flash.Unknown:
		text = fmt.Sprintf("Record %s was not read. Reload with r.", f.Extent)
	case flash.OK:
		text = fmt.Sprintf("X %d, Y %d; record %s holds %s.", dev.DPI.X, dev.DPI.Y, f.Extent, spacedHex(f.Raw))
		if dev.DPI.X != dev.DPI.Y {
			text += " The axes differ; they stay as they are unless you change this stage, and a change writes the same value to both."
		}
	default:
		text = fmt.Sprintf("Record %s holds %s, which is %s", f.Extent, spacedHex(f.Raw), f.State)
		if f.Err != nil {
			text += ": " + plain(f.Err)
		}
		text += ". A new value replaces it."
	}
	if v, ok := s.staged[i]; ok {
		text += fmt.Sprintf(" Pending: %d on both axes.", v)
	}
	lines := wrap(text, w-1, head, pad)
	if cf := dev.ColorField; t.details && cf.State != flash.Unknown {
		lines = append(lines, wrap(fmt.Sprintf("Colour record %s holds %s, a hidden setting shown read-only.", cf.Extent, spacedHex(cf.Raw)), w-1, pad, pad)...)
	}
	return lines
}

func dpiTiers(c *Context) string {
	var parts []string
	for _, f := range []struct {
		name string
		f    mouse.Feature
	}{{"values", mouse.FeatureDPI}, {"count", mouse.FeatureStages}, {"current", mouse.FeatureCurrent}} {
		tier, _ := c.Tier(f.f)
		parts = append(parts, f.name+" "+tier.String())
	}
	return strings.Join(parts, " "+c.Glyphs.Bullet+" ")
}

// spacedHex is b as spaced hex, "--" when it was never read.
func spacedHex(b []byte) string {
	if b == nil {
		return "--"
	}
	return fmt.Sprintf("% x", b)
}

// dilArrows names arrow keys in the glyph set the screen uses, so that the
// footer measures what --ascii shows.
func dilArrows(c *Context, unicode, ascii string) string {
	if c.Glyphs == asciiGlyphs {
		return ascii
	}
	return unicode
}
