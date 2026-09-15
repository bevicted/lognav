package cmd

import (
	"context"

	"github.com/bevicted/lognav/internal/sessionbus"
)

// snapshotNotifier is the CLI seam for telling live sessions the managed
// snapshot set changed. Overridable in tests. Synchronous by contract — callers
// MUST invoke it before returning so the send flushes before process exit.
var snapshotNotifier = func() sessionbus.Broadcaster { return sessionbus.NewClient() }

// notifySnapshotsDirty broadcasts snapshots_dirty synchronously. Errors are
// non-fatal (the FS mutation already succeeded) and surfaced by the caller to
// stderr if errOut != nil.
func notifySnapshotsDirty(errOut func(format string, a ...any)) {
	notifySnapshotsDirtyContext(context.Background(), errOut)
}

// notifySnapshotsDirtyContext lets cancellation stop a headless query while a
// synchronous peer notification is in progress.
func notifySnapshotsDirtyContext(ctx context.Context, errOut func(format string, a ...any)) {
	if err := snapshotNotifier().Broadcast(ctx, sessionbus.MethodSnapshotsDirty, sessionbus.SnapshotsDirtyParams{}); err != nil && errOut != nil {
		errOut("snapshot change notification failed: %v\n", err)
	}
}
