package queryeditor

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHandleKeyTestModel creates a queryeditor Model suitable for HandleKey unit tests,
// routing XDG data to a per-test temp directory. It injects a FakePoster so
// that actions that post focus messages off-loop do not panic. Must be called
// from the *outer* test function (before t.Parallel() in subtests) because
// t.Setenv is not allowed after t.Parallel() has been called.
func newHandleKeyTestModel(t *testing.T) (*Model, *msgstest.FakePoster) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := New(t.Context(), depstest.NewTest(t), &EventHandler{OnConfirmPressed: func() {}})
	require.NoError(t, err)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	return m, fp
}

func TestQueryEditor_HandleKey_AlwaysHandleHit(t *testing.T) {
	// ctrl+s is bound to alwaysHandleKH (Snapshot key). Cannot use t.Parallel
	// here because newHandleKeyTestModel calls t.Setenv.
	tests := []struct {
		name  string
		input uv.KeyPressEvent
		want  component.KeyResult
	}{
		{
			name:  "alwaysHandle ctrl+s fires regardless of mode",
			input: keystest.PressCtrlUV(t, 's'),
			want:  component.KeyHandled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp := newHandleKeyTestModel(t)
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)
			// ctrl+s (Snapshot) posts ShowDialogMsg off-loop via poster.
			require.Len(t, fp.Posted, 1, "Snapshot must post exactly one event")
			dlg, ok := fp.Posted[0].(msgs.ShowDialogMsg)
			require.True(t, ok, "posted event must be ShowDialogMsg")
			require.NotNil(t, dlg.Input)
			assert.Empty(t, dlg.Input.Value(), "query save dialog must not suggest a filename")
			require.NotEmpty(t, dlg.Buttons)
			require.NotNil(t, dlg.Buttons[0].Cmd)
			require.EqualError(t, dlg.Buttons[0].Cmd(""), "enter a filename")
			require.EqualError(t, dlg.Buttons[0].Cmd(" \t"), "enter a filename")
		})
	}
}

func TestQueryEditor_HandleKey_DefaultMode_KHKey(t *testing.T) {
	// "q" is bound to m.kh (Quit). With no active mode and textarea unfocused,
	// only m.kh is consulted in the default branch.
	tests := []struct {
		name  string
		input uv.KeyPressEvent
		want  component.KeyResult
	}{
		{
			name:  "kh quit key handled",
			input: keystest.PressRuneUV(t, 'q'),
			want:  component.KeyHandled,
		},
		{
			name:  "unbound key ignored",
			input: keystest.PressRuneUV(t, 'z'),
			want:  component.KeyIgnored,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newHandleKeyTestModel(t)
			// Ensure default mode: no showFiles, no showSnippets, textarea unfocused.
			require.False(t, m.showFiles, "setup: showFiles must be false")
			require.False(t, m.showSnippets, "setup: showSnippets must be false")
			require.False(t, m.editor.Focused(), "setup: editor must be unfocused")
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestQueryEditor_HandleKey_TextAreaFocused_ConsumesKey(t *testing.T) {
	// When the textArea is focused, any unbound key is fed to the textarea and
	// KeyHandled is returned (leaf-consumes-all for focused textArea).
	tests := []struct {
		name       string
		input      uv.KeyPressEvent
		want       component.KeyResult
		wantPosted []uv.Event
	}{
		{
			name:  "typed character consumed by focused textarea",
			input: keystest.PressRuneUV(t, 'a'),
			want:  component.KeyHandled,
		},
		{
			name:  "textAreaKH esc key handled (CancelTextInput)",
			input: keystest.PressKeyUV(t, uv.KeyEscape),
			want:  component.KeyHandled,
			// CancelTextInput posts ReleaseFocus off-loop via poster.
			wantPosted: []uv.Event{msgs.FocusMsg{GrabFocus: false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp := newHandleKeyTestModel(t)
			// Focus the editor to enter the editor-focused branch.
			m.editor.Focus()
			require.True(t, m.editor.Focused(), "setup: editor must be focused")
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)
			if tt.wantPosted != nil {
				assert.Equal(t, tt.wantPosted, fp.Posted, "posted events must match")
			}
		})
	}
}

func TestQueryEditor_HandleKey_TextAreaFocused_ClearKey_ClearsQuery(t *testing.T) {
	// The Clear key (ctrl+c) clears the query while the editor is focused,
	// mirroring the clear behavior of the other input fields.
	m, _ := newHandleKeyTestModel(t)
	m.setQueryInEditor("namespace foo | limit 10")
	require.Equal(t, "namespace foo | limit 10", m.getQuery(), "setup: query must be seeded")

	m.editor.Focus()
	require.True(t, m.editor.Focused(), "setup: editor must be focused")

	got := m.HandleKey(keystest.PressCtrlUV(t, 'c'))
	assert.Equal(t, component.KeyHandled, got)
	assert.Empty(t, m.getQuery(), "ctrl+c must clear the query text")
}

func TestQueryEditor_HandleKey_OverlayMode_RoutesToChild(t *testing.T) {
	// Both showSnippets and showFiles mode forward keys to their child
	// HandleKey, returning KeyHandled unconditionally (mirroring the Update
	// arm's fall-through). The kh miss path (esc) is exercised in the
	// showSnippets sub-case; the fall-through path (unbound key) in showFiles.
	tests := []struct {
		name       string
		input      uv.KeyPressEvent
		setupMode  func(m *Model)
		want       component.KeyResult
		wantPosted []uv.Event
	}{
		{
			name:      "showSnippets: snippetListKH esc handled",
			input:     keystest.PressKeyUV(t, uv.KeyEscape),
			setupMode: func(m *Model) { m.showSnippets = true },
			want:      component.KeyHandled,
			// hideSnippets posts GrabFocus off-loop via poster.
			wantPosted: []uv.Event{msgs.FocusMsg{GrabFocus: true}},
		},
		{
			name:      "showSnippets: unfocused snippetList fall-through still handled",
			input:     keystest.PressRuneUV(t, 'z'),
			setupMode: func(m *Model) { m.showSnippets = true },
			want:      component.KeyHandled,
		},
		{
			name:      "showFiles: fhKeyKH esc handled",
			input:     keystest.PressKeyUV(t, uv.KeyEscape),
			setupMode: func(m *Model) { m.showFiles = true },
			want:      component.KeyHandled,
		},
		{
			name:      "showFiles: unfocused fh fall-through still handled",
			input:     keystest.PressRuneUV(t, 'z'),
			setupMode: func(m *Model) { m.showFiles = true },
			want:      component.KeyHandled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp := newHandleKeyTestModel(t)
			tt.setupMode(m)
			got := m.HandleKey(tt.input)
			assert.Equal(t, tt.want, got)
			if tt.wantPosted != nil {
				assert.Equal(t, tt.wantPosted, fp.Posted, "posted events must match")
			}
		})
	}
}
