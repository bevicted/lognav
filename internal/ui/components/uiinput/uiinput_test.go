package uiinput

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func press(code rune, text string) uv.KeyPressEvent {
	return uv.KeyPressEvent{Code: code, Text: text}
}

func cellAt(s component.Screen, x, y int) *uv.Cell { return s.CellAt(x, y) }

func TestInput_InsertAndValue(t *testing.T) {
	t.Parallel()
	i := New()
	assert.True(t, i.HandleKey(press('a', "a")))
	assert.True(t, i.HandleKey(press('b', "b")))
	assert.Equal(t, "ab", i.Value())
	assert.Equal(t, 2, i.Position())
}

func TestInput_EditingKeys(t *testing.T) {
	t.Parallel()
	i := New()
	i.SetValue("abc")
	assert.True(t, i.HandleKey(press(uv.KeyLeft, "")))
	assert.True(t, i.HandleKey(press(uv.KeyBackspace, "")))
	assert.Equal(t, "ac", i.Value())
	assert.True(t, i.HandleKey(press(uv.KeyHome, "")))
	assert.True(t, i.HandleKey(press(uv.KeyDelete, "")))
	assert.Equal(t, "c", i.Value())
	assert.True(t, i.HandleKey(press(uv.KeyEnd, "")))
	assert.Equal(t, 1, i.Position())
}

func TestInput_CtrlAltNotConsumed(t *testing.T) {
	t.Parallel()
	i := New()
	ev := uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}
	assert.False(t, i.HandleKey(ev))
	assert.Empty(t, i.Value())
}

func TestInput_MaxLen(t *testing.T) {
	t.Parallel()
	i := New()
	i.SetMaxLen(3)
	for _, r := range "abcdef" {
		i.HandleKey(press(r, string(r)))
	}
	assert.Equal(t, "abc", i.Value())
	i.InsertText("xyz")
	assert.Equal(t, "abc", i.Value())
}

func TestInput_InsertTextPaste(t *testing.T) {
	t.Parallel()
	i := New()
	i.SetValue("ad")
	i.SetCursor(1)
	i.InsertText("bc")
	assert.Equal(t, "abcd", i.Value())
	assert.Equal(t, 3, i.Position())
}

func TestInput_DrawValueAndCursorCell(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(10, 1)
	i := New()
	i.SetPrompt("> ")
	i.SetWidth(6)
	i.SetValue("hi")
	i.Focus()
	i.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})
	assert.Equal(t, ">", cellAt(s, 0, 0).Content)
	assert.Equal(t, "h", cellAt(s, 2, 0).Content)
	assert.Equal(t, "i", cellAt(s, 3, 0).Content)
	assert.NotZero(t, cellAt(s, 4, 0).Style.Attrs&uv.AttrReverse)
}

func TestInput_DrawPlaceholderWhenEmpty(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(20, 1)
	i := New()
	i.SetWidth(18)
	i.SetPlaceholder("fuzzyfind")
	i.Draw(s, component.Rect{X: 0, Y: 0, W: 20, H: 1})
	assert.Equal(t, "f", cellAt(s, 0, 0).Content)
	assert.NotZero(t, cellAt(s, 0, 0).Style.Attrs&uv.AttrFaint)
}

func TestInput_DrawMaskedHidesValue(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(10, 1)
	i := New()
	i.SetWidth(10)
	i.SetMasked(true)
	i.SetValue("pin")
	i.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})
	assert.Equal(t, "•", cellAt(s, 0, 0).Content)
	assert.Equal(t, "•", cellAt(s, 1, 0).Content)
}

func TestInput_HorizontalScrollKeepsCursorVisible(t *testing.T) {
	t.Parallel()
	i := New()
	i.SetWidth(3)
	i.SetValue("abcdef")
	assert.GreaterOrEqual(t, i.viewOffset, 3)
	i.SetCursor(0)
	assert.Equal(t, 0, i.viewOffset)
}

// TestInput_WidenReclaimsLeftSlack pins viewOffset near the end by setting the
// value while the viewport is tiny (reproducing the dialog's SetValue-before-
// SetWidth order), then widens the viewport and asserts the left-scroll pass in
// clampView reclaims the slack so the whole value is visible from column 0.
// Without that pass, viewOffset stayed pinned at the end and the widened input
// rendered nothing.
func TestInput_WidenReclaimsLeftSlack(t *testing.T) {
	t.Parallel()
	i := New()
	i.SetWidth(1)
	i.SetValue("abcdef") // value set while width is tiny → scrolled to the end
	require.Positive(t, i.viewOffset, "precondition: viewOffset must be pinned past the start")

	i.SetWidth(10) // viewport widens; clampView must reclaim left slack
	assert.Equal(t, 0, i.viewOffset, "widening should reveal the value from the start")

	// End-to-end: the first glyph renders at column 0, nothing is clipped.
	s := uv.NewScreenBuffer(10, 1)
	i.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})
	assert.Equal(t, "a", cellAt(s, 0, 0).Content, "first glyph must render at col 0 after widen")
}

// hasReverseOnRow reports whether any cell on row y carries AttrReverse.
func hasReverseOnRow(s component.Screen, y, maxX int) bool {
	for x := range maxX {
		if c := s.CellAt(x, y); c != nil && c.Style.Attrs&uv.AttrReverse != 0 {
			return true
		}
	}
	return false
}

func TestInput_WideRuneScrollKeepsCursorVisible(t *testing.T) {
	t.Parallel()
	i := New()
	i.SetWidth(4)
	value := "a世b界c你" // mix of width-1 and width-2 runes
	i.SetValue(value)
	i.Focus()
	for pos := 0; pos <= len([]rune(value)); pos++ {
		s := uv.NewScreenBuffer(10, 1)
		i.SetCursor(pos)
		i.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})
		assert.Truef(t, hasReverseOnRow(s, 0, 10),
			"cursor not visible (no reverse cell on row) at position %d", pos)
	}
}

func TestInput_MaskedCursorShowsReversedDot(t *testing.T) {
	t.Parallel()
	s := uv.NewScreenBuffer(10, 1)
	i := New()
	i.SetWidth(10)
	i.SetMasked(true)
	i.SetValue("pin")
	i.SetCursor(1)
	i.Focus()
	i.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})
	c := cellAt(s, 1, 0)
	assert.Equal(t, "•", c.Content)
	assert.NotZero(t, c.Style.Attrs&uv.AttrReverse)
}

func TestSetCellStyle_BandBehindValuePromptCursor(t *testing.T) {
	t.Parallel()
	in := New()
	in.SetPrompt("S ")
	in.SetValue("hi")
	in.SetWidth(20)
	in.Focus()
	in.SetCursor(1) // caret on the 'i'
	in.SetCellStyle(uv.Style{Bg: ansi.Blue})

	s := uv.NewScreenBuffer(20, 1)
	in.Draw(s, component.Rect{X: 0, Y: 0, W: 20, H: 1})

	// Prompt cell, value cells, and the cursor cell all carry the band Bg.
	for _, x := range []int{0, 1, 2, 3} { // "S "=cols 0-1, "hi"=cols 2-3
		c := s.CellAt(x, 0)
		require.NotNil(t, c, "cell %d", x)
		assert.Equal(t, ansi.Blue, c.Style.Bg, "cell %d must carry the band Bg", x)
	}
	// The cursor cell (the 'i', col 3) still has reverse video ON TOP of the band.
	cur := s.CellAt(3, 0)
	require.NotNil(t, cur)
	assert.NotZero(t, cur.Style.Attrs&uv.AttrReverse, "cursor cell keeps reverse video")
	assert.Equal(t, ansi.Blue, cur.Style.Bg, "cursor cell keeps the band Bg under reverse")
}

func TestSetCellStyle_EmptyFocusedCursorOnBand(t *testing.T) {
	t.Parallel()
	in := New()
	in.SetWidth(10)
	in.Focus()
	in.SetCellStyle(uv.Style{Bg: ansi.Blue})

	s := uv.NewScreenBuffer(10, 1)
	in.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})

	// Empty + focused -> the lone cursor cell carries band Bg + reverse.
	c := s.CellAt(0, 0)
	require.NotNil(t, c)
	assert.Equal(t, ansi.Blue, c.Style.Bg, "empty-input cursor cell carries the band Bg")
	assert.NotZero(t, c.Style.Attrs&uv.AttrReverse, "empty-input cursor cell is reverse")
}

func TestSetCellStyle_PlaceholderOnBand(t *testing.T) {
	t.Parallel()
	in := New()
	in.SetPlaceholder("type…")
	in.SetWidth(10)
	in.SetCellStyle(uv.Style{Bg: ansi.Blue})

	s := uv.NewScreenBuffer(10, 1)
	in.Draw(s, component.Rect{X: 0, Y: 0, W: 10, H: 1})

	c := s.CellAt(0, 0) // first placeholder glyph
	require.NotNil(t, c)
	assert.Equal(t, ansi.Blue, c.Style.Bg, "placeholder cell carries the band Bg")
	assert.NotZero(t, c.Style.Attrs&uv.AttrFaint, "placeholder stays faint")
}
