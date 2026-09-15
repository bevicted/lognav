package msgs

import (
	"github.com/bevicted/lognav/internal/snapshot"
)

type SaveSnapshotMsg struct{}

// ManualSaveSnapshotMsg is sent by components when the user presses the save keybind.
// The root UI intercepts it to show a save dialog.
type ManualSaveSnapshotMsg struct{}

// ManualSaveDoneMsg is sent after a manual snapshot save completes successfully.
// It tells the snapshot tab to refresh its file list without triggering another
// write. Name is the saved filename so the tab can drop any stale cached preview
// for it (the save os.Renames over the file outside the filehandler's IO ops).
type ManualSaveDoneMsg struct{ Name string }

type SnapshotRestoreMsg struct {
	Snapshot    snapshot.Snapshot
	BackingPath string // path to .lognav file for lazy loading
	Err         error
}

// SnapshotRenamedMsg tells the TUI another session (or this one) renamed a
// managed snapshot. From/To are sanitized basenames incl. the .lognav ext.
type SnapshotRenamedMsg struct {
	From string
	To   string
}

// SnapshotsDirtyMsg tells the TUI the managed snapshot set or some .inuse
// status changed and the list/coloring should be recomputed.
type SnapshotsDirtyMsg struct{}
