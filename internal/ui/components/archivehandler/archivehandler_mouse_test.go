package archivehandler

import (
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// mouseModel seeds two ready archives, lists them and assigns a rect + one draw
// pass so the inner list geometry (2-row fuzzy header, then rows) is live. The
// left (list) half is x < 20 for the 40-wide rect; y=3 is list display row 1.
func mouseModel(t *testing.T) (*Model, *fakePoster) {
	t.Helper()
	// Recent SubmittedAt so the archives are ready-and-collectable (an old one
	// would be expired, and activation would route to the notice branch instead).
	now := time.Now()
	m, f := realModel(t,
		&archive.Archive{Name: "arch-a", SubmittedAt: now.Add(-2 * time.Minute), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}},
		&archive.Archive{Name: "arch-b", SubmittedAt: now.Add(-1 * time.Minute), Instances: []archive.InstanceEntry{instance("a", archive.StateSuccess)}},
	)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))
	require.NotEmpty(t, m.fh.GetItemUnderCursor(), "setup: a row must be under the cursor")
	return m, f
}

// TestOnMouseHover_DelegatesToFileHandler proves the Archive tab's hover seam is
// wired: a hover over an archive row forwards through the owned filehandler to the
// inner list (tinting it) without moving the cursor, and reports a change only on
// an actual row move.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnMouseHover_DelegatesToFileHandler(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := mouseModel(t)
	top := m.fh.GetItemUnderCursor()

	changed := m.OnMouseHover(5, 3)

	assert.True(t, changed, "hover over an archive row delegates and reports a change")
	assert.Equal(t, top, m.fh.GetItemUnderCursor(), "hover must not move the cursor")
	assert.False(t, m.OnMouseHover(6, 3), "re-hovering the same row reports no change")
}

// TestOnMouseScroll_DelegatesToFileHandler proves a vertical wheel notch over the
// archive list forwards through the owned filehandler and is consumed (so the root
// does not fall back to a synthetic arrow key).
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnMouseScroll_DelegatesToFileHandler(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := mouseModel(t)

	assert.True(t, m.OnMouseScroll(5, 5, 5),
		"a wheel notch over the archive list must be consumed (delegated to the filehandler)")
}

// TestOnMousePaste_DelegatesToFileHandler proves a middle-click on the archive
// list's fuzzy header forwards through the filehandler to the list paste, narrowing
// the list to the matching archive.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnMousePaste_DelegatesToFileHandler(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := mouseModel(t)

	ok := m.OnMousePaste(3, 0, "arch-a")

	assert.True(t, ok, "middle-click on the archive fuzzy header must delegate and consume")
	assert.Contains(t, m.fh.GetItemUnderCursor(), "arch-a", "pasted filter must narrow the archive list")
}

// TestOnMouseRight_DelegatesToFileHandler proves the right-click seam reaches the
// filehandler's context menu (Rename/Delete anchored at the pointer).
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnMouseRight_DelegatesToFileHandler(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := mouseModel(t)

	ok := m.OnMouseRight(5, 3)

	assert.True(t, ok, "right-click on an archive row must be consumed")
	assert.True(t, hasContextMenu(f.events()), "right-click must post an anchored context menu")
}

// TestOnMouseDoubleClick_DelegatesToFileHandler proves the double-click seam
// activates the row (the archive Enter router), observable as the collect emit for
// a ready archive, while a single click only selects.
//
//nolint:paralleltest // New + seedArchive use the XDG_DATA_HOME env tempdir.
func TestOnMouseDoubleClick_DelegatesToFileHandler(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, f := mouseModel(t)

	require.True(t, m.OnMouseClick(5, 3, uv.MouseLeft), "single click on an archive row is consumed")
	require.False(t, hasCollect(f.events()), "a single click must NOT activate (select only)")

	assert.True(t, m.OnMouseDoubleClick(5, 3, uv.MouseLeft), "double-click must be consumed")
	assert.True(t, hasCollect(f.events()), "double-click must run the activate action (collect a ready archive)")
}

// TestSeams_ArchiveTabOptsIntoEveryInputSeam records the Archive tab's deliberate
// seam opt-ins. The wrapper HOLDS its filehandler rather than embedding it, so each
// seam is an explicit decision; this is the readable counterpart to the
// compile-time assertion block (which fails the build on a signature change).
func TestSeams_ArchiveTabOptsIntoEveryInputSeam(t *testing.T) {
	t.Parallel()

	implemented, missing := component.Seams((*Model)(nil))

	assert.Equal(t, []string{
		"KeyTarget", "MouseTarget", "SecondaryMouseTarget", "DoubleClickTarget",
		"MousePasteTarget", "ScrollTarget", "MouseHoverTarget", "PasteTarget",
		"FocusReleaser", "ContextMenuOpener",
	}, implemented, "the Archive tab opts into every input seam the filehandler offers")
	assert.Empty(t, missing, "no input seam is left unrouted on the Archive tab")
}

// hasContextMenu reports whether any posted event is an anchored context menu.
func hasContextMenu(events []uv.Event) bool {
	for _, ev := range events {
		if cm, ok := ev.(msgs.ShowContextMenuMsg); ok && cm.Anchored {
			return true
		}
	}
	return false
}

// hasCollect reports whether any posted event is an ArchiveCollectMsg (the ready-
// archive activation).
func hasCollect(events []uv.Event) bool {
	for _, ev := range events {
		if _, ok := ev.(ArchiveCollectMsg); ok {
			return true
		}
	}
	return false
}
