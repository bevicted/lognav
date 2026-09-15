package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
)

// recordingBroadcaster counts Broadcast invocations synchronously.
type recordingBroadcaster struct {
	calls atomic.Int64
}

func (r *recordingBroadcaster) Broadcast(_ context.Context, _ string, _ any) error {
	r.calls.Add(1)
	return nil
}

// setSnapshotNotifier replaces the package-level snapshotNotifier with one
// that returns b, and returns a restore func (suitable for t.Cleanup).
func setSnapshotNotifier(b sessionbus.Broadcaster) func() {
	prev := snapshotNotifier
	snapshotNotifier = func() sessionbus.Broadcaster { return b }
	return func() { snapshotNotifier = prev }
}

//nolint:paralleltest // touches snapshot.Dir() global + snapshotNotifier global
func TestRmBroadcastsAfterDelete(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rb := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(rb))

	seedSnapshot(t, "m_target.lognav", snapshot.Snapshot{}, nil)

	cmd := newSnapshotRm()
	cmd.SetArgs([]string{"m_target"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	assert.Equal(t, int64(1), rb.calls.Load(), "broadcast must fire exactly once after deletion")
}

//nolint:paralleltest // touches snapshot.Dir() global + snapshotNotifier global + SetPidAliveForTest
func TestRmPartialSkipStillBroadcasts(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rb := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(rb))

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	seedSnapshot(t, "m_free.lognav", snapshot.Snapshot{}, nil)
	seedSnapshot(t, "m_held.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9001.inuse"), []byte("m_held.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9001 })
	t.Cleanup(restore)

	cmd := newSnapshotRm()
	cmd.SetArgs([]string{"m_free", "m_held"})
	cmd.SetOut(io.Discard)
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	execErr := cmd.Execute()

	// Command exits EX_UNAVAILABLE because one was skipped.
	assert.Equal(t, ExitUnavailable, ExitCode(execErr))
	// But the one successful deletion must still have triggered a broadcast.
	assert.Equal(t, int64(1), rb.calls.Load(), "broadcast must fire once even when some targets are skipped")
}

//nolint:paralleltest // touches snapshot.Dir() global + snapshotNotifier global + SetPidAliveForTest
func TestRmNoMutationNoBroadcast(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rb := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(rb))

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// Plant a held snapshot — rm will skip it, nothing gets deleted.
	seedSnapshot(t, "m_held2.lognav", snapshot.Snapshot{}, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "9002.inuse"), []byte("m_held2.lognav\n"), 0o600))
	restore := snapshot.SetPidAliveForTest(func(pid int) bool { return pid == 9002 })
	t.Cleanup(restore)

	cmd := newSnapshotRm()
	cmd.SetArgs([]string{"m_held2"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	_ = cmd.Execute() // exits EX_UNAVAILABLE; we don't care here

	assert.Equal(t, int64(0), rb.calls.Load(), "no broadcast when nothing was deleted")
}

//nolint:paralleltest // touches snapshot.Dir() global + snapshotNotifier global
func TestPruneDryRunDoesNotBroadcast(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rb := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(rb))

	// Seed a stale .wip (dead PID) that WOULD be pruned on a real run.
	writeSnapshotFileForTest(t, ".random_999999.lognav.wip", timeNowForTest())
	// Also seed an auto snapshot selectable via --auto.
	seedSnapshot(t, "auto-20260814-120000.lognav", snapshot.Snapshot{}, nil)

	cmd := newSnapshotPrune()
	cmd.SetArgs([]string{"--auto", "--dry-run"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	assert.Equal(t, int64(0), rb.calls.Load(), "dry-run must NOT broadcast even though files would be deleted")
}

//nolint:paralleltest // touches snapshot.Dir() global + snapshotNotifier global
func TestPruneBroadcastsWhenDeleted(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rb := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(rb))

	// Seed a stale .wip with a dead PID — bare prune deletes it.
	writeSnapshotFileForTest(t, ".random_999999.lognav.wip", timeNowForTest())

	cmd := newSnapshotPrune()
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	assert.Equal(t, int64(1), rb.calls.Load(), "prune that deletes ≥1 file must broadcast exactly once")
}

//nolint:paralleltest // touches snapshot.Dir() global + snapshotNotifier global
func TestAdoptBroadcastsOnSuccess(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rb := &recordingBroadcaster{}
	t.Cleanup(setSnapshotNotifier(rb))

	// Create a real snapshot file outside the managed dir.
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "m_external.lognav")
	writeSnapshotAt(t, src)

	cmd := newSnapshotAdopt()
	cmd.SetArgs([]string{"-y", src})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())

	assert.Equal(t, int64(1), rb.calls.Load(), "adopt that transfers a file must broadcast exactly once")
}
