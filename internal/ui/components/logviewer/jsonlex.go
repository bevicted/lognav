package logviewer

import (
	"bytes"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// TokenType identifies a class of JSON token for syntax coloring.
type TokenType uint8

const (
	TokenKey    TokenType = iota // object key
	TokenString                  // string value
	TokenNumber                  // number literal
	TokenBool                    // true, false
	TokenNull                    // null
	TokenSyntax                  // structural: { } [ ] : ,
)

// tokenizePerLineCapHint is the per-line initial Token slice capacity used
// by tokenize. Sized to ~p50 tokens per JSON line on the lognav corpus
// (~12 tokens median; rounded up). append() doubles past this; 16 vs 32
// is at most one extra reallocation in the tail per oversized line.
const tokenizePerLineCapHint = 16

// tokenizeMaxLineHint caps the lines slice capacity at allocation time.
// Mirrors G[P1] D14's hard-bound precedent against adversarial input: a
// 1 GiB user log with 100M newlines should NOT cause an up-front 800 MB
// slice header allocation. append() still grows past this if a real log
// actually has more than 64Ki lines (rare in practice).
const tokenizeMaxLineHint = 1 << 16

// Token marks a span in a single line of rendered JSON.
type Token struct {
	Type  TokenType
	Start int    // column offset in line (display width)
	End   int    // column offset in line (exclusive)
	Text  string // token text content
}

// tokenizedLine pairs a rendered line with its token spans and color-rule spans.
type tokenizedLine struct {
	text       string
	tokens     []Token
	matchSpans []matchSpan // overlap-resolved color-rule spans; nil when the engine is disabled or the line has no hits
}

// buildTokenizedLines pairs each '\n'-separated line of b with its token slice
// from tokens (as returned by tokenize(b)) and, when engine is enabled, its
// resolved color-rule spans. It is the render-cache entry builder.
//
// b's text is copied exactly ONCE, into text: every line's string is a
// sub-string of that single allocation (as strings.Split would also give, and
// with the same whole-buffer retention), and every line's bytes are a
// sub-slice of b, so the color-rule scan and its byte→column map read the line
// in place instead of copying it a second time. When the engine is disabled no
// per-line byte work happens at all.
//
// b's sub-slices are used only during the call — nothing retained in the
// returned entry aliases b — so a caller may reuse b afterwards.
func buildTokenizedLines(b []byte, tokens [][]Token, engine *colorRuleEngine) []tokenizedLine {
	text := string(b)
	out := make([]tokenizedLine, 0, bytes.Count(b, []byte{'\n'})+1)
	for start := 0; ; {
		end, next := len(b), -1
		if nl := bytes.IndexByte(b[start:], '\n'); nl >= 0 {
			end = start + nl
			next = end + 1
		}

		var toks []Token
		if i := len(out); i < len(tokens) {
			toks = tokens[i]
		}
		var spans []matchSpan
		if engine.enabled {
			lineBytes := b[start:end]
			spans = engine.scan(lineBytes, byteToCol(lineBytes))
		}
		out = append(out, tokenizedLine{
			text:       text[start:end],
			tokens:     toks,
			matchSpans: spans,
		})

		if next < 0 {
			return out
		}
		start = next
	}
}

// unquoteKey strips surrounding quotes from a key token string.
func unquoteKey(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// findKeyToken returns the first TokenKey on the line and its indent (Start position).
// Returns nil, -1 if no key is found.
func findKeyToken(tl tokenizedLine) (*Token, int) {
	for i := range tl.tokens {
		if tl.tokens[i].Type == TokenKey {
			return &tl.tokens[i], tl.tokens[i].Start
		}
	}
	return nil, -1
}

// extractKeyPath returns the full key path from root to the field at cursorLine.
// It walks upward through decreasing indentation levels to find parent keys.
// For example, in {"a": {"b": "val"}}, cursor on "b" returns ["a", "b"].
// Returns nil if no key is found.
func extractKeyPath(lines []tokenizedLine, cursorLine int) []string {
	if cursorLine < 0 || cursorLine >= len(lines) {
		return nil
	}

	// First, find the key for the cursor line (walk up past wrapped continuations)
	keyLine := -1
	for i := cursorLine; i >= 0; i-- {
		if tok, _ := findKeyToken(lines[i]); tok != nil {
			keyLine = i
			break
		}
		// Stop if we hit non-string tokens (past field boundary)
		if i < cursorLine {
			for _, tok := range lines[i].tokens {
				if tok.Type != TokenString {
					return nil
				}
			}
		}
	}
	if keyLine < 0 {
		return nil
	}

	// Build path by collecting this key and all parent keys at decreasing indentation
	tok, indent := findKeyToken(lines[keyLine])
	path := []string{unquoteKey(tok.Text)}

	// Walk upward to find parent keys at lower indent levels
	for i := keyLine - 1; i >= 0; i-- {
		parentTok, parentIndent := findKeyToken(lines[i])
		if parentTok == nil {
			continue
		}
		if parentIndent < indent {
			path = append(path, unquoteKey(parentTok.Text))
			indent = parentIndent
		}
	}

	// Reverse to get root-to-leaf order
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// tokenize lexes JSON bytes (from json.Marshal or json.MarshalIndent)
// and returns per-line token slices. Line splitting follows '\n'.
// Token offsets are in column (display width) space, not byte offsets.
//
//nolint:gocyclo,funlen // single-pass JSON lexer; state-machine branching is irreducible without slowdown.
func tokenize(b []byte) [][]Token {
	if len(b) == 0 {
		return [][]Token{nil}
	}

	lineHint := min(bytes.Count(b, []byte{'\n'})+1, tokenizeMaxLineHint)
	var (
		lines       = make([][]Token, 0, lineHint)
		current     = make([]Token, 0, tokenizePerLineCapHint)
		lineColBase int    // column offset where the current line starts
		col         int    // current column (display width)
		stack       []byte // '{' or '[' — tracks object vs array context
		afterKey    bool   // true after a key's closing quote, before ':'
	)

	emit := func(typ TokenType, startByte, endByte, startCol, endCol int) {
		current = append(current, Token{
			Type:  typ,
			Start: startCol - lineColBase,
			End:   endCol - lineColBase,
			Text:  string(b[startByte:endByte]),
		})
	}

	i := 0
	for i < len(b) {
		ch := b[i]
		switch {
		case ch == '\n':
			lines = append(lines, current)
			current = nil
			i++
			lineColBase = col // newline has zero display width

		case ch == '{' || ch == '[':
			startCol := col
			col++
			emit(TokenSyntax, i, i+1, startCol, col)
			stack = append(stack, ch)
			i++

		case ch == '}' || ch == ']':
			startCol := col
			col++
			emit(TokenSyntax, i, i+1, startCol, col)
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			afterKey = false
			i++

		case ch == ':':
			startCol := col
			col++
			emit(TokenSyntax, i, i+1, startCol, col)
			afterKey = false
			i++

		case ch == ',':
			startCol := col
			col++
			emit(TokenSyntax, i, i+1, startCol, col)
			afterKey = false
			i++

		case ch == '"':
			startByte := i
			startCol := col
			col++ // opening quote
			i++   // skip opening quote
			for i < len(b) && b[i] != '"' {
				if b[i] == '\n' {
					// Line wrapped mid-string (from WrapLinesAt).
					// Emit partial token for current line, start new line.
					emit(TokenString, startByte, i, startCol, col)
					lines = append(lines, current)
					current = nil
					i++
					lineColBase = col // newline has zero display width
					startByte = i     // continuation starts at new line
					startCol = col
					continue
				}
				if b[i] == '\\' {
					col++ // backslash (width 1)
					i++
					// Count the escaped byte below as a literal so an escaped
					// quote is not read as the terminator. A trailing backslash
					// (EOF) or one before a raw newline has no escaped byte: re-loop
					// so EOF ends the string and a newline still breaks the line.
					if i >= len(b) || b[i] == '\n' {
						continue
					}
				}
				w, size := runeWidthAt(b, i)
				col += w
				i += size
			}
			if i < len(b) {
				col++ // closing quote
				i++   // skip closing quote
			}
			// Determine if this string is a key: must be inside an object
			// and the next non-whitespace char must be ':'
			inObject := len(stack) > 0 && stack[len(stack)-1] == '{'
			if inObject && isKeyString(b, i) {
				emit(TokenKey, startByte, i, startCol, col)
				afterKey = true
			} else {
				emit(TokenString, startByte, i, startCol, col)
			}

		case ch == 't', ch == 'f':
			startByte := i
			startCol := col
			switch {
			case i+4 <= len(b) && string(b[i:i+4]) == "true":
				col += 4
				emit(TokenBool, startByte, i+4, startCol, col)
				i += 4
			case i+5 <= len(b) && string(b[i:i+5]) == "false":
				col += 5
				emit(TokenBool, startByte, i+5, startCol, col)
				i += 5
			default:
				col++
				i++
			}

		case ch == 'n':
			startByte := i
			startCol := col
			if i+4 <= len(b) && string(b[i:i+4]) == "null" {
				col += 4
				emit(TokenNull, startByte, i+4, startCol, col)
				i += 4
			} else {
				col++
				i++
			}

		case ch == '-' || (ch >= '0' && ch <= '9'):
			startByte := i
			startCol := col
			col++
			i++
			for i < len(b) && isNumberChar(b[i]) {
				col++
				i++
			}
			emit(TokenNumber, startByte, i, startCol, col)

		default:
			// whitespace or unexpected byte/rune — advance by display width so
			// zero-width control chars and multi-byte runes don't desync col.
			w, size := runeWidthAt(b, i)
			col += w
			i += size
		}

		_ = afterKey // used for future field interaction
	}

	// append final line
	lines = append(lines, current)
	return lines
}

// isKeyString checks whether the next non-whitespace byte after position i is ':'.
func isKeyString(b []byte, i int) bool {
	for i < len(b) {
		if b[i] == ':' {
			return true
		}
		if b[i] != ' ' && b[i] != '\t' {
			return false
		}
		i++
	}
	return false
}

// isNumberChar reports whether ch can appear inside a JSON number literal.
func isNumberChar(ch byte) bool {
	return (ch >= '0' && ch <= '9') || ch == '.' || ch == 'e' || ch == 'E' || ch == '+' || ch == '-'
}

// runeWidthAt returns the terminal cell width and byte size of the character
// starting at b[i]. ASCII control bytes (C0 range and DEL) are zero-width,
// other ASCII is width 1, and multi-byte UTF-8 is measured with uniseg. Keeping
// tokenize's per-character advance equal to display width is what holds the
// column invariant (0 <= Start <= End <= uniseg.StringWidth(line)).
func runeWidthAt(b []byte, i int) (width, size int) {
	if c := b[i]; c < utf8.RuneSelf {
		if c < 0x20 || c == 0x7f {
			return 0, 1
		}
		return 1, 1
	}
	r, size := utf8.DecodeRune(b[i:])
	return uniseg.StringWidth(string(r)), size
}
