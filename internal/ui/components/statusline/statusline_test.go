package statusline

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/status"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusLine_SelectsFirstFittingVariantAndCachesActions(t *testing.T) {
	t.Parallel()
	line := New(depstest.NewTest(t))
	line.SetRect(component.Rect{W: 12, H: 1})
	clicked := 0
	variants := []component.StatusVariant{
		{{Head: "Instance", Value: "fetching"}},
		{{Head: "I", Value: "run", Action: func() { clicked++ }}},
	}
	canvas := uv.NewScreenBuffer(12, 1)
	line.Draw(canvas, variants)

	require.NotNil(t, canvas.CellAt(0, 0))
	assert.Equal(t, "I", canvas.CellAt(0, 0).Content)
	assert.True(t, line.OnMouseClick(0, 0, uv.MouseLeft))
	assert.Equal(t, 1, clicked)
	assert.False(t, line.OnMouseClick(11, 0, uv.MouseLeft), "whitespace is inert")
}

func TestStatusLine_WidePillClippingAndPreviousFrameRanges(t *testing.T) {
	t.Parallel()
	line := New(depstest.NewTest(t))
	line.SetRect(component.Rect{W: 4, H: 1})
	clicked := 0
	canvas := uv.NewScreenBuffer(4, 1)
	line.Draw(canvas, []component.StatusVariant{{{Head: "界", Value: "timer", Action: func() { clicked++ }}}})

	assert.True(t, line.OnMouseClick(3, 0, uv.MouseLeft), "last drawn clipped cell remains actionable")
	assert.Equal(t, 1, clicked)
	line.SetRect(component.Rect{W: 1, H: 1})
	assert.True(t, line.OnMouseClick(3, 0, uv.MouseLeft), "mouse uses the last draw, not a live width")
	assert.Equal(t, 2, clicked)
}

func TestStatusLine_ClippedFinalWideGraphemeHasNoActionRange(t *testing.T) {
	t.Parallel()
	line := New(depstest.NewTest(t))
	line.SetRect(component.Rect{W: 3, H: 1})
	clicked := 0
	canvas := uv.NewScreenBuffer(3, 1)
	line.Draw(canvas, []component.StatusVariant{{
		{Head: "A"},
		{Head: "界", Action: func() { clicked++ }},
	}})

	assert.False(t, line.OnMouseClick(2, 0, uv.MouseLeft), "a clipped wide grapheme draws no actionable cell")
	assert.Zero(t, clicked)
}

func TestStatusLine_ComposesValueStyle(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	line := New(bundle)
	line.SetRect(component.Rect{W: 20, H: 1})
	style := status.Error.LabelStyle(bundle)
	style.Attrs = uv.AttrReverse | uv.AttrItalic
	require.Nil(t, style.Bg, "setup: instancepicker phase uses the default background")
	canvas := uv.NewScreenBuffer(20, 1)
	line.Draw(canvas, []component.StatusVariant{{{Head: "State", Value: "error", ValueStyle: style}}})

	cell := canvas.CellAt(6, 0)
	require.NotNil(t, cell)
	assert.Equal(t, style.Fg, cell.Style.Fg)
	assert.Equal(t, style.Bg, cell.Style.Bg)
	assert.NotZero(t, cell.Style.Attrs&uv.AttrReverse)
	assert.NotZero(t, cell.Style.Attrs&uv.AttrItalic)
}
