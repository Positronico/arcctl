package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/positronico/arcctl/internal/mouse"
)

// cjkPunct are the full-width and CJK marks the web app strips from macro
// names besides ASCII punctuation.
const cjkPunct = "·！￥…—（）《》？：“”【】、；‘’，。"

// nameDrops reports whether the name sanitiser removes r: ASCII punctuation
// and symbols, the CJK marks, white space (with U+FEFF), and, arcctl's
// addition, control and format characters and bytes that are not UTF-8.
func nameDrops(r rune) bool {
	switch {
	case r == utf8.RuneError || r == '\ufeff':
		return true
	case unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		return true
	case r < utf8.RuneSelf:
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	}
	return strings.ContainsRune(cjkPunct, r)
}

// sanitizeName applies the web app's macro name rules before a name reaches
// the planner (§7.7): the characters nameDrops names go, then whole
// characters come off the end until the name fits in 30 UTF-8 bytes.
func sanitizeName(s string) string { return cutName(stripName(s)) }

// stripName drops the characters nameDrops names.
func stripName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !nameDrops(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cutName takes whole characters off the end of s until it fits in a name.
func cutName(s string) string {
	for len(s) > mouse.MaxNameLen {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// shownName is a macro name for the screen: control and format characters,
// backslashes and bytes that are not UTF-8 are escaped, so a name read from
// the mouse cannot move the cursor or hide its text.
func shownName(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[0])
		case r == '\\':
			b.WriteString(`\\`)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ':
			if r < 0x100 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
		s = s[n:]
	}
	return b.String()
}
