package filtermenu

import (
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/ui/components/buttonstrip"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/component/componenttest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStrip builds the menu's button strip for the inner span [x, maxX), the
// same way Model.strip does, so tests can compute absolute button columns.
func testStrip(x, maxX int) buttonstrip.Strip {
	return buttonstrip.Strip{Labels: buttonLabels[:], X: x, MaxX: maxX}
}

func hasMsg[T any](fp *msgstest.FakePoster) bool {
	for _, ev := range fp.Posted {
		if _, ok := ev.(T); ok {
			return true
		}
	}
	return false
}

func newTestMenu(t *testing.T, rules []filter.Rule) *Model {
	t.Helper()
	return New(depstest.NewTest(t), rules)
}

func TestNew_PrefillsRulesPlusTrailingPlaceholder(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "foo"},
		{Type: filter.Exclude, Value: "noise"},
	})
	// two real rows + one trailing Unknown placeholder
	require.Len(t, m.rows, 3)
	assert.Equal(t, filter.Include, m.rows[0].typ)
	assert.Equal(t, "foo", m.rows[0].input.Value())
	assert.Equal(t, filter.Exclude, m.rows[1].typ)
	assert.Equal(t, "noise", m.rows[1].input.Value())
	assert.Equal(t, filter.Unknown, m.rows[2].typ)
	assert.Empty(t, m.rows[2].input.Value())
}

func TestNew_EmptyRules_HasOnlyPlaceholder(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil)
	require.Len(t, m.rows, 1)
	assert.Equal(t, filter.Unknown, m.rows[0].typ)
}

func TestNew_CursorStartsOnFirstRow(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "foo"}})
	assert.Equal(t, focusRow, m.focus)
	assert.Equal(t, 0, m.cursorRow)
	assert.True(t, m.rows[0].input.Focused())
}

func TestModel_ImplementsOverlayInterfaces(t *testing.T) {
	t.Parallel()
	var _ component.Component = (*Model)(nil)
	var _ component.KeyTarget = (*Model)(nil)
	var _ component.Sizer = (*Model)(nil)
}

func TestCycleType_ForwardWraps(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "foo"}})
	m.cycleType(+1)
	assert.Equal(t, filter.Exclude, m.rows[0].typ)
	m.cycleType(+1)
	assert.Equal(t, filter.HasField, m.rows[0].typ)
	m.cycleType(+1)
	assert.Equal(t, filter.LacksField, m.rows[0].typ)
	m.cycleType(+1) // wrap
	assert.Equal(t, filter.Include, m.rows[0].typ)
}

func TestCycleType_BackwardWraps(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "foo"}})
	m.cycleType(-1)
	assert.Equal(t, filter.LacksField, m.rows[0].typ)
}

func TestCycleType_OnPlaceholderCommitsAndAppends(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil) // only the Unknown placeholder
	require.Len(t, m.rows, 1)
	m.cycleType(+1)
	// the placeholder became a real Include row, and a new placeholder appended
	require.Len(t, m.rows, 2)
	assert.Equal(t, filter.Include, m.rows[0].typ)
	assert.Equal(t, filter.Unknown, m.rows[1].typ)
	assert.Equal(t, focusRow, m.focus)
	assert.Equal(t, 0, m.cursorRow)
}

func TestNav_DownThroughRowsIntoButtons(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "a"},
		{Type: filter.Exclude, Value: "b"},
	}) // rows: a, b, placeholder
	assert.Equal(t, 0, m.cursorRow)
	m.moveRow(+1)
	assert.Equal(t, 1, m.cursorRow)
	assert.True(t, m.rows[1].input.Focused())
	assert.False(t, m.rows[0].input.Focused())
	m.moveRow(+1) // onto placeholder
	assert.Equal(t, 2, m.cursorRow)
	m.moveRow(+1) // off last row -> buttons
	assert.Equal(t, focusButtons, m.focus)
	assert.Equal(t, btnApply, m.selectedButton)
	assert.False(t, m.rows[2].input.Focused())
}

func TestNav_UpFromButtonsResetsToApply(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	m.focus = focusButtons
	m.selectedButton = btnCancel
	m.moveRow(-1) // up from buttons -> last row
	assert.Equal(t, focusRow, m.focus)
	assert.Equal(t, len(m.rows)-1, m.cursorRow)
	// re-entering buttons later defaults to Apply
	m.focus = focusButtons
	m.selectedButton = btnApply
	assert.Equal(t, btnApply, m.selectedButton)
}

func TestNav_ButtonHorizontal(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil)
	m.focus = focusButtons
	m.selectedButton = btnApply
	m.moveButton(+1)
	assert.Equal(t, btnClear, m.selectedButton)
	m.moveButton(+1)
	assert.Equal(t, btnCancel, m.selectedButton)
	m.moveButton(+1) // clamp at last
	assert.Equal(t, btnCancel, m.selectedButton)
	m.moveButton(-3) // clamp at first
	assert.Equal(t, btnApply, m.selectedButton)
}

func TestClearRow_EmptiesValueKeepsRow(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "foo"}})
	m.clearRow()
	assert.Empty(t, m.rows[0].input.Value())
	require.Len(t, m.rows, 2) // row + placeholder still present
	assert.Equal(t, filter.Include, m.rows[0].typ)
}

func TestClearAll_ResetsRowsAndFocusesApply(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "a"},
		{Type: filter.Exclude, Value: "b"},
	})
	m.clearAll()
	require.Len(t, m.rows, 1) // sole blank placeholder
	assert.Equal(t, filter.Unknown, m.rows[0].typ)
	assert.Empty(t, m.rows[0].input.Value())
	assert.Equal(t, focusButtons, m.focus, "clear parks focus on the button row")
	assert.Equal(t, btnApply, m.selectedButton, "clear selects the Apply button")
	assert.False(t, m.rows[0].input.Focused(), "row input is blurred while focus is on buttons")
}

func TestDeletePrev_RemovesEmptyRowLandsPrevious(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "a"},
		{Type: filter.Exclude, Value: ""}, // empty real row
	}) // rows: a, (empty exclude), placeholder
	m.cursorRow = 1
	m.deletePrev()
	require.Len(t, m.rows, 2) // a, placeholder
	assert.Equal(t, 0, m.cursorRow)
	assert.Equal(t, "a", m.rows[0].input.Value())
	assert.Equal(t, len(m.rows[0].input.Value()), m.rows[0].input.Position())
}

func TestDeletePrev_EmptyFirstRowLandsNewFirst(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: ""}, // empty first
		{Type: filter.Exclude, Value: "b"},
	}) // rows: (empty include), b, placeholder
	m.cursorRow = 0
	m.deletePrev()
	require.Len(t, m.rows, 2) // b, placeholder
	assert.Equal(t, 0, m.cursorRow)
	assert.Equal(t, "b", m.rows[0].input.Value())
}

func TestDeletePrev_EmptyFirstRowEmptiesToPlaceholder(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: ""}})
	// rows: (empty include), placeholder
	m.cursorRow = 0
	m.deletePrev()
	require.Len(t, m.rows, 1) // sole placeholder remains
	assert.Equal(t, filter.Unknown, m.rows[0].typ)
	assert.Equal(t, 0, m.cursorRow)
}

func TestDeletePrev_OnPlaceholderJustMovesUp(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	// rows: a, placeholder
	m.cursorRow = 1 // placeholder
	m.deletePrev()
	require.Len(t, m.rows, 2) // placeholder NOT deleted
	assert.Equal(t, 0, m.cursorRow)
}

func TestDeleteHere_RemovesEmptyRowLandsNext(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: ""}, // empty
		{Type: filter.Exclude, Value: "b"},
	}) // rows: (empty include), b, placeholder
	m.cursorRow = 0
	m.deleteHere()
	require.Len(t, m.rows, 2) // b, placeholder
	assert.Equal(t, 0, m.cursorRow)
	assert.Equal(t, "b", m.rows[0].input.Value())
}

func TestDeleteHere_OnPlaceholderNoop(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	m.cursorRow = 1 // placeholder
	m.deleteHere()
	require.Len(t, m.rows, 2)
	assert.Equal(t, 1, m.cursorRow)
}

func TestHandleKey_BackspaceEmptyRowDeletesRow(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "a"},
		{Type: filter.Exclude, Value: ""},
	}) // rows: a, (empty exclude), placeholder
	m.cursorRow = 1
	m.focusActiveRowInput()
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyBackspace})
	require.Len(t, m.rows, 2) // empty row deleted
	assert.Equal(t, 0, m.cursorRow)
}

func TestHandleKey_BackspaceNonEmptyRowEditsInput(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "ab"}})
	m.cursorRow = 0
	m.focusActiveRowInput()
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyBackspace})
	assert.Equal(t, "a", m.rows[0].input.Value()) // input deleted a char, row kept
	require.Len(t, m.rows, 2)
}

func TestHandleKey_TypingOnPlaceholderCommitsToInclude(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil) // only placeholder
	m.cursorRow = 0
	m.focusActiveRowInput()
	m.HandleKey(uv.KeyPressEvent{Code: 'x', Text: "x"})
	require.Len(t, m.rows, 2) // committed + new placeholder
	assert.Equal(t, filter.Include, m.rows[0].typ)
	assert.Equal(t, "x", m.rows[0].input.Value())
	assert.Equal(t, filter.Unknown, m.rows[1].typ)
}

func TestHandleKey_TabCyclesType(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyTab})
	assert.Equal(t, filter.Exclude, m.rows[0].typ)
}

func TestHandleKey_DownEntersButtonsAndEnterFiresApply(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	// Down through rows then into buttons.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyDown}) // -> placeholder
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyDown}) // -> buttons (Apply)
	assert.Equal(t, focusButtons, m.focus)
	closed := 0
	m.SetOnClose(func() { closed++ })
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) // press Apply
	assert.True(t, hasMsg[msgs.FilterAppliedMsg](fp))
	assert.Equal(t, 1, closed, "Apply must call the host close seam once")
}

func TestHandleKey_EscCancels(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	closed := 0
	m.SetOnClose(func() { closed++ })
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
	assert.Equal(t, 1, closed, "Esc must call the host close seam once")
	assert.False(t, hasMsg[msgs.FilterAppliedMsg](fp)) // Cancel does not apply
}

func TestPreferredSize_GrowsWithRows(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "a"},
		{Type: filter.Exclude, Value: "b"},
	})
	w, h := m.PreferredSize(200, 100)
	assert.Positive(t, w)
	assert.Positive(t, h)
	// clamps to the available area
	wc, hc := m.PreferredSize(5, 3)
	assert.LessOrEqual(t, wc, 5)
	assert.LessOrEqual(t, hc, 3)
}

func TestContentW_ReservesCaretCellForWidestValue(t *testing.T) {
	t.Parallel()
	val := "include-me-please-this-is-a-long-value" // ASCII: width == len
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: val}})
	// Value band width = inner width minus the chip column and its gap.
	valueBandW := m.contentW() - chipPadW - chipSpace
	assert.Greater(t, valueBandW, len(val),
		"value band must be one cell wider than the widest value so the end caret has its own cell (no scroll/crop)")
}

func TestChipLabel_PadsToLacksFieldWidth(t *testing.T) {
	t.Parallel()
	assert.Len(t, chipLabel(filter.Include), chipPadW)
	assert.Len(t, chipLabel(filter.LacksField), chipPadW)
	assert.Len(t, chipLabel(filter.Unknown), chipPadW) // blank, still padded
}

func TestDraw_RendersFilterTitleOnBorder(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "foo"}})
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	m.SetRect(rect)
	s := uv.NewScreenBuffer(rect.X+rect.W, rect.Y+rect.H)
	m.Draw(s)

	// The "Filter" title is drawn onto the top border row (rect.Y).
	var top strings.Builder
	for x := rect.X; x < rect.X+rect.W; x++ {
		if c := s.CellAt(x, rect.Y); c != nil {
			top.WriteString(c.Content)
		}
	}
	assert.Contains(t, top.String(), "Filter", "the Filter title must render on the top border")
}

func TestDraw_DoesNotPanic(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "foo"},
		{Type: filter.HasField, Value: ".data.log"},
	})
	componenttest.DrawMatrix(t, m.SetRect, m.Draw)
}

func TestOnMouseClick_RuleRowFocuses(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{
		{Type: filter.Include, Value: "a"},
		{Type: filter.Exclude, Value: "b"},
	})
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	// inner.X==2, inner.Y==1; the value band starts at chipPadW+chipSpace past
	// inner.X (x>=14). Click row 1 (y==2) inside the value band.
	consumed := m.OnMouseClick(15, 2, uv.MouseLeft)
	assert.True(t, consumed)
	assert.Equal(t, focusRow, m.focus)
	assert.Equal(t, 1, m.cursorRow)
	assert.True(t, m.rows[1].input.Focused())
	assert.Equal(t, filter.Exclude, m.rows[1].typ, "a value-band click must not cycle the mode")
}

func TestOnMouseClick_ChipCyclesMode(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	// inner.X==2, inner.Y==1; the chip column is [2, 2+chipPadW). Click inside it
	// on row 0.
	consumed := m.OnMouseClick(3, 1, uv.MouseLeft)
	assert.True(t, consumed)
	assert.Equal(t, focusRow, m.focus)
	assert.Equal(t, 0, m.cursorRow)
	assert.Equal(t, filter.Exclude, m.rows[0].typ, "clicking the mode chip cycles to the next mode")
	// A second click advances again.
	m.OnMouseClick(3, 1, uv.MouseLeft)
	assert.Equal(t, filter.HasField, m.rows[0].typ)
}

func TestOnMouseClick_ChipOnPlaceholderCommits(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil) // only the trailing Unknown placeholder
	require.Len(t, m.rows, 1)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	// Click the placeholder's (blank) chip: commits it to Include and appends a
	// fresh placeholder, mirroring Tab.
	consumed := m.OnMouseClick(3, 1, uv.MouseLeft)
	assert.True(t, consumed)
	require.Len(t, m.rows, 2)
	assert.Equal(t, filter.Include, m.rows[0].typ)
	assert.Equal(t, filter.Unknown, m.rows[1].typ)
}

func TestOnMouseClick_ButtonRowPressesApply(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	// rows: a, placeholder => 2 rows; button row at inner.Y+2 == 3. The strip is
	// centered (inner.X=2, inner.X+inner.W=38), so click inside the centered
	// " Apply " chunk.
	startX := testStrip(2, 38).StartX()
	consumed := m.OnMouseClick(startX+1, 3, uv.MouseLeft)
	assert.True(t, consumed)
	assert.True(t, hasMsg[msgs.FilterAppliedMsg](fp))
}

func TestOnMouseHover_OverButtonMovesSelection(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	require.Equal(t, btnApply, m.selectedButton) // default selection
	// Button row at inner.Y+2 == 3. " Apply "(7) + " Clear "(7) precede " Cancel ".
	cancelX := testStrip(2, 38).StartX() + 7 + 7 + 1
	changed := m.OnMouseHover(cancelX, 3)
	assert.True(t, changed, "hovering a new button reports a change")
	assert.Equal(t, btnCancel, m.selectedButton, "hover moves selection onto the hovered button")
	// Hovering off the button row leaves the selection untouched.
	assert.False(t, m.OnMouseHover(cancelX, 2), "hovering a rule row reports no change")
}

func TestDraw_DefaultHighlightsApplyButton(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "foo"}})
	require.Equal(t, focusRow, m.focus) // default focus is the row, not buttons
	w, h := m.PreferredSize(80, 20)
	rect := component.Rect{X: 0, Y: 0, W: w, H: h}
	m.SetRect(rect)
	s := uv.NewScreenBuffer(rect.X+rect.W, rect.Y+rect.H)
	m.Draw(s)

	// "A" of a reverse-video " Apply " must render even though focus is on a row.
	found := false
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			c := s.CellAt(x, y)
			if c != nil && c.Content == "A" && c.Style.Attrs&uv.AttrReverse != 0 {
				found = true
			}
		}
	}
	assert.True(t, found, "Apply is highlighted by default while focus is on a rule row")
}

// TestOnMouseClick_InBoxBelowButtonsIsConsumedNoop verifies that a click inside
// the box but below the button row (neither a rule row nor the button row) is
// consumed without changing any menu state or posting any message.
func TestOnMouseClick_InBoxBelowButtonsIsConsumedNoop(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	// Give a tall box so there is room below the button row.
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 20})
	// rows: a, placeholder => 2 rows; button row at inner.Y+2 == 3.
	// Y == 5 is below the button row but still inside the box (H==20).
	prevFocus := m.focus
	prevCursor := m.cursorRow
	consumed := m.OnMouseClick(5, 5, uv.MouseLeft)
	assert.True(t, consumed, "in-box click must be consumed")
	assert.Equal(t, prevFocus, m.focus, "focus zone must not change")
	assert.Equal(t, prevCursor, m.cursorRow, "cursor row must not change")
	assert.Empty(t, fp.Posted, "no messages must be posted")
}

// TestDismiss_ClosesOnly verifies that Dismiss closes through the host seam and
// does NOT post FilterAppliedMsg (it is a cancel, not an apply).
func TestDismiss_ClosesOnly(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, []filter.Rule{{Type: filter.Include, Value: "a"}})
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	closed := 0
	m.SetOnClose(func() { closed++ })
	m.Dismiss()
	assert.Equal(t, 1, closed, "Dismiss must call the host close seam once")
	assert.False(t, hasMsg[msgs.FilterAppliedMsg](fp), "Dismiss must NOT post FilterAppliedMsg")
}

// TestClose_NilSeam_NoPanic verifies a menu closed before the root wired its
// seam is a silent no-op rather than a panic.
func TestClose_NilSeam_NoPanic(t *testing.T) {
	t.Parallel()
	m := newTestMenu(t, nil)
	m.SetPoster(&msgstest.FakePoster{})
	assert.NotPanics(t, m.Dismiss)
}
