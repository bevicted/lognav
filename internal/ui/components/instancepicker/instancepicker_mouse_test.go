package instancepicker

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/status"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMouseTestModel builds a picker with n instances and a list sized large
// enough to show all rows, with a fakePoster wired so emits are recorded. The
// list's items are seeded 1:1 with the instances (refreshList normally does this
// in Draw, but the mouse hit-test only needs the item count and a non-zero rect).
func newMouseTestModel(t *testing.T, n int) (*Model, *fakePoster) {
	t.Helper()
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	fp := &fakePoster{}
	m.SetPoster(fp)

	insts := make(Instances, n)
	items := make([][]list.Segment, n)
	for i := range n {
		insts[i] = NewInstance(bundle, "inst-"+string(rune('a'+i)), "http://example.com", testCRN("test"), "prod", "%.2fs")
		items[i] = list.PlainItem(insts[i].Name)
	}
	m.instances = insts
	m.list.WithItems(items)
	// drawRect: 2-row fuzzy header at the top, then one row per instance below.
	m.list.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 2 + n})
	return m, fp
}

// rowY returns the absolute screen row for instance display index i (header is
// the top 2 rows, items start at row 2 with no scroll).
func rowY(i int) int { return 2 + i }

// statusX / bodyX return columns inside the status-label region and inside the
// row body (name/count/time) respectively, given the configured label width.
func statusX(*Model) int { return 0 }
func bodyX(m *Model) int { return status.MaxLabelWidth(m.bundle) + 2 }

// instancepicker implements the hover seam.
var _ component.MouseHoverTarget = (*Model)(nil)

// TestOnMouseHover_InstanceRow_TintsWithoutSelecting verifies a hover over an
// instance row tints it (via the inner list) without moving the selection or
// re-emitting InstanceSelectMsg (hover is visual only, unlike scroll), and
// reports a change only on a row move.
func TestOnMouseHover_InstanceRow_TintsWithoutSelecting(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor at top")

	changed := m.OnMouseHover(bodyX(m), rowY(1))

	assert.True(t, changed, "hovering an instance row reports a change")
	assert.Equal(t, 0, m.list.GetCursor(), "hover must not move the selection")
	assert.Empty(t, fp.events(), "hover must not emit any event (no preview sync)")
	assert.False(t, m.OnMouseHover(bodyX(m), rowY(1)), "re-hovering the same row reports no change")
}

// TestOnMousePaste_FuzzyHeader_DelegatesToList verifies a middle-click on the
// fuzzy header forwards to the inner list's paste, narrowing the instance list.
func TestOnMousePaste_FuzzyHeader_DelegatesToList(t *testing.T) {
	t.Parallel()
	m, _ := newMouseTestModel(t, 3)

	// "inst-a" fuzzy-matches only inst-a (inst-b/inst-c lack the trailing 'a').
	ok := m.OnMousePaste(3, 0, "inst-a")

	assert.True(t, ok, "middle-click on the fuzzy header must delegate to the list and consume")
	assert.Equal(t, 1, m.list.VisibleLen(), "pasted filter must narrow the instance list")
}

// TestOnMousePaste_InstanceRow_Ignored verifies a middle-click on an instance
// row (not the fuzzy header) inserts nothing and is not consumed.
func TestOnMousePaste_InstanceRow_Ignored(t *testing.T) {
	t.Parallel()
	m, _ := newMouseTestModel(t, 3)

	ok := m.OnMousePaste(bodyX(m), rowY(0), "inst-a")

	assert.False(t, ok, "middle-click on an instance row must not paste")
	assert.Equal(t, 3, m.list.VisibleLen(), "an ignored paste must not filter the list")
}

// postedSelect reports whether the fake poster recorded an InstanceSelectMsg for
// name with the given OpenLogViewer flag.
func postedSelect(fp *fakePoster, name string, openLogViewer bool) bool {
	for _, ev := range fp.events() {
		if ism, ok := ev.(InstanceSelectMsg); ok && ism.Name == name && ism.OpenLogViewer == openLogViewer {
			return true
		}
	}
	return false
}

// anySelect reports whether the poster recorded any InstanceSelectMsg.
func anySelect(fp *fakePoster) bool {
	for _, ev := range fp.events() {
		if _, ok := ev.(InstanceSelectMsg); ok {
			return true
		}
	}
	return false
}

// instancepicker implements the viewport-scroll seam.
var _ component.ScrollTarget = (*Model)(nil)

// TestOnMouseScroll_DragOntoNewInstance_EmitsSelect verifies a wheel notch that
// drags the selection onto a different instance pans the list and re-emits an
// InstanceSelectMsg (preview sync), not an OpenLogViewer select.
func TestOnMouseScroll_DragOntoNewInstance_EmitsSelect(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 20)
	// listAreaHeight = H-2 = 10; 20 instances → the list can scroll.
	m.list.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 12})
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")

	ok := m.OnMouseScroll(5, 5, 5) // wheel down 5: drags selection from row 0 to row 5
	assert.True(t, ok, "a wheel notch over the instance list must be consumed")
	assert.Equal(t, 5, m.list.GetCursor(), "wheel drag must move the selection at the edge")
	assert.True(t, postedSelect(fp, "inst-f", false), "drag onto a new instance must sync the preview")
	assert.False(t, postedSelect(fp, "inst-f", true), "wheel drag must NOT open the Logs tab")
}

// TestOnMouseScroll_PanKeepsSelection_NoEmit verifies a wheel notch that pans the
// list without pushing the selection off-screen leaves the selection put and
// emits no InstanceSelectMsg (no spurious preview reload).
func TestOnMouseScroll_PanKeepsSelection_NoEmit(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 20)
	m.list.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 12}) // listAreaHeight = 10
	m.list.SetCursor(5)                                      // visible at row 5, yOffset 0

	ok := m.OnMouseScroll(5, 5, 3) // pan down 3: cursor 5 stays within [3, 13)
	assert.True(t, ok)
	assert.Equal(t, 5, m.list.GetCursor(), "a pure pan must not move the selection")
	assert.False(t, anySelect(fp), "a pan that keeps the selection visible must not emit a select")
}

func TestInstancePicker_OnMouseClick_StatusColumn_NonCursorRow_SelectsAndCycles(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")
	require.False(t, m.instances[1].IsEnabled(), "setup: target instance starts disabled")

	consumed := m.OnMouseClick(statusX(m), rowY(1), uv.MouseLeft)
	assert.True(t, consumed, "status-column click on an instance row must be consumed")
	assert.Equal(t, 1, m.list.GetCursor(), "status click must place the cursor on the clicked row")
	assert.True(t, m.instances[1].IsEnabled(), "status click must cycle the instance's status (Disabled -> Enabled)")
	assert.Equal(t, status.Enabled, m.instances[1].state)
	assert.True(t, postedSelect(fp, "inst-b", false), "status click must sync the preview (InstanceSelectMsg, no tab switch)")
	assert.False(t, postedSelect(fp, "inst-b", true), "status click must NOT open the Logs tab")
}

func TestInstancePicker_OnMouseClick_StatusColumn_CursorRow_Cycles(t *testing.T) {
	t.Parallel()
	m, _ := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")
	require.False(t, m.instances[0].IsEnabled(), "setup: cursor instance starts disabled")

	consumed := m.OnMouseClick(statusX(m), rowY(0), uv.MouseLeft)
	assert.True(t, consumed, "status-column click on the cursor row must be consumed")
	assert.Equal(t, 0, m.list.GetCursor(), "status click on the cursor row must not move the cursor")
	assert.Equal(t, status.Enabled, m.instances[0].state, "status click must cycle the cursor instance's status")
}

func TestInstancePicker_OnMouseClick_Body_NonCursorRow_SelectsOnly(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")
	require.False(t, m.instances[2].IsEnabled(), "setup: target instance starts disabled")

	consumed := m.OnMouseClick(bodyX(m), rowY(2), uv.MouseLeft)
	assert.True(t, consumed, "body-column click on an instance row must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "body click must place the cursor on the clicked row")
	assert.False(t, m.instances[2].IsEnabled(), "body click must NOT cycle the instance's status")
	// Single click is always select-only: moves the cursor and syncs the preview;
	// it must NOT open the Logs tab regardless of whether the row was the cursor.
	assert.True(t, postedSelect(fp, "inst-c", false), "body click on a non-cursor row must sync the preview")
	assert.False(t, postedSelect(fp, "inst-c", true), "body click on a non-cursor row must NOT open the Logs tab")
}

func TestInstancePicker_OnMouseClick_Body_SecondClickSelectsOnly(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")

	// First body click on a non-cursor row selects it (no open).
	m.OnMouseClick(bodyX(m), rowY(2), uv.MouseLeft)
	require.Equal(t, 2, m.list.GetCursor(), "first click selects the row")
	require.False(t, postedSelect(fp, "inst-c", true), "first click must not open")

	// Second body click on the now-cursor row also only selects: single click is
	// always select-only; activation requires a double-click.
	consumed := m.OnMouseClick(bodyX(m), rowY(2), uv.MouseLeft)
	assert.True(t, consumed, "second body click on the cursor row must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "second click stays on the same row")
	assert.False(t, postedSelect(fp, "inst-c", true), "second single click on the cursor row must NOT open the Logs tab")
}

func TestInstancePicker_OnMouseClick_Body_CursorRow_SelectsOnly(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")

	consumed := m.OnMouseClick(bodyX(m), rowY(0), uv.MouseLeft)
	assert.True(t, consumed, "body-column click on the cursor row must be consumed")
	assert.Equal(t, 0, m.list.GetCursor(), "cursor stays put")
	assert.False(t, m.instances[0].IsEnabled(), "body click on the cursor row must NOT toggle status")
	// Single click is always select-only — even on the cursor row.
	assert.True(t, postedSelect(fp, "inst-a", false), "body single-click on the cursor row must only preview-sync, not open")
	assert.False(t, postedSelect(fp, "inst-a", true), "body single-click on the cursor row must NOT open the Logs tab")
}

// TestInstancePicker_OnMouseDoubleClick_Body_MovesAndOpens verifies that a
// double-click on an instance row body moves the cursor to the clicked row and
// opens it in the log viewer (emits InstanceSelectMsg{OpenLogViewer:true}).
//
//nolint:dupl // structurally similar to TestInstancePicker_OnMouseDoubleClick_Body_CursorRow_Opens but clicks a different row (non-cursor) and asserts cursor movement
func TestInstancePicker_OnMouseDoubleClick_Body_MovesAndOpens(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")

	consumed := m.OnMouseDoubleClick(bodyX(m), rowY(2), uv.MouseLeft)
	assert.True(t, consumed, "double-click on an instance row body must be consumed")
	assert.Equal(t, 2, m.list.GetCursor(), "double-click must place the cursor on the clicked row")
	assert.False(t, m.instances[2].IsEnabled(), "double-click on body must NOT toggle instance status")
	assert.True(t, postedSelect(fp, "inst-c", true), "double-click on body must open the instance in the log viewer")
}

// TestInstancePicker_OnMouseDoubleClick_Body_CursorRow_Opens verifies that a
// double-click on the already-cursor row also opens it in the log viewer.
//
//nolint:dupl // structurally similar to TestInstancePicker_OnMouseDoubleClick_Body_MovesAndOpens but clicks the cursor row (no cursor movement) and asserts open
func TestInstancePicker_OnMouseDoubleClick_Body_CursorRow_Opens(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")

	consumed := m.OnMouseDoubleClick(bodyX(m), rowY(0), uv.MouseLeft)
	assert.True(t, consumed, "double-click on the cursor row must be consumed")
	assert.Equal(t, 0, m.list.GetCursor(), "cursor stays put on the same row")
	assert.False(t, m.instances[0].IsEnabled(), "double-click must NOT toggle instance status")
	assert.True(t, postedSelect(fp, "inst-a", true), "double-click on the cursor row must open the instance in the log viewer")
}

// TestInstancePicker_OnMouseDoubleClick_StatusLabel_NoOp verifies that a
// double-click on the status-label columns is consumed but does NOT cycle the
// status a second time (the preceding single-click already cycled it) and does
// NOT open the log viewer.
func TestInstancePicker_OnMouseDoubleClick_StatusLabel_NoOp(t *testing.T) {
	t.Parallel()
	m, fp := newMouseTestModel(t, 3)
	require.Equal(t, 0, m.list.GetCursor(), "setup: cursor starts at the top row")
	require.False(t, m.instances[1].IsEnabled(), "setup: target instance starts disabled")

	// Simulate the single-click that preceded the double-click (cycled status).
	m.OnMouseClick(statusX(m), rowY(1), uv.MouseLeft)
	require.Equal(t, status.Enabled, m.instances[1].state, "setup: single click cycled status to Enabled")
	fp.mu.Lock()
	fp.posted = fp.posted[:0] // reset recorded events
	fp.mu.Unlock()

	// The double-click must be a no-op: status stays Enabled, no open, consumed.
	consumed := m.OnMouseDoubleClick(statusX(m), rowY(1), uv.MouseLeft)
	assert.True(t, consumed, "double-click on status label must be consumed")
	assert.Equal(t, status.Enabled, m.instances[1].state, "double-click on status label must NOT cycle status again")
	assert.False(t, postedSelect(fp, "inst-b", true), "double-click on status label must NOT open the Logs tab")
}

func TestInstancePicker_OnMouseClick_HeaderRow_FocusesFuzzyInput(t *testing.T) {
	t.Parallel()
	m, _ := newMouseTestModel(t, 3)
	require.False(t, m.list.IsFocused(), "setup: fuzzy input starts blurred")

	consumed := m.OnMouseClick(2, 0, uv.MouseLeft)
	assert.True(t, consumed, "header-row click must be consumed")
	assert.True(t, m.list.IsFocused(), "header-row click must focus the fuzzy input")
}

func TestInstancePicker_OnMouseClick_OutsideRows_NotConsumed(t *testing.T) {
	t.Parallel()
	m, _ := newMouseTestModel(t, 2)
	// Click below the last item row (drawRect H = 2 + 2 = 4; rows 2,3 are items;
	// row 4 is past the rect).
	consumed := m.OnMouseClick(0, 2+5, uv.MouseLeft)
	assert.False(t, consumed, "a click past the last item row is not consumed")
}

// TestRegionAt verifies the shared pointer-geometry resolver both click seams
// read: the fuzzy header, the status-label columns, the row body, and the
// non-row fallbacks (past the last item, empty list).
func TestRegionAt(t *testing.T) {
	t.Parallel()
	m, _ := newMouseTestModel(t, 3)
	lw := status.MaxLabelWidth(m.bundle)

	tests := []struct {
		name       string
		x, y       int
		wantRegion pickerRegion
		wantIdx    int
	}{
		{"fuzzy header", 2, 0, regionHeader, 0},
		{"status column, first row", 0, rowY(0), regionStatus, 0},
		{"status column, last column", lw - 1, rowY(2), regionStatus, 2},
		{"row body, first body column", lw, rowY(1), regionBody, 1},
		{"row body, far right", 39, rowY(1), regionBody, 1},
		{"past the last item row", 0, rowY(3), regionNone, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			region, idx := m.regionAt(tt.x, tt.y)
			assert.Equal(t, tt.wantRegion, region, "region")
			assert.Equal(t, tt.wantIdx, idx, "row index")
		})
	}

	empty, _ := newMouseTestModel(t, 0)
	region, _ := empty.regionAt(0, rowY(0))
	assert.Equal(t, regionNone, region, "an empty instance list resolves to no region")
}
