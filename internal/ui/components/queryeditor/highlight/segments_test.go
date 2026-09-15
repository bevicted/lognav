package highlight

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/ui/components/list"
)

// TestSegments_HighlightsAndCoalesces verifies that Segments produces one entry
// per source line, preserves the original text per line, and colors recognized
// tokens with the same styles as the editor while merging unstyled runs.
func TestSegments_HighlightsAndCoalesces(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	keywordStyle := TokenCellStyle(cfg, TokenKeyword)

	tests := []struct {
		name      string
		source    string
		wantLines int
		// assert runs extra checks against the produced segments.
		assert func(t *testing.T, lines [][]list.Segment)
	}{
		{
			name:      "empty source is one plain blank line",
			source:    "",
			wantLines: 1,
			assert: func(t *testing.T, lines [][]list.Segment) {
				assert.Equal(t, list.PlainItem(""), lines[0])
			},
		},
		{
			name:      "keyword is bold-colored, rest plain",
			source:    "source logs",
			wantLines: 1,
			assert: func(t *testing.T, lines [][]list.Segment) {
				require.GreaterOrEqual(t, len(lines[0]), 2, "expected a keyword run plus a plain run")
				assert.Equal(t, "source", lines[0][0].Text)
				got := lines[0][0].Style
				assert.True(t, got.Equal(&keywordStyle), "first run uses the keyword style")
				assert.Equal(t, "source logs", list.ItemText(lines[0]), "text round-trips")
			},
		},
		{
			name:      "multi-line keeps per-line entries",
			source:    "source logs\n| filter $d.msg ~ 'x'",
			wantLines: 2,
			assert: func(t *testing.T, lines [][]list.Segment) {
				assert.Equal(t, "source logs", list.ItemText(lines[0]))
				assert.Equal(t, "| filter $d.msg ~ 'x'", list.ItemText(lines[1]))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines := Segments(cfg, tt.source)
			require.Len(t, lines, tt.wantLines)
			tt.assert(t, lines)
		})
	}
}

// TestCellStyle_Precedence locks in the per-column precedence shared by the
// editor render path and the preview segment builder: a non-error token sets
// the foreground; an error token adds a curly underline only when it does not
// overwrite an already-empty foreground (mirroring render.go exactly).
func TestCellStyle_Precedence(t *testing.T) {
	t.Parallel()
	cfg := config.New()

	kw := Token{Type: TokenKeyword, Start: 0, End: 6}
	errTok := Token{Type: TokenError, Start: 0, End: 6}

	tests := []struct {
		name   string
		tokens []Token
		col    int
		want   uv.Style
	}{
		{
			name:   "uncovered column is zero style",
			tokens: []Token{kw},
			col:    10,
			want:   uv.Style{},
		},
		{
			name:   "covered column gets keyword style",
			tokens: []Token{kw},
			col:    2,
			want:   TokenCellStyle(cfg, TokenKeyword),
		},
		{
			name:   "keyword then error: foreground kept, underline added",
			tokens: []Token{kw, errTok},
			col:    2,
			want: func() uv.Style {
				s := TokenCellStyle(cfg, TokenKeyword)
				s.Underline = uv.UnderlineCurly
				s.UnderlineColor = cfg.Style.DpErrorFg.Color
				return s
			}(),
		},
		{
			name:   "error then keyword: keyword overwrites, no underline",
			tokens: []Token{errTok, kw},
			col:    2,
			want:   TokenCellStyle(cfg, TokenKeyword),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CellStyle(cfg, tt.tokens, tt.col)
			assert.True(t, got.Equal(&tt.want), "got %+v want %+v", got, tt.want)
		})
	}
}
