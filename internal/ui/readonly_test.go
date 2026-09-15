package ui

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

// TestReadOnlyBackingEvictionClearsLoadedStore exercises the injected on-loop
// removal seam: a transient row currently shown in the log viewer is detached
// when its backing is removed.
func TestSnapshotRestoreClearsAdoptedStoreOnlyOnSuccess(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "success clears adopted store"},
		{name: "failure preserves adopted store", err: errors.New("restore failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bundle := depstest.NewTest(t)
			bundle.Config.ICL.Instances = []config.ICLInstanceConfig{{
				Name: "test", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test::"),
			}}
			m, err := New(t.Context(), bundle)
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Close() })
			m.OnResize(120, 40)
			m.bundle.State.SetJQ("")
			instances := m.instances.GetInstances()
			require.NotEmpty(t, instances)
			store := instances[0].Store
			store.SetLogsRaw([]icl.Log{{Data: map[string]any{"message": "old log"}}})
			m.logviewer.SetStore(store)
			m.loadedInstance = instances[0].CRN
			m.intendedInstance = instances[0].CRN
			require.Same(t, store, m.logviewer.GetStore())
			require.Len(t, m.logviewer.StatusVariants()[0], 3, "setup: old store has contextual status")

			m.Update(msgs.SnapshotRestoreMsg{Err: tt.err})

			if tt.err == nil {
				assert.Nil(t, m.logviewer.GetStore())
				assert.Empty(t, m.logviewer.StatusVariants()[0], "old contextual status is gone")
				assert.Empty(t, m.loadedInstance)
				assert.Empty(t, m.intendedInstance)
				return
			}
			assert.Same(t, store, m.logviewer.GetStore())
			assert.Equal(t, 1, store.GetLogCount())
			assert.Len(t, m.logviewer.StatusVariants()[0], 3, "old contextual status is unchanged")
			assert.Equal(t, instances[0].CRN, m.loadedInstance)
			assert.Equal(t, instances[0].CRN, m.intendedInstance)
		})
	}
}

func TestReadOnlyBackingEvictionClearsLoadedStatus(t *testing.T) {
	// ui.New writes package-level key styles, so this cannot run in parallel.
	m, err := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })

	crn := "crn:v1:bluemix:public:logs:us-south:a/account:unknown::"
	backing := filepath.Join(t.TempDir(), "restored.lognav")
	m.Update(msgs.SnapshotRestoreMsg{Snapshot: snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{{
			CRN: crn, State: int(status.Success), LogCount: 2,
		}}},
	}, BackingPath: backing})
	inst := m.instances.GetInstances().FindByCRN(crn)
	require.NotNil(t, inst)
	require.True(t, inst.IsReadOnly())

	m.loadedInstance = crn
	m.intendedInstance = crn
	m.logviewer.SetStore(inst.Store)
	m.instances.EvictIfCurrentBacking(filepath.Base(backing))

	assert.Empty(t, m.loadedInstance)
	assert.Empty(t, m.intendedInstance)
}
