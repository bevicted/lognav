package logviewer

import (
	"context"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFilterChunkMsg_Fields locks the FilterChunkMsg field set used by the
// filter chunk worker and HandleFilterChunk.
func TestFilterChunkMsg_Fields(t *testing.T) {
	t.Parallel()
	msg := FilterChunkMsg{
		Instance: "i",
		ID:       7,
		Offset:   2,
		Hidden:   []bool{true, false},
		Err:      nil,
	}
	assert.Equal(t, "i", msg.Instance)
	assert.Equal(t, uint64(7), msg.ID)
	assert.Equal(t, 2, msg.Offset)
	assert.Equal(t, []bool{true, false}, msg.Hidden)
	assert.NoError(t, msg.Err)
}

// TestCancelAll_CancelsFilterEpoch verifies CancelAll and Close cancel the
// filter epoch (cancelFilter) like the jq/search epochs.
func TestCancelAll_CancelsFilterEpoch(t *testing.T) {
	t.Parallel()
	s := NewLogStore(depstest.NewTest(t), nil, "i")
	cancelled := false
	s.cancelFilter = func() { cancelled = true }
	s.CancelAll()
	assert.True(t, cancelled, "CancelAll must cancel the filter epoch")
	assert.Nil(t, s.cancelFilter, "CancelAll nils cancelFilter")
}

// TestCompileFieldRule_SurfacesCompileError verifies the field-rule evaluator
// surfaces gojq compile errors (e.g. unknown function), not just parse errors.
func TestCompileFieldRule_SurfacesCompileError(t *testing.T) {
	t.Parallel()
	_, err := compileFieldRule(".foo | nosuchfunc")
	require.Error(t, err, "compile error (unknown function) must surface")

	_, err = compileFieldRule("((((")
	require.Error(t, err, "parse error must surface")

	code, err := compileFieldRule(".data.status")
	require.NoError(t, err)
	assert.NotNil(t, code)
}

// TestHideForFieldRule covers has-field/lacks-field over raw data.
func TestHideForFieldRule(t *testing.T) {
	t.Parallel()
	code, err := compileFieldRule(".status")
	require.NoError(t, err)

	withField := map[string]any{"status": "ok"}
	withEmpty := map[string]any{"status": ""}
	without := map[string]any{"other": 1}

	// has field: keep non-empty -> hide when empty/absent.
	assert.False(t, hideForFieldRule(context.Background(), code, filter.HasField, withField))
	assert.True(t, hideForFieldRule(context.Background(), code, filter.HasField, withEmpty))
	assert.True(t, hideForFieldRule(context.Background(), code, filter.HasField, without))

	// lacks field: keep empty -> hide when non-empty.
	assert.True(t, hideForFieldRule(context.Background(), code, filter.LacksField, withField))
	assert.False(t, hideForFieldRule(context.Background(), code, filter.LacksField, withEmpty))
	assert.False(t, hideForFieldRule(context.Background(), code, filter.LacksField, without))
}

// TestHideForTermRule covers include/exclude over the byte corpus.
func TestHideForTermRule(t *testing.T) {
	t.Parallel()
	needle := []byte("error")
	matches := []byte(`{"msg":"error here"}`)
	clean := []byte(`{"msg":"all good"}`)

	// include: keep matching -> hide when no match.
	assert.False(t, hideForTermRule(needle, filter.Include, matches))
	assert.True(t, hideForTermRule(needle, filter.Include, clean))

	// exclude: drop matching -> hide when match.
	assert.True(t, hideForTermRule(needle, filter.Exclude, matches))
	assert.False(t, hideForTermRule(needle, filter.Exclude, clean))

	// empty needle matches nothing (prior single-needle AC behavior).
	assert.True(t, hideForTermRule(nil, filter.Include, matches))
	assert.False(t, hideForTermRule(nil, filter.Exclude, matches))
}

// TestApplyFilters_AND_MixedRuleTypes verifies a log is hidden unless it passes
// EVERY rule, across mixed term + field rule types, over RAW data (not display jq).
func TestApplyFilters_AND_MixedRuleTypes(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	// Display jq projects only .msg — filters must still see raw .status.
	bundle.State.SetJQ(".msg")
	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "error A", "status": "down"}}, // 0: include+lacks? has status -> drops on lacks
		{Data: map[string]any{fieldMsg: "error B"}},                   // 1: matches include, no status
		{Data: map[string]any{fieldMsg: "ok C"}},                      // 2: no include match
	})

	bundle.State.SetFilters([]filter.Rule{
		{Type: filter.Include, Value: "error"},      // keep msg containing "error"
		{Type: filter.LacksField, Value: ".status"}, // keep logs WITHOUT status
	})
	s.ApplyFilters()

	assert.True(t, s.state.filtered[0], "0 has status -> hidden by lacks field")
	assert.False(t, s.state.filtered[1], "1 matches include and lacks status -> visible")
	assert.True(t, s.state.filtered[2], "2 no include match -> hidden")
}

// TestApplyFilters_EmptyRules_ClearsFiltered verifies an empty rule list hides
// nothing and clears any prior filtered state.
func TestApplyFilters_EmptyRules_ClearsFiltered(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "a"}},
		{Data: map[string]any{fieldMsg: "b"}},
	})
	s.state.filtered.Set(0, true) // prior state
	bundle.State.SetFilters(nil)
	s.ApplyFilters()
	assert.Empty(t, s.state.filtered, "empty rule list hides nothing")
}

// TestApplyFilters_BadFieldRuleIsNoOp verifies a rule whose jq fails to compile
// does not hide every log (it is skipped, not applied as "always empty").
func TestApplyFilters_BadFieldRuleIsNoOp(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "a"}}})
	bundle.State.SetFilters([]filter.Rule{{Type: filter.HasField, Value: "((((bad"}})
	s.ApplyFilters()
	assert.False(t, s.state.filtered[0], "uncompilable field rule must not hide logs")
}

// TestApplyFilters_SetsAppliedSnapshot verifies the applied-rules snapshot is
// recorded so SetStore can compare against state.Filters().
func TestApplyFilters_SetsAppliedSnapshot(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "a"}}})
	rules := []filter.Rule{{Type: filter.Exclude, Value: "noise"}}
	bundle.State.SetFilters(rules)
	s.ApplyFilters()
	assert.Equal(t, rules, s.appliedFilters)
}

// filterChunks returns the FilterChunkMsg events captured by the fake poster.
func (f *fakePoster) filterChunks() []FilterChunkMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FilterChunkMsg, 0, len(f.posted))
	for _, ev := range f.posted {
		if fcm, ok := ev.(FilterChunkMsg); ok {
			out = append(out, fcm)
		}
	}
	return out
}

// TestSpawnFilterChunk_PostsHiddenDecisions verifies the worker computes the
// per-log hide decisions and posts a FilterChunkMsg carrying them, stamped with
// the spawn-time queryID.
func TestSpawnFilterChunk_PostsHiddenDecisions(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "error one"}},
		{Data: map[string]any{fieldMsg: "clean"}},
	})
	s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cancelFilter registered with store; CancelAll cleans up
	t.Cleanup(s.cancelFilter)

	rules := []filter.Rule{{Type: filter.Include, Value: "error"}}
	ok := s.spawnFilterChunk(rules, 0, s.logs[:2])
	assert.True(t, ok, "spawn returns true on compilable rules")

	chunks := fp.filterChunks()
	require.Len(t, chunks, 1)
	assert.Equal(t, s.queryID, chunks[0].ID, "chunk stamped with spawn queryID")
	assert.Equal(t, s.filterEpoch, chunks[0].Epoch, "chunk stamped with spawn filterEpoch")
	assert.Equal(t, 0, chunks[0].Offset)
	// include "error": log 0 visible (false), log 1 hidden (true).
	assert.Equal(t, []bool{false, true}, chunks[0].Hidden)
}

// TestSpawnFilterChunk_CompileError_ReturnsFalse verifies a field rule that fails
// to compile aborts the spawn (no worker) and returns false.
func TestSpawnFilterChunk_CompileError_ReturnsFalse(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "a"}}})
	s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cancelFilter registered with store; CancelAll cleans up
	t.Cleanup(s.cancelFilter)

	ok := s.spawnFilterChunk([]filter.Rule{{Type: filter.HasField, Value: "((((bad"}}, 0, s.logs[:1])
	assert.False(t, ok, "uncompilable field rule -> no worker, return false")
	assert.Empty(t, fp.filterChunks(), "no chunk posted on compile failure")
}

// TestHandleFilterChunk_AppliesHidden writes the chunk's decisions into
// s.state.filtered at the chunk offset.
func TestHandleFilterChunk_AppliesHidden(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "a"}},
		{Data: map[string]any{fieldMsg: "b"}},
	})
	m := New(bundle)
	m.SetPoster(&fakePoster{})
	m.store = s // HandleFilterChunk is a *Model method; route through it.
	msg := FilterChunkMsg{Instance: "test-instance", ID: s.queryID, Epoch: s.filterEpoch, Offset: 0, Hidden: []bool{true, false}}
	m.HandleFilterChunk(msg)
	assert.True(t, s.state.filtered[0])
	assert.False(t, s.state.filtered[1])
}

// TestHandleFilterChunk_DropsStaleQueryID is the regression guard for the
// jqchunk stale-chunk panic class: a chunk whose ID no longer matches queryID
// (store was cleared) must be dropped, never indexing the emptied logs.
func TestHandleFilterChunk_DropsStaleQueryID(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "a"}}})
	m := New(bundle)
	m.SetPoster(&fakePoster{})
	m.store = s // HandleFilterChunk is a *Model method; route through it.
	staleID := s.queryID
	s.ClearData() // bumps queryID, nils s.logs
	// A late worker carrying the pre-clear queryID with an out-of-range offset.
	msg := FilterChunkMsg{Instance: "test-instance", ID: staleID, Offset: 99, Hidden: []bool{true, true}}
	assert.NotPanics(t, func() { m.HandleFilterChunk(msg) }, "stale chunk must be dropped, not panic")
	assert.Empty(t, s.state.filtered, "stale chunk writes nothing")
}

// TestHandleFilterChunk_DropsStaleEpoch is the regression guard for the C1 epoch
// guard: a jq re-run or filter recompute bumps filterEpoch WITHOUT bumping
// queryID, so a worker that computed against superseded data/rules carries a
// matching queryID but a stale Epoch and must be dropped (queryID alone cannot
// catch it).
func TestHandleFilterChunk_DropsStaleEpoch(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "a"}}, {Data: map[string]any{fieldMsg: "b"}}})
	m := New(bundle)
	m.SetPoster(&fakePoster{})
	m.store = s // HandleFilterChunk is a *Model method; route through it.
	// A jq re-run / filter recompute has since bumped filterEpoch (queryID
	// unchanged). The late worker carries the pre-bump epoch.
	staleEpoch := s.filterEpoch
	s.filterEpoch++
	msg := FilterChunkMsg{Instance: "test-instance", ID: s.queryID, Epoch: staleEpoch, Offset: 0, Hidden: []bool{true, true}}
	m.HandleFilterChunk(msg)
	assert.Empty(t, s.state.filtered, "stale-epoch chunk dropped, writes nothing")
}

// TestHandleFilterChunk_WrongInstance_Dropped verifies cross-instance chunks are
// ignored.
func TestHandleFilterChunk_WrongInstance_Dropped(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "a"}}})
	m := New(bundle)
	m.SetPoster(&fakePoster{})
	m.store = s // HandleFilterChunk is a *Model method; route through it.
	m.HandleFilterChunk(FilterChunkMsg{Instance: "other", ID: s.queryID, Offset: 0, Hidden: []bool{true}})
	assert.Empty(t, s.state.filtered, "wrong-instance chunk ignored")
}

// TestHandleJQChunk_SpawnsFilterChunk verifies the jq-active path: HandleJQChunk
// sequences a filter chunk AFTER applying the jq result (the dispatch loop never
// spawns a filter pass while jq is active — Task 11). The spawned chunk reflects
// the jq-rewritten data.
func TestHandleJQChunk_SpawnsFilterChunk(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "error"}})
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "raw0"}},
		{Data: map[string]any{fieldMsg: "raw1"}},
	})
	// jq rewrites both entries; result 0 now matches "error", result 1 does not.
	// JQChunkMsg.Results is []any; use map[string]any values as any.
	msg := JQChunkMsg{
		Instance: s.identity, ID: s.queryID, Offset: 0,
		Results: []any{map[string]any{fieldMsg: "error here"}, map[string]any{fieldMsg: "clean"}},
	}
	s.HandleJQChunk(msg)

	chunks := fp.filterChunks()
	require.Len(t, chunks, 1, "jq-active completion spawns one filter chunk")
	assert.Equal(t, []bool{false, true}, chunks[0].Hidden,
		"filter evaluated over jq-rewritten data: log0 visible, log1 hidden")
}

// TestDispatchPendingBatches_SpawnsFilterChunk verifies the streaming dispatch
// fires an independent filter chunk when rules are set, in addition to (or
// independent of) the jq/search branch.
func TestDispatchPendingBatches_SpawnsFilterChunk(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "error"}})
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	// Simulate streaming: append 2 logs (one full batch) and dispatch.
	s.StartStream(int(icl.SyncQueryLimit))
	s.HandleLogStreamMsg(&msgs.LogStreamMsg{
		ID: s.queryID,
		Logs: []icl.Log{
			{Data: map[string]any{fieldMsg: "error one"}},
			{Data: map[string]any{fieldMsg: "clean"}},
		},
	})

	chunks := fp.filterChunks()
	require.Len(t, chunks, 1, "one full batch -> one filter chunk")
	assert.Equal(t, []bool{false, true}, chunks[0].Hidden,
		"include error: log0 visible, log1 hidden")
}

// TestDispatchPendingBatches_NoRules_NoFilterChunk verifies no filter chunk is
// spawned when the rule set is empty.
func TestDispatchPendingBatches_NoRules_NoFilterChunk(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.StartStream(int(icl.SyncQueryLimit))
	s.HandleLogStreamMsg(&msgs.LogStreamMsg{
		ID:   s.queryID,
		Logs: []icl.Log{{Data: map[string]any{fieldMsg: "a"}}, {Data: map[string]any{fieldMsg: "b"}}},
	})
	assert.Empty(t, fp.filterChunks(), "no rules -> no filter chunk")
}

// TestSetLogsRaw_AppliesFilters verifies the reapply pair recomputes filtered
// from the surviving rule set when logs are (re)loaded.
func TestSetLogsRaw_AppliesFilters(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "keep"}})
	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.SetLogsRaw([]icl.Log{
		{Data: map[string]any{fieldMsg: "keep this"}},
		{Data: map[string]any{fieldMsg: "drop this"}},
	})
	assert.False(t, s.state.filtered[0], "matching log kept")
	assert.True(t, s.state.filtered[1], "non-matching log hidden")
	assert.Equal(t, []filter.Rule{{Type: filter.Include, Value: "keep"}}, s.appliedFilters)
}

// TestSetLogs_AppliesFilters verifies the full-clear reapply path filters too.
func TestSetLogs_AppliesFilters(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Exclude, Value: "noise"}})
	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.SetLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "signal"}},
		{Data: map[string]any{fieldMsg: "noise here"}},
	})
	assert.False(t, s.state.filtered[0])
	assert.True(t, s.state.filtered[1], "excluded log hidden")
}

// runAll executes every captured worker fn in order (filter_test helper).
func (p *deferredPoster) runAll() {
	p.mu.Lock()
	fns := append([]func(context.Context){}, p.fns...)
	p.mu.Unlock()
	for _, fn := range fns {
		fn(context.Background())
	}
}

// filterChunks returns the FilterChunkMsg events captured (filter_test helper).
func (p *deferredPoster) filterChunks() []FilterChunkMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]FilterChunkMsg, 0, len(p.posted))
	for _, ev := range p.posted {
		if fcm, ok := ev.(FilterChunkMsg); ok {
			out = append(out, fcm)
		}
	}
	return out
}

// TestFilterChunk_RacesClear_QueryIDGuard verifies a filter worker that posts
// after a ClearData carries a stale queryID, so HandleFilterChunk drops it and
// never indexes the emptied logs (the jqchunk stale-chunk panic regression).
func TestFilterChunk_RacesClear_QueryIDGuard(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "x"}})
	s := NewLogStore(bundle, nil, "i")
	dp := &deferredPoster{}
	s.SetPoster(dp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "xx"}}, {Data: map[string]any{fieldMsg: "yy"}}})
	s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cancelFilter registered with store; CancelAll cleans up
	t.Cleanup(s.cancelFilter)

	// Spawn the worker (captured, not run), then clear the store (bumps queryID,
	// nils s.logs), THEN run the worker so it posts with the now-stale queryID.
	// batchLogs (s.logs[:2]) is captured by the closure before ClearData nils
	// s.logs, so the worker still reads the old backing array and posts.
	s.spawnFilterChunk(bundle.State.Filters(), 0, s.logs[:2])
	s.ClearData()
	dp.runAll()

	chunks := dp.filterChunks()
	require.Len(t, chunks, 1, "worker still posts (it cannot recall itself)")

	m := New(bundle)
	m.SetPoster(&fakePoster{})
	m.SetStore(s)
	assert.NotPanics(t, func() { m.HandleFilterChunk(chunks[0]) },
		"stale filter chunk must be dropped by the queryID guard")
	assert.Empty(t, s.state.filtered, "stale chunk wrote nothing")
}

// TestFilterWorker_RacesApplyJQ is the regression guard for the fixed Critical
// finding C1. A streaming filter worker runs off-loop while ApplyJQ (a jq-query
// change) overwrites s.logs ON-loop. The fix: spawnFilterChunk slices.Clone()s
// the batch on the loop so the worker reads an isolated copy, never the live
// s.logs backing array — eliminating the data race structurally (ApplyJQ bumping
// filterEpoch + cancelling cancelFilter additionally drops the now-stale chunk).
// This must pass under `go test -race`; before the clone it flagged a data race
// on the entry backing array.
func TestFilterWorker_RacesApplyJQ(t *testing.T) {
	bundle := depstest.NewTest(t)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "x"}})
	s := NewLogStore(bundle, nil, "i")
	bp := newBlockingPoster()
	defer bp.stop()
	s.SetPoster(bp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "xx"}}, {Data: map[string]any{fieldMsg: "yy"}}})
	s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cleaned up below
	t.Cleanup(s.cancelFilter)

	// Off-loop filter worker (real goroutine via blockingPoster). Pre-fix it read
	// the live s.logs backing array; post-fix spawnFilterChunk clones the batch on
	// the loop, so the worker reads an isolated copy.
	s.spawnFilterChunk(bundle.State.Filters(), 0, s.logs[:2])
	// On-loop ApplyJQ empty-query path overwrites every s.logs entry, lockless.
	// Pre-fix this raced the worker's read of the aliased backing array; post-fix
	// the writes hit disjoint memory (s.logs) from the worker's clone, so -race is
	// clean. ApplyJQ also bumps filterEpoch + cancels cancelFilter.
	bundle.State.SetJQ("")
	s.ApplyJQ()
	// Wait for the worker to finish its read loop and reach PostCritical (where it
	// closes posting, then blocks on release). This guarantees its read of the
	// batch overlapped the ApplyJQ write above — the interleaving -race must
	// observe as clean post-fix (and flagged pre-fix). The deferred bp.stop()
	// then releases the blocked worker so its goroutine exits.
	<-bp.posting
}
