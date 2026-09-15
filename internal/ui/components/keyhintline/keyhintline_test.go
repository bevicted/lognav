package keyhintline

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrawAndClick(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	var calls int
	m := New(bundle)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 3, H: 1})
	m.SetBindings([]keys.Binding{{
		Keys:     []string{"?"},
		Label:    "help",
		Priority: 1,
		Action: func() {
			calls++
		},
	}})

	s := uv.NewScreenBuffer(3, 1)
	m.Draw(s)

	chip := s.CellAt(0, 0)
	line := s.CellAt(1, 0)
	require.NotNil(t, chip)
	require.NotNil(t, line)
	assert.Equal(t, "?", chip.Content)
	assert.Equal(t, " ", line.Content)
	assert.Equal(t, bundle.Config.Style.PillChipBg.Color, chip.Style.Bg)
	assert.Equal(t, bundle.Config.Style.PillLineBg.Color, line.Style.Bg)
	for x := range 3 {
		assert.NotEqual(t, ":", s.CellAt(x, 0).Content)
	}

	assert.True(t, m.OnMouseClick(2, 0, uv.MouseLeft), "visible clipped pill cell must hit")
	assert.Equal(t, 1, calls)
	assert.False(t, m.OnMouseClick(3, 0, uv.MouseLeft), "clipped-off cell must not hit")
	assert.False(t, m.OnMouseClick(2, 0, uv.MouseRight), "only left click activates")
}

func TestDrawEmbeddedPrefixWithoutSeparator(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	var calls int
	m := New(bundle)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 5, H: 1})
	m.SetBindings([]keys.Binding{{
		Keys:     []string{"w"},
		Label:    "watch",
		Priority: 1,
		Action:   func() { calls++ },
	}})

	s := uv.NewScreenBuffer(5, 1)
	m.Draw(s)

	for x, want := range []string{"w", "a", "t", "c", "h"} {
		require.NotNil(t, s.CellAt(x, 0))
		assert.Equal(t, want, s.CellAt(x, 0).Content)
	}
	assert.Equal(t, bundle.Config.Style.PillChipBg.Color, s.CellAt(0, 0).Style.Bg)
	assert.Equal(t, bundle.Config.Style.PillLineBg.Color, s.CellAt(1, 0).Style.Bg)
	assert.True(t, m.OnMouseClick(4, 0, uv.MouseLeft))
	assert.Equal(t, 1, calls)
}

func TestLayoutOrderingAndWhitespace(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	m.SetRect(component.Rect{X: 0, Y: 4, W: 40, H: 1})
	m.SetBindings([]keys.Binding{
		{Keys: []string{"z"}, Label: "zulu", Priority: 2},
		{Keys: []string{"a"}, Label: "alpha", Priority: 2},
		{Keys: []string{"?"}, Label: "help", Priority: 1},
	})

	layouts := m.layout()
	require.Len(t, layouts, 3)
	assert.Equal(t, []string{"help", "alpha", "zulu"}, []string{
		layouts[0].binding.Label,
		layouts[1].binding.Label,
		layouts[2].binding.Label,
	})
	assert.False(t, m.OnMouseClick(layouts[0].end, 4, uv.MouseLeft), "gap is whitespace")
	assert.False(t, m.OnMouseClick(0, 5, uv.MouseLeft), "other rows are ignored")
}
