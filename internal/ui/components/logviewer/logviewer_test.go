package logviewer

import (
	"bytes"
	"iter"
	"log/slog"
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/timeline"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloseTimelineActive(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.timelineActive = true
	m.timeline.Open(fakeTimelineStore{}, 0, false)

	m.CloseTimeline()

	assert.False(t, m.timelineActive, "timelineActive should be cleared")
}

func TestCloseTimelineIdempotent(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	// timelineActive defaults to false
	m.CloseTimeline()
	m.CloseTimeline()
	assert.False(t, m.timelineActive)
}

//nolint:paralleltest // slog.SetDefault is process-global; cannot run alongside other parallel tests that emit slog.
func TestLogviewerNew_NegativeRenderCacheSize_LogsWarn(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	bundle := depstest.NewTest(t)
	bundle.Config.Logs.RenderCacheSize = -5
	_ = New(bundle)

	assert.Contains(t, buf.String(), "renderCacheSize <= 0")
}

// TestDraw_EmptyStore_ClippyVCentered asserts that Draw places the clippy ASCII
// art vertically centered within drawRect when no store is attached.
func TestDraw_EmptyStore_ClippyVCentered(t *testing.T) {
	t.Parallel()

	const canvasW, canvasH = 80, 20
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{X: 0, Y: 0, W: canvasW, H: canvasH})

	s := uv.NewScreenBuffer(canvasW, canvasH)
	m.Draw(s)

	clippyLines := strings.Split(clippy, "\n")
	clippyLineCount := len(clippyLines) // 7
	expectedStartY := (canvasH - clippyLineCount) / 2

	// The first rune of clippy's first line is '╭' at column 0.
	c := s.CellAt(0, expectedStartY)
	require.NotNil(t, c, "expected a cell at (0, %d)", expectedStartY)
	assert.Equal(t, "╭", c.Content, "clippy first glyph should be vertically centered at row %d", expectedStartY)
}

// TestApplyJQAndRefresh_EmptyQuery_PurgesRenderCache verifies that applying an
// empty jq (cleared) purges the stale jq-rendered lines from the render cache.
// ApplyJQ's empty-query path restores raw data synchronously and posts no
// JQChunkMsg, so applyJQAndRefresh must purge it itself.
func TestApplyJQAndRefresh_EmptyQuery_PurgesRenderCache(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	fp := &fakePoster{}
	m.SetPoster(fp)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 80, H: 20})

	store := NewLogStore(bundle, nil, "test")
	store.SetPoster(fp)
	store.SetLogs([]icl.Log{{Data: map[string]any{"k": "v"}}})
	m.SetStore(store)

	// Seed a stale render-cache entry for a log index that does not exist, so
	// updateDisplay cannot legitimately repopulate it; only a Purge removes it.
	m.renderCache.Add(99, []tokenizedLine{})
	require.Positive(t, m.renderCache.Len(), "render cache should be seeded")

	m.bundle.State.SetJQ("") // cleared
	m.applyJQAndRefresh(true)

	_, ok := m.renderCache.Get(99)
	assert.False(t, ok, "clearing jq must purge stale render-cache entries")
}

// TestApplyJQAndRefresh_EmptyQuery_PreservesFilteredRows verifies that applying
// an empty jq (cleared) leaves the filtered state untouched. Filtering is
// strictly additive and is cleared only via its dedicated unfilter action.
func TestApplyJQAndRefresh_EmptyQuery_PreservesFilteredRows(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	fp := &fakePoster{}
	m.SetPoster(fp)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 80, H: 20})

	store := NewLogStore(bundle, nil, "test")
	store.SetPoster(fp)
	store.SetLogs([]icl.Log{
		{Data: map[string]any{"k": "v"}},
		{Data: map[string]any{"k": "w"}},
	})
	m.SetStore(store)

	// Two rows currently hidden by a filter.
	store.state.filtered.Set(0, true)
	store.state.filtered.Set(1, true)

	m.bundle.State.SetJQ("") // cleared
	m.applyJQAndRefresh(true)

	assert.True(t, store.state.filtered[0], "clearing jq must not unhide row 0")
	assert.True(t, store.state.filtered[1], "clearing jq must not unhide row 1")
}

// TestSetStore_RefetchedStore_ResetsRowIndexedState pins both halves of the
// re-fetch reset: adopting the SAME store on a new query generation must drop
// the cursor and the n/N jump counter, and the jump-counter reset must survive
// the jq-mismatch early return. With the default jq (".data") a re-fetched store
// always mismatches on jq, so a reset placed only in the search arm never ran.
func TestSetStore_RefetchedStore_ResetsRowIndexedState(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	m.SetPoster(&fakePoster{})

	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.SetLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "a"}},
		{Data: map[string]any{fieldMsg: "b"}},
		{Data: map[string]any{fieldMsg: "c"}},
	})
	m.SetStore(s)

	// The user has jumped to a match deep in this query's rows.
	m.cursor.log = 2
	m.currentMatch = 4
	bundle.State.SetSearch("b")
	require.NotEmpty(t, bundle.State.JQ(), "default jq must be active for this case")

	// Re-fetch: Clear bumps the store's query generation, then new rows load.
	s.Clear()
	s.SetLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "x"}},
		{Data: map[string]any{fieldMsg: "y"}},
		{Data: map[string]any{fieldMsg: "z"}},
	})

	m.SetStore(s) // same store pointer, new generation

	assert.Equal(t, 0, m.cursor.log, "cursor must not index the previous query's rows")
	assert.Equal(t, 0, m.currentMatch, "n/N jump counter must reset on re-fetch")
}

// TestSetStore_FilterMismatch_Reapplies verifies SetStore recomputes filtered
// when the store's applied-rules snapshot differs from current state.Filters().
func TestSetStore_FilterMismatch_Reapplies(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	m.SetPoster(&fakePoster{})

	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(&fakePoster{})
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "error one"}},
		{Data: map[string]any{fieldMsg: "clean"}},
	})
	s.appliedFilters = nil // store thinks no rules applied

	// State now carries a rule the store has not applied.
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "error"}})

	m.SetStore(s)

	assert.Equal(t, []filter.Rule{{Type: filter.Include, Value: "error"}}, s.appliedFilters,
		"SetStore reapplies filters on mismatch")
	assert.True(t, s.state.filtered[1], "clean log hidden after reapply")
	assert.False(t, s.state.filtered[0], "error log visible after reapply")
}

// TestOnFilterApplied_RecomputesAndEvictsCursor verifies OnFilterApplied runs the
// full filter recompute and moves the cursor off a now-hidden log. The
// contextual provider reads that raw cursor directly.
func TestOnFilterApplied_RecomputesAndEvictsCursor(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	fp := &fakePoster{}
	m.SetPoster(fp)

	s := NewLogStore(bundle, nil, "i")
	s.SetPoster(fp)
	s.loadLogs([]icl.Log{
		{Data: map[string]any{fieldMsg: "visible"}},
		{Data: map[string]any{fieldMsg: "hideme"}},
	})
	m.SetStore(s)
	m.cursor.log = 1 // park cursor on the log about to be hidden

	bundle.State.SetFilters([]filter.Rule{{Type: filter.Exclude, Value: "hideme"}})
	m.OnFilterApplied()

	assert.True(t, s.state.filtered[1], "excluded log hidden after Apply")
	assert.NotEqual(t, 1, m.cursor.log, "cursor evicted off the hidden log")
}

// fakeTimelineStore is a minimal timeline.Store for exercising CloseTimeline
// without constructing a real LogStore.
type fakeTimelineStore struct{}

func (fakeTimelineStore) GetTimeRange() (int64, int64) { return 0, 0 }
func (fakeTimelineStore) GetLogCount() int             { return 0 }
func (fakeTimelineStore) IterLogMeta() iter.Seq[timeline.LogMeta] {
	return func(yield func(timeline.LogMeta) bool) {}
}
