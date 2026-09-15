package logviewer

import (
	"image/color"
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hasShowContextMenu reports whether any captured event is a ShowContextMenuMsg.
func hasShowContextMenu(posted []uv.Event) bool {
	for _, ev := range posted {
		if _, ok := ev.(msgs.ShowContextMenuMsg); ok {
			return true
		}
	}
	return false
}

// newViewerModel builds a viewer with a populated adopted store and a recording
// poster shared by the viewer and store.
func newViewerModel(t *testing.T, n int) (*Model, *msgstest.FakePoster) {
	t.Helper()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	poster := &msgstest.FakePoster{}
	m.SetPoster(poster)
	m.SetRect(component.Rect{W: 80, H: 20})
	logs := make([]icl.Log, n)
	for i := range logs {
		logs[i] = icl.Log{Data: map[string]any{fieldMsg: "line"}, Metadata: icl.Metadata{TSMicro: int64(1_000 + i), Severity: icl.SeverityInfo}}
	}
	store := NewLogStore(bundle, nil, "test-instance")
	store.SetPoster(poster)
	store.loadLogs(logs)
	m.SetStore(store)
	return m, poster
}

// TestLogviewer_OnMouseClick_RightClick_OnField_OpensContextMenu verifies that a
// right-click on a field line of an expanded log moves the cursor onto that line
// in place AND opens the context menu. It must NOT toggle expansion.
func TestLogviewer_OnMouseClick_RightClick_OnField_OpensContextMenu(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t))
	fp := &msgstest.FakePoster{}
	m.SetPoster(fp)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 200, H: 40})
	store := NewLogStore(depstest.NewTest(t), nil, "test")
	store.SetPoster(fp)
	store.SetLogs([]icl.Log{{Data: map[string]any{
		"data": map[string]any{"log": map[string]any{"status_code": float64(200)}},
	}}})
	m.SetStore(store)
	m.SetExpand(0, true)
	// Establish the viewport with the cursor parked at line 0.
	s := uv.NewScreenBuffer(200, 40)
	m.Draw(s)
	require.Equal(t, 0, m.cursor.logLine, "setup: cursor parked at line 0")

	_, logsR, _ := m.subRects()
	// Expanded lines: 0:{ 1:"data":{ 2:"log":{ 3:"status_code":200. Right-click
	// the status_code field row (relative row 3).
	consumed := m.OnMouseClick(logsR.X+1, logsR.Y+3, uv.MouseRight)
	assert.True(t, consumed, "right-click on a field row must be consumed")
	assert.Equal(t, 3, m.cursor.logLine, "right-click moves the cursor onto the clicked field line")
	assert.True(t, m.store.state.expanded[0], "right-click must NOT collapse the log")
	assert.True(t, hasShowContextMenu(fp.Posted), "right-click on a field must open the context menu")
}

// TestLogviewer_OnMouseClick_RightClick_Collapsed_OpensContextMenu verifies that
// a right-click on a collapsed log moves the cursor (parity with left-click
// select) and opens the context menu — regardless of expansion, the menu shows
// the always-applicable items (the field items are simply absent).
func TestLogviewer_OnMouseClick_RightClick_Collapsed_OpensContextMenu(t *testing.T) {
	t.Parallel()
	m, fp := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log)
	require.False(t, m.store.state.expanded[3], "target log starts collapsed")

	const target = 3
	consumed := m.OnMouseClick(logsR.X+1, logsR.Y+target, uv.MouseRight)
	assert.True(t, consumed, "right-click on a collapsed log must be consumed")
	assert.Equal(t, target, m.cursor.log, "right-click moves the cursor to the clicked log")
	assert.False(t, m.store.state.expanded[target], "right-click must not expand a collapsed log")
	assert.True(t, hasShowContextMenu(fp.collected()), "right-click on a collapsed log opens the context menu")
}

// newMouseModel builds a logviewer with a known rect, several single-line
// (collapsed) logs, and a render pass so the viewport/cursor are established.
// Collapsed logs marshal to exactly one line each, so with the cursor parked at
// log 0 / line 0 (the default after SetStore) and cursor.y==0, log k renders at
// relative row k inside the logs sub-rect (logsR.Y + k). The rect is tall enough
// (H=20 ⇒ logs area H=18) that all logs are on screen.
func newMouseModel(t *testing.T, nLogs int) (*Model, *fakePoster) {
	t.Helper()
	bundle := depstest.NewTest(t)
	m := New(bundle)
	fp := &fakePoster{}
	m.SetPoster(fp)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 80, H: 20})

	store := NewLogStore(bundle, nil, "test")
	store.SetPoster(fp)
	logs := make([]icl.Log, nLogs)
	for i := range logs {
		logs[i] = icl.Log{Data: map[string]any{"i": i, "msg": "hello"}}
	}
	store.SetLogs(logs)
	m.SetStore(store)

	// Establish the viewport: cursor at log 0/line 0, y 0.
	s := uv.NewScreenBuffer(80, 20)
	m.Draw(s)
	require.Equal(t, 0, m.cursor.log)
	require.Equal(t, 0, m.cursor.logLine)
	require.Equal(t, 0, m.cursor.y)
	return m, fp
}

// TestLogviewer_OnMouseClick_SearchBarRow verifies a click on the search-bar row
// TestLogviewer_OnMouseClick_LogArea_NonCursorLog verifies a click on a log that
// is not the current cursor log moves the selection ONTO that log IN-PLACE — the
// clicked row stays where it is, with no viewport recenter and no horizontal
// scroll change. This locks in the in-place behavior so a regression back to
// Center() (which would hard-set cursor.y = h>>1 and reset xOffset) fails.
func TestLogviewer_OnMouseClick_LogArea_NonCursorLog(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log)

	// Establish a non-zero horizontal scroll so we can assert it is preserved.
	m.xOffset = 4

	// Log 3 renders at relative row 3 (single-line logs, cursor at log0/line0/y0),
	// i.e. absolute logsR.Y + 3. Click inside the text area (x past the sidebar).
	const target = 3
	consumed := m.OnMouseClick(logsR.X+1, logsR.Y+target, uv.MouseLeft)
	assert.True(t, consumed, "log-area click must be consumed")
	assert.Equal(t, target, m.cursor.log, "selection must move to the clicked log")
	assert.Equal(t, 0, m.cursor.logLine, "collapsed log selection lands on line 0")
	// In-place: the clicked row stays put (cursor.y == relY), NOT recentered to
	// the middle of the viewport (which is what Center would do).
	assert.Equal(t, target, m.cursor.y, "in-place select: clicked row must stay put (no recenter)")
	assert.NotEqual(t, logsR.H>>1, m.cursor.y, "must not recenter to mid-viewport")
	assert.Equal(t, 4, m.xOffset, "in-place select must preserve horizontal scroll")
}

// TestLogviewer_OnMouseClick_LogArea_SidebarColumn verifies a click in the
// sidebar gutter column (x < logsR.X) still maps to the log at that row, so the
// whole row is clickable.
func TestLogviewer_OnMouseClick_LogArea_SidebarColumn(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	sidebarR, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log)
	require.Less(t, sidebarR.X, logsR.X, "sidebar must sit left of the logs area")

	const target = 2
	consumed := m.OnMouseClick(sidebarR.X, logsR.Y+target, uv.MouseLeft)
	assert.True(t, consumed, "sidebar-column click must be consumed (whole row clickable)")
	assert.Equal(t, target, m.cursor.log, "sidebar click must select the log at that row")
	assert.Equal(t, target, m.cursor.y, "sidebar select is also in-place (no recenter)")
}

// TestLogviewer_OnMouseClick_LogArea_CursorLog verifies that a single left-click
// on the log already under the cursor only selects (moves cursor) and does NOT
// toggle expansion — expansion is now a double-click action.
func TestLogviewer_OnMouseClick_LogArea_CursorLog(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log)
	require.False(t, m.store.state.expanded[0], "log 0 starts collapsed")

	// Cursor log 0 renders at relative row 0: single click must NOT expand.
	consumed := m.OnMouseClick(logsR.X+1, logsR.Y, uv.MouseLeft)
	assert.True(t, consumed, "cursor-log click must be consumed")
	assert.False(t, m.store.state.expanded[0], "single left-click must NOT toggle expansion")
	assert.Equal(t, 0, m.cursor.log, "cursor stays on log 0")

	// A second single click also must not toggle.
	consumed = m.OnMouseClick(logsR.X+1, logsR.Y, uv.MouseLeft)
	assert.True(t, consumed)
	assert.False(t, m.store.state.expanded[0], "repeated single left-click must still NOT toggle expansion")
}

// TestLogviewer_OnMouseClick_SameLog_OtherLine_MovesCursor verifies that
// clicking a line of the expanded cursor log that is NOT the cursor's own line
// moves the in-log cursor to that line WITHOUT collapsing the log (line != log).
func TestLogviewer_OnMouseClick_SameLog_OtherLine_MovesCursor(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log)
	require.Equal(t, 0, m.cursor.logLine)

	// Expand the cursor log so it spans multiple rows.
	m.SetExpand(0, true)
	require.Greater(t, len(m.getRenderCacheEntry(0)), 1, "setup: expanded log must render multiple lines")

	// Click line 1 of the same (cursor) log.
	consumed := m.OnMouseClick(logsR.X+1, logsR.Y+1, uv.MouseLeft)
	assert.True(t, consumed, "in-log line click must be consumed")
	assert.True(t, m.store.state.expanded[0], "clicking a different line of the same log must NOT collapse it")
	assert.Equal(t, 0, m.cursor.log, "cursor stays on the same log")
	assert.Equal(t, 1, m.cursor.logLine, "cursor moves to the clicked line within the expanded log")
	assert.Equal(t, 1, m.cursor.y, "in-place: the clicked line stays put")
}

// TestLogviewer_OnMouseClick_CursorLine_NoExpansionToggle verifies that a single
// left-click on the cursor's own line does NOT toggle expansion (expansion is now
// a double-click action). The click must be consumed and the cursor must stay put.
func TestLogviewer_OnMouseClick_CursorLine_NoExpansionToggle(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.False(t, m.store.state.expanded[0], "log 0 starts collapsed")

	// Single click on the cursor line (log0/line0 at row 0) — must NOT expand.
	consumed := m.OnMouseClick(logsR.X+1, logsR.Y, uv.MouseLeft)
	assert.True(t, consumed, "click on cursor line must be consumed")
	assert.False(t, m.store.state.expanded[0], "single left-click must NOT expand")

	// Manually expand so we can verify collapse is also NOT triggered by a click.
	m.SetExpand(0, true)
	require.True(t, m.store.state.expanded[0], "setup: log 0 now expanded")
	require.Greater(t, len(m.getRenderCacheEntry(0)), 1, "setup: expanded log must render multiple lines")

	// Move in-log cursor to line 1, then single-click it — must not collapse.
	m.OnMouseClick(logsR.X+1, logsR.Y+1, uv.MouseLeft)
	require.Equal(t, 1, m.cursor.logLine, "setup: cursor moved onto line 1")
	consumed = m.OnMouseClick(logsR.X+1, logsR.Y+1, uv.MouseLeft)
	assert.True(t, consumed)
	assert.True(t, m.store.state.expanded[0], "single left-click on cursor's own line must NOT collapse")
}

// TestLogviewer_OnMouseClick_TimelineActive verifies that when the timeline
// overlay is active a click forwards to timeline.OnMouseClick and the logviewer
// acts on the resulting PendingJump/WantsClose exactly like the key path: a
// jump-producing click moves the log cursor and closes the overlay.
func TestLogviewer_OnMouseClick_TimelineActive(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)

	// Open the timeline over the store, cursor parked at the top bucket.
	ts, ok := m.store.GetTSMicro(m.cursor.log)
	m.timeline.SetRect(m.drawRect)
	m.timeline.Open(m.store, ts, ok)
	m.timelineActive = true

	// A click on the cursor bucket row (top row, y == drawRect.Y) requests a
	// jump in the timeline; the logviewer must read it, move the log cursor, and
	// close the overlay — mirroring the Enter key path in HandleKey.
	consumed := m.OnMouseClick(2, m.drawRect.Y, uv.MouseLeft)
	assert.True(t, consumed, "timeline-active click on a bucket row must be consumed")
	assert.False(t, m.timelineActive, "a jump-producing timeline click must close the overlay")
}

// TestLogviewer_OnMousePaste_LogArea_Ignored verifies a middle-click in the log
// area inserts nothing (there is no text input there).
func TestLogviewer_OnMousePaste_LogArea_Ignored(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()

	ok := m.OnMousePaste(logsR.X+1, logsR.Y+1, "abc")

	assert.False(t, ok, "middle-click in the log area must not paste")
}

// TestLogviewer_OnMouseDoubleClick_CollapsedLog_ExpandsLog verifies that a
// double-click on a collapsed log row moves the cursor onto it AND expands it.
func TestLogviewer_OnMouseDoubleClick_CollapsedLog_ExpandsLog(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log)
	require.False(t, m.store.state.expanded[3], "log 3 starts collapsed")

	const target = 3
	consumed := m.OnMouseDoubleClick(logsR.X+1, logsR.Y+target, uv.MouseLeft)
	assert.True(t, consumed, "double-click on a collapsed log must be consumed")
	assert.Equal(t, target, m.cursor.log, "double-click moves cursor to the clicked log")
	assert.Equal(t, 0, m.cursor.logLine, "collapsed log cursor lands on line 0")
	assert.Equal(t, target, m.cursor.y, "double-click places cursor in-place (no recenter)")
	assert.True(t, m.store.state.expanded[target], "double-click must expand the clicked log")
}

// TestLogviewer_OnMouseDoubleClick_ExpandedLog_CollapsesLog verifies that a
// double-click on an already-expanded log collapses it.
func TestLogviewer_OnMouseDoubleClick_ExpandedLog_CollapsesLog(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	m.SetExpand(2, true)
	require.True(t, m.store.state.expanded[2], "log 2 starts expanded")

	const target = 2
	consumed := m.OnMouseDoubleClick(logsR.X+1, logsR.Y+target, uv.MouseLeft)
	assert.True(t, consumed, "double-click on an expanded log must be consumed")
	assert.Equal(t, target, m.cursor.log, "double-click moves cursor to the clicked log")
	assert.False(t, m.store.state.expanded[target], "double-click must collapse the already-expanded log")
}

var (
	_ component.FocusReleaser    = (*Model)(nil)
	_ component.MouseHoverTarget = (*Model)(nil)
)

// TestLogviewer_OnMouseHover_OverLogRow_SetsHover verifies that moving the
// pointer over a log row records that (log, line) as the hover row and reports a
// change.
func TestLogviewer_OnMouseHover_OverLogRow_SetsHover(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()

	const target = 2 // single-line logs: log k at relative row k
	changed := m.OnMouseHover(logsR.X+3, logsR.Y+target)

	assert.True(t, changed, "hovering a new log row reports a change")
	assert.True(t, m.hasHover, "hover becomes active")
	assert.Equal(t, target, m.hoverLog, "hover records the log under the pointer")
	assert.Equal(t, 0, m.hoverLine, "collapsed log hover lands on line 0")
}

// TestLogviewer_OnMouseHover_EmptyRow_ClearsHover verifies that moving onto an
// empty row (past the last rendered log) clears an active hover and reports the
// change.
func TestLogviewer_OnMouseHover_EmptyRow_ClearsHover(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.True(t, m.OnMouseHover(logsR.X+3, logsR.Y+2), "setup: hover a log row")
	require.True(t, m.hasHover)

	// Row 10 is inside the logs rect (H=18) but past the 5 logs → no log there.
	changed := m.OnMouseHover(logsR.X+3, logsR.Y+10)

	assert.True(t, changed, "clearing an active hover reports a change")
	assert.False(t, m.hasHover, "moving onto an empty row clears the hover")
}

// TestLogviewer_OnMouseHover_SameRow_NoChange verifies that re-hovering the same
// row reports no change (so callers can gate redraw work to real moves).
func TestLogviewer_OnMouseHover_SameRow_NoChange(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.True(t, m.OnMouseHover(logsR.X+3, logsR.Y+2), "setup: first hover changes")

	changed := m.OnMouseHover(logsR.X+5, logsR.Y+2) // same row, different column

	assert.False(t, changed, "re-hovering the same row reports no change")
	assert.Equal(t, 2, m.hoverLog, "hover stays on the same log")
}

// TestLogviewer_OnMouseHover_TimelineActive_DelegatesToTimeline verifies that
// while the timeline overlay is active a hover is forwarded to the timeline
// (which records the bucket under the pointer) rather than tinting a log row.
func TestLogviewer_OnMouseHover_TimelineActive_DelegatesToTimeline(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	ts, ok := m.store.GetTSMicro(m.cursor.log)
	m.timeline.SetRect(m.drawRect)
	m.timeline.Open(m.store, ts, ok)
	m.timelineActive = true

	changed := m.OnMouseHover(2, m.drawRect.Y) // top bucket row

	assert.True(t, changed, "hover while the timeline is active must delegate to it (records the bucket)")
	assert.False(t, m.hasHover, "logviewer must not hover its own log rows while the timeline is active")
}

// bgAt returns the background color of the cell at (x, y), or nil when the cell
// is absent or has no background.
func bgAt(s component.Screen, x, y int) color.Color {
	c := s.CellAt(x, y)
	if c == nil {
		return nil
	}
	return c.Style.Bg
}

// TestLogviewer_HoverRow_TintsTextRow verifies that the hovered (non-cursor) row
// gets the configured hover background tint across its text area while the cursor
// row and other rows do not. The probe column sits in the row's blank padding
// (past the short log content) so its background comes only from the hover
// overlay, never from token styling.
func TestLogviewer_HoverRow_TintsTextRow(t *testing.T) {
	t.Parallel()
	m, _ := newMouseModel(t, 5)
	_, logsR, _ := m.subRects()
	require.Equal(t, 0, m.cursor.log, "setup: cursor on log 0 (row 0)")
	require.True(t, m.bundle.Config.Style.HoverRowBg.IsSet(), "setup: hover bg has a default color")
	want := m.bundle.Config.Style.HoverRowBg.Color

	const target = 2
	require.True(t, m.OnMouseHover(logsR.X+3, logsR.Y+target), "setup: hover log 2")

	s := uv.NewScreenBuffer(80, 20)
	m.Draw(s)

	const probe = 30 // blank padding column, background comes only from the tint
	assert.Equal(t, want, bgAt(s, logsR.X+probe, logsR.Y+target),
		"hovered row must get the hover background tint")
	assert.NotEqual(t, want, bgAt(s, logsR.X+probe, logsR.Y),
		"cursor row must NOT be tinted (cursor highlight wins)")
	assert.NotEqual(t, want, bgAt(s, logsR.X+probe, logsR.Y+1),
		"a non-hovered, non-cursor row must not be tinted")
}

// TestLogviewer_NoInternalPillRow verifies normal logs reclaim the row that
// formerly contained modifier pills; root now owns the contextual status row.
func TestLogviewer_NoInternalPillRow(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 5)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 80, H: 20})

	_, logsR, statusR := m.subRects()
	assert.Equal(t, 20, logsR.H)
	assert.Equal(t, component.Rect{}, statusR)
}
