package logviewer

import (
	"bytes"
	"strings"

	"github.com/rivo/uniseg"
)

// wrapLinesAt wraps the input bytes at width n graphemes per line, handling
// JSON-indent continuation. Moved from ui/utils/utils.go as part of A.1;
// the function had a single caller in this package and was unexported on move.
func wrapLinesAt(in []byte, n int) []byte {
	var (
		buf    = &bytes.Buffer{}
		isJSON = rune(in[0]) == '{'

		state   = -1
		cluster []byte
		rest    = in
		width   int

		currentWidth     = 0
		indent           = 0
		reachedFirstChar = false
		reachedEndOfKey  = !isJSON
	)

	buf.Grow(len(in))

	for len(rest) > 0 {
		cluster, rest, width, state = uniseg.FirstGraphemeCluster(rest, state)

		if bytes.Equal(cluster, []byte{'\n'}) {
			currentWidth = 0
			indent = 0
			reachedFirstChar = false
			reachedEndOfKey = !isJSON
			buf.WriteRune('\n')
			continue
		}

		// Wrap if the cluster would exceed the line width, or if writing
		// a backslash would land exactly at the limit. A trailing backslash
		// must stay with the character it escapes so the JSON tokenizer
		// sees the full escape sequence on one line.
		wrapNeeded := currentWidth+width >= n ||
			(currentWidth+width == n-1 && bytes.Equal(cluster, []byte{'\\'}))
		if wrapNeeded {
			buf.WriteRune('\n')
			buf.WriteString(strings.Repeat(" ", indent))
			currentWidth = indent
		}

		if !reachedFirstChar {
			switch {
			case !reachedEndOfKey && bytes.Equal(cluster, []byte{':'}):
				reachedEndOfKey = true
				indent++
			case !reachedEndOfKey || bytes.Equal(cluster, []byte{' '}) || (isJSON && bytes.Equal(cluster, []byte{'"'})):
				indent++
			default:
				reachedFirstChar = true
			}
		}

		buf.Write(cluster)

		currentWidth += width
	}

	return buf.Bytes()
}
