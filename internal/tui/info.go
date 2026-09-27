package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/positronico/arcctl/internal/catalog"
	"github.com/positronico/arcctl/internal/flash"
	"github.com/positronico/arcctl/internal/mouse"
	"github.com/positronico/arcctl/internal/plan"
	"github.com/positronico/arcctl/internal/session"
)

// InfoTab shows what arcctl knows about the device (§7.8): model, identity,
// versions, battery, connection, the cmd-14 and cmd-23 probes, every
// feature's tier, and the hidden fields read-only with their raw bytes (D5).
type InfoTab struct {
	scroll lineScroll
}

func NewInfoTab() *InfoTab { return &InfoTab{} }

func (t *InfoTab) Title() string          { return "Info" }
func (t *InfoTab) Needs() []mouse.Feature { return nil }
func (t *InfoTab) Capturing() bool        { return false }

func (t *InfoTab) Update(c *Context, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		t.scroll.key(k.String())
	}
	return nil
}

func (t *InfoTab) Hints(c *Context) []key.Binding {
	return []key.Binding{hint(dilArrows(c, "↑/↓", "up/down"), "scroll"), hint("pgup/pgdn", "page"), hint("g/G", "top/end")}
}

// infoSplit is the width from which the device facts and the hidden fields
// sit side by side (§7.1); infoHiddenW is the width of the hidden fields.
const (
	infoSplit   = 100
	infoHiddenW = 66
)

func (t *InfoTab) View(c *Context, w, h int) string {
	var lines []string
	if w >= infoSplit {
		lw := max(44, min(56, w-2-infoHiddenW))
		left := append(infoFacts(c, lw), "")
		left = append(left, infoTiers(c, lw)...)
		lines = besideCols(left, infoHidden(c, w-lw-2), lw, 2)
	} else {
		lines = infoFacts(c, w)
		lines = append(lines, "")
		lines = append(lines, infoTiers(c, w)...)
		lines = append(lines, "")
		lines = append(lines, infoHidden(c, w)...)
	}
	return strings.Join(t.scroll.window(c, lines, h), "\n")
}

const infoLabel = 13

// infoLine is one "Label  text" fact, wrapped under its text.
func infoLine(label, text string, w int) []string {
	first := fmt.Sprintf(" %-*s", infoLabel, label)
	return wrap(text, w-1, first, strings.Repeat(" ", infoLabel+1))
}

func infoFacts(c *Context, w int) []string {
	sn := c.Snapshot
	st := c.Styles
	out := []string{st.Bold.Render(" Device")}
	add := func(label, text string) { out = append(out, infoLine(label, text, w)...) }
	m := sn.Model
	switch {
	case m != nil:
		text := fmt.Sprintf("%s (%s), %s", modelName(m), modelKey(m), m.Family)
		if m.Alias != "" && m.Alias != m.Name {
			text += ", listed as " + m.Name
		}
		add("Model", text)
		if m.Sensor != nil {
			add("Sensor", fmt.Sprintf("%s, 1-%d DPI stages, up to %d DPI", m.Sensor.ID, m.Stages, dpiLimit(m)))
		}
	case sn.Handshake != nil:
		add("Model", fmt.Sprintf("unknown device %02X/%02X: read-only", sn.Handshake.CID, sn.Handshake.MID))
	default:
		add("Model", "not known yet")
	}
	if sn.Handshake != nil {
		add("Identity", infoIdentity(sn.Identity))
		add("Address", infoAddress(sn.Identity))
	}
	if d := sn.Device; d != nil {
		add("Receiver", fmt.Sprintf("%04x:%04x, interface %d, %s", d.VID, d.PID, d.Interface, d.Product))
		add("Path", d.Path)
	}
	if hs := sn.Handshake; hs != nil {
		text := hs.ConnString()
		if hz, ok := mouse.MaxRate(hs.Conn); ok {
			text += fmt.Sprintf(", reports up to %d Hz", hz)
		}
		add("Connection", text)
	}
	online := "asleep or out of range"
	if sn.Online {
		online = "online"
	}
	if sn.Device != nil {
		add("Mouse", online)
	}
	add("Firmware", fmt.Sprintf("mouse %s, receiver %s", infoOr(sn.Versions.Mouse), infoOr(sn.Versions.Receiver)))
	add("Battery", infoBattery(sn.Battery))
	add("Profiles", infoProbe(sn.Profile, "cmd 14", func(v byte) string { return fmt.Sprintf("onboard profile %d is active", v) },
		"refused: no onboard profiles"))
	add("Long range", infoProbe(sn.LongRange, "cmd 23", func(v byte) string {
		if v == 1 {
			return "on"
		}
		return "off"
	}, "refused: not supported"))
	add("Loaded", infoLoaded(sn))
	others := "no other program seen on the receiver"
	if s := clientList(sn.Clients); s != "" {
		others = s
	}
	add("Clients", others)
	s := sn.Stats
	add("Traffic", fmt.Sprintf("%d transactions, %d unanswered tries, %d NAKs, %d foreign, %d duplicate and %d late replies",
		s.Transactions, s.FailedTries, s.NAKs, s.Foreign, s.Duplicates, s.Late))
	add("Writes", infoWrites(c))
	if c.dataDir != "" {
		add("Data", c.dataDir)
	}
	for _, n := range c.notes {
		add("Note", n)
	}
	return out
}

func infoOr(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// infoIdentity names the device the journal and backups key on, without
// ever printing its address.
func infoIdentity(id plan.Identity) string {
	if id.AddrTrusted {
		return fmt.Sprintf("model %02X/%02X and the mouse address", id.CID, id.MID)
	}
	return id.Key() + " (the receiver's USB IDs and the model)"
}

func infoAddress(id plan.Identity) string {
	switch {
	case id.Addr == [3]byte{}:
		return "not read"
	case id.AddrTrusted:
		return "trusted: backups and the journal key on it"
	}
	return "read, not trusted yet: backups and the journal key on the receiver's USB IDs and the model"
}

func infoBattery(b *session.Battery) string {
	if b == nil {
		return "not read yet"
	}
	src := 5
	if b.Direct {
		src = 10
	}
	text := fmt.Sprintf("%d%% raw (reply byte %d), %d mV", b.Level, src, b.MilliVolts)
	if b.Charging {
		return text + ", charging"
	}
	return text + ", not charging"
}

func infoProbe(p session.Probe, cmd string, value func(byte) string, refused string) string {
	switch {
	case !p.Asked:
		return cmd + " not asked yet"
	case !p.Supported:
		return cmd + " " + refused
	}
	return cmd + ": " + value(p.Value)
}

func infoLoaded(sn *session.Snapshot) string {
	if sn.Image == nil {
		return "nothing yet"
	}
	n := 0
	for _, e := range sn.Image.KnownExtents() {
		n += e.Len
	}
	text := fmt.Sprintf("%d bytes", n)
	if k := len(sn.Unread); k > 0 {
		text += fmt.Sprintf("; %s could not be read", plural(k, "chunk", "chunks"))
	}
	return text
}

func infoWrites(c *Context) string {
	var parts []string
	m := c.Model()
	switch {
	case c.Mode == ModeReadOnly:
		return "none: this session is read-only"
	case m == nil || m.Family != catalog.FamilyMouse:
		return "none: arcctl only reads this device"
	case c.Mode == ModeDryRun:
		parts = append(parts, "dry run: packets go to an overlay")
	default:
		parts = append(parts, "staged, then Review & Apply")
	}
	g := c.Gates
	for _, f := range []struct {
		on   bool
		flag string
	}{{g.AllowUntested, "--allow-untested"}, {g.Experimental, "--experimental"}, {g.AllowForeignClient, "--allow-foreign-client"}} {
		if f.on {
			parts = append(parts, f.flag)
		}
	}
	parts = append(parts, "guard "+c.Snapshot.Policy.String())
	return strings.Join(parts, "; ")
}

var infoTierOrder = []catalog.Tier{catalog.Verified, catalog.Untested, catalog.Experimental, catalog.ReadOnly, catalog.Off}

// infoTiers lists every feature under its tier on this device; a feature
// verified only on other firmware is marked.
func infoTiers(c *Context, w int) []string {
	m := c.Model()
	fw := infoOr(c.Snapshot.Versions.Mouse)
	out := []string{c.Styles.Bold.Render(" Feature tiers") + " on firmware " + fw}
	if m == nil {
		return append(out, "   none until the model is known")
	}
	groups := map[catalog.Tier][]string{}
	elsewhere := false
	for _, f := range mouse.Features() {
		t, _ := c.Tier(f)
		name := string(f)
		if t < catalog.Verified && len(c.verified.Find(m.Key, name)) > 0 {
			name += "*"
			elsewhere = true
		}
		groups[t] = append(groups[t], name)
	}
	for _, t := range infoTierOrder {
		names := groups[t]
		if len(names) == 0 {
			names = []string{"none"}
		}
		first := fmt.Sprintf("   %-13s ", t)
		out = append(out, wrap(strings.Join(names, " "), w-1, first, strings.Repeat(" ", len(first)))...)
	}
	if elsewhere {
		out = append(out, "   * verified on other firmware")
	}
	return out
}

// infoHidden is the table of hidden fields, read-only (D5).
func infoHidden(c *Context, w int) []string {
	st := c.Styles
	out := []string{st.Bold.Render(" Hidden fields") + " (read-only)"}
	cfg, m := c.Config(), c.Model()
	if cfg == nil || m == nil {
		return append(out, "  decoded once a mouse configuration is loaded")
	}
	l := mouse.Layout(m)
	fields := append([]mouse.Field{cfg.Rate}, cfg.Hidden...)
	out = append(out, st.Faint.Render(fmt.Sprintf("  %-20s %-7s %-14s %-8s %s", "Field", "Record", "Raw", "State", "Writable")))
	for _, f := range fields {
		state := f.State.String()
		if f.Raw == nil {
			state = "not read"
		}
		switch f.State {
		case flash.Invalid:
			state = st.Bad.Render(fmt.Sprintf("%-8s", state))
		case flash.Erased, flash.Unknown:
			state = st.Faint.Render(fmt.Sprintf("%-8s", state))
		default:
			state = fmt.Sprintf("%-8s", state)
		}
		name := f.Name
		if ansi.StringWidth(name) > 20 {
			name = ansi.Truncate(name, 20, c.Glyphs.Ellipsis)
		}
		out = append(out, fmt.Sprintf("  %-20s %-7s %-14s %s %s", name, f.Extent, shortHex(c, f.Raw, 5), state, infoWritable(l, f)))
	}
	note := "once tested: writable only after its own hardware test passed, with --experimental. " +
		"never: KeyOperation stays read-only, so a button can always click. optional: a feature this model lacks. unmapped: no known field."
	return append(out, wrap(note, w-1, "  ", "  ")...)
}

func infoWritable(l plan.Layout, f mouse.Field) string {
	switch {
	case slices.Contains(l.Frozen, f.Extent):
		return "never"
	case f.Name == "unmapped":
		return "unmapped"
	case slices.Contains(l.Records, f.Extent):
		return "once tested"
	}
	return "optional"
}

// shortHex is b as spaced hex, cut after n bytes.
func shortHex(c *Context, b []byte, n int) string {
	if len(b) <= n {
		return spacedHex(b)
	}
	return spacedHex(b[:n-1]) + " " + c.Glyphs.Ellipsis
}

// besideCols sets right beside left, left padded to lw columns and gap
// spaces apart.
func besideCols(left, right []string, lw, gap int) []string {
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := range n {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = fit(l, lw, "") + strings.Repeat(" ", gap) + r
	}
	return out
}

// lineScroll keeps the first shown line of a scrolled text.
type lineScroll struct {
	top  int
	page int
}

func (s *lineScroll) key(k string) {
	page := max(1, s.page-2)
	switch k {
	case "up", "k":
		s.top--
	case "down", "j":
		s.top++
	case "pgup":
		s.top -= page
	case "pgdown", "space":
		s.top += page
	case "home", "g":
		s.top = 0
	case "end", "G":
		s.top = 1 << 30
	}
	s.top = max(0, s.top)
}

// window cuts lines to h rows from the scroll position; when they overflow,
// the last row says which lines show.
func (s *lineScroll) window(c *Context, lines []string, h int) []string {
	s.page = h
	if len(lines) <= h || h < 2 {
		s.top = 0
		return lines
	}
	room := h - 1
	s.top = max(0, min(s.top, len(lines)-room))
	out := slices.Clone(lines[s.top : s.top+room])
	return append(out, c.Styles.Faint.Render(fmt.Sprintf(" %s lines %d-%d of %d", c.Glyphs.Ellipsis, s.top+1, s.top+room, len(lines))))
}
