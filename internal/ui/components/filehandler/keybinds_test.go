package filehandler

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBindKeyhandlersToModel_RegistersExpectedActions asserts that
// bindKeyhandlersToModel registers all expected keybinding contexts.
// keys.Action is func() and has no Name() method; we identify
// bindings by their Context field instead.
func TestBindKeyhandlersToModel_RegistersExpectedActions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))
	m, _, _, _ := newTestModel(t, dir)

	// Expected Context values from bindKeyhandlersToModel in keybinds.go.
	expectedContexts := []string{
		"select file", // accept / confirm
		"delete file", // delete under cursor
		"rename file", // rename under cursor
	}

	bindings := m.GetKeybinds()
	assert.NotEmpty(t, bindings, "GetKeybinds must return at least the handler bindings")

	seen := make(map[string]bool)
	for _, b := range bindings {
		seen[b.Context] = true
	}
	for _, want := range expectedContexts {
		assert.Truef(t, seen[want], "missing keybind context: %q", want)
	}
}

// TestDecorateHints_CopiesFileActionsWithoutMutatingSource verifies that
// context-specific hint metadata preserves the shared actions and source slice.
func TestDecorateHints_CopiesFileActionsWithoutMutatingSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))
	m, fp, read, _ := newTestModel(t, dir)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	source := m.GetKeybinds()
	decorated := DecorateHints(source)
	hints := keys.HintBindings(decorated)
	require.Len(t, hints, 3)
	assert.Equal(t, []string{"open", "rename", "delete"}, []string{hints[0].Label, hints[1].Label, hints[2].Label})
	assert.Equal(t, []int{2, 3, 4}, []int{hints[0].Priority, hints[1].Priority, hints[2].Priority})
	assert.Empty(t, keys.HintBindings(source), "shared filehandler bindings stay undecorated")

	byContext := make(map[string]keys.Binding, len(source))
	for _, binding := range source {
		byContext[binding.Context] = binding
	}
	for _, hint := range hints {
		original := byContext[map[string]string{
			"open":   "select file",
			"rename": "rename file",
			"delete": "delete file",
		}[hint.Label]]
		assert.Equal(t, reflect.ValueOf(original.Action).Pointer(), reflect.ValueOf(hint.Action).Pointer(),
			"%s must retain its existing action", hint.Label)
	}

	hints[0].Action()
	assert.NotEmpty(t, read.Path, "decorated open action reads the selected file")

	hints[1].Action()
	var dialog *msgs.ShowDialogMsg
	for _, event := range fp.events() {
		if candidate, ok := event.(msgs.ShowDialogMsg); ok {
			dialog = &candidate
		}
	}
	require.NotNil(t, dialog)
	assert.Equal(t, "Rename", dialog.Title, "decorated rename action opens the existing dialog")

	hints[2].Action()
	_, err := os.Stat(filepath.Join(dir, "x.dat"))
	assert.ErrorIs(t, err, os.ErrNotExist, "decorated delete action uses the existing delete path")
}

// TestRenameKeybind_PostsShowDialogMsg_OffLoop verifies that pressing the rename
// key (default "r") posts a ShowDialogMsg off-loop via the poster and returns nil
// from HandleKey (R5a A7: ShowDialogMsg production moved off-loop).
func TestRenameKeybind_PostsShowDialogMsg_OffLoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	// Populate the list so the cursor points at a file.
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.NotEmpty(t, m.GetItemUnderCursor(), "list must be non-empty before pressing rename")

	// Reset recorded events so we only see the rename-triggered post.
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()

	// Default rename key is "r".
	ev := keystest.PressRuneUV(t, 'r')
	m.HandleKey(ev)

	// The fakePoster runs Go inline, so the ShowDialogMsg is already in Posted.
	events := fp.events()
	require.Len(t, events, 1, "rename must post exactly one message")
	dlg, ok := events[0].(msgs.ShowDialogMsg)
	require.True(t, ok, "posted message must be a ShowDialogMsg")
	assert.Equal(t, "Rename", dlg.Title)
	assert.NotNil(t, dlg.Input, "ShowDialogMsg must carry a non-nil Input")

	// Verify dialog has OK and Cancel buttons.
	require.Len(t, dlg.Buttons, 2)
	assert.Equal(t, "OK", dlg.Buttons[0].Label)
	assert.Equal(t, "Cancel", dlg.Buttons[1].Label)

	// Drive the OK button with a new name — RenameFile posts a MoveOP.
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	require.NoError(t, dlg.Buttons[0].Cmd("newname"))
	assert.NotEmpty(t, fp.events(), "OK button with a new name must kick off RenameFile (posts MoveOP)")

	// Drive the OK button with the same name — RenameFile must NOT be called.
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	require.NoError(t, dlg.Buttons[0].Cmd("x")) // same as current ("x" from GetItemUnderCursor)
	assert.Empty(t, fp.events(), "OK button with unchanged name must not post anything")

	// Cancel button has no Cmd — only Label.
	assert.Nil(t, dlg.Buttons[1].Cmd, "Cancel button must have no Cmd")
}

// TestRenameKeybind_EmptyList_ReturnsNil verifies that pressing rename when the
// list is empty returns nil without posting anything.
func TestRenameKeybind_EmptyList_ReturnsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fp, _, _ := newTestModel(t, dir)
	// List is empty — no files in dir.

	ev := keystest.PressRuneUV(t, 'r')
	m.HandleKey(ev)
	assert.Empty(t, fp.events(), "rename on empty list must not post anything")
}
