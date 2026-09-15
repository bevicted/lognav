package logviewer

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"testing"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
)

// Synthetic fixture corpus for hot-path benches. All builders take
// testing.TB and call tb.Helper() so b.Fatalf reports at the caller.
// Seed is fixed via math/rand/v2 PCG; outputs deterministic per run.

const (
	benchFixtureSeed1 uint64 = 42
	benchFixtureSeed2 uint64 = 0
)

// newBenchLogs builds n synthetic icl.Log records with mixed severity,
// JSON shapes (flat key/value + nested objects), and deterministic
// timestamps. Used by render/tokenize/colorrule/search/colmap benches.
func newBenchLogs(tb testing.TB, n int) []icl.Log {
	tb.Helper()
	r := rand.New(rand.NewPCG(benchFixtureSeed1, benchFixtureSeed2))
	logs := make([]icl.Log, n)
	for i := range logs {
		logs[i] = icl.Log{
			Data:     randomBenchPayload(r, i),
			Metadata: icl.Metadata{Severity: randomBenchSeverity(r), TSMicro: int64(1_000_000 + i)},
		}
	}
	return logs
}

// newBenchStore builds a primed LogStore around newBenchLogs(tb, n).
// The second NewLogStore argument (nil) is the optional getWidth func;
// nil disables width-driven wrap clamping (per NewLogStore's godoc).
// SetLogs runs for effect; its chunk workers post via the poster (nil here),
// so the bench context has no event loop to dispatch them.
func newBenchStore(tb testing.TB, n int) *LogStore {
	tb.Helper()
	s := NewLogStore(depstest.NewTest(tb), nil /* no width clamping */, "bench")
	s.SetLogs(newBenchLogs(tb, n))
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Logf("bench LogStore.Close: %v", err)
		}
	})
	return s
}

// newBenchColorRules builds k synthetic ColorRule entries with disjoint
// literal Match strings chosen to match ~10% of generated log lines.
// Match `lvl<i>` is unlikely to collide with the JSON-shape vocabulary
// used by randomBenchPayload. Returns a config.ColorRuleList slice
// ready to be passed to a colour-rule engine in T7; the list is not
// registered with any engine here. Foreground colour is set so the
// rule survives the engine's later filter pass.
func newBenchColorRules(tb testing.TB, k int) config.ColorRuleList {
	tb.Helper()
	rules := make(config.ColorRuleList, k)
	for i := range rules {
		rules[i] = config.ColorRule{
			Match: fmt.Sprintf("lvl%d", i),
			Fg:    config.ConfigColor{Color: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}},
		}
	}
	return rules
}

// buildBenchLogviewer constructs a Model wired to the given store and
// sized to a viewport of `height` rows. Used by render benches to pin
// the warm window (D16). SetStore runs for effect; the SetRect cmd is
// intentionally dropped — bench context has no event loop to dispatch it.
func buildBenchLogviewer(tb testing.TB, store *LogStore, height int) *Model {
	tb.Helper()
	bundle := depstest.NewTest(tb)
	m := New(bundle)
	m.SetRect(component.Rect{W: 120, H: height})
	m.SetStore(store)
	return m
}

// buildBenchLogviewerWithBundle constructs a Model using the provided
// bundle, wired to the given store and sized to a viewport of `height`
// rows. Used by benches that must control bundle fields (e.g.
// RenderCacheSize) before construction. The caller must set any desired
// bundle overrides before calling this helper.
func buildBenchLogviewerWithBundle(tb testing.TB, bundle deps.Bundle, store *LogStore, height int) *Model {
	tb.Helper()
	m := New(bundle)
	m.SetRect(component.Rect{W: 120, H: height})
	m.SetStore(store)
	return m
}

// randomBenchPayload returns a deterministic JSON-shaped map[string]any
// approximating a real log record. Mix of flat + nested keys; ~10% of
// records contain "lvl<i mod 20>" to interact with newBenchColorRules.
func randomBenchPayload(r *rand.Rand, i int) map[string]any {
	out := map[string]any{
		"msg":  fmt.Sprintf("event %d at offset %d", i, r.IntN(1<<16)),
		"code": r.IntN(500),
		"id":   fmt.Sprintf("req-%08d", i),
	}
	if i%10 == 0 {
		out["tag"] = fmt.Sprintf("lvl%d", i%20)
	}
	if i%5 == 0 {
		out["meta"] = map[string]any{
			"trace": fmt.Sprintf("trace-%d", r.IntN(1<<20)),
			"span":  fmt.Sprintf("span-%d", r.IntN(1<<10)),
		}
	}
	return out
}

// randomBenchSeverity returns one of the 6 named severity values
// (omitting SeverityUnknown which is a "no data" sentinel) with a stable
// weighted distribution chosen to look like real log traffic.
func randomBenchSeverity(r *rand.Rand) icl.Severity {
	n := r.IntN(100)
	switch {
	case n < 2:
		return icl.SeverityCritical
	case n < 10:
		return icl.SeverityError
	case n < 25:
		return icl.SeverityWarning
	case n < 80:
		return icl.SeverityInfo
	case n < 92:
		return icl.SeverityDebug
	default:
		return icl.SeverityVerbose
	}
}
