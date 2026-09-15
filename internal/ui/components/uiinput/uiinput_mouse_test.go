package uiinput

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInput_RuneIndexAtViewportX covers the inverse of the Draw layout: given an
// x relative to the input's drawn left origin (rect.X), RuneIndexAtViewportX
// returns the rune index in the value the click lands on. It mirrors Draw's
// per-rune display-width advance, the prompt width, and the horizontal scroll
// (viewOffset). The contract is RELATIVE x: callers subtract the draw rect's X
// before calling (Input does not store its rect).
func TestInput_RuneIndexAtViewportX(t *testing.T) {
	t.Parallel()

	t.Run("short value, viewOffset 0", func(t *testing.T) {
		t.Parallel()
		i := New()
		i.SetPrompt("> ") // PromptWidth 2
		i.SetWidth(20)
		i.SetValue("hello world")
		require.Equal(t, 2, i.PromptWidth())

		s := uv.NewScreenBuffer(30, 1)
		i.Draw(s, component.Rect{X: 0, Y: 0, W: 30, H: 1})
		require.Equal(t, 0, i.viewOffset, "precondition: short value fits, viewOffset 0")

		// Click at the start of the value text → rune index 0.
		assert.Equal(t, 0, i.RuneIndexAtViewportX(i.PromptWidth()))
		// Click 3 cells into the value (all single-width runes) → rune index 3.
		assert.Equal(t, 3, i.RuneIndexAtViewportX(i.PromptWidth()+3))
		// Click inside the prompt clamps to the first visible rune (0).
		assert.Equal(t, 0, i.RuneIndexAtViewportX(0))
		assert.Equal(t, 0, i.RuneIndexAtViewportX(i.PromptWidth()-1))
		// Click far past the end clamps to len(runes).
		assert.Equal(t, len([]rune("hello world")), i.RuneIndexAtViewportX(100))
	})

	t.Run("nonzero rect origin uses relative x", func(t *testing.T) {
		t.Parallel()
		i := New()
		i.SetPrompt("> ")
		i.SetWidth(20)
		i.SetValue("hello")

		s := uv.NewScreenBuffer(40, 1)
		x0 := 7
		i.Draw(s, component.Rect{X: x0, Y: 0, W: 30, H: 1})
		require.Equal(t, 0, i.viewOffset)

		// Caller subtracts rect.X: an absolute click at x0+PromptWidth()+2 becomes
		// a relative x of PromptWidth()+2 → rune index 2.
		absClick := x0 + i.PromptWidth() + 2
		rel := absClick - x0
		assert.Equal(t, 2, i.RuneIndexAtViewportX(rel))
	})

	t.Run("scrolled value accounts for viewOffset", func(t *testing.T) {
		t.Parallel()
		i := New()
		i.SetWidth(3) // tiny viewport forces horizontal scroll
		i.SetValue("abcdefgh")
		i.SetCursor(len("abcdefgh")) // cursor at end → scrolled right
		require.Positive(t, i.viewOffset, "precondition: must be scrolled past the start")

		s := uv.NewScreenBuffer(10, 1)
		i.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})
		off := i.viewOffset

		// No prompt: relative x 0 is the first visible rune = viewOffset.
		assert.Equal(t, off, i.RuneIndexAtViewportX(0))
		// One cell in → next rune (single-width runes).
		assert.Equal(t, off+1, i.RuneIndexAtViewportX(1))
	})

	t.Run("masked value maps cells like unmasked", func(t *testing.T) {
		t.Parallel()
		i := New()
		i.SetWidth(20)
		i.SetMasked(true)
		i.SetValue("secret")
		i.SetCursor(0) // keep viewOffset 0

		s := uv.NewScreenBuffer(20, 1)
		i.Draw(s, component.Rect{X: 0, Y: 0, W: 20, H: 1})
		require.Equal(t, 0, i.viewOffset)

		// Each "•" is width-1, so cell offsets map 1:1 to rune indices, identical
		// to the unmasked single-width case. This locks the mask-path symmetry.
		for cell := range len("secret") {
			assert.Equalf(t, cell, i.RuneIndexAtViewportX(cell),
				"masked cell %d should map to rune %d", cell, cell)
		}
		assert.Equal(t, len([]rune("secret")), i.RuneIndexAtViewportX(100))
	})

	t.Run("wide rune second cell resolves to that rune", func(t *testing.T) {
		t.Parallel()
		i := New()
		i.SetWidth(20)
		i.SetValue("a世b") // 世 is a 2-cell-wide rune at rune index 1
		i.SetCursor(0)    // keep viewOffset 0

		s := uv.NewScreenBuffer(20, 1)
		i.Draw(s, component.Rect{X: 0, Y: 0, W: 20, H: 1})
		require.Equal(t, 0, i.viewOffset)

		// Cell layout (no prompt): col0='a'(rune0), col1+col2='世'(rune1), col3='b'(rune2).
		assert.Equal(t, 0, i.RuneIndexAtViewportX(0)) // 'a'
		assert.Equal(t, 1, i.RuneIndexAtViewportX(1)) // lead cell of '世'
		assert.Equal(t, 1, i.RuneIndexAtViewportX(2)) // trailing cell of '世' → still rune 1
		assert.Equal(t, 2, i.RuneIndexAtViewportX(3)) // 'b'
	})
}
