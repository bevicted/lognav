package ui

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/helpoverlay"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/logviewer"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hintLineTestComponent struct {
	rect component.Rect
}

func (c *hintLineTestComponent) Init() {}

func (c *hintLineTestComponent) SetRect(r component.Rect) { c.rect = r }

func (c *hintLineTestComponent) Draw(component.Screen) *component.Cursor { return nil }

func (c *hintLineTestComponent) GetKeybinds() []keys.Binding { return nil }

func (c *hintLineTestComponent) OnMouseClick(int, int, uv.MouseButton) bool { return true }

type trackingScreen struct {
	uv.ScreenBuffer
	touched [][]bool
}

func newTrackingScreen(w, h int) *trackingScreen {
	touched := make([][]bool, h)
	for y := range touched {
		touched[y] = make([]bool, w)
	}
	return &trackingScreen{
		ScreenBuffer: compose(w, h),
		touched:      touched,
	}
}

func (s *trackingScreen) SetCell(x, y int, cell *uv.Cell) {
	s.touched[y][x] = true
	s.ScreenBuffer.SetCell(x, y, cell)
}

type statusLineTestComponent struct {
	hintLineTestComponent
	actionHits int
}

func (c *statusLineTestComponent) StatusVariants() []component.StatusVariant {
	return []component.StatusVariant{{{Head: "Status", Value: "ready", Action: func() { c.actionHits++ }}}}
}

func TestContextualStatusReservesAndRoutesFooterRow(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		showHints bool
		wantH     int
		statusY   int
	}{
		{"with hints", true, 21, 22},
		{"without hints", false, 22, 23},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := depstest.NewTest(t)
			bundle.Config.Core.ShowKeyHints = tt.showHints
			m, err := New(t.Context(), bundle)
			require.NoError(t, err)
			active := &statusLineTestComponent{}
			m.tabs.ReplaceComponent(QueryTab, active)
			m.OnResize(80, 24)
			m.DrawTo(compose(80, 24))

			assert.Equal(t, tt.wantH, active.rect.H)
			assert.Equal(t, tt.statusY, m.footerTop())
			m.onMouseClick(0, tt.statusY)
			assert.Equal(t, 1, active.actionHits, "visible status pill receives left click")
		})
	}
}

// TestLogsToSnapshotsRepaintsReclaimedStatusRow prevents a provider-owned row
// from surviving in the terminal when Snapshots immediately reclaims it.
func TestLogsToSnapshotsRepaintsReclaimedStatusRow(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Core.ShowKeyHints = false
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.OnResize(80, 24)

	store := logviewer.NewLogStore(bundle, nil, "loaded")
	store.SetPoster(&msgstest.FakePoster{})
	store.SetLogs([]icl.Log{{Data: map[string]any{"message": "log"}}})
	m.logviewer.SetStore(store)
	m.tabs.Select(LogsTab)

	before := compose(80, 24)
	m.DrawTo(before)
	for x, want := range []string{"S", "p", "a", "n"} {
		assert.Equal(t, want, before.CellAt(x, 23).Content)
	}

	m.tabs.Select(SnapshotTab)
	assert.Zero(t, m.footerRows())
	after := newTrackingScreen(80, 24)
	m.DrawTo(after)
	for x := range 80 {
		assert.Truef(t, after.touched[23][x], "reclaimed cell %d was not repainted", x)
	}
	for x := range 4 {
		assert.Equal(t, " ", after.CellAt(x, 23).Content)
	}
}

func TestContextualStatusWinsAtHeightOne(t *testing.T) {
	t.Parallel()
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	active := &statusLineTestComponent{}
	m.tabs.ReplaceComponent(QueryTab, active)

	m.OnResize(1, 0)
	assert.Zero(t, m.footerRows())
	m.OnResize(1, 1)
	assert.Equal(t, 1, m.footerRows())
	assert.False(t, m.hasKeyHintLine())
	assert.NotPanics(t, func() { m.DrawTo(compose(1, 1)) })
}

func TestKeyHintLineReservesConfiguredFooterRow(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		showHints bool
		wantH     int
	}{
		{"enabled", true, 22},
		{"disabled", false, 23},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := depstest.NewTest(t)
			bundle.Config.Core.ShowKeyHints = tt.showHints
			m, err := New(t.Context(), bundle)
			require.NoError(t, err)
			active := &hintLineTestComponent{}
			m.tabs.ReplaceComponent(QueryTab, active)
			m.OnResize(80, 24)

			assert.Equal(t, tt.wantH, active.rect.H)
			assert.Equal(t, 1, active.rect.Y)
		})
	}
}

func TestKeyHintLineHelpClickAndOverlayPrecedence(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.OnResize(80, 24)

	buf := compose(80, 24)
	m.DrawTo(buf)
	chip := buf.CellAt(0, 23)
	require.NotNil(t, chip)
	assert.Equal(t, "?", chip.Content, "Help is the first hint")

	m.onMouseClick(0, 23)
	_, opened := m.overlay.(*helpoverlay.Model)
	assert.True(t, opened, "clicking Help runs its existing action")

	items := make([][]list.Segment, 30)
	for i := range items {
		items[i] = list.PlainItem("binding")
	}
	m.overlay = helpoverlay.New(bundle, "help", items, nil)
	m.assignOverlayRect()
	buf = compose(80, 24)
	m.DrawTo(buf)
	rect, ok := m.overlayRect()
	require.True(t, ok)
	assert.Equal(t, component.Rect{W: 80, H: 23}, rect, "full Help overlay excludes the hint row")
	assert.NotZero(t, buf.CellAt(0, 23).Style.Attrs&uv.AttrFaint, "overlay dims the hint row")
	m.onMouseClick(0, 23)
	assert.Nil(t, m.overlay, "outside overlay click dismisses instead of activating Help")

	m.onMouseClick(0, 22)
	assert.Nil(t, m.overlay, "content click after dismissal is inert here")
}

// TestKeyHintLineQueryModesIncludeHelp verifies the root draws the active
// query subview's current bindings with Help first.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestKeyHintLineQueryModesIncludeHelp(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)

	assertHintLabels(t, m, []string{"help", "edit", "snippets", "files", "save", "context menu"})
	m.HandleKey(uv.KeyPressEvent{Code: 'f', Text: "f"})
	assertHintLabels(t, m, []string{"help", "open", "rename", "delete", "save", "context menu"})
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
	m.queryeditor.OnMouseClick(0, 1, uv.MouseLeft) // focus the editor before its Tab binding
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyTab})
	assertHintLabels(t, m, []string{"help", "insert", "close", "filter", "save", "context menu"})
}

func assertHintLabels(t *testing.T, m *Model, want []string) {
	t.Helper()
	hints := keys.HintBindings(m.GetKeybinds())
	labels := make([]string, len(hints))
	for i, hint := range hints {
		labels[i] = hint.Label
	}
	assert.Equal(t, want, labels)
}

// TestKeyHintLineArchiveShowsOnlyGlobalHints keeps experimental Archive's full
// Help inventory while suppressing its shared filehandler actions from the hint row.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestKeyHintLineArchiveShowsOnlyGlobalHints(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.Core.EnableExperimental = true
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.tabs.Select(ArchiveTab)

	hints := keys.HintBindings(m.GetKeybinds())
	require.Len(t, hints, 2)
	assert.Equal(t, []string{"help", "context menu"}, []string{hints[0].Label, hints[1].Label})
	assert.Equal(t, []int{1, 6}, []int{hints[0].Priority, hints[1].Priority})

	contexts := make(map[string]bool)
	for _, binding := range m.GetKeybinds() {
		contexts[binding.Context] = true
	}
	for _, want := range []string{"select file", "rename file", "delete file", "open/collect/poll the selected archive"} {
		assert.Truef(t, contexts[want], "complete Help binding set must retain %q", want)
	}
}

func TestKeyHintLineInstances(t *testing.T) {
	t.Parallel()
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	m.tabs.Select(InstancesTab)

	assertHintLabels(t, m, []string{"help", "select", "fetch", "load", "all", "context menu", "first", "watch", "cancel all", "save"})
}

func TestKeyHintLineLogsTimelineTransition(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.OnResize(80, 24)
	m.tabs.Select(LogsTab)

	store := logviewer.NewLogStore(bundle, nil, "test")
	store.SetPoster(&msgstest.FakePoster{})
	store.SetLogs([]icl.Log{{Data: map[string]any{"message": "log"}}})
	m.logviewer.SetStore(store)
	assertHintLabels(t, m, []string{"help", "expand", "all", "search", "jq", "context menu", "filter", "timeline", "save"})

	var foundContextHint bool
	for _, hint := range keys.HintBindings(m.GetKeybinds()) {
		if hint.Label == "context menu" {
			foundContextHint = true
			assert.Equal(t, []string{"c"}, hint.Keys)
		}
	}
	assert.True(t, foundContextHint, "Logs hints must include the context-menu key")

	m.HandleKey(keystest.PressRuneUV(t, 't'))
	m.DrawTo(compose(80, 24))
	assertHintLabels(t, m, []string{"help", "jump", "close", "save", "context menu"})

	m.HandleKey(keystest.PressRuneUV(t, 't'))
	m.DrawTo(compose(80, 24))
	assertHintLabels(t, m, []string{"help", "expand", "all", "search", "jq", "context menu", "filter", "timeline", "save"})
}

func TestContextualStatusTimelineDelegationAndActions(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Core.ShowKeyHints = false
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	poster := &msgstest.FakePoster{}
	m.SetPoster(poster)
	m.OnResize(80, 24)
	m.tabs.Select(LogsTab)

	store := logviewer.NewLogStore(bundle, nil, "test")
	store.SetPoster(poster)
	store.SetLogs([]icl.Log{
		{Data: map[string]any{"message": "a"}, Metadata: icl.Metadata{TSMicro: 0, Severity: icl.SeverityInfo}},
		{Data: map[string]any{"message": "b"}, Metadata: icl.Metadata{TSMicro: 100, Severity: icl.SeverityInfo}},
		{Data: map[string]any{"message": "c"}, Metadata: icl.Metadata{TSMicro: 200, Severity: icl.SeverityInfo}},
		{Data: map[string]any{"message": "d"}, Metadata: icl.Metadata{TSMicro: 300, Severity: icl.SeverityInfo}},
	})
	m.logviewer.SetStore(store)
	bundle.State.SetJQ(".")

	m.DrawTo(compose(80, 24))
	assert.Equal(t, []string{"Span", "Log", "JQ"}, statusPillHeads(m.logviewer.StatusVariants()[0]))
	poster.Posted = nil

	m.HandleKey(keystest.PressRuneUV(t, 't'))
	m.DrawTo(compose(80, 24))
	assert.Equal(t, []string{"Bucket", "Time", "Logs"}, statusPillHeads(m.logviewer.StatusVariants()[0]))
	assert.Equal(t, "1/4", statusPillValue(t, m.logviewer.StatusVariants()[0], "Bucket"))
	for x := range 80 {
		m.onMouseClick(x, m.footerTop())
	}
	assert.Empty(t, poster.Posted, "timeline redraw clears normal modifier action ranges")

	m.HandleKey(keystest.PressRuneUV(t, 'j'))
	m.DrawTo(compose(80, 24))
	assert.Equal(t, "2/4", statusPillValue(t, m.logviewer.StatusVariants()[0], "Bucket"),
		"keyboard selection updates the rendered provider without a post")
	m.OnMouse(uv.MouseClickEvent{X: 1, Y: 4, Button: uv.MouseLeft})
	m.DrawTo(compose(80, 24))
	assert.Equal(t, "4/4", statusPillValue(t, m.logviewer.StatusVariants()[0], "Bucket"),
		"mouse selection updates the rendered provider without a post")

	m.HandleKey(keystest.PressRuneUV(t, 't'))
	m.DrawTo(compose(80, 24))
	assert.Equal(t, []string{"Span", "Log", "JQ"}, statusPillHeads(m.logviewer.StatusVariants()[0]))
	for x := range 80 {
		m.onMouseClick(x, m.footerTop())
	}
	assert.NotEmpty(t, poster.Posted, "normal modifier action ranges return when Timeline closes")
}

func statusPillHeads(pills component.StatusVariant) []string {
	heads := make([]string, len(pills))
	for i, pill := range pills {
		heads[i] = pill.Head
	}
	return heads
}

func statusPillValue(t *testing.T, pills component.StatusVariant, head string) string {
	t.Helper()
	for _, pill := range pills {
		if pill.Head == head {
			return pill.Value
		}
	}
	t.Fatalf("missing %q pill in %#v", head, pills)
	return ""
}

func TestKeyHintLineUndersizedScreensAreSafe(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)

	for _, size := range [][2]int{{0, 0}, {1, 1}, {1, 2}} {
		m.OnResize(size[0], size[1])
		if size[0] > 0 && size[1] > 0 {
			assert.NotPanics(t, func() {
				m.DrawTo(compose(size[0], size[1]))
			})
		}
	}
}
