package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/positronico/arcctl/internal/flash"
)

// hexdump prints the lines of im that hold bytes of extents, 16 bytes a line.
// Bytes outside the extents or never read show as "--".
func hexdump(w io.Writer, im *flash.Image, extents []flash.Extent) {
	in := func(a int) bool {
		for _, e := range extents {
			if a >= e.Addr && a < e.End() {
				return true
			}
		}
		return false
	}
	last := -1
	for line := 0; line < flash.Size; line += 16 {
		used := false
		for a := line; a < line+16; a++ {
			if in(a) {
				used = true
				break
			}
		}
		if !used {
			continue
		}
		if last >= 0 && line != last+16 {
			fmt.Fprintln(w, "...")
		}
		last = line
		var hex, text strings.Builder
		for a := line; a < line+16; a++ {
			if a == line+8 {
				hex.WriteByte(' ')
			}
			b, known := im.Byte(a)
			switch {
			case !in(a) || !known:
				hex.WriteString(" --")
				text.WriteByte(' ')
			default:
				fmt.Fprintf(&hex, " %02x", b)
				if b >= 0x20 && b < 0x7F {
					text.WriteByte(b)
				} else {
					text.WriteByte('.')
				}
			}
		}
		fmt.Fprintf(w, "%04x %s  |%s|\n", line, hex.String(), text.String())
	}
}

// asciiWriter replaces what the CLI prints outside ASCII: the arrow keys by
// their names, anything else by '?'.
type asciiWriter struct{ w io.Writer }

var asciiNames = strings.NewReplacer("↑", "Up", "↓", "Down", "←", "Left", "→", "Right", "×", "x", "…", "...")

func (a asciiWriter) Write(p []byte) (int, error) {
	s := asciiNames.Replace(string(p))
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	if _, err := io.WriteString(a.w, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// table prints rows in columns separated by two spaces; the last column is
// not padded.
func table(w io.Writer, prefix string, rows [][]string) {
	widths := map[int]int{}
	for _, row := range rows {
		for i, c := range row[:max(len(row)-1, 0)] {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	for _, row := range rows {
		var b strings.Builder
		b.WriteString(prefix)
		for i, c := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(c)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// fill wraps each line of text at width. A wrapped line goes on with indent
// plus the leading spaces of the line it continues.
func fill(text, indent string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		lead := line[:len(line)-len(trimmed)]
		words := strings.Fields(trimmed)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		out = append(out, wrap(lead, indent+lead, words, " ", width))
	}
	return strings.Join(out, "\n")
}

// wrap joins words with sep, breaking lines at width; continuation lines start
// with indent.
func wrap(first, indent string, words []string, sep string, width int) string {
	var b strings.Builder
	b.WriteString(first)
	col := utf8.RuneCountInString(first)
	for i, w := range words {
		piece := w
		if i < len(words)-1 {
			piece += strings.TrimRight(sep, " ")
		}
		n := utf8.RuneCountInString(piece)
		if i > 0 {
			if col+1+n > width {
				b.WriteString("\n" + indent)
				col = utf8.RuneCountInString(indent)
			} else {
				b.WriteByte(' ')
				col++
			}
		}
		b.WriteString(piece)
		col += n
	}
	return b.String()
}
