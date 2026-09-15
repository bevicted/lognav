package uieditor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEditor_SetCursorAtDisplay_ReverseMapsSoftWrap verifies that a click at a
// display position (x, y) — measured from the editor's drawn content origin,
// including the prompt — reverse-maps through the soft-wrap layout to the
// logical (row, col) under that position, mirroring moveVertical's walk.
func TestEditor_SetCursorAtDisplay_ReverseMapsSoftWrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		width   int
		prompt  string
		yOffset int
		x, y    int
		wantRow int
		wantCol int
	}{
		{
			// dls: ["abcdef ", "xyz "] (each logical line is its own segment at
			// width 40). Click row 0 at x=3 lands on the 4th rune (col 3).
			name:    "row 0 single-width no wrap",
			value:   "abcdef\nxyz",
			width:   40,
			x:       3,
			y:       0,
			wantRow: 0,
			wantCol: 3,
		},
		{
			// "hello world" @ width 8 wraps to ["hello ", "world "]; dl[1] has
			// LogicalIdx 0 and StartCol 6. Click x=2 on that continuation row →
			// col 6+2 = 8 (value[0][8] == 'r').
			name:    "wrapped continuation row",
			value:   "hello world",
			width:   8,
			x:       2,
			y:       1,
			wantRow: 0,
			wantCol: 8,
		},
		{
			// Clicking far past the segment text clamps to the segment end. dl[0]
			// is "hello " (6 runes) → col 6, clamped to the logical line length.
			name:    "past end of display segment clamps",
			value:   "hello world",
			width:   8,
			x:       50,
			y:       0,
			wantRow: 0,
			wantCol: 6,
		},
		{
			// 5 logical lines, each its own display line at width 40. With
			// yOffset 2, click y=1 maps to displayIdx 3 → logical line 3. x=1 →
			// col 1 ('3' of "l3").
			name:    "scrolled yOffset maps displayIdx",
			value:   "l0\nl1\nl2\nl3\nl4",
			width:   40,
			yOffset: 2,
			x:       1,
			y:       1,
			wantRow: 3,
			wantCol: 1,
		},
		{
			// Prompt "> " has width 2. A click at x=1 falls inside the prompt →
			// target column clamps to 0 → caret at col 0.
			name:    "click inside prompt clamps to col 0",
			value:   "hello world",
			width:   8,
			prompt:  "> ",
			x:       1,
			y:       0,
			wantRow: 0,
			wantCol: 0,
		},
		{
			// Prompt width 2, click x=5 → text-relative target 5-2=3 → col 3
			// ('l' of "hello").
			name:    "click beyond prompt subtracts promptWidth",
			value:   "hello world",
			width:   8,
			prompt:  "> ",
			x:       5,
			y:       0,
			wantRow: 0,
			wantCol: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := New()
			e.SetWidth(tt.width)
			e.SetPrompt(tt.prompt)
			e.SetValue(tt.value)
			e.yOffset = tt.yOffset // white-box: simulate a scrolled viewport

			e.SetCursorAtDisplay(tt.x, tt.y)

			assert.Equal(t, tt.wantRow, e.row, "row")
			assert.Equal(t, tt.wantCol, e.col, "col")
		})
	}
}

// TestEditor_SetCursorAtDisplay_DerivedFromDisplayLines cross-checks the wrapped
// case against the actual DisplayLines model rather than hand-computed indices,
// proving the reverse map agrees with the forward wrap authority.
func TestEditor_SetCursorAtDisplay_DerivedFromDisplayLines(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(8)
	e.SetValue("hello world")

	dls := e.DisplayLines()
	require.Len(t, dls, 2)
	require.Equal(t, 0, dls[1].LogicalIdx)
	require.Equal(t, 6, dls[1].StartCol)

	// Click the continuation row (y=1) at x=3 → StartCol + 3 = col 9.
	e.SetCursorAtDisplay(3, 1)
	assert.Equal(t, dls[1].LogicalIdx, e.row)
	assert.Equal(t, dls[1].StartCol+3, e.col)
}

// TestEditor_SetCursorAtDisplay_EmptyBufferNoPanic ensures an empty editor (still
// one empty logical line) does not panic and leaves the caret at the origin.
func TestEditor_SetCursorAtDisplay_EmptyBufferNoPanic(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetCursorAtDisplay(5, 3)
	assert.Equal(t, 0, e.row)
	assert.Equal(t, 0, e.col)
}
