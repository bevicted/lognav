package logviewer

import (
	"strings"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusVariants_NormalPillOrderAndModifiers(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 3)
	m.SetInstanceStatusLookup(func(string) (InstanceStatus, bool) {
		return InstanceStatus{Name: "prod", Phase: "FETCHING", Timer: "1.0s", PhaseStyle: uv.Style{Fg: ansi.Red}}, true
	})
	m.bundle.State.SetJQ(".data")
	m.bundle.State.SetSearch("error")
	m.bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "error"}})

	variants := m.StatusVariants()
	require.Len(t, variants, 4)
	full := variants[0]
	require.Len(t, full, 6)
	assert.Equal(t, []string{"prod", "Span", "Log", "JQ", "Search", "Filter"}, pillHeads(full))
	assert.Equal(t, "FETCHING 1.0s", full[0].Value)
	assert.Equal(t, "1/3", full[2].Value)
	assert.NotNil(t, full[3].Action)
	assert.NotNil(t, full[4].Action)
	assert.NotNil(t, full[5].Action)
	assert.Equal(t, "FETCHING", variants[1][0].Value, "second fallback drops only elapsed timer")
	assert.True(t, strings.HasPrefix(variants[2][3].Value, ".data"))
	assert.Equal(t, []string{"prod", "S", "L", "J", "S", "F"}, pillHeads(variants[3]))
}

func TestStatusVariants_TimelineUsesSelectedVisibleBucket(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 4)
	m.bundle.Config.Logs.TimelineBuckets = 4
	m.store.loadLogs([]icl.Log{
		{Metadata: icl.Metadata{TSMicro: 0, Severity: icl.SeverityInfo}},
		{Metadata: icl.Metadata{TSMicro: 100, Severity: icl.SeverityInfo}},
		{Metadata: icl.Metadata{TSMicro: 200, Severity: icl.SeverityInfo}},
		{Metadata: icl.Metadata{TSMicro: 1_000, Severity: icl.SeverityInfo}},
	})
	m.SetInstanceStatusLookup(func(string) (InstanceStatus, bool) {
		return InstanceStatus{Name: "prod", Phase: "DONE", Timer: "1.0s", PhaseStyle: uv.Style{Fg: ansi.Red}}, true
	})
	m.timeline.SetRect(m.drawRect)
	m.timeline.Open(m.store, 1_000, true)
	m.timelineActive = true

	variants := m.StatusVariants()

	require.Len(t, variants, 3)
	assert.Equal(t, []string{"prod", "Bucket", "Time", "Logs"}, pillHeads(variants[0]))
	assert.Equal(t, "DONE", variants[0][0].Value, "timeline omits the normal timer")
	assert.Equal(t, "2/2", variants[0][1].Value, "hidden buckets do not affect position")
	assert.Equal(t, "00:00:00.000750..00:00:00.001001 UTC", variants[0][2].Value,
		"the final bucket's existing exclusive end is shown")
	assert.Equal(t, "1", variants[0][3].Value)
	assert.Equal(t, []string{"prod", "B", "T", "L"}, pillHeads(variants[1]))
	assert.Equal(t, []string{"prod", "B", "L"}, pillHeads(variants[2]))
	for _, variant := range variants {
		for _, pill := range variant {
			assert.Nil(t, pill.Action, "timeline pills are display-only")
		}
	}
}

func TestStatusVariants_TimelineInvalidStateIsEmpty(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.timelineActive = true

	assert.Equal(t, []component.StatusVariant{{}}, m.StatusVariants())
}

func TestStatusVariants_TimelineSelectionUpdatesWithoutPost(t *testing.T) {
	t.Parallel()
	m, fp := newViewerModel(t, 4)
	m.bundle.Config.Logs.TimelineBuckets = 4
	m.timeline.SetRect(m.drawRect)
	m.timeline.Open(m.store, 1_000, true)
	m.timelineActive = true
	before := len(fp.Posted)

	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyDown})
	assert.Equal(t, "2/4", timelinePillValue(t, m.StatusVariants()[0], "Bucket"),
		"keyboard movement is visible without a refresh post")
	assert.Len(t, fp.Posted, before)

	consumed := m.OnMouseClick(1, m.drawRect.Y+3, uv.MouseLeft)
	assert.True(t, consumed)
	assert.Equal(t, "4/4", timelinePillValue(t, m.StatusVariants()[0], "Bucket"),
		"mouse selection is visible in the same frame")
	assert.Len(t, fp.Posted, before)
}

func timelinePillValue(t *testing.T, pills component.StatusVariant, head string) string {
	t.Helper()
	for _, pill := range pills {
		if pill.Head == head {
			return pill.Value
		}
	}
	t.Fatalf("missing %q pill in %#v", head, pills)
	return ""
}

func TestStatusVariants_OmitEmptyStoreStatisticsAndInactiveModifiers(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.bundle.State.SetJQ("")
	m.SetInstanceStatusLookup(func(string) (InstanceStatus, bool) { return InstanceStatus{}, false })

	for _, variant := range m.StatusVariants() {
		assert.Empty(t, variant)
	}
}

func TestStatusVariantActionsUseExistingEditors(t *testing.T) {
	t.Parallel()
	m, fp := newViewerModel(t, 1)
	m.bundle.State.SetJQ(".")
	m.bundle.State.SetSearch("error")
	m.bundle.State.SetFilters([]filter.Rule{{Type: filter.Include, Value: "error"}})
	pills := m.StatusVariants()[0]

	pills[2].Action()
	pills[3].Action()
	pills[4].Action()
	var dialogs, menus int
	for _, ev := range fp.Posted {
		switch ev.(type) {
		case msgs.ShowDialogMsg:
			dialogs++
		case msgs.ShowFilterMenuMsg:
			menus++
		}
	}
	assert.Equal(t, 2, dialogs, "jq and search actions post dialogs")
	assert.Equal(t, 1, menus, "filter action posts its menu")
}

func TestFormatSpan(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		span int64
		want string
	}{
		{0, "0ms"},
		{999, "1ms"},
		{1_500, "1.5ms"},
		{1_000_000, "1s"},
		{9_500_000, "9.5s"},
		{10_000_000, "10s"},
		{60_000_000, "1m"},
		{3_600_000_000, "1h"},
		{-1, "0ms"},
	} {
		assert.Equal(t, tt.want, formatSpan(tt.span))
	}
}

func pillHeads(pills component.StatusVariant) []string {
	heads := make([]string, len(pills))
	for i, pill := range pills {
		heads[i] = pill.Head
	}
	return heads
}
