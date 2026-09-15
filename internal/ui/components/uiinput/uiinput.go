// Package uiinput is a single-line, cell-native text input that replaces
// bubbles/textinput at lognav's single-line sites (bar, list fuzzy filter,
// dialog input). It is pure ultraviolet — no bubbletea, bubbles, or lipgloss —
// and renders its own cursor as a reverse-video cell (lognav owns the cursor;
// there is no terminal cursor blink). Keys arrive via HandleKey (R2 dispatch);
// pasted text arrives via InsertText.
package uiinput

import (
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// Input is a single-line editable value with a cursor, optional prompt,
// placeholder, masking, max length, and horizontal scroll.
type Input struct {
	value       []rune
	cursor      int // rune index, 0..len(value)
	viewOffset  int // first visible rune index (horizontal scroll)
	prompt      string
	placeholder string
	width       int // inner viewport width in cells (prompt excluded)
	masked      bool
	maxLen      int // 0 = unlimited
	focused     bool
	cellStyle   uv.Style // base style (bg band) painted on every drawn cell
}

// New returns an empty, blurred input.
func New() *Input { return &Input{} }

func (i *Input) Value() string { return string(i.value) }

// SetValue replaces the value (truncated to maxLen) and moves the cursor to the end.
func (i *Input) SetValue(s string) {
	i.value = []rune(s)
	if i.maxLen > 0 && len(i.value) > i.maxLen {
		i.value = i.value[:i.maxLen]
	}
	i.cursor = len(i.value)
	i.clampView()
}

func (i *Input) Position() int { return i.cursor }

func (i *Input) SetCursor(n int) {
	i.cursor = min(max(n, 0), len(i.value))
	i.clampView()
}

// RuneIndexAtViewportX maps a click column x — measured RELATIVE to the input's
// drawn left origin (the rect.X passed to Draw) — to the rune index in the value
// the click lands on, so a caller can SetCursor(idx) to place the caret there.
// It is the inverse of the Draw layout: it accounts for the prompt width and the
// horizontal scroll (viewOffset, a rune index = first visible value rune), and
// walks the value rune-by-rune by per-rune display width (uniseg.StringWidth),
// exactly as Draw advances. A click on the trailing cell of a wide rune resolves
// to that rune's index, not the next. The result is clamped to [0, len(runes)].
//
// Contract: x is RELATIVE to the input's drawn origin, NOT absolute. Callers that
// know the draw rect (bar, list) must pass clickX-rect.X — Input does not store
// its rect, so it cannot subtract an absolute origin itself.
//
// Note: unlike Draw, this does not clip at the right viewport edge. Draw stops
// painting once a rune would exceed maxX, but this keeps walking to len(value),
// so a click to the right of the last visible rune returns the rune at that cell
// offset (or len(value)) rather than the last drawn one. This is harmless in
// practice: a caller that SetCursor(idx)s the result triggers Draw to re-scroll
// that rune into view.
func (i *Input) RuneIndexAtViewportX(x int) int {
	// Cell offset into the visible value text (after the prompt). A click in the
	// prompt or before it clamps to the first visible rune.
	cellOff := x - i.PromptWidth()
	if cellOff <= 0 {
		return min(i.viewOffset, len(i.value))
	}
	// Walk runes from the first visible rune, mirroring Draw's per-rune advance,
	// until the accumulated cell width passes the clicked column. Landing inside a
	// wide rune's span returns that rune (the comparison uses col+w, so the
	// trailing cell still maps to the rune whose lead cell precedes it).
	col := 0
	for k := i.viewOffset; k < len(i.value); k++ {
		ch := string(i.value[k])
		if i.masked {
			ch = "•"
		}
		w := uniseg.StringWidth(ch)
		if w == 0 {
			continue
		}
		if cellOff < col+w {
			return k
		}
		col += w
	}
	return len(i.value)
}

func (i *Input) Focus()        { i.focused = true }
func (i *Input) Blur()         { i.focused = false }
func (i *Input) Focused() bool { return i.focused }

// SetWidth sets the inner viewport width (the caller subtracts the prompt width).
func (i *Input) SetWidth(w int) {
	if w < 1 {
		w = 1
	}
	i.width = w
	i.clampView()
}

func (i *Input) SetPrompt(s string)      { i.prompt = s }
func (i *Input) Prompt() string          { return i.prompt }
func (i *Input) PromptWidth() int        { return uniseg.StringWidth(i.prompt) }
func (i *Input) SetPlaceholder(s string) { i.placeholder = s }
func (i *Input) SetMasked(b bool)        { i.masked = b }
func (i *Input) SetMaxLen(n int)         { i.maxLen = n }

// SetCellStyle sets the base style painted behind the prompt, value, placeholder,
// and cursor cells. It lets a pill render a colored line band under the editable
// text: Draw uses this as each cell's Style (the cursor cell ORs AttrReverse on
// top, preserving the band Bg). The zero value (uv.Style{}) is the prior
// behavior — transparent cells.
func (i *Input) SetCellStyle(st uv.Style) { i.cellStyle = st }

// InsertText inserts s at the cursor, honoring maxLen. Callers route the paste
// event content here.
func (i *Input) InsertText(s string) {
	for _, r := range s {
		i.insertRune(r)
	}
}

// HandleKey applies one editing key and reports whether it was consumed.
// Ctrl/Alt-modified keys are NOT consumed (the parent keeps its bindings).
func (i *Input) HandleKey(ev uv.KeyPressEvent) bool {
	if ev.Mod&(uv.ModCtrl|uv.ModAlt) != 0 {
		return false
	}
	switch ev.Code {
	case uv.KeyLeft:
		if i.cursor > 0 {
			i.cursor--
			i.clampView()
		}
		return true
	case uv.KeyRight:
		if i.cursor < len(i.value) {
			i.cursor++
			i.clampView()
		}
		return true
	case uv.KeyHome:
		i.cursor = 0
		i.clampView()
		return true
	case uv.KeyEnd:
		i.cursor = len(i.value)
		i.clampView()
		return true
	case uv.KeyBackspace:
		if i.cursor > 0 {
			i.value = append(i.value[:i.cursor-1], i.value[i.cursor:]...)
			i.cursor--
			i.clampView()
		}
		return true
	case uv.KeyDelete:
		if i.cursor < len(i.value) {
			i.value = append(i.value[:i.cursor], i.value[i.cursor+1:]...)
			i.clampView()
		}
		return true
	}
	if ev.Text != "" {
		i.InsertText(ev.Text)
		return true
	}
	return false
}

// Draw renders prompt + value (or placeholder when empty) + the reverse-video
// cursor cell (only when focused) onto s within r. It returns nothing: the
// visible cursor is the reverse-video cell, matching the prior virtual-cursor
// behavior.
//
//nolint:gocyclo // single path per rendering state (empty/placeholder/value/masked/scroll); splitting would obscure the drawing flow.
func (i *Input) Draw(s component.Screen, r component.Rect) {
	if r.W < 1 || r.H < 1 {
		return
	}
	maxX := r.X + r.W
	x := uicanvas.PlaceText(s, r.X, r.Y, i.prompt, i.cellStyle, maxX)
	innerStart := x

	if len(i.value) == 0 {
		if i.placeholder != "" {
			ph := i.cellStyle
			ph.Attrs |= uv.AttrFaint
			uicanvas.PlaceText(s, innerStart, r.Y, i.placeholder, ph, maxX)
		}
		if i.focused {
			reverseCell(s, innerStart, r.Y, i.cellStyle)
		}
		return
	}

	col := innerStart
	cursorCol := -1
	for k := i.viewOffset; k < len(i.value); k++ {
		ch := string(i.value[k])
		if i.masked {
			ch = "•"
		}
		w := uniseg.StringWidth(ch)
		if w == 0 {
			continue
		}
		if col+w > maxX {
			break
		}
		if k == i.cursor {
			cursorCol = col
		}
		s.SetCell(col, r.Y, &uv.Cell{Content: ch, Width: w, Style: i.cellStyle})
		col += w
	}
	if i.cursor >= len(i.value) && col < maxX {
		cursorCol = col
	}
	if i.focused && cursorCol >= 0 {
		reverseCell(s, cursorCol, r.Y, i.cellStyle)
	}
}

func (i *Input) insertRune(r rune) {
	if i.maxLen > 0 && len(i.value) >= i.maxLen {
		return
	}
	i.value = append(i.value, 0)
	copy(i.value[i.cursor+1:], i.value[i.cursor:])
	i.value[i.cursor] = r
	i.cursor++
	i.clampView()
}

// clampView keeps the cursor visible: scroll left when the cursor is before the
// window, scroll right (advancing viewOffset) until the run from viewOffset to
// the cursor — plus the cursor cell — fits the inner width, then reclaim slack
// on the left when the viewport grew (or the value shrank) so the window shows
// as much leading content as fits. The final left-scroll pass matters when the
// value is set before the width (the caller's SetValue/SetWidth order): without
// it, viewOffset can stay pinned at the end after the viewport widens.
func (i *Input) clampView() {
	inner := max(i.width, 1)
	if i.cursor < i.viewOffset {
		i.viewOffset = i.cursor
		return
	}
	for i.viewOffset < i.cursor && i.cellWidth(i.viewOffset, i.cursor)+1 > inner {
		i.viewOffset++
	}
	for i.viewOffset > 0 && i.cellWidth(i.viewOffset-1, i.cursor)+1 <= inner {
		i.viewOffset--
	}
}

func (i *Input) cellWidth(from, to int) int {
	w := 0
	for k := from; k < to && k < len(i.value); k++ {
		ch := string(i.value[k])
		if i.masked {
			ch = "•"
		}
		w += uniseg.StringWidth(ch)
	}
	return w
}

// reverseCell applies AttrReverse to the cell at (x, y), drawing the cursor.
// It reverses only the lead cell of a wide glyph (matching the terminal
// block-cursor convention); the trailing width-0 cell is left untouched. When
// the cell has no content (cursor sits past the value end, or on an empty
// input) it is filled with a space carrying base so the band Bg shows under
// the cursor block. The band Bg survives under AttrReverse either way: a
// pre-drawn value cell already carries cellStyle from Draw's SetCell, so base
// is written explicitly only for the empty/zero-space case.
func reverseCell(s component.Screen, x, y int, base uv.Style) {
	c := s.CellAt(x, y)
	if c == nil {
		return
	}
	if c.Content == "" || c.Content == " " && c.Style == (uv.Style{}) {
		c.Content = " "
		c.Width = 1
		c.Style = base
	}
	c.Style.Attrs |= uv.AttrReverse
}
