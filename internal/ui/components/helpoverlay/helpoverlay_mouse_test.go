package helpoverlay

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// helpoverlay implements the viewport-scroll seam.
var _ component.ScrollTarget = (*Model)(nil)

// helpoverlay implements the hover seam.
var _ component.MouseHoverTarget = (*Model)(nil)

// TestOnMouseHover_DelegatesToList verifies a hover over a help-binding row
// forwards to the inner list (tinting it) without activating, and reports a
// change only on a row move.
func TestOnMouseHover_DelegatesToList(t *testing.T) {
	t.Parallel()
	m, activated := newMouseTestModel(t)

	// Item rows are at screen y=3 (idx 0), y=4 (idx 1), y=5 (idx 2).
	changed := m.OnMouseHover(5, 4)

	assert.True(t, changed, "hovering a help row delegates and reports a change")
	assert.Equal(t, 0, m.list.GetCursor(), "hover must not move the selection")
	assert.Equal(t, 0, *activated, "hover must not run the activate hook")
	assert.False(t, m.OnMouseHover(6, 4), "re-hovering the same row reports no change")
}

// TestOnMouseScroll_PansList verifies a vertical wheel notch pans the inner help
// list (drag-at-edge) and is consumed.
func TestOnMouseScroll_PansList(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	items := make([][]list.Segment, 20)
	for i := range items {
		items[i] = list.PlainItem("binding " + string(rune('a'+i%26)))
	}
	m := New(bundle, "Help", items, nil)
	m.SetPoster(&msgstest.FakePoster{})
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 12})
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor at top")

	ok := m.OnMouseScroll(5, 5, 5)
	assert.True(t, ok, "a wheel notch over the help overlay must be consumed")
	assert.Positive(t, m.list.GetCursor(), "wheel must pan the help list, dragging the cursor at the edge")
}

// newMouseTestModel builds a help overlay with three item rows and a spy
// onSelect that records how many times the activate hook fired. The overlay rect
// is {Y:0, H:10}: the 1-row header pushes the inner list to Y=1, whose own 2-row
// fuzzy input pushes item rows to screen y=3 (idx 0), y=4 (idx 1), y=5 (idx 2).
func newMouseTestModel(t *testing.T) (*Model, *int) {
	t.Helper()
	bundle := depstest.NewTest(t)
	items := [][]list.Segment{
		list.PlainItem("first binding"),
		list.PlainItem("second binding"),
		list.PlainItem("third binding"),
	}
	var activated int
	onSelect := keys.Action(func() { activated++ })
	m := New(bundle, "Test Overlay", items, onSelect)
	m.SetPoster(&msgstest.FakePoster{})
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	return m, &activated
}

// TestOnMousePaste_FuzzyHeader_DelegatesToList verifies a middle-click on the
// help overlay's fuzzy header forwards to the inner list's paste, narrowing the
// binding list. The 1-row header pushes the list to Y=1, whose own 2-row fuzzy
// input occupies screen rows y=1..2.
func TestOnMousePaste_FuzzyHeader_DelegatesToList(t *testing.T) {
	t.Parallel()
	m, activated := newMouseTestModel(t)

	ok := m.OnMousePaste(5, 2, "second") // fuzzy input row; matches "second binding"

	assert.True(t, ok, "middle-click on the help fuzzy header must delegate and consume")
	assert.Equal(t, 1, m.list.VisibleLen(), "pasted filter must narrow the help list")
	assert.Equal(t, 0, *activated, "a paste must not run the activate hook")
}

// TestOnMouseClick_NonCursorRow_MovesCursor verifies that clicking a non-cursor
// help row moves the cursor (select) and does not run the activate hook.
func TestOnMouseClick_NonCursorRow_MovesCursor(t *testing.T) {
	t.Parallel()
	m, activated := newMouseTestModel(t)
	require.Equal(t, 0, m.GetCursor(), "setup: cursor starts at the top row")

	// Item idx 1 sits at screen y = headerH(1) + fuzzy(2) + 1 = 4.
	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "click on a help row must be consumed")
	assert.Equal(t, 1, m.GetCursor(), "non-cursor row click selects the clicked row")
	assert.Equal(t, 0, *activated, "selecting a non-cursor row must NOT run the activate hook")
}

// TestOnMouseClick_CursorRow_RunsActivate verifies that clicking the already-
// selected help row runs the focused-binding activate hook (onSelect) without
// moving the cursor.
func TestOnMouseClick_CursorRow_RunsActivate(t *testing.T) {
	t.Parallel()
	m, activated := newMouseTestModel(t)
	require.Equal(t, 0, m.GetCursor(), "setup: cursor starts at the top row")

	// Item idx 0 (the cursor row) sits at screen y = headerH(1) + fuzzy(2) + 0 = 3.
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed, "cursor-row click must be consumed")
	assert.Equal(t, 0, m.GetCursor(), "activate must not move the cursor")
	assert.Equal(t, 1, *activated, "cursor-row click must run the activate hook exactly once")
}

// TestOnMouseClick_Header_NotConsumed verifies that a click on the 1-row title
// header (above the inner list) is not consumed by the overlay.
func TestOnMouseClick_Header_NotConsumed(t *testing.T) {
	t.Parallel()
	m, activated := newMouseTestModel(t)

	// y=0 is the title header row, above the inner list's rect (Y=1).
	consumed := m.OnMouseClick(5, 0, uv.MouseLeft)
	assert.False(t, consumed, "a click on the title header must not be consumed")
	assert.Equal(t, 0, *activated, "header click must not run the activate hook")
}

var _ component.MouseTarget = (*Model)(nil)
