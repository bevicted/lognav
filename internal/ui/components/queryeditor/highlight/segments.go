package highlight

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/ui/components/list"
)

// CellStyle returns the uv.Style for display column col given a line's tokens.
// It mirrors the query editor's per-cell precedence (see render.go): a
// TokenError applies a curly underline in the error color; the first non-error
// token covering the column sets the foreground, and later non-error tokens do
// not override an already-set foreground. Token offsets are display-width
// columns, so col must be the display column, not a byte offset. Sharing this
// with the editor keeps previews colored identically to the live query.
func CellStyle(cfg *config.Config, tokens []Token, col int) uv.Style {
	var style uv.Style
	for _, tok := range tokens {
		if col < tok.Start || col >= tok.End {
			continue
		}
		if tok.Type == TokenError {
			style.Underline = uv.UnderlineCurly
			style.UnderlineColor = cfg.Style.DpErrorFg.Color
		} else if style.Fg == nil {
			style = TokenCellStyle(cfg, tok.Type)
		}
	}
	return style
}

// Segments highlights source and returns it as cell-native styled lines for
// list/preview rendering (e.g. the query-file and snapshot previews). The result
// has one entry per line of source (split on "\n", so always at least one line);
// within a line, runs of graphemes sharing a style are coalesced into a single
// Segment. A line with no highlight tokens — or when highlighting is unavailable
// (Highlight returned nil after a panic) — yields a single plain segment,
// matching list.PlainItem. Zero-width graphemes are dropped, mirroring the
// editor's render path so token column boundaries line up.
func Segments(cfg *config.Config, source string) [][]list.Segment {
	lines := strings.Split(source, "\n")
	tokensByLine := Highlight(source)
	out := make([][]list.Segment, len(lines))
	for i, line := range lines {
		var toks []Token
		if i < len(tokensByLine) {
			toks = tokensByLine[i]
		}
		out[i] = lineSegments(cfg, line, toks)
	}
	return out
}

// lineSegments converts a single line of text plus its tokens into coalesced
// styled segments. It walks the line grapheme-by-grapheme, tracking the display
// column to look up each cell's style, and merges consecutive graphemes sharing
// a style into one Segment. Returns a single plain segment when the line has no
// tokens or no styled runs.
func lineSegments(cfg *config.Config, line string, tokens []Token) []list.Segment {
	if len(tokens) == 0 {
		return list.PlainItem(line)
	}

	var segs []list.Segment
	var b strings.Builder
	var cur uv.Style
	started := false
	col := 0

	flush := func() {
		if b.Len() == 0 {
			return
		}
		segs = append(segs, list.Segment{Text: b.String(), Style: cur})
		b.Reset()
	}

	gr := uniseg.NewGraphemes(line)
	for gr.Next() {
		gw := gr.Width()
		if gw == 0 {
			continue
		}
		st := CellStyle(cfg, tokens, col)
		if !started || !cur.Equal(&st) {
			flush()
			cur = st
			started = true
		}
		b.WriteString(gr.Str())
		col += gw
	}
	flush()

	if len(segs) == 0 {
		return list.PlainItem(line)
	}
	return segs
}
