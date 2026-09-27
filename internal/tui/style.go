package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Glyphs are the non-letter characters the screens draw; --ascii swaps them.
type Glyphs struct {
	Rule     string
	Bullet   string
	Cursor   string
	Current  string
	BarFull  string
	BarEmpty string
	Arrow    string
	Check    string
	Cross    string
	Ellipsis string
}

var (
	unicodeGlyphs = Glyphs{Rule: "─", Bullet: "·", Cursor: "›", Current: "●", BarFull: "█", BarEmpty: "░",
		Arrow: "→", Check: "✓", Cross: "✗", Ellipsis: "…"}
	asciiGlyphs = Glyphs{Rule: "-", Bullet: "-", Cursor: ">", Current: "*", BarFull: "#", BarEmpty: ".",
		Arrow: "->", Check: "+", Cross: "x", Ellipsis: "..."}
)

// Styles colour the screens. State is always spelled out as text as well;
// colour only repeats it (§7.1), and --no-color or NO_COLOR drop it.
type Styles struct {
	App      lipgloss.Style
	Faint    lipgloss.Style
	Bold     lipgloss.Style
	Key      lipgloss.Style
	Selected lipgloss.Style
	TabOn    lipgloss.Style
	TabOff   lipgloss.Style
	Good     lipgloss.Style
	Warn     lipgloss.Style
	Bad      lipgloss.Style
	Info     lipgloss.Style
}

func defaultStyles() Styles {
	s := lipgloss.NewStyle()
	return Styles{
		App:      s.Bold(true),
		Faint:    s.Faint(true),
		Bold:     s.Bold(true),
		Key:      s.Bold(true),
		Selected: s.Reverse(true),
		TabOn:    s.Bold(true).Reverse(true),
		TabOff:   s,
		Good:     s.Foreground(lipgloss.Green),
		Warn:     s.Foreground(lipgloss.Yellow).Bold(true),
		Bad:      s.Foreground(lipgloss.Red).Bold(true),
		Info:     s.Foreground(lipgloss.Cyan),
	}
}

// tone picks the style of a badge or banner.
type tone uint8

const (
	toneInfo tone = iota
	toneGood
	toneWarn
	toneBad
)

func (s Styles) tone(t tone) lipgloss.Style {
	switch t {
	case toneGood:
		return s.Good
	case toneWarn:
		return s.Warn
	case toneBad:
		return s.Bad
	}
	return s.Info
}

// fit cuts s to w cells and pads it to exactly w.
func fit(s string, w int, tail string) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, tail)
	}
	if n := w - ansi.StringWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// wrap breaks plain text into lines of at most w cells, each starting with
// indent after the first, which starts with first.
func wrap(text string, w int, first, indent string) []string {
	var out []string
	line := first
	empty := true
	for _, word := range strings.Fields(text) {
		switch {
		case empty:
			line += word
			empty = false
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = indent + word
		}
	}
	if !empty {
		out = append(out, line)
	}
	return out
}

var asciiNames = strings.NewReplacer("↑", "up", "↓", "down", "←", "left", "→", "->", "…", "...", "─", "-", "·", "-",
	"›", ">", "●", "*", "█", "#", "░", ".", "✓", "+", "✗", "x", "×", "x")

// asciiLine is l with --ascii applied: known glyphs by their ASCII stand-ins,
// anything else outside ASCII by '?'. Escape sequences are ASCII already.
func asciiLine(l string) string {
	l = asciiNames.Replace(l)
	var b strings.Builder
	for _, r := range l {
		if r < utf8.RuneSelf {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// midCut shortens s to w cells by cutting out its middle, so that its start
// and its end show, such as a path's folder and its file name.
func midCut(c *Context, s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	keep := w - ansi.StringWidth(c.Glyphs.Ellipsis)
	r := []rune(s)
	if keep < 2 {
		return ansi.Truncate(s, max(0, w), "")
	}
	tail := keep * 2 / 3
	return string(r[:keep-tail]) + c.Glyphs.Ellipsis + string(r[len(r)-tail:])
}
