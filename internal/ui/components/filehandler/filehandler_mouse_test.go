package filehandler

import (
	"os"
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/list"
)

// filehandler implements the viewport-scroll seam.
var _ component.ScrollTarget = (*Model)(nil)

// filehandler implements the hover seam.
var _ component.MouseHoverTarget = (*Model)(nil)

// TestOnMouseHover_LeftHalfTints_RightHalfClears verifies that a hover in the
// left (list) half tints the row under the pointer (without moving the cursor),
// and moving into the right (preview) half clears the active hover.
func TestOnMouseHover_LeftHalfTints_RightHalfClears(t *testing.T) {
	t.Parallel()
	m, _, _, _ := newTestModel(t, t.TempDir())
	items := make([][]list.Segment, 5)
	for i := range items {
		items[i] = list.PlainItem("f" + string(rune('a'+i)))
	}
	m.list.WithItems(items)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	// Left half (x=5 < listW=20), y=4 → listRect.Y=2, row=2, display idx 2.
	assert.True(t, m.OnMouseHover(5, 4), "left-half hover over a row reports a change")
	assert.Equal(t, 0, m.list.GetCursor(), "hover must not move the list cursor")
	assert.False(t, m.OnMouseHover(6, 4), "re-hovering the same row reports no change")
	// Right (preview) half clears the active list hover.
	assert.True(t, m.OnMouseHover(30, 4), "moving into the preview half clears the list hover")
}

// TestOnMouseScroll_PansInnerList verifies a vertical wheel notch pans the inner
// file list (drag-at-edge) and is consumed. The list is seeded directly so the
// test needs no file IO; Draw establishes the inner list's rect.
func TestOnMouseScroll_PansInnerList(t *testing.T) {
	t.Parallel()
	m, _, _, _ := newTestModel(t, t.TempDir())
	items := make([][]list.Segment, 20)
	for i := range items {
		items[i] = list.PlainItem("f" + string(rune('a'+i%26)))
	}
	m.list.WithItems(items)
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor at top")

	ok := m.OnMouseScroll(5, 5, 5)
	assert.True(t, ok, "a wheel notch over the filehandler must be consumed")
	assert.Positive(t, m.list.GetCursor(), "wheel must pan the list, dragging the cursor at the edge")
}

// TestOnMouseClick_LeftHalf_ForwardsToList verifies that a click in the left half
// (x < drawRect.X+listW) is forwarded to the inner list's OnMouseClick.
//
// The inner list's drawRect is set inside filehandler.Draw (not in SetRect), so
// Draw must be called once before clicking to establish the list's rect. Without
// that prior Draw, the list's drawRect is zero and every hit-test inside the list
// would return false for the wrong reason.
func TestOnMouseClick_LeftHalf_ForwardsToList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create files so the list has items — row y=4 maps to display index 2
	// (listRect starts at Y=2, row = 4-2 = 2).
	for _, name := range []string{"alpha.dat", "beta.dat", "charlie.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	// Populate the list by running ListFiles and processing the posted IOMsg.
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.Equal(t, 3, m.list.Len(), "list must have 3 items before clicking")

	// SetRect then Draw once so the inner list's drawRect is established.
	// listW = 40/2 = 20; the list occupies {X:0, Y:0, W:20, H:10}.
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	listW := rect.W / 2 // 20
	require.Equal(t, 20, listW)

	// Click at x=5 (left half, x < 20), y=4.
	// The inner list: listRect.Y = 2 (after 2-row fuzzy header), row = 4-2 = 2,
	// idx = 0 (yOffset) + 2 = 2, which is a valid item (display idx 2 = "charlie").
	// Cursor starts at 0; clicking idx 2 != cursor → select it → returns true.
	initialCursor := m.list.GetCursor()
	require.Equal(t, 0, initialCursor)

	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "click in the left half must be consumed (forwarded to list)")
	assert.Equal(t, 2, m.list.GetCursor(), "click in the left half must move the list cursor")
}

// TestOnMouseClick_RightHalf_ReturnsFalse verifies that a click in the right half
// (x >= drawRect.X+listW, the preview pane) returns false and does not affect the
// inner list cursor.
func TestOnMouseClick_RightHalf_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"alpha.dat", "beta.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	// SetRect then Draw so the inner list's drawRect is established.
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	listW := rect.W / 2 // 20
	require.Equal(t, 20, listW)

	// Cursor starts at 0; click in the right half must not affect it.
	initialCursor := m.list.GetCursor()

	// x=30 >= listW (20) → right half (preview pane).
	consumed := m.OnMouseClick(30, 4, uv.MouseLeft)
	assert.False(t, consumed, "click in the right half (preview) must not be consumed")
	assert.Equal(t, initialCursor, m.list.GetCursor(), "click in the right half must not move the list cursor")
}

// TestOnMouseClick_LeftHalf_SecondClickSelectsOnly verifies that with the inner
// list in WithDoubleClickActivate mode, a second single click on the cursor row
// is select-only (does NOT activate). Activation now requires OnMouseDoubleClick.
func TestOnMouseClick_LeftHalf_SecondClickSelectsOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"alpha.dat", "beta.dat", "charlie.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	var activated int
	m.WithActivate(func() { activated++ })

	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	// First click: x=5, y=4 → display idx 2 != cursor (0) → select it → true.
	consumed1 := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed1, "first click must be consumed (moves cursor to idx 2)")
	assert.Equal(t, 2, m.list.GetCursor(), "first click must move cursor to idx 2")
	assert.Equal(t, 0, activated, "first click (select) must NOT run the activate hook")

	// Second single click on the cursor row: select-only in WithDoubleClickActivate
	// mode — does NOT activate, even though the row is already the cursor.
	consumed2 := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed2, "second click on cursor row must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "cursor must stay at idx 2")
	assert.Equal(t, 0, activated, "second single click on cursor row must NOT activate (double-click-activate mode)")

	// Right half still returns false.
	consumed3 := m.OnMouseClick(30, 4, uv.MouseLeft)
	assert.False(t, consumed3, "right-half click must still return false")
}

// TestOnMouseDoubleClick_LeftHalf_Activates verifies that OnMouseDoubleClick on a
// left-half file row runs the WithActivate hook (activation), while a right-half
// double-click returns false (preview pane, mirroring OnMouseClick's half-split).
func TestOnMouseDoubleClick_LeftHalf_Activates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"alpha.dat", "beta.dat", "charlie.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	var activated int
	m.WithActivate(func() { activated++ })

	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	// Double-click on display idx 2 (x=5, y=4): moves cursor to 2 AND activates.
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at 0")
	consumed := m.OnMouseDoubleClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "left-half double-click must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "double-click must move cursor to the clicked row")
	assert.Equal(t, 1, activated, "double-click must run the activate hook exactly once")
}

// TestOnMouseDoubleClick_RightHalf_ReturnsFalse verifies that a double-click in
// the right (preview) half returns false and does not activate, mirroring
// OnMouseClick's half-split guard.
func TestOnMouseDoubleClick_RightHalf_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"alpha.dat", "beta.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	var activated int
	m.WithActivate(func() { activated++ })

	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	// x=30 >= listW (20) → right half (preview pane).
	consumed := m.OnMouseDoubleClick(30, 4, uv.MouseLeft)
	assert.False(t, consumed, "right-half double-click must not be consumed")
	assert.Equal(t, 0, activated, "right-half double-click must not activate")
}

// TestOnMouseClick_CompileAssertion is satisfied by the var _ assertion in
// filehandler.go; this test is a documentation anchor, not a behavioral check.
func TestOnMouseClick_ImplementsMouseTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	var _ component.MouseTarget = m
	assert.NotNil(t, m, "filehandler.Model must satisfy component.MouseTarget")
}

// TestOnMouseClick_EmptyList_LeftHalf_ReturnsFalse verifies that a left-half
// click when the list has no items returns false (the list itself does so).
func TestOnMouseClick_EmptyList_LeftHalf_ReturnsFalse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	// x=5 in the left half, y=4 → list has no items at idx 2.
	// The fuzzy input header (rows 0-1) is at y<2, so y=4 maps to the item area.
	// But for the input rows (y=0 or y=1), click returns true (focuses input).
	// Here y=4 is item area with no items → list returns false.
	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.False(t, consumed, "left-half click with empty list must not be consumed (no items at row)")
}

// TestOnMouseClick_InputRows verifies that clicking the 2-row fuzzy input area
// (y=0) in the left half focuses the list and returns true, while the same y in
// the right half returns false and does not focus the list.
func TestOnMouseClick_InputRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		x            int
		wantConsumed bool
		wantFocused  bool
	}{
		{name: "left_half_focuses_list", x: 5, wantConsumed: true, wantFocused: true},
		{name: "right_half_returns_false", x: 25, wantConsumed: false, wantFocused: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			m, _, _, _ := newTestModel(t, dir)

			rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
			m.SetRect(rect)
			canvas := uv.NewScreenBuffer(rect.W, rect.H)
			_ = m.Draw(canvas)

			require.False(t, m.IsFocused())

			consumed := m.OnMouseClick(tt.x, 0, uv.MouseLeft)
			assert.Equal(t, tt.wantConsumed, consumed)
			assert.Equal(t, tt.wantFocused, m.IsFocused())
		})
	}
}

// Compile-time assertion: filehandler.Model implements component.MouseTarget.
// This lives here to document the intent; the var _ in filehandler.go is the
// enforcement.
var _ component.MouseTarget = (*Model)(nil)

// Verify the list.Segment import is used.
var _ []list.Segment
