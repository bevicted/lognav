package cmd

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// sanitizeStderrText removes terminal escape sequences and replaces control
// characters with spaces. It preserves printable Unicode, including wide and
// joined graphemes, while ensuring a dynamic value cannot add a terminal
// command or a physical output row.
func sanitizeStderrText(text string) string {
	return sanitizeStderr(text, false)
}

// sanitizeStderrError sanitizes an error while preserving line feeds used by
// structured parser diagnostics.
func sanitizeStderrError(text string) string {
	return sanitizeStderr(text, true)
}

func sanitizeStderr(text string, preserveLineFeeds bool) string {
	text = ansi.Strip(text)
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		if r == '\n' && preserveLineFeeds {
			out.WriteByte('\n')
			continue
		}
		if unicode.IsControl(r) {
			out.WriteByte(' ')
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
