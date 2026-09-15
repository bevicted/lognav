// Package externaleditor provides the shared external-query-editor workflow.
package externaleditor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bevicted/lognav/internal/config"
)

const (
	separator   = "========== dataprime snippets =========="
	instruction = `
Do not modify or remove the line above.
Everything below it will be ignored.

`
)

// Prepare writes query content for an external editor and returns its command
// plus a reader cleanup that removes the temporary file. Call cleanup after the
// command has exited; it returns the edited query with an optional reference
// section removed.
func Prepare(ctx context.Context, query string, includeSnippets bool, snippets []config.Snippet) (*exec.Cmd, func() (string, error), error) {
	content := query
	if includeSnippets {
		content = strings.TrimSpace(query) + "\n\n" + separator + instruction + formatSnippets(snippets)
	}
	file, err := os.CreateTemp("", "query*.dataprime")
	if err != nil {
		return nil, nil, err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return nil, nil, err
	}

	cmd := NewCommand(ctx, file.Name())
	cleanup := func() (string, error) {
		defer os.Remove(file.Name())
		data, err := os.ReadFile(file.Name())
		if err != nil {
			return "", err
		}
		out := string(data)
		if includeSnippets {
			out, _, _ = strings.Cut(out, separator)
		}
		return strings.TrimSpace(out), nil
	}
	return cmd, cleanup, nil
}

// NewCommand constructs an external editor command for path. EDITOR is split
// on whitespace only; quoted arguments and shell expansion are not supported.
func NewCommand(ctx context.Context, path string) *exec.Cmd {
	args := strings.Fields(os.Getenv("EDITOR"))
	if len(args) == 0 {
		args = []string{"vim"}
	}
	return exec.CommandContext(ctx, args[0], append(args[1:], path)...) //nolint:gosec // $EDITOR is intentional user configuration
}

func formatSnippets(snippets []config.Snippet) string {
	longest := 0
	for _, snippet := range snippets {
		longest = max(longest, len(snippet.Snippet))
	}
	var b strings.Builder
	for _, snippet := range snippets {
		fmt.Fprintf(&b, "%-*s %s\n", longest, snippet.Snippet, snippet.Desc)
	}
	return b.String()
}
