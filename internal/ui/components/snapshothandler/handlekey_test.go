package snapshothandler

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// TestHandleKey_Dispatch exercises HandleKey's three-leg precedence:
//  1. alwaysHandleKH fires unconditionally (ctrl+s saves snapshot).
//  2. m.kh (quit, "q") fires only when fh is NOT focused.
//  3. Every unconsumed key falls through unconditionally to m.fh.HandleKey.
//
// The "kh key when fh IS focused" variant cannot be exercised here because
// filehandler does not expose a public Focus() method; that invariant is
// covered by filehandler's own handlekey_test.go (the focused-list path)
// combined with the guard condition visible in snapshothandler.go.
//
//nolint:paralleltest // newTestModel calls t.Setenv; cannot parallelize.
func TestHandleKey_Dispatch(t *testing.T) {
	tests := []struct {
		name  string
		input uv.KeyPressEvent
		want  component.KeyResult
	}{
		{
			// alwaysHandleKH: ctrl+s is bound to ManualSaveSnapshot regardless of
			// fh focus state. Posts ManualSaveSnapshotMsg off-loop via the poster;
			// returns KeyHandled (R5a A11).
			name:  "alwaysHandleKH hit: ctrl+s returns KeyHandled",
			input: keystest.PressCtrlUV(t, 's'),
			want:  component.KeyHandled,
		},
		{
			// kh (quit) when fh is NOT focused: the guard !m.fh.IsFocused() passes
			// and the Quit action runs. It posts msgs.QuitMsg off-loop via the
			// poster and returns KeyHandled (R5a B2a).
			name:  "kh hit when fh not focused: q returns KeyHandled",
			input: keystest.PressRuneUV(t, 'q'),
			want:  component.KeyHandled,
		},
		{
			// Unconditional fh forward: an unbound key (not in kh or alwaysHandleKH)
			// falls through to m.fh.HandleKey. fh returns KeyIgnored for unbound keys
			// when its list is not focused, proving the unconditional forward path.
			name:  "unbound key routes unconditionally to fh: returns KeyIgnored",
			input: keystest.PressRuneUV(t, 'p'),
			want:  component.KeyIgnored,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			// Precondition: fh must not be focused for these test cases, since the
			// "not focused" branch is what we are exercising for the kh case.
			require.False(t, m.fh.IsFocused(), "setup: fh must not be focused")

			got := m.HandleKey(tt.input)

			assert.Equal(t, tt.want, got)
		})
	}
}

// TestHandleKey_SaveShortcut_PostsManualSaveSnapshotMsg verifies that pressing
// the save shortcut (ctrl+s) posts a ManualSaveSnapshotMsg via the poster
// rather than returning it as a tea.Cmd (R5a A11 off-loop conversion).
//
//nolint:paralleltest // newTestModel calls t.Setenv; cannot parallelize.
func TestHandleKey_SaveShortcut_PostsManualSaveSnapshotMsg(t *testing.T) {
	m, fp := newTestModel(t)

	result := m.HandleKey(keystest.PressCtrlUV(t, 's'))

	assert.Equal(t, component.KeyHandled, result)
	require.Len(t, fp.events(), 1, "save shortcut must post exactly one event")
	assert.Equal(t, msgs.ManualSaveSnapshotMsg{}, fp.events()[0])
}

// TestHandleKey_QuitAction_PostsQuitMsg verifies that pressing the Quit key
// ("q") when fh is not focused posts msgs.QuitMsg off-loop via the poster and
// returns a nil cmd (R5a B2a: the quit action no longer returns tea.Quit).
//
//nolint:paralleltest // newTestModel calls t.Setenv; cannot parallelize.
func TestHandleKey_QuitAction_PostsQuitMsg(t *testing.T) {
	m, fp := newTestModel(t)
	require.False(t, m.fh.IsFocused(), "setup: fh must not be focused")

	m.HandleKey(keystest.PressRuneUV(t, 'q'))

	var found bool
	for _, ev := range fp.events() {
		if _, ok := ev.(msgs.QuitMsg); ok {
			found = true
		}
	}
	assert.True(t, found, "quit action must post msgs.QuitMsg off-loop")
}
