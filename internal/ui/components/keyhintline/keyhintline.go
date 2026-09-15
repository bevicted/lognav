// Package keyhintline draws the active view's curated keybinding hints.
package keyhintline

import (
	"github.com/bevicted/lognav/internal/deps"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

const spacing = 1

type hintLayout struct {
	binding keys.Binding
	chip    string
	line    string
	joined  bool
	start   int
	end     int
}

// Model owns the hint row's drawing and click ranges. The root supplies the
// active context's bindings and routes clicks after resolving overlay precedence.
type Model struct {
	bundle   deps.Bundle
	drawRect component.Rect
	bindings []keys.Binding
}

// New constructs a key hint line using the configured pill colors.
func New(bundle deps.Bundle) *Model {
	return &Model{bundle: bundle}
}

// SetRect stores the row allocated by the root.
func (m *Model) SetRect(r component.Rect) { m.drawRect = r }

// SetBindings replaces the active context's bindings. The copy prevents a
// component's dynamic binding slice from changing a layout between draw and hit
// testing.
func (m *Model) SetBindings(bindings []keys.Binding) {
	m.bindings = append(m.bindings[:0], bindings...)
}

// layout computes both the rendered pill text and the clipped absolute click
// ranges. Draw and OnMouseClick use this one pass, so visible cells and hit
// targets cannot drift apart.
func (m *Model) layout() []hintLayout {
	r := m.drawRect
	if r.W < 1 || r.H < 1 {
		return nil
	}

	maxX := r.X + r.W
	x := r.X
	layouts := make([]hintLayout, 0, len(m.bindings))
	for _, binding := range keys.HintBindings(m.bindings) {
		if x >= maxX {
			break
		}
		chip, line := binding.HintPill()
		joined := line != "" && chip+line == binding.Label
		width := pillWidth(chip, line, joined)
		end := min(x+width, maxX)
		if end > x {
			layouts = append(layouts, hintLayout{
				binding: binding,
				chip:    chip,
				line:    line,
				joined:  joined,
				start:   x,
				end:     end,
			})
		}
		x += width + spacing
	}
	return layouts
}

// Draw fills the allocated row and paints visible hints in priority order.
func (m *Model) Draw(s component.Screen) *component.Cursor {
	r := m.drawRect
	if r.W < 1 || r.H < 1 {
		return nil
	}

	uicanvas.FillRowAt(s, r.X, r.Y, r.W, uv.Style{})
	st := m.bundle.Config.Style
	chipStyle := uv.Style{Bg: st.PillChipBg.Color, Fg: st.PillFg.Color, Attrs: uv.AttrBold}
	lineStyle := uv.Style{Bg: st.PillLineBg.Color, Fg: st.PillFg.Color}
	maxX := r.X + r.W
	for _, hint := range m.layout() {
		if hint.joined {
			col := uicanvas.DrawPill(s, hint.start, r.Y, hint.chip, "", chipStyle, lineStyle, maxX)
			uicanvas.PlaceText(s, col, r.Y, hint.line, lineStyle, maxX)
			continue
		}
		uicanvas.DrawPill(s, hint.start, r.Y, hint.chip, hint.line, chipStyle, lineStyle, maxX)
	}
	return nil
}

// OnMouseClick invokes the binding action under a left click. Whitespace and
// clipped-off portions of a pill are intentionally not clickable.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	if btn != uv.MouseLeft || y != m.drawRect.Y {
		return false
	}
	for _, hint := range m.layout() {
		if x >= hint.start && x < hint.end {
			if hint.binding.Action != nil {
				hint.binding.Action()
			}
			return true
		}
	}
	return false
}

// pillWidth mirrors the hint renderer's grapheme-cell layout. Embedded first
// letters join the label remainder directly; other pills retain DrawPill's
// separator cell between the key and label.
func pillWidth(chip, line string, joined bool) int {
	width := uniseg.StringWidth(chip)
	if line != "" {
		if !joined {
			width++
		}
		width += uniseg.StringWidth(line)
	}
	return width
}
