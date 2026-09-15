package filehandler

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// withOneFile builds a test model, writes one file, populates the list, draws so
// the inner list rect is set, and resets the poster so only menu-triggered posts
// are observed.
func withOneFile(t *testing.T) (*Model, *fakePoster) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))
	m, fp, _, _ := newTestModel(t, dir)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.NotEmpty(t, m.GetItemUnderCursor())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	_ = m.Draw(uv.NewScreenBuffer(40, 10))
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	return m, fp
}

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

func TestOpenContextMenu_PostsRenameAndDelete(t *testing.T) {
	t.Parallel()
	m, fp := withOneFile(t)
	m.OpenContextMenu()
	msg := firstShowContextMenu(t, fp.events())
	assert.True(t, msg.Anchored)
	assert.Equal(t, 0, msg.AnchorX)
	assert.Equal(t, 3, msg.AnchorY)
	require.Len(t, msg.Items, 2)
	assert.Equal(t, "rename", msg.Items[0].Label)
	assert.Equal(t, "delete", msg.Items[1].Label)
}

func TestOpenContextMenu_EmptyList_NoPost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fp, _, _ := newTestModel(t, dir)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	_ = m.Draw(uv.NewScreenBuffer(40, 10))
	m.OpenContextMenu()
	assert.Empty(t, fp.events())
}

func TestContextMenu_DeleteItem_PostsConfirmDialog(t *testing.T) {
	t.Parallel()
	m, fp := withOneFile(t)
	m.OpenContextMenu()
	items := firstShowContextMenu(t, fp.events()).Items
	del := itemByLabel(t, items, "delete")
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	del.Action()
	events := fp.events()
	require.Len(t, events, 1)
	dlg, ok := events[0].(msgs.ShowDialogMsg)
	require.True(t, ok, "Delete item must post a ShowDialogMsg")
	assert.Equal(t, "Delete", dlg.Title)
	assert.Nil(t, dlg.Input, "delete confirm has no input field")
	require.Len(t, dlg.Buttons, 2)
	assert.Equal(t, "OK", dlg.Buttons[0].Label)
	assert.Equal(t, "Cancel", dlg.Buttons[1].Label)
	assert.Nil(t, dlg.Buttons[1].Cmd)
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	require.NoError(t, dlg.Buttons[0].Cmd(""))
	assert.NotEmpty(t, fp.events(), "OK must kick off the delete")
}

func TestContextMenu_RenameItem_PostsRenameDialog(t *testing.T) {
	t.Parallel()
	m, fp := withOneFile(t)
	m.OpenContextMenu()
	items := firstShowContextMenu(t, fp.events()).Items
	ren := itemByLabel(t, items, "rename")
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	ren.Action()
	events := fp.events()
	require.Len(t, events, 1)
	dlg, ok := events[0].(msgs.ShowDialogMsg)
	require.True(t, ok)
	assert.Equal(t, "Rename", dlg.Title)
	assert.NotNil(t, dlg.Input)
}

// withOneFileAndValidator builds a test model with a Validator set that rejects
// the name "reserved.dat". Used to test rename-validator integration.
func withOneFileAndValidator(t *testing.T, validator func(string) error) (*Model, *fakePoster) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))
	bundle := depstest.NewTest(t)
	m := New(bundle, FileOpts{Dir: dir, Ext: ".dat", Validator: validator}, func(_ io.Reader, _ string) uv.Event { return nil }, func(_ io.Writer) uv.Event { return nil })
	fp := &fakePoster{}
	m.SetPoster(fp)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.NotEmpty(t, m.GetItemUnderCursor())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	_ = m.Draw(uv.NewScreenBuffer(40, 10))
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	return m, fp
}

// TestRenameDialog_Validator_RejectsReservedName verifies that when FileOpts.Validator
// is set and returns an error, the rename dialog's OK Cmd propagates that error (the
// rename is aborted) and does not spawn a RenameFile worker.
func TestRenameDialog_Validator_RejectsReservedName(t *testing.T) {
	t.Parallel()
	rejectReserved := func(name string) error {
		if name == "reserved.dat" {
			return errors.New(`"reserved" is a reserved name`)
		}
		return nil
	}
	m, fp := withOneFileAndValidator(t, rejectReserved)
	m.OpenContextMenu()
	items := firstShowContextMenu(t, fp.events()).Items
	ren := itemByLabel(t, items, "rename")
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	ren.Action() // opens rename dialog
	events := fp.events()
	require.Len(t, events, 1)
	dlg, ok := events[0].(msgs.ShowDialogMsg)
	require.True(t, ok, "rename item must post a ShowDialogMsg")

	// Simulate user typing the reserved name and pressing OK.
	err := dlg.Buttons[0].Cmd("reserved")
	require.Error(t, err, "OK on a reserved name must return a non-nil error")

	// No MoveOP (no RenameFile goroutine) should have been spawned.
	for _, ev := range fp.events() {
		if io, isIO := ev.(IOMsg); isIO {
			require.NotEqual(t, MoveOP, io.op, "a rejected rename must not spawn a MoveOP worker")
		}
	}
}

// TestRenameDialog_Validator_AllowsNormalName verifies that a non-reserved name passes
// the validator and proceeds to RenameFile.
func TestRenameDialog_Validator_AllowsNormalName(t *testing.T) {
	t.Parallel()
	rejectReserved := func(name string) error {
		if name == "reserved.dat" {
			return errors.New(`"reserved" is a reserved name`)
		}
		return nil
	}
	m, fp := withOneFileAndValidator(t, rejectReserved)
	m.OpenContextMenu()
	items := firstShowContextMenu(t, fp.events()).Items
	ren := itemByLabel(t, items, "rename")
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	ren.Action()
	events := fp.events()
	require.Len(t, events, 1)
	dlg, ok := events[0].(msgs.ShowDialogMsg)
	require.True(t, ok)

	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()

	// Simulate user typing a valid (non-reserved) name and pressing OK.
	err := dlg.Buttons[0].Cmd("goodname")
	require.NoError(t, err, "OK on an allowed name must not return an error")

	// A MoveOP worker must have been spawned (file rename in progress).
	var hasMoveOP bool
	for _, ev := range fp.events() {
		if io, isIO := ev.(IOMsg); isIO && io.op == MoveOP {
			hasMoveOP = true
		}
	}
	require.True(t, hasMoveOP, "allowed rename must spawn a MoveOP worker")
}

// TestRenameDialog_NilValidator_AlwaysAllows verifies that when FileOpts.Validator
// is nil (e.g. the .dataprime query browser) no reserved-name check runs and
// every name is accepted.
func TestRenameDialog_NilValidator_AlwaysAllows(t *testing.T) {
	t.Parallel()
	// newTestModel builds with no Validator (nil by default) using the .dat ext.
	m, fp := withOneFile(t)
	m.OpenContextMenu()
	items := firstShowContextMenu(t, fp.events()).Items
	ren := itemByLabel(t, items, "rename")
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	ren.Action()
	events := fp.events()
	require.Len(t, events, 1)
	dlg, ok := events[0].(msgs.ShowDialogMsg)
	require.True(t, ok)

	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()

	// "reserved" typed in the .dat browser — nil validator means it passes.
	err := dlg.Buttons[0].Cmd("reserved")
	require.NoError(t, err, "nil Validator must never block a rename")
}

// TestOnMouseRight_LeftHalf_MovesCursorAndAnchorsAtPointer verifies a left-half
// right-click moves the cursor to the clicked item row and anchors the menu one
// row below the pointer.
func TestOnMouseRight_LeftHalf_MovesCursorAndAnchorsAtPointer(t *testing.T) {
	t.Parallel()
	m, fp := withOneFile(t)
	consumed := m.OnMouseRight(5, 2) // listW=20; item row 0 at listRect.Y=2; left half
	assert.True(t, consumed)
	msg := firstShowContextMenu(t, fp.events())
	assert.True(t, msg.Anchored)
	assert.Equal(t, 5, msg.AnchorX)
	assert.Equal(t, 3, msg.AnchorY) // y+1
	require.Len(t, msg.Items, 2)
}

// TestOnMouseRight_PreviewHalf_NotConsumed verifies a right-click in the preview
// (right) half is not consumed and posts nothing.
func TestOnMouseRight_PreviewHalf_NotConsumed(t *testing.T) {
	t.Parallel()
	m, fp := withOneFile(t)
	consumed := m.OnMouseRight(25, 2) // x >= drawRect.X + listW (20)
	assert.False(t, consumed)
	assert.Empty(t, fp.events())
}

// TestOnMouseRight_EmptyList_NotConsumed verifies an empty list is not consumed.
func TestOnMouseRight_EmptyList_NotConsumed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fp, _, _ := newTestModel(t, dir)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	_ = m.Draw(uv.NewScreenBuffer(40, 10))
	consumed := m.OnMouseRight(5, 2)
	assert.False(t, consumed)
	assert.Empty(t, fp.events())
}

// TestOnMouseRight_FuzzyHeader_OpensWithoutMovingCursor verifies a right-click on
// the 2-row fuzzy header opens the menu without moving the cursor and without
// focusing the fuzzy input.
func TestOnMouseRight_FuzzyHeader_OpensWithoutMovingCursor(t *testing.T) {
	t.Parallel()
	m, fp := withOneFile(t)
	before := m.list.DisplayCursor()
	consumed := m.OnMouseRight(5, 0) // header row (y < listRect.Y=2)
	assert.True(t, consumed)
	assert.Equal(t, before, m.list.DisplayCursor(), "header right-click must not move the cursor")
	assert.False(t, m.list.IsFocused(), "header right-click must not focus the fuzzy input")
	_ = firstShowContextMenu(t, fp.events())
}

// withOneFileAndOnRenamed builds a test Model with a single file and an
// OnRenamed hook.
func withOneFileAndOnRenamed(t *testing.T) (*Model, *fakePoster, *renameHookCapture) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("a"), 0o600))
	bundle := depstest.NewTest(t)

	hookCap := &renameHookCapture{}
	opts := FileOpts{
		Dir: dir,
		Ext: ".dat",
		OnRenamed: func(_ context.Context, oldBase, newBase string) {
			hookCap.mu.Lock()
			defer hookCap.mu.Unlock()
			hookCap.called = true
			hookCap.oldBase = oldBase
			hookCap.newBase = newBase
		},
	}
	m := New(bundle, opts, func(_ io.Reader, _ string) uv.Event { return nil }, func(_ io.Writer) uv.Event { return nil })
	fp := &fakePoster{}
	m.SetPoster(fp)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.NotEmpty(t, m.GetItemUnderCursor())
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	_ = m.Draw(uv.NewScreenBuffer(40, 10))
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	return m, fp, hookCap
}

// renameHookCapture records whether OnRenamed was called and with which args.
type renameHookCapture struct {
	mu      sync.Mutex
	called  bool
	oldBase string
	newBase string
}

// TestRenameFile_ProceedsAndFiresOnRenamed verifies a successful rename posts
// MoveOP and invokes OnRenamed with the correct basenames.
func TestRenameFile_ProceedsAndFiresOnRenamed(t *testing.T) {
	t.Parallel()
	m, fp, hookCap := withOneFileAndOnRenamed(t)

	m.RenameFile("a", "c") // fakePoster runs the goroutine inline

	ioMsg, ok := firstIOMsg(fp.events(), MoveOP)
	require.True(t, ok, "RenameFile must spawn a MoveOP IOMsg")
	require.NoError(t, ioMsg.err, "rename to a non-existing destination must succeed")

	hookCap.mu.Lock()
	defer hookCap.mu.Unlock()
	assert.True(t, hookCap.called, "OnRenamed must be invoked after a successful rename")
	assert.Equal(t, "a.dat", hookCap.oldBase, "OnRenamed must receive the old full basename")
	assert.Equal(t, "c.dat", hookCap.newBase, "OnRenamed must receive the new full basename")
}

// TestRenameFile_CleanupFailurePublishesRefreshesAndFiresOnRenamed verifies that
// the linked destination remains visible and receives the normal rename lifecycle
// when source cleanup fails. The original source remains visible too.
//
//nolint:paralleltest // renameNoClobber is a package-level filesystem seam.
func TestRenameFile_CleanupFailurePublishesRefreshesAndFiresOnRenamed(t *testing.T) {
	origRename := renameNoClobber
	renameNoClobber = func(src, dst string) error {
		require.NoError(t, os.Link(src, dst))
		return &snapshot.RenameCleanupError{Err: errors.New("unlink failed")}
	}
	t.Cleanup(func() { renameNoClobber = origRename })

	m, fp, hookCap := withOneFileAndOnRenamed(t)
	m.RenameFile("a", "c") // fakePoster runs the goroutine inline

	moveMsg, ok := firstIOMsg(fp.events(), MoveOP)
	require.True(t, ok, "rename must post a MoveOP IOMsg")
	require.NoError(t, moveMsg.err, "published destination must complete the rename lifecycle")
	var cleanupErr *snapshot.RenameCleanupError
	require.ErrorAs(t, moveMsg.wrn, &cleanupErr)

	hookCap.mu.Lock()
	assert.True(t, hookCap.called, "published destination must invoke OnRenamed")
	assert.Equal(t, "a.dat", hookCap.oldBase)
	assert.Equal(t, "c.dat", hookCap.newBase)
	hookCap.mu.Unlock()

	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()
	m.OnFileIO(moveMsg)
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok, "published destination must refresh the file list")
	m.OnFileIO(listMsg)
	assert.Equal(t, 2, m.list.Len())
	assert.True(t, m.list.SelectByName("a"), "uncleaned source must remain visible")
	assert.True(t, m.list.SelectByName("c"), "published destination must become visible")
	for _, ev := range fp.events() {
		_, isDialog := ev.(msgs.ShowDialogMsg)
		assert.False(t, isDialog, "published cleanup failure must not be reported as a failed rename")
	}
}

// TestRenameFile_DestinationCollision_RefusesAndSkipsOnRenamed verifies that
// renaming onto an existing file is refused atomically: the MoveOP IOMsg carries
// a non-nil error and OnRenamed is NOT invoked.
func TestRenameFile_DestinationCollision_RefusesAndSkipsOnRenamed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.dat"), []byte("b"), 0o600))
	bundle := depstest.NewTest(t)

	var hookCalled bool
	opts := FileOpts{
		Dir: dir,
		Ext: ".dat",
		OnRenamed: func(_ context.Context, _, _ string) {
			hookCalled = true
		},
	}
	m := New(bundle, opts, func(_ io.Reader, _ string) uv.Event { return nil }, func(_ io.Writer) uv.Event { return nil })
	fp := &fakePoster{}
	m.SetPoster(fp)

	m.RenameFile("a", "b") // fakePoster runs the goroutine inline

	ioMsg, ok := firstIOMsg(fp.events(), MoveOP)
	require.True(t, ok, "RenameFile must post a MoveOP IOMsg even on collision")
	require.Error(t, ioMsg.err, "rename onto existing destination must fail")
	assert.False(t, hookCalled, "OnRenamed must NOT be invoked when the rename fails")

	// Both files must still exist on disk.
	_, errA := os.Stat(filepath.Join(dir, "a.dat"))
	require.NoError(t, errA, "source a.dat must still exist after a failed rename")
	_, errB := os.Stat(filepath.Join(dir, "b.dat"))
	require.NoError(t, errB, "destination b.dat must still exist and be unmodified")
}

// TestOnFileIO_MoveOP_Error_PostsCannotRenameDialog verifies that feeding a
// MoveOP IOMsg with a non-nil error into OnFileIO causes a ShowDialogMsg with
// Title "Cannot rename" to be posted (off-loop via poster.Go, which the
// fakePoster runs inline). This covers the rename-collision user-visible path.
func TestOnFileIO_MoveOP_Error_PostsCannotRenameDialog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.dat"), []byte("b"), 0o600))
	bundle := depstest.NewTest(t)
	m := New(bundle, FileOpts{Dir: dir, Ext: ".dat"}, func(_ io.Reader, _ string) uv.Event { return nil }, func(_ io.Writer) uv.Event { return nil })
	fp := &fakePoster{}
	m.SetPoster(fp)

	// Trigger a real rename collision so we get a MoveOP IOMsg with a non-nil error.
	m.RenameFile("a", "b") // fakePoster runs the goroutine inline

	ioMsg, ok := firstIOMsg(fp.events(), MoveOP)
	require.True(t, ok, "RenameFile must post a MoveOP IOMsg on collision")
	require.Error(t, ioMsg.err, "collision IOMsg must carry a non-nil error")

	// Reset the poster so only the dialog post is observed.
	fp.mu.Lock()
	fp.posted = nil
	fp.mu.Unlock()

	// Feed the error IOMsg into OnFileIO — this should post a "Cannot rename" dialog.
	m.OnFileIO(ioMsg)

	events := fp.events()
	var found bool
	for _, ev := range events {
		if dlg, ok := ev.(msgs.ShowDialogMsg); ok && dlg.Title == "Cannot rename" {
			found = true
			require.Len(t, dlg.Buttons, 1, "Cannot rename dialog must have exactly one button")
			assert.Equal(t, "OK", dlg.Buttons[0].Label)
			break
		}
	}
	assert.True(t, found, "MoveOP error in OnFileIO must post a 'Cannot rename' ShowDialogMsg")
}
