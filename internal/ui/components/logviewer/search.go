package logviewer

import (
	"bytes"
	"context"

	ahocorasick "github.com/petar-dambovaliev/aho-corasick"
	"github.com/rivo/uniseg"
)

// colMap is a byte-offset → display-column mapping for a single rendered
// line. Length is len(line)+1 by construction.
type colMap []int

// MapByte returns the display column at byte offset off, or off itself
// when off is at or past the mapping's tail (callers treat the mapping
// as identity past EOF — matches the pre-F.2 open-coded guard behavior).
func (c colMap) MapByte(off int) int {
	if off < len(c) {
		return c[off]
	}
	return off
}

// isASCII reports whether b contains only 7-bit ASCII bytes.
func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

type SearchMatch struct {
	line  int
	start int
	end   int
}

type SearchMap map[int][]SearchMatch

type SearchChunkMsg struct {
	Instance string
	// ID is the LogStore queryID captured at spawn time. HandleSearchChunk
	// rejects any chunk whose ID no longer matches the store's queryID, so a
	// chunk that finished against pre-Clear data cannot write stale indices.
	ID      uint64
	Offset  int
	Results SearchMap
	Err     error
}

// FilteredCount returns the total number of search matches over logs that are
// NOT filtered out (hidden), i.e. the filter-aware match total the bottom bar
// shows. A nil/empty filtered map counts every match.
func (sm SearchMap) FilteredCount(filtered indexMap) int {
	var n int
	for logIdx, matches := range sm {
		if filtered[logIdx] {
			continue
		}
		n += len(matches)
	}
	return n
}

// MatchIndex returns the zero-based global index of the match at
// sm[logIdx][matchIdx], counting only logs hidden does NOT reject (a nil map
// hides nothing). The index advances by one per matching, filtered-in log below
// logIdx — matching logs, not individual matches — then adds matchIdx; that is
// the number the bottom bar has always reported, so it is reproduced as-is.
// logIdx not present in sm, or hidden, yields 0.
//
// One pass over the keys, no allocation: ordering is not needed because only
// the count of keys below logIdx matters, never their sequence.
func (sm SearchMap) MatchIndex(logIdx, matchIdx int, hidden indexMap) int {
	if _, ok := sm[logIdx]; !ok || hidden[logIdx] {
		return 0
	}
	n := 0
	for k := range sm {
		if k < logIdx && !hidden[k] {
			n++
		}
	}
	return n + matchIdx
}

// byteToCol builds a byte-offset to display-column mapping for a line.
// ASCII fast path: skip uniseg entirely when no byte >= 0x80, because for
// pure ASCII column == byte offset.
//
// The non-ASCII path walks line in place with uniseg.FirstGraphemeCluster
// rather than uniseg.NewGraphemes, which takes a string and would force a
// copy of the whole line on every call. Cluster boundaries and widths are
// identical between the two — Graphemes.Next/Width is StepString driving the
// same state machine (verified against uniseg v0.4.7, and by
// TestByteToCol_MatchesUnisegPath).
func byteToCol(line []byte) colMap {
	n := len(line)
	mapping := make(colMap, n+1)
	if isASCII(line) {
		for i := range mapping {
			mapping[i] = i
		}
		return mapping
	}
	var (
		col     int
		off     int
		cluster []byte
		w       int
		state   = -1
	)
	for rest := line; len(rest) > 0; {
		cluster, rest, w, state = uniseg.FirstGraphemeCluster(rest, state)
		for b := off; b < off+len(cluster); b++ {
			mapping[b] = col
		}
		off += len(cluster)
		col += w
	}
	mapping[n] = col
	return mapping
}

// buildSearchAC builds the AC automaton used by searchChunk for one
// query dispatch. The whole query string is used as a single literal needle
// (no delimiter splitting). The pattern list is prefixed by "\n" so search()
// can compute per-line offsets within multi-line records. An empty query
// produces no search needle (only the "\n" offset marker is registered).
//
// The returned AhoCorasick value is safe to share across goroutines:
// iterators (IterByte) own their per-call state. Verified against
// petar-dambovaliev/aho-corasick at the version pinned in go.mod:
//   - ahocorasick.go: IterByte allocates a fresh prefilterState per call;
//     value receiver on AhoCorasick.
//   - dfa.go / nfa.go: state-transition methods are pure reads.
//   - prefilter.go: rareBytesOne and friends mutate only the
//     caller-supplied *prefilterState, never receiver state.
//
// Re-verify on any library bump (see TestBuildSearchAC_ConcurrentIterByte_NoDataRace).
func buildSearchAC(query string) ahocorasick.AhoCorasick {
	patterns := []string{"\n"}
	if query != "" {
		patterns = append(patterns, query)
	}
	b := ahocorasick.NewAhoCorasickBuilder(ahocorasick.Opts{})
	return b.Build(patterns)
}

// search runs the pre-built AC automaton over bmatrix, returning a
// SearchMap of per-log per-line match spans. ac MUST be built via
// buildSearchAC (patterns prefixed with "\n" for line-offset tracking).
func search(ac ahocorasick.AhoCorasick, bmatrix [][]byte) SearchMap {
	results := make(SearchMap)

	for idx, b := range bmatrix {
		var (
			line        int
			lastNewline int
		)
		acIter := ac.IterByte(b)
		for match := acIter.Next(); match != nil; match = acIter.Next() {
			if match.Pattern() == 0 {
				line++
				lastNewline = match.End()
				continue
			}
			results[idx] = append(results[idx], SearchMatch{
				line:  line,
				start: match.Start() - lastNewline,
				end:   match.End() - lastNewline,
			})
		}

		// Convert byte offsets to column positions
		if matches, ok := results[idx]; ok && len(matches) > 0 {
			lines := bytes.Split(b, []byte{'\n'})
			colMaps := make([]colMap, len(lines)) // nil until needed
			for i := range matches {
				m := &matches[i]
				if m.line < len(lines) {
					if colMaps[m.line] == nil {
						colMaps[m.line] = byteToCol(lines[m.line])
					}
					cm := colMaps[m.line]
					m.start = cm.MapByte(m.start)
					m.end = cm.MapByte(m.end)
				}
			}
		}
	}

	return results
}

// searchChunk runs search() over one chunk and returns the SearchChunkMsg.
// ok is false if ctx is already cancelled (caller skips the post). The AC
// automaton is built once per dispatch by the caller and shared across chunk
// goroutines (safe per buildSearchAC).
func searchChunk(ctx context.Context, ac ahocorasick.AhoCorasick, instance string, offset, batchSize int, logs []entry, expanded indexMap) (SearchChunkMsg, bool) {
	select {
	case <-ctx.Done():
		return SearchChunkMsg{}, false
	default:
	}
	globalOffset := offset * batchSize
	bmatrix := make([][]byte, len(logs))
	for i, l := range logs {
		bmatrix[i] = l.Bytes(expanded[globalOffset+i])
	}
	results := search(ac, bmatrix)
	global := make(SearchMap, len(results))
	for k, v := range results {
		global[globalOffset+k] = v
	}
	return SearchChunkMsg{Instance: instance, Offset: offset, Results: global}, true
}
