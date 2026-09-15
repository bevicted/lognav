// Package componenttest exposes shared test helpers for components in
// ui/components/. The current API is intentionally tiny — Draw rect matrix
// only. Expansion requires explicit spec amendment in a future cluster.
package componenttest

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
)

// CanonicalRects is the standard rect set used across all component Draw
// tests. Oversized = 2000x100 (200K cells — enough to exercise overflow
// branches without transient memory spikes).
var CanonicalRects = []struct {
	Name  string
	Input component.Rect
}{
	{Name: "zero", Input: component.Rect{}},
	{Name: "tiny", Input: component.Rect{W: 1, H: 1}},
	{Name: "normal", Input: component.Rect{W: 80, H: 24}},
	{Name: "oversized", Input: component.Rect{W: 2000, H: 100}},
}

// DrawMatrix runs Draw against each CanonicalRects entry, asserting Draw
// does not panic at any rect size. Caller supplies setRect (typically
// m.SetRect) and draw (typically m.Draw). Subtests are sequential because
// they share the caller's model, which is not concurrency-safe. The matrix
// is cheap (4 rects); sequential evaluation is fast enough.
func DrawMatrix(
	t *testing.T,
	setRect func(component.Rect),
	draw func(component.Screen) *component.Cursor,
) {
	t.Helper()
	for _, tc := range CanonicalRects {
		t.Run(tc.Name, func(t *testing.T) {
			// No t.Parallel() — DrawMatrix subtests share the caller's model,
			// which is not concurrency-safe. The matrix is cheap (4 rects);
			// sequential evaluation is fast enough.
			setRect(tc.Input)
			w, h := tc.Input.W, tc.Input.H
			if w < 1 {
				w = 1
			}
			if h < 1 {
				h = 1
			}
			canvas := uv.NewScreenBuffer(w, h)
			require.NotPanics(t, func() { _ = draw(canvas) })
		})
	}
}
