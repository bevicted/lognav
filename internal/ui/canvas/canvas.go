// Package canvas provides terminal-canvas cell helpers used by TUI
// components. Functions place text and fill row/span regions on ultraviolet cells.
package canvas

import (
	"slices"
	"strings"

	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// PlaceText writes the graphemes of str onto the screen at row y starting at
// column x with the given style and returns the column reached after the last
// grapheme. Graphemes that would extend past maxX are dropped. Zero-width
// graphemes are skipped.
func PlaceText(s component.Screen, x, y int, str string, style uv.Style, maxX int) int {
	return placeText(s, x, y, str, style, uv.Link{}, maxX)
}

// PlaceLink is PlaceText with a hyperlink: every placed cell carries link as
// its OSC8 hyperlink, so the text is both styled and click-to-open in
// supporting terminals. A zero uv.Link is equivalent to PlaceText.
func PlaceLink(s component.Screen, x, y int, str string, style uv.Style, link uv.Link, maxX int) int {
	return placeText(s, x, y, str, style, link, maxX)
}

func placeText(s component.Screen, x, y int, str string, style uv.Style, link uv.Link, maxX int) int {
	col := x
	gr := uniseg.NewGraphemes(str)
	for gr.Next() {
		w := gr.Width()
		if w == 0 {
			continue
		}
		if col+w > maxX {
			break
		}
		s.SetCell(col, y, &uv.Cell{Content: gr.Str(), Width: w, Style: style, Link: link})
		col += w
	}
	return col
}

// FillRow writes w space cells with the given style starting at column 0 of
// row y. Used to apply a base background to a canvas row before placing
// content cells; without it, canvas.Render strips trailing default cells and
// the row's background does not extend to the full width.
func FillRow(s component.Screen, y, w int, style uv.Style) {
	FillRowAt(s, 0, y, w, style)
}

// FillRowAt writes w space cells with the given style starting at column x of
// row y.
func FillRowAt(s component.Screen, x, y, w int, style uv.Style) {
	for i := range w {
		s.SetCell(x+i, y, &uv.Cell{Content: " ", Width: 1, Style: style})
	}
}

// FillSpan writes n space cells with the given style starting at (x, y).
// Returns the column reached after the last cell. Cells beyond maxX are
// dropped.
func FillSpan(s component.Screen, x, y, n int, style uv.Style, maxX int) int {
	col := x
	for range n {
		if col >= maxX {
			break
		}
		s.SetCell(col, y, &uv.Cell{Content: " ", Width: 1, Style: style})
		col++
	}
	return col
}

// BorderSet holds the glyphs for a cell-drawn box. An empty field means that
// edge/corner is not drawn. Edge glyphs span the full side when no adjacent
// corner is set, so single-edge sets (e.g. {Bottom:"─"}) draw a full-width rule.
type BorderSet struct {
	Top, Bottom, Left, Right                   string
	TopLeft, TopRight, BottomLeft, BottomRight string
}

// RoundedSet matches lipgloss.RoundedBorder() (dialog box). Edge glyphs are the
// same single-line runes as NormalBorder; only the corners differ, so the
// single-edge cases (filehandler left "│", list/helpoverlay bottom "─") just set
// the relevant edge with no corners.
var RoundedSet = BorderSet{
	Top: "─", Bottom: "─", Left: "│", Right: "│",
	TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯",
}

// putCell writes glyph g at (x, y) if g is non-empty.
func putCell(s component.Screen, x, y int, g string, style uv.Style) {
	if g != "" {
		s.SetCell(x, y, &uv.Cell{Content: g, Width: 1, Style: style})
	}
}

// drawHEdges fills the top and bottom horizontal edges of a box, inset by any
// corners already placed.
func drawHEdges(s component.Screen, b BorderSet, left, right, top, bottom int, style uv.Style) {
	hStart, hEnd := left, right
	if b.TopLeft != "" || b.BottomLeft != "" {
		hStart++
	}
	if b.TopRight != "" || b.BottomRight != "" {
		hEnd--
	}
	for x := hStart; x <= hEnd; x++ {
		putCell(s, x, top, b.Top, style)
		putCell(s, x, bottom, b.Bottom, style)
	}
}

// drawVEdges fills the left and right vertical edges of a box, inset by any
// corners already placed.
func drawVEdges(s component.Screen, b BorderSet, left, right, top, bottom int, style uv.Style) {
	vStart, vEnd := top, bottom
	if b.TopLeft != "" || b.TopRight != "" {
		vStart++
	}
	if b.BottomLeft != "" || b.BottomRight != "" {
		vEnd--
	}
	for y := vStart; y <= vEnd; y++ {
		putCell(s, left, y, b.Left, style)
		putCell(s, right, y, b.Right, style)
	}
}

// DrawBox writes b's glyphs around r's perimeter, one cell each, clipped to r.
// Corners are placed where their glyph is non-empty; each edge fills the span
// between the corners it has (full side when a corner is absent). No-op for
// W<1 or H<1.
func DrawBox(s component.Screen, r component.Rect, b BorderSet, style uv.Style) {
	if r.W < 1 || r.H < 1 {
		return
	}
	left, right := r.X, r.X+r.W-1
	top, bottom := r.Y, r.Y+r.H-1

	putCell(s, left, top, b.TopLeft, style)
	putCell(s, right, top, b.TopRight, style)
	putCell(s, left, bottom, b.BottomLeft, style)
	putCell(s, right, bottom, b.BottomRight, style)

	drawHEdges(s, b, left, right, top, bottom, style)
	drawVEdges(s, b, left, right, top, bottom, style)
}

// Pad returns r inset by the given margins (clamped to non-negative size).
func Pad(r component.Rect, top, right, bottom, left int) component.Rect {
	w := r.W - left - right
	h := r.H - top - bottom
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return component.Rect{X: r.X + left, Y: r.Y + top, W: w, H: h}
}

// CenterX returns the X offset that centers contentW within outerW, never negative.
func CenterX(outerW, contentW int) int {
	if contentW >= outerW {
		return 0
	}
	return (outerW - contentW) / 2
}

// CenterY returns the Y offset that centers contentH within outerH, never negative.
func CenterY(outerH, contentH int) int {
	if contentH >= outerH {
		return 0
	}
	return (outerH - contentH) / 2
}

// PlaceVCentered draws text (split on "\n") vertically centered within r, each
// line left-aligned at r.X and clipped to r (vertical center, horizontal left).
func PlaceVCentered(s component.Screen, r component.Rect, text string, style uv.Style) {
	if r.W < 1 || r.H < 1 || text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	startY := r.Y + CenterY(r.H, len(lines))
	for i, line := range lines {
		y := startY + i
		if y < r.Y || y >= r.Y+r.H {
			continue
		}
		PlaceText(s, r.X, y, line, style, r.X+r.W)
	}
}

// TruncateFront returns s clipped to display width maxW by dropping LEADING
// grapheme clusters and prefixing "...", so the END of s stays visible
// (front-ellipsis, e.g. "ni_cncf_io" -> "...f_io"). It is the counterpart to
// PlaceText's tail-clip and backs the bottom-bar overflow rule.
//
// Width accounting is by display cell (uniseg), so wide graphemes never split.
// When maxW <= 0 it returns "". When 0 < maxW < 3 there is no room for both the
// ellipsis and a tail, so it returns that many dots (".", ".."). At maxW == 3 it
// returns "..." with no tail. For maxW >= 3 the tail budget is maxW-3 cells: the
// trailing graphemes whose cumulative width fits maxW-3 are kept (a wide cluster
// that would overflow the budget is dropped, not split).
func TruncateFront(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if uniseg.StringWidth(s) <= maxW {
		return s
	}
	if maxW < 3 {
		return strings.Repeat(".", maxW)
	}
	tailBudget := maxW - 3
	// Collect graphemes, then walk from the end accumulating width until the
	// next cluster would exceed tailBudget. The kept clusters are a contiguous
	// SUFFIX (the true tail); a wide cluster that would overflow the budget is
	// dropped whole (not split), and so is everything before it.
	var clusters []string
	gr := uniseg.NewGraphemes(s)
	for gr.Next() {
		clusters = append(clusters, gr.Str())
	}
	width := 0
	start := len(clusters)
	for i, cl := range slices.Backward(clusters) {
		w := uniseg.StringWidth(cl)
		if width+w > tailBudget {
			break
		}
		width += w
		start = i
	}
	return "..." + strings.Join(clusters[start:], "")
}

// DrawPill renders a powerline-style pill at (x, y): a CHIP (a bright label band)
// followed, when line != "", by a single separator space and the LINE (a fainter
// value band). The chip cells carry chipStyle; the separator and line cells carry
// lineStyle. Both bands are solid (every cell painted, so the background shows as
// a continuous band even where a glyph is absent). Content is clipped at maxX.
// DrawPill returns the column reached after the last drawn cell (so callers can
// place adjacent pills). The chip is drawn pre-styled by the caller (no padding);
// the filter menu pads its chips to a fixed width before calling.
func DrawPill(s component.Screen, x, y int, chip, line string, chipStyle, lineStyle uv.Style, maxX int) int {
	col := drawBand(s, x, y, chip, chipStyle, maxX)
	if line == "" {
		return col
	}
	if col >= maxX {
		return col
	}
	// separator space in lineStyle.
	s.SetCell(col, y, &uv.Cell{Content: " ", Width: 1, Style: lineStyle})
	col++
	return drawBand(s, col, y, line, lineStyle, maxX)
}

// drawBand paints text as a solid band: every grapheme cell carries style, and
// no separate fill is needed because the glyph cells themselves hold the bg.
// It returns the column after the last placed grapheme, clipped at maxX.
func drawBand(s component.Screen, x, y int, text string, style uv.Style, maxX int) int {
	return PlaceText(s, x, y, text, style, maxX)
}
