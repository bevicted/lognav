package highlight

import (
	_ "embed"
	"log/slog"
	"slices"
	"strings"

	"github.com/rivo/uniseg"
	tree_sitter_dataprime "github.com/smrtrfszm/tree-sitter-dataprime/bindings/go"
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/bevicted/lognav/internal/logging"
)

//go:embed highlights.scm
var highlightsQuery string

var (
	language *sitter.Language
	query    *sitter.Query
)

var logger = slog.Default().With(logging.KeyComponent, "highlight")

// defaultHighlighter wraps the real tree-sitter highlight path. Exposed as a
// package-internal var so tests can substitute a panicking impl (to exercise
// Highlight's recover() gate against tree-sitter binding panics) without
// touching production code.
var defaultHighlighter = highlightInternal

func init() {
	language = sitter.NewLanguage(tree_sitter_dataprime.Language())
	var qErr *sitter.QueryError
	query, qErr = sitter.NewQuery(language, highlightsQuery)
	if qErr != nil {
		panic("highlight: failed to compile highlights query: " + qErr.Error())
	}
}

// Highlight parses source text and returns tokens grouped by line. The
// deferred recover catches Go-level panics from the tree-sitter binding
// (nil deref, slice out-of-range); on panic returns nil tokens and the
// caller falls back to plain rendering. Note: cgo SIGSEGV is NOT caught
// by recover() — this is defense-in-depth, not a hard guarantee. The
// init() panic on a malformed highlights.scm is intentionally unguarded
// (developer error caught at startup, not runtime input).
// Named return `tokens` is REQUIRED; the deferred closure assigns to that slot.
func Highlight(source string) (tokens [][]Token) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("highlight panic", slog.Any("panic", r))
			tokens = nil
		}
	}()
	return defaultHighlighter(source)
}

// highlightInternal parses the source text and returns tokens grouped by line.
// Each inner slice contains tokens for that logical line, with display-width column offsets.
func highlightInternal(source string) [][]Token {
	if len(source) == 0 {
		return nil
	}

	sourceBytes := []byte(source)

	parser := sitter.NewParser()
	defer parser.Close()
	_ = parser.SetLanguage(language)

	tree := parser.Parse(sourceBytes, nil)
	defer tree.Close()

	root := tree.RootNode()

	lineStarts := buildLineStarts(sourceBytes)
	lineCount := strings.Count(source, "\n") + 1
	tokensByLine := make([][]Token, lineCount)

	cursor := sitter.NewQueryCursor()
	defer cursor.Close()

	captures := cursor.Captures(query, root, sourceBytes)
	for match, captureIdx := captures.Next(); match != nil; match, captureIdx = captures.Next() {
		if int(captureIdx) >= len(match.Captures) {
			continue
		}
		capture := match.Captures[captureIdx]
		captureName := query.CaptureNames()[capture.Index]
		tokenType := captureToTokenType(captureName)
		if tokenType == TokenNone {
			continue
		}

		node := capture.Node
		toks := nodeToTokens(sourceBytes, node, tokenType, lineStarts)
		startLine := int(node.StartPosition().Row)
		for i, tok := range toks {
			lineIdx := startLine + i
			if lineIdx < lineCount {
				tokensByLine[lineIdx] = append(tokensByLine[lineIdx], tok)
			}
		}
	}

	collectErrors(root, sourceBytes, lineStarts, tokensByLine)

	return tokensByLine
}

// buildLineStarts returns byte offsets where each line begins.
func buildLineStarts(source []byte) []int {
	starts := []int{0}
	for i, b := range source {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func byteToLineCol(source []byte, byteOffset int, lineStarts []int) (int, int) {
	line := 0
	for i, start := range slices.Backward(lineStarts) {
		if byteOffset >= start {
			line = i
			break
		}
	}
	lineBytes := source[lineStarts[line]:byteOffset]
	col := uniseg.StringWidth(string(lineBytes))
	return line, col
}

func nodeToTokens(source []byte, node sitter.Node, tokenType TokenType, lineStarts []int) []Token {
	startByte := int(node.StartByte())
	endByte := int(node.EndByte())

	startLine, startCol := byteToLineCol(source, startByte, lineStarts)
	endLine, endCol := byteToLineCol(source, endByte, lineStarts)

	if startLine == endLine {
		return []Token{{Type: tokenType, Start: startCol, End: endCol}}
	}

	var tokens []Token
	for l := startLine; l <= endLine; l++ {
		var tStart, tEnd int
		if l == endLine && l != startLine {
			tStart = 0
			tEnd = endCol
		} else {
			if l == startLine {
				tStart = startCol
			}
			tEnd = lineDisplayWidth(source, l, lineStarts)
		}
		if tEnd > tStart {
			tokens = append(tokens, Token{Type: tokenType, Start: tStart, End: tEnd})
		}
	}
	return tokens
}

func lineDisplayWidth(source []byte, l int, lineStarts []int) int {
	lineEnd := len(source)
	if l+1 < len(lineStarts) {
		lineEnd = lineStarts[l+1] - 1 // exclude '\n'
	}
	return uniseg.StringWidth(string(source[lineStarts[l]:lineEnd]))
}

func collectErrors(node *sitter.Node, source []byte, lineStarts []int, tokensByLine [][]Token) {
	if node.IsError() {
		toks := nodeToTokens(source, *node, TokenError, lineStarts)
		startLine := int(node.StartPosition().Row)
		for i, tok := range toks {
			lineIdx := startLine + i
			if lineIdx < len(tokensByLine) {
				tokensByLine[lineIdx] = append(tokensByLine[lineIdx], tok)
			}
		}
		return
	}
	for i := range node.ChildCount() {
		child := node.Child(i)
		if child != nil {
			collectErrors(child, source, lineStarts, tokensByLine)
		}
	}
}
