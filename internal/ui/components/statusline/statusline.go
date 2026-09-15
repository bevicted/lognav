// Package statusline renders view-owned contextual status pills.
package statusline

import (
	"math"

	"github.com/bevicted/lognav/internal/deps"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const spacing = 1

type actionRange struct {
	start, end int
	action     func()
}

// StatusLine owns the shared contextual-row rendering and the action ranges
// painted in its previous frame.
type StatusLine struct {
	bundle   deps.Bundle
	drawRect component.Rect
	ranges   []actionRange
}

// New constructs a contextual status renderer.
func New(bundle deps.Bundle) *StatusLine { return &StatusLine{bundle: bundle} }

// SetRect stores the row allocated by the root.
func (m *StatusLine) SetRect(r component.Rect) { m.drawRect = r }

// Draw chooses the first variant that fits the row. If none fit, it draws the
// final variant clipped at the right edge. Hit ranges are retained from this
// exact draw instead of being recomputed during mouse handling.
func (m *StatusLine) Draw(s component.Screen, variants []component.StatusVariant) *component.Cursor {
	r := m.drawRect
	m.ranges = m.ranges[:0]
	if r.W < 1 || r.H < 1 {
		return nil
	}

	uicanvas.FillRowAt(s, r.X, r.Y, r.W, uv.Style{})
	if len(variants) == 0 {
		return nil
	}
	chosen := variants[len(variants)-1]
	for _, variant := range variants {
		if variantWidth(variant) <= r.W {
			chosen = variant
			break
		}
	}

	st := m.bundle.Config.Style
	chipStyle := uv.Style{Bg: st.PillChipBg.Color, Fg: st.PillFg.Color, Attrs: uv.AttrBold}
	lineStyle := uv.Style{Bg: st.PillLineBg.Color, Fg: st.PillFg.Color}
	maxX := r.X + r.W
	x := r.X
	for _, pill := range chosen {
		if x >= maxX {
			break
		}
		width := pillWidth(pill)
		valueStyle := mergeStyle(lineStyle, pill.ValueStyle)
		drawnEnd := uicanvas.DrawPill(s, x, r.Y, pill.Head, pill.Value, chipStyle, valueStyle, maxX)
		if pill.Action != nil && drawnEnd > x {
			m.ranges = append(m.ranges, actionRange{start: x, end: drawnEnd, action: pill.Action})
		}
		x += width + spacing
	}
	return nil
}

// OnMouseClick invokes only an action whose visible cells were painted in the
// previous Draw. Whitespace and clipped-off content are inert.
func (m *StatusLine) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	if btn != uv.MouseLeft || y != m.drawRect.Y {
		return false
	}
	for _, r := range m.ranges {
		if x >= r.start && x < r.end {
			r.action()
			return true
		}
	}
	return false
}

func mergeStyle(base, override uv.Style) uv.Style {
	if override.Fg != nil || override.Bg != nil {
		base.Fg = override.Fg
		base.Bg = override.Bg
	}
	base.Attrs |= override.Attrs
	return base
}

func variantWidth(variant component.StatusVariant) int {
	width := 0
	for i, pill := range variant {
		if i > 0 {
			width += spacing
		}
		width += pillWidth(pill)
	}
	return width
}

// measureScreen obtains geometry from DrawPill itself rather than duplicating
// its grapheme-cell and separator rules.
type measureScreen struct{}

func (measureScreen) Bounds() uv.Rectangle        { return uv.Rectangle{} }
func (measureScreen) CellAt(int, int) *uv.Cell    { return nil }
func (measureScreen) SetCell(int, int, *uv.Cell)  {}
func (measureScreen) WidthMethod() uv.WidthMethod { return ansi.WcWidth }

func pillWidth(pill component.StatusPill) int {
	return uicanvas.DrawPill(measureScreen{}, 0, 0, pill.Head, pill.Value, uv.Style{}, uv.Style{}, math.MaxInt)
}
