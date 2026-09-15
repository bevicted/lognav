package logviewer

import (
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
)

const (
	// benchLogCount is the synthetic store size used by render/tokenize/
	// colorrule/search benches that walk the full corpus.
	benchLogCount = 5000

	// benchWarmLineCount is the viewport / warm-window size for CacheHit
	// (D16). Chosen so the warm set fits inside the LRU cap (128) with
	// margin, ensuring every loop iteration hits the cache.
	benchWarmLineCount = 40

	// benchScrollViewport is the visible-line count used by the D21 scroll
	// hypothesis benches. Large enough to require ~10 entries per frame.
	benchScrollViewport = 50

	// benchScrollStep is the number of cursor lines advanced between each
	// viewport render call in the scroll trace. Chosen to be large relative
	// to benchScrollViewport so every advance evicts most of the warm window,
	// creating pressure on small LRU caps.
	benchScrollStep = 500

	// benchScrollTraceIters is the number of render+advance cycles per bench
	// loop iteration. Ten cycles walk 5 000 lines (10 × 500) — the full
	// corpus — so the LRU eviction pattern is deterministic and repeatable.
	benchScrollTraceIters = 10
)

// BenchmarkRender_CacheHit pre-fills the LRU with benchWarmLineCount
// entries (well under the 128 cap), then loops over those same indices.
// Every loop iteration is a guaranteed cache hit. Measures the pure
// LRU Get path.
func BenchmarkRender_CacheHit(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchWarmLineCount)

	// Warm the cache with the first benchWarmLineCount indices.
	for i := range benchWarmLineCount {
		_ = m.getRenderCacheEntry(i)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		for i := range benchWarmLineCount {
			_ = m.getRenderCacheEntry(i)
		}
	}
}

// BenchmarkRender_CacheMiss purges the LRU between every iteration so
// each call walks the full miss path: store.logs[i].Bytes() →
// tokenize → strings.Split → ruleEngine.scan → renderCache.Add.
// b.StopTimer/StartTimer exclude the Purge call itself from the timed
// region (D16). Each miss path includes one icl.Log.Bytes() call that
// allocates — this is intentional and matches the production path.
// Do NOT hoist Bytes() out of the loop.
func BenchmarkRender_CacheMiss(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchWarmLineCount)

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m.renderCache.Purge()
		b.StartTimer()
		for i := range benchWarmLineCount {
			_ = m.getRenderCacheEntry(i)
		}
	}
}

// runScrollBench drives the Cap128/Cap1024 hypothesis pair (D1, D21).
// It simulates a realistic scroll trace: benchScrollTraceIters render
// frames per b.Loop iteration, each advancing the cursor by
// benchScrollStep lines. The large step forces the LRU to evict cached
// entries every frame when the cap is small (128), but retains them
// when the cap is large enough to cover the advance window (1024).
// The two callers differ ONLY in cacheSize; all other parameters are
// identical so results are directly comparable.
//
// Render simulation: each frame calls getRenderCacheEntry for
// benchScrollViewport consecutive entries starting at the cursor log,
// mirroring the hot path inside placeLogs. CursorDown advances the
// cursor between frames. CursorToTop resets position at the start of
// each b.Loop iteration so the trace is deterministic.
func runScrollBench(b *testing.B, cacheSize int) {
	b.Helper()
	bundle := depstest.NewTest(b)
	bundle.Config.Logs.RenderCacheSize = cacheSize
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewerWithBundle(b, bundle, store, benchScrollViewport)

	b.ReportAllocs()
	for b.Loop() {
		// Reset to top so every iteration follows the same trace.
		b.StopTimer()
		// Drive the cursor for its side effect; the returned log index is
		// irrelevant to the render bench.
		m.CursorToTop()
		m.renderCache.Purge()
		b.StartTimer()

		for range benchScrollTraceIters {
			// Render the visible viewport: fetch benchScrollViewport
			// consecutive entries from the cursor's current log position.
			for j := range benchScrollViewport {
				idx := m.cursor.log + j
				_ = m.getRenderCacheEntry(idx)
			}
			// Advance the cursor by benchScrollStep lines to simulate
			// the user scrolling quickly through the corpus.
			m.CursorDown(benchScrollStep)
		}
	}
}

// BenchmarkRender_Scroll1k_Cap128 measures a realistic scroll trace
// against the F.1 baseline cache size (128). Used as the control arm
// of the D1 / D21 hypothesis comparison.
func BenchmarkRender_Scroll1k_Cap128(b *testing.B) { runScrollBench(b, 128) }

// benchExpandedColorRuleCount is the number of colour rules wired into the
// expanded-scroll bench. Enough to build a real AC automaton; the exact count
// is irrelevant to the allocation delta being measured.
const benchExpandedColorRuleCount = 8

// legacyBuildTokenizedLines is the pre-refactor render-cache entry builder,
// kept here (test-only) as the control arm of
// BenchmarkRenderCacheFill_ScrollExpanded. It copies the log's text once for
// the whole-buffer string, then AGAIN per line via []byte(line) —
// unconditionally, even with the colour-rule engine disabled — and the
// byte→column map used to copy it a third time inside uniseg.
// Production has exactly one implementation: buildTokenizedLines.
//
// It calls legacyByteToCol, not byteToCol, so the arm is a faithful control:
// the shipped byteToCol also dropped a copy, and folding that win into this
// arm's baseline would overstate the delta this bench attributes to the
// entry builder.
func legacyBuildTokenizedLines(b []byte, tokens [][]Token, engine *colorRuleEngine) []tokenizedLine {
	lines := strings.Split(string(b), "\n")
	entry := make([]tokenizedLine, len(lines))
	for i, line := range lines {
		var toks []Token
		if i < len(tokens) {
			toks = tokens[i]
		}
		lineBytes := []byte(line)
		var spans []matchSpan
		if engine.enabled {
			spans = engine.scan(lineBytes, legacyByteToCol(lineBytes))
		}
		entry[i] = tokenizedLine{text: line, tokens: toks, matchSpans: spans}
	}
	return entry
}

// legacyByteToCol is the pre-refactor byteToCol: ASCII fast path, then a
// uniseg.NewGraphemes walk that copies the line into a string first.
func legacyByteToCol(line []byte) colMap {
	if isASCII(line) {
		mapping := make(colMap, len(line)+1)
		for i := range mapping {
			mapping[i] = i
		}
		return mapping
	}
	return byteToColUnisegReference(line)
}

// legacyRenderCacheEntry mirrors getRenderCacheEntry exactly — same bounds
// guard, same LRU get/add — differing only in the entry builder, so the two
// bench arms are comparable one-for-one.
func legacyRenderCacheEntry(m *Model, index int) []tokenizedLine {
	if m.store == nil || index < 0 || index >= m.store.GetLogCount() {
		return nil
	}
	entry, ok := m.renderCache.Get(index)
	if !ok {
		b := m.store.logs[index].Bytes(m.store.state.expanded[index])
		entry = legacyBuildTokenizedLines(b, tokenize(b), m.ruleEngine)
		_ = m.renderCache.Add(index, entry)
	}
	return entry
}

// newBenchExpandedModel builds the heaviest realistic render-cache workload:
// every log expanded (indented, multi-line JSON) and the colour-rule engine
// enabled, so each cached line pays a byte→column map plus a rule scan.
func newBenchExpandedModel(tb testing.TB, n int) *Model {
	tb.Helper()
	bundle := depstest.NewTest(tb)
	bundle.Config.Logs.ExtraColorRules = newBenchColorRules(tb, benchExpandedColorRuleCount)
	bundle.Config.Logs.RenderCacheSize = 1024
	store := newBenchStore(tb, n)
	store.ToggleExpandAll()
	m := buildBenchLogviewerWithBundle(tb, bundle, store, benchScrollViewport)
	if !m.ruleEngine.enabled {
		tb.Fatal("bench setup: colour-rule engine must be enabled")
	}
	return m
}

// runExpandedScrollBench drives the same scroll trace as runScrollBench over a
// store of EXPANDED logs, filling the render cache through fill. The cache is
// purged per iteration so the trace pays real fills; both arms share the trace,
// the store and the LRU policy, so allocs/op differs only by the entry builder.
func runExpandedScrollBench(b *testing.B, m *Model, fill func(*Model, int) []tokenizedLine) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		m.CursorToTop()
		m.renderCache.Purge()
		b.StartTimer()

		for range benchScrollTraceIters {
			for j := range benchScrollViewport {
				_ = fill(m, m.cursor.log+j)
			}
			m.CursorDown(benchScrollStep)
		}
	}
}

// BenchmarkRenderCacheFill_ScrollExpanded is the audit-item-38 acceptance
// bench: a scroll trace over a store of expanded logs, filled the pre-refactor
// way vs the shipped way. Legacy copies each line's bytes twice more than
// needed; Shipped splits sub-slices of the single whole-log copy. The
// allocs/op delta is the point — ns/op follows it.
func BenchmarkRenderCacheFill_ScrollExpanded(b *testing.B) {
	b.Run("Legacy", func(b *testing.B) {
		m := newBenchExpandedModel(b, benchLogCount)
		runExpandedScrollBench(b, m, legacyRenderCacheEntry)
	})

	b.Run("Shipped", func(b *testing.B) {
		m := newBenchExpandedModel(b, benchLogCount)
		runExpandedScrollBench(b, m, (*Model).getRenderCacheEntry)
	})
}

// BenchmarkRender_Scroll1k_Cap1024 measures the same scroll trace with
// D1's proposed default (1024). F.2 lands the 1024 default only if
// this bench's ns/op is strictly lower OR its allocs/op is strictly
// lower than _Cap128's (non-overlapping CIs via benchstat). If both
// metrics are equivalent (~), the default should remain 128 and T4's
// value should be revised downward.
func BenchmarkRender_Scroll1k_Cap1024(b *testing.B) { runScrollBench(b, 1024) }
