package queryeditor

import (
	"testing"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetKeybinds_CuratesHintsByMode verifies that each query subview exposes
// only its current curated actions. The root adds Help separately.
func TestGetKeybinds_CuratesHintsByMode(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := New(t.Context(), depstest.NewTest(t), &EventHandler{})
	require.NoError(t, err)

	cases := []struct {
		name   string
		setup  func()
		labels []string
	}{
		{
			name:   "normal",
			setup:  func() { m.showFiles, m.showSnippets = false, false },
			labels: []string{"edit", "snippets", "files", "save"},
		},
		{
			name:   "files",
			setup:  func() { m.showFiles, m.showSnippets = true, false },
			labels: []string{"open", "rename", "delete", "save"},
		},
		{
			name:   "snippets",
			setup:  func() { m.showFiles, m.showSnippets = false, true },
			labels: []string{"insert", "close", "filter", "save"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			hints := keys.HintBindings(m.GetKeybinds())
			require.Len(t, hints, len(tt.labels))
			labels := make([]string, len(hints))
			priorities := make([]int, len(hints))
			for i, hint := range hints {
				labels[i] = hint.Label
				priorities[i] = hint.Priority
			}
			assert.Equal(t, tt.labels, labels)
			assert.Equal(t, []int{2, 3, 4, 5}, priorities)
		})
	}
}

// TestGetKeybinds_RemappedSaveHintUsesConfiguredKey verifies that the hint and
// dispatch both follow a remapped first key, while a modified key stays whole.
func TestGetKeybinds_RemappedSaveHintUsesConfiguredKey(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	defaultModel, err := New(t.Context(), depstest.NewTest(t), &EventHandler{})
	require.NoError(t, err)
	defaultSave := findHint(t, defaultModel.GetKeybinds(), "save")
	chip, line := defaultSave.HintPill()
	assert.Equal(t, "ctrl+s", chip)
	assert.Equal(t, "save", line)

	bundle := depstest.NewTest(t)
	bundle.Config.Keys.Snapshot = config.KeyBind{"s"}
	m, err := New(t.Context(), bundle, &EventHandler{})
	require.NoError(t, err)
	poster := &msgstest.FakePoster{}
	m.SetPoster(poster)

	remappedSave := findHint(t, m.GetKeybinds(), "save")
	chip, line = remappedSave.HintPill()
	assert.Equal(t, "s", chip)
	assert.Equal(t, "ave", line)

	m.HandleKey(keystest.PressRuneUV(t, 's'))
	require.Len(t, poster.Posted, 1)
	assert.IsType(t, msgs.ShowDialogMsg{}, poster.Posted[0], "remapped hint keeps the existing query-save action")
}

func findHint(t *testing.T, bindings []keys.Binding, label string) keys.Binding {
	t.Helper()
	for _, hint := range keys.HintBindings(bindings) {
		if hint.Label == label {
			return hint
		}
	}
	t.Fatalf("hint %q not found", label)
	return keys.Binding{}
}
