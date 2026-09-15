package instancepicker

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstancePicker_OnPaste_HonoursListFocus pins the focus gate: a
// bracketed paste only reaches the fuzzy search input when that input is
// focused, matching HandleKey (a focused input is the only text sink) and
// filehandler.OnPaste. Without the gate, a paste arriving while the search is
// closed silently filtered the instance list.
func TestInstancePicker_OnPaste_HonoursListFocus(t *testing.T) {
	t.Parallel()

	t.Run("unfocused search ignores the paste", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestModelWithKeybinds(t)
		seedNamedInstances(t, m, "us-east", "us-south", "eu-de")
		require.False(t, m.list.IsFocused(), "the search input must start unfocused")

		m.OnPaste(uv.PasteEvent{Content: "us-"})

		assert.Equal(t, 3, m.list.VisibleLen(), "an unfocused search must not be filtered by a paste")
	})

	t.Run("focused search receives the paste", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestModelWithKeybinds(t)
		seedNamedInstances(t, m, "us-east", "us-south", "eu-de")
		m.list.Focus()
		require.True(t, m.list.IsFocused(), "the search input must be focused")

		m.OnPaste(uv.PasteEvent{Content: "us-"})

		assert.Equal(t, 2, m.list.VisibleLen(), "a focused search must filter to the two us-* instances")
	})
}
