package logviewer

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/ui/components/timeline"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/itchyny/gojq"
	ahocorasick "github.com/petar-dambovaliev/aho-corasick"
)

// LogStore holds all log data and data-manipulation state for a single ICL instance.
// It is the data layer that the logviewer reads from during View().
type LogStore struct {
	bundle deps.Bundle
	// getWidth returns the current line-wrap column count for expanded entries,
	// supplied by the owning logviewer. May be nil in tests where wrapping is
	// not exercised.
	getWidth func() int
	// poster spawns the off-loop search chunk workers and delivers their
	// SearchChunkMsg results back through the runtime loop. Wired by the
	// instancepicker (which owns the static instance/store set) via SetPoster
	// once the runtime injects it; see internal/ui/components/logviewer/search.go.
	poster      msgs.Poster
	identity    string
	displayName string
	logger      *slog.Logger

	message string
	state   State
	mutexes []*sync.RWMutex

	logs []entry
	// logCount is the count of valid entries at the FRONT of s.logs; it is the
	// single source of truth for the valid range (s.logs is pre-sized to full
	// capacity by sizeBacking and its slice header is never mutated afterward).
	// It is atomic because off-loop readers load it
	// to bound-check s.logs/s.mutexes WITHOUT holding a lock: the on-loop writer
	// stores it with release semantics AFTER writing the element, so a reader that
	// observes a count > n has a happens-before with the s.logs[n] write and may
	// read that entry lock-free. Only the loop goroutine ever writes it.
	logCount           atomic.Int64
	earliestLogTSMicro int64
	latestLogTSMicro   int64
	contextRootLogID   string

	jqCount  int
	cancelJQ context.CancelFunc
	jqCtx    context.Context

	searchCount  int
	cancelSearch context.CancelFunc
	searchCtx    context.Context

	filterCount  int
	cancelFilter context.CancelFunc
	filterCtx    context.Context
	// filterEpoch is the stale-worker guard for jq/filter CHANGES. It is captured
	// onto every spawned FilterChunkMsg (Epoch) and bumped by ApplyJQ (every jq
	// re-run), ApplyFilters (full recompute), and ClearData — operations that
	// supersede an in-flight filter worker's data/rules WITHOUT bumping queryID
	// (queryID alone is insufficient). HandleFilterChunk drops any chunk whose
	// Epoch no longer matches.
	filterEpoch uint64

	// epoch caches the compiled artifacts (parsed jq query, compiled filter
	// rules, search automaton) shared by every chunk worker of the current
	// dispatch epoch, so a long stream compiles each expression once instead of
	// once per batch. Loop-goroutine-only, like the epoch contexts above; see
	// epochcache.go for the invalidation and sharing contract.
	epoch epochCache

	// filteredTotal is the cached filter-aware search-match total (matches over
	// non-hidden logs). filteredTotalDirty is true when any write to state.search
	// or state.filtered has invalidated the cached value. Both fields are
	// loop-goroutine-only state; no lock is needed or allowed.
	filteredTotal      int
	filteredTotalDirty bool

	streaming         bool
	pendingBatchCount int
	queryID           uint64

	appliedSearch string
	appliedJQ     string
	// appliedFilters is the rule-set snapshot the engine last applied. It is the
	// re-apply discriminator (compared against state.Filters() in SetStore),
	// NOT an epoch guard — the queryID stamp on FilterChunkMsg is the stale-worker
	// guard. nil means "no rules applied yet".
	appliedFilters []filter.Rule
}

// NewLogStore creates a LogStore whose identity and display name are the same.
// Tests and non-instance callers use it when no separate routing identity is
// needed.
func NewLogStore(bundle deps.Bundle, getWidth func() int, instance string) *LogStore {
	return NewLogStoreWithIdentity(bundle, getWidth, instance, instance)
}

// NewLogStoreWithIdentity creates a LogStore with a routing identity separate
// from its cosmetic display name. Async chunk messages use identity while logs
// always use displayName.
func NewLogStoreWithIdentity(bundle deps.Bundle, getWidth func() int, identity, displayName string) *LogStore {
	// Use defaults when bundle has no config.
	if bundle.Config == nil {
		bundle.Config = config.New()
	}
	return &LogStore{
		bundle:      bundle,
		getWidth:    getWidth,
		identity:    identity,
		displayName: displayName,
		state:       newState(),
		mutexes:     nil, // sized at StartStream/loadLogs to the per-query capacity
		logger:      slog.Default().With(logging.KeyComponent, "logstore", logging.KeyInstance, displayName),
	}
}

// JQChunkResult contains information the logviewer needs after a JQ chunk is processed.
type JQChunkResult struct {
	CacheInvalidated []int // log indices whose rendered output changed
	CursorAffected   bool  // whether the log under cursor was in this chunk
	ChunkOffset      int
	ResultCount      int
}

// ClearData resets log entries and data-derived state while preserving
// user-driven visual state (expanded, filtered) for reuse after lazy reload.
func (s *LogStore) ClearData() {
	s.CancelAll()
	// Publish the zero count (release) before dropping the backing slice so an
	// off-loop reader that acquires the new count observes an empty valid range.
	s.logCount.Store(0)
	s.logs = nil
	s.earliestLogTSMicro = 0
	s.latestLogTSMicro = 0
	s.message = ""
	s.contextRootLogID = ""
	s.streaming = false
	s.pendingBatchCount = 0
	s.jqCtx = nil
	s.searchCtx = nil
	s.filterCtx = nil
	s.filterCount = 0
	s.state.marked = make(indexMap)
	s.state.search = make(SearchMap)
	s.markFilteredTotalDirty()
	s.appliedJQ = ""
	s.appliedSearch = ""
	s.appliedFilters = nil
	s.jqCount = 0
	s.searchCount = 0
	// Bump queryID so any in-flight LogStreamMsg/LogStreamDoneMsg from a
	// stream that targeted the pre-clear state is rejected on arrival.
	s.queryID++
	// A cleared store also invalidates in-flight filter workers; the queryID bump
	// already drops their chunks, but bump filterEpoch too for consistency (every
	// filter-invalidating operation bumps it).
	s.filterEpoch++
}

// Clear resets all state in the store, including visual state.
func (s *LogStore) Clear() {
	s.ClearData() // already marks the filtered-total cache dirty
	s.state.expanded = make(indexMap)
	s.state.filtered = make(indexMap)
}

// sizeBacking allocates s.logs (FULL length = capacity) and a mutex pool covering
// it, BEFORE s.logCount is published. The backing arrays are never reallocated
// afterward, so off-loop readers that index s.logs/s.mutexes
// unlocked observe immutable headers. capacity must bound the rows this store holds.
func (s *LogStore) sizeBacking(capacity int) {
	capacity = max(capacity, 1)
	batch := int(s.bundle.Config.Logs.BatchOpChunkSize)
	chunks := max((capacity+batch-1)/batch, 1)
	s.logs = make([]entry, capacity)
	s.mutexes = make([]*sync.RWMutex, chunks)
	for i := range chunks {
		s.mutexes[i] = &sync.RWMutex{}
	}
}

// StartStream prepares the store for a new streaming query, sizing the backing
// arrays to capacity (never grown afterward). Call after Clear().
func (s *LogStore) StartStream(capacity int) {
	s.sizeBacking(capacity)
	s.streaming = true
	s.queryID++
}

// cancelAndNil calls cancel and sets the pointer to nil if non-nil.
func cancelAndNil(cancel *context.CancelFunc) {
	if *cancel != nil {
		(*cancel)()
		*cancel = nil
	}
}

// CancelAll cancels any in-flight search, jq, and filter operations.
func (s *LogStore) CancelAll() {
	cancelAndNil(&s.cancelSearch)
	cancelAndNil(&s.cancelJQ)
	cancelAndNil(&s.cancelFilter)
}

// Close cancels any in-flight jq, search, or filter goroutines and zeroes the
// cancel funcs so subsequent Close calls are no-ops. Returns nil; cancellation
// cannot fail. Safe to call multiple times.
func (s *LogStore) Close() error {
	cancelAndNil(&s.cancelJQ)
	cancelAndNil(&s.cancelSearch)
	cancelAndNil(&s.cancelFilter)
	return nil
}

// StreamResult contains information the logviewer needs after processing a stream message.
type StreamResult struct {
	ContextRootIdx int // index of the context root log, or -1 if not found
}

// HandleLogStreamMsg appends incoming logs and dispatches pending batch operations.
// Returns a StreamResult so the logviewer can handle centering on context root logs.
func (s *LogStore) HandleLogStreamMsg(msg *msgs.LogStreamMsg) StreamResult {
	result := StreamResult{ContextRootIdx: -1}
	if msg.ID != s.queryID {
		return result
	}
	if len(msg.Errs) > 0 {
		if s.message != "" {
			s.message += "\n---\n"
		}
		s.message += strings.Join(msg.Errs, "\n---\n")
	}
	batch := int(s.bundle.Config.Logs.BatchOpChunkSize)
	// n is the loop-goroutine-private running count; only this goroutine writes
	// s.logCount, so caching it is safe. Each row writes s.logs[n] FIRST, then
	// publishes n+1 via an atomic store (release). An off-loop reader that loads a
	// count > n (acquire) is therefore happens-after the element write and reads
	// s.logs[n] lock-free. Per-row publish preserves incremental visibility.
	n := int(s.logCount.Load())
	added := 0
	for _, logMsg := range msg.Logs {
		if n >= len(s.logs) {
			break
		}
		s.logs[n] = newEntry(s.getWidth, s.mutexes[n/batch], logMsg)
		if n == 0 || logMsg.Metadata.TSMicro < s.earliestLogTSMicro {
			s.earliestLogTSMicro = logMsg.Metadata.TSMicro
		}
		if n == 0 || logMsg.Metadata.TSMicro > s.latestLogTSMicro {
			s.latestLogTSMicro = logMsg.Metadata.TSMicro
		}
		if s.contextRootLogID != "" && logMsg.Metadata.ID == s.contextRootLogID {
			s.state.marked.Set(n, true)
			result.ContextRootIdx = n
			s.contextRootLogID = ""
		}
		n++
		s.logCount.Store(int64(n)) // release: element write above is now visible
		added++
	}
	s.pendingBatchCount += added
	s.dispatchPendingBatches()
	return result
}

// HandleLogStreamDoneMsg finalizes the stream and dispatches any remaining
// pending batches. The batch dispatch spawns jq/search workers via the poster
// (results flow back asynchronously), so this runs purely for effect.
func (s *LogStore) HandleLogStreamDoneMsg(msg *msgs.LogStreamDoneMsg) {
	if msg.ID != s.queryID {
		return
	}
	s.streaming = false
	if len(msg.Errs) > 0 {
		if s.message != "" {
			s.message += "\n---\n"
		}
		s.message += strings.Join(msg.Errs, "\n---\n")
	}
	s.dispatchPendingBatches()
	// Do NOT reslice s.logs here. The slice header is immutable for the store's
	// life after sizeBacking (len == capacity), so off-loop readers observe a
	// stable header lock-free; s.logCount is the sole source of the valid range.
}

// spawnJQ spawns an off-loop worker that runs the ALREADY-PARSED query q over
// the given batch chunk via the poster, posting the JQChunkMsg result back
// through the loop. It is the single spawn point for jq chunk workers — the
// batch-dispatch path and ApplyJQ both go through it — so the stale-chunk
// contract lives in exactly one place: the spawn-time queryID is captured HERE,
// on the loop, before the closure, so a later ClearData queryID++ cannot change
// the stamp HandleJQChunk guards on.
//
// Parsing is deliberately the caller's job: the two callers' parse-failure
// recovery differs (ApplyJQ clears every entry's jq output; the dispatch path
// tears down the streaming jq epoch and leaves rendered entries alone), and the
// parse itself is served from the per-epoch cache (parsedJQ).
//
// parentCtx is the caller's jq epoch — s.jqCtx for the streaming dispatch,
// ApplyJQ's own JqTimeoutMs-bounded context for the full apply. The worker
// derives a per-chunk JqTimeoutMs timeout from it; since context.WithTimeout
// takes the EARLIER of parent deadline and now+timeout, an already-bounded
// parent (ApplyJQ's) keeps its own deadline and an unbounded one (s.jqCtx) gets
// the per-chunk bound. The poster-supplied gctx is used only for PostCritical
// delivery so shutdown can unblock it.
//
// The worker reads `data` (a private slice of the entries' immutable data maps,
// built on the loop below), never the live s.logs, so it needs no chunk lock --
// mirroring the lockless spawnFilterChunk/spawnSearchChunk.
func (s *LogStore) spawnJQ(parentCtx context.Context, q *gojq.Query, chunkIdx int, batchLogs []entry) {
	data := make([]map[string]any, len(batchLogs))
	for i, l := range batchLogs {
		data[i] = l.data
	}
	localIdx := chunkIdx
	qid := s.queryID
	to := time.Millisecond * time.Duration(s.bundle.Config.Logs.JqTimeoutMs)
	s.poster.Go(func(gctx context.Context) {
		ctx, cancel := context.WithTimeout(parentCtx, to)
		defer cancel()
		jqMsg := JQChunkMsg{Instance: s.identity, ID: qid, Offset: localIdx}
		jqMsg.Results, jqMsg.Err = jqBatch(ctx, q, data)
		_ = s.poster.PostCritical(gctx, jqMsg)
	})
}

// resetJQEpoch is the batch-dispatch path's jq parse-failure recovery: the query
// cannot produce output, so drop the applied marker, the running count and the
// streaming jq epoch. It deliberately does NOT clear the entries' jq output —
// that is ApplyJQ's (different) recovery, which must not fire here or a stream
// batch would wipe the output the previous, valid query produced.
func (s *LogStore) resetJQEpoch() {
	s.appliedJQ = ""
	s.jqCount = 0
	cancelAndNil(&s.cancelJQ)
	s.jqCtx = nil
}

// spawnSearchChunk runs one search chunk off-loop via the poster. ctx is the
// search epoch/cancel handle (stale dispatches are cancelled through it); the
// poster-supplied gctx is used only for PostCritical so shutdown can unblock
// delivery. The caller builds ac once per dispatch and shares it across chunks.
func (s *LogStore) spawnSearchChunk(ctx context.Context, ac ahocorasick.AhoCorasick, id uint64, offset, batchSize int, logs []entry) {
	expanded := s.state.expanded
	s.poster.Go(func(gctx context.Context) {
		if msg, ok := searchChunk(ctx, ac, s.identity, offset, batchSize, logs, expanded); ok {
			// Stamp the spawn-time queryID so HandleSearchChunk drops the chunk
			// if the store was reset (ClearData bumps queryID) while it ran.
			msg.ID = id
			_ = s.poster.PostCritical(gctx, msg)
		}
	})
}

// dispatchPendingBatches spawns jq, search, and/or filter workers for each
// pending batch chunk through the poster. All paths deliver their results
// asynchronously back through the loop, so this method runs purely for effect.
func (s *LogStore) dispatchPendingBatches() {
	batchSize := int(s.bundle.Config.Logs.BatchOpChunkSize)

	jqQuery := s.bundle.State.JQ()
	searchQuery := s.bundle.State.Search()
	filterRules := s.bundle.State.Filters()

	searchAC := s.prepareDispatchEpochs(jqQuery, searchQuery, filterRules)

	for s.pendingBatchCount >= batchSize || (!s.streaming && s.pendingBatchCount > 0) {
		count := min(s.pendingBatchCount, batchSize)
		batchStart := int(s.logCount.Load()) - s.pendingBatchCount
		batchEnd := batchStart + count
		batchLogs := s.logs[batchStart:batchEnd]
		chunkIdx := batchStart / batchSize

		if jqQuery != "" {
			// Served from the per-epoch cache: parsed once for the whole stream,
			// not once per batch. Parse failures are cached too, so a broken
			// query does not re-parse per batch either.
			q, err := s.parsedJQ(jqQuery)
			if err != nil {
				s.logger.Error("jq parse failed in batch dispatch", logging.KeyError, err)
				s.resetJQEpoch()
				s.pendingBatchCount = 0
				return
			}
			s.spawnJQ(s.jqCtx, q, chunkIdx, batchLogs)
		} else if searchQuery != "" {
			s.spawnSearchChunk(s.searchCtx, searchAC, s.queryID, chunkIdx, batchSize, batchLogs)
		}

		// Independent filter pass — NOT spawned when jq is active: the off-loop
		// filter worker would race with HandleJQChunk's on-loop, lockless
		// whole-struct overwrite (s.logs[logIdx] = s.logs[logIdx].WithData(...)).
		// For the jq-active case the filter spawn is sequenced from HandleJQChunk.
		if len(filterRules) > 0 && jqQuery == "" {
			s.spawnFilterChunk(filterRules, chunkIdx, batchLogs)
		}

		s.pendingBatchCount -= count
	}
}

// prepareDispatchEpochs initializes the jq/search/filter epoch contexts lazily
// and returns the search AC automaton (built once per EPOCH via the epoch cache,
// reused across every dispatch and every chunk).
// Extracted from dispatchPendingBatches to keep its cyclomatic complexity in check.
func (s *LogStore) prepareDispatchEpochs(jqQuery, searchQuery string, filterRules []filter.Rule) ahocorasick.AhoCorasick {
	if jqQuery != "" && s.jqCtx == nil {
		s.jqCtx, s.cancelJQ = context.WithCancel(context.Background())
	}
	// The AC automaton is taken from the epoch cache and reused across all
	// chunks below; it is only requested when search is active and jq is not,
	// matching the condition that creates searchCtx.
	var searchAC ahocorasick.AhoCorasick
	if searchQuery != "" && jqQuery == "" {
		if s.searchCtx == nil {
			s.searchCtx, s.cancelSearch = context.WithCancel(context.Background())
		}
		searchAC = s.searchAutomaton(searchQuery)
	}
	if len(filterRules) > 0 && s.filterCtx == nil {
		s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cancelFilter is called via cancelAndNil in CancelAll/Close
	}
	return searchAC
}

// DoSearch dispatches chunked search across all logs, updating appliedSearch
// tracking. The empty-query path just clears the match map; the non-empty path
// spawns per-chunk workers whose results flow back as SearchChunkMsg. Runs for
// effect.
func (s *LogStore) DoSearch() {
	if s.cancelSearch != nil {
		s.cancelSearch()
	}
	s.searchCtx = nil

	query := s.bundle.State.Search()

	if query == "" {
		s.appliedSearch = ""
		s.state.search = make(SearchMap)
		s.markFilteredTotalDirty()
		return
	}

	var ctx context.Context
	ctx, s.cancelSearch = context.WithCancel(context.Background())
	s.searchCount = 0
	s.state.search = make(SearchMap)
	s.markFilteredTotalDirty()

	batchSize := int(s.bundle.Config.Logs.BatchOpChunkSize)
	ac := s.searchAutomaton(query)
	var chunkIdx int
	for chunk := range slices.Chunk(s.logs[:int(s.logCount.Load())], batchSize) {
		// ctx is the search epoch (the cancel handle in s.cancelSearch); it
		// cancels stale dispatches. Results flow back via the poster.
		s.spawnSearchChunk(ctx, ac, s.queryID, chunkIdx, batchSize, chunk)
		chunkIdx++
	}

	s.logger.Info("search dispatched", logging.KeyAction, "search", "query", query, "chunks", chunkIdx)
}

// ApplyJQ dispatches chunked jq across all logs, updating appliedJQ tracking.
// Each chunk is computed off-loop by a poster worker that posts a JQChunkMsg
// back through the loop; the empty-query and parse-failure write-backs stay on
// the loop goroutine (single-writer). Runs for effect — results flow via the
// poster.
func (s *LogStore) ApplyJQ() {
	if s.cancelJQ != nil {
		s.cancelJQ()
	}
	s.jqCtx = nil
	s.pendingBatchCount = 0
	// Every ApplyJQ invalidates in-flight filter workers: their hide decisions
	// were computed against the OLD jq output, but ApplyJQ overwrites s.logs
	// (WithData) below on every path (empty-query, parse-fail, normal) without
	// bumping queryID. Bump filterEpoch and cancel the filter epoch so a late
	// worker's chunk is dropped by HandleFilterChunk's epoch guard, and any
	// freshly sequenced filter pass (from HandleJQChunk) carries the new epoch.
	s.filterEpoch++
	cancelAndNil(&s.cancelFilter)

	queryString := s.bundle.State.JQ()

	if queryString == "" {
		s.appliedJQ = ""
		for i := range s.logs[:int(s.logCount.Load())] {
			s.logs[i] = s.logs[i].WithData(nil)
		}
		return
	}

	var ctx context.Context
	ctx, s.cancelJQ = context.WithTimeout(context.Background(), time.Millisecond*time.Duration(s.bundle.Config.Logs.JqTimeoutMs))
	s.jqCount = 0

	// Served from the per-epoch cache; a fresh query string is a miss, so an
	// actual apply parses exactly once.
	q, err := s.parsedJQ(queryString)
	if err != nil {
		s.logger.Error("jq parse failed", logging.KeyError, err)
		s.appliedJQ = ""
		s.jqCount = 0
		for i := range s.logs[:int(s.logCount.Load())] {
			s.logs[i] = s.logs[i].WithData(nil)
		}
		return
	}

	batchSize := int(s.bundle.Config.Logs.BatchOpChunkSize)
	var chunkIdx int
	for chunk := range slices.Chunk(s.logs[:int(s.logCount.Load())], batchSize) {
		// ctx is the apply-wide jq epoch (WithTimeout(Background, JqTimeoutMs)
		// stored in s.cancelJQ); it cancels/times out the compute and stays the
		// binding deadline for every chunk worker spawned below.
		s.spawnJQ(ctx, q, chunkIdx, chunk)
		chunkIdx++
	}

	s.logger.Info("jq applied", logging.KeyAction, "jq", "query", queryString, "chunks", chunkIdx)
}

// HandleJQChunk processes jq results for a single chunk. It returns a JQChunkResult
// with cache invalidation info; the logviewer handles render cache invalidation
// and cursor adjustment using the result. Any jq-triggered re-search of the
// affected chunk is dispatched off-loop via the poster within this method.
func (s *LogStore) HandleJQChunk(msg JQChunkMsg) JQChunkResult {
	result := JQChunkResult{ChunkOffset: msg.Offset}
	if msg.Instance != s.identity {
		return result
	}
	// Drop a chunk computed against a now-reset store. ClearData bumps queryID
	// and nils s.logs; a worker that finished before the reset carries the old
	// queryID and its offset would index out of the emptied slice (the
	// jqchunk stale-chunk panic). CancelAll cannot recall an already-finished
	// worker, so this stamp/guard pair is the only thing that catches it.
	if msg.ID != s.queryID {
		return result
	}
	if msg.Err != nil {
		if errors.Is(msg.Err, context.Canceled) || errors.Is(msg.Err, context.DeadlineExceeded) {
			s.logger.Debug("jq chunk cancelled", logging.KeyInstance, s.displayName, logging.KeyError, msg.Err)
		} else {
			s.logger.Error("jq chunk failed", logging.KeyInstance, s.displayName, logging.KeyError, msg.Err)
		}
		cancelAndNil(&s.cancelJQ)
		return result
	}

	offsetIdx := msg.Offset * int(s.bundle.Config.Logs.BatchOpChunkSize)
	// Defense-in-depth: even with a matching queryID, never index past the
	// populated range. s.logs stays full-length (== capacity) for the store's
	// life, so bound against logCount (the valid range), NOT len(s.logs), so a
	// misdispatched chunk cannot touch a zero-value tail entry (nil entry.mu).
	logCount := int(s.logCount.Load())
	if offsetIdx+len(msg.Results) > logCount {
		s.logger.Warn("jq chunk out of range; dropped",
			"offset", msg.Offset, logging.KeyCount, len(msg.Results), "logs", logCount)
		return result
	}
	result.ResultCount = len(msg.Results)

	for i := range msg.Results {
		logIdx := offsetIdx + i
		s.logs[logIdx] = s.logs[logIdx].WithData(msg.Results[i])
		result.CacheInvalidated = append(result.CacheInvalidated, logIdx)
	}

	s.logger.Debug("jq chunk processed", "offset", msg.Offset, logging.KeyCount, len(msg.Results))

	s.jqCount += len(msg.Results)
	if !s.streaming && s.jqCount >= int(s.logCount.Load()) {
		cancelAndNil(&s.cancelJQ)
		// All jq chunks done — mark jq as applied and update search tracking
		// since jq-triggered per-chunk searches bypass DoSearch().
		s.appliedJQ = s.bundle.State.JQ()
		s.appliedSearch = s.bundle.State.Search()
	}

	s.rsearchAffectedChunk(msg.Offset, len(msg.Results))

	// Filter is sequenced AFTER the jq overwrite (happens-before): the dispatch
	// loop does NOT spawn a filter pass while jq is active (Task 11), so this is
	// the only filter pass over jq-rewritten entries. Reading the just-written
	// s.logs slice on the loop, then handing it to the off-loop worker, gives a
	// happens-before between the overwrite and the filter read.
	if rules := s.bundle.State.Filters(); len(rules) > 0 {
		if s.filterCtx == nil {
			s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cancelFilter is called via cancelAndNil in CancelAll/Close
		}
		s.spawnFilterChunk(rules, msg.Offset, s.logs[offsetIdx:offsetIdx+len(msg.Results)])
	}

	return result
}

// rsearchAffectedChunk re-runs the active search over the chunk the jq
// write-back just rewrote (jq-triggered per-chunk searches bypass DoSearch). It
// is a no-op when no search is active. The chunk search runs off-loop via the
// poster: the on-loop jq write-back stays the single writer; the worker only
// reads entry bytes (under each entry's read lock). context.Background() is used
// because the jq-triggered re-search has no per-dispatch search epoch.
func (s *LogStore) rsearchAffectedChunk(offset, resultCount int) {
	query := s.bundle.State.Search()
	if query == "" {
		return
	}
	offsetIdx := offset * int(s.bundle.Config.Logs.BatchOpChunkSize)
	chunkLogs := s.logs[offsetIdx:]
	if len(chunkLogs) > resultCount {
		chunkLogs = chunkLogs[:resultCount]
	}
	ac := s.searchAutomaton(query)
	s.spawnSearchChunk(context.Background(), ac, s.queryID, offset, int(s.bundle.Config.Logs.BatchOpChunkSize), chunkLogs)
}

// HandleSearchChunk accumulates search results for a single chunk. It returns
// true when the chunk was processed (the logviewer uses this to gate its
// post-chunk display/filter update) and false when the chunk was rejected (wrong
// instance or error). The bottom bar reads the live match count off the store on
// every draw, so no match-count message is emitted.
func (s *LogStore) HandleSearchChunk(msg SearchChunkMsg) bool {
	if msg.Instance != s.identity {
		return false
	}
	// Drop a chunk computed against a now-reset store (ClearData bumps queryID).
	// HandleSearchChunk cannot panic (it bounds on s.logCount and mutates maps),
	// but without this guard a stale chunk silently writes outdated indices into
	// state.search. Mirrors the HandleJQChunk guard.
	if msg.ID != s.queryID {
		return false
	}
	if msg.Err != nil {
		s.logger.Error("search chunk failed", logging.KeyInstance, s.displayName, logging.KeyError, msg.Err)
		return false
	}

	offsetIdx := msg.Offset * int(s.bundle.Config.Logs.BatchOpChunkSize)
	logCount := int(s.logCount.Load())
	chunkEnd := min(offsetIdx+int(s.bundle.Config.Logs.BatchOpChunkSize), logCount)

	for i := offsetIdx; i < chunkEnd; i++ {
		delete(s.state.search, i)
	}
	maps.Copy(s.state.search, msg.Results)
	s.markFilteredTotalDirty()

	s.searchCount += chunkEnd - offsetIdx
	if !s.streaming && s.searchCount >= logCount {
		if s.cancelSearch != nil {
			s.cancelSearch()
		}
		s.appliedSearch = s.bundle.State.Search()
	}

	s.logger.Debug("search chunk processed", "offset", msg.Offset,
		logging.KeyCount, s.state.search.FilteredCount(s.state.filtered))
	return true
}

// Snapshotize serializes the store's logs with mutex locking.
// Returns the logs and a function the caller must invoke to unlock the mutexes.
func (s *LogStore) Snapshotize() ([]icl.Log, func()) {
	count := int(s.logCount.Load())
	logs := make([]icl.Log, count)
	for _, mu := range s.mutexes {
		mu.RLock()
	}
	for i, e := range s.logs[:count] {
		logs[i] = icl.Log{
			Data:     e.data,
			Metadata: e.metadata,
		}
	}
	return logs, func() {
		for _, mu := range s.mutexes {
			mu.RUnlock()
		}
	}
}

// loadLogs populates s.logs from the given logs slice.
// The caller must clear the store (via Clear or ClearData) before calling this.
// It sizes the backing arrays (and mutex pool) to len(logs), fully populates
// them, and publishes s.logCount LAST so an off-loop reader never observes a
// large count against an unsized array.
func (s *LogStore) loadLogs(logs []icl.Log) {
	s.streaming = false
	if len(logs) < 1 {
		s.logCount.Store(0)
		return
	}

	s.sizeBacking(len(logs)) // sizes mutex pool to ceil(len/batch)
	batch := int(s.bundle.Config.Logs.BatchOpChunkSize)
	s.earliestLogTSMicro = logs[0].Metadata.TSMicro
	s.latestLogTSMicro = logs[0].Metadata.TSMicro

	for idx, logMsg := range logs {
		s.logs[idx] = newEntry(s.getWidth, s.mutexes[idx/batch], logMsg)
		if logMsg.Metadata.TSMicro < s.earliestLogTSMicro {
			s.earliestLogTSMicro = logMsg.Metadata.TSMicro
		}
		if logMsg.Metadata.TSMicro > s.latestLogTSMicro {
			s.latestLogTSMicro = logMsg.Metadata.TSMicro
		}
	}

	s.logCount.Store(int64(len(logs))) // published LAST (release): entries above are now visible
	s.logger.Info("logs set", logging.KeyCount, len(logs), "earliest", s.earliestLogTSMicro, "latest", s.latestLogTSMicro)
}

// SetLogs clears all state and loads logs, applying filters, jq and search from
// global state. ApplyFilters, ApplyJQ and DoSearch spawn their chunk workers via
// the poster, so this runs purely for effect.
func (s *LogStore) SetLogs(logs []icl.Log) {
	s.Clear()
	s.loadLogs(logs)
	s.ApplyFilters()
	s.ApplyJQ()
	s.DoSearch()
}

// SetLogsRaw loads logs preserving visual state (expanded, filtered) and
// reapplies filters, jq and search from global state. ApplyFilters, ApplyJQ
// and DoSearch spawn their chunk workers via the poster, so this runs purely
// for effect.
func (s *LogStore) SetLogsRaw(logs []icl.Log) {
	s.ClearData()
	s.loadLogs(logs)
	s.ApplyFilters()
	s.ApplyJQ()
	s.DoSearch()
}

// SetExpand toggles expansion for a single log entry. If the entry had prior
// search results, the search is re-run over the new byte representation.
func (s *LogStore) SetExpand(idx int, b bool) {
	if idx < 0 || idx >= int(s.logCount.Load()) {
		return
	}
	s.logger.Debug("expand toggled", "index", idx, "all", false)
	s.state.expanded.Set(idx, b)

	if _, ok := s.state.search[idx]; ok {
		bs := s.logs[idx].Bytes(b)
		ac := s.searchAutomaton(s.bundle.State.Search())
		results := search(ac, [][]byte{bs})
		if len(results) > 0 {
			s.state.search[idx] = results[0]
		} else {
			delete(s.state.search, idx)
		}
		s.markFilteredTotalDirty()
	}
}

// RefreshExpandedSearch re-runs the search over every expanded log entry that
// already has search results. This is necessary after a window resize because
// expanded entries are wrapped at the current window width, so the line indices
// stored in SearchMatch become stale when the width changes.
func (s *LogStore) RefreshExpandedSearch() {
	if len(s.state.search) == 0 {
		return
	}
	ac := s.searchAutomaton(s.bundle.State.Search())
	for idx := range s.state.search {
		if !s.state.expanded[idx] {
			continue
		}
		bs := s.logs[idx].Bytes(true)
		results := search(ac, [][]byte{bs})
		if len(results) > 0 {
			s.state.search[idx] = results[0]
		} else {
			delete(s.state.search, idx)
		}
	}
	s.markFilteredTotalDirty()
}

// FilteredMatchTotal returns the filter-aware search-match total (matches over
// non-hidden logs), recomputing only when a search/filter/expand mutation has
// marked the cache dirty. Loop-goroutine-only; no lock.
func (s *LogStore) FilteredMatchTotal() int {
	if s.filteredTotalDirty {
		s.filteredTotal = s.state.search.FilteredCount(s.state.filtered)
		s.filteredTotalDirty = false
	}
	return s.filteredTotal
}

func (s *LogStore) markFilteredTotalDirty() { s.filteredTotalDirty = true }

// ToggleExpandAll expands all logs if any are collapsed, or collapses all if
// every log is already expanded.
func (s *LogStore) ToggleExpandAll() {
	count := int(s.logCount.Load())
	notAllExpanded := len(s.state.expanded) != count
	s.logger.Debug("expand toggled", "index", -1, "all", true)
	for i := range s.logs[:count] {
		s.SetExpand(i, notAllExpanded)
	}
}

// IterLogs returns an iterator over non-filtered log indices starting from
// `from` with the given step direction.
func (s *LogStore) IterLogs(from, step int) iter.Seq[int] {
	return func(yield func(int) bool) {
		count := int(s.logCount.Load())
		for i := from; i >= 0 && i < count; i += step {
			if !s.state.filtered[i] && !yield(i) {
				return
			}
		}
	}
}

// GetLogCount returns the number of logs currently in the store. It loads the
// count with acquire semantics so an off-loop caller that then
// indexes s.logs[<count] observes a happens-before with the on-loop element write.
func (s *LogStore) GetLogCount() int {
	return int(s.logCount.Load())
}

// GetInstance returns the CRN routing identity this store belongs to.
func (s *LogStore) GetInstance() string {
	return s.identity
}

// ReadLog returns the log at the given index with chunk-level read locking.
// Returns (zero, false) if idx is out of bounds. Safe to call off-loop:
// the logCount acquire pairs with HandleLogStreamMsg's release, so an in-bounds
// idx observes a fully written s.logs[idx]; the chunk RLock then guards against
// concurrent jq write-backs to already-published entries.
func (s *LogStore) ReadLog(idx int) (icl.Log, bool) {
	if idx < 0 || idx >= int(s.logCount.Load()) {
		return icl.Log{}, false
	}
	chunkIdx := idx / int(s.bundle.Config.Logs.BatchOpChunkSize)
	s.mutexes[chunkIdx].RLock()
	e := s.logs[idx]
	log := icl.Log{Data: e.data, Metadata: e.metadata}
	s.mutexes[chunkIdx].RUnlock()
	return log, true
}

// ReadLogs returns logs at the given indices. Out-of-bounds indices are skipped.
// Each index is read individually with per-call chunk locking via ReadLog.
func (s *LogStore) ReadLogs(indices []int) []icl.Log {
	if len(indices) == 0 {
		return nil
	}
	result := make([]icl.Log, 0, len(indices))
	for _, idx := range indices {
		if log, ok := s.ReadLog(idx); ok {
			result = append(result, log)
		}
	}
	return result
}

// GetTSMicro returns the timestamp of the log at the given index in microseconds.
// Acquires a chunk-level read lock for memory model correctness during streaming.
func (s *LogStore) GetTSMicro(idx int) (int64, bool) {
	if idx < 0 || idx >= int(s.logCount.Load()) {
		return 0, false
	}
	chunkIdx := idx / int(s.bundle.Config.Logs.BatchOpChunkSize)
	s.mutexes[chunkIdx].RLock()
	ts := s.logs[idx].metadata.TSMicro
	s.mutexes[chunkIdx].RUnlock()
	return ts, true
}

// GetSeverity returns the severity of the log at the given index.
// Acquires a chunk-level read lock for memory model correctness during streaming.
func (s *LogStore) GetSeverity(idx int) (icl.Severity, bool) {
	if idx < 0 || idx >= int(s.logCount.Load()) {
		return icl.SeverityUnknown, false
	}
	chunkIdx := idx / int(s.bundle.Config.Logs.BatchOpChunkSize)
	s.mutexes[chunkIdx].RLock()
	sev := s.logs[idx].metadata.Severity
	s.mutexes[chunkIdx].RUnlock()
	return sev, true
}

// GetTimeRange returns the earliest and latest log timestamps in microseconds.
func (s *LogStore) GetTimeRange() (earliest, latest int64) {
	return s.earliestLogTSMicro, s.latestLogTSMicro
}

// SetPoster injects the runtime poster used to spawn off-loop search chunk
// workers and deliver their SearchChunkMsg results back through the loop. The
// instancepicker binds it to every static store once the runtime supplies it,
// so every store has a non-nil poster before any DoSearch/dispatch runs.
func (s *LogStore) SetPoster(p msgs.Poster) {
	s.poster = p
}

// SetWidthSource updates the closure used for entry line wrapping. Called by
// the logviewer's SetStore to bind a newly attached store to the viewer's logs
// sub-rect width source.
func (s *LogStore) SetWidthSource(getWidth func() int) {
	s.getWidth = getWidth
	for i := range s.logs[:int(s.logCount.Load())] {
		s.logs[i].getWidth = getWidth
	}
}

// IsStreaming returns whether the store is currently receiving streamed logs.
func (s *LogStore) IsStreaming() bool {
	return s.streaming
}

// GetMessage returns the current message (errors, clippy, etc.).
func (s *LogStore) GetMessage() string {
	return s.message
}

// SetMessage replaces the store's message (the fetch-error text the logviewer
// shows when the store has no logs). Used by snapshot restore to put a restored
// errored instance's fetch error back into the field. Unlike the stream-error
// path it does not append; callers pass the full message.
func (s *LogStore) SetMessage(msg string) {
	s.message = msg
}

// SetContextRootLogID sets the log ID to mark and center on when it arrives via streaming.
func (s *LogStore) SetContextRootLogID(id string) {
	s.contextRootLogID = id
}

// GetQueryID returns the current query ID used for stream message filtering.
func (s *LogStore) GetQueryID() uint64 {
	return s.queryID
}

// IterLogMeta yields (idx, tsMicro, severity) for every log in the store.
// It acquires a per-chunk read lock when crossing into a new chunk. It does
// NOT respect the filtered state — the timeline shows the raw distribution.
// Safe to call during streaming.
func (s *LogStore) IterLogMeta() iter.Seq[timeline.LogMeta] {
	return func(yield func(timeline.LogMeta) bool) {
		batch := int(s.bundle.Config.Logs.BatchOpChunkSize)
		// Load once with acquire semantics; entries in [0,count) are fully
		// written (paired with HandleLogStreamMsg's release store).
		count := int(s.logCount.Load())
		lockedChunk := -1
		defer func() {
			if lockedChunk >= 0 {
				s.mutexes[lockedChunk].RUnlock()
			}
		}()
		for i := range count {
			chunk := i / batch
			if chunk != lockedChunk {
				if lockedChunk >= 0 {
					s.mutexes[lockedChunk].RUnlock()
				}
				s.mutexes[chunk].RLock()
				lockedChunk = chunk
			}
			md := s.logs[i].metadata
			if !yield(timeline.LogMeta{
				Idx:      i,
				TSMicro:  md.TSMicro,
				Severity: md.Severity,
			}) {
				return
			}
		}
	}
}
