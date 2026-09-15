package uieditor

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newScrollEditor builds an editor of nLines short, non-wrapping logical lines
// (so display-line index == logical row) with width 40 and viewport height 5.
func newScrollEditor(t *testing.T, nLines int) *Editor {
	t.Helper()
	e := New()
	e.SetWidth(40)
	e.SetHeight(5)
	lines := make([]string, nLines)
	for i := range lines {
		lines[i] = "l" + strconv.Itoa(i)
	}
	e.SetValue(strings.Join(lines, "\n"))
	return e
}

// TestEditor_ScrollViewport_DragAtEdge verifies the editor's wheel-scroll
// viewport pan: 20 single-display-line logical lines, height 5 → maxOffset 15.
// The caret rides its logical line while it stays visible (phase 1) and is
// dragged to the viewport edge once a pan pushes it off-screen (phase 2), with
// yOffset clamped to the valid range.
func TestEditor_ScrollViewport_DragAtEdge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		startRow    int
		startYOff   int
		scroll      int
		wantYOffset int
		wantRow     int
	}{
		{name: "phase1 down keeps caret line", startRow: 10, startYOff: 8, scroll: 2, wantYOffset: 10, wantRow: 10},
		{name: "phase2 down drags caret to top edge", startRow: 10, startYOff: 10, scroll: 3, wantYOffset: 13, wantRow: 13},
		{name: "phase1 up keeps caret line", startRow: 10, startYOff: 8, scroll: -2, wantYOffset: 6, wantRow: 10},
		{name: "phase2 up drags caret to bottom edge", startRow: 10, startYOff: 8, scroll: -5, wantYOffset: 3, wantRow: 7},
		{name: "clamp at bottom drags caret down", startRow: 0, startYOff: 0, scroll: 100, wantYOffset: 15, wantRow: 15},
		{name: "clamp at top drags caret up", startRow: 19, startYOff: 15, scroll: -100, wantYOffset: 0, wantRow: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newScrollEditor(t, 20)
			e.row, e.col, e.yOffset = tt.startRow, 0, tt.startYOff

			e.ScrollViewport(tt.scroll)

			assert.Equal(t, tt.wantYOffset, e.yOffset, "yOffset")
			assert.Equal(t, tt.wantRow, e.row, "caret logical row")
		})
	}
}

// TestEditor_ScrollViewport_FitsInViewport verifies that when all content fits
// (maxOffset 0) a wheel notch is a no-op (yOffset stays 0, caret unmoved).
func TestEditor_ScrollViewport_FitsInViewport(t *testing.T) {
	t.Parallel()
	e := newScrollEditor(t, 3) // 3 lines, height 5 → everything visible
	e.row, e.col, e.yOffset = 1, 0, 0

	e.ScrollViewport(5)

	assert.Equal(t, 0, e.yOffset, "no scroll when content fits")
	assert.Equal(t, 1, e.row, "caret must not move when nothing scrolls")
}

// TestEditor_ScrollViewport_Empty verifies ScrollViewport is a no-op on an empty
// buffer (a single empty display line).
func TestEditor_ScrollViewport_Empty(t *testing.T) {
	t.Parallel()
	e := New()
	e.SetWidth(40)
	e.SetHeight(5)
	require.Empty(t, e.Value())

	e.ScrollViewport(5) // must not panic
	assert.Equal(t, 0, e.yOffset)
}
