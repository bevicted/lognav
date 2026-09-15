package logviewer

import (
	"slices"

	"github.com/bevicted/lognav/internal/filter"
	"github.com/itchyny/gojq"
	ahocorasick "github.com/petar-dambovaliev/aho-corasick"
)

// epochCache holds the per-epoch compiled artifacts that every chunk worker of a
// dispatch epoch shares: the parsed jq query, the compiled filter rules and the
// search Aho-Corasick automaton. Without it a long stream rebuilds each of them
// once per BatchOpChunkSize-row batch — hundreds of times over a 1M-row ingest,
// all on the loop goroutine, inside HandleLogStreamMsg.
//
// It lives alongside the existing per-epoch contexts (jqCtx/searchCtx/filterCtx)
// on the LogStore and, like them, is loop-goroutine-only state: it is read and
// written exclusively from the on-loop dispatch/apply paths, never from a worker.
//
// INVALIDATION is by key, not by explicit clearing: every entry stores the exact
// input it was built from and a miss rebuilds. jq and search key on their query
// text; the compiled filter rules key on the rule set AND s.filterEpoch, the
// counter that ApplyJQ/ApplyFilters/ClearData bump whenever a filter worker's
// inputs are superseded. A stale artifact therefore cannot be served: either the
// key matches — in which case the artifact is by construction the one the caller
// would have built — or it is rebuilt.
//
// SHARING: a served artifact is handed to off-loop chunk workers that run
// concurrently, so it must be read-only and goroutine-safe after construction.
//   - *gojq.Query and *gojq.Code: gojq documents both as safe to reuse across
//     goroutines (query.go "It is safe to call this method in goroutines, to
//     reuse a parsed [*Query]"; compiler.go, same for [*Code]). Its caveat is
//     about the INPUT values, and each worker passes its own private per-log
//     copy (jqSingle clones before handing data to gojq).
//   - ahocorasick.AhoCorasick: already shared across chunk workers today; see
//     buildSearchAC's godoc for the per-version verification.
//   - []compiledRule: a slice of value structs holding a filter.Type, a
//     *gojq.Code and a needle []byte, none of which is written after
//     compileRules returns. hideEntry only reads them.
type epochCache struct {
	jqSrc   string
	jqQuery *gojq.Query
	jqErr   error
	jqOK    bool

	filterRules    []filter.Rule
	filterEpoch    uint64
	filterCompiled []compiledRule
	filterOK       bool

	searchSrc string
	searchAC  ahocorasick.AhoCorasick
	searchOK  bool

	// Build counters, incremented on a MISS only. They are instrumentation, not
	// state: nothing reads them to make a decision, and the per-epoch-compile
	// guarantee is asserted against them (see TestEpochCache_* in
	// epochcache_test.go) rather than by inspecting the code.
	jqParses       int
	filterCompiles int
	searchBuilds   int
}

// parsedJQ returns the parsed form of src, parsing it only when src differs from
// the cached source. The parse OUTCOME is cached, errors included, so a query
// that cannot parse is not re-parsed once per batch either.
//
// Parse-failure recovery belongs to the caller and deliberately differs between
// them: ApplyJQ clears every entry's jq output, while the batch-dispatch path
// tears down the streaming jq epoch and leaves already-rendered entries alone.
func (s *LogStore) parsedJQ(src string) (*gojq.Query, error) {
	if s.epoch.jqOK && s.epoch.jqSrc == src {
		return s.epoch.jqQuery, s.epoch.jqErr
	}
	q, err := gojq.Parse(src)
	s.epoch.jqSrc, s.epoch.jqQuery, s.epoch.jqErr, s.epoch.jqOK = src, q, err, true
	s.epoch.jqParses++
	return q, err
}

// compiledFilterRules returns the compiled form of rules for the current filter
// epoch, recompiling only when the rule set or s.filterEpoch changed. Callers
// must not mutate the returned slice: it is shared with in-flight chunk workers.
func (s *LogStore) compiledFilterRules(rules []filter.Rule) []compiledRule {
	if s.epoch.filterOK && s.epoch.filterEpoch == s.filterEpoch && slices.Equal(s.epoch.filterRules, rules) {
		return s.epoch.filterCompiled
	}
	crules := s.compileRules(rules)
	s.epoch.filterRules = slices.Clone(rules)
	s.epoch.filterEpoch = s.filterEpoch
	s.epoch.filterCompiled = crules
	s.epoch.filterOK = true
	s.epoch.filterCompiles++
	return crules
}

// searchAutomaton returns the Aho-Corasick automaton for query, building it only
// when the query text changed. The empty query is a valid, cacheable input (see
// buildSearchAC: it yields the newline-only pattern set).
func (s *LogStore) searchAutomaton(query string) ahocorasick.AhoCorasick {
	if s.epoch.searchOK && s.epoch.searchSrc == query {
		return s.epoch.searchAC
	}
	ac := buildSearchAC(query)
	s.epoch.searchSrc, s.epoch.searchAC, s.epoch.searchOK = query, ac, true
	s.epoch.searchBuilds++
	return ac
}
