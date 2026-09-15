package filehandler

import (
	"os"
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/keys/keystest"
)

// TestWithConfirmAction_OverridesEnter proves WithConfirmAction replaces the
// default Enter (fuzzy-confirm) action wired in New (ReadFileUnderCursor): pressing
// Enter while the list is focused runs the override and does NOT trigger a file
// read. This is the seam the archive handler uses to route Enter to its on-loop
// collect/poll/expired router.
func TestWithConfirmAction_OverridesEnter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("x"), 0o600))

	m, fp, rc, _ := newTestModel(t, dir)
	confirmed := 0
	m.WithConfirmAction(func() { confirmed++ })

	// Populate the list and focus it so the Enter keybind (ConfirmTextInput) fires
	// the onFuzzyConfirm action.
	m.ListFiles()
	msg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(msg)
	m.list.Focus()

	ev := keystest.PressKeyUV(t, uv.KeyEnter)
	_ = m.HandleKey(ev)

	assert.Equal(t, 1, confirmed, "Enter must run the overridden confirm action")
	assert.Empty(t, rc.Path, "the override must replace the default read-and-restore (no file read)")
}
