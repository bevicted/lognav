package instancepicker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// msgKey is the log Data field key used across this package's test fixtures.
const msgKey = "msg"

// newTestModel returns a *Model wired only with what the OpenInstance state
// machine needs. Bypasses New() to avoid pulling in config singletons. The
// caller supplies the poster so it can choose inline (fakePoster) vs deferred
// (deferredPoster) goroutine execution.
func newTestModel(t *testing.T, poster msgs.Poster) *Model {
	t.Helper()
	return &Model{
		instances:    Instances{},
		pendingLoads: map[string]struct{}{},
		logger:       slog.Default(),
		ctx:          context.Background(),
		poster:       poster,
	}
}

func TestOpenInstance_NoBackingFile(t *testing.T) {
	t.Parallel()
	f := &fakePoster{}
	m := newTestModel(t, f)

	m.OpenInstance("nope")

	evs := f.events()
	require.Len(t, evs, 1)
	ready, ok := evs[0].(InstanceLoadReadyMsg)
	require.True(t, ok, "expected InstanceLoadReadyMsg, got %T", evs[0])
	assert.Equal(t, "nope", ready.CRN)
	assert.ErrorIs(t, ready.Err, ErrNoBackingFile)
}

func TestOpenInstance_FailedInstance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state status.Phase
	}{
		{"error", status.Error},
		{"disabled", status.Disabled},
		{"cancelled", status.Cancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakePoster{}
			m := newTestModel(t, f)
			m.setBackingPath("/dev/null") // backing path present so we fall to instance check
			inst := newTestInstance(t, "inst-"+tc.name)
			inst.state = tc.state
			m.instances = append(m.instances, inst)

			m.OpenInstance(inst.CRN)

			evs := f.events()
			require.Len(t, evs, 1)
			ready, ok := evs[0].(InstanceLoadReadyMsg)
			require.True(t, ok)
			require.ErrorIs(t, ready.Err, ErrInstanceFailed)
			assert.Empty(t, m.pendingLoads, "failed instances must not queue pending loads")
		})
	}
}

// TestOpenInstance_AlreadyFlushed_OpensFdOnLoop proves the rename-race fix is
// preserved after the poster.Go conversion: the backing container fd is opened
// SYNCHRONOUSLY on the loop goroutine (before any goroutine spawns), and only
// the read (LoadInstanceLogs) is dispatched off-loop. Using a deferredPoster,
// the result is produced only when the captured body is run — proving exactly
// one Go was scheduled and the on-loop open already succeeded.
func TestOpenInstance_AlreadyFlushed_OpensFdOnLoop(t *testing.T) {
	// backing file on disk; cannot parallelize.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lognav.wip")
	f, err := os.Create(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	c := snapshot.NewWriter(f)
	logs := []icl.Log{
		{Data: map[string]any{msgKey: "hi"}, Metadata: icl.Metadata{ID: "1"}},
	}
	require.NoError(t, snapshot.SaveInstanceFrame(c, testCRN("eu-de"), logs))
	require.NoError(t, c.Close())
	require.NoError(t, f.Close())

	dp := &deferredPoster{}
	m := newTestModel(t, dp)
	m.setBackingPath(path)
	inst := newTestInstance(t, "eu-de")
	inst.state = status.Success
	inst.flushedLogCount = 1
	m.instances = append(m.instances, inst)

	m.OpenInstance(inst.CRN)

	// Durable path: exactly one Go captured; nothing posted yet (the fd was
	// opened on-loop, but the read body has not run).
	require.Len(t, dp.spawned, 1, "durable path must spawn exactly one poster.Go")
	require.Empty(t, dp.posted, "durable path must not post before the body runs")

	// Run the captured body: the on-loop fd is read and the result posted.
	dp.spawned[0](context.Background())
	require.Len(t, dp.posted, 1)
	ready, ok := dp.posted[0].(InstanceLoadReadyMsg)
	require.True(t, ok)
	require.NoError(t, ready.Err)
	assert.Len(t, ready.Logs, 1)
}

// TestOpenInstance_TerminalStateWithFlushedLogs_StillLoads proves that an
// instance in a terminal state (Cancelled or Error) whose logs are already
// durable on disk is loaded from the backing file rather than rejected with
// ErrInstanceFailed. This is the snapshot-restore case: a cancelled/errored
// instance keeps its terminal state across restore while its logs live in the
// restored backing container, so opening it must surface those logs.
func TestOpenInstance_TerminalStateWithFlushedLogs_StillLoads(t *testing.T) {
	cases := []struct {
		name  string
		state status.Phase
	}{
		{"cancelled", status.Cancelled},
		{"error", status.Error},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// backing file on disk; cannot parallelize.
			dir := t.TempDir()
			path := filepath.Join(dir, "test.lognav.wip")
			f, err := os.Create(path) // #nosec G304 -- test-only path via t.TempDir
			require.NoError(t, err)
			c := snapshot.NewWriter(f)
			logs := []icl.Log{
				{Data: map[string]any{msgKey: "kept"}, Metadata: icl.Metadata{ID: "1"}},
			}
			require.NoError(t, snapshot.SaveInstanceFrame(c, testCRN("eu-de"), logs))
			require.NoError(t, c.Close())
			require.NoError(t, f.Close())

			dp := &deferredPoster{}
			m := newTestModel(t, dp)
			m.setBackingPath(path)
			inst := newTestInstance(t, "eu-de")
			inst.state = tc.state
			inst.flushedLogCount = 1
			m.instances = append(m.instances, inst)

			m.OpenInstance(inst.CRN)

			require.Len(t, dp.spawned, 1, "durable path must spawn exactly one poster.Go")
			require.Empty(t, dp.posted, "durable path must not post before the body runs")

			dp.spawned[0](context.Background())
			require.Len(t, dp.posted, 1)
			ready, ok := dp.posted[0].(InstanceLoadReadyMsg)
			require.True(t, ok)
			require.NoError(t, ready.Err, "terminal-state instance with durable logs must load, not fail")
			assert.Len(t, ready.Logs, 1)
		})
	}
}

// TestOpenInstance_LiveStore_SnapshotizesOnLoop proves the live-store path takes
// the snapshot synchronously on the loop goroutine and dispatches only the post
// off-loop.
func TestOpenInstance_LiveStore_SnapshotizesOnLoop(t *testing.T) {
	t.Parallel()
	dp := &deferredPoster{}
	m := newTestModel(t, dp)
	m.setBackingPath("/dev/null") // backing path present; durable path skipped (flushedLogCount==0)

	inst := newTestInstance(t, "live")
	inst.state = status.Success
	inst.flushedLogCount = 0
	inst.Store.SetLogs([]icl.Log{{Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"}}})
	require.Equal(t, 1, inst.Store.GetLogCount())
	m.instances = append(m.instances, inst)

	m.OpenInstance(inst.CRN)

	require.Len(t, dp.spawned, 1, "live path must spawn exactly one poster.Go")
	require.Empty(t, dp.posted, "live path must not post before the body runs")

	dp.spawned[0](context.Background())
	require.Len(t, dp.posted, 1)
	ready, ok := dp.posted[0].(InstanceLoadReadyMsg)
	require.True(t, ok)
	require.NoError(t, ready.Err)
	assert.Len(t, ready.Logs, 1)
}

func TestOpenInstance_PendingQueuesOnLoop(t *testing.T) {
	t.Parallel()

	// Seed a wip with NO frame yet; instance is in the flush window
	// (flushedLogCount == 0, state == Success, store empty).
	f := &fakePoster{}
	m := newTestModel(t, f)
	m.setBackingPath("/dev/null")
	inst := newTestInstance(t, "us-south")
	inst.state = status.Success
	inst.flushedLogCount = 0
	m.instances = append(m.instances, inst)

	m.OpenInstance(inst.CRN)

	// pendingLoads mutated on-loop, before any post is observed.
	_, queued := m.pendingLoads[inst.CRN]
	assert.True(t, queued, "pending load must be queued on-loop")

	evs := f.events()
	require.Len(t, evs, 1)
	_, isPending := evs[0].(InstanceLoadPendingMsg)
	require.True(t, isPending, "expected pending msg, got %T", evs[0])
}

func TestDrainPendingLoadsOnFetchAbort(t *testing.T) {
	t.Parallel()
	f := &fakePoster{}
	m := newTestModel(t, f)
	m.pendingLoads["a"] = struct{}{}
	m.pendingLoads["b"] = struct{}{}

	// drainPendingLoads now emits InstanceLoadReadyMsg{Err} off-loop via the
	// poster (the fakePoster runs Go inline and records PostCritical).
	m.drainPendingLoads(ErrFetchAborted)
	assert.Empty(t, m.pendingLoads)

	seen := map[string]bool{}
	for _, ev := range f.events() {
		ready, ok := ev.(InstanceLoadReadyMsg)
		require.True(t, ok)
		seen[ready.CRN] = true
		require.ErrorIs(t, ready.Err, ErrFetchAborted)
	}
	assert.True(t, seen["a"] && seen["b"])
}

func TestBackingPathClearedOnClose(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, &fakePoster{})
	m.setBackingPath("/tmp/x")
	require.Equal(t, "/tmp/x", m.GetBackingPath())
	_ = m.closeBackingContainer()
	assert.Empty(t, m.GetBackingPath())
}
