package logviewer

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/require"
)

// FuzzJSONLex feeds adversarial JSON-ish inputs into tokenize and asserts:
//   - no panic
//   - per-line column-space invariants: 0 <= tok.Start <= tok.End <= uniseg.StringWidth(line)
//   - non-overlapping tokens within a line
//   - TokenType in declared enum range
//
// The seed corpus in testdata/fuzz/FuzzJSONLex preserves inputs that once broke
// column accounting (zero-width control chars, multi-byte runes outside strings,
// trailing/at-newline escapes). tokenize now advances col by display width
// (runeWidthAt) so they pass; they remain as regression seeds. The named,
// readable equivalents live in TestTokenize_ColumnWidthInvariant.
func FuzzJSONLex(f *testing.F) {
	seeds := []string{
		// happy-path
		`{"k":"v"}`,
		`[]`,
		`null`,
		``,
		`{"a":[1,2,3]}`,
		// adversarial — per spec D6
		strings.Repeat("[", 1000),
		`"unterminated`,
		`"\\\\\""`,
		"\"\xff\xfe\"",
		"\"a\nb\"",
		"\"\xc0\"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		lines := tokenize([]byte(s))
		sourceLines := strings.Split(s, "\n")
		for i, toks := range lines {
			var lineText string
			if i < len(sourceLines) {
				lineText = sourceLines[i]
			}
			lineW := uniseg.StringWidth(lineText)
			for j, tok := range toks {
				require.GreaterOrEqual(t, tok.Start, 0,
					"tok.Start must be non-negative; line=%d tok=%d", i, j)
				require.LessOrEqual(t, tok.Start, tok.End,
					"tok.Start must not exceed tok.End; line=%d tok=%d", i, j)
				require.LessOrEqual(t, tok.End, lineW,
					"tok.End must not exceed line column width; line=%d tok=%d (End=%d, lineW=%d)",
					i, j, tok.End, lineW)
				require.LessOrEqual(t, tok.Type, TokenSyntax,
					"tok.Type must be in declared enum range; line=%d tok=%d", i, j)
				if j+1 < len(toks) {
					require.LessOrEqual(t, tok.End, toks[j+1].Start,
						"tokens must not overlap; line=%d tok=%d", i, j)
				}
			}
		}
	})
}
