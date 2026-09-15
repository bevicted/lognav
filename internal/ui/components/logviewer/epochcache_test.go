package logviewer

import (
	"context"
	"fmt"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// dropPoster is a Poster whose Go() DISCARDS the worker body. The on-loop half
// of a spawn still runs — taking the epoch artifact, building the per-chunk data
// slice, cloning the batch — while the off-loop compute does not. That is
// exactly the half the per-epoch compile cache targets (the compile work the
// ingest path puts on the loop goroutine), and dropping the rest is what keeps a
// 1M-row test from paying for a million jq evaluations.
type dropPoster struct{}

func (dropPoster) Go(func(context.Context))                     {}
func (dropPoster) PostCritical(context.Context, uv.Event) error { return nil }
func (dropPoster) Post(uv.Event)                                {}

// goPoster runs each worker on a real goroutine, so artifacts shared out of the
// epoch cache are genuinely used concurrently (meaningful under -race).
type goPoster struct {
	wg     sync.WaitGroup
	mu     sync.Mutex
	posted []uv.Event
}

func (p *goPoster) Go(fn func(context.Context)) {
	p.wg.Go(func() { fn(context.Background()) })
}

func (p *goPoster) PostCritical(_ context.Context, ev uv.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posted = append(p.posted, ev)
	return nil
}

func (p *goPoster) Post(uv.Event) {}

func (p *goPoster) wait() []uv.Event {
	p.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uv.Event(nil), p.posted...)
}

// TestEpochCache_Stream1M_CompilesOncePerEpoch is the ticket-06 counter check: a
// 1M-row stream with jq, filter rules and search all active must compile each
// expression ONCE, not once per 2000-row batch (which would be 500 times).
// Asserted against the cache's miss counters, not by reading the code.
func TestEpochCache_Stream1M_CompilesOncePerEpoch(t *testing.T) {
	t.Parallel()
	const (
		rows  = 1_000_000
		batch = 2000
	)
	bundle := depstest.NewTest(t)
	bundle.State.SetJQ(".msg")
	bundle.State.SetSearch("event")
	bundle.State.SetFilters([]filter.Rule{
		{Type: filter.Include, Value: "event"},
		{Type: filter.HasField, Value: ".msg"},
	})
	s := NewLogStore(bundle, nil, "epoch-1m")
	s.SetPoster(dropPoster{})
	t.Cleanup(func() { assert.NoError(t, s.Close()) })

	// One immutable data map shared by every row: entry data is never mutated in
	// place, and the drop poster means no worker ever reads it concurrently, so
	// sharing keeps a million-row fixture inside a sane heap.
	data := map[string]any{"msg": "event happened"}
	logs := make([]icl.Log, batch)
	for i := range logs {
		logs[i] = icl.Log{Data: data, Metadata: icl.Metadata{TSMicro: int64(i)}}
	}
	// Stand-in for what the dropped jq worker would have posted back.
	results := make([]any, batch)
	for i := range results {
		results[i] = "event happened"
	}

	s.StartStream(rows)
	for k := range rows / batch {
		s.HandleLogStreamMsg(&msgs.LogStreamMsg{ID: s.GetQueryID(), Logs: logs})
		// While jq is active the batch dispatch does NOT spawn a filter pass;
		// HandleJQChunk is what sequences the filter pass and the chunk
		// re-search, so drive it to exercise all three cached artifacts.
		s.HandleJQChunk(JQChunkMsg{Instance: "epoch-1m", ID: s.GetQueryID(), Offset: k, Results: results})
	}
	s.HandleLogStreamDoneMsg(&msgs.LogStreamDoneMsg{ID: s.GetQueryID()})

	require.Equal(t, rows, s.GetLogCount())
	assert.Equal(t, 1, s.epoch.jqParses, "jq query parsed once for the whole 1M-row stream")
	assert.Equal(t, 1, s.epoch.filterCompiles, "filter rules compiled once for the whole 1M-row stream")
	assert.Equal(t, 1, s.epoch.searchBuilds, "search automaton built once for the whole 1M-row stream")
}

// TestEpochCache_InvalidatesOnKeyChange covers the second acceptance criterion
// directly: each artifact is rebuilt when its own key — the query text, the rule
// set, or the filter epoch — changes, and only then.
func TestEpochCache_InvalidatesOnKeyChange(t *testing.T) {
	t.Parallel()
	s := NewLogStore(depstest.NewTest(t), nil, "epoch-keys")
	s.SetPoster(dropPoster{})
	t.Cleanup(func() { assert.NoError(t, s.Close()) })

	t.Run("jq query", func(t *testing.T) {
		q1, err := s.parsedJQ(".a")
		require.NoError(t, err)
		q2, err := s.parsedJQ(".a")
		require.NoError(t, err)
		assert.Same(t, q1, q2, "same source is served from the cache")
		assert.Equal(t, 1, s.epoch.jqParses)

		_, err = s.parsedJQ(".b")
		require.NoError(t, err)
		assert.Equal(t, 2, s.epoch.jqParses, "changed query re-parses")

		// A parse FAILURE is cached too, so a broken query is not re-parsed once
		// per batch either.
		_, err1 := s.parsedJQ("((((bad")
		_, err2 := s.parsedJQ("((((bad")
		require.Error(t, err1)
		assert.Equal(t, err1, err2)
		assert.Equal(t, 3, s.epoch.jqParses)
	})

	t.Run("search automaton", func(t *testing.T) {
		before := s.epoch.searchBuilds
		s.searchAutomaton("needle")
		s.searchAutomaton("needle")
		assert.Equal(t, before+1, s.epoch.searchBuilds, "same query is served from the cache")
		s.searchAutomaton("other")
		assert.Equal(t, before+2, s.epoch.searchBuilds, "changed query rebuilds")
	})

	t.Run("filter rules and epoch", func(t *testing.T) {
		before := s.epoch.filterCompiles
		rules := []filter.Rule{{Type: filter.HasField, Value: ".msg"}}
		c1 := s.compiledFilterRules(rules)
		c2 := s.compiledFilterRules(rules)
		require.Len(t, c1, 1)
		assert.Same(t, c1[0].code, c2[0].code, "same rules are served from the cache")
		assert.Equal(t, before+1, s.epoch.filterCompiles)

		s.compiledFilterRules([]filter.Rule{{Type: filter.HasField, Value: ".other"}})
		assert.Equal(t, before+2, s.epoch.filterCompiles, "changed rules recompile")

		// filterEpoch is part of the key: an epoch bump (ApplyJQ / ApplyFilters /
		// ClearData) must never serve the previous epoch's compiled rules.
		s.compiledFilterRules(rules)
		n := s.epoch.filterCompiles
		s.filterEpoch++
		s.compiledFilterRules(rules)
		assert.Equal(t, n+1, s.epoch.filterCompiles, "epoch bump recompiles identical rules")
	})
}

// TestEpochCache_QueryEditMidStream_OutputUnchanged pins the fifth acceptance
// criterion: crossing an epoch boundary (a jq edit mid-stream) must leave
// filtering, jq output and search highlighting exactly as a same-query run
// produces them. It runs the whole ingest pipeline with the inline poster, so
// jq output, filter decisions and search matches are all real.
func TestEpochCache_QueryEditMidStream_OutputUnchanged(t *testing.T) {
	t.Parallel()
	const rows = 8
	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2 // 4 chunks, several epochs' worth of dispatch
	bundle.State.SetJQ(".kind")
	bundle.State.SetSearch("even")
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Exclude, Value: "odd"}})

	logs := make([]icl.Log, rows)
	for i := range logs {
		kind := "even"
		if i%2 == 1 {
			kind = "odd"
		}
		logs[i] = icl.Log{
			Data:     map[string]any{"kind": kind, "n": fmt.Sprintf("row-%d", i)},
			Metadata: icl.Metadata{TSMicro: int64(i)},
		}
	}

	s := NewLogStore(bundle, nil, "epoch-edit")
	fp := &fakePoster{}
	s.SetPoster(fp)
	m := New(bundle)
	m.SetPoster(fp)
	m.store = s
	t.Cleanup(func() { assert.NoError(t, s.Close()) })

	drain := func() {
		for range 20 {
			fp.mu.Lock()
			evs := fp.posted
			fp.posted = nil
			fp.mu.Unlock()
			if len(evs) == 0 {
				return
			}
			for _, ev := range evs {
				switch msg := ev.(type) {
				case JQChunkMsg:
					s.HandleJQChunk(msg)
				case FilterChunkMsg:
					m.HandleFilterChunk(msg)
				case SearchChunkMsg:
					s.HandleSearchChunk(msg)
				}
			}
		}
	}

	s.StartStream(rows)
	s.HandleLogStreamMsg(&msgs.LogStreamMsg{ID: s.GetQueryID(), Logs: logs[:4]})
	drain()
	// Epoch boundary: edit the jq query mid-stream, then keep streaming.
	bundle.State.SetJQ(".n")
	s.ApplyJQ()
	drain()
	s.HandleLogStreamMsg(&msgs.LogStreamMsg{ID: s.GetQueryID(), Logs: logs[4:]})
	drain()
	s.HandleLogStreamDoneMsg(&msgs.LogStreamDoneMsg{ID: s.GetQueryID()})
	drain()

	require.Equal(t, rows, s.GetLogCount())
	for i := range rows {
		// jq output: every row, including the pre-edit ones, carries the NEW
		// query's result.
		assert.Equal(t, fmt.Sprintf("row-%d", i), s.logs[i].modifiedData, "row %d jq output", i)
		// Filtering: the Exclude "odd" rule is evaluated against the jq output,
		// which after the edit is "row-N" for every row — so nothing is hidden.
		assert.False(t, s.state.filtered[i], "row %d must not be hidden", i)
		// Search highlighting: "even" no longer appears in the jq output either.
		assert.Empty(t, s.state.search[i], "row %d must have no match", i)
	}
	// The edit is one epoch boundary: exactly one extra compile of the jq query
	// and of the filter rules (ApplyJQ bumps filterEpoch); the search query never
	// changed, so its automaton was built once.
	assert.Equal(t, 2, s.epoch.jqParses)
	assert.Equal(t, 1, s.epoch.searchBuilds)
}

// TestEpochCache_SharedFilterRules_ConcurrentWorkers is the race guard for the
// artifact SHARING the cache introduces: before it, every chunk worker got its
// own []compiledRule; now all workers of an epoch evaluate the SAME *gojq.Code.
// gojq documents that as safe — this test makes it observable under -race.
func TestEpochCache_SharedFilterRules_ConcurrentWorkers(t *testing.T) {
	t.Parallel()
	const chunks = 8
	bundle := depstest.NewTest(t)
	bundle.Config.Logs.BatchOpChunkSize = 2
	s := NewLogStore(bundle, nil, "epoch-race")
	gp := &goPoster{}
	s.SetPoster(gp)
	t.Cleanup(func() { assert.NoError(t, s.Close()) })

	logs := make([]icl.Log, chunks*2)
	for i := range logs {
		logs[i] = icl.Log{Data: map[string]any{"msg": fmt.Sprintf("event %d", i)}}
	}
	s.loadLogs(logs)
	s.filterCtx, s.cancelFilter = context.WithCancel(context.Background()) //nolint:gosec // cancelFilter is owned by the store; Close cancels it
	rules := []filter.Rule{
		{Type: filter.HasField, Value: ".msg"},
		{Type: filter.Include, Value: "event"},
	}
	for c := range chunks {
		require.True(t, s.spawnFilterChunk(rules, c, s.logs[c*2:c*2+2]))
	}
	posted := gp.wait()

	assert.Equal(t, 1, s.epoch.filterCompiles, "all workers share one compiled rule set")
	assert.Len(t, posted, chunks)
	for _, ev := range posted {
		msg, ok := ev.(FilterChunkMsg)
		require.True(t, ok)
		assert.Equal(t, []bool{false, false}, msg.Hidden, "every row matches both rules")
	}
}
