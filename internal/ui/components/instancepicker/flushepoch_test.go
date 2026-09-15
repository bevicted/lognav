package instancepicker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/list"
)

// compressOne builds a one-log compressed instance frame payload, the shape
// InstanceFlushedMsg carries.
func compressOne(t *testing.T, id string) []byte {
	t.Helper()
	compressed, _, err := snapshot.CompressInstanceLogs([]icl.Log{
		{Data: map[string]any{msgKey: id}, Metadata: icl.Metadata{ID: id}},
	})
	require.NoError(t, err)
	return compressed
}

// TestOnInstanceFlushed_StaleEpochIsDropped is a regression test for the
// cross-fetch flush bleed: instance X's compression from fetch A completes after
// fetch B has already opened a fresh backing container. Without the fetchEpoch
// stamp the stale frame was written into B's container (so B's snapshot carried
// A's logs under X's name, and B's own flush for X then failed with "frame
// already exists"), and it consumed one of B's pendingFlushes credits.
func TestOnInstanceFlushed_StaleEpochIsDropped(t *testing.T) {
	t.Parallel()
	f := &fakePoster{}
	m := newTestModel(t, f)
	m.bundle = depstest.NewTest(t)
	attachBackingFile(t, m)

	const instName = "au-syd"
	m.fetchEpoch = 7
	m.pendingFlushes = 1 // the CURRENT fetch has one flush of its own in flight
	m.pendingLoads[instName] = struct{}{}

	m.OnInstanceFlushed(InstanceFlushedMsg{crn: instName, compressed: compressOne(t, "stale"), epoch: 6})

	assert.Equal(t, 1, m.pendingFlushes, "a stale flush must not consume the current fetch's flush credit")
	assert.Contains(t, m.pendingLoads, instName, "a stale flush must not resolve the current fetch's pending load")
	assert.Empty(t, f.events(), "a stale flush must emit nothing")

	// The instance's frame slot in the current backing must still be free: this
	// write is what the current fetch's own flush would do, and it fails with
	// "frame already exists" if the stale frame was written.
	require.NoError(t, snapshot.WriteCompressedInstanceFrame(m.backingContainer, testCRN(instName), compressOne(t, "fresh")),
		"the current fetch must still be able to write the instance's frame")
}

// TestStartFetchWithSources_InvalidatesPreviousFetchFlushes covers the fetch-start
// half of the same bug: a new fetch resets the flush accounting, invalidates the
// abandoned fetch's in-flight flushes, and unlinks the .wip it is abandoning
// (closeBackingContainer alone left it orphaned until a future CleanStaleWip).
//
// Not parallel: it sets XDG_DATA_HOME (process-wide) and the .wip filename embeds
// the PID, both OS-level shared state.
func TestStartFetchWithSources_InvalidatesPreviousFetchFlushes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	f := &fakePoster{}
	m := newTestModel(t, f)
	bundle := depstest.NewTest(t)
	m.bundle = bundle
	m.list = list.New(bundle)

	const instName = "au-syd"
	abandonedWip := attachBackingFile(t, m)
	m.pendingFlushes = 2 // fetch A's compressions, still running when B starts
	staleEpoch := m.fetchEpoch

	require.NoError(t, m.startFetchWithSources())

	assert.NotEqual(t, staleEpoch, m.fetchEpoch, "a new fetch must get its own epoch")
	assert.Equal(t, 0, m.pendingFlushes, "the abandoned fetch's flush credits must not carry over")
	assert.NoFileExists(t, abandonedWip, "the abandoned, unfinalized .wip must be unlinked")
	require.NotNil(t, m.backingContainer, "the new fetch must have its own backing container")

	// Fetch A's flush lands now, after B opened its container.
	m.OnInstanceFlushed(InstanceFlushedMsg{crn: instName, compressed: compressOne(t, "stale"), epoch: staleEpoch})

	assert.Equal(t, 0, m.pendingFlushes, "a stale flush must not corrupt the new fetch's accounting")
	require.NoError(t, snapshot.WriteCompressedInstanceFrame(m.backingContainer, testCRN(instName), compressOne(t, "fresh")),
		"fetch B's own flush must still be able to write the instance's frame")

	require.NoError(t, m.Close())
}

// TestDiscardBackingContainer_KeepsFinalizedBacking pins the deletion guard: only
// a .wip is ever unlinked, so a finalized snapshot the model still points at
// survives a discard.
func TestDiscardBackingContainer_KeepsFinalizedBacking(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, &fakePoster{})
	m.bundle = depstest.NewTest(t)

	finalPath := filepath.Join(t.TempDir(), "auto-20260814-120000.lognav")
	file, err := os.Create(finalPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	m.backingFile = file
	m.backingContainer = snapshot.NewWriter(file)
	m.setBackingPath(finalPath)

	m.discardBackingContainer()

	assert.FileExists(t, finalPath, "a finalized backing must never be unlinked")
	assert.Nil(t, m.backingContainer)
}
