package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeybind_Slash_OpensSearchDialog verifies '/' posts a ShowDialogMsg.
func TestKeybind_Slash_OpensSearchDialog(t *testing.T) {
	t.Parallel()
	m, fp := newViewerModel(t, 3)
	r := m.HandleKey(keystest.PressRuneUV(t, '/'))
	require.Equal(t, component.KeyHandled, r)
	assert.True(t, hasMsg[msgs.ShowDialogMsg](fp.Posted), "'/' opens the search dialog")
}

// TestKeybind_Backslash_OpensJQDialog verifies '\\' posts a ShowDialogMsg.
func TestKeybind_Backslash_OpensJQDialog(t *testing.T) {
	t.Parallel()
	m, fp := newViewerModel(t, 3)
	r := m.HandleKey(keystest.PressRuneUV(t, '\\'))
	require.Equal(t, component.KeyHandled, r)
	assert.True(t, hasMsg[msgs.ShowDialogMsg](fp.Posted), "'\\' opens the jq dialog")
}

// TestKeybind_F_OpensFilterMenu verifies 'f' posts a ShowFilterMenuMsg.
func TestKeybind_F_OpensFilterMenu(t *testing.T) {
	t.Parallel()
	m, fp := newViewerModel(t, 3)
	r := m.HandleKey(keystest.PressRuneUV(t, 'f'))
	require.Equal(t, component.KeyHandled, r)
	assert.True(t, hasMsg[msgs.ShowFilterMenuMsg](fp.Posted), "'f' opens the filter menu")
}

func TestKeybind_ExpandHintAction(t *testing.T) {
	t.Parallel()

	t.Run("nil store", func(t *testing.T) {
		m := New(depstest.NewTest(t))

		require.NotPanics(t, expandHintAction(t, m))
	})
	t.Run("empty store", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m := New(bundle)
		store := NewLogStore(bundle, nil, "test")
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		m.SetStore(store)

		require.NotPanics(t, expandHintAction(t, m))
		assert.Empty(t, store.state.expanded)
	})
	t.Run("populated store toggles cursor log", func(t *testing.T) {
		m, _ := newViewerModel(t, 1)
		action := expandHintAction(t, m)

		action()
		assert.True(t, m.store.state.expanded[m.cursor.log])
		action()
		assert.False(t, m.store.state.expanded[m.cursor.log])
	})
}

// expandHintAction returns the action from the binding composed for the hint line.
func expandHintAction(t testing.TB, m *Model) func() {
	t.Helper()
	for _, binding := range m.GetKeybinds() {
		if binding.Label == "expand" {
			return binding.Action
		}
	}
	require.FailNow(t, "expand hint binding not found")
	return nil
}

// hasMsg reports whether evs contains a value of type T.
func hasMsg[T any](evs []uv.Event) bool {
	for _, ev := range evs {
		if _, ok := ev.(T); ok {
			return true
		}
	}
	return false
}
