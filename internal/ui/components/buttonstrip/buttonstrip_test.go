package buttonstrip_test

import (
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/buttonstrip"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// labels is a three-button strip: " OK "(4) + " Cancel "(8) + " Nope "(6) = 18.
var labels = []string{"OK", "Cancel", "Nope"}

// rowText reads back row y of scr over [x, x+w) as a plain string.
func rowText(scr component.Screen, x, y, w int) string {
	var b strings.Builder
	for i := range w {
		if c := scr.CellAt(x+i, y); c != nil {
			b.WriteString(c.Content)
		}
	}
	return b.String()
}

func TestWidth(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, buttonstrip.Width(nil), "an empty strip is zero-width")
	assert.Equal(t, 18, buttonstrip.Width(labels), "each label gets one pad column per side")
	assert.Equal(t, 18, buttonstrip.Strip{Labels: labels}.Width(), "the method matches the function")
}

func TestStartX_CentersWithinTheSpan(t *testing.T) {
	t.Parallel()
	// Span [10, 40) is 30 wide, strip is 18: (30-18)/2 = 6 columns of slack.
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 40}
	assert.Equal(t, 16, s.StartX())
}

func TestStartX_FlushLeftWhenWiderThanTheSpan(t *testing.T) {
	t.Parallel()
	// A strip wider than its span never starts left of X — it clips on the right.
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 20}
	assert.Equal(t, 10, s.StartX())
}

func TestHitTest(t *testing.T) {
	t.Parallel()
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 40}
	start := s.StartX() // 16
	tests := []struct {
		name string
		x    int
		want int
	}{
		{"left of the strip", start - 1, -1},
		{"OK leading pad", start, 0},
		{"OK label", start + 1, 0},
		{"OK trailing pad", start + 3, 0},
		{"Cancel first column", start + 4, 1},
		{"Cancel last column", start + 11, 1},
		{"Nope first column", start + 12, 2},
		{"Nope last column", start + 17, 2},
		{"right of the strip", start + 18, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, s.HitTest(tc.x))
		})
	}
	assert.Equal(t, -1, buttonstrip.Strip{X: 10, MaxX: 40}.HitTest(20), "an empty strip hits nothing")
}

func TestDraw_HighlightsOnlyTheSelectedChunk(t *testing.T) {
	t.Parallel()
	scr := uv.NewScreenBuffer(40, 2)
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 40}
	s.Draw(scr, 1, 1)

	require.Equal(t, " OK  Cancel  Nope ", rowText(scr, s.StartX(), 1, 18))
	// The whole " Cancel " chunk (pads included) is bold+reverse; its neighbours
	// are not.
	for i := range 8 {
		c := scr.CellAt(s.StartX()+4+i, 1)
		require.NotNil(t, c)
		assert.NotZero(t, c.Style.Attrs&uv.AttrReverse, "column %d of the selected chunk is reverse", i)
		assert.NotZero(t, c.Style.Attrs&uv.AttrBold, "column %d of the selected chunk is bold", i)
	}
	assert.Zero(t, scr.CellAt(s.StartX()+3, 1).Style.Attrs, "the chunk before the selection is unstyled")
	assert.Zero(t, scr.CellAt(s.StartX()+12, 1).Style.Attrs, "the chunk after the selection is unstyled")
}

func TestDraw_NoSelection(t *testing.T) {
	t.Parallel()
	scr := uv.NewScreenBuffer(40, 2)
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 40}
	s.Draw(scr, 1, -1)
	for i := range 18 {
		assert.Zero(t, scr.CellAt(s.StartX()+i, 1).Style.Attrs, "column %d is unstyled with no selection", i)
	}
}

// TestDraw_ClipsAtMaxX is the narrow-terminal case: the strip is wider than its
// span, so it starts flush at X and no cell is painted at or past MaxX — the
// caller's border (and anything beyond it) stays intact.
func TestDraw_ClipsAtMaxX(t *testing.T) {
	t.Parallel()
	scr := uv.NewScreenBuffer(40, 2)
	// Pre-paint a sentinel row so any overrun is visible.
	for x := range 40 {
		scr.SetCell(x, 1, &uv.Cell{Content: "#", Width: 1})
	}
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 20} // span 10 < strip 18
	s.Draw(scr, 1, 0)

	require.Equal(t, 10, s.StartX())
	assert.Equal(t, " OK  Cance", rowText(scr, 10, 1, 10), "the strip is truncated at the right bound")
	assert.Equal(t, "####", rowText(scr, 20, 1, 4), "nothing is painted at or past MaxX")
	assert.Equal(t, "##########", rowText(scr, 0, 1, 10), "nothing is painted left of X")
}

// TestHitTest_CoversClippedChunks pins the deliberate asymmetry: hit-testing
// spans the strip's full logical extent even where Draw clipped it.
func TestHitTest_CoversClippedChunks(t *testing.T) {
	t.Parallel()
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 20}
	assert.Equal(t, 2, s.HitTest(25), "a clipped chunk still hit-tests")
}

func TestHover(t *testing.T) {
	t.Parallel()
	s := buttonstrip.Strip{Labels: labels, X: 10, MaxX: 40}
	start := s.StartX()

	sel := 0
	assert.True(t, s.Hover(start+12, &sel), "hovering another button reports a change")
	assert.Equal(t, 2, sel, "hover moves the selection onto the hovered button")

	assert.False(t, s.Hover(start+12, &sel), "re-hovering the selected button reports no change")
	assert.Equal(t, 2, sel, "re-hovering leaves the selection untouched")

	assert.False(t, s.Hover(start-1, &sel), "hovering off the strip reports no change")
	assert.Equal(t, 2, sel, "hovering off the strip leaves the selection untouched")
}
