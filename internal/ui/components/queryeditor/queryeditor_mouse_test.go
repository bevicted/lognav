package queryeditor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// queryeditor implements the viewport-scroll seam.
var _ component.ScrollTarget = (*Model)(nil)

// queryeditor implements the hover seam.
var _ component.MouseHoverTarget = (*Model)(nil)

// TestOnMouseHover_EditorMode_NoHover verifies the default text-editor mode has
// no row to hover (returns false).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseHover_EditorMode_NoHover(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode")
	require.False(t, m.showSnippets, "setup: editor mode")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})

	assert.False(t, m.OnMouseHover(5, 5), "editor mode has no row hover")
}

// TestOnMouseHover_SnippetMode_DelegatesToList verifies snippet-picker mode tints
// the hovered snippet row (without moving the cursor) and reports a change only
// on a row move.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseHover_SnippetMode_DelegatesToList(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	m.showSnippets = true
	items := make([][]list.Segment, 20)
	for i := range items {
		items[i] = list.PlainItem("snip " + string(rune('a'+i%26)))
	}
	m.snippetList.WithItems(items)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 12})
	require.Equal(t, 0, m.snippetList.GetCursor(), "setup: snippet cursor at top")

	// Snippet list rows: listRect.Y=2 → y=4 maps to display idx 2.
	changed := m.OnMouseHover(5, 4)

	assert.True(t, changed, "snippet-mode hover over a row reports a change")
	assert.Equal(t, 0, m.snippetList.GetCursor(), "hover must not move the snippet cursor")
	assert.False(t, m.OnMouseHover(6, 4), "re-hovering the same row reports no change")
}

// TestOnMouseScroll_EditorMode_PansEditor verifies that in the default text
// editor mode a wheel notch pans the editor viewport (drag-at-edge, caret stays)
// and is consumed — the same decoupled scroll the list panes get.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseScroll_EditorMode_PansEditor(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode")
	require.False(t, m.showSnippets, "setup: editor mode")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10}) // editor height 10

	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i)
	}
	m.editor.SetValue(strings.Join(lines, "\n")) // taller than the viewport
	before := m.editor.ScrollYOffset()
	require.Positive(t, before, "setup: editor scrolled to the buffer end")

	ok := m.OnMouseScroll(5, 5, -3) // wheel up
	assert.True(t, ok, "editor-mode wheel must be consumed (pans the editor viewport)")
	assert.Less(t, m.editor.ScrollYOffset(), before, "editor-mode wheel must pan the editor viewport")
}

// TestOnMouseScroll_SnippetMode_PansList verifies that in snippet-picker mode a
// wheel notch pans the snippet list (drag-at-edge) and is consumed.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseScroll_SnippetMode_PansList(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	m.showSnippets = true
	items := make([][]list.Segment, 20)
	for i := range items {
		items[i] = list.PlainItem("snip " + string(rune('a'+i%26)))
	}
	m.snippetList.WithItems(items)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 12})
	require.Equal(t, 0, m.snippetList.GetCursor(), "setup: snippet cursor at top")

	ok := m.OnMouseScroll(5, 5, 5)
	assert.True(t, ok, "snippet-mode wheel must be consumed")
	assert.Positive(t, m.snippetList.GetCursor(), "snippet-mode wheel must pan the snippet list")
}

// writeQueryFile writes a saved-query file (name+.dataprime) into the
// queryeditor's queries dir so the file browser lists it.
func writeQueryFile(t *testing.T, name, body string) {
	t.Helper()
	dir, err := getDirPath()
	require.NoError(t, err, "getDirPath must resolve the queries dir")
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+queryFileExt), []byte(body), 0o600))
}

// hasQueryLoad reports whether any posted event is a successful (no-error)
// QueryLoadMsg — the signal that the cursor-row click loaded a saved query.
func hasQueryLoad(events []uv.Event) bool {
	for _, ev := range events {
		if q, ok := ev.(QueryLoadMsg); ok && q.Err == nil {
			return true
		}
	}
	return false
}

// TestOnMouseClick_FileBrowserMode_ForwardsToFileHandler verifies that in
// file-browser mode a non-cursor row click selects (moves the cursor, select
// only) and a single cursor-row click does NOT load (double-click required for
// activation in double-click-activate mode).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseClick_FileBrowserMode_ForwardsToFileHandler(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	writeQueryFile(t, "query-a", "source logs a")
	writeQueryFile(t, "query-b", "source logs b")

	m.showFiles = true

	// Populate the filehandler list and render once so the inner list's rect is set.
	m.fh.ListFiles()
	for _, ev := range fp.Posted {
		if io, ok := ev.(filehandler.IOMsg); ok {
			m.OnFileIO(io)
		}
	}
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	topItem := m.fh.GetItemUnderCursor()
	require.NotEmpty(t, topItem, "list must have at least one saved-query row under the cursor")

	// Non-cursor row click (display idx 1) selects it: listRect.Y=2, row=3-2=1.
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed, "left-half click on a query row must be consumed in file-browser mode")
	assert.NotEqual(t, topItem, m.fh.GetItemUnderCursor(), "non-cursor row click must move the cursor")
	require.False(t, hasQueryLoad(fp.Posted), "selecting a row must NOT load it")

	// Single click on the cursor row: select-only in double-click-activate mode.
	consumed2 := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed2, "cursor-row single click must be consumed")
	assert.False(t, hasQueryLoad(fp.Posted), "cursor-row single click must NOT load (double-click required)")
}

// TestOnMouseDoubleClick_FileBrowserMode_Activates verifies that in file-browser
// mode OnMouseDoubleClick forwards to the filehandler and loads the query
// (runs the activate hook → QueryLoadMsg), while editor mode falls back to
// single-click behavior (focuses editor, no panic).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseDoubleClick_FileBrowserMode_Activates(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	writeQueryFile(t, "query-a", "source logs a")
	writeQueryFile(t, "query-b", "source logs b")

	m.showFiles = true

	m.fh.ListFiles()
	for _, ev := range fp.Posted {
		if io, ok := ev.(filehandler.IOMsg); ok {
			m.OnFileIO(io)
		}
	}
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	topItem := m.fh.GetItemUnderCursor()
	require.NotEmpty(t, topItem, "list must have at least one query row")

	// Single click: selects (non-cursor row → move cursor).
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed, "single click must be consumed (select)")
	require.False(t, hasQueryLoad(fp.Posted), "single click must NOT load the query")

	// Double-click on the (now-cursor) row: activates (loads the query).
	consumed2 := m.OnMouseDoubleClick(5, 3, uv.MouseLeft)
	assert.True(t, consumed2, "double-click in file-browser mode must be consumed")
	assert.True(t, hasQueryLoad(fp.Posted), "double-click must run the load action (QueryLoadMsg)")
}

// TestOnMouseClick_SnippetMode_SelectsRow verifies that in snippet-picker mode a
// left-click on a snippet row moves the snippet-list cursor (select only) and is
// consumed by the snippet list — it must NOT fall through to the editor caret
// placement (the background-caret bug).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseClick_SnippetMode_SelectsRow(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	m.editor.SetValue("")
	m.snippets = []snippet{
		{Snippet: "| filter a", Description: "filter a"},
		{Snippet: "| filter b", Description: "filter b"},
		{Snippet: "| filter c", Description: "filter c"},
	}
	m.snippetList.WithItems(formatSnippetList(m.snippets))
	m.showSnippets = true
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 12}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))
	require.Equal(t, 0, m.snippetList.GetCursor(), "setup: snippet cursor at top")

	// Snippet rows: listRect.Y=2 → y=3 maps to display idx 1.
	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)

	assert.True(t, consumed, "snippet-mode click must be consumed by the snippet list")
	assert.Equal(t, 1, m.snippetList.GetCursor(), "click must move the snippet cursor (select)")
	assert.True(t, m.showSnippets, "single click must not exit snippet mode")
	assert.Empty(t, m.getQuery(), "single click must not insert a snippet")
	assert.False(t, m.editor.Focused(), "snippet-mode click must NOT focus the editor in the background")
}

// TestOnMouseDoubleClick_SnippetMode_InsertsSnippet verifies that in
// snippet-picker mode a double-click on a snippet row inserts that snippet into
// the editor, exits snippet mode, and re-grabs editor focus — the click analogue
// of the Enter (fuzzy-confirm) path.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseDoubleClick_SnippetMode_InsertsSnippet(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	m.editor.SetValue("")
	m.snippets = []snippet{
		{Snippet: "| filter a", Description: "filter a"},
		{Snippet: "| filter b", Description: "filter b"},
		{Snippet: "| filter c", Description: "filter c"},
	}
	m.snippetList.WithItems(formatSnippetList(m.snippets))
	m.showSnippets = true
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 12}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))
	fp.Posted = nil

	// Double-click snippet row at display idx 1 (listRect.Y=2 → y=3).
	consumed := m.OnMouseDoubleClick(5, 3, uv.MouseLeft)

	assert.True(t, consumed, "snippet-mode double-click must be consumed")
	assert.Equal(t, "| filter b", m.getQuery(), "double-click must insert the clicked snippet")
	assert.False(t, m.showSnippets, "double-click must exit snippet mode")
	assert.True(t, hasGrabFocus(fp.Posted), "double-click must re-grab editor focus")
}

// TestOnMouseDoubleClick_EditorMode_BehavesLikeSingleClick verifies that in
// editor mode OnMouseDoubleClick delegates to OnMouseClick — focusing the editor
// and placing the caret — since there are no file rows to activate. No panic must
// occur.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseDoubleClick_EditorMode_BehavesLikeSingleClick(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode")

	m.editor.SetValue("line zero\nline one\nline two")
	m.rehighlight()

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	// Double-click on display row 1 ("line one"), col 4.
	consumed := m.OnMouseDoubleClick(4, 1, uv.MouseLeft)
	assert.True(t, consumed, "editor-mode double-click must be consumed (falls back to single-click)")
	assert.Equal(t, 1, m.editor.Line(), "caret must move to the clicked logical row")
	assert.True(t, m.editor.Focused(), "editor must be focused")
	assert.True(t, hasGrabFocus(fp.Posted), "double-click in editor mode must emit FocusMsg{GrabFocus:true}")
}

// hasGrabFocus reports whether any posted event is a FocusMsg{GrabFocus:true} —
// the off-loop focus-grab the key path emits when the editor takes editing focus.
func hasGrabFocus(events []uv.Event) bool {
	for _, ev := range events {
		if f, ok := ev.(msgs.FocusMsg); ok && f.GrabFocus {
			return true
		}
	}
	return false
}

// TestOnMouseClick_EditorMode_FocusesAndPlacesCaret verifies that in editor mode
// a click moves the editor caret to the corresponding (row, col) and focuses the
// editor the same way the FocusTextInput key path does: editor.Focus() plus an
// off-loop FocusMsg{GrabFocus:true}. The click is consumed (returns true).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseClick_EditorMode_FocusesAndPlacesCaret(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode (not file browser)")

	// Multi-line value so a y>0 click lands on a later logical row.
	m.editor.SetValue("line zero\nline one\nline two")
	m.rehighlight()

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	// Click on display row 1 ("line one"), column 4 → caret on logical row 1, col 4.
	consumed := m.OnMouseClick(4, 1, uv.MouseLeft)
	assert.True(t, consumed, "click in editor mode must be consumed (Task 4.2)")
	assert.Equal(t, 1, m.editor.Line(), "caret must move to the clicked logical row")
	assert.True(t, m.editor.Focused(), "editor must be focused after a text click")
	assert.True(t, hasGrabFocus(fp.Posted), "click must emit FocusMsg{GrabFocus:true} off-loop, mirroring the key path")
}

// TestOnMousePaste_EditorMode_InsertsAtCaret verifies that in editor mode a
// middle-click moves the caret to the clicked (row, col), focuses the editor,
// and inserts the clipboard content there (re-staging the query).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMousePaste_EditorMode_InsertsAtCaret(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode (not file browser)")

	m.editor.SetValue("line zero\nline one\nline two")
	m.rehighlight()
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	// Middle-click display row 1 ("line one"), column 4 (the space after "line").
	ok := m.OnMousePaste(4, 1, "XX")

	assert.True(t, ok, "editor-mode middle-click must paste and consume")
	assert.True(t, m.editor.Focused(), "paste focuses the editor")
	assert.Equal(t, 1, m.editor.Line(), "caret moved to the clicked logical row")
	assert.Contains(t, m.getQuery(), "lineXX one", "content inserted at the clicked caret offset")
	assert.True(t, hasGrabFocus(fp.Posted), "paste emits FocusMsg{GrabFocus:true} off-loop like the key path")
}

// TestOnMousePaste_FileBrowserMode_DelegatesToFileHandler verifies that a
// middle-click on the saved-queries fuzzy header (left half) in file-browser
// mode forwards to the filehandler/list paste and is consumed.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMousePaste_FileBrowserMode_DelegatesToFileHandler(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	writeQueryFile(t, "query-a", "source logs a")
	writeQueryFile(t, "query-b", "source logs b")
	m.showFiles = true

	// Populate the list and render once so the inner list's rect is set.
	m.fh.ListFiles()
	for _, ev := range fp.Posted {
		if io, ok := ev.(filehandler.IOMsg); ok {
			m.OnFileIO(io)
		}
	}
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	// Middle-click the fuzzy-input header (top row) in the left (list) half:
	// "query-a" fuzzy-matches only query-a, narrowing the list to one row.
	ok := m.OnMousePaste(3, 0, "query-a")

	assert.True(t, ok, "browser-mode middle-click on the fuzzy header must delegate to the list and consume")
	assert.Contains(t, m.fh.GetItemUnderCursor(), "query-a", "pasted filter must narrow the saved-query list")
}

var (
	_ component.MouseTarget      = (*Model)(nil)
	_ component.MousePasteTarget = (*Model)(nil)
	_ component.FocusReleaser    = (*Model)(nil)
)

// TestReleaseFocus_BlursEditor verifies that ReleaseFocus drops the editor's
// input focus (the path the root model takes when a click switches tabs while
// the query editor holds focus).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestReleaseFocus_BlursEditor(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	m.editor.Focus()
	require.True(t, m.editor.Focused(), "setup: editor focused")

	m.ReleaseFocus()

	assert.False(t, m.editor.Focused(), "ReleaseFocus must blur the editor")
}

// TestModel_ImplementsContextMenuSeams asserts queryeditor opts into the context
// menu seams (delegated to its saved-queries filehandler in file-browser mode).
func TestModel_ImplementsContextMenuSeams(t *testing.T) {
	t.Parallel()
	var _ component.ContextMenuOpener = (*Model)(nil)
	var _ component.SecondaryMouseTarget = (*Model)(nil)
}

// TestOnMouseRight_SnippetMode_NotConsumed verifies that in the snippet picker the
// right-click is not consumed (no menu in snippet mode).
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseRight_SnippetMode_NotConsumed(t *testing.T) {
	m, _ := newHandleKeyTestModel(t)
	m.showSnippets = true
	assert.False(t, m.OnMouseRight(1, 1))
}

// TestOnMouseRight_EditorMode_MovesCaretToClick verifies that in editor mode a
// right-click moves the editor caret to the clicked (row, col) before opening
// the context menu — mirroring the left-click caret placement — so a subsequent
// "insert snippet" lands at the clicked position rather than the stale caret.
// The menu is still posted.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestOnMouseRight_EditorMode_MovesCaretToClick(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode")
	require.False(t, m.showSnippets, "setup: editor mode")

	m.editor.SetValue("line zero\nline one\nline two")
	m.rehighlight()
	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	_ = m.Draw(uv.NewScreenBuffer(rect.W, rect.H))

	// Right-click display row 1 ("line one"), column 4.
	consumed := m.OnMouseRight(4, 1)

	assert.True(t, consumed, "editor-mode right-click must be consumed")
	assert.Equal(t, 1, m.editor.Line(), "right-click must move the caret to the clicked logical row before opening the menu")
	assert.True(t, hasShowContextMenu(fp.Posted), "right-click must still post the context menu")
}

// firstShowContextMenu returns the first posted ShowContextMenuMsg, failing the
// test if none was posted.
func firstShowContextMenu(t *testing.T, events []uv.Event) msgs.ShowContextMenuMsg {
	t.Helper()
	for _, ev := range events {
		if msg, ok := ev.(msgs.ShowContextMenuMsg); ok {
			return msg
		}
	}
	t.Fatalf("no ShowContextMenuMsg posted; got %#v", events)
	return msgs.ShowContextMenuMsg{}
}

// hasShowContextMenu reports whether any posted event is a ShowContextMenuMsg.
func hasShowContextMenu(events []uv.Event) bool {
	for _, ev := range events {
		if _, ok := ev.(msgs.ShowContextMenuMsg); ok {
			return true
		}
	}
	return false
}

// itemByLabel returns the menu item with the given label, failing the test if
// none matches.
func itemByLabel(t *testing.T, items []msgs.ContextMenuItem, label string) msgs.ContextMenuItem {
	t.Helper()
	for _, it := range items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no menu item labelled %q; got %#v", label, items)
	return msgs.ContextMenuItem{}
}

// itemLabels extracts the labels of the menu items in order.
func itemLabels(items []msgs.ContextMenuItem) []string {
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.Label
	}
	return labels
}

// TestEditorContextMenu_RightClick_PostsItems verifies that a right-click in
// editor mode posts an anchored, close-release-suppressed menu just below the
// pointer with the insert snippet / reset to default / clear items in order.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestEditorContextMenu_RightClick_PostsItems(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode")
	require.False(t, m.showSnippets, "setup: editor mode")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 60, H: 20})

	assert.True(t, m.OnMouseRight(4, 5), "editor-mode right-click must be consumed")

	msg := firstShowContextMenu(t, fp.Posted)
	assert.True(t, msg.Anchored, "menu must be anchored")
	assert.True(t, msg.SuppressCloseRelease, "menu must suppress close-release (items re-grab focus)")
	assert.Equal(t, 4, msg.AnchorX, "anchor X is the pointer column")
	assert.Equal(t, 6, msg.AnchorY, "anchor Y is one row below the pointer")
	assert.Equal(t, []string{"insert snippet", "reset to default", "clear"}, itemLabels(msg.Items), "items in order")
}

// TestEditorContextMenu_Keyboard_PostsItems verifies that OpenContextMenu in
// editor mode posts the same anchored, close-release-suppressed three-item menu.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestEditorContextMenu_Keyboard_PostsItems(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.showFiles, "setup: editor mode")
	require.False(t, m.showSnippets, "setup: editor mode")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 60, H: 20})

	m.OpenContextMenu()

	msg := firstShowContextMenu(t, fp.Posted)
	assert.True(t, msg.Anchored, "menu must be anchored")
	assert.True(t, msg.SuppressCloseRelease, "menu must suppress close-release")
	assert.Len(t, msg.Items, 3, "editor menu has three items")
}

// TestEditorContextMenu_SnippetMode_NoMenu verifies that the snippet picker
// neither consumes a right-click nor posts a menu on OpenContextMenu.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestEditorContextMenu_SnippetMode_NoMenu(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	m.openSnippets()
	require.True(t, m.showSnippets, "setup: snippet mode")
	fp.Posted = nil // drop the GrabFocus from openSnippets

	assert.False(t, m.OnMouseRight(4, 5), "snippet-mode right-click is not consumed")
	m.OpenContextMenu()
	assert.False(t, hasShowContextMenu(fp.Posted), "snippet mode must not post a context menu")
}

// TestEditorContextMenu_ClearItem_EmptiesQuery verifies that activating the clear
// item empties the editor query.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestEditorContextMenu_ClearItem_EmptiesQuery(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.NotEmpty(t, m.getQuery(), "setup: constructor seeds the default query")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 60, H: 20})

	require.True(t, m.OnMouseRight(4, 5))
	clearItem := itemByLabel(t, firstShowContextMenu(t, fp.Posted).Items, "clear")
	fp.Posted = nil // isolate the item's focus-grab post
	clearItem.Action()

	assert.Empty(t, m.getQuery(), "clear item must empty the query")
	assert.True(t, hasGrabFocus(fp.Posted), "clear must re-grab editor focus")
}

// TestEditorContextMenu_ResetItem_RestoresDefault verifies that activating the
// reset item restores the configured default query.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestEditorContextMenu_ResetItem_RestoresDefault(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	m.setQueryInEditor("something else")
	require.Equal(t, "something else", m.getQuery(), "setup: query overridden")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 60, H: 20})

	require.True(t, m.OnMouseRight(4, 5))
	reset := itemByLabel(t, firstShowContextMenu(t, fp.Posted).Items, "reset to default")
	fp.Posted = nil // isolate the item's focus-grab post
	reset.Action()

	assert.Equal(t, m.defaultQuery, m.getQuery(), "reset item must restore the resolved startup default query")
	assert.True(t, hasGrabFocus(fp.Posted), "reset must re-grab editor focus")
}

// TestEditorContextMenu_InsertSnippetItem_EntersSnippetMode verifies that
// activating the insert snippet item switches to snippet-picker mode.
//
//nolint:paralleltest // newHandleKeyTestModel calls t.Setenv (XDG_DATA_HOME); cannot parallelize.
func TestEditorContextMenu_InsertSnippetItem_EntersSnippetMode(t *testing.T) {
	m, fp := newHandleKeyTestModel(t)
	require.False(t, m.ShowingSnippets(), "setup: not in snippet mode")
	m.SetRect(component.Rect{X: 0, Y: 0, W: 60, H: 20})

	require.True(t, m.OnMouseRight(4, 5))
	insert := itemByLabel(t, firstShowContextMenu(t, fp.Posted).Items, "insert snippet")
	insert.Action()

	assert.True(t, m.ShowingSnippets(), "insert snippet item must enter snippet mode")
}
