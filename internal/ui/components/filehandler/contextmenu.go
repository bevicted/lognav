package filehandler

import (
	"fmt"
	"strings"

	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// RenameUnderCursor opens an input dialog pre-filled with the selected file's
// name and, on OK, renames it via RenameFile. Shared by the Rename keybind and
// the context-menu Rename item.
// Runs on the loop goroutine; the dialog is posted via msgs.PostAsync.
func (m *Model) RenameUnderCursor() {
	if m.list.Len() < 1 {
		return
	}
	current := m.list.GetItemUnderCursor()
	in := uiinput.New()
	in.SetValue(current)
	dlg := msgs.ShowDialogMsg{
		Title:   "Rename",
		Message: "Enter new name:",
		Input:   in,
		Buttons: []msgs.DialogButton{
			{
				Label: "OK",
				Cmd: func(filename string) error {
					filename = strings.TrimSuffix(filename, m.opts.Ext)
					if filename == "" || filename == current {
						return nil
					}
					if m.opts.Validator != nil {
						if err := m.opts.Validator(filename + m.opts.Ext); err != nil {
							return err // shown to the user; rename aborted
						}
					}
					m.RenameFile(current, filename) // void: spawns a tracked poster goroutine
					return nil
				},
			},
			{Label: "Cancel"},
		},
	}
	msgs.PostAsync(m.poster, dlg)
}

// postContextMenu builds the Rename/Delete items for the file under the cursor
// and posts an anchored ShowContextMenuMsg at (ax, ay). Shared by the keyboard
// (OpenContextMenu) and right-click (OnMouseRight) entry points. No-op if no
// file is selected or the poster is not yet wired (SetPoster runs at startup,
// not in the constructor — mirrors logviewer's nil-poster guard).
func (m *Model) postContextMenu(ax, ay int) {
	if m.list.Len() < 1 || m.poster == nil {
		return
	}
	name := m.list.GetItemUnderCursor() // ext-stripped, for the confirm message
	items := []msgs.ContextMenuItem{
		{Label: "rename", Action: m.RenameUnderCursor},
		{Label: "delete", Action: func() { m.confirmDelete(name) }},
	}
	msgs.PostAsync(m.poster, msgs.ShowContextMenuMsg{Items: items, Anchored: true, AnchorX: ax, AnchorY: ay})
}

// confirmDelete posts a confirmation dialog; on OK it runs the existing
// (immediate) DeleteFileUnderCursor path, which still applies the OnDelete
// in-use guard. The modal overlay prevents the cursor from moving between
// confirm and OK. The menu Action runs on the loop, so the dialog is posted
// via msgs.PostAsync.
func (m *Model) confirmDelete(name string) {
	msgs.PostAsync(m.poster, msgs.ShowDialogMsg{
		Title:   "Delete",
		Message: fmt.Sprintf("Delete %q?", name),
		Buttons: []msgs.DialogButton{
			{Label: "OK", Cmd: func(string) error { m.DeleteFileUnderCursor(); return nil }},
			{Label: "Cancel"},
		},
	})
}

// OpenContextMenu implements component.ContextMenuOpener: open the Rename/Delete
// menu anchored one row below the selected row at the list's left column. No-op
// on an empty list. Like OnMouseClick it relies on the inner list rect set in
// filehandler.Draw, valid because the active tab is always drawn before the
// keybind fires.
func (m *Model) OpenContextMenu() {
	if m.list.Len() < 1 {
		return
	}
	y, ok := m.list.SelectedRowY()
	if !ok {
		return
	}
	m.postContextMenu(m.drawRect.X, y+1)
}

// OnMouseRight implements component.SecondaryMouseTarget: a right-click in the
// left (list) half moves the cursor to the clicked item row (if any) and opens
// the Rename/Delete menu anchored just below the pointer. A right-click on the
// 2-row fuzzy header opens the menu without moving the cursor and without
// focusing the fuzzy input — it uses RowAtY+SetCursor directly rather than
// list.OnMouseClick (which would focus the input on a header click). Right-clicks
// in the preview half, or on an empty list, are not consumed.
func (m *Model) OnMouseRight(x, y int) bool {
	listW := m.drawRect.W / 2
	if x >= m.drawRect.X+listW {
		return false
	}
	if m.list.Len() < 1 {
		return false
	}
	if idx, ok := m.list.RowAtY(y); ok {
		m.list.SetCursor(idx)
	}
	m.postContextMenu(x, y+1)
	return true
}
