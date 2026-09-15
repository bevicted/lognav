package snapshothandler

import (
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// writeSnapshotFile writes a valid snapshot container named name+ext into the
// session's snapshot dir so the filehandler lists it and ReadFileUnderCursor can
// restore it. Fails the test on any IO error.
func writeSnapshotFile(t *testing.T, name string) {
	t.Helper()
	dir, err := snapshot.Dir()
	require.NoError(t, err, "snapshot.Dir must resolve")
	p := filepath.Join(dir, name+snapshot.FileExt)
	c := snapshottest.NewContainerFile(t, p)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "q " + name}), "state frame must be written")
	require.NoError(t, c.Close(), "Close must succeed after writing the snapshot")
}

// hasRestoreMsg reports whether any posted event is a successful (no-error)
// SnapshotRestoreMsg — the signal that the cursor-row click ran the load action.
func hasRestoreMsg(events []uv.Event) bool {
	for _, ev := range events {
		if rm, ok := ev.(msgs.SnapshotRestoreMsg); ok && rm.Err == nil {
			return true
		}
	}
	return false
}

// drainFileIO forwards every posted filehandler.IOMsg to OnFileIO so the
// filehandler's list reflects the directory contents.
func drainFileIO(m *Model, fp *fakePoster) {
	for _, ev := range fp.events() {
		if io, ok := ev.(filehandler.IOMsg); ok {
			m.OnFileIO(io)
		}
	}
}

// TestOnMouseClick_ForwardsToFileHandler verifies that a non-cursor row click
// forwards to the owned filehandler and moves the list cursor (select only),
// and that a single cursor-row click does NOT run the activate hook (double-click
// required for activation).
//
//nolint:paralleltest // newTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseClick_ForwardsToFileHandler(t *testing.T) {
	m, fp := newTestModel(t)
	// Two snapshot files so the list has multiple rows.
	writeSnapshotFile(t, "snap-a")
	writeSnapshotFile(t, "snap-b")

	// Populate the filehandler list and render once so the inner list's rect is set.
	m.fh.ListFiles()
	drainFileIO(m, fp)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	topItem := m.fh.GetItemUnderCursor()
	require.NotEmpty(t, topItem, "list must have at least one snapshot row under the cursor")

	// Non-cursor row click (display idx 1) selects it: listRect.Y=2, row=3-2=1.
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed, "left-half click on a snapshot row must be consumed")
	assert.NotEqual(t, topItem, m.fh.GetItemUnderCursor(),
		"non-cursor row click must move the cursor to a different snapshot")
	require.False(t, hasRestoreMsg(fp.events()), "selecting a row must NOT restore it")

	// Single click on the (now-cursor) row: select-only in double-click-activate
	// mode — does NOT restore (no activate).
	consumed2 := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed2, "cursor-row single click must be consumed")
	assert.False(t, hasRestoreMsg(fp.events()), "single click on cursor row must NOT restore (double-click required)")
}

// TestOnMouseDoubleClick_ForwardsToFileHandler_Activates verifies that
// OnMouseDoubleClick delegates to the owned filehandler and activates (restores)
// a snapshot row, and that a single click still does not activate.
//
//nolint:paralleltest // newTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseDoubleClick_ForwardsToFileHandler_Activates(t *testing.T) {
	m, fp := newTestModel(t)
	writeSnapshotFile(t, "snap-a")
	writeSnapshotFile(t, "snap-b")

	m.fh.ListFiles()
	drainFileIO(m, fp)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	topItem := m.fh.GetItemUnderCursor()
	require.NotEmpty(t, topItem, "list must have at least one snapshot row under the cursor")

	// Single click on a non-cursor row selects (moves cursor) without restoring.
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed, "single click on non-cursor row must be consumed (select)")
	require.False(t, hasRestoreMsg(fp.events()), "single click on non-cursor row must NOT restore")

	// Double-click on that (now-cursor) row: moves cursor AND restores.
	consumed2 := m.OnMouseDoubleClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed2, "double-click must be consumed")
	assert.True(t, hasRestoreMsg(fp.events()), "double-click must run the restore action")
}

// TestOnMouseHover_DelegatesToFileHandler verifies a hover over a snapshot row
// forwards through the filehandler to the inner list (tinting it) without moving
// the cursor, and reports a change only on a row move.
//
//nolint:paralleltest // newTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseHover_DelegatesToFileHandler(t *testing.T) {
	m, fp := newTestModel(t)
	writeSnapshotFile(t, "snap-a")
	writeSnapshotFile(t, "snap-b")
	m.fh.ListFiles()
	drainFileIO(m, fp)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))
	top := m.fh.GetItemUnderCursor()

	// Left half, y=3 → list display idx 1 (listRect.Y=2, row=1).
	changed := m.OnMouseHover(5, 3)

	assert.True(t, changed, "hover over a snapshot row delegates and reports a change")
	assert.Equal(t, top, m.fh.GetItemUnderCursor(), "hover must not move the cursor")
	assert.False(t, m.OnMouseHover(6, 3), "re-hovering the same row reports no change")
}

// TestOnMouseScroll_DelegatesToFileHandler verifies a vertical wheel notch over
// the snapshot list forwards through the owned filehandler and is consumed.
//
//nolint:paralleltest // newTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseScroll_DelegatesToFileHandler(t *testing.T) {
	m, fp := newTestModel(t)
	writeSnapshotFile(t, "snap-a")
	writeSnapshotFile(t, "snap-b")
	m.fh.ListFiles()
	drainFileIO(m, fp)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	ok := m.OnMouseScroll(5, 5, 5)
	assert.True(t, ok, "a wheel notch over the snapshot list must be consumed (delegated to the filehandler)")
}

// TestOnMousePaste_DelegatesToFileHandler verifies that a middle-click on the
// snapshot-list fuzzy header forwards through the filehandler to the list paste,
// narrowing the list to the matching snapshot.
//
//nolint:paralleltest // newTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMousePaste_DelegatesToFileHandler(t *testing.T) {
	m, fp := newTestModel(t)
	writeSnapshotFile(t, "snap-a")
	writeSnapshotFile(t, "snap-b")

	m.fh.ListFiles()
	drainFileIO(m, fp)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	// Middle-click the fuzzy header (top row) in the left (list) half: "snap-a"
	// matches only snap-a.
	ok := m.OnMousePaste(3, 0, "snap-a")

	assert.True(t, ok, "middle-click on the snapshot fuzzy header must delegate and consume")
	assert.Contains(t, m.fh.GetItemUnderCursor(), "snap-a", "pasted filter must narrow the snapshot list")
}

// TestOnMouseRight_DelegatesToFileHandler verifies the right-click seam reaches
// the filehandler's context menu (Rename/Delete anchored at the pointer).
//
//nolint:paralleltest // newTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseRight_DelegatesToFileHandler(t *testing.T) {
	m, fp := newTestModel(t)
	writeSnapshotFile(t, "snap-a")
	writeSnapshotFile(t, "snap-b")
	m.fh.ListFiles()
	drainFileIO(m, fp)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	ok := m.OnMouseRight(5, 3)

	assert.True(t, ok, "right-click on a snapshot row must be consumed")
	assert.True(t, hasContextMenu(fp.events()), "right-click must post an anchored context menu")
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

// TestSeams_SnapshotTabOptsIntoItsInputSeams records tab 3's deliberate seam
// opt-ins. The wrapper HOLDS its filehandler rather than embedding it, so each
// seam is an explicit decision; this is the readable counterpart to the
// compile-time assertion block in snapshothandler.go (which fails the build on a
// signature change). FocusReleaser is the one seam tab 3 does not take — unlike
// the Archive tab, which wires it (see archivehandler.ReleaseFocus).
func TestSeams_SnapshotTabOptsIntoItsInputSeams(t *testing.T) {
	t.Parallel()

	implemented, missing := component.Seams((*Model)(nil))

	assert.Equal(t, []string{
		"KeyTarget", "MouseTarget", "SecondaryMouseTarget", "DoubleClickTarget",
		"MousePasteTarget", "ScrollTarget", "MouseHoverTarget", "PasteTarget",
		"ContextMenuOpener",
	}, implemented, "tab 3 routes every input seam the filehandler offers")
	assert.Equal(t, []string{"FocusReleaser"}, missing,
		"FocusReleaser is the only seam tab 3 does not opt into")
}
