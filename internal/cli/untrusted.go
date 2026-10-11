package cli

import (
	"strings"
	"unicode"
)

// Text that someone else chose reaches the terminal only through the
// functions here: a secret's text and its files' names, which the sender
// chose, and an error message, which may come from the server. Left alone,
// control characters in it could steer the terminal: move the cursor over
// what was printed, retitle the window, show a link that leads elsewhere,
// or set the clipboard. Format characters such as U+202E reverse text on
// display: invoice, U+202E, txt.exe reads as invoiceexe.txt. Pipes, files,
// --copy and --json get the text as it is.

// printable is a file's name as the terminal may show it: control and
// format characters, and bytes that aren't UTF-8, become '?'.
func printable(name string) string {
	return strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) || hidden(r) {
			return '?'
		}
		return r
	}, name)
}

// printableMessage is an error message as the terminal may show it: like
// printable, but line breaks and tabs stay.
func printableMessage(message string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == unicode.ReplacementChar || unicode.IsControl(r) || hidden(r) {
			return '?'
		}
		return r
	}, message)
}

// printableText is a text secret as the terminal may show it: line breaks
// (CR LF too) and tabs stay, other control characters and bytes that
// aren't UTF-8 become U+FFFD, and changed tells whether any did. Format
// characters stay: they belong to the text, in Arabic or Hebrew say, and
// without control characters they can't reach beyond it.
func printableText(text []byte) (shown string, changed bool) {
	s := string(text)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r == '\r' && strings.HasPrefix(s[i:], "\r\n"):
		case r == unicode.ReplacementChar && !strings.HasPrefix(s[i:], "\uFFFD"):
			// A byte that isn't UTF-8, rather than U+FFFD itself.
			changed = true
		case unicode.IsControl(r):
			r, changed = unicode.ReplacementChar, true
		}
		b.WriteRune(r)
	}
	return b.String(), changed
}

// hidden reports whether r is a format character that changes how the
// text around it shows, or doesn't show at all: direction marks and
// overrides, zero-width spaces, invisible tags. Joiners stay: U+200C and
// U+200D hold Persian and Indic words, and emoji, together.
func hidden(r rune) bool {
	return r != '\u200C' && r != '\u200D' && unicode.Is(unicode.Cf, r)
}
