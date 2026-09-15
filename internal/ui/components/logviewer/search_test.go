package logviewer

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkEntries builds a slice of entry values from message strings, mirroring
// how a LogStore holds logs. getWidth is nil (no wrapping) and each entry gets
// its own mutex so Bytes()'s read lock works.
func chunkEntries(msgs ...string) []entry {
	logs := make([]entry, len(msgs))
	for i, m := range msgs {
		logs[i] = newEntry(nil, &sync.RWMutex{}, icl.Log{Data: map[string]any{fieldMsg: m}})
	}
	return logs
}

// TestSearchChunk_ReturnsExpectedMsg verifies the pure searchChunk helper
// produces the same SearchChunkMsg the old closure factory did: global
// offsets are applied to the per-chunk results, and the instance/offset are
// echoed back. A pre-cancelled ctx makes searchChunk skip work and return
// ok=false.
func TestSearchChunk_ReturnsExpectedMsg(t *testing.T) {
	t.Parallel()
	const (
		instance  = "inst"
		batchSize = 4
	)
	ac := buildSearchAC("error")

	t.Run("non-cancelled ctx returns globally offset results", func(t *testing.T) {
		t.Parallel()
		// Two chunks worth of logs; search the second chunk (offset 1) so the
		// global-offset remapping (offset*batchSize) is exercised.
		logs := chunkEntries("error here", "no match", "another error", "clean")
		msg, ok := searchChunk(context.Background(), ac, instance, 1, batchSize, logs, indexMap{})
		require.True(t, ok)
		assert.Equal(t, instance, msg.Instance)
		assert.Equal(t, 1, msg.Offset)
		// Local indices 0 and 2 match "error" -> global 4 and 6 (offset 1 * 4).
		assert.Contains(t, msg.Results, 4)
		assert.Contains(t, msg.Results, 6)
		assert.NotContains(t, msg.Results, 5)
		assert.NotContains(t, msg.Results, 7)
	})

	t.Run("pre-cancelled ctx returns ok=false and no work", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		logs := chunkEntries("error here")
		msg, ok := searchChunk(ctx, ac, instance, 0, batchSize, logs, indexMap{})
		assert.False(t, ok)
		assert.Empty(t, msg.Results)
	})
}

// TestByteToCol_ASCII_MatchesUnisegPath asserts the ASCII fast path
// produces the same mapping as the uniseg-based reference path for pure-
// ASCII input. Generates 1000 random ASCII lines via the F.1 D2 seed
// (math/rand/v2 PCG, 42), compares byteToCol(b) element-by-element.
func TestByteToCol_ASCII_MatchesUnisegPath(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for reproducible test vectors
	for i := range 1000 {
		n := r.IntN(256) + 1
		line := make([]byte, n)
		for j := range line {
			line[j] = byte(0x20 + r.IntN(0x5F)) // printable ASCII range
		}
		got := byteToCol(line)
		want := byteToColUnisegReference(line)
		require.Len(t, got, len(want), "line %d", i)
		assert.Equal(t, []int(want), []int(got), "line %d body=%q", i, string(line))
	}
}

// TestByteToCol_NonASCII_MatchesUnisegPath pins the in-place
// FirstGraphemeCluster walk to the uniseg.NewGraphemes reference it replaced:
// same cluster boundaries, same widths, same mapping — for wide CJK, emoji
// (including a ZWJ sequence and variation selectors), combining marks and
// mixed ASCII, at every byte offset including the len(line) tail.
func TestByteToCol_NonASCII_MatchesUnisegPath(t *testing.T) {
	t.Parallel()
	lines := []string{
		"你好世界",
		"🌟abc你好XYZ",
		"école",                // combining acute
		"👩‍👩‍👧‍👦 family",        // ZWJ sequence
		"❤️ and ❤︎",             // emoji vs text variation selector
		"a🌟b́c你d",               // dense mix
		"{\"msg\":\"héllo 🌍\"}", // JSON-shaped, the real render-cache input
		"\u200b\u0301",          // zero-width space + combining acute, leading cluster
	}
	for _, line := range lines {
		b := []byte(line)
		got := byteToCol(b)
		want := byteToColUnisegReference(b)
		require.Len(t, got, len(want), "line %q", line)
		assert.Equal(t, []int(want), []int(got), "line %q", line)
	}
}

// byteToColUnisegReference is the uniseg-only implementation kept for
// the cross-check above. Mirrors the pre-D3 byteToCol body.
func byteToColUnisegReference(line []byte) colMap {
	mapping := make(colMap, len(line)+1)
	col := 0
	gr := uniseg.NewGraphemes(string(line))
	for gr.Next() {
		from, to := gr.Positions()
		w := gr.Width()
		for b := from; b < to; b++ {
			mapping[b] = col
		}
		col += w
	}
	mapping[len(line)] = col
	return mapping
}

// TestSearchMap_MatchIndex_SkipsHidden verifies MatchIndex does not advance the
// global index for matches on hidden logs, so the reported position is over the
// filtered-in population (filter-aware counter). The total it is reported
// against is FilteredCount, verified separately.
func TestSearchMap_MatchIndex_SkipsHidden(t *testing.T) {
	t.Parallel()
	sm := SearchMap{
		0: {{line: 0, start: 0, end: 3}},
		1: {{line: 0, start: 0, end: 3}},
		2: {{line: 0, start: 0, end: 3}},
	}
	hidden := indexMap{1: true} // log 1 is filtered out

	assert.Equal(t, 2, sm.FilteredCount(hidden), "hidden log 1's match is excluded from the total")

	// Global index of a match on log 2 should be 1 (log 1 skipped), not 2.
	assert.Equal(t, 1, sm.MatchIndex(2, 0, hidden), "hidden logs do not advance the global match index")
	assert.Equal(t, 0, sm.MatchIndex(0, 0, hidden), "the first filtered-in log is index 0")
	assert.Equal(t, 0, sm.MatchIndex(1, 0, hidden), "a hidden log reports 0, as before")
	assert.Equal(t, 0, sm.MatchIndex(9, 0, hidden), "a log with no matches reports 0, as before")
}

// TestSearchMap_MatchIndex_NilHidden_CountsAll verifies a nil hidden map filters
// nothing.
func TestSearchMap_MatchIndex_NilHidden_CountsAll(t *testing.T) {
	t.Parallel()
	sm := SearchMap{0: {{}}, 1: {{}}, 2: {{}}}
	assert.Equal(t, 3, sm.FilteredCount(nil))
	assert.Equal(t, 2, sm.MatchIndex(2, 0, nil))
	assert.Equal(t, 3, sm.MatchIndex(2, 1, nil), "matchIdx offsets within the log")
}

// TestColMap_MapByte_GuardsOutOfRange verifies the safe accessor returns
// identity past the mapping's end — matches pre-F.2 open-coded guard behavior.
func TestColMap_MapByte_GuardsOutOfRange(t *testing.T) {
	t.Parallel()
	cm := colMap{0, 1, 2, 3}
	assert.Equal(t, 0, cm.MapByte(0))
	assert.Equal(t, 3, cm.MapByte(3))
	assert.Equal(t, 7, cm.MapByte(7)) // past end → identity
}

// TestBuildSearchAC_SingleNeedle verifies the whole query is one literal needle:
// a query containing the old delimiter (two spaces) is NOT split into terms.
func TestBuildSearchAC_SingleNeedle(t *testing.T) {
	t.Parallel()
	ac := buildSearchAC("foo  bar") // would have split on "  " under the old API
	sm := search(ac, [][]byte{[]byte("foo  bar baz")})
	require.Len(t, sm[0], 1, "the whole query matches as one needle, not two terms")
	assert.Equal(t, 0, sm[0][0].start)
	assert.Equal(t, len("foo  bar"), sm[0][0].end)
}
