package list

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/component/componenttest"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// newTestModel is the fixture seam: constructs a fresh *Model with a default
// depstest.Bundle and optionally pre-loads items. A FakePoster is injected so
// that any Focus/Unfocus calls do not panic on a nil poster.
func newTestModel(t *testing.T, items [][]Segment) *Model {
	t.Helper()
	m, _ := newTestModelWithPoster(t, items)
	return m
}

// newTestModelWithPoster is like newTestModel but also returns the FakePoster so
// callers can inspect posted FocusMsg events.
func newTestModelWithPoster(t *testing.T, items [][]Segment) (*Model, *msgstest.FakePoster) {
	t.Helper()
	bundle := depstest.NewTest(t)
	fp := &msgstest.FakePoster{}
	m := New(bundle).WithItems(items)
	m.SetPoster(fp)
	return m, fp
}

// threeItems returns a fixed set of three plain-text items for tests that
// need a non-trivial list without caring about specific content.
func threeItems() [][]Segment {
	return [][]Segment{
		PlainItem("alpha"),
		PlainItem("bravo"),
		PlainItem("charlie"),
	}
}

// TestNew_DefaultsApplied verifies that New constructs a non-nil model with an
// initialised keyhandler and a fuzzy-finder input.
func TestNew_DefaultsApplied(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	require.NotNil(t, m)
	assert.NotNil(t, m.kh, "keyhandler must be bound")
	assert.False(t, m.showItemIdx)
	assert.Nil(t, m.fuzziedItems)
	assert.Equal(t, 0, m.cursor)
}

// TestWithItems_SetsItemsAndRebuildsTexts verifies that WithItems populates
// m.items and rebuilds m.itemTexts from the segment content.
func TestWithItems_SetsItemsAndRebuildsTexts(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	require.Len(t, m.items, 3)
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, m.itemTexts)
}

// TestWithFuzzyConfirmAction_StoresAction verifies that the action is stored
// and not called immediately.
func TestWithFuzzyConfirmAction_StoresAction(t *testing.T) {
	t.Parallel()
	called := false
	action := func() {
		called = true
	}
	m := newTestModel(t, threeItems()).WithFuzzyConfirmAction(keys.Action(action))
	assert.NotNil(t, m.onFuzzyConfirm)
	assert.False(t, called, "action must not be called during construction")
}

// TestShowIndex_TogglesIndexColumn verifies that ShowIndex stores the flag and
// that consecutive calls override each other.
func TestShowIndex_TogglesIndexColumn(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, nil)
	assert.False(t, m.showItemIdx)
	m.ShowIndex(true)
	assert.True(t, m.showItemIdx)
	m.ShowIndex(false)
	assert.False(t, m.showItemIdx)
}

// TestSetRect_PropagatesToInnerTextinput verifies that SetRect stores the rect
// on m.drawRect. The fuzzy-finder input uses drawRect to compute its width
// at render time, so storage is the correct contract to pin.
func TestSetRect_PropagatesToInnerTextinput(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, nil)
	want := component.Rect{X: 2, Y: 3, W: 80, H: 24}
	m.SetRect(want)
	assert.Equal(t, want, m.drawRect)
}

// TestUpdate_FuzzyInput_FiltersItems focuses the model, sends a multi-character
// prefix, and asserts that fuzziedItems narrows the visible set.
func TestUpdate_FuzzyInput_FiltersItems(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	// Focus so that text input receives keystrokes.
	m.Focus()
	require.True(t, m.IsFocused())

	// Send "alph" — fuzzy-matches only "alpha" and not "bravo" or "charlie".
	for _, r := range "alph" {
		m.HandleKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	// fuzziedItems should be non-nil (filter active) and narrower than items.
	require.NotNil(t, m.fuzziedItems, "fuzzy filter must activate on text input")
	assert.Less(t, len(m.fuzziedItems), len(m.items), "filter must narrow items")
}

// TestUpdate_CursorDown_Advances sends a MoveDown key and verifies the cursor
// advances by 1.
func TestUpdate_CursorDown_Advances(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	initial := m.cursor
	m.HandleKey(uv.KeyPressEvent{Code: 'j', Text: "j"})
	assert.Equal(t, initial+1, m.cursor)
}

// TestUpdate_CursorUp_ClampsAtZero verifies that sending MoveUp when cursor is
// already at 0 does not go negative.
func TestUpdate_CursorUp_ClampsAtZero(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	assert.Equal(t, 0, m.cursor)
	m.HandleKey(uv.KeyPressEvent{Code: 'k', Text: "k"})
	assert.Equal(t, 0, m.cursor, "cursor must not go below 0")
}

// TestUpdate_CursorDown_ClampsAtEnd verifies that moving down past the last
// item clamps at len(items)-1.
func TestUpdate_CursorDown_ClampsAtEnd(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	// Move down many times — must not exceed len-1.
	for range 10 {
		m.HandleKey(uv.KeyPressEvent{Code: 'j', Text: "j"})
	}
	assert.Equal(t, len(m.items)-1, m.cursor)
}

// TestUpdate_PageDown_AdvancesByPage sends MovePageDown (pgdn) and verifies the
// cursor advances by at least listAreaHeight.
func TestUpdate_PageDown_AdvancesByPage(t *testing.T) {
	t.Parallel()
	// Use enough items so there is something to scroll.
	items := make([][]Segment, 20)
	for i := range items {
		items[i] = PlainItem("item")
	}
	m := newTestModel(t, items)
	m.SetRect(component.Rect{W: 80, H: 10})
	page := m.listAreaHeight()
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyPgDown})
	assert.Equal(t, min(page, len(items)-1), m.cursor)
}

// TestUpdate_PageUp_RetractsByPage sends MovePageUp (pgup) from the bottom and
// verifies the cursor retreats by at least listAreaHeight.
func TestUpdate_PageUp_RetractsByPage(t *testing.T) {
	t.Parallel()
	items := make([][]Segment, 20)
	for i := range items {
		items[i] = PlainItem("item")
	}
	m := newTestModel(t, items)
	m.SetRect(component.Rect{W: 80, H: 10})
	// Move to bottom first.
	m.HandleKey(uv.KeyPressEvent{Code: 'G', Text: "G"})
	atBottom := m.cursor
	page := m.listAreaHeight()
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyPgUp})
	assert.Equal(t, max(0, atBottom-page), m.cursor)
}

// TestUpdate_Home_ResetsCursor sends MoveToTop ('g') and verifies cursor lands
// at 0.
func TestUpdate_Home_ResetsCursor(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	// Move down first.
	m.HandleKey(uv.KeyPressEvent{Code: 'j', Text: "j"})
	require.Positive(t, m.cursor)
	m.HandleKey(uv.KeyPressEvent{Code: 'g', Text: "g"})
	assert.Equal(t, 0, m.cursor)
}

// TestUpdate_End_JumpsToLast sends MoveToBottom ('G') and verifies the cursor
// lands at the last item.
func TestUpdate_End_JumpsToLast(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.HandleKey(uv.KeyPressEvent{Code: 'G', Text: "G"})
	assert.Equal(t, len(m.items)-1, m.cursor)
}

// TestJumpToTopBottom_AbsoluteUnderFilterAndShortList verifies that MoveToTop
// ('g') and MoveToBottom ('G') set an absolute display position: with a fuzzy
// filter active the bottom jump lands on the last *visible* row (the visible
// count, not the unfiltered item count, bounds it), and on a list that fits the
// viewport neither jump scrolls (yOffset stays 0). The scrolling case pins that
// the jumps still reclamp yOffset to keep the cursor on screen.
func TestJumpToTopBottom_AbsoluteUnderFilterAndShortList(t *testing.T) {
	t.Parallel()

	t.Run("filter_active_shorter_than_viewport", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, threeItems())
		// listAreaHeight = 8 > 3 items: the list always fits the viewport.
		m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
		m.Focus()
		// "l" matches alpha and charlie, not bravo → 2 visible of 3 items.
		m.HandleKey(uv.KeyPressEvent{Code: 'l', Text: "l"})
		require.Equal(t, 2, m.VisibleLen(), "filter must leave 2 visible rows")
		// Leave the input so the regular (unfocused) group handles g/G; the
		// filter stays applied.
		m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEsc})
		require.False(t, m.IsFocused())

		m.HandleKey(uv.KeyPressEvent{Code: 'G', Text: "G"})
		assert.Equal(t, m.VisibleLen()-1, m.cursor, "bottom jump must land on the last visible row")
		assert.Equal(t, 0, m.yOffset, "a list that fits the viewport must not scroll")

		m.HandleKey(uv.KeyPressEvent{Code: 'g', Text: "g"})
		assert.Equal(t, 0, m.cursor, "top jump must land on the first visible row")
		assert.Equal(t, 0, m.yOffset, "a list that fits the viewport must not scroll")
	})

	t.Run("taller_than_viewport_scrolls", func(t *testing.T) {
		t.Parallel()
		// 20 items, listAreaHeight = 5 → maxOffset 15.
		m := newTestModel(t, manyItems(20))
		m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 7})

		m.HandleKey(uv.KeyPressEvent{Code: 'G', Text: "G"})
		assert.Equal(t, 19, m.cursor)
		assert.Equal(t, 15, m.yOffset, "bottom jump must scroll the last row into view")

		m.HandleKey(uv.KeyPressEvent{Code: 'g', Text: "g"})
		assert.Equal(t, 0, m.cursor)
		assert.Equal(t, 0, m.yOffset, "top jump must scroll back to the first row")
	})
}

// TestConfirmAction_FiresOnEnter tests that pressing Enter (ConfirmTextInput)
// calls Unfocus (which posts ReleaseFocus off-loop) regardless of whether
// onFuzzyConfirm is set.
func TestConfirmAction_FiresOnEnter(t *testing.T) {
	t.Parallel()
	m, fp := newTestModelWithPoster(t, threeItems())
	// ConfirmTextInput is an alwaysHandle action — it fires regardless of focus.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	// Unfocus posts ReleaseFocus off-loop; cmd itself is nil when no confirm action is set.
	require.Len(t, fp.Posted, 1, "Enter must trigger exactly one posted event (ReleaseFocus)")
	assert.Equal(t, msgs.FocusMsg{GrabFocus: false}, fp.Posted[0], "posted event must be ReleaseFocus")
}

// TestFuzzyConfirmAction_FiresOnFilteredEnter verifies that when
// onFuzzyConfirm is set and Enter is pressed, the action is invoked alongside
// Unfocus.
func TestFuzzyConfirmAction_FiresOnFilteredEnter(t *testing.T) {
	t.Parallel()
	called := false
	m := newTestModel(t, threeItems()).WithFuzzyConfirmAction(func() {
		called = true
	})
	// Press Enter — alwaysHandle fires the confirm/unfocus path.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	assert.True(t, called, "onFuzzyConfirm must be called on Enter")
}

// TestItemText_ConcatenatesSegments verifies that ItemText joins all segment
// texts without separator.
func TestItemText_ConcatenatesSegments(t *testing.T) {
	t.Parallel()
	segs := []Segment{{Text: "hello"}, {Text: " "}, {Text: "world"}}
	assert.Equal(t, "hello world", ItemText(segs))
}

// TestPlainItem_SingleSegment verifies that PlainItem returns exactly one
// segment containing the given text with a zero style.
func TestPlainItem_SingleSegment(t *testing.T) {
	t.Parallel()
	item := PlainItem("hello")
	require.Len(t, item, 1)
	assert.Equal(t, "hello", item[0].Text)
}

// TestIsFocused_TogglesViaInnerTextinput verifies that IsFocused reflects the
// inner input focus state.
func TestIsFocused_TogglesViaInnerTextinput(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, nil)
	assert.False(t, m.IsFocused())
	m.Focus()
	assert.True(t, m.IsFocused())
}

// TestFocus_Unfocus_TogglesFocusState verifies that Focus and Unfocus correctly
// toggle the model's focus state.
func TestFocus_Unfocus_TogglesFocusState(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, nil)
	m.Focus()
	require.True(t, m.IsFocused())
	m.Unfocus()
	assert.False(t, m.IsFocused())
}

// TestClearFuzzy_DropsFilter verifies that ClearFuzzy resets both the
// input value and the fuzzied items slice.
func TestClearFuzzy_DropsFilter(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.Focus()
	// Activate fuzzy filter with "alph" — matches only "alpha".
	for _, r := range "alph" {
		m.HandleKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	require.NotNil(t, m.fuzziedItems, "filter must be active before ClearFuzzy")
	m.ClearFuzzy()
	assert.Nil(t, m.fuzziedItems, "fuzziedItems must be nil after ClearFuzzy")
	assert.Empty(t, m.input.Value(), "input value must be empty after ClearFuzzy")
}

// TestVisibleLen_ReturnsFilteredCount verifies that VisibleLen returns the
// total count when no filter is active, and the filtered count when fuzzy
// filtering is active.
func TestVisibleLen_ReturnsFilteredCount(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	assert.Equal(t, 3, m.VisibleLen(), "no filter: VisibleLen must equal total items")

	m.Focus()
	// Send "alph" to activate filter — matches only "alpha".
	for _, r := range "alph" {
		m.HandleKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	require.NotNil(t, m.fuzziedItems)
	assert.Equal(t, len(m.fuzziedItems), m.VisibleLen(), "with filter: VisibleLen must equal fuzzied count")
}

// TestMoveCursor_AdvancesAndClamps verifies that MoveCursor moves the cursor by
// the given delta and clamps at both ends.
func TestMoveCursor_AdvancesAndClamps(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.MoveCursor(2)
	assert.Equal(t, 2, m.cursor)
	// Clamp at end.
	m.MoveCursor(100)
	assert.Equal(t, 2, m.cursor)
	// Clamp at beginning.
	m.MoveCursor(-100)
	assert.Equal(t, 0, m.cursor)
}

// manyItems returns n plain-text items labelled "item-<i>" for scroll tests that
// need a list taller than the viewport.
func manyItems(n int) [][]Segment {
	items := make([][]Segment, n)
	for i := range n {
		items[i] = PlainItem("item-" + string(rune('a'+i%26)))
	}
	return items
}

// TestScrollViewport_PansAndDragsCursorAtEdge verifies that ScrollViewport pans
// the viewport (yOffset) by the signed line count, keeps the selection on its
// item while it stays visible, drags the selection to the viewport edge once the
// pan would push it off-screen, and clamps yOffset to the valid range.
func TestScrollViewport_PansAndDragsCursorAtEdge(t *testing.T) {
	t.Parallel()
	// 20 items, drawRect.H = 7 -> listAreaHeight = 5; maxOffset = 20 - 5 = 15.
	tests := []struct {
		name        string
		setupCursor int // MoveCursor delta applied before the scroll
		scroll      int // ScrollViewport delta under test
		wantYOffset int
		wantCursor  int
	}{
		{name: "keeps cursor when still visible", setupCursor: 3, scroll: 1, wantYOffset: 1, wantCursor: 3},
		{name: "drags cursor down at top edge", setupCursor: 0, scroll: 3, wantYOffset: 3, wantCursor: 3},
		{name: "clamps yOffset at bottom", setupCursor: 0, scroll: 100, wantYOffset: 15, wantCursor: 15},
		{name: "drags cursor up at bottom edge", setupCursor: 19, scroll: -3, wantYOffset: 12, wantCursor: 16},
		{name: "clamps yOffset at top", setupCursor: 19, scroll: -100, wantYOffset: 0, wantCursor: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t, manyItems(20))
			m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 7})
			m.MoveCursor(tt.setupCursor)
			m.ScrollViewport(tt.scroll)
			assert.Equal(t, tt.wantYOffset, m.yOffset, "yOffset")
			assert.Equal(t, tt.wantCursor, m.cursor, "cursor")
		})
	}
}

// TestLen_ReturnsItemCount verifies that Len returns the total number of items
// regardless of any fuzzy filter.
func TestLen_ReturnsItemCount(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	assert.Equal(t, 3, m.Len())
	// Len must not change when a filter is active.
	m.Focus()
	m.HandleKey(uv.KeyPressEvent{Code: 'a', Text: "a"})
	assert.Equal(t, 3, m.Len(), "Len must always return total items, not filtered count")
}

// TestAddItem_AppendsAndRebuilds verifies that AddItem appends the item and
// rebuilds itemTexts.
func TestAddItem_AppendsAndRebuilds(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.AddItem(PlainItem("delta"))
	require.Len(t, m.items, 4)
	assert.Equal(t, "delta", m.itemTexts[3])
}

// TestClear_ResetsItems verifies that Clear leaves the model with no items.
func TestClear_ResetsItems(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.Clear()
	assert.Empty(t, m.items)
	assert.Empty(t, m.itemTexts)
}

// TestSetItemAtIndex_ReplacesItem verifies that SetItemAtIndex replaces the
// item and text at the given index.
func TestSetItemAtIndex_ReplacesItem(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.SetItemAtIndex(1, PlainItem("updated"))
	assert.Equal(t, "updated", m.itemTexts[1])
	assert.Equal(t, "updated", m.items[1][0].Text)
}

// TestSetItemUnderCursor_ReplacesCurrentItem verifies that SetItemUnderCursor
// replaces the item at the current cursor position.
func TestSetItemUnderCursor_ReplacesCurrentItem(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.MoveCursor(1)
	require.Equal(t, 1, m.cursor)
	m.SetItemUnderCursor(PlainItem("replaced"))
	assert.Equal(t, "replaced", m.itemTexts[1])
}

// TestGetCursor_FuzzyReturnsOriginalIndex verifies that GetCursor returns the
// original index from m.items (not the fuzzy-rank index) when fuzzy filtering
// is active. This pins list.go:415-420.
func TestGetCursor_FuzzyReturnsOriginalIndex(t *testing.T) {
	t.Parallel()
	// items: index 0=alpha, 1=bravo, 2=charlie
	m := newTestModel(t, threeItems())
	// SetRect to give listAreaHeight a sensible value.
	m.SetRect(component.Rect{W: 80, H: 24})
	m.Focus()
	// Filter to only "charlie" — OriginalIndex must be 2.
	for _, r := range "charlie" {
		m.HandleKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	require.NotNil(t, m.fuzziedItems, "fuzzy filter must be active")
	require.NotEmpty(t, m.fuzziedItems, "at least one match required")
	// cursor is 0 inside fuzziedItems, but GetCursor must return the original index.
	orig := m.fuzziedItems[m.cursor].OriginalIndex
	assert.Equal(t, orig, m.GetCursor(), "GetCursor must return OriginalIndex when filter is active")
	assert.Equal(t, 2, m.GetCursor(), "OriginalIndex for 'charlie' must be 2")
}

// TestGetItemUnderCursor_ReturnsTextAtCursor verifies that GetItemUnderCursor
// returns an empty string on an empty list and returns the text of the item at
// the current cursor position on a non-empty list.
func TestGetItemUnderCursor_ReturnsTextAtCursor(t *testing.T) {
	t.Parallel()
	t.Run("empty_list_returns_empty_string", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, nil)
		assert.Empty(t, m.GetItemUnderCursor())
	})
	t.Run("non_empty_list_returns_cursor_item", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, threeItems())
		m.MoveCursor(1)
		assert.Equal(t, "bravo", m.GetItemUnderCursor())
	})
}

// TestModel_Draw_DoesNotPanicAcrossRectSizes exercises Draw at all canonical
// rect sizes.
func TestModel_Draw_DoesNotPanicAcrossRectSizes(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	componenttest.DrawMatrix(t, m.SetRect, m.Draw)
}

// cellContent returns the content string at (x, y), or "" for nil/space cells.
func cellContent(s component.Screen, x, y int) string {
	c := s.CellAt(x, y)
	if c == nil || c.Content == " " {
		return ""
	}
	return c.Content
}

// TestDraw_FuzzyRow_RendersPromptValueAndBorder pins the cell-native Draw path:
// the prompt "> " at the fuzzy-row start, a typed value glyph after it, and the
// bottom-border separator on the row below the input (tiRect.Y + tiRect.H - 1).
// The rect height (24) keeps tiRect.H == 2, so the input draws on row 0 and the
// separator lands on row 1.
func TestDraw_FuzzyRow_RendersPromptValueAndBorder(t *testing.T) {
	t.Parallel()
	const w, h = 40, 24
	m := newTestModel(t, threeItems())
	m.SetRect(component.Rect{X: 0, Y: 0, W: w, H: h})

	// Focus, then draw once so the input picks up its viewport width (Draw is the
	// only place list propagates width to the input, mirroring the runtime draw
	// loop). Typing before any draw would leave the input at width 0 and scroll
	// the single glyph out of view.
	m.Focus()
	require.True(t, m.IsFocused())
	_ = m.Draw(uv.NewScreenBuffer(w, h))
	m.HandleKey(uv.KeyPressEvent{Code: 'a', Text: "a"})

	canvas := uv.NewScreenBuffer(w, h)
	_ = m.Draw(canvas)

	// Prompt "> " at the fuzzy-row start (row 0).
	assert.Equal(t, ">", cellContent(canvas, 0, 0), "prompt glyph must render at column 0")
	assert.Empty(t, cellContent(canvas, 1, 0), "prompt space must follow the '>' glyph")

	// Typed value glyph immediately after the 2-cell prompt.
	assert.Equal(t, "a", cellContent(canvas, 2, 0), "typed value glyph must render after the prompt")

	// Bottom-border separator on the row below the input.
	tiRect, _ := m.subRects()
	borderRow := tiRect.Y + tiRect.H - 1
	require.Equal(t, 1, borderRow, "border must land on row 1 when tiRect.H == 2")
	assert.Equal(t, "─", cellContent(canvas, 0, borderRow), "bottom-border separator must render below the input")
	assert.Equal(t, "─", cellContent(canvas, w-1, borderRow), "bottom-border separator must span the full width")
}

// TestOnPaste_RefiltersItems exercises the paste-driven refilter path:
// a uv.PasteEvent routes through InsertText + refilter, so the pasted text must
// activate the fuzzy filter (fuzziedItems non-nil and narrowed).
func TestOnPaste_RefiltersItems(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.Focus()
	require.True(t, m.IsFocused())

	// Paste "alph" — fuzzy-matches only "alpha".
	m.OnPaste(uv.PasteEvent{Content: "alph"})

	require.NotNil(t, m.fuzziedItems, "paste must activate the fuzzy filter")
	assert.Less(t, len(m.fuzziedItems), len(m.items), "pasted text must narrow the visible items")
	assert.Equal(t, "alph", m.input.Value(), "pasted content must be inserted into the input")
}

// TestHandleKey_BoundKey_ReturnsHandled verifies that a key bound in the
// regular handler (MoveDown = 'j') returns KeyHandled and advances the cursor.
func TestHandleKey_BoundKey_ReturnsHandled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ev         uv.KeyPressEvent
		wantResult component.KeyResult
		wantCursor int
	}{
		{
			name:       "regular_move_down_handled",
			ev:         keystest.PressRuneUV(t, 'j'),
			wantResult: component.KeyHandled,
			wantCursor: 1,
		},
		{
			name:       "regular_move_up_handled_clamps",
			ev:         keystest.PressRuneUV(t, 'k'),
			wantResult: component.KeyHandled,
			wantCursor: 0,
		},
		{
			name:       "always_handle_enter_handled",
			ev:         keystest.PressKeyUV(t, uv.KeyEnter),
			wantResult: component.KeyHandled,
			wantCursor: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t, threeItems())
			result := m.HandleKey(tt.ev)
			assert.Equal(t, tt.wantResult, result)
			assert.Equal(t, tt.wantCursor, m.cursor)
		})
	}
}

// TestHandleKey_UnboundKey_ReturnsIgnored verifies that a key not bound in any
// handler group returns KeyIgnored when the list is unfocused.
func TestHandleKey_UnboundKey_ReturnsIgnored(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	// 'z' is not bound in alwaysHandle, ti, or regular groups.
	result := m.HandleKey(keystest.PressRuneUV(t, 'z'))
	assert.Equal(t, component.KeyIgnored, result)
}

// TestHandleKey_FocusedTI_ConsumesAllKeys verifies that when the fuzzy-filter
// input is focused, HandleKey returns KeyHandled for any key (the focused
// input swallows all unhandled keys as text).
func TestHandleKey_FocusedTI_ConsumesAllKeys(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.Focus()
	require.True(t, m.IsFocused())

	// 'z' is not bound in the ti group, so it falls through to m.input.HandleKey — consumed.
	result := m.HandleKey(keystest.PressRuneUV(t, 'z'))
	assert.Equal(t, component.KeyHandled, result, "focused input must consume all keys")
}

// TestHandleKey_FocusedTI_CancelReturnsHandled verifies that the cancel (Escape)
// action in the ti group returns KeyHandled and unfocuses the model.
func TestHandleKey_FocusedTI_CancelReturnsHandled(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.Focus()
	require.True(t, m.IsFocused())

	result := m.HandleKey(keystest.PressKeyUV(t, uv.KeyEsc))
	assert.Equal(t, component.KeyHandled, result)
}

// TestVisibleIndices_ReturnsAllWhenUnfiltered verifies VisibleIndices returns
// every item index in order when no fuzzy filter is active.
func TestVisibleIndices_ReturnsAllWhenUnfiltered(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	assert.Equal(t, []int{0, 1, 2}, m.VisibleIndices())
}

// TestVisibleIndices_ReturnsFilteredOriginalIndices verifies VisibleIndices
// returns the OriginalIndex of each fuzzy match (not the rank position) when a
// filter is active.
func TestVisibleIndices_ReturnsFilteredOriginalIndices(t *testing.T) {
	t.Parallel()
	// items: 0=alpha 1=bravo 2=charlie
	m := newTestModel(t, threeItems())
	m.Focus()
	for _, r := range "charlie" {
		m.HandleKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	require.NotNil(t, m.fuzziedItems, "fuzzy filter must be active")
	assert.Equal(t, []int{2}, m.VisibleIndices(),
		"only 'charlie' (original index 2) is visible under the filter")
}

// TestHandleKey_FocusedTI_ClearKey_DropsFilter verifies that the Clear key
// (ctrl+c) clears the active fuzzy filter while the input is focused, keeping
// focus, and reports the key handled.
func TestHandleKey_FocusedTI_ClearKey_DropsFilter(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, threeItems())
	m.Focus()
	require.True(t, m.IsFocused())
	for _, r := range "alph" {
		m.HandleKey(uv.KeyPressEvent{Code: r, Text: string(r)})
	}
	require.NotNil(t, m.fuzziedItems, "filter must be active before clear")

	result := m.HandleKey(keystest.PressCtrlUV(t, 'c'))
	assert.Equal(t, component.KeyHandled, result)
	assert.Nil(t, m.fuzziedItems, "ctrl+c must clear the fuzzy filter")
	assert.Empty(t, m.input.Value(), "ctrl+c must clear the input value")
	assert.True(t, m.IsFocused(), "ctrl+c must keep the input focused")
}

// TestSelectedRowY_ReversesRowAtY verifies SelectedRowY returns the absolute
// screen row of the cursor's item (the reverse of RowAtY), accounting for the
// 2-row fuzzy header and the yOffset scroll position, and reports not-ok on an
// empty list.
func TestSelectedRowY_ReversesRowAtY(t *testing.T) {
	t.Parallel()

	t.Run("empty list reports not ok", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, nil)
		m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 10})
		_, ok := m.SelectedRowY()
		assert.False(t, ok)
	})

	t.Run("cursor at top, no scroll", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, threeItems())
		m.SetRect(component.Rect{X: 5, Y: 3, W: 20, H: 10})
		m.SetCursor(0)
		y, ok := m.SelectedRowY()
		require.True(t, ok)
		// listRect.Y = drawRect.Y + 2 = 5; cursor 0, yOffset 0.
		assert.Equal(t, 5, y)
	})

	t.Run("scrolled viewport tracks the visible cursor row", func(t *testing.T) {
		t.Parallel()
		items := make([][]Segment, 20)
		for i := range items {
			items[i] = PlainItem("item")
		}
		m := newTestModel(t, items)
		m.SetRect(component.Rect{X: 0, Y: 0, W: 20, H: 7}) // items area H=5
		m.SetCursor(19)                                    // scrolls to bottom
		y, ok := m.SelectedRowY()
		require.True(t, ok)
		idx, rok := m.RowAtY(y)
		require.True(t, rok)
		assert.Equal(t, m.DisplayCursor(), idx)
	})
}

// TestSelectByName moves the cursor onto the row matching a given display text,
// returns whether it matched, and (when a fuzzy filter is active) resolves against
// the visible rows so GetCursor maps back to the right original item.
func TestSelectByName(t *testing.T) {
	t.Parallel()

	t.Run("unfiltered match moves cursor", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, threeItems())
		require.Equal(t, 0, m.GetCursor(), "setup: cursor at first row")

		assert.True(t, m.SelectByName("charlie"), "an existing row name matches")
		assert.Equal(t, 2, m.GetCursor(), "cursor lands on the matched row")
	})

	t.Run("no match leaves cursor", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, threeItems())
		m.SetCursor(1)

		assert.False(t, m.SelectByName("does-not-exist"), "an absent name does not match")
		assert.Equal(t, 1, m.GetCursor(), "cursor is unchanged on a miss")
	})

	t.Run("resolves original index while filtering", func(t *testing.T) {
		t.Parallel()
		m := newTestModel(t, threeItems())
		// Filter to the two 'a'-bearing rows (alpha, bravo, charlie all contain 'a';
		// use a tighter filter). "har" matches only "charlie".
		m.input.SetValue("har")
		m.refilter()
		require.Equal(t, 1, m.VisibleLen(), "setup: filter narrows to one row")

		assert.True(t, m.SelectByName("charlie"), "the visible row matches by name")
		assert.Equal(t, 2, m.GetCursor(), "GetCursor maps the display index back to charlie's original index")
	})
}
