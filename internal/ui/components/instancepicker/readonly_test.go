package instancepicker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

func newReadOnlyRestoreModel(t *testing.T) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	bundle.Config.ICL.Instances = []config.ICLInstanceConfig{
		{Name: "configured-present", CRN: config.MustCRNFromString(testCRN("configured-present"))},
		{Name: "configured-absent", CRN: config.MustCRNFromString(testCRN("configured-absent"))},
	}
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func snapshotMember(crn string, phase status.Phase, count int, message string) snapshot.InstanceSnapshot {
	var logsSizeBytes uint64
	if count != 0 {
		logsSizeBytes = 1
	}
	return snapshot.InstanceSnapshot{
		CRN: crn, State: int(phase), LogCount: count, LogsSizeBytes: logsSizeBytes,
		StartTimeMicro: 10, LastUpdateTimeMicro: 20, Message: message,
	}
}

func TestSnapshotRestore_AppendsReadOnlyAndResetsAbsentConfigured(t *testing.T) {
	t.Parallel()
	m := newReadOnlyRestoreModel(t)
	present := m.instances.FindByName("configured-present")
	absent := m.instances.FindByName("configured-absent")
	absent.state = status.Success
	absent.flushedLogCount = 9
	absent.flushedLogsSize = 1
	absent.Store.SetMessage("stale")

	unknownA := testCRN("samepref-A")
	unknownB := testCRN("samepref-B")
	m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			snapshotMember(present.CRN, status.Success, 3, ""),
			snapshotMember(unknownA, status.Error, 1, "saved error"),
			snapshotMember(unknownB, status.Success, 0, "saved zero-log message"),
		}},
	}})

	require.Len(t, m.instances, 4)
	assert.Equal(t, []string{"configured-present", "configured-absent", "us-south/samepref", "us-south/samepref"},
		[]string{m.instances[0].Name, m.instances[1].Name, m.instances[2].Name, m.instances[3].Name})
	assert.Equal(t, status.Success, present.state)
	assert.Equal(t, 3, present.flushedLogCount)
	assert.Equal(t, status.Disabled, absent.state)
	assert.Zero(t, absent.flushedLogCount)
	assert.Empty(t, absent.Store.GetMessage())
	assert.True(t, absent.startTime.IsZero())
	assert.True(t, absent.lastUpdateTime.IsZero())
	for _, inst := range m.instances[2:] {
		assert.True(t, inst.IsReadOnly())
		assert.Equal(t, status.ReadOnly, inst.state)
		assert.False(t, inst.IsEnabled())
	}
	assert.Equal(t, "saved error", m.instances[2].Store.GetMessage())
	assert.Equal(t, "saved zero-log message", m.instances[3].Store.GetMessage())
}

func TestReadOnlyRows_AreImmutableAndExcluded(t *testing.T) {
	t.Parallel()
	m := newReadOnlyRestoreModel(t)
	unknown := testCRN("unknown")
	m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
			snapshotMember(unknown, status.Error, 1, "saved"),
		}},
	}})
	readOnly := m.instances.FindByCRN(unknown)
	require.NotNil(t, readOnly)

	readOnly.Toggle()
	m.instances.SetAll(true)
	m.instances.EnableCRNs([]string{unknown})
	assert.Equal(t, status.ReadOnly, readOnly.state)
	assert.False(t, readOnly.IsEnabled())
	assert.NotContains(t, m.instances.EnabledCRNs(), unknown)
	assert.Empty(t, m.instances.StartDispatchTimers())

	readOnly.Store.ClearData()
	assert.Same(t, readOnly, m.instances.FindByCRN(unknown), "resident-store eviction must retain the transient row")
}

//nolint:paralleltest // fetch/collect create PID-named backing files.
func TestReadOnlyRows_AreRemovedAtBackingReplacementChokepoints(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, tc := range []struct {
		name    string
		replace func(*Model)
	}{
		{name: "normal fetch", replace: func(m *Model) {
			m.instances.SetAll(false)
			require.NoError(t, m.startFetch())
		}},
		{name: "collect", replace: func(m *Model) {
			require.NoError(t, m.startCollect(&archive.Archive{}))
		}},
		{name: "current backing eviction", replace: func(m *Model) {
			m.setBackingPath(filepath.Join(t.TempDir(), "current.lognav"))
			m.EvictIfCurrentBacking("current.lognav")
		}},
		{name: "another restore", replace: func(m *Model) {
			m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newReadOnlyRestoreModel(t)
			m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{
				InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
					snapshotMember(testCRN("unknown"), status.Success, 1, ""),
				}},
			}})
			require.NotNil(t, m.instances.FindByCRN(testCRN("unknown")))
			tc.replace(m)
			assert.Nil(t, m.instances.FindByCRN(testCRN("unknown")))
		})
	}
}

//nolint:paralleltest // writes a backing fixture.
func TestReadOnlyRows_LazyLoadAndManualSavePreserveOriginalSnapshotData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.lognav")
	unknown := testCRN("unknown")
	original := snapshotMember(unknown, status.Error, 1, "original message")
	f, err := os.Create(path) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	src := snapshot.NewWriter(f)
	require.NoError(t, snapshot.SaveInstanceFrame(src, unknown, []icl.Log{{Metadata: icl.Metadata{ID: "kept"}}}))
	require.NoError(t, snapshot.SaveStateFrame(src, snapshot.Snapshot{InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{original}}}))
	require.NoError(t, src.Close())
	require.NoError(t, f.Close())

	m := newReadOnlyRestoreModel(t)
	m.poster = &fakePoster{}
	m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{original}},
	}, BackingPath: path})
	readOnly := m.instances.FindByCRN(unknown)
	require.NotNil(t, readOnly)
	m.OpenInstance(unknown)
	poster, ok := m.poster.(*fakePoster)
	require.True(t, ok)
	posted := poster.events()
	require.Len(t, posted, 2) // restore selection + lazy-load result
	ready, ok := posted[1].(InstanceLoadReadyMsg)
	require.True(t, ok)
	require.NoError(t, ready.Err)
	require.Len(t, ready.Logs, 1)

	dstPath := filepath.Join(dir, "manual.lognav")
	dstFile, err := os.Create(dstPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	dst := snapshot.NewWriter(dstFile)
	opened, err := snapshot.OpenContainerReadOnly(path)
	require.NoError(t, err)
	require.NoError(t, snapshot.CopyWithState(dst, opened, m.SnapshotMeta()))
	require.NoError(t, opened.Close())
	require.NoError(t, dst.Close())
	require.NoError(t, dstFile.Close())

	out, err := snapshot.OpenContainerReadOnly(dstPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = out.Close() })
	state, err := snapshot.LoadState(out)
	require.NoError(t, err)
	require.Equal(t, []snapshot.InstanceSnapshot{original}, state.InstancePickerSnapshot.Instances)
	logs, err := snapshot.LoadInstanceLogs(out, unknown)
	require.NoError(t, err)
	assert.Len(t, logs, 1)
	source, err := snapshot.OpenContainerReadOnly(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })
	wantFrame, err := source.ReadFrame("i_" + unknown)
	require.NoError(t, err)
	gotFrame, err := out.ReadFrame("i_" + unknown)
	require.NoError(t, err)
	assert.Equal(t, wantFrame, gotFrame, "manual save copies the original read-only frame bytes")
}

func TestNewReadOnlyInstance_EmptyFrameMessageSurvivesLazyLoadReset(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	inst, err := NewReadOnlyInstance(bundle, snapshotMember(testCRN("unknown"), status.Error, 0, "saved message"))
	require.NoError(t, err)
	inst.Store.SetLogsRaw(nil)
	inst.RestoreReadOnlyMessage()
	assert.Equal(t, status.ReadOnly, inst.state)
	assert.Equal(t, "saved message", inst.Store.GetMessage())
}
