package highlight

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHighlight(t *testing.T) {
	tests := []struct {
		name   string
		source string
		line   int
		want   []Token
	}{
		{
			name:   "keyword and constant",
			source: `source logs`,
			line:   0,
			want: []Token{
				{Type: TokenKeyword, Start: 0, End: 6},
			},
		},
		{
			name:   "filter with operator",
			source: `filter $d.msg == 'hello'`,
			line:   0,
			want: []Token{
				{Type: TokenKeyword, Start: 0, End: 6},
				{Type: TokenOperator, Start: 14, End: 16},
				{Type: TokenString, Start: 17, End: 24},
			},
		},
		{
			name:   "number literal",
			source: `limit 100`,
			line:   0,
			want: []Token{
				{Type: TokenKeyword, Start: 0, End: 5},
				{Type: TokenNumber, Start: 6, End: 9},
			},
		},
		{
			name:   "comment",
			source: `// this is a comment`,
			line:   0,
			want: []Token{
				{Type: TokenComment, Start: 0, End: 20},
			},
		},
		{
			name:   "multi-line keywords on both lines",
			source: "source logs\n| filter $d.msg ~ 'test'",
			line:   1,
			want: []Token{
				{Type: TokenKeyword, Start: 2, End: 8},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokensByLine := Highlight(tt.source)
			if tt.line >= len(tokensByLine) {
				t.Fatalf("line %d out of range (got %d lines)", tt.line, len(tokensByLine))
			}
			lineTokens := tokensByLine[tt.line]
			for _, want := range tt.want {
				found := false
				for _, got := range lineTokens {
					if got.Type == want.Type && got.Start == want.Start && got.End == want.End {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected token %+v not found in line %d tokens: %v", want, tt.line, lineTokens)
				}
			}
		})
	}
}

func TestHighlightErrors(t *testing.T) {
	tokensByLine := Highlight("source logs | filter ==")
	hasError := false
	for _, lineTokens := range tokensByLine {
		for _, tok := range lineTokens {
			if tok.Type == TokenError {
				hasError = true
				break
			}
		}
	}
	if !hasError {
		t.Errorf("expected TokenError for invalid syntax, got: %v", tokensByLine)
	}
}

func TestHighlightEmpty(t *testing.T) {
	tokensByLine := Highlight("")
	if tokensByLine != nil {
		t.Errorf("expected nil for empty input, got %v", tokensByLine)
	}
}

//nolint:paralleltest // mutates package-level defaultHighlighter
func TestHighlight_PanickingHighlighter_ReturnsNil(t *testing.T) {
	original := defaultHighlighter
	t.Cleanup(func() { defaultHighlighter = original })

	defaultHighlighter = func(source string) [][]Token {
		panic("simulated cgo-binding panic")
	}

	tokens := Highlight("source logs")
	assert.Nil(t, tokens,
		"Highlight must return nil when defaultHighlighter panics (G[P2] D3 recover gate)")
}

//nolint:paralleltest // mutates package-level logger and defaultHighlighter; not parallel-safe.
func TestHighlight_RecoversFromPanic(t *testing.T) {
	// Substitute a panicking highlighter and capture slog output.
	var buf bytes.Buffer
	prevLogger := logger
	prevHl := defaultHighlighter
	logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	defaultHighlighter = func(string) [][]Token { panic("simulated tree-sitter crash") }
	t.Cleanup(func() {
		logger = prevLogger
		defaultHighlighter = prevHl
	})

	result := Highlight("any source")
	assert.Nil(t, result)
	assert.Contains(t, buf.String(), "highlight panic")
	assert.Contains(t, buf.String(), "simulated tree-sitter crash")
}
