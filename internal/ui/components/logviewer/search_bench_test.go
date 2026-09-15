package logviewer

import (
	"fmt"
	"strings"
	"testing"

	ahocorasick "github.com/petar-dambovaliev/aho-corasick"
)

// benchDispatchChunkSize matches Logs.BatchOpChunkSize default.
const benchDispatchChunkSize = 2000

// benchSearchTermCount is the number of patterns used by both AC benches.
// Shared between BenchmarkSearch_ACBuild and BenchmarkSearch_Match so the
// build cost is directly comparable to the match cost (per spec §6).
const benchSearchTermCount = 10

// benchSearchTerms returns a deterministic slice of raw search terms
// (without the leading "\n" delimiter used by buildSearchAC internally).
// Used by BenchmarkSearch_Dispatch_Amortized to build a "|"-joined query
// string matching the production call shape.
func benchSearchTerms() []string {
	terms := make([]string, benchSearchTermCount)
	for i := range benchSearchTermCount {
		terms[i] = fmt.Sprintf("lvl%d", i)
	}
	return terms
}

// benchSearchPatterns returns the production pattern shape: a leading "\n"
// (used by search() as the line delimiter — see search.go:85's
// `append([]string{"\n"}, patterns...)`) followed by benchSearchTermCount
// terms. Matches the fixture used by both search benches so the
// build/match costs reflect realistic production input.
func benchSearchPatterns() []string {
	patterns := make([]string, 0, benchSearchTermCount+1)
	patterns = append(patterns, "\n")
	for i := range benchSearchTermCount {
		patterns = append(patterns, fmt.Sprintf("lvl%d", i))
	}
	return patterns
}

// BenchmarkSearch_ACBuild measures the Aho-Corasick automaton build cost
// for benchSearchTermCount+1 patterns. Historically this cost was paid once
// per chunk; F.2 reduced it to one build per dispatch (the automaton is now
// built by the caller and shared across chunk workers via searchChunk). This
// bench quantifies the per-build budget.
func BenchmarkSearch_ACBuild(b *testing.B) {
	patterns := benchSearchPatterns()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		// builder must be a named variable: Build is a pointer-receiver
		// method and cannot be called on the non-addressable return
		// value of NewAhoCorasickBuilder.
		builder := ahocorasick.NewAhoCorasickBuilder(ahocorasick.Opts{})
		_ = builder.Build(patterns)
	}
}

// BenchmarkSearch_Match measures the match phase over the full
// benchLogCount corpus. The automaton is built outside the timed region
// so the bench isolates the match-iteration cost. Each log's bytes are
// pre-fetched via store.logs[i].Bytes(false).
func BenchmarkSearch_Match(b *testing.B) {
	patterns := benchSearchPatterns()
	builder := ahocorasick.NewAhoCorasickBuilder(ahocorasick.Opts{})
	ac := builder.Build(patterns)

	store := newBenchStore(b, benchLogCount)
	bytesPerLog := make([][]byte, benchLogCount)
	for i := range benchLogCount {
		bytesPerLog[i] = store.logs[i].Bytes(false)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		for _, raw := range bytesPerLog {
			iter := ac.IterByte(raw)
			for m := iter.Next(); m != nil; m = iter.Next() {
				_ = m
			}
		}
	}
}

// BenchmarkSearch_Dispatch_Amortized measures the post-D2 dispatch shape:
// build AC once via buildSearchAC, then run search() over the full corpus
// split into chunks of benchDispatchChunkSize. Sequential, single
// goroutine — the timed region is the AC build cost amortized over
// chunk_count search() calls (D2 reuse win). Concurrency safety of the
// shared `ac` is tested by TestBuildSearchAC_ConcurrentIterByte_NoDataRace
// (D23), not by this bench.
func BenchmarkSearch_Dispatch_Amortized(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	bytesPerLog := make([][]byte, benchLogCount)
	for i := range benchLogCount {
		bytesPerLog[i] = store.logs[i].Bytes(false)
	}
	query := strings.Join(benchSearchTerms(), "|")
	b.ReportAllocs()
	for b.Loop() {
		ac := buildSearchAC(query)
		for off := 0; off < benchLogCount; off += benchDispatchChunkSize {
			end := min(off+benchDispatchChunkSize, benchLogCount)
			_ = search(ac, bytesPerLog[off:end])
		}
	}
}
