// Package testutil provides shared helpers for component tests.
package testutil

import (
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
)

// RenderAt assigns rect to c, draws onto a fresh canvas large enough to hold
// the full extent of the rect (rect.X+rect.W, rect.Y+rect.H), and returns the
// rendered string and cursor. The cursor is in absolute coordinates.
//
// Sizing the canvas to the rect's far corner (rather than just W,H) lets tests
// pass non-zero rect offsets to verify cursor translation while still capturing
// the component's rendered cells in the canvas output.
func RenderAt(c component.Component, rect component.Rect) (string, *component.Cursor) {
	c.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.X+rect.W, rect.Y+rect.H)
	cursor := c.Draw(canvas)
	return canvas.Render(), cursor
}
