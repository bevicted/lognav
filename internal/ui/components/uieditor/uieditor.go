// Package uieditor is a pure-ultraviolet multi-line text editor that replaces
// bubbles/textarea at lognav's only multi-line site (the query editor). It owns
// its soft-wrap (wrap.go), stores text as per-line rune slices with a flat
// (row,col) cursor, and is driven by the parent's HandleKey/Draw — it imports no
// bubbletea, bubbles, or lipgloss. The parent (queryeditor) renders it via the
// DisplayLines()/CursorDisplayLine()/LineInfo() model and places the real
// terminal cursor itself.
//
// Scope is the query box's daily-driver editing keys only (arrows, word motion,
// home/end, backspace/delete with line merges, word delete, enter split,
// printable insertion); textarea's emacs aliases, kill-line, document
// begin/end, page keys, and case/transpose ops are intentionally not ported.
package uieditor

import (
	"strings"

	"github.com/rivo/uniseg"
)

// LineInfo locates the cursor within the wrapped display of its logical line.
// Only the two fields lognav's renderer consumes are kept (textarea exposes 7):
// RowOffset is the wrapped-line index within the logical line; CharOffset is the
// display-width columns before the cursor on that wrapped line.
type LineInfo struct {
	RowOffset  int
	CharOffset int
}

// DisplayLine is one soft-wrapped segment of a logical line, flattened in render
// order. ColWidth/StartCol locate the segment within its logical line (display
// columns / rune offset respectively) so the renderer can apply highlight tokens
// and the cursor mapping can translate display positions back to (row, col).
type DisplayLine struct {
	Runes      []rune
	LogicalIdx int
	ColWidth   int
	StartCol   int
}

// Editor is a multi-line, cell-native text buffer with a soft-wrapped display
// and a flat (row, col) cursor. The zero value is not usable; construct with New.
type Editor struct {
	value       [][]rune // one rune slice per logical line; never empty (>=1 line)
	row, col    int      // cursor: logical line index, rune offset within value[row]
	width       int      // wrap/render width in cells (>=1)
	height      int      // viewport height in display rows (>=1)
	yOffset     int      // vertical scroll position, in display lines
	prompt      string
	placeholder string
	focused     bool
}

// New returns an empty, blurred editor with minimal (1x1) sizing; the parent
// sizes it via SetWidth/SetHeight.
func New() *Editor {
	return &Editor{value: [][]rune{{}}, width: 1, height: 1}
}

// Value returns the full buffer, logical lines joined by "\n".
func (e *Editor) Value() string {
	lines := make([]string, len(e.value))
	for i, l := range e.value {
		lines[i] = string(l)
	}
	return strings.Join(lines, "\n")
}

// SetValue resets the buffer and inserts s through the one insertion path, so
// the cursor lands at the end of the text (matching textarea's reset-then-insert)
// and multi-line input wraps identically to paste/keystroke insertion.
func (e *Editor) SetValue(s string) {
	e.value = [][]rune{{}}
	e.row, e.col, e.yOffset = 0, 0, 0
	e.insertString(s)
}

// Line returns the cursor's logical line index (0-based).
func (e *Editor) Line() int { return e.row }

func (e *Editor) Focus()        { e.focused = true }
func (e *Editor) Blur()         { e.focused = false }
func (e *Editor) Focused() bool { return e.focused }

// SetWidth sets the wrap/render width in cells (clamped to >=1) and repositions
// the view, since a width change re-wraps and may move the cursor's display line.
func (e *Editor) SetWidth(w int) {
	if w < 1 {
		w = 1
	}
	e.width = w
	e.repositionView()
}

// Width returns the current wrap/render width in cells.
func (e *Editor) Width() int { return e.width }

// SetHeight sets the viewport height in display rows (clamped to >=1) and
// repositions the view so the cursor stays visible after a resize.
func (e *Editor) SetHeight(h int) {
	if h < 1 {
		h = 1
	}
	e.height = h
	e.repositionView()
}

// Height returns the viewport height in display rows.
func (e *Editor) Height() int { return e.height }

func (e *Editor) SetPrompt(s string)      { e.prompt = s }
func (e *Editor) Prompt() string          { return e.prompt }
func (e *Editor) SetPlaceholder(s string) { e.placeholder = s }
func (e *Editor) Placeholder() string     { return e.placeholder }

// ScrollYOffset returns the vertical scroll position, in display lines.
func (e *Editor) ScrollYOffset() int { return e.yOffset }

// promptWidth returns the prompt's display width in cells. The renderer draws
// the prompt before the text and offsets the cursor by this amount, so a click's
// x must subtract it to reach the editor's text-relative display column.
func (e *Editor) promptWidth() int { return uniseg.StringWidth(e.prompt) }

// SetCursorAtDisplay places the caret at the display position (x, y), where y is
// a display-row offset from the current vertical scroll and x is a column offset
// from the editor's drawn content origin (including the prompt). It reverse-maps
// the soft-wrapped display layout back to a logical (row, col), mirroring
// moveVertical via the shared placeAtDisplayLine walk: it selects the DisplayLine
// at the scrolled index, sets the row to that line's logical index, and walks the
// segment from its start column until the accumulated display width passes
// (x - promptWidth), clamping to the segment/line end. Does not call Focus; the
// caller is responsible for focusing the editor if a click should also take focus.
func (e *Editor) SetCursorAtDisplay(x, y int) {
	dls := e.DisplayLines()
	if len(dls) == 0 {
		return
	}
	idx := min(max(e.yOffset+y, 0), len(dls)-1)
	wantX := max(x-e.promptWidth(), 0)
	e.placeAtDisplayLine(dls[idx], wantX)
	e.repositionView()
}

// wrapRow soft-wraps a single logical line at the current width.
func (e *Editor) wrapRow(row int) [][]rune {
	return wrapLine(e.value[row], e.width)
}

// DisplayLines returns every logical line soft-wrapped into segments, flattened
// in render order. This is the single wrap authority the renderer consumes;
// because the cursor mapping (LineInfo/CursorDisplayLine) wraps through the same
// wrapLine, render and cursor cannot disagree.
func (e *Editor) DisplayLines() []DisplayLine {
	var out []DisplayLine
	for li := range e.value {
		grid := e.wrapRow(li)
		colWidth, startCol := 0, 0
		for _, seg := range grid {
			out = append(out, DisplayLine{
				Runes:      seg,
				LogicalIdx: li,
				ColWidth:   colWidth,
				StartCol:   startCol,
			})
			colWidth += uniseg.StringWidth(string(seg))
			startCol += len(seg)
		}
	}
	return out
}

// LineInfo locates the cursor within its logical line's wrapped display. Ported
// from bubbles/textarea's LineInfo() (the cursor-drift-critical mapping): wrap
// the cursor's logical line, walk the grid accumulating rune counts, and at the
// wrapped segment containing the cursor return its row/char offsets. The first
// branch is the wrap-break rule — when the cursor sits exactly at a segment
// boundary that has a following segment, it belongs to the next segment at
// offset 0.
func (e *Editor) LineInfo() LineInfo {
	grid := e.wrapRow(e.row)
	counter := 0
	for i, line := range grid {
		if counter+len(line) == e.col && i+1 < len(grid) {
			return LineInfo{RowOffset: i + 1, CharOffset: 0}
		}
		if counter+len(line) >= e.col {
			return LineInfo{
				RowOffset:  i,
				CharOffset: uniseg.StringWidth(string(line[:max(0, e.col-counter)])),
			}
		}
		counter += len(line)
	}
	return LineInfo{}
}

// CursorDisplayLine returns the cursor's absolute display-line index: the sum of
// every prior logical line's wrapped height plus the cursor's RowOffset.
func (e *Editor) CursorDisplayLine() int {
	n := 0
	for i := range e.row {
		n += len(e.wrapRow(i))
	}
	return n + e.LineInfo().RowOffset
}

// ScrollViewport pans the editor viewport by n display lines (n > 0 scrolls
// toward later lines, n < 0 toward earlier) WITHOUT moving the caret, then drags
// the caret to the nearest visible display line if the pan pushed it off-screen
// (vim-style drag-at-edge). The caret keeps its horizontal display column when
// dragged (LineInfo().CharOffset, the same column moveVertical preserves). It is
// the pure-pan counterpart to moveVertical: there the caret leads and yOffset
// follows (repositionView); here yOffset leads and the caret follows only at the
// viewport edge. No-op on an empty/short buffer that already fits.
func (e *Editor) ScrollViewport(n int) {
	if n == 0 {
		return
	}
	dls := e.DisplayLines()
	if len(dls) == 0 {
		return
	}
	maxOff := max(len(dls)-e.height, 0)
	e.yOffset = min(max(e.yOffset+n, 0), maxOff)

	cur := e.CursorDisplayLine()
	wantX := e.LineInfo().CharOffset
	switch {
	case cur < e.yOffset:
		e.placeAtDisplayLine(dls[e.yOffset], wantX)
	case cur > e.yOffset+e.height-1:
		bottom := min(e.yOffset+e.height-1, len(dls)-1)
		e.placeAtDisplayLine(dls[bottom], wantX)
	}
}

// clampScroll keeps yOffset within [0, max(0, displayLineCount-height)] so the
// scroll position is always representable after edits/resizes.
func (e *Editor) clampScroll() {
	maxOff := max(len(e.DisplayLines())-e.height, 0)
	e.yOffset = min(max(e.yOffset, 0), maxOff)
}
