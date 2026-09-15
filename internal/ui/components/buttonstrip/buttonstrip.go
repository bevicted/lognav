// Package buttonstrip lays out, draws and hit-tests the single-row button strip
// used by the dialog and the filter-menu overlays: each label padded with one
// space on either side, the padded chunks placed adjacent with no separator, and
// the whole strip centred within an explicit horizontal span.
//
// The span's right edge is always given by the caller (Strip.MaxX): a strip
// wider than its span starts flush at the left bound and clips on the right
// instead of overrunning the caller's border.
package buttonstrip

import (
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

// Strip is a button strip bound to the horizontal span [X, MaxX). It is a
// throwaway value: callers build one per event/draw from their current inner
// rect rather than caching geometry that a resize would stale.
type Strip struct {
	// Labels are the button labels, left to right.
	Labels []string
	// X is the inclusive left bound of the span the strip is centred in.
	X int
	// MaxX is the exclusive right bound: no cell is painted at or past it.
	MaxX int
}

// Width returns the total cell width of a strip built from labels: for each
// label one pad column, the label, one pad column.
func Width(labels []string) int {
	total := 0
	for _, label := range labels {
		total += 1 + uniseg.StringWidth(label) + 1
	}
	return total
}

// Width returns the strip's total cell width, ignoring the MaxX clip. Callers
// fold it into their content width so the box is wide enough for the strip.
func (s Strip) Width() int { return Width(s.Labels) }

// StartX returns the absolute column the first button starts at: the strip
// centred within [X, MaxX), never left of X.
func (s Strip) StartX() int {
	return s.X + uicanvas.CenterX(s.MaxX-s.X, s.Width())
}

// HitTest returns the index of the button whose padded chunk contains absolute
// column x, or -1 when x falls outside every chunk. It covers the strip's full
// logical extent, including chunks Draw clipped at MaxX.
func (s Strip) HitTest(x int) int {
	col := s.StartX()
	for i, label := range s.Labels {
		chunk := 1 + uniseg.StringWidth(label) + 1
		if x >= col && x < col+chunk {
			return i
		}
		col += chunk
	}
	return -1
}

// Draw paints the strip on row y, clipped at MaxX. The button at index selected
// is drawn bold+reverse across its whole padded chunk (pads included); any index
// outside the strip (e.g. -1) highlights nothing.
func (s Strip) Draw(scr component.Screen, y, selected int) {
	col := s.StartX()
	for i, label := range s.Labels {
		style := uv.Style{}
		if i == selected {
			style.Attrs |= uv.AttrBold | uv.AttrReverse
		}
		col = uicanvas.PlaceText(scr, col, y, " "+label+" ", style, s.MaxX)
	}
}

// Hover moves *selected onto the button under absolute column x and reports
// whether the selection changed. A cell outside every button — or the
// already-selected button — leaves *selected untouched and returns false, so
// stray pointer drift never wrecks the keyboard selection and callers can gate a
// redraw on the return. Callers must first check that the pointer is on the
// strip's row.
func (s Strip) Hover(x int, selected *int) bool {
	idx := s.HitTest(x)
	if idx < 0 || idx == *selected {
		return false
	}
	*selected = idx
	return true
}
