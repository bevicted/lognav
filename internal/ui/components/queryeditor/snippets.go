package queryeditor

import (
	"fmt"
	"strings"

	"github.com/bevicted/lognav/internal/ui/components/list"
)

type snippet struct {
	Snippet     string
	Description string
}

// formatSnippetList formats snippets as "snippet    description" rows
// for display in a list, matching the help view style.
func formatSnippetList(snippets []snippet) [][]list.Segment {
	items := make([][]list.Segment, len(snippets))
	var longestSnippet int

	for _, s := range snippets {
		if l := len(s.Snippet); l > longestSnippet {
			longestSnippet = l
		}
	}
	for i, s := range snippets {
		items[i] = []list.Segment{
			{Text: fmt.Sprintf("%-*s", longestSnippet, s.Snippet)},
			{Text: " " + s.Description},
		}
	}
	return items
}

// snippetsForEditor builds the text appended after the separator
// when opening an external editor, using the same column-aligned style.
func snippetsForEditor(snippets []snippet) string {
	display := make([]string, len(snippets))
	var longest int
	for i, s := range snippets {
		display[i] = s.Snippet
		if l := len(display[i]); l > longest {
			longest = l
		}
	}
	var sb strings.Builder
	for i, s := range snippets {
		fmt.Fprintf(&sb, "%-*s %s\n", longest, display[i], s.Description)
	}
	return sb.String()
}
