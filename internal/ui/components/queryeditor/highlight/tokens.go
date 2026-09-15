package highlight

// TokenType identifies a class of Dataprime syntax element for coloring.
type TokenType uint8

const (
	TokenNone        TokenType = iota // no highlight (default fg)
	TokenKeyword                      // source, filter, groupby, etc.
	TokenOperator                     // &&, ||, +, -, ==, etc.
	TokenString                       // string literals, escape sequences, regexes
	TokenNumber                       // number literals, intervals
	TokenComment                      // line and block comments
	TokenFunction                     // function calls
	TokenVariable                     // variables, fields, parameters
	TokenType_                        // type names (underscore to avoid conflict with Go keyword)
	TokenBoolNull                     // true, false, null
	TokenPunctuation                  // brackets, delimiters
	TokenError                        // tree-sitter ERROR nodes
)

// Token marks a highlighted span in a single line of query text.
// Offsets are in display-width (column) space, not byte offsets.
type Token struct {
	Type  TokenType
	Start int // column offset in line (display width, inclusive)
	End   int // column offset in line (display width, exclusive)
}

// captureToTokenType maps a tree-sitter capture name to a TokenType.
func captureToTokenType(name string) TokenType {
	switch name {
	case "keyword":
		return TokenKeyword
	case "operator":
		return TokenOperator
	case "string", "string.escape", "string.regexp":
		return TokenString
	case "number":
		return TokenNumber
	case "comment":
		return TokenComment
	case "function.call", "function.method.call", "function.builtin":
		return TokenFunction
	case "variable", "variable.member", "variable.builtin", "variable.parameter":
		return TokenVariable
	case "type":
		return TokenType_
	case "boolean", "constant.builtin":
		return TokenBoolNull
	case "punctuation.bracket", "punctuation.delimiter", "punctuation.special":
		return TokenPunctuation
	default:
		return TokenNone
	}
}
