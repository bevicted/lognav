package logviewer

import (
	"encoding/json"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	uv "github.com/charmbracelet/ultraviolet"
)

// Hot-path benches for the per-line draw loop (audit items 6 + 26). Each is a
// PAIR of sub-benchmarks — the pre-refactor shape and the shipped shape — so a
// single `go test -bench` run shows the delta directly, without needing a
// baseline commit to A/B against:
//
//	TokenLookup/Rescan  vs TokenLookup/Cursor   → comparisons per line (cmp/op)
//	SearchMatches/Filtered vs SearchMatches/Indexed → allocs per frame
//
// The "old" arms live only in this file; production code has one implementation.

// benchTokenFixture returns a rendered JSON line and its token list, shaped like
// a real expanded log row (many small tokens, left-to-right, disjoint).
func benchTokenFixture(tb testing.TB) (string, []Token) {
	tb.Helper()
	payload := map[string]any{
		"msg":      "connection reset by peer while streaming chunk",
		"code":     503,
		"id":       "req-00004242",
		"trace":    "trace-9f2c1b",
		"span":     "span-0041",
		"retries":  3,
		"fatal":    false,
		"upstream": "logs.eu-de.icl.example.com",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		tb.Fatalf("marshal fixture: %v", err)
	}
	lines := tokenize(b)
	if len(lines) != 1 {
		tb.Fatalf("fixture must be a single line, got %d", len(lines))
	}
	return string(b), lines[0]
}

// rescanComparisons counts the token-span comparisons the pre-refactor lookup
// performed for a full left-to-right walk over cols columns: for every column
// it rescanned the token list from index 0.
func rescanComparisons(tokens []Token, cols int) int {
	n := 0
	for col := range cols {
		for i := range tokens {
			n++
			if col >= tokens[i].Start && col < tokens[i].End {
				break
			}
		}
	}
	return n
}

// cursorComparisons counts the comparisons the shipped tokenCursor performs for
// the same walk. It drives the REAL at() and derives the count from the cursor's
// observable advance: each at() does one End-comparison per advance plus one
// terminating one, and at most one Start-comparison. The cursor advances at most
// len(tokens) times in TOTAL across the whole line, so the count is
// O(cols + len(tokens)) instead of O(cols * len(tokens)).
func cursorComparisons(tokens []Token, cols int) int {
	n := 0
	tc := tokenCursor{tokens: tokens}
	for col := range cols {
		before := tc.i
		tok := tc.at(col)
		n += (tc.i - before) + 1
		if tok != nil {
			n++
		}
	}
	return n
}

// BenchmarkRenderLine_TokenLookup times, and reports the comparison count of,
// the covering-token lookup for one full line — the inner loop of Step 2. The
// cmp/op metric is the number the audit item is about; ns/op is secondary.
func BenchmarkRenderLine_TokenLookup(b *testing.B) {
	line, tokens := benchTokenFixture(b)
	cols := len([]rune(line)) // ASCII fixture: one column per rune

	b.Run("Rescan", func(b *testing.B) {
		b.ReportAllocs()
		var sink int
		for b.Loop() {
			sink = rescanComparisons(tokens, cols)
		}
		b.ReportMetric(float64(sink), "cmp/line")
	})

	b.Run("Cursor", func(b *testing.B) {
		b.ReportAllocs()
		var sink int
		for b.Loop() {
			sink = cursorComparisons(tokens, cols)
		}
		b.ReportMetric(float64(sink), "cmp/line")
	})
}

// benchSearchLineCount is the number of wrapped/expanded lines per log in the
// search-match benches — enough that the per-line filtered copy is a real cost.
const benchSearchLineCount = 24

// benchSearchMatchesPerLine is how many search hits each of those lines carries.
const benchSearchMatchesPerLine = 3

// benchWholeLogMatches builds one log's whole match slice: matches spread over
// benchSearchLineCount lines, sorted by line as search() emits them.
func benchWholeLogMatches() []SearchMatch {
	out := make([]SearchMatch, 0, benchSearchLineCount*benchSearchMatchesPerLine)
	for line := range benchSearchLineCount {
		for k := range benchSearchMatchesPerLine {
			start := 4 + k*9
			out = append(out, SearchMatch{line: line, start: start, end: start + 6})
		}
	}
	return out
}

// legacyFilterByLine reproduces the deleted SearchMap.Filter + LineFilter pair:
// one closure plus one filtered slice allocated per rendered line, per frame.
func legacyFilterByLine(matches []SearchMatch, line int) []SearchMatch {
	filterF := func(m SearchMatch) bool { return m.line == line }
	out := make([]SearchMatch, 0, len(matches))
	for _, m := range matches {
		if filterF(m) {
			out = append(out, m)
		}
	}
	return out
}

// BenchmarkRenderLine_SearchMatches renders one screenful of lines from a log
// that has search hits on every line, two ways: the pre-refactor way (allocate
// a filtered copy + closure per line) and the shipped way (pass the whole log's
// slice and index by lineIdx). Both arms paint identical cells; the allocs/op
// delta is the whole point.
func BenchmarkRenderLine_SearchMatches(b *testing.B) {
	const (
		w       = 100
		sideBar = 1
		total   = w + sideBar
	)
	line, tokens := benchTokenFixture(b)
	whole := benchWholeLogMatches()
	m := New(depstest.NewTest(b))
	s := uv.NewScreenBuffer(total, benchSearchLineCount)

	base := renderLineOpts{
		line:         line,
		tokens:       tokens,
		width:        w,
		sideBarWidth: sideBar,
	}

	b.Run("Filtered", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for j := range benchSearchLineCount {
				opts := base
				opts.lineIdx = j
				opts.searchMatches = legacyFilterByLine(whole, j)
				m.renderLine(s, j, opts)
			}
		}
	})

	b.Run("Indexed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for j := range benchSearchLineCount {
				opts := base
				opts.lineIdx = j
				opts.searchMatches = whole
				m.renderLine(s, j, opts)
			}
		}
	})
}

// BenchmarkRender_DrawWithSearch is the end-to-end scroll-shaped counterpart:
// a full viewport Draw with a populated SearchMap, i.e. what a redraw costs
// while a search is active. On the pre-refactor code every drawn row allocated
// a filtered slice here; now the frame allocates none for search.
func BenchmarkRender_DrawWithSearch(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchScrollViewport)
	s := uv.NewScreenBuffer(120, benchScrollViewport)
	m.Draw(s) // settle layout/cursor before timing

	// Give every log in the visible window search hits on its first lines.
	for i := range benchLogCount {
		store.state.search[i] = benchWholeLogMatches()
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.Draw(s)
	}
}
