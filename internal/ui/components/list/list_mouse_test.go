package list

import (
	"image/color"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
)

// fiveItems returns a fixed set of five plain-text items for the mouse tests.
// With a rect of {Y:0, H:10} the items area starts at listRect.Y == drawRect.Y+2
// (== 2 only because these tests use Y=0) and spans rows y=2..9, so only the
// first five rows (y=2..6) carry an item and the rest are empty.
func fiveItems() [][]Segment {
	return [][]Segment{
		PlainItem("a"),
		PlainItem("b"),
		PlainItem("c"),
		PlainItem("d"),
		PlainItem("e"),
	}
}

// TestOnMousePaste_InputRow_InsertsAndRefilters verifies a middle-click on the
// fuzzy-input header focuses the input, inserts the clipboard content, and
// re-filters (mirroring the bracketed-paste OnPaste path).
func TestOnMousePaste_InputRow_InsertsAndRefilters(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	require.False(t, m.IsFocused())

	ok := m.OnMousePaste(3, 0, "c") // prompt row; "c" matches item "c"

	assert.True(t, ok, "middle-click on the input row must paste and consume")
	assert.True(t, m.IsFocused(), "paste focuses the fuzzy input")
	assert.Equal(t, "c", m.input.Value(), "content inserted into the fuzzy filter")
	assert.Equal(t, 1, m.VisibleLen(), "fuzzy filter must re-apply after a paste")
}

// TestOnMousePaste_ListRow_Ignored verifies a middle-click on a list item row
// (not the input header) inserts nothing and is not consumed.
func TestOnMousePaste_ListRow_Ignored(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	_, listRect := m.subRects()

	ok := m.OnMousePaste(3, listRect.Y, "c")

	assert.False(t, ok, "middle-click on a list row must not paste")
	assert.Empty(t, m.input.Value(), "no insertion on a list-row middle-click")
}

// TestOnMouseScroll_PansViewportAndConsumes verifies that OnMouseScroll pans the
// viewport by the signed line count (delegating to ScrollViewport) and always
// reports the wheel notch consumed, regardless of pointer position.
func TestOnMouseScroll_PansViewportAndConsumes(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	// H=4 → items area height 2; 5 items → maxOffset = 3.
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 4})

	ok := m.OnMouseScroll(5, 3, 2) // wheel down 2 rows
	assert.True(t, ok, "a wheel notch over the list must be consumed")
	assert.Equal(t, 2, m.yOffset, "wheel down must pan the viewport down")

	ok = m.OnMouseScroll(5, 3, -1) // wheel up 1 row
	assert.True(t, ok)
	assert.Equal(t, 1, m.yOffset, "wheel up must pan the viewport up")
}

// list.Model implements the viewport-scroll seam.
var _ component.ScrollTarget = (*Model)(nil)

// TestSetCursor_AbsoluteClamps verifies SetCursor moves the cursor to an
// absolute display index and clamps both ends. With no active fuzzy filter
// GetCursor() equals the display index, so it is a valid cursor accessor here.
func TestSetCursor_AbsoluteClamps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{name: "in_range", input: 3, want: 3},
		{name: "above_range_clamps_to_last", input: 99, want: 4},
		{name: "below_range_clamps_to_zero", input: -1, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t, fiveItems())
			m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
			m.SetCursor(tt.input)
			assert.Equal(t, tt.want, m.GetCursor())
		})
	}
}

// TestSetCursor_ReclampsYOffset verifies that SetCursor to an item below the
// visible window scrolls yOffset so the cursor stays on screen. The items area
// height is drawRect.H-2 == 2 here, so jumping to the last item must move
// yOffset to keep the cursor within [yOffset, yOffset+areaH).
func TestSetCursor_ReclampsYOffset(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	// H=4 → items area height = 2; 5 items so the window must scroll.
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 4})
	areaH := m.listAreaHeight()
	require.Equal(t, 2, areaH)

	m.SetCursor(4)
	assert.Equal(t, 4, m.cursor)
	assert.GreaterOrEqual(t, m.cursor, m.yOffset, "cursor must be at or below yOffset")
	assert.Less(t, m.cursor, m.yOffset+areaH, "cursor must be within the visible window")

	// Jumping back to the top scrolls the window up as well.
	m.SetCursor(0)
	assert.Equal(t, 0, m.cursor)
	assert.GreaterOrEqual(t, m.cursor, m.yOffset)
	assert.Less(t, m.cursor, m.yOffset+areaH)
}

// TestOnMouseClick_NonCursorRowSelects verifies that clicking an item row that
// is not the cursor row moves the cursor to that row, returns true, and does
// not fire the activate hook.
func TestOnMouseClick_NonCursorRowSelects(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true })
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	// cursor starts at 0; click y=4 → row 2 → display idx 2.
	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "click on a valid item row must be consumed")
	assert.Equal(t, 2, m.GetCursor(), "click must select the row under the pointer")
	assert.False(t, activated, "selecting a non-cursor row must not activate")
}

// TestOnMouseClick_CursorRowActivates verifies that clicking the already-
// selected row runs the activate hook and returns true.
func TestOnMouseClick_CursorRowActivates(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true })
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	m.SetCursor(2)
	// click y=4 → row 2 → display idx 2 == cursor.
	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "click on the cursor row must be consumed")
	assert.True(t, activated, "click on the cursor row must activate")
	assert.Equal(t, 2, m.GetCursor(), "activating must not move the cursor")
}

// TestOnMouseClick_FilterActive_SelectsDisplayRow verifies that the click hit
// test (now shared with OnMouseDoubleClick via RowAtY) still resolves a screen
// row to a *display* index while a fuzzy filter is active: clicking the second
// visible row selects it, and a click on a row past the filtered count — which
// still carries an item in the unfiltered list — is not consumed.
func TestOnMouseClick_FilterActive_SelectsDisplayRow(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, [][]Segment{
		PlainItem("alpha"),
		PlainItem("bravo"),
		PlainItem("charlie"),
		PlainItem("delta"),
		PlainItem("echo"),
	})
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	m.Focus()
	// "l" matches alpha, charlie and delta — not bravo or echo.
	m.OnPaste(uv.PasteEvent{Content: "l"})
	require.Equal(t, 3, m.VisibleLen(), "filter must leave three rows visible")

	// y=3 → row 1 → display idx 1 (the second visible row).
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed, "click on a visible filtered row must be consumed")
	assert.Equal(t, 1, m.DisplayCursor(), "click must select the clicked display row")

	// y=5 → row 3 → display idx 3 ≥ VisibleLen (3): no visible item there, even
	// though the unfiltered list has 5 items.
	assert.False(t, m.OnMouseClick(5, 5, uv.MouseLeft), "click past the filtered rows must not be consumed")
	assert.Equal(t, 1, m.DisplayCursor(), "an unconsumed click must not move the cursor")
}

// TestOnMouseClick_InputRowsFocus verifies that clicking the top 2-row fuzzy
// input focuses it and returns true.
func TestOnMouseClick_InputRowsFocus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		y    int
	}{
		{name: "prompt_row", y: 0},
		{name: "border_row", y: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t, fiveItems())
			m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
			require.False(t, m.IsFocused())
			consumed := m.OnMouseClick(3, tt.y, uv.MouseLeft)
			assert.True(t, consumed, "click on the input area must be consumed")
			assert.True(t, m.IsFocused(), "click on the input area must focus it")
		})
	}
}

// TestOnMouseClick_BelowItemsReturnsFalse verifies that clicking an item-area
// row that has no item (display index >= VisibleLen) is not consumed.
func TestOnMouseClick_BelowItemsReturnsFalse(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	// y=9 → row 7 → display idx 7 ≥ VisibleLen (5): no item there.
	consumed := m.OnMouseClick(5, 9, uv.MouseLeft)
	assert.False(t, consumed, "click below the last item must not be consumed")
}

// TestRowAtY_MapsScreenRowToDisplayIndex verifies that RowAtY maps an absolute
// screen row to the visible display index of the item rendered there, returning
// ok=false for the fuzzy-input header, rows below the last item, and an empty
// list. With rect {Y:0, H:10} the items area starts at row 2.
func TestRowAtY_MapsScreenRowToDisplayIndex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		y       int
		wantIdx int
		wantOK  bool
	}{
		{name: "header_top_row", y: 0, wantOK: false},
		{name: "header_second_row", y: 1, wantOK: false},
		{name: "first_item", y: 2, wantIdx: 0, wantOK: true},
		{name: "third_item", y: 4, wantIdx: 2, wantOK: true},
		{name: "last_item", y: 6, wantIdx: 4, wantOK: true},
		{name: "below_last_item", y: 7, wantOK: false},
		{name: "far_below", y: 99, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t, fiveItems())
			m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
			idx, ok := m.RowAtY(tt.y)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantIdx, idx)
			}
		})
	}
}

// TestRowAtY_AccountsForYOffset verifies that RowAtY adds the scroll offset, so a
// click maps to the scrolled-into-view item, not the raw row.
func TestRowAtY_AccountsForYOffset(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	// H=4 → items area height 2; scroll to the last item so yOffset > 0.
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 4})
	m.SetCursor(4)
	require.Positive(t, m.yOffset, "setup: window must have scrolled")

	// First visible row (y == listRect.Y) maps to the first scrolled-in item.
	_, listRect := m.subRects()
	idx, ok := m.RowAtY(listRect.Y)
	require.True(t, ok)
	assert.Equal(t, m.yOffset, idx, "first visible row maps to yOffset")
}

// TestRowAtY_EmptyList verifies RowAtY reports no row when there are no items.
func TestRowAtY_EmptyList(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, nil)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	_, ok := m.RowAtY(2)
	assert.False(t, ok, "an empty list has no clickable row")
}

// TestSubRects_ExposesInputAndListAreas verifies the public SubRects wrapper
// returns the same 2-row input header and items area the private subRects does.
func TestSubRects_ExposesInputAndListAreas(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 3, Y: 0, W: 20, H: 10})
	tiRect, listRect := m.SubRects()
	priv1, priv2 := m.subRects()
	assert.Equal(t, priv1, tiRect, "public SubRects must match the private input rect")
	assert.Equal(t, priv2, listRect, "public SubRects must match the private list rect")
	assert.Equal(t, 3, listRect.X, "list content X must reflect the drawRect X origin")
}

// TestDisplayCursor_ReturnsDisplayIndexNotOriginal verifies DisplayCursor tracks
// the visible (fuzzied) row of the cursor, whereas GetCursor maps back to the
// original item index. Filtering to a single deep match makes the two diverge.
func TestDisplayCursor_ReturnsDisplayIndexNotOriginal(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems()) // alpha, bravo, charlie
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	m.Focus()
	// "c" fuzzy-matches only "charlie" (original index 2).
	m.HandleKey(uv.KeyPressEvent{Code: 'c', Text: "c"})
	require.Equal(t, 1, m.VisibleLen(), "filter must narrow to the single match")

	m.SetCursor(0)
	assert.Equal(t, 0, m.DisplayCursor(), "DisplayCursor is the visible row index")
	assert.Equal(t, 2, m.GetCursor(), "GetCursor is the original item index of that match")
}

// TestOnMouseClick_InputRowPlacesCaretAtX verifies that clicking the fuzzy-input
// row places the input caret at the rune index corresponding to the clicked
// column. The test draws once to give the input a viewport width, types "abcdef"
// so there are glyphs to click on, then draws again so the input scroll state is
// current before the click.
func TestOnMouseClick_InputRowPlacesCaretAtX(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		clickX  int // absolute screen column (tiRect.X is 0 here)
		wantPos int // expected rune-cursor index inside the input value
	}{
		{name: "caret_at_value_start", clickX: 2, wantPos: 0}, // column 2 = after "> " prompt; rune 0
		{name: "caret_at_rune_3", clickX: 5, wantPos: 3},      // columns 2+0,2+1,2+2 → runes 0,1,2; column 5 → rune 3
		{name: "caret_at_value_end", clickX: 8, wantPos: 6},   // all 6 runes visible; column 8 → rune 6 (clamped to len)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const w, h = 40, 10
			m := newTestModel(t, fiveItems())
			m.SetRect(component.Rect{X: 0, Y: 0, W: w, H: h})

			// Draw once to propagate viewport width to the input (mirroring the
			// runtime draw loop — typing before any draw leaves width 0).
			m.Focus()
			require.True(t, m.IsFocused())
			_ = m.Draw(uv.NewScreenBuffer(w, h))

			// Type "abcdef" into the focused fuzzy input.
			for _, ch := range "abcdef" {
				m.HandleKey(uv.KeyPressEvent{Code: ch, Text: string(ch)})
			}

			// Draw again so the input's view-scroll state reflects the full value.
			_ = m.Draw(uv.NewScreenBuffer(w, h))

			tiRect, _ := m.subRects()

			// Click on the input row (y == tiRect.Y).
			consumed := m.OnMouseClick(tt.clickX, tiRect.Y, uv.MouseLeft)
			assert.True(t, consumed, "click on input row must be consumed")
			assert.True(t, m.IsFocused(), "input must remain focused after click")
			assert.Equal(t, tt.wantPos, m.input.Position(), "caret must be placed at the rune index under the clicked column")
		})
	}
}

var _ component.MouseHoverTarget = (*Model)(nil)

// hoverBgAt returns the background color of the cell at (x, y), or nil.
func hoverBgAt(s component.Screen, x, y int) color.Color {
	c := s.CellAt(x, y)
	if c == nil {
		return nil
	}
	return c.Style.Bg
}

// TestList_OnMouseHover_OverRow_SetsHover verifies that hovering an item row
// records it as the hover row and reports a change; the cursor/selection is not
// moved (hover is visual only).
func TestList_OnMouseHover_OverRow_SetsHover(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	_, listRect := m.subRects()
	require.Equal(t, 0, m.cursor, "setup: cursor at item 0")

	changed := m.OnMouseHover(5, listRect.Y+2)

	assert.True(t, changed, "hovering a new row reports a change")
	assert.Equal(t, 2, m.hoverRow, "hover records the row under the pointer")
	assert.Equal(t, 0, m.cursor, "hover must not move the cursor/selection")
}

// TestList_OnMouseHover_OffRows_ClearsHover verifies that moving onto the fuzzy
// header (or below the items) clears an active hover and reports the change.
func TestList_OnMouseHover_OffRows_ClearsHover(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	_, listRect := m.subRects()
	require.True(t, m.OnMouseHover(5, listRect.Y+2), "setup: hover a row")

	changed := m.OnMouseHover(5, 0) // header row

	assert.True(t, changed, "clearing an active hover reports a change")
	assert.Equal(t, -1, m.hoverRow, "moving off the item rows clears the hover")
}

// TestList_OnMouseHover_SameRow_NoChange verifies re-hovering the same row
// reports no change (gate to row moves).
func TestList_OnMouseHover_SameRow_NoChange(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	_, listRect := m.subRects()
	require.True(t, m.OnMouseHover(5, listRect.Y+2), "setup: first hover changes")

	changed := m.OnMouseHover(8, listRect.Y+2) // same row, different column

	assert.False(t, changed, "re-hovering the same row reports no change")
	assert.Equal(t, 2, m.hoverRow)
}

// TestList_HoverRow_TintsRow verifies the hovered (non-cursor) row is tinted with
// the configured hover background while the cursor row and other rows are not.
func TestList_HoverRow_TintsRow(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, fiveItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	require.True(t, m.bundle.Config.Style.HoverRowBg.IsSet(), "setup: hover bg has a default color")
	want := m.bundle.Config.Style.HoverRowBg.Color
	_, listRect := m.subRects()
	require.Equal(t, 0, m.cursor, "setup: cursor at item 0 (top row)")
	require.True(t, m.OnMouseHover(5, listRect.Y+2), "setup: hover item 2")

	s := uv.NewScreenBuffer(20, 10)
	m.Draw(s)

	const probe = 15 // blank padding column past the short item text
	assert.Equal(t, want, hoverBgAt(s, listRect.X+probe, listRect.Y+2),
		"hovered row must get the hover background tint")
	assert.NotEqual(t, want, hoverBgAt(s, listRect.X+probe, listRect.Y),
		"cursor row must NOT be tinted")
	assert.NotEqual(t, want, hoverBgAt(s, listRect.X+probe, listRect.Y+1),
		"a non-hovered, non-cursor row must not be tinted")
}

// TestWithDoubleClickActivate_SingleClick_CursorRow_DoesNotActivate verifies
// that on a list built with WithDoubleClickActivate, a single click on the
// already-selected row does NOT fire the activate hook (select-only mode).
//
//nolint:dupl // structurally similar to TestOnMouseDoubleClick_CursorRow_Activates but asserts opposite behavior (no activation)
func TestWithDoubleClickActivate_SingleClick_CursorRow_DoesNotActivate(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }).WithDoubleClickActivate()
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	m.SetCursor(2)
	// click y=4 → row 2 → display idx 2 == cursor.
	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "click on the cursor row must still be consumed")
	assert.False(t, activated, "single click on cursor row must NOT activate in doubleClickActivate mode")
	assert.Equal(t, 2, m.GetCursor(), "cursor must not move")
}

// TestWithDoubleClickActivate_SingleClick_NonCursorRow_MovesOnly verifies
// that on a list built with WithDoubleClickActivate, a single click on a
// non-cursor row only moves the cursor and never activates.
//
//nolint:dupl // structurally similar to TestOnMouseDoubleClick_Row_MovesAndActivates but calls OnMouseClick (not OnMouseDoubleClick) and asserts no activation
func TestWithDoubleClickActivate_SingleClick_NonCursorRow_MovesOnly(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }).WithDoubleClickActivate()
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	// cursor starts at 0; click y=4 → row 2 → display idx 2 ≠ cursor.
	consumed := m.OnMouseClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "click on a valid item row must be consumed")
	assert.Equal(t, 2, m.GetCursor(), "click must move the cursor to the clicked row")
	assert.False(t, activated, "single click must not activate in doubleClickActivate mode")
}

// TestOnMouseDoubleClick_Row_MovesAndActivates verifies that OnMouseDoubleClick
// on an item row moves the cursor there AND fires the activate hook.
//
//nolint:dupl // structurally similar to TestWithDoubleClickActivate_SingleClick_NonCursorRow_MovesOnly but calls OnMouseDoubleClick and asserts activation
func TestOnMouseDoubleClick_Row_MovesAndActivates(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }).WithDoubleClickActivate()
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	// y=4 → row 2 → display idx 2.
	consumed := m.OnMouseDoubleClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "double-click on a valid item row must be consumed")
	assert.Equal(t, 2, m.GetCursor(), "double-click must move the cursor to the clicked row")
	assert.True(t, activated, "double-click on an item row must activate")
}

// TestOnMouseDoubleClick_CursorRow_Activates verifies double-click on the
// already-selected row also activates.
//
//nolint:dupl // structurally similar to TestWithDoubleClickActivate_SingleClick_CursorRow_DoesNotActivate but calls OnMouseDoubleClick and asserts activation
func TestOnMouseDoubleClick_CursorRow_Activates(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }).WithDoubleClickActivate()
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	m.SetCursor(2)
	// click y=4 → display idx 2 == cursor.
	consumed := m.OnMouseDoubleClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "double-click on the cursor row must be consumed")
	assert.True(t, activated, "double-click on the cursor row must activate")
	assert.Equal(t, 2, m.GetCursor(), "cursor must stay on the same row")
}

// TestOnMouseDoubleClick_FuzzyHeader_FocusesInput verifies double-click on the
// fuzzy-input header focuses the input without activating.
func TestOnMouseDoubleClick_FuzzyHeader_FocusesInput(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }).WithDoubleClickActivate()
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	require.False(t, m.IsFocused())
	// y=0 is the fuzzy-input prompt row.
	consumed := m.OnMouseDoubleClick(3, 0, uv.MouseLeft)
	assert.True(t, consumed, "double-click on the input header must be consumed")
	assert.True(t, m.IsFocused(), "double-click on the input header must focus it")
	assert.False(t, activated, "double-click on the input header must not activate")
}

// TestOnMouseDoubleClick_OutsideItems_ReturnsFalse verifies double-click below
// the last item row returns false and does not activate.
func TestOnMouseDoubleClick_OutsideItems_ReturnsFalse(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }).WithDoubleClickActivate()
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	// y=9 → row 7 → display idx 7 ≥ VisibleLen(5): no item there.
	consumed := m.OnMouseDoubleClick(5, 9, uv.MouseLeft)
	assert.False(t, consumed, "double-click below the last item must not be consumed")
	assert.False(t, activated, "double-click outside items must not activate")
}

// TestOnMouseDoubleClick_NoDoubleClickActivateFlag_StillActivates verifies that
// OnMouseDoubleClick works even when WithDoubleClickActivate was NOT called —
// the method is unconditional (it is only reached via the double-click route).
func TestOnMouseDoubleClick_NoDoubleClickActivateFlag_StillActivates(t *testing.T) {
	t.Parallel()
	activated := false
	m := newTestModel(t, fiveItems())
	m.WithActivate(func() { activated = true }) // no WithDoubleClickActivate
	m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
	// y=4 → display idx 2.
	consumed := m.OnMouseDoubleClick(5, 4, uv.MouseLeft)
	assert.True(t, consumed, "double-click on a valid row must be consumed regardless of the flag")
	assert.Equal(t, 2, m.GetCursor())
	assert.True(t, activated, "OnMouseDoubleClick must activate regardless of the flag")
}
