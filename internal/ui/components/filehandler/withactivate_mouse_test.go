package filehandler

import (
	"os"
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
)

// TestWithActivate_SingleClickSelectOnly_DoubleClickActivates proves that with
// WithDoubleClickActivate (enabled by filehandler.New), the WithActivate hook is
// NOT run on a cursor-row single click (select-only), but IS run on
// OnMouseDoubleClick.
func TestWithActivate_SingleClickSelectOnly_DoubleClickActivates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"alpha.dat", "beta.dat", "charlie.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	var activated int
	m.WithActivate(func() { activated++ })

	// Populate the list.
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.Equal(t, 3, m.list.Len(), "list must have 3 items before clicking")

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	// First click: non-cursor row (display idx 2) selects it, no activate.
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top")
	consumed1 := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed1, "non-cursor row click must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "non-cursor row click selects the row")
	assert.Equal(t, 0, activated, "selecting a non-cursor row must NOT run the activate hook")

	// Second single click on the cursor row: in WithDoubleClickActivate mode, still
	// select-only — does NOT activate.
	consumed2 := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed2, "cursor-row single click must be consumed (select-only)")
	assert.Equal(t, 2, m.list.GetCursor(), "cursor stays at idx 2")
	assert.Equal(t, 0, activated, "cursor-row single click must NOT activate in double-click-activate mode")

	// Double-click on the cursor row: moves cursor AND activates.
	consumed3 := m.OnMouseDoubleClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed3, "double-click must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "cursor stays at idx 2 after double-click")
	assert.Equal(t, 1, activated, "double-click must run the activate hook exactly once")
}
