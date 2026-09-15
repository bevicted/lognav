package logviewer

import (
	"strings"
	"testing"
)

// benchRuleCount is the number of synthetic rules loaded into the engine.
// Matches the modulus used by randomBenchPayload (`lvl<i mod 20>`), so the
// engine collectively matches ~10% of lines via 1-in-20 patterns.
const benchRuleCount = 20

// BenchmarkColorRule_ScanNoRules is a tripwire for the disabled-engine
// fast path: newColorRuleEngine(nil) yields an engine with enabled=false,
// and scan() returns nil at the top check without touching cm or
// allocating. If this bench ever reports non-zero allocs or >10 ns/op,
// someone has removed or perturbed the early-return.
//
// cm setup is OUTSIDE the b.Loop() timed region; the fast path never
// reads it.
func BenchmarkColorRule_ScanNoRules(b *testing.B) {
	e := newColorRuleEngine(nil)
	line := []byte(strings.Repeat("a", 200))
	cm := make(colMap, len(line)+1)
	for i := range cm {
		cm[i] = i
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = e.scan(line, cm)
	}
}

// BenchmarkColorRule_Scan measures per-line scan cost over an engine
// loaded with benchRuleCount synthetic rules. The engine is built
// directly via newColorRuleEngine (per-Model field since the sync.Once
// global was removed). Pre-converts entry bytes outside the timed
// region — the conversion is not what we're measuring.
func BenchmarkColorRule_Scan(b *testing.B) {
	rules := newBenchColorRules(b, benchRuleCount)
	engine := newColorRuleEngine(rules)

	store := newBenchStore(b, benchLogCount)
	bytesPerLog := make([][]byte, benchLogCount)
	for i := range benchLogCount {
		bytesPerLog[i] = store.logs[i].Bytes(false)
	}

	b.ReportAllocs()
	for b.Loop() {
		for _, line := range bytesPerLog {
			cm := byteToCol(line)
			_ = engine.scan(line, cm)
		}
	}
}
