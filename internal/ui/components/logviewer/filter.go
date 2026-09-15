package logviewer

import (
	"bytes"
	"context"
	"slices"
	"time"

	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/itchyny/gojq"
)

// FilterChunkMsg carries one filter chunk's per-log hide decisions back through
// the loop. Hidden[i] is true when the log at (Offset*BatchOpChunkSize + i)
// should be hidden. ID is the LogStore queryID captured at spawn time;
// HandleFilterChunk drops any chunk whose ID no longer matches the store's
// queryID — a chunk that finished computing against pre-Clear data must not
// index the emptied logs (the documented jqchunk stale-chunk panic class).
//
// Epoch is the LogStore filterEpoch captured at spawn time and is the
// stale-worker guard for jq/filter CHANGES that queryID alone cannot catch: a
// jq re-run (ApplyJQ) or a full filter recompute (ApplyFilters) bumps
// filterEpoch WITHOUT bumping queryID, so a worker that computed against the
// superseded jq output / rule set carries a stale Epoch and is dropped by
// HandleFilterChunk.
type FilterChunkMsg struct {
	Instance string
	ID       uint64
	Epoch    uint64
	Offset   int
	Hidden   []bool
	Err      error
}

// compileFieldRule parses then compiles a field rule's jq expression, surfacing
// both parse and compile errors (compile catches "no such function" / unknown
// vars that otherwise leak per-log). The compiled *gojq.Code is reused across
// every log in one Apply pass.
func compileFieldRule(expr string) (*gojq.Code, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, err
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, err
	}
	return code, nil
}

// hideForFieldRule reports whether one log (its raw data map) should be hidden
// under a field rule. It runs code against data, takes the FIRST result, and
// applies isEmpty (logstore semantics): HasField keeps non-empty (hide when
// empty); LacksField keeps empty (hide when non-empty). A nil/absent path and
// any jq error both read as empty.
func hideForFieldRule(ctx context.Context, code *gojq.Code, t filter.Type, data map[string]any) bool {
	empty := true
	iter := code.RunWithContext(ctx, data)
	if v, ok := iter.Next(); ok {
		if _, isErr := v.(error); !isErr {
			empty = isEmpty(v)
		}
	}
	switch t {
	case filter.HasField:
		return empty // keep non-empty -> hide when empty
	case filter.LacksField:
		return !empty // keep empty -> hide when non-empty
	default:
		return false
	}
}

// hideForTermRule reports whether one log's byte corpus should be hidden under a
// term rule. Include keeps matching (hide when no match); Exclude drops matching
// (hide when match). A single literal needle is a plain substring search:
// bytes.Contains (SIMD/memchr + Rabin-Karp) is dramatically faster and
// allocation-free versus a single-pattern Aho-Corasick automaton, which only
// pays off across many simultaneous patterns. An empty needle matches nothing,
// preserving the prior single-needle AC behavior (an empty filter term never
// matched the corpus).
func hideForTermRule(needle []byte, t filter.Type, bs []byte) bool {
	matched := len(needle) > 0 && bytes.Contains(bs, needle)
	switch t {
	case filter.Include:
		return !matched // keep matching -> hide when no match
	case filter.Exclude:
		return matched // drop matching -> hide when match
	default:
		return false
	}
}

// compiledRule pairs a rule with its evaluation state. Exactly one of code/needle
// is set per rule kind; term rules carry a needle, field rules a *gojq.Code. skip
// is true when a field rule failed to compile (it is treated as a no-op so a
// broken rule never hides everything).
type compiledRule struct {
	t      filter.Type
	code   *gojq.Code
	needle []byte // set for term rules; empty matches nothing
	term   bool
	skip   bool
}

// compileRules compiles a rule set once for a recompute pass. Field rules
// surface compile errors via skip+log; term rules store the literal needle.
// Unknown-type rules are dropped (the trailing placeholder is never applied).
func (s *LogStore) compileRules(rules []filter.Rule) []compiledRule {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		switch r.Type {
		case filter.Include, filter.Exclude:
			out = append(out, compiledRule{t: r.Type, needle: []byte(r.Value), term: true})
		case filter.HasField, filter.LacksField:
			code, err := compileFieldRule(r.Value)
			if err != nil {
				s.logger.Error("filter field rule compile failed; skipping rule",
					"value", r.Value, logging.KeyError, err)
				out = append(out, compiledRule{t: r.Type, skip: true})
				continue
			}
			out = append(out, compiledRule{t: r.Type, code: code})
		default:
			// filter.Unknown / placeholder: never applied.
		}
	}
	return out
}

// ApplyFilters rebuilds s.state.filtered from scratch over all loaded logs by
// AND-ing every rule (mirrors DoSearch/ApplyJQ). Empty rule set => nothing
// hidden. Runs synchronously on the loop goroutine (single writer); field-rule
// jq is bounded by JqTimeoutMs. The streaming/async path is dispatchPendingBatches.
func (s *LogStore) ApplyFilters() {
	rules := s.bundle.State.Filters()
	// The full sync recompute supersedes any in-flight async filter chunk from a
	// prior rule set: bump filterEpoch so a late worker's chunk is dropped by
	// HandleFilterChunk's epoch guard (queryID is unchanged here).
	s.filterEpoch++
	s.state.filtered = make(indexMap)
	s.appliedFilters = rules
	count := int(s.logCount.Load())
	if len(rules) == 0 || count == 0 {
		s.markFilteredTotalDirty()
		return
	}
	// Served from the per-epoch cache. The filterEpoch++ above is part of the
	// cache key, so this recompute is guaranteed to be a miss (one compile per
	// epoch) and the streaming chunk dispatch that follows reuses this result.
	crules := s.compiledFilterRules(rules)
	if len(crules) == 0 {
		s.markFilteredTotalDirty()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Millisecond*time.Duration(s.bundle.Config.Logs.JqTimeoutMs))
	defer cancel()
	for i := range s.logs[:count] {
		s.state.filtered.Set(i, hideEntry(ctx, crules, &s.logs[i], s.state.expanded[i]))
	}
	s.markFilteredTotalDirty()
	s.logger.Debug("filters applied", logging.KeyCount, len(s.state.filtered), "rules", len(rules))
}

// hideEntry AND-evaluates the compiled rules against a single entry: raw e.data
// for field rules (immutable, lockless read) and the byte corpus via e.Bytes for
// term rules (e.Bytes RLocks the entry's own mutex per call). Hidden iff ANY rule
// says hide (pure AND). Shared by ApplyFilters (sync) and spawnFilterChunk (async).
func hideEntry(ctx context.Context, crules []compiledRule, e *entry, expanded bool) bool {
	var bs []byte
	for i := range crules {
		cr := &crules[i]
		if cr.skip {
			continue
		}
		if cr.term {
			if bs == nil {
				bs = e.Bytes(expanded)
			}
			if hideForTermRule(cr.needle, cr.t, bs) {
				return true
			}
			continue
		}
		if hideForFieldRule(ctx, cr.code, cr.t, e.data) {
			return true
		}
	}
	return false
}

// spawnFilterChunk takes the rule set compiled for the current filter epoch and
// spawns an off-loop worker that computes per-log hide decisions over batchLogs,
// posting a FilterChunkMsg back through the loop. Returns false (no worker
// spawned) when a field rule fails to compile or the rule set yields nothing
// applicable; the caller skips that batch.
//
// The compiled rules come from the per-epoch cache, so a long stream compiles
// them once per epoch rather than once per batch. The SAME []compiledRule is
// therefore shared by every concurrently running chunk worker of that epoch —
// safe because it is read-only after compilation and *gojq.Code is documented
// goroutine-safe (see epochCache).
//
// Mirrors spawnJQ: qid (and epoch) are captured BEFORE the closure (so a
// later ClearData queryID++ / ApplyJQ filterEpoch++ does not change the stamp),
// and the per-chunk worker derives its compute ctx from the filterCtx epoch with
// a JqTimeoutMs timeout.
//
// RACE SAFETY — the worker reads ISOLATED COPIES, never the live s.logs. We
// slices.Clone(batchLogs) ON THE LOOP before the poster.Go; the clone is a
// shallow copy of the entry structs into a fresh backing array, so the worker
// reads `batch`, not s.logs. HandleJQChunk overwrites WHOLE entry values on the
// loop goroutine, lockless (s.logs[logIdx] = s.logs[logIdx].WithData(...),
// logstore.go) — that write replaces elements in s.logs' backing array and never
// touches the clone's memory. The shallow copy is correct because entry data is
// immutable-or-replaced, never mutated in place: field rules read entry.data
// (newEntry-assigned, "Should never be changed" — see entry.go) and WithData
// returns a NEW entry rather than mutating; term rules read the byte corpus via
// entry.Bytes (GetData RLocks the entry's own mutex). Cloning, not aliasing, is
// what makes this worker -race clean against an interleaved ApplyJQ/HandleJQChunk
// write (cooperative cancellation alone cannot deterministically stop the worker
// mid-read before that write). It also means we do NOT take s.mutexes[localIdx]:
// holding it AND calling e.Bytes — which RLocks the SAME mutex — would
// self-deadlock (sync.RWMutex is non-reentrant); this matches spawnSearchChunk.
//
// The per-row expanded flags are snapshotted on the loop into exp[] for the same
// reason: s.state.expanded is a shared map written on the loop, so the worker
// must read a copy, not the live map.
//
// Beyond the clone, the captured filterEpoch is the stale-worker guard for jq
// re-runs and filter recomputes: those bump s.filterEpoch (ApplyJQ/ApplyFilters)
// without bumping queryID, so a worker that finished against superseded data or
// rules carries a stale Epoch that HandleFilterChunk drops.
func (s *LogStore) spawnFilterChunk(rules []filter.Rule, chunkIdx int, batchLogs []entry) bool {
	crules := s.compiledFilterRules(rules)
	applicable := false
	for i := range crules {
		if !crules[i].skip {
			applicable = true
			break
		}
	}
	if !applicable {
		return false
	}
	localIdx := chunkIdx
	parentCtx := s.filterCtx
	qid := s.queryID
	epoch := s.filterEpoch
	to := time.Millisecond * time.Duration(s.bundle.Config.Logs.JqTimeoutMs)
	offsetIdx := chunkIdx * int(s.bundle.Config.Logs.BatchOpChunkSize)
	// Clone the entry structs and snapshot the per-row expanded flags ON THE LOOP
	// so the worker never reads the live s.logs backing array or the shared
	// s.state.expanded map (see RACE SAFETY above).
	batch := slices.Clone(batchLogs)
	exp := make([]bool, len(batch))
	for i := range batch {
		exp[i] = s.state.expanded[offsetIdx+i]
	}
	s.poster.Go(func(gctx context.Context) {
		ctx, cancel := context.WithTimeout(parentCtx, to)
		defer cancel()
		msg := FilterChunkMsg{Instance: s.identity, ID: qid, Epoch: epoch, Offset: localIdx}
		hidden := make([]bool, len(batch))
		for i := range batch {
			hidden[i] = hideEntry(ctx, crules, &batch[i], exp[i])
		}
		msg.Hidden = hidden
		_ = s.poster.PostCritical(gctx, msg)
	})
	return true
}
