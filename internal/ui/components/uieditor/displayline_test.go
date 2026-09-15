package uieditor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// white-box helper: position the cursor by direct field access (in-package).
func atRowCol(e *Editor, row, col int) {
	e.row = row
	e.col = col
}

func TestEditor_DisplayLines_WrapsLogicalLine(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetValue("hello world")
	e.SetWidth(8)

	dls := e.DisplayLines()
	require.Len(t, dls, 2)
	assert.Equal(t, "hello ", string(dls[0].Runes))
	assert.Equal(t, 0, dls[0].LogicalIdx)
	assert.Equal(t, 0, dls[0].ColWidth)
	assert.Equal(t, 0, dls[0].StartCol)
	assert.Equal(t, "world ", string(dls[1].Runes))
	assert.Equal(t, 0, dls[1].LogicalIdx)
	assert.Equal(t, 6, dls[1].ColWidth)
	assert.Equal(t, 6, dls[1].StartCol)
}

func TestEditor_DisplayLines_MultipleLogicalLines(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetValue("a\nbb")
	e.SetWidth(10)

	dls := e.DisplayLines()
	require.Len(t, dls, 2)
	assert.Equal(t, 0, dls[0].LogicalIdx)
	assert.Equal(t, 1, dls[1].LogicalIdx)
}

func TestEditor_LineInfo_AcrossWrapBoundary(t *testing.T) {
	t.Parallel()
	// "hello world" @ width 8 wraps to grid ["hello ", "world "] (6 + 6 runes).
	tests := []struct {
		name           string
		col            int
		wantRowOffset  int
		wantCharOffset int
	}{
		{name: "first display line mid", col: 4, wantRowOffset: 0, wantCharOffset: 4},
		{name: "wrap break belongs to next line", col: 6, wantRowOffset: 1, wantCharOffset: 0},
		{name: "second display line mid", col: 8, wantRowOffset: 1, wantCharOffset: 2},
		{name: "end of line", col: 11, wantRowOffset: 1, wantCharOffset: 5},
		{name: "start of line", col: 0, wantRowOffset: 0, wantCharOffset: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			e.SetValue("hello world")
			e.SetWidth(8)
			atRowCol(e, 0, tt.col)
			li := e.LineInfo()
			assert.Equal(t, tt.wantRowOffset, li.RowOffset, "RowOffset")
			assert.Equal(t, tt.wantCharOffset, li.CharOffset, "CharOffset")
		})
	}
}

func TestEditor_LineInfo_CJKDoubleWidth(t *testing.T) {
	t.Parallel()
	// "日本語" @ width 10 stays on one display line; each rune is width 2.
	e := New()
	e.SetValue("日本語")
	e.SetWidth(10)
	atRowCol(e, 0, 2) // cursor after 本, before 語
	li := e.LineInfo()
	assert.Equal(t, 0, li.RowOffset)
	assert.Equal(t, 4, li.CharOffset, "two wide runes precede the cursor → 4 display columns")
}

func TestEditor_CursorDisplayLine_SumsPriorWraps(t *testing.T) {
	t.Parallel()
	// "a" (1 display line) + "hello world" (2 display lines @ width 8).
	e := New()
	e.SetValue("a\nhello world")
	e.SetWidth(8)

	// cursor at end (row 1, col 11) → 1 (line 0) + RowOffset 1 = 2.
	assert.Equal(t, 1, e.Line())
	assert.Equal(t, 2, e.CursorDisplayLine())

	// cursor at start of logical line 1 → 1 (line 0) + RowOffset 0 = 1.
	atRowCol(e, 1, 0)
	assert.Equal(t, 1, e.CursorDisplayLine())
}

func TestEditor_Scroll_KeepsCursorVisibleAcrossResize(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetValue("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7") // 8 logical lines, 1 display line each at wide width
	e.SetWidth(40)

	// Invariant after every resize: the cursor's display line is visible, i.e.
	// 0 <= CursorDisplayLine - ScrollYOffset < height, and the scroll offset
	// never exceeds the maximum representable offset.
	requireVisible := func(t *testing.T) {
		t.Helper()
		rel := e.CursorDisplayLine() - e.ScrollYOffset()
		assert.GreaterOrEqual(t, rel, 0, "cursor above viewport")
		assert.Less(t, rel, e.Height(), "cursor below viewport")
		maxOff := max(len(e.DisplayLines())-e.Height(), 0)
		assert.LessOrEqual(t, e.ScrollYOffset(), maxOff, "scroll past end")
	}

	e.SetHeight(3) // cursor at end (line 7) → scrolls into view
	requireVisible(t)
	e.SetHeight(2) // shrink → cursor stays visible
	requireVisible(t)
	e.SetHeight(20) // grow past content → clamps to 0
	requireVisible(t)
	assert.Equal(t, 0, e.ScrollYOffset())
}
