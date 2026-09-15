package externaleditor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
)

func TestPrepare_CleanupPreservesLiteralSeparatorWithoutSnippets(t *testing.T) {
	t.Parallel()
	query := "source logs\n" + separator + "\nwhere service = 'api'"
	_, finish, err := Prepare(t.Context(), query, false, nil)
	require.NoError(t, err)
	got, err := finish()
	require.NoError(t, err)
	assert.Equal(t, query, got)
}

func TestPrepare_CleanupStripsSnippetReference(t *testing.T) {
	t.Parallel()
	cmd, finish, err := Prepare(t.Context(), "source logs", true, []config.Snippet{{Snippet: "snippet", Desc: "description"}})
	require.NoError(t, err)

	assert.Equal(t, ".dataprime", filepath.Ext(cmd.Args[1]))
	data, err := os.ReadFile(cmd.Args[1])
	require.NoError(t, err)
	require.Contains(t, string(data), "========== dataprime snippets ==========")
	assert.NotContains(t, string(data), "cheat sheet")
	got, err := finish()
	require.NoError(t, err)
	assert.Equal(t, "source logs", got)
}

//nolint:paralleltest // temporarily modifies process-global env
func TestNewCommand_UnsetEditorUsesVim(t *testing.T) {
	t.Setenv("EDITOR", "")
	require.NoError(t, os.Unsetenv("EDITOR"))

	assert.Equal(t, []string{"vim", "/tmp/lognav-editor-target"}, NewCommand(t.Context(), "/tmp/lognav-editor-target").Args)
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestNewCommand_EditorArguments(t *testing.T) {
	target := "/tmp/lognav-editor-target"
	for _, tt := range []struct {
		name   string
		editor string
		want   []string
	}{
		{name: "arguments", editor: "code --wait", want: []string{"code", "--wait", target}},
		{name: "multiple whitespace arguments", editor: "editor\t--first  --second", want: []string{"editor", "--first", "--second", target}},
		{name: "whitespace fallback", editor: " \t ", want: []string{"vim", target}},
		{name: "literal metacharacters", editor: "editor --flag; $(command) *", want: []string{"editor", "--flag;", "$(command)", "*", target}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EDITOR", tt.editor)
			cmd := NewCommand(t.Context(), target)
			assert.Equal(t, tt.want, cmd.Args)
		})
	}
}
