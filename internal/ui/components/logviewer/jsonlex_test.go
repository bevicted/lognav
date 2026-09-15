package logviewer

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/assert"
)

// TestTokenize_ColumnWidthInvariant pins the per-line column invariant
// (0 <= Start <= End <= display width of the line) for inputs that previously
// broke column accounting -- the three FuzzJSONLex carve-outs: zero-width
// control chars, multi-byte runes outside strings, and trailing/at-newline
// escapes. Terminated escapes must stay correct.
func TestTokenize_ColumnWidthInvariant(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"\"\x1e\"",          // U+001E (zero-width control) inside a string
		"\"\x7f\"",          // U+007F DEL inside a string
		"\r0",               // CR (zero-width) then a number at top level
		"é0",                // multi-byte rune outside a string, then a number
		"\"\\",              // unterminated trailing escape
		"\"\\\n0",           // backslash immediately before a wrap newline
		"{\"k\":\"a\\nb\"}", // terminated escape (must remain correct)
		"\"café\"",          // multi-byte inside a string (already handled)
	}
	for _, s := range inputs {
		lines := tokenize([]byte(s))
		sourceLines := strings.Split(s, "\n")
		for li, toks := range lines {
			var lineText string
			if li < len(sourceLines) {
				lineText = sourceLines[li]
			}
			lineW := uniseg.StringWidth(lineText)
			for ti, tok := range toks {
				assert.GreaterOrEqualf(t, tok.Start, 0, "input=%q line=%d tok=%d", s, li, ti)
				assert.LessOrEqualf(t, tok.Start, tok.End, "input=%q line=%d tok=%d", s, li, ti)
				assert.LessOrEqualf(t, tok.End, lineW,
					"input=%q line=%d tok=%d End=%d lineW=%d", s, li, ti, tok.End, lineW)
			}
		}
	}
}

// Raw-string token text constants used across multiple jsonlex test cases.
const (
	tokTextA      = `"a"`
	tokTextB      = `"b"`
	tokTextIndent = `    }`
)

func TestTokenize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  [][]Token
	}{
		{
			name:  "empty object",
			input: "{}",
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenSyntax, Start: 1, End: 2, Text: "}"},
			}},
		},
		{
			name:  "single string field",
			input: `{"name":"alice"}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 7, Text: `"name"`},
				{Type: TokenSyntax, Start: 7, End: 8, Text: ":"},
				{Type: TokenString, Start: 8, End: 15, Text: `"alice"`},
				{Type: TokenSyntax, Start: 15, End: 16, Text: "}"},
			}},
		},
		{
			name:  "number value",
			input: `{"age":30}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 6, Text: `"age"`},
				{Type: TokenSyntax, Start: 6, End: 7, Text: ":"},
				{Type: TokenNumber, Start: 7, End: 9, Text: "30"},
				{Type: TokenSyntax, Start: 9, End: 10, Text: "}"},
			}},
		},
		{
			name:  "bool and null values",
			input: `{"a":true,"b":false,"c":null}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 4, Text: tokTextA},
				{Type: TokenSyntax, Start: 4, End: 5, Text: ":"},
				{Type: TokenBool, Start: 5, End: 9, Text: "true"},
				{Type: TokenSyntax, Start: 9, End: 10, Text: ","},
				{Type: TokenKey, Start: 10, End: 13, Text: tokTextB},
				{Type: TokenSyntax, Start: 13, End: 14, Text: ":"},
				{Type: TokenBool, Start: 14, End: 19, Text: "false"},
				{Type: TokenSyntax, Start: 19, End: 20, Text: ","},
				{Type: TokenKey, Start: 20, End: 23, Text: `"c"`},
				{Type: TokenSyntax, Start: 23, End: 24, Text: ":"},
				{Type: TokenNull, Start: 24, End: 28, Text: "null"},
				{Type: TokenSyntax, Start: 28, End: 29, Text: "}"},
			}},
		},
		{
			name:  "array value",
			input: `{"items":[1,"two",true]}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 8, Text: `"items"`},
				{Type: TokenSyntax, Start: 8, End: 9, Text: ":"},
				{Type: TokenSyntax, Start: 9, End: 10, Text: "["},
				{Type: TokenNumber, Start: 10, End: 11, Text: "1"},
				{Type: TokenSyntax, Start: 11, End: 12, Text: ","},
				{Type: TokenString, Start: 12, End: 17, Text: `"two"`},
				{Type: TokenSyntax, Start: 17, End: 18, Text: ","},
				{Type: TokenBool, Start: 18, End: 22, Text: "true"},
				{Type: TokenSyntax, Start: 22, End: 23, Text: "]"},
				{Type: TokenSyntax, Start: 23, End: 24, Text: "}"},
			}},
		},
		{
			name:  "escaped string",
			input: `{"msg":"hello \"world\""}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 6, Text: `"msg"`},
				{Type: TokenSyntax, Start: 6, End: 7, Text: ":"},
				{Type: TokenString, Start: 7, End: 24, Text: `"hello \"world\""`},
				{Type: TokenSyntax, Start: 24, End: 25, Text: "}"},
			}},
		},
		{
			name:  "multiline indented",
			input: "{\n    \"name\": \"alice\"\n}",
			want: [][]Token{
				{{Type: TokenSyntax, Start: 0, End: 1, Text: "{"}},
				{
					{Type: TokenKey, Start: 4, End: 10, Text: `"name"`},
					{Type: TokenSyntax, Start: 10, End: 11, Text: ":"},
					{Type: TokenString, Start: 12, End: 19, Text: `"alice"`},
				},
				{{Type: TokenSyntax, Start: 0, End: 1, Text: "}"}},
			},
		},
		{
			name:  "negative and float numbers",
			input: `{"a":-3.14,"b":1e10}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 4, Text: tokTextA},
				{Type: TokenSyntax, Start: 4, End: 5, Text: ":"},
				{Type: TokenNumber, Start: 5, End: 10, Text: "-3.14"},
				{Type: TokenSyntax, Start: 10, End: 11, Text: ","},
				{Type: TokenKey, Start: 11, End: 14, Text: tokTextB},
				{Type: TokenSyntax, Start: 14, End: 15, Text: ":"},
				{Type: TokenNumber, Start: 15, End: 19, Text: "1e10"},
				{Type: TokenSyntax, Start: 19, End: 20, Text: "}"},
			}},
		},
		{
			name:  "empty input",
			input: "",
			want:  [][]Token{nil},
		},
		{
			name:  "non-json string",
			input: "hello world",
			want:  [][]Token{{}},
		},
		{
			name:  "wrapped string value",
			input: "{\n    \"key\": \"hello\n              world\"\n}",
			want: [][]Token{
				// {
				{{Type: TokenSyntax, Start: 0, End: 1, Text: "{"}},
				// "key": "hello
				{
					{Type: TokenKey, Start: 4, End: 9, Text: `"key"`},
					{Type: TokenSyntax, Start: 9, End: 10, Text: ":"},
					{Type: TokenString, Start: 11, End: 17, Text: `"hello`},
				},
				//               world"
				{
					{Type: TokenString, Start: 0, End: 20, Text: `              world"`},
				},
				// }
				{{Type: TokenSyntax, Start: 0, End: 1, Text: "}"}},
			},
		},
		{
			name:  "wrapped string with multiple continuations",
			input: "{\n    \"k\": \"aa\n          bb\n          cc\"\n}",
			want: [][]Token{
				// {
				{{Type: TokenSyntax, Start: 0, End: 1, Text: "{"}},
				// "k": "aa
				{
					{Type: TokenKey, Start: 4, End: 7, Text: `"k"`},
					{Type: TokenSyntax, Start: 7, End: 8, Text: ":"},
					{Type: TokenString, Start: 9, End: 12, Text: `"aa`},
				},
				//           bb
				{
					{Type: TokenString, Start: 0, End: 12, Text: "          bb"},
				},
				//           cc"
				{
					{Type: TokenString, Start: 0, End: 13, Text: `          cc"`},
				},
				// }
				{{Type: TokenSyntax, Start: 0, End: 1, Text: "}"}},
			},
		},
		{
			name:  "unicode value",
			input: `{"k":"日本語"}`,
			want: [][]Token{{
				{Type: TokenSyntax, Start: 0, End: 1, Text: "{"},
				{Type: TokenKey, Start: 1, End: 4, Text: `"k"`},
				{Type: TokenSyntax, Start: 4, End: 5, Text: ":"},
				{Type: TokenString, Start: 5, End: 13, Text: `"日本語"`},
				{Type: TokenSyntax, Start: 13, End: 14, Text: "}"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tokenize([]byte(tt.input))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCursorRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		tokens    []Token
		expanded  bool
		wantStart int
		wantEnd   int
	}{
		{
			name:      "expanded with key highlights key",
			line:      `    "name": "alice"`,
			tokens:    []Token{{Type: TokenKey, Start: 4, End: 10}, {Type: TokenSyntax, Start: 10, End: 11}, {Type: TokenString, Start: 12, End: 19}},
			expanded:  true,
			wantStart: 4,
			wantEnd:   10,
		},
		{
			name:      "collapsed with key highlights full content",
			line:      `{"name":"alice","age":30}`,
			tokens:    []Token{{Type: TokenSyntax, Start: 0, End: 1}, {Type: TokenKey, Start: 1, End: 7}},
			expanded:  false,
			wantStart: 0,
			wantEnd:   25,
		},
		{
			name:      "no key, has content",
			line:      `    "hello"`,
			tokens:    []Token{{Type: TokenString, Start: 4, End: 11}},
			expanded:  true,
			wantStart: 4,
			wantEnd:   11,
		},
		{
			name:      "no key, syntax only",
			line:      tokTextIndent,
			tokens:    []Token{{Type: TokenSyntax, Start: 4, End: 5}},
			expanded:  true,
			wantStart: 4,
			wantEnd:   5,
		},
		{
			name:      "all whitespace",
			line:      `    `,
			tokens:    nil,
			expanded:  true,
			wantStart: -1,
			wantEnd:   -1,
		},
		{
			name:      "empty line",
			line:      "",
			tokens:    nil,
			expanded:  true,
			wantStart: -1,
			wantEnd:   -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStart, gotEnd := cursorRange(tt.line, tt.tokens, tt.expanded)
			assert.Equal(t, tt.wantStart, gotStart, "start")
			assert.Equal(t, tt.wantEnd, gotEnd, "end")
		})
	}
}

func TestExtractKeyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		lines      []tokenizedLine
		cursorLine int
		want       []string
	}{
		{
			name: "top-level key",
			lines: []tokenizedLine{
				{text: `    "name": "alice"`, tokens: []Token{{Type: TokenKey, Start: 4, End: 10, Text: `"name"`}, {Type: TokenSyntax, Start: 10, End: 11, Text: ":"}, {Type: TokenString, Start: 12, End: 19, Text: `"alice"`}}},
			},
			cursorLine: 0,
			want:       []string{"name"},
		},
		{
			name: "nested key",
			lines: []tokenizedLine{
				{text: `{`, tokens: []Token{{Type: TokenSyntax, Start: 0, End: 1, Text: "{"}}},
				{text: `    "a": {`, tokens: []Token{{Type: TokenKey, Start: 4, End: 7, Text: tokTextA}, {Type: TokenSyntax, Start: 7, End: 8, Text: ":"}, {Type: TokenSyntax, Start: 9, End: 10, Text: "{"}}},
				{text: `        "b": "val"`, tokens: []Token{{Type: TokenKey, Start: 8, End: 11, Text: tokTextB}, {Type: TokenSyntax, Start: 11, End: 12, Text: ":"}, {Type: TokenString, Start: 13, End: 18, Text: `"val"`}}},
				{text: tokTextIndent, tokens: []Token{{Type: TokenSyntax, Start: 4, End: 5, Text: "}"}}},
				{text: `}`, tokens: []Token{{Type: TokenSyntax, Start: 0, End: 1, Text: "}"}}},
			},
			cursorLine: 2,
			want:       []string{"a", "b"},
		},
		{
			name: "deeply nested key",
			lines: []tokenizedLine{
				{text: `{`, tokens: []Token{{Type: TokenSyntax, Start: 0, End: 1, Text: "{"}}},
				{text: `    "a": {`, tokens: []Token{{Type: TokenKey, Start: 4, End: 7, Text: tokTextA}, {Type: TokenSyntax, Start: 7, End: 8, Text: ":"}, {Type: TokenSyntax, Start: 9, End: 10, Text: "{"}}},
				{text: `        "b": {`, tokens: []Token{{Type: TokenKey, Start: 8, End: 11, Text: tokTextB}, {Type: TokenSyntax, Start: 11, End: 12, Text: ":"}, {Type: TokenSyntax, Start: 13, End: 14, Text: "{"}}},
				{text: `            "c": 42`, tokens: []Token{{Type: TokenKey, Start: 12, End: 15, Text: `"c"`}, {Type: TokenSyntax, Start: 15, End: 16, Text: ":"}, {Type: TokenNumber, Start: 17, End: 19, Text: "42"}}},
				{text: `        }`, tokens: []Token{{Type: TokenSyntax, Start: 8, End: 9, Text: "}"}}},
				{text: tokTextIndent, tokens: []Token{{Type: TokenSyntax, Start: 4, End: 5, Text: "}"}}},
				{text: `}`, tokens: []Token{{Type: TokenSyntax, Start: 0, End: 1, Text: "}"}}},
			},
			cursorLine: 3,
			want:       []string{"a", "b", "c"},
		},
		{
			name: "cursor on wrapped continuation",
			lines: []tokenizedLine{
				{text: `    "msg": "hello`, tokens: []Token{{Type: TokenKey, Start: 4, End: 9, Text: `"msg"`}, {Type: TokenSyntax, Start: 9, End: 10, Text: ":"}, {Type: TokenString, Start: 11, End: 17, Text: `"hello`}}},
				{text: `           world"`, tokens: []Token{{Type: TokenString, Start: 0, End: 17, Text: `           world"`}}},
			},
			cursorLine: 1,
			want:       []string{fieldMsg},
		},
		{
			name: "no key on line",
			lines: []tokenizedLine{
				{text: tokTextIndent, tokens: []Token{{Type: TokenSyntax, Start: 4, End: 5, Text: "}"}}},
			},
			cursorLine: 0,
			want:       nil,
		},
		{
			name:       "empty lines",
			lines:      nil,
			cursorLine: 0,
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractKeyPath(tt.lines, tt.cursorLine)
			assert.Equal(t, tt.want, got)
		})
	}
}
