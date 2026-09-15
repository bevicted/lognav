package logviewer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePoster is a test double for msgs.Poster. Go runs fn inline (no real
// goroutine, so goleak stays clean) and PostCritical records every delivered
// event for later assertions.
type fakePoster struct {
	mu     sync.Mutex
	posted []uv.Event
}

func (f *fakePoster) Go(fn func(context.Context)) { fn(context.Background()) }

func (f *fakePoster) PostCritical(_ context.Context, ev uv.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, ev)
	return nil
}

func (f *fakePoster) Post(uv.Event) {}

// collected returns a snapshot of every event delivered via PostCritical.
func (f *fakePoster) collected() []uv.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uv.Event(nil), f.posted...)
}

// searchChunks returns the SearchChunkMsg events captured so far.
func (f *fakePoster) searchChunks() []SearchChunkMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SearchChunkMsg, 0, len(f.posted))
	for _, ev := range f.posted {
		if scm, ok := ev.(SearchChunkMsg); ok {
			out = append(out, scm)
		}
	}
	return out
}

// jqChunks returns the JQChunkMsg events captured so far.
func (f *fakePoster) jqChunks() []JQChunkMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]JQChunkMsg, 0, len(f.posted))
	for _, ev := range f.posted {
		if jcm, ok := ev.(JQChunkMsg); ok {
			out = append(out, jcm)
		}
	}
	return out
}

func newTestStore(t *testing.T, logs []icl.Log) *LogStore {
	t.Helper()
	s := NewLogStore(depstest.NewTest(t), nil, "test-instance")
	s.SetPoster(&fakePoster{}) // search dispatch needs a poster; results discarded
	if len(logs) == 0 {
		return s
	}
	s.SetLogsRaw(logs) // jq/search run for effect via the poster
	return s
}

func TestLogStore_ChunkFailureLogsDisplayName(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))

	const (
		crn         = "crn:v1:bluemix:public:logs:us-south:a/account:instance::"
		displayName = "production-logs"
	)
	store := NewLogStoreWithIdentity(depstest.NewTest(t), nil, crn, displayName)
	store.HandleSearchChunk(SearchChunkMsg{Instance: crn, ID: store.queryID, Err: errors.New("chunk failed")})

	assert.Contains(t, buf.String(), "instance="+displayName)
	assert.NotContains(t, buf.String(), crn)
}

func TestReadLog(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{fieldMsg: "first"}, Metadata: icl.Metadata{Severity: icl.SeverityInfo, TSMicro: 1000}},
		{Data: map[string]any{fieldMsg: "second"}, Metadata: icl.Metadata{Severity: icl.SeverityError, TSMicro: 2000}},
		{Data: map[string]any{fieldMsg: "third"}, Metadata: icl.Metadata{Severity: icl.SeverityCritical, TSMicro: 3000}},
	}
	tests := []struct {
		name    string
		idx     int
		wantOK  bool
		wantMsg string
	}{
		{name: "valid first", idx: 0, wantOK: true, wantMsg: "first"},
		{name: "valid last", idx: 2, wantOK: true, wantMsg: "third"},
		{name: "out of bounds high", idx: 3, wantOK: false},
		{name: "out of bounds negative", idx: -1, wantOK: false},
	}
	store := newTestStore(t, logs)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, ok := store.ReadLog(tt.idx)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantMsg, log.Data[fieldMsg])
			}
		})
	}
}

func TestReadLogs(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{"i": 0}},
		{Data: map[string]any{"i": 1}},
		{Data: map[string]any{"i": 2}},
		{Data: map[string]any{"i": 3}},
		{Data: map[string]any{"i": 4}},
	}
	store := newTestStore(t, logs)
	tests := []struct {
		name    string
		indices []int
		want    int
	}{
		{name: "subset", indices: []int{0, 2, 4}, want: 3},
		{name: "all", indices: []int{0, 1, 2, 3, 4}, want: 5},
		{name: "with out of bounds", indices: []int{0, 10, 2}, want: 2},
		{name: "empty indices", indices: []int{}, want: 0},
		{name: "all out of bounds", indices: []int{10, 20}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := store.ReadLogs(tt.indices)
			assert.Len(t, result, tt.want)
		})
	}
}

func TestGetSeverity(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{}, Metadata: icl.Metadata{Severity: icl.SeverityInfo}},
		{Data: map[string]any{}, Metadata: icl.Metadata{Severity: icl.SeverityError}},
		{Data: map[string]any{}, Metadata: icl.Metadata{Severity: icl.SeverityCritical}},
	}
	store := newTestStore(t, logs)
	tests := []struct {
		name string
		idx  int
		want icl.Severity
		ok   bool
	}{
		{name: "info", idx: 0, want: icl.SeverityInfo, ok: true},
		{name: matchError, idx: 1, want: icl.SeverityError, ok: true},
		{name: "out of bounds", idx: 5, want: icl.SeverityUnknown, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sev, ok := store.GetSeverity(tt.idx)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, sev)
		})
	}
}

func TestGetTimeRange(t *testing.T) {
	t.Parallel()
	t.Run("with logs", func(t *testing.T) {
		logs := []icl.Log{
			{Data: map[string]any{}, Metadata: icl.Metadata{TSMicro: 1000}},
			{Data: map[string]any{}, Metadata: icl.Metadata{TSMicro: 5000}},
		}
		store := newTestStore(t, logs)
		earliest, latest := store.GetTimeRange()
		require.Equal(t, int64(1000), earliest)
		require.Equal(t, int64(5000), latest)
	})
	t.Run("empty store", func(t *testing.T) {
		store := newTestStore(t, nil)
		earliest, latest := store.GetTimeRange()
		assert.Equal(t, int64(0), earliest)
		assert.Equal(t, int64(0), latest)
	})
}

func TestIterLogMeta(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Metadata: icl.Metadata{TSMicro: 1000, Severity: icl.SeverityInfo}, Data: map[string]any{"i": 0}},
		{Metadata: icl.Metadata{TSMicro: 2000, Severity: icl.SeverityError}, Data: map[string]any{"i": 1}},
		{Metadata: icl.Metadata{TSMicro: 3000, Severity: icl.SeverityCritical}, Data: map[string]any{"i": 2}},
	}
	store := newTestStore(t, logs)

	var (
		idxs []int
		tss  []int64
		sevs []icl.Severity
	)
	for m := range store.IterLogMeta() {
		idxs = append(idxs, m.Idx)
		tss = append(tss, m.TSMicro)
		sevs = append(sevs, m.Severity)
	}

	assert.Equal(t, []int{0, 1, 2}, idxs)
	assert.Equal(t, []int64{1000, 2000, 3000}, tss)
	assert.Equal(t, []icl.Severity{icl.SeverityInfo, icl.SeverityError, icl.SeverityCritical}, sevs)
}

func TestIterLogMetaBypassesFilterState(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Metadata: icl.Metadata{TSMicro: 1000, Severity: icl.SeverityInfo}},
		{Metadata: icl.Metadata{TSMicro: 2000, Severity: icl.SeverityError}},
		{Metadata: icl.Metadata{TSMicro: 3000, Severity: icl.SeverityWarning}},
	}
	store := newTestStore(t, logs)
	store.state.filtered.Set(1, true) // filter out log index 1

	var count int
	for range store.IterLogMeta() {
		count++
	}
	assert.Equal(t, 3, count, "IterLogMeta must yield filtered logs too")
}

func TestClearData_PreservesVisualState(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{fieldMsg: "a"}, Metadata: icl.Metadata{TSMicro: 1000}},
		{Data: map[string]any{fieldMsg: "b"}, Metadata: icl.Metadata{TSMicro: 2000}},
		{Data: map[string]any{fieldMsg: "c"}, Metadata: icl.Metadata{TSMicro: 3000}},
	}
	store := newTestStore(t, logs)

	// Set visual state.
	store.state.expanded.Set(0, true)
	store.state.expanded.Set(2, true)
	store.state.filtered.Set(1, true)

	// Set data state that should be cleared.
	store.state.marked.Set(0, true)

	store.ClearData()

	// Visual state preserved.
	assert.True(t, store.state.expanded[0], "expanded[0] should persist")
	assert.True(t, store.state.expanded[2], "expanded[2] should persist")
	assert.True(t, store.state.filtered[1], "filtered[1] should persist")

	// Data state cleared.
	assert.Empty(t, store.state.marked, "marked should be cleared")
	assert.Empty(t, store.state.search, "search should be cleared")
	assert.Equal(t, 0, store.GetLogCount(), "logCount should be 0")
	assert.Nil(t, store.logs, "logs should be nil")
	assert.Empty(t, store.appliedJQ, "appliedJQ should be empty")
	assert.Empty(t, store.appliedSearch, "appliedSearch should be empty")
	assert.Equal(t, 0, store.jqCount, "jqCount should be 0")
	assert.Equal(t, 0, store.searchCount, "searchCount should be 0")
}

func TestClear_ClearsEverything(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{fieldMsg: "a"}, Metadata: icl.Metadata{TSMicro: 1000}},
	}
	store := newTestStore(t, logs)

	store.state.expanded.Set(0, true)
	store.state.filtered.Set(0, true)

	store.Clear()

	assert.Empty(t, store.state.expanded, "expanded should be cleared")
	assert.Empty(t, store.state.filtered, "filtered should be cleared")
	assert.Equal(t, 0, store.GetLogCount())
	assert.Equal(t, 0, store.jqCount, "jqCount should be 0")
	assert.Equal(t, 0, store.searchCount, "searchCount should be 0")
}

func TestSetLogsRaw_PreservesVisualState(t *testing.T) {
	t.Parallel()
	logs := []icl.Log{
		{Data: map[string]any{fieldMsg: "a"}, Metadata: icl.Metadata{TSMicro: 1000}},
		{Data: map[string]any{fieldMsg: "b"}, Metadata: icl.Metadata{TSMicro: 2000}},
	}
	store := newTestStore(t, logs)

	// Set visual state before reload.
	store.state.expanded.Set(0, true)
	// Note: filtered is no longer preserved by SetLogsRaw — it is re-derived from
	// state.Filters() by ApplyFilters (Task 14). expanded is still preserved.

	// Simulate reload with same logs.
	store.SetLogsRaw(logs)

	// expanded state preserved; filtered is re-derived (no rules -> nothing hidden).
	assert.True(t, store.state.expanded[0], "expanded[0] should persist through SetLogsRaw")
	assert.Empty(t, store.state.filtered, "no rules set -> filtered re-derived as empty")

	// Logs reloaded.
	assert.Equal(t, 2, store.GetLogCount())
	log, ok := store.ReadLog(0)
	require.True(t, ok)
	assert.Equal(t, "a", log.Data[fieldMsg])
}

func TestHandleSearchChunkWithNilCancelSearch(t *testing.T) {
	t.Parallel()
	// Regression test: HandleSearchChunk must not panic when s.cancelSearch is
	// nil. This happens when HandleJQChunk dispatches a search chunk via
	// context.Background() without ever initializing cancelSearch (the
	// jq-first path in dispatchPendingBatches skips cancelSearch setup).
	logs := []icl.Log{
		{Metadata: icl.Metadata{TSMicro: 1000, Severity: icl.SeverityInfo}},
		{Metadata: icl.Metadata{TSMicro: 2000, Severity: icl.SeverityError}},
		{Metadata: icl.Metadata{TSMicro: 3000, Severity: icl.SeverityWarning}},
	}
	store := newTestStore(t, logs)
	// Sanity: cancelSearch was never set because DoSearch was never called.
	require.Nil(t, store.cancelSearch)
	require.False(t, store.streaming)
	require.Equal(t, 3, store.GetLogCount())

	// Simulate a search chunk arriving that covers the whole store. This
	// makes searchCount >= logCount inside HandleSearchChunk and hits the
	// unguarded cancelSearch() call at the completion branch.
	msg := SearchChunkMsg{
		Instance: "test-instance",
		ID:       store.GetQueryID(), // current epoch, so the guard accepts it
		Offset:   0,
		Results:  SearchMap{},
		Err:      nil,
	}

	assert.NotPanics(t, func() {
		_ = store.HandleSearchChunk(msg)
	})
}

// TestDoSearch_PostsChunksViaPoster verifies the R4 conversion: DoSearch
// spawns per-chunk search workers through the poster instead of returning
// tea.Cmds. Each worker posts a SearchChunkMsg (carrying the store instance
// and globally-offset results) which HandleSearchChunk accumulates.
func TestDoSearch_PostsChunksViaPoster(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	// Force a small chunk size so multiple chunks are dispatched.
	bundle.Config.Logs.BatchOpChunkSize = 2
	bundle.State.SetSearch("error")

	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "error one"}},     // global 0, chunk 0
		{Data: map[string]any{fieldMsg: "clean"}},         // global 1, chunk 0
		{Data: map[string]any{fieldMsg: "another error"}}, // global 2, chunk 1
		{Data: map[string]any{fieldMsg: "fine"}},          // global 3, chunk 1
	})

	s.DoSearch() // results flow via the poster, not a return value

	chunks := fp.searchChunks()
	require.Len(t, chunks, 2, "two chunks of size 2 over four logs")

	// Every posted chunk targets this store's instance (epoch identity).
	for _, c := range chunks {
		assert.Equal(t, "test-instance", c.Instance)
	}

	// Feed every posted chunk back through HandleSearchChunk and assert the
	// accumulated match set covers exactly the two "error" logs at global 0 and 2.
	for _, c := range chunks {
		_ = s.HandleSearchChunk(c)
	}
	assert.Contains(t, s.state.search, 0, "global log 0 matched")
	assert.Contains(t, s.state.search, 2, "global log 2 matched")
	assert.NotContains(t, s.state.search, 1)
	assert.NotContains(t, s.state.search, 3)
}

// TestDoSearch_CancelledEpoch_SuppressesPosts verifies the search epoch ctx
// (s.cancelSearch) gates worker delivery: cancelling it before the workers run
// makes searchChunk return ok=false, so nothing is posted.
func TestDoSearch_CancelledEpoch_SuppressesPosts(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	bundle.State.SetSearch("error")

	s := NewLogStore(bundle, nil, "test-instance")
	// Poster that cancels the search epoch before invoking the worker, so the
	// per-chunk ctx.Done() guard in searchChunk trips.
	cp := &cancelBeforeRunPoster{store: s}
	s.SetPoster(cp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "error one"}}})

	s.DoSearch()
	assert.Empty(t, cp.posted, "cancelled search epoch suppresses all posts")
}

// cancelBeforeRunPoster cancels the store's search epoch immediately before
// running each worker, exercising searchChunk's pre-cancelled ctx path.
type cancelBeforeRunPoster struct {
	store  *LogStore
	posted []uv.Event
}

func (p *cancelBeforeRunPoster) Go(fn func(context.Context)) {
	if p.store.cancelSearch != nil {
		p.store.cancelSearch()
	}
	fn(context.Background())
}

func (p *cancelBeforeRunPoster) PostCritical(_ context.Context, ev uv.Event) error {
	p.posted = append(p.posted, ev)
	return nil
}

func (p *cancelBeforeRunPoster) Post(uv.Event) {}

// TestHandleSearchChunk_EpochFiltering verifies a SearchChunkMsg tagged for a
// different instance is discarded (stale-epoch filtering preserved by the
// poster conversion).
func TestHandleSearchChunk_EpochFiltering(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, []icl.Log{{Data: map[string]any{fieldMsg: "error"}}})

	// A chunk for a different instance must not mutate this store's search map.
	stale := SearchChunkMsg{Instance: "other-instance", Offset: 0, Results: SearchMap{0: {{line: 0, start: 0, end: 5}}}}
	_ = s.HandleSearchChunk(stale)
	assert.NotContains(t, s.state.search, 0, "stale-instance chunk must be ignored")
}

func TestApplyJQ_ResetsStateOnParseFailure(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetJQ("not a jq expression at all (((")
	s := NewLogStore(bundle, nil, "test")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.SetLogs([]icl.Log{
		{Data: map[string]any{"k": "v"}, Metadata: icl.Metadata{Severity: icl.SeverityInfo}},
	})
	// Simulate a prior-applied jq so the reset is observable.
	s.appliedJQ = "stale"
	s.jqCount = 99

	s.ApplyJQ()
	assert.Empty(t, s.appliedJQ)
	assert.Equal(t, 0, s.jqCount)
	// Parse failure short-circuits on the loop goroutine: no chunk worker spawned.
	assert.Empty(t, fp.jqChunks(), "parse failure spawns no jq chunk worker")
}

// TestApplyJQ_PostsChunksViaPoster verifies the R4 conversion: ApplyJQ spawns
// per-chunk jq workers through the poster instead of returning tea.Cmds. Each
// worker posts a JQChunkMsg (carrying the store instance and the chunk offset)
// which HandleJQChunk applies on the loop goroutine.
func TestApplyJQ_PostsChunksViaPoster(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	// Force a small chunk size so multiple chunks are dispatched.
	bundle.Config.Logs.BatchOpChunkSize = 2
	bundle.State.SetJQ(".msg")

	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "one"}},   // global 0, chunk 0
		{Data: map[string]any{fieldMsg: "two"}},   // global 1, chunk 0
		{Data: map[string]any{fieldMsg: "three"}}, // global 2, chunk 1
		{Data: map[string]any{fieldMsg: "four"}},  // global 3, chunk 1
	})

	s.ApplyJQ()

	chunks := fp.jqChunks()
	require.Len(t, chunks, 2, "two chunks of size 2 over four logs")

	// Every posted chunk targets this store's instance (epoch identity) and the
	// offsets cover both chunks.
	offsets := make([]int, 0, len(chunks))
	for _, c := range chunks {
		assert.Equal(t, "test-instance", c.Instance)
		require.NoError(t, c.Err)
		offsets = append(offsets, c.Offset)
	}
	assert.ElementsMatch(t, []int{0, 1}, offsets)

	// Feed every posted chunk back through HandleJQChunk and assert the data
	// write-back stored each chunk's jq result into the entry's modifiedData
	// (the original data is preserved).
	for _, c := range chunks {
		s.HandleJQChunk(c)
	}
	for i, want := range []string{"one", "two", "three", "four"} {
		_, modified, unlock := s.logs[i].GetData()
		assert.Equal(t, want, modified, "jq result written to modifiedData for log %d", i)
		unlock()
	}
}

// TestHandleJQChunk_EpochFiltering verifies a JQChunkMsg tagged for a different
// instance is discarded (stale-epoch filtering preserved by the poster
// conversion). JQChunkMsg.Instance is the epoch field.
func TestHandleJQChunk_EpochFiltering(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "original"}}})

	// A chunk for a different instance must not write back over this store.
	stale := JQChunkMsg{Instance: "other-instance", Offset: 0, Results: []any{"clobbered"}}
	result := s.HandleJQChunk(stale)
	assert.Empty(t, result.CacheInvalidated, "stale-instance chunk applies no writes")
	_, modified, unlock := s.logs[0].GetData()
	unlock()
	assert.Nil(t, modified, "stale-instance chunk must not mutate entry data")
}

// deferredPoster captures worker fns instead of running them inline, so a test
// can drive their execution and probe state mid-flight. PostCritical records the
// delivered events; last() returns the most recent (nil if none).
type deferredPoster struct {
	mu     sync.Mutex
	fns    []func(context.Context)
	posted []uv.Event
}

func (p *deferredPoster) Go(fn func(context.Context)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fns = append(p.fns, fn)
}

func (p *deferredPoster) PostCritical(_ context.Context, ev uv.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posted = append(p.posted, ev)
	return nil
}

func (p *deferredPoster) Post(uv.Event) {}

// blockingPoster runs each worker on a real goroutine and makes PostCritical
// block (as the real unbuffered events channel does when the loop is busy
// drawing and not yet receiving). It signals once the first worker reaches the
// post. stop() unblocks every blocked PostCritical so no goroutine leaks.
type blockingPoster struct {
	posting chan struct{} // closed once the first worker reaches PostCritical
	release chan struct{} // closed by stop() to unblock every blocked post
	once    sync.Once
}

func newBlockingPoster() *blockingPoster {
	return &blockingPoster{posting: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingPoster) Go(fn func(context.Context)) { go fn(context.Background()) }

func (b *blockingPoster) Post(uv.Event) {}

func (b *blockingPoster) PostCritical(_ context.Context, _ uv.Event) error {
	b.once.Do(func() { close(b.posting) })
	<-b.release // block like the loop-not-receiving case until stop() releases
	return nil
}

func (b *blockingPoster) stop() { close(b.release) }

// TestApplyJQ_LocklessWorkerDoesNotBlockReadLog is a regression test for the
// instance-load deadlock (R5a smoke): the per-chunk jq worker holds NO chunk
// lock (it reads a private copy of immutable data), so the Draw path (ReadLog →
// chunk RLock) on the same chunk can read while the worker is blocked inside
// PostCritical (loop busy drawing, not receiving) — the exact state when opening
// the logviewer on a freshly loaded instance with a jq query. A worker that took
// the chunk lock across the post would deadlock ReadLog here.
func TestApplyJQ_LocklessWorkerDoesNotBlockReadLog(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetJQ(".msg")
	s := NewLogStore(bundle, nil, "test-instance")
	bp := newBlockingPoster()
	t.Cleanup(bp.stop)
	s.SetPoster(bp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "x"}}})

	s.ApplyJQ() // spawns one chunk worker on a real goroutine

	// Wait until the worker has finished compute and is blocked inside PostCritical.
	select {
	case <-bp.posting:
	case <-time.After(2 * time.Second):
		t.Fatal("jq worker never reached PostCritical")
	}

	// The Draw path must be able to read the chunk while the worker is blocked
	// posting. A worker holding the chunk lock across the post would deadlock here.
	done := make(chan struct{})
	go func() {
		_, _ = s.ReadLog(0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: ReadLog blocked while the jq worker was posting (worker must hold no chunk lock)")
	}
}

// TestApplyJQ_CancelledEpoch_SuppressesResults verifies the jq epoch ctx
// (s.cancelJQ) gates worker output: cancelling it before the workers run makes
// jqBatch return a context error, so the posted chunk carries Err and
// HandleJQChunk treats it as a cancelled chunk (no write-back).
func TestApplyJQ_CancelledEpoch_SuppressesResults(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetJQ(".msg")
	s := NewLogStore(bundle, nil, "test-instance")
	cp := &cancelJQBeforeRunPoster{store: s}
	s.SetPoster(cp)
	s.loadLogs([]icl.Log{{Data: map[string]any{fieldMsg: "cancelled"}}})

	s.ApplyJQ()

	require.Len(t, cp.posted, 1, "one chunk posted")
	jcm, ok := cp.posted[0].(JQChunkMsg)
	require.True(t, ok)
	require.Error(t, jcm.Err, "cancelled jq epoch yields a context error")

	// HandleJQChunk must treat the cancelled chunk as a no-op write-back.
	result := s.HandleJQChunk(jcm)
	assert.Empty(t, result.CacheInvalidated, "cancelled chunk applies no writes")
}

// cancelJQBeforeRunPoster cancels the store's jq epoch immediately before
// running each worker, exercising the jqBatch context-cancelled path.
type cancelJQBeforeRunPoster struct {
	store  *LogStore
	posted []uv.Event
}

func (p *cancelJQBeforeRunPoster) Go(fn func(context.Context)) {
	if p.store.cancelJQ != nil {
		p.store.cancelJQ()
	}
	fn(context.Background())
}

func (p *cancelJQBeforeRunPoster) PostCritical(_ context.Context, ev uv.Event) error {
	p.posted = append(p.posted, ev)
	return nil
}

func (p *cancelJQBeforeRunPoster) Post(uv.Event) {}

func TestDispatchPendingBatches_ResetsStateOnParseFailure(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetJQ("not a jq expression at all (((")
	s := NewLogStore(bundle, nil, "test")
	s.StartStream(int(icl.SyncQueryLimit))
	qid := s.GetQueryID()
	s.HandleLogStreamMsg(&msgs.LogStreamMsg{
		CRN: "test",
		ID:  qid,
		Logs: []icl.Log{
			{Data: map[string]any{"k": "v"}, Metadata: icl.Metadata{Severity: icl.SeverityInfo}},
		},
	})
	// Force the jq parse path with a bad query and a streaming-done event so
	// dispatchPendingBatches drains.

	s.appliedJQ = "stale"
	s.jqCount = 99

	s.HandleLogStreamDoneMsg(&msgs.LogStreamDoneMsg{CRN: "test", ID: qid})

	assert.Empty(t, s.appliedJQ, "appliedJQ reset on parse failure during dispatch")
	assert.Equal(t, 0, s.jqCount)
	assert.Nil(t, s.cancelJQ, "cancelJQ nilled on parse failure")
	assert.Nil(t, s.jqCtx, "jqCtx nilled on parse failure")
}

func TestHandleLogStreamMsg_TracksOutOfOrderTimeRange(t *testing.T) {
	t.Parallel()
	store := NewLogStore(depstest.NewTest(t), nil, "test-instance")
	store.StartStream(3)
	store.HandleLogStreamMsg(&msgs.LogStreamMsg{
		ID: store.GetQueryID(),
		Logs: []icl.Log{
			{Metadata: icl.Metadata{TSMicro: 9_000}},
			{Metadata: icl.Metadata{TSMicro: 1_000}},
			{Metadata: icl.Metadata{TSMicro: 5_000}},
		},
	})

	earliest, latest := store.GetTimeRange()
	assert.Equal(t, int64(1_000), earliest)
	assert.Equal(t, int64(9_000), latest)
}

func TestHandleLogStreamMsg_NoPanicAfterReloadDuringStream(t *testing.T) {
	t.Parallel()
	// Regression: when a streaming store gets reloaded mid-flight (the
	// instance-picker lazy-load path calls SetLogsRaw), any LogStreamMsg
	// still tagged with the pre-reload queryID must be filtered, not
	// processed against the now-truncated logs slice. Otherwise
	// pendingBatchCount overflows logCount and dispatchPendingBatches
	// panics with negative slice bounds.
	store := NewLogStore(depstest.NewTest(t), nil, "test-instance")
	store.StartStream(int(icl.SyncQueryLimit))
	qid := store.GetQueryID()

	// Lazy-load clobbers the streaming store with a small snapshot.
	store.SetLogsRaw([]icl.Log{
		{Data: map[string]any{fieldMsg: "a"}, Metadata: icl.Metadata{TSMicro: 1000}},
		{Data: map[string]any{fieldMsg: "b"}, Metadata: icl.Metadata{TSMicro: 2000}},
	})

	// In-flight stream message arrives with the pre-reload queryID and a
	// batch larger than the new logCount.
	streamMsg := &msgs.LogStreamMsg{
		CRN:  "test-instance",
		ID:   qid,
		Logs: make([]icl.Log, 50),
	}
	assert.NotPanics(t, func() {
		store.HandleLogStreamMsg(streamMsg)
	})
}

func TestLogStore_Close_CancelsJQAndSearch(t *testing.T) {
	t.Parallel()

	s := newTestStore(t, nil)

	// Install dummy cancel funcs so we can observe nilling.
	jqCancelled := false
	searchCancelled := false
	s.cancelJQ = func() { jqCancelled = true }
	s.cancelSearch = func() { searchCancelled = true }

	require.NoError(t, s.Close())
	assert.True(t, jqCancelled, "cancelJQ should have been invoked")
	assert.True(t, searchCancelled, "cancelSearch should have been invoked")
	assert.Nil(t, s.cancelJQ, "cancelJQ should be nil after Close")
	assert.Nil(t, s.cancelSearch, "cancelSearch should be nil after Close")

	t.Run("second_call_is_noop", func(t *testing.T) {
		require.NoError(t, s.Close())
		assert.Nil(t, s.cancelJQ)
		assert.Nil(t, s.cancelSearch)
	})
}

func TestBuildSearchAC_PatternsIncludeNewline(t *testing.T) {
	t.Parallel()
	ac := buildSearchAC("error")
	corpus := []byte("ok\nerror\nwarn\n")
	it := ac.IterByte(corpus)
	var patterns []int
	for m := it.Next(); m != nil; m = it.Next() {
		patterns = append(patterns, m.Pattern())
	}
	// Pattern 0 == "\n"; expect 3 newlines.
	newlineCount := 0
	for _, p := range patterns {
		if p == 0 {
			newlineCount++
		}
	}
	assert.Equal(t, 3, newlineCount)
}

func TestBuildSearchAC_EmptyQuery_FilteredOut(t *testing.T) {
	t.Parallel()
	// Empty query → only the "\n" pattern survives; no panic from
	// zero-length AC patterns.
	ac := buildSearchAC("")
	corpus := []byte("a\nb")
	it := ac.IterByte(corpus)
	for m := it.Next(); m != nil; m = it.Next() {
		assert.Equal(t, 0, m.Pattern(), "only \\n pattern expected")
	}
}

// TestBuildSearchAC_ConcurrentIterByte_NoDataRace (D23) locks in the
// claim that ahocorasick.AhoCorasick is safe to share across goroutines.
// Run under `go test -race` (part of `just ci`).
func TestBuildSearchAC_ConcurrentIterByte_NoDataRace(t *testing.T) {
	t.Parallel()
	ac := buildSearchAC("error")
	const goroutines = 8
	corpora := make([][]byte, goroutines)
	for i := range corpora {
		corpora[i] = fmt.Appendf(nil, "error %d\nwarn %d\ninfo %d\n", i, i, i)
	}
	var wg sync.WaitGroup
	for i := range corpora {
		wg.Add(1)
		go func(corpus []byte) {
			defer wg.Done()
			it := ac.IterByte(corpus)
			count := 0
			for m := it.Next(); m != nil; m = it.Next() {
				count++
			}
			assert.Positive(t, count)
		}(corpora[i])
	}
	wg.Wait()
}

// TestHandleJQChunk_StaleChunkAfterClear_NoPanic is the regression test for the
// jqchunk stale-chunk panic (`index out of range [N] with length 0`). A jq
// worker computes its chunk and stamps the spawn-time queryID; the store is then
// reset by ClearData (nils s.logs, bumps queryID) before the chunk is dispatched.
// The stale, fully-computed chunk must be dropped on its queryID, not indexed
// into the emptied slice. Without the fix, HandleJQChunk panics on s.logs[N].
func TestHandleJQChunk_StaleChunkAfterClear_NoPanic(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2
	bundle.State.SetJQ(".msg")
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "one"}},   // chunk 0
		{Data: map[string]any{fieldMsg: "two"}},   // chunk 0
		{Data: map[string]any{fieldMsg: "three"}}, // chunk 1, offset 1 -> idx 2
		{Data: map[string]any{fieldMsg: "four"}},  // chunk 1
	})

	s.ApplyJQ() // fakePoster runs workers inline; each posts a JQChunkMsg
	chunks := fp.jqChunks()
	require.NotEmpty(t, chunks, "ApplyJQ posts jq chunks")
	for _, c := range chunks {
		require.Equal(t, s.GetQueryID(), c.ID, "chunk stamped with the spawn-time queryID")
		require.NoError(t, c.Err)
	}

	// Store reset mid-flight: logs nilled, queryID bumped. The chunks are stale.
	s.ClearData()
	require.Nil(t, s.logs)

	assert.NotPanics(t, func() {
		for _, c := range chunks {
			res := s.HandleJQChunk(c)
			assert.Empty(t, res.CacheInvalidated, "stale jq chunk applies no write-back")
		}
	})
}

// TestHandleSearchChunk_StaleChunkAfterClear_Dropped verifies the search-path
// counterpart: a search chunk computed before ClearData cannot panic, but
// without the queryID guard it silently writes stale indices into state.search
// (maps.Copy of the stale results). The guard must drop it.
func TestHandleSearchChunk_StaleChunkAfterClear_Dropped(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2
	bundle.State.SetSearch("error")
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "error alpha"}},
		{Data: map[string]any{fieldMsg: "error beta"}},
	})

	s.DoSearch() // fakePoster runs workers inline; each posts a SearchChunkMsg
	chunks := fp.searchChunks()
	require.NotEmpty(t, chunks, "DoSearch posts search chunks")
	for _, c := range chunks {
		require.Equal(t, s.GetQueryID(), c.ID, "chunk stamped with the spawn-time queryID")
	}

	s.ClearData() // bumps queryID; resets state.search

	for _, c := range chunks {
		assert.False(t, s.HandleSearchChunk(c), "stale search chunk rejected")
	}
	assert.Empty(t, s.state.search, "stale search chunk must not write into the cleared search map")
}

// TestSearchChunk_DoesNotTouchFiltered verifies the search pass no longer writes
// s.state.filtered (filter is now an independent pass; Phase 2). Even with rules
// set, a search chunk must leave the filtered map untouched.
func TestSearchChunk_DoesNotTouchFiltered(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetSearch("error")
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "error"}})
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "error one"}},
		{Data: map[string]any{fieldMsg: "clean"}},
	})

	msg := SearchChunkMsg{
		Instance: "test-instance",
		ID:       s.queryID,
		Offset:   0,
		Results:  SearchMap{0: {{line: 0, start: 0, end: 5}}},
	}
	_ = s.HandleSearchChunk(msg)

	assert.Empty(t, s.state.filtered,
		"search pass must not write s.state.filtered (filter is independent)")
}

// TestJQChunk_DoesNotTouchFiltered verifies the jq pass no longer writes
// s.state.filtered.
func TestJQChunk_DoesNotTouchFiltered(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.State.SetJQ(".msg")
	bundle.State.SetFilters([]filter.Rule{{Type: filter.HasField, Value: ".msg"}})
	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "one"}},
		{Data: map[string]any{fieldMsg: "two"}},
	})

	msg := JQChunkMsg{Instance: "test-instance", ID: s.queryID, Offset: 0, Results: []any{"one", ""}}
	_ = s.HandleJQChunk(msg)

	assert.Empty(t, s.state.filtered,
		"jq pass must not write s.state.filtered (filter is independent)")
}

// TestAppliedFilters_TracksAppliedRules verifies the appliedFilters snapshot is
// what jq/search completion records (replacing the removed uint8 filter fields).
func TestAppliedFilters_TracksAppliedRules(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	s := NewLogStore(bundle, nil, "test-instance")
	rules := []filter.Rule{{Type: filter.Exclude, Value: "noise"}}
	bundle.State.SetFilters(rules)
	// ClearData must keep appliedFilters re-derivable: it nils nothing rule-related.
	s.appliedFilters = rules
	s.ClearData()
	assert.Nil(t, s.appliedFilters, "ClearData resets the applied-rules snapshot")
}

// TestLoadLogs_Over50k_NoMutexPanic loads more logs than the old fixed 50k /
// 25-chunk mutex pool, exercising the load path (SetLogsRaw -> loadLogs). Before
// per-query sizing, loadLogs indexed s.mutexes[idx/BatchOpChunkSize] against a
// pool pre-seeded to ceil(SyncQueryLimit/BatchOpChunkSize)=25 and panicked with
// index out of range once idx crossed 50000.
func TestLoadLogs_Over50k_NoMutexPanic(t *testing.T) {
	t.Parallel()
	n := int(icl.SyncQueryLimit) + 3*2000 // > 50k, crosses the old fixed 25-chunk pool (BatchOpChunkSize=2000)
	logs := make([]icl.Log, n)
	for i := range logs {
		logs[i] = icl.Log{Metadata: icl.Metadata{TSMicro: int64(i)}, Data: map[string]any{"i": i}}
	}
	s := newTestStore(t, nil)
	s.SetLogsRaw(logs) // -> loadLogs; previously panicked indexing s.mutexes[idx/2000]
	require.Equal(t, n, s.GetLogCount())
	_, ok := s.ReadLog(n - 1)
	require.True(t, ok)
}

// TestHandleLogStream_CapsAtCapacity_NoPanic streams more than the per-query
// capacity and confirms the store caps at capacity (never grows the backing
// arrays) without panicking on the per-chunk mutex index.
func TestHandleLogStream_CapsAtCapacity_NoPanic(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, nil)
	capRows := 3 * 2000
	s.StartStream(capRows)
	for b := 0; b < 2*capRows; b += 2000 {
		batch := make([]icl.Log, 2000)
		for i := range batch {
			batch[i] = icl.Log{Metadata: icl.Metadata{TSMicro: int64(b + i)}}
		}
		s.HandleLogStreamMsg(&msgs.LogStreamMsg{ID: s.GetQueryID(), Logs: batch})
	}
	require.Equal(t, capRows, s.GetLogCount())
}

// TestFilteredTotal_CachedTracksMutations verifies FilteredMatchTotal returns the
// filter-aware match total and stays in sync with every mutation that can change
// it. It drives mutations that PROVABLY change the total so a missing dirty-mark
// at any write site would surface a stale cached value:
//   - search-chunk delivery seeds a positive total (t1),
//   - ApplyFilters hiding every matched log must drop it to 0 (ApplyFilters path),
//   - a HandleFilterChunk un-hiding the log must restore it (HandleFilterChunk path),
//   - SetExpand on a matched log keeps the cache consistent (SetExpand path).
func TestFilteredTotal_CachedTracksMutations(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2
	bundle.State.SetSearch("error")

	s := NewLogStore(bundle, nil, "test-instance")
	fp := &fakePoster{}
	s.SetPoster(fp)
	s.SetLogsRaw([]icl.Log{
		{Data: map[string]any{fieldMsg: "error one"}}, // global 0, chunk 0 — matches
		{Data: map[string]any{fieldMsg: "clean"}},     // global 1, chunk 0 — no match
		{Data: map[string]any{fieldMsg: "error two"}}, // global 2, chunk 1 — matches
		{Data: map[string]any{fieldMsg: "fine"}},      // global 3, chunk 1 — no match
	})

	// Feed back any search chunks that DoSearch (called inside SetLogsRaw) posted.
	for _, c := range fp.searchChunks() {
		s.HandleSearchChunk(c)
	}

	// Flush the cache once and capture the positive baseline total.
	t1 := s.FilteredMatchTotal()
	require.Equal(t, s.state.search.FilteredCount(s.state.filtered), t1)
	require.Positive(t, t1)

	// ApplyFilters path: hide EVERY matched log (both contain "error") via the same
	// state seam the code reads (State.SetFilters + LogStore.ApplyFilters). The
	// filter-aware total must now recompute to 0 — if ApplyFilters failed to mark
	// the cache dirty, FilteredMatchTotal would return the stale t1 here.
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "zzz-no-match"}})
	s.ApplyFilters() // sync recompute: every log fails the include rule -> all hidden
	got := s.FilteredMatchTotal()
	require.Equal(t, s.state.search.FilteredCount(s.state.filtered), got)
	require.Equal(t, 0, got, "all matched logs hidden -> filtered-aware total is 0")
	require.NotEqual(t, t1, got, "total must change after ApplyFilters (discriminates a missing dirty-mark)")

	// HandleFilterChunk path: un-hide the two matched logs by routing a chunk with
	// Hidden=false for their offsets through the *Model handler (the real write
	// seam). The total must recompute back to t1 — a missing dirty-mark in
	// HandleFilterChunk would leave FilteredMatchTotal reporting the stale 0.
	m := New(bundle)
	m.SetPoster(&fakePoster{})
	m.store = s
	m.HandleFilterChunk(FilterChunkMsg{
		Instance: "test-instance", ID: s.queryID, Epoch: s.filterEpoch,
		Offset: 0, Hidden: []bool{false, false},
	})
	m.HandleFilterChunk(FilterChunkMsg{
		Instance: "test-instance", ID: s.queryID, Epoch: s.filterEpoch,
		Offset: 1, Hidden: []bool{false, false},
	})
	require.Equal(t, s.state.search.FilteredCount(s.state.filtered), s.FilteredMatchTotal())
	require.Equal(t, t1, s.FilteredMatchTotal(), "un-hiding matched logs restores the total")

	// SetExpand path: expanding a matched log recomputes its per-log match list;
	// the cache must stay consistent with a direct FilteredCount.
	s.SetExpand(0, true)
	require.Equal(t, s.state.search.FilteredCount(s.state.filtered), s.FilteredMatchTotal())
}

// TestStore_ConcurrentOffLoopReads_Race exercises the full off-loop reader
// surface (GetLogCount/ReadLog/GetSeverity/GetTSMicro/IterLogMeta index
// s.logs/s.mutexes unlocked, bounds-checked against s.logCount) concurrently
// with an on-loop stream fill AND the done finalization. It must be race-clean:
// the backing arrays are sized once by sizeBacking and never reallocated
// (HandleLogStreamDoneMsg no longer reslices the header), and s.logCount is an
// atomic published with release semantics AFTER each element write, so a reader
// that acquires a count > n has a happens-before with the s.logs[n] write.
func TestStore_ConcurrentOffLoopReads_Race(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, nil)
	capRows := 10 * 2000
	s.StartStream(capRows)
	qid := s.GetQueryID()

	stop := make(chan struct{})
	done := make(chan struct{})
	// Off-loop reader: spin the full read surface until the writer signals
	// stop (after the done message), so reads overlap both the arrival fill and
	// the finalization.
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n := s.GetLogCount(); n > 0 {
				_, _ = s.ReadLog(n - 1)
				_, _ = s.GetSeverity(n - 1)
				_, _ = s.GetTSMicro(n - 1)
				_ = s.ReadLogs([]int{0, n / 2, n - 1})
				var seen int
				for range s.IterLogMeta() {
					seen++
				}
				_ = seen
			}
		}
	}()

	for b := 0; b < capRows; b += 2000 {
		batch := make([]icl.Log, 2000)
		for i := range batch {
			batch[i] = icl.Log{Metadata: icl.Metadata{TSMicro: int64(b + i), Severity: icl.SeverityInfo}}
		}
		s.HandleLogStreamMsg(&msgs.LogStreamMsg{ID: qid, Logs: batch})
	}
	// Finalize the stream while the reader is still spinning: this drives the
	// done path (which used to reslice s.logs and race the header read).
	s.HandleLogStreamDoneMsg(&msgs.LogStreamDoneMsg{ID: qid})

	close(stop)
	<-done
	require.Equal(t, capRows, s.GetLogCount())
	require.False(t, s.IsStreaming())
}
