package sessionbus

import (
	"path/filepath"
	"strings"
)

// Notify method names handled by each session's HTTP dispatch.
const (
	MethodSnapshotRenamed = "snapshot_renamed"
	MethodSnapshotsDirty  = "snapshots_dirty"
)

// SnapshotRenamedParams is the wire body for MethodSnapshotRenamed. From/To are
// sanitized basenames (see SanitizeBasename) — never raw paths.
type SnapshotRenamedParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SnapshotsDirtyParams is the (empty) wire body for MethodSnapshotsDirty.
type SnapshotsDirtyParams struct{}

// SanitizeBasename reduces raw to a single safe basename and reports whether it
// is usable. It rejects path separators, NUL, and the empty / "." / ".."
// degenerate names so a notify can never re-point a session's backing or
// .inuse claim outside the snapshot dir. The returned value is the validated
// basename callers store in the event struct (no re-validation downstream).
func SanitizeBasename(raw string) (string, bool) {
	if strings.ContainsAny(raw, `/\`+"\x00") {
		return "", false
	}
	b := filepath.Base(raw)
	if b == "" || b == "." || b == ".." {
		return "", false
	}
	return b, true
}
