package instancepicker

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

func assertSnapshotMembership(t *testing.T, path string, want []string) {
	t.Helper()
	c, err := snapshot.OpenContainerReadOnly(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	assertContainerMembership(t, c, want)
}

func assertContainerMembership(t *testing.T, c *snapshot.Container, want []string) {
	t.Helper()
	state, err := snapshot.LoadState(c)
	require.NoError(t, err)
	gotState := make([]string, 0, len(state.InstancePickerSnapshot.Instances))
	for _, inst := range state.InstancePickerSnapshot.Instances {
		gotState = append(gotState, inst.CRN)
	}
	assert.Equal(t, want, gotState, "state membership")
	assert.ElementsMatch(t, want, snapshot.InstanceCRNs(c), "instance frame names")
}

func newSnapshotMembershipModel(t *testing.T) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	m := newTestModel(t, &fakePoster{})
	m.bundle = bundle
	m.list = list.New(bundle)
	m.instances = Instances{
		newTestInstance(t, "selected-logs"),
		newTestInstance(t, "selected-empty"),
		newTestInstance(t, "unselected"),
	}
	return m
}

func writeTestFrame(t *testing.T, m *Model, name string) {
	t.Helper()
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, name, []icl.Log{{
		Data: map[string]any{"message": name}, Metadata: icl.Metadata{ID: name},
	}}))
}

//nolint:paralleltest // finalization writes this process's PID-named .inuse guard
func TestFinalizeBackingContainer_ScopesNormalAndPartialFetchesToCapturedSelection(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, tc := range []struct {
		name       string
		emptyState status.Phase
	}{
		{name: "normal", emptyState: status.Success},
		{name: "partial error", emptyState: status.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newSnapshotMembershipModel(t)
			m.captureSnapshotMembers([]string{testCRN("selected-logs"), testCRN("selected-empty")})
			attachBackingFile(t, m)

			withLogs := m.instances.FindByName("selected-logs")
			withLogs.state = status.Success
			withLogs.flushedLogCount = 1
			withLogs.flushedLogsSize = 1
			writeTestFrame(t, m, withLogs.CRN)
			empty := m.instances.FindByName("selected-empty")
			empty.state = tc.emptyState
			m.instances.FindByName("unselected").state = status.Disabled

			// Changing selection after fetch start must not alter the declaration.
			withLogs.Disable()
			m.maybeFinalizeFetch()

			assertSnapshotMembership(t, m.GetBackingPath(), []string{testCRN("selected-logs"), testCRN("selected-empty")})
			c, err := snapshot.OpenContainerReadOnly(m.GetBackingPath())
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })
			emptyLogs, err := snapshot.LoadInstanceLogs(c, testCRN("selected-empty"))
			require.NoError(t, err)
			assert.Empty(t, emptyLogs, "selected zero-result or failed member gets an empty frame")
			state, err := snapshot.LoadState(c)
			require.NoError(t, err)
			assert.Equal(t, int(tc.emptyState), state.InstancePickerSnapshot.Instances[1].State,
				"the selected member's terminal state is retained")
		})
	}
}

//nolint:paralleltest // discard clears this process's PID-named .inuse guard
func TestMaybeFinalizeFetch_AllEmptyDiscardsCapturedSelection(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newSnapshotMembershipModel(t)
	m.captureSnapshotMembers([]string{testCRN("selected-logs"), testCRN("selected-empty")})
	wipPath := attachBackingFile(t, m)
	m.instances.FindByName("selected-logs").state = status.Success
	m.instances.FindByName("selected-empty").state = status.Error
	m.instances.FindByName("unselected").state = status.Disabled

	m.maybeFinalizeFetch()

	assert.Empty(t, m.GetBackingPath())
	assert.NoFileExists(t, wipPath)
	assert.NoFileExists(t, autoSnapshotPath(filepath.Dir(wipPath), m.queryStartTime))
}

//nolint:paralleltest // finalization writes this process's PID-named .inuse guard
func TestEndWatch_PreservesCapturedSelectionInSnapshot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newSnapshotMembershipModel(t)
	m.captureSnapshotMembers([]string{testCRN("selected-logs"), testCRN("selected-empty")})
	attachBackingFile(t, m)
	m.watching = true
	m.instances.FindByName("selected-logs").state = status.Success
	m.instances.FindByName("selected-logs").flushedLogCount = 1
	m.instances.FindByName("selected-logs").flushedLogsSize = 1
	writeTestFrame(t, m, testCRN("selected-logs"))
	m.instances.FindByName("selected-empty").state = status.Cancelled
	m.instances.FindByName("unselected").state = status.Disabled

	m.endWatch("test")

	assertSnapshotMembership(t, m.GetBackingPath(), []string{testCRN("selected-logs"), testCRN("selected-empty")})
}

//nolint:paralleltest // watch finalization writes this process's PID-named .inuse guard
func TestWatch_SelectionChangedDuringWatchDoesNotFetchOrWriteUnselectedInstance(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var queries atomic.Int32
	queryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/query", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		if queries.Add(1) == 1 {
			return // First round is empty, so the watch starts another round.
		}
		_, _ = w.Write([]byte("data: {\"result\":{\"results\":[{\"user_data\":\"{}\",\"metadata\":[{\"key\":\"logid\",\"value\":\"selected-log\"}]}]}}\n\n"))
	}))
	t.Cleanup(queryServer.Close)

	bundle := depstest.NewTest(t)
	bundle.Config.Core.WatchCooldownSeconds = 0
	selected := NewInstance(bundle, "selected", queryServer.URL, testCRN("a"), icl.EnvProd, "%.2f")
	selected.Enable()
	unselected := NewInstance(bundle, "unselected", queryServer.URL, testCRN("b"), icl.EnvProd, "%.2f")
	unselected.Disable()
	am, closeTokenServer := oidcTokenServer(t, selected.Name, unselected.Name)
	t.Cleanup(closeTokenServer)
	m := newCollectModel(t, am, Instances{selected, unselected})
	m.bundle.Config.Core.WatchCooldownSeconds = 0
	m.bundle.Config.Core.MaxAutoSnapshots = 1
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	oldPath := autoSnapshotPath(dir, time.Unix(1, 0))
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o600))
	require.NoError(t, os.Chtimes(oldPath, time.Time{}, time.Unix(1, 0)))

	qp, ok := m.poster.(*queuePoster)
	require.True(t, ok)
	dispatched := 0
	dispatch := func() {
		for dispatched < len(qp.posted) {
			switch msg := qp.posted[dispatched].(type) {
			case MemberAuthResolvedMsg:
				m.OnMemberAuthResolved(msg)
			case WatchTickMsg:
				m.OnWatchTick(msg)
			case InstanceFlushedMsg:
				m.OnInstanceFlushed(msg)
			}
			dispatched++
		}
	}

	m.startWatch()
	qp.drain() // Resolve round one's selected member.
	dispatch() // Start its query.
	qp.drain() // Complete its empty query and post the next-round tick.

	unselected.Enable() // Selection changes while the watch is still active.
	assert.Equal(t, status.Enabled, unselected.state)
	dispatch() // The tick may re-fetch only the captured selected member.
	qp.drain() // Resolve round two's selected member.
	dispatch() // Start its query.
	qp.drain() // Complete the query and its asynchronous frame compression.
	dispatch() // Write the selected frame and finalize the watch.

	assert.False(t, m.IsWatching(), "the captured member has logs, so the watch finalizes")
	assert.Equal(t, int32(2), queries.Load(), "only the selected instance is fetched in both rounds")
	require.NotEmpty(t, m.GetBackingPath())
	assert.NoFileExists(t, oldPath, "watch finalization must retain synchronously")
	assertSnapshotMembership(t, m.GetBackingPath(), []string{testCRN("a")})
}

//nolint:paralleltest // restore clears this process's PID-named .inuse guard
func TestSnapshotRestore_ManualSavePreservesBackingMembership(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newSnapshotMembershipModel(t)
	restored := snapshot.Snapshot{InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
		{CRN: testCRN("selected-logs"), State: int(status.Success), LogCount: 1, LogsSizeBytes: 1},
		{CRN: testCRN("selected-empty"), State: int(status.Error)},
	}}}
	m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: restored})

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "backing.lognav")
	srcFile, err := os.Create(srcPath) // #nosec G304 -- test-only path
	require.NoError(t, err)
	src := snapshot.NewWriter(srcFile)
	for _, name := range []string{"selected-logs", "selected-empty", "unselected"} {
		writeLogs := []icl.Log(nil)
		if name == "selected-logs" {
			writeLogs = []icl.Log{{Metadata: icl.Metadata{ID: name}}}
		}
		require.NoError(t, snapshot.SaveInstanceFrame(src, testCRN(name), writeLogs))
	}
	require.NoError(t, snapshot.SaveStateFrame(src, restored))
	require.NoError(t, src.Close())
	require.NoError(t, srcFile.Close())

	srcRO, err := snapshot.OpenContainerReadOnly(srcPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srcRO.Close() })
	dstPath := filepath.Join(dir, "manual.lognav")
	dstFile, err := os.Create(dstPath) // #nosec G304 -- test-only path
	require.NoError(t, err)
	dst := snapshot.NewWriter(dstFile)
	require.NoError(t, snapshot.CopyWithState(dst, srcRO, m.SnapshotMeta()))
	require.NoError(t, dst.Close())
	require.NoError(t, dstFile.Close())

	assertSnapshotMembership(t, dstPath, []string{testCRN("selected-logs"), testCRN("selected-empty")})
}
