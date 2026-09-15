package canvas

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// cell returns the content string at (x, y), or "" for nil/empty/space-only
// cells (uv.EmptyCell has Content:" " so unset positions read as " ").
func cell(s component.Screen, x, y int) string {
	c := s.CellAt(x, y)
	if c == nil || c.Content == " " {
		return ""
	}
	return c.Content
}

func TestDrawBox_RoundedPerimeter(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(4, 3)
	DrawBox(s, component.Rect{X: 0, Y: 0, W: 4, H: 3}, RoundedSet, uv.Style{})
	assert.Equal(t, "╭", cell(s, 0, 0))
	assert.Equal(t, "╮", cell(s, 3, 0))
	assert.Equal(t, "╰", cell(s, 0, 2))
	assert.Equal(t, "╯", cell(s, 3, 2))
	assert.Equal(t, "─", cell(s, 1, 0))
	assert.Equal(t, "│", cell(s, 0, 1))
}

func TestDrawBox_SingleBottomEdgeFullWidth(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(3, 2)
	DrawBox(s, component.Rect{X: 0, Y: 0, W: 3, H: 2}, BorderSet{Bottom: "─"}, uv.Style{})
	assert.Equal(t, "─", cell(s, 0, 1))
	assert.Equal(t, "─", cell(s, 2, 1))
	assert.Empty(t, cell(s, 0, 0))
}

func TestDrawBox_SingleLeftEdgeFullHeight(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(3, 3)
	DrawBox(s, component.Rect{X: 0, Y: 0, W: 3, H: 3}, BorderSet{Left: "│"}, uv.Style{})
	assert.Equal(t, "│", cell(s, 0, 0))
	assert.Equal(t, "│", cell(s, 0, 1))
	assert.Equal(t, "│", cell(s, 0, 2))
}

func TestDrawBox_OffsetRect(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(6, 5)
	DrawBox(s, component.Rect{X: 2, Y: 1, W: 3, H: 3}, RoundedSet, uv.Style{})
	assert.Equal(t, "╭", cell(s, 2, 1))
	assert.Equal(t, "╯", cell(s, 4, 3))
}

func TestDrawBox_TinyRectNoPanic(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(1, 1)
	assert.NotPanics(t, func() {
		DrawBox(s, component.Rect{X: 0, Y: 0, W: 0, H: 0}, RoundedSet, uv.Style{})
		DrawBox(s, component.Rect{X: 0, Y: 0, W: -3, H: 1}, RoundedSet, uv.Style{})
	})
}

func TestPad(t *testing.T) {
	t.Parallel()
	got := Pad(component.Rect{X: 2, Y: 3, W: 10, H: 6}, 1, 2, 1, 2)
	assert.Equal(t, component.Rect{X: 4, Y: 4, W: 6, H: 4}, got)
	clamped := Pad(component.Rect{X: 0, Y: 0, W: 2, H: 2}, 2, 2, 2, 2)
	assert.Equal(t, component.Rect{X: 2, Y: 2, W: 0, H: 0}, clamped)
}

func TestCenterX(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 3, CenterX(10, 4))
	assert.Equal(t, 0, CenterX(4, 10))
}

func TestCenterY(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, CenterY(5, 3))
	assert.Equal(t, 0, CenterY(3, 10))
}

func TestPlaceVCentered_LeftAlignedBlock(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(10, 5)
	PlaceVCentered(s, component.Rect{X: 0, Y: 0, W: 10, H: 5}, "ab\ncd", uv.Style{})
	assert.Equal(t, "a", cell(s, 0, 1))
	assert.Equal(t, "c", cell(s, 0, 2))
}

func TestDrawPill_ChipThenLine(t *testing.T) {
	t.Parallel()
	chipStyle := uv.Style{Bg: ansi.BrightBlue}
	lineStyle := uv.Style{Bg: ansi.Blue}
	s := uv.NewScreenBuffer(20, 1)

	// chip "J", line ".data", starting at x=0, maxX=20.
	end := DrawPill(s, 0, 0, "J", ".data", chipStyle, lineStyle, 20)

	// chip "J" carries chipStyle.Bg.
	c0 := s.CellAt(0, 0)
	assert.Equal(t, "J", c0.Content)
	assert.Equal(t, ansi.BrightBlue, c0.Style.Bg, "chip cell uses chipStyle")
	// a single space separates chip from line, carrying lineStyle.
	// line cells carry lineStyle.Bg.
	got := contentRow(s, 0, 20)
	assert.Contains(t, got, "J")
	assert.Contains(t, got, ".data")
	// end is the column after the last line cell (chip 1 + sep 1 + line 5 = 7).
	assert.Equal(t, 7, end, "DrawPill returns the column after the line")
	// verify a line cell's Bg.
	lc := s.CellAt(3, 0) // somewhere inside ".data"
	assert.Equal(t, ansi.Blue, lc.Style.Bg, "line cell uses lineStyle")
}

func TestDrawPill_EmptyLineChipOnly(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(20, 1)
	end := DrawPill(s, 2, 0, "Filter", "", uv.Style{Bg: ansi.BrightBlue}, uv.Style{Bg: ansi.Blue}, 20)
	got := contentRow(s, 0, 20)
	assert.Contains(t, got, "Filter")
	// No line -> no trailing separator/line; end is just after the chip word.
	assert.Equal(t, 2+6, end, "chip-only pill ends after the chip word")
}

func TestDrawPill_ClipsAtMaxX(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(8, 1)
	// maxX=6 -> chip "J"(1) + sep(1) + only 4 of "abcdef" fit.
	end := DrawPill(s, 0, 0, "J", "abcdef", uv.Style{}, uv.Style{}, 6)
	assert.LessOrEqual(t, end, 6, "DrawPill never writes past maxX")
}

// contentRow collects visible glyphs on row y from x=0..w as a string.
func contentRow(s component.Screen, y, w int) string {
	var b []rune
	for x := range w {
		if c := s.CellAt(x, y); c != nil && c.Content != "" {
			b = append(b, []rune(c.Content)...)
		}
	}
	return string(b)
}

func TestTruncateFront(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		s    string
		maxW int
		want string
	}{
		{"fits exactly", "abcdef", 6, "abcdef"},
		{"fits with room", "abc", 10, "abc"},
		{"empty string", "", 5, ""},
		{"maxW 0 drops everything", "abcdef", 0, ""},
		{"maxW 1 cannot show ellipsis+tail", "abcdef", 1, "."},
		{"maxW 2 partial ellipsis no tail", "abcdef", 2, ".."},
		{"maxW 3 full ellipsis no tail", "abcdef", 3, "..."},
		{"maxW 4 ellipsis plus one tail cell", "abcdef", 4, "...f"},
		{"maxW 5 ellipsis plus two tail cells", "abcdefgh", 5, "...gh"},
		{"keeps the tail not the head", "ni_cncf_io", 7, "...f_io"},
		{"negative maxW treated as zero", "abc", -3, ""},
		{"wide grapheme fits without truncation", "ab世", 5, "ab世"},
		{"wide grapheme kept in tail when it fits the budget", "abcd世", 5, "...世"},
		{"wide grapheme dropped whole when budget too small", "abc世", 4, "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateFront(tt.s, tt.maxW)
			assert.Equal(t, tt.want, got, "TruncateFront(%q, %d)", tt.s, tt.maxW)
		})
	}
}
