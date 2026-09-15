package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJQPath_BuildsDottedPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{"simple", []string{"data", "log"}, ".data.log"},
		{"nested", []string{"data", "log", "status_code"}, ".data.log.status_code"},
		{"space key", []string{"data", "a b"}, `.data.["a b"]`},
		{"leading digit", []string{"data", "1x"}, `.data.["1x"]`},
		{"dash key", []string{"data", "x-y"}, `.data.["x-y"]`},
		{"backslash key", []string{"data", `a\b`}, `.data.["a\\b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, jqPath(tt.input))
		})
	}
}

func TestDataprimeRef_MapsRootToSigil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{"data", []string{"data", "log", "status_code"}, "$d.log.status_code"},
		{"labels", []string{"labels", "subsystemname"}, "$l.subsystemname"},
		{"metadata", []string{"metadata", "severity"}, "$m.severity"},
		{"root only", []string{"data"}, "$d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, dataprimeRef(tt.input))
		})
	}
}

func TestDataprimeLiteral_ByType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  any
		want   string
		wantOK bool
	}{
		{"string", "example-service", `'example-service'`, true},
		{"string with quote", "it's", `'it\'s'`, true},
		{"string with backslash", `a\b`, `'a\\b'`, true},
		{"float", float64(200), "200", true},
		{"bool", true, "true", true},
		{"nil", nil, "null", true},
		{"map", map[string]any{"a": 1}, "", false},
		{"slice", []any{1, 2}, "", false},
		{"rawlog", RawLog{"a": 1}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := dataprimeLiteral(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSearchValue_ByType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  any
		want   string
		wantOK bool
	}{
		{"string", "example-service", "example-service", true},
		{"float", float64(200), "200", true},
		{"bool", false, "false", true},
		{"nil", nil, "null", true},
		{"map", map[string]any{"a": 1}, "", false},
		{"slice", []any{1, 2}, "", false},
		{"rawlog", RawLog{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := searchValue(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsScalarValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input any
		want  bool
	}{
		{"string", "x", true},
		{"float", float64(1), true},
		{"bool", true, true},
		{"nil", nil, true},
		{"map", map[string]any{}, false},
		{"slice", []any{}, false},
		{"rawlog", RawLog{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isScalarValue(tt.input))
		})
	}
}

func TestResolveCursorField_ExpandedScalar(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	store.SetLogs([]icl.Log{{Data: map[string]any{
		"data": map[string]any{
			"log": map[string]any{"status_code": float64(200)},
		},
	}}})
	m.SetStore(store)
	m.SetExpand(0, true)
	// Expanded MarshalIndent (4-space) lines with a wide terminal (no wrap):
	//   0: {
	//   1:     "data": {
	//   2:         "log": {
	//   3:             "status_code": 200
	m.SetCursor(0, 3, 0, 3, 0)

	path, value, ok := m.resolveCursorField()
	assert.True(t, ok)
	assert.Equal(t, []string{"data", "log", "status_code"}, path)
	assert.InDelta(t, float64(200), value, 0)
}

func TestResolveCursorField_NotExpanded_ReturnsNotOK(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	store.SetLogs([]icl.Log{{Data: map[string]any{"data": map[string]any{"k": "v"}}}})
	m.SetStore(store)
	m.SetCursor(0, 0, 0, 0, 0)

	_, _, ok := m.resolveCursorField()
	assert.False(t, ok)
}

func TestParseJQPathPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		want     []string
		wantPath bool
	}{
		{"empty", "", nil, true},
		{"identity", ".", nil, true},
		{"single", ".data", []string{"data"}, true},
		{"nested", ".data.log", []string{"data", "log"}, true},
		{"with spaces around", "  .data.log  ", []string{"data", "log"}, true},
		{"bracket key", `.["weird key"].x`, []string{"weird key", "x"}, true},
		{"pipe not a path", ".data | .log", nil, false},
		{"function not a path", ".data | keys", nil, false},
		{"iteration not a path", ".data[]", nil, false},
		{"numeric index not a key", ".[0]", nil, false},
		{"no leading dot", "keys", nil, false},
		{"trailing dot", ".data.", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseJQPathPrefix(tt.input)
			assert.Equal(t, tt.wantPath, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func itemLabels(items []msgs.ContextMenuItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Label
	}
	return out
}

func showMenuItems(t *testing.T, m *Model) []msgs.ContextMenuItem {
	t.Helper()
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.showContextMenu()
	for _, ev := range fp.Posted {
		if msg, ok := ev.(msgs.ShowContextMenuMsg); ok {
			return msg.Items
		}
	}
	require.FailNow(t, "expected a ShowContextMenuMsg")
	return nil
}

func showMenuMsg(t *testing.T, m *Model) msgs.ShowContextMenuMsg {
	t.Helper()
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.showContextMenu()
	for _, ev := range fp.Posted {
		if msg, ok := ev.(msgs.ShowContextMenuMsg); ok {
			return msg
		}
	}
	require.FailNow(t, "expected a ShowContextMenuMsg")
	return msgs.ShowContextMenuMsg{}
}

func scalarStore(t *testing.T) *LogStore {
	t.Helper()
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	store.SetLogs([]icl.Log{{Data: map[string]any{
		"data": map[string]any{"log": map[string]any{"status_code": float64(200)}},
	}}})
	return store
}

func TestShowContextMenu_Collapsed_NoPod_OnlyAlwaysItems(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetCursor(0, 0, 0, 0, 0)

	items := showMenuItems(t, m)
	assert.Equal(t, []string{"copy log", "mark log"}, itemLabels(items))
}

func TestShowContextMenu_MarkedLog_OffersUnmark(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetCursor(0, 0, 0, 0, 0)
	m.store.state.marked.Set(0, true)

	items := showMenuItems(t, m)
	assert.Equal(t, []string{"copy log", "unmark log"}, itemLabels(items))
}

func TestShowContextMenu_CollapsedWithPod_AddsDataprimePodContext(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	store.SetLogs([]icl.Log{{Data: map[string]any{
		"data": map[string]any{"kubernetes": map[string]any{"pod_id": "pod-123"}},
	}}})
	m.SetStore(store)
	m.SetCursor(0, 0, 0, 0, 0)

	items := showMenuItems(t, m)
	assert.Equal(t, []string{"copy log", "dataprime: pod context", "mark log"}, itemLabels(items))
}

func TestShowContextMenu_ExpandedScalar_PathJQ_NineItems(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetExpand(0, true)
	m.SetCursor(0, 3, 0, 3, 0)

	items := showMenuItems(t, m)
	assert.Equal(t, []string{
		"copy value", "copy log", "search value", "jq: select field",
		"filter: include value", "filter: exclude value", "filter: has field",
		"dataprime: filter field == value", "mark log",
	}, itemLabels(items))
}

func TestShowContextMenu_ExpandedScalar_NonPathJQ_OmitsRawFieldActions(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	store := scalarStore(t)
	m.SetStore(store)
	m.SetExpand(0, true)
	m.SetCursor(0, 3, 0, 3, 0)
	// Set the non-path jq AFTER SetStore: SetStore's jq-mismatch check calls
	// ApplyJQ, which would reset appliedJQ back to "" if set beforehand.
	store.appliedJQ = "keys"

	// Under a non-path jq the field's raw path is unrecoverable, so both
	// Dataprime filtering and the has-field filter are omitted.
	items := showMenuItems(t, m)
	assert.NotContains(t, itemLabels(items), "dataprime: filter field == value")
	assert.NotContains(t, itemLabels(items), "filter: has field")
	assert.Equal(t, []string{
		"copy value", "copy log", "search value", "jq: select field",
		"filter: include value", "filter: exclude value", "mark log",
	}, itemLabels(items))
}

func TestJQExprForField_PathJQ_BuildsAbsoluteRawPath(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	m.SetStore(store)
	// A pure navigation path jq: relPath is resolved against its OUTPUT, so the
	// The has-field filter value must prepend the jq prefix to reach raw.
	store.appliedJQ = ".data"

	jq, basePath, isPath := m.jqExprForField([]string{"log", "status_code"})
	assert.True(t, isPath, "a pure path jq is path-navigable")
	assert.Equal(t, []string{"data", "log", "status_code"}, basePath, "basePath = jq prefix + relPath")
	assert.Equal(t, ".data.log.status_code", jq)
	// The has-field action uses jqPath(basePath): the absolute raw path, not the
	// bare displayed relPath (".log.status_code", which would miss on raw data).
	assert.Equal(t, ".data.log.status_code", jqPath(basePath))
}

func TestShowContextMenu_ExpandedContainerField_FourItems(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	store.SetLogs([]icl.Log{{Data: map[string]any{
		"data": map[string]any{"log": map[string]any{"nested": map[string]any{"a": float64(1)}}},
	}}})
	m.SetStore(store)
	m.SetExpand(0, true)
	m.SetCursor(0, 3, 0, 3, 0)

	items := showMenuItems(t, m)
	assert.Equal(t, []string{
		"copy value", "copy log", "jq: select field", "filter: has field", "mark log",
	}, itemLabels(items))
}

func TestShowContextMenu_Anchor_RowBelowCursor(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetCursor(0, 0, 0, 0, 0)

	msg := showMenuMsg(t, m)

	assert.True(t, msg.Anchored, "menu posted from the logviewer must be anchored")
	// cursor.y 0 → menu opens one row below the selected line.
	assert.Equal(t, 1, msg.AnchorY)
	// logs area starts at logsR.X (drawRect.X 0 + sidebarWidth 1); a collapsed
	// line has no leading indent, so the anchor sits at the logs-area left edge.
	assert.GreaterOrEqual(t, msg.AnchorX, 1)
}

func TestShowContextMenu_Anchor_ExpandedFirstChar(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetExpand(0, true)
	// Expanded MarshalIndent layout (see TestResolveCursorField_ExpandedScalar):
	//   3:             "status_code": 200   → 12 leading spaces.
	m.SetCursor(0, 3, 0, 3, 0)

	msg := showMenuMsg(t, m)

	assert.True(t, msg.Anchored)
	// x = logsR.X(1) + firstNonBlank(12).
	assert.Equal(t, 13, msg.AnchorX)
	// cursor.y 3 → one row below = 4.
	assert.Equal(t, 4, msg.AnchorY)
}

func TestShowContextMenu_Anchor_HonorsHorizontalScroll(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetExpand(0, true)
	// Same row as above (12 leading spaces) but scrolled right by 4 columns.
	m.SetCursor(0, 3, 0, 3, 4)

	msg := showMenuMsg(t, m)

	// x = logsR.X(1) + max(0, firstNonBlank(12) - xOffset(4)) = 1 + 8 = 9.
	assert.Equal(t, 9, msg.AnchorX)
}

func TestShowContextMenu_RightClick_AnchorsAtMouse(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetCursor(0, 0, 0, 0, 0)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)

	// Right-click on the single (row 0) log at an arbitrary column. The menu must
	// anchor at the pointer column, one row BELOW the pointer (the same
	// below-the-reference convention the `c` path uses), so it never covers the
	// clicked line — distinct from the `c` path, whose x would be logsR.X=1.
	consumed := m.OnMouseRight(7, 0)
	require.True(t, consumed)

	var msg msgs.ShowContextMenuMsg
	var found bool
	for _, ev := range fp.Posted {
		if mm, ok := ev.(msgs.ShowContextMenuMsg); ok {
			msg, found = mm, true
		}
	}
	require.True(t, found, "right-click must post a ShowContextMenuMsg")
	assert.True(t, msg.Anchored)
	assert.Equal(t, 7, msg.AnchorX, "right-click anchors at the mouse x")
	assert.Equal(t, 1, msg.AnchorY, "right-click anchors one row below the mouse y")
}

// TestOpenContextMenu_TimelineActive_NoMenu verifies that while the timeline
// overlay is active, the keyboard OpenContextMenu (global `c`) is a no-op — the
// timeline owns all input, matching HandleKey's timelineActive swallow.
func TestOpenContextMenu_TimelineActive_NoMenu(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetCursor(0, 0, 0, 0, 0)
	m.timelineActive = true

	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.OpenContextMenu()

	for _, ev := range fp.Posted {
		_, ok := ev.(msgs.ShowContextMenuMsg)
		require.False(t, ok, "OpenContextMenu must not post a menu while the timeline is active")
	}
}

// TestOpenContextMenu_NoTimeline_PostsMenu is the positive control: with the
// timeline closed and logs present, OpenContextMenu opens the menu.
func TestOpenContextMenu_NoTimeline_PostsMenu(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{W: 200, H: 40})
	m.SetStore(scalarStore(t))
	m.SetCursor(0, 0, 0, 0, 0)

	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.OpenContextMenu()

	var found bool
	for _, ev := range fp.Posted {
		if _, ok := ev.(msgs.ShowContextMenuMsg); ok {
			found = true
		}
	}
	require.True(t, found, "OpenContextMenu must post a menu when timeline is closed and logs exist")
}

func TestValueToClipboard_StringRaw_NonStringJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"string raw", "example-service", "example-service"},
		{"number json", float64(200), "200"},
		{"bool json", true, "true"},
		{"null json", nil, "null"},
		{"object json", map[string]any{"a": float64(1)}, `{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := valueToClipboard(tt.in)
			assert.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAppendFilterRule_IncludeAppliesAndPosts(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	bundle.State.SetFilters([]filter.Rule{{Type: filter.Exclude, Value: "old"}})

	m.appendFilterRule(filter.Include, "needle")

	got := bundle.State.Filters()
	require.Len(t, got, 2)
	assert.Equal(t, filter.Exclude, got[0].Type)
	assert.Equal(t, filter.Include, got[1].Type)
	assert.Equal(t, "needle", got[1].Value)

	found := false
	for _, ev := range fp.Posted {
		if _, ok := ev.(msgs.FilterAppliedMsg); ok {
			found = true
		}
	}
	assert.True(t, found, "appendFilterRule should post FilterAppliedMsg")
}
