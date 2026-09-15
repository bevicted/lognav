package logviewer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rescanTokenAt is the pre-refactor covering-token lookup: a full scan of the
// token list from index 0, taking the first token whose [Start, End) span
// contains col. Kept in the test file only, as the oracle tokenCursor must
// agree with for every column.
func rescanTokenAt(tokens []Token, col int) *Token {
	for i := range tokens {
		if col >= tokens[i].Start && col < tokens[i].End {
			return &tokens[i]
		}
	}
	return nil
}

// TestTokenCursor_AgreesWithRescan_EveryColumn is the pixel-identity proof for
// the Step 2 change: over a set of token layouts that exercise contiguous
// spans, gaps, a leading gap, a zero-width token and an empty list, the
// monotonic cursor must return exactly what the old full rescan returned for
// every column — including columns past the end of the last token.
func TestTokenCursor_AgreesWithRescan_EveryColumn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tokens []Token
	}{
		{name: "empty", tokens: nil},
		{name: "contiguous", tokens: []Token{
			{Type: TokenSyntax, Start: 0, End: 1},
			{Type: TokenKey, Start: 1, End: 6},
			{Type: TokenSyntax, Start: 6, End: 7},
			{Type: TokenString, Start: 7, End: 14},
		}},
		{name: "gaps between tokens", tokens: []Token{
			{Type: TokenKey, Start: 2, End: 5},
			{Type: TokenNumber, Start: 9, End: 12},
			{Type: TokenBool, Start: 20, End: 24},
		}},
		{name: "leading gap only", tokens: []Token{
			{Type: TokenString, Start: 7, End: 9},
		}},
		{name: "zero-width token", tokens: []Token{
			{Type: TokenSyntax, Start: 3, End: 3},
			{Type: TokenKey, Start: 3, End: 6},
		}},
		{name: "zero-width token at line start", tokens: []Token{
			{Type: TokenSyntax, Start: 0, End: 0},
			{Type: TokenNull, Start: 0, End: 4},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A single cursor walks columns ascending, exactly as renderLine does.
			tc := tokenCursor{tokens: tt.tokens}
			for col := range 32 {
				want := rescanTokenAt(tt.tokens, col)
				got := tc.at(col)
				assert.Equal(t, want, got, "col %d", col)
			}
		})
	}
}

// TestTokenCursor_SkippedColumnsCatchUp covers the horizontal-scroll path:
// renderLine skips graphemes left of xOffset without querying the cursor, so
// the first at() call can jump far to the right. The cursor must catch up
// lazily and still return the rescan answer.
func TestTokenCursor_SkippedColumnsCatchUp(t *testing.T) {
	t.Parallel()

	tokens := []Token{
		{Type: TokenKey, Start: 0, End: 5},
		{Type: TokenSyntax, Start: 5, End: 6},
		{Type: TokenString, Start: 6, End: 20},
		{Type: TokenNumber, Start: 25, End: 30},
	}
	for _, start := range []int{0, 6, 7, 19, 20, 25, 29, 30, 100} {
		tc := tokenCursor{tokens: tokens}
		assert.Equal(t, rescanTokenAt(tokens, start), tc.at(start), "first query at col %d", start)
		// and the walk stays correct once it resumes stepping.
		for col := start; col < start+8; col++ {
			assert.Equal(t, rescanTokenAt(tokens, col), tc.at(col), "col %d after jump to %d", col, start)
		}
	}
}

// renderHotpathLine is a JSON-ish line wide enough to hold several distinct
// tokens and two occurrences of a search needle.
const renderHotpathLine = `{"msg":"needle in a needle stack","code":42}`

// screenRows renders every cell of s into a comparable string form, including
// the full style, so two buffers can be asserted pixel-identical.
func screenRows(t *testing.T, s component.Screen, w, h int) []string {
	t.Helper()
	rows := make([]string, 0, h)
	var row strings.Builder
	for y := range h {
		row.Reset()
		for x := range w {
			c := s.CellAt(x, y)
			if c == nil {
				row.WriteString("<nil>|")
				continue
			}
			fmt.Fprintf(&row, "%q/%v/%v/%d|", c.Content, c.Style.Fg, c.Style.Bg, c.Style.Attrs)
		}
		rows = append(rows, row.String())
	}
	return rows
}

// renderHotpathOpts builds the renderLine options for one line of a synthetic
// log, with the caller supplying the match slice under test.
func renderHotpathOpts(matches []SearchMatch, lineIdx, w, sideBar int) renderLineOpts {
	return renderLineOpts{
		line:          renderHotpathLine,
		searchMatches: matches,
		logIdx:        0,
		lineIdx:       lineIdx,
		width:         w,
		sideBarWidth:  sideBar,
		tokens:        tokenize([]byte(renderHotpathLine))[0],
	}
}

// TestRenderLine_WholeLogMatches_PixelIdenticalToPreFiltered is the pixel-identity
// proof for the Step 4 change. Passing the whole log's match slice plus lineIdx
// must produce a byte-identical screen to passing the old pre-filtered
// (per-line) slice — across xOffset values and on/off the cursor row.
func TestRenderLine_WholeLogMatches_PixelIdenticalToPreFiltered(t *testing.T) {
	t.Parallel()

	const (
		w       = 60
		sideBar = 1
		total   = w + sideBar
		lineIdx = 2
	)

	// Matches spread over three lines of the same log; only line 2's belong to
	// the rendered row.
	whole := []SearchMatch{
		{line: 0, start: 3, end: 9},
		{line: 1, start: 0, end: 6},
		{line: lineIdx, start: 8, end: 14},
		{line: lineIdx, start: 21, end: 27},
		{line: 4, start: 2, end: 8},
	}
	var preFiltered []SearchMatch
	for _, sm := range whole {
		if sm.line == lineIdx {
			preFiltered = append(preFiltered, sm)
		}
	}
	require.Len(t, preFiltered, 2)

	for _, xOffset := range []int{0, 5, 30} {
		for _, underCursor := range []bool{false, true} {
			name := fmt.Sprintf("xOffset=%d/cursor=%v", xOffset, underCursor)
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				render := func(matches []SearchMatch) component.Screen {
					m := New(depstest.NewTest(t))
					m.xOffset = xOffset
					s := uv.NewScreenBuffer(total, 1)
					opts := renderHotpathOpts(matches, lineIdx, w, sideBar)
					opts.isUnderCursor = underCursor
					m.renderLine(s, 0, opts)
					return s
				}

				got := screenRows(t, render(whole), total, 1)
				want := screenRows(t, render(preFiltered), total, 1)
				assert.Equal(t, want, got)
			})
		}
	}
}

// TestRenderLine_MatchesOnlyOnOtherLines_PaintsNothing pins the "has any
// matches" fast-path trap: searchMatches is now the whole log's slice, so a
// non-empty slice does NOT mean this line matches. A line whose log matches
// only elsewhere must render exactly as a log with no matches at all.
func TestRenderLine_MatchesOnlyOnOtherLines_PaintsNothing(t *testing.T) {
	t.Parallel()

	const (
		w       = 60
		sideBar = 1
		total   = w + sideBar
		lineIdx = 3
	)

	otherLines := []SearchMatch{
		{line: 0, start: 8, end: 14},
		{line: 1, start: 21, end: 27},
		{line: 7, start: 0, end: 6},
	}

	render := func(matches []SearchMatch) component.Screen {
		m := New(depstest.NewTest(t))
		s := uv.NewScreenBuffer(total, 1)
		m.renderLine(s, 0, renderHotpathOpts(matches, lineIdx, w, sideBar))
		return s
	}

	got := screenRows(t, render(otherLines), total, 1)
	want := screenRows(t, render(nil), total, 1)
	assert.Equal(t, want, got, "matches on other lines must not tint this line")
}

// TestRenderLine_MatchesOnThisLine_StillPaint is the positive control for the
// test above: the same whole-log slice with an entry on lineIdx must differ
// from the no-match render, i.e. the highlight is not silently dropped.
func TestRenderLine_MatchesOnThisLine_StillPaint(t *testing.T) {
	t.Parallel()

	const (
		w       = 60
		sideBar = 1
		total   = w + sideBar
		lineIdx = 3
	)

	render := func(matches []SearchMatch) component.Screen {
		m := New(depstest.NewTest(t))
		s := uv.NewScreenBuffer(total, 1)
		m.renderLine(s, 0, renderHotpathOpts(matches, lineIdx, w, sideBar))
		return s
	}

	// Column 8..14 is inside the first "needle" occurrence.
	withMatch := []SearchMatch{
		{line: 0, start: 0, end: 6},
		{line: lineIdx, start: 8, end: 14},
	}
	got := screenRows(t, render(withMatch), total, 1)
	want := screenRows(t, render(nil), total, 1)
	assert.NotEqual(t, want, got, "a match on this line must still be painted")
}
