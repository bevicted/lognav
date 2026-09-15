package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingBroadcaster is a sessionbus.Broadcaster test double that records
// every Broadcast method instead of dialing real sockets.
type recordingBroadcaster struct {
	mu      sync.Mutex
	methods []string
}

func (r *recordingBroadcaster) Broadcast(_ context.Context, method string, _ any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append(r.methods, method)
	return nil
}

func (r *recordingBroadcaster) recordedMethods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.methods...)
}

// confirmSave proceeds when idle.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_ConfirmSave_AllowsWhenIdle(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedModelWithPoster(t)
	require.False(t, m.instances.IsFetching(), "setup: no fetch")

	err := m.confirmSave("incident")
	assert.NoError(t, err)
}

//nolint:paralleltest // ui.New writes package-level key styles.
func TestUI_ManualSaveDialog_StartsWithEmptyFilename(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedModelWithPoster(t)

	require.True(t, m.handleSnapshot(msgs.ManualSaveSnapshotMsg{}))
	require.NotEmpty(t, fp.Posted)
	dlg, ok := fp.Posted[len(fp.Posted)-1].(msgs.ShowDialogMsg)
	require.True(t, ok)
	require.NotNil(t, dlg.Input)
	assert.Empty(t, dlg.Input.Value())
}

// validateSaveFilename must reduce free dialog text to a basename inside the
// managed snapshot dir: separators, "..", and empty names are refused outright
// (they would rename the .wip outside the dir).
func TestUI_ValidateSaveFilename(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "incident", want: "incident.lognav"},
		{in: "incident.lognav", want: "incident.lognav"},
		{in: "  incident  ", want: "incident.lognav"},
		{in: "latest", wantErr: true},
		{in: "auto-incident", wantErr: true},
		{in: "auto-20260814-120000.lognav", wantErr: true},
		{in: "incident-auto", want: "incident-auto.lognav"},
		{in: "../foo", wantErr: true},
		{in: "sub/dir", wantErr: true},
		{in: `sub\dir`, wantErr: true},
		{in: "..", wantErr: true},
		{in: ".", wantErr: true},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "a\x00b", wantErr: true},
	} {
		got, err := validateSaveFilename(c.in)
		if c.wantErr {
			require.Errorf(t, err, "%q must be refused", c.in)
			continue
		}
		require.NoErrorf(t, err, "%q must be accepted", c.in)
		assert.Equalf(t, c.want, got, "normalization of %q", c.in)
	}
}

// confirmSave refuses a name already taken by a snapshot file: v1 has no
// overwrite flow, so the dialog stays open (non-nil error) and the existing
// file is left alone.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_ConfirmSave_RejectsUnsafeNameWithoutSaving(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedModelWithPoster(t)
	broadcaster := &recordingBroadcaster{}
	m.SetBroadcaster(broadcaster)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.Error(t, m.confirmSave("../escape"))

	assert.NoFileExists(t, filepath.Join(dir, "escape.lognav"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(dir), "escape.lognav"))
	assert.Empty(t, fp.Posted)
	assert.Empty(t, broadcaster.recordedMethods())
}

func TestUI_ConfirmSave_RefusesExistingSnapshot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedModelWithPoster(t)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "taken.lognav"), []byte("original"), 0o600))

	err = m.confirmSave("taken")
	require.Error(t, err, "an existing snapshot name must be refused")
	assert.Contains(t, err.Error(), "already exists")

	data, rerr := os.ReadFile(filepath.Join(dir, "taken.lognav")) // #nosec G304 -- test-only path
	require.NoError(t, rerr)
	assert.Equal(t, "original", string(data), "the refused save must not touch the existing file")
}

// confirmSave refuses a name another LIVE session holds via its <pid>.inuse
// guard, with a message naming that reason (overwriting would swap the backing
// file out from under a peer).
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME), PID-named files, and the package-level pidAlive seam.
func TestUI_ConfirmSave_RefusesInUseByOtherSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedModelWithPoster(t)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "held.lognav"), []byte("peer's file"), 0o600))
	// A peer session's guard file. The PID must not be ours, and must read alive.
	peer := os.Getpid() + 1
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, strconv.Itoa(peer)+".inuse"), []byte("held.lognav\n"), 0o600,
	))
	t.Cleanup(snapshot.SetPidAliveForTest(func(pid int) bool { return pid == peer }))

	err = m.confirmSave("held")
	require.Error(t, err, "a snapshot held by another live session must be refused")
	assert.Contains(t, err.Error(), "in use by another session")
}

// The worker's RenameNoClobber backstop: when the name is taken AFTER
// confirmSave's check (simulated by calling saveSnapshot directly), the save
// must fail without overwriting, and the user must see a notice — not just a
// log line.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_SaveSnapshot_RenameNoClobber_RefusesAndNotifies(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedModelWithPoster(t)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	final := filepath.Join(dir, "raced.lognav")
	require.NoError(t, os.WriteFile(final, []byte("original"), 0o600))

	fp.Posted = nil
	m.saveSnapshot("raced.lognav") // FakePoster.Go runs the worker synchronously

	data, rerr := os.ReadFile(final) // #nosec G304 -- test-only path
	require.NoError(t, rerr)
	assert.Equal(t, "original", string(data), "RenameNoClobber must not overwrite the existing snapshot")

	var notice, done bool
	for _, ev := range fp.Posted {
		if d, ok := ev.(msgs.ShowDialogMsg); ok && d.Title == "Cannot save snapshot" {
			notice = true
		}
		if _, ok := ev.(msgs.ManualSaveDoneMsg); ok {
			done = true
		}
	}
	assert.True(t, notice, "a clobber refusal must surface a dialog, not only a log line")
	assert.False(t, done, "a refused save must not report success")

	entries, derr := os.ReadDir(dir)
	require.NoError(t, derr)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), snapshot.WipSuffix, "the failed save must clean up its .wip")
	}
}

// A post-link WIP cleanup failure still publishes the manual snapshot, so its
// completion and peer visibility effects must run while stale cleanup retains
// the source WIP.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) and manualRenameNoClobber are process-global test seams.
func TestUI_SaveSnapshot_CleanupFailurePublishesAndNotifies(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedModelWithPoster(t)
	broadcaster := &recordingBroadcaster{}
	m.SetBroadcaster(broadcaster)

	origRename := manualRenameNoClobber
	manualRenameNoClobber = func(src, dst string) error {
		require.NoError(t, os.Link(src, dst))
		return &snapshot.RenameCleanupError{Err: errors.New("unlink failed")}
	}
	t.Cleanup(func() { manualRenameNoClobber = origRename })

	m.saveSnapshot("incident.lognav")

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	finalPath := filepath.Join(dir, "incident.lognav")
	assert.FileExists(t, finalPath)
	container, err := snapshot.OpenContainerReadOnly(finalPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Close() })
	_, err = snapshot.LoadState(container)
	require.NoError(t, err)

	var done bool
	for _, ev := range fp.Posted {
		if msg, ok := ev.(msgs.ManualSaveDoneMsg); ok && msg.Name == "incident.lognav" {
			done = true
		}
	}
	assert.True(t, done, "a published manual snapshot must report completion")
	assert.Contains(t, broadcaster.recordedMethods(), sessionbus.MethodSnapshotsDirty)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var wipFound bool
	for _, entry := range entries {
		wipFound = wipFound || strings.HasSuffix(entry.Name(), snapshot.WipSuffix)
	}
	assert.True(t, wipFound, "published cleanup failure must leave the WIP for stale cleanup")
}

// A save with no backing (no fetch yet) writes a readable snapshot via the
// src==nil degrade path and posts ManualSaveDoneMsg.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_SaveSnapshot_NoBacking_WritesReadableSnapshot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedModelWithPoster(t)

	m.saveSnapshot("incident.lognav") // FakePoster.Go runs the IO synchronously

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	c, err := snapshot.OpenContainerReadOnly(filepath.Join(dir, "incident.lognav"))
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	_, err = snapshot.LoadState(c)
	require.NoError(t, err, "saved file must have a readable state frame")

	var done bool
	for _, ev := range fp.Posted {
		if d, ok := ev.(msgs.ManualSaveDoneMsg); ok && d.Name == "incident.lognav" {
			done = true
		}
	}
	assert.True(t, done, "a successful save posts ManualSaveDoneMsg with the filename")
}

// A restored backing may have fewer members than the current configuration. A
// manual save must retain the backing declaration rather than adding every row
// currently configured in the picker.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestUI_SaveSnapshot_AfterRestorePreservesBackingMembership(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.ICL.Instances = []config.ICLInstanceConfig{
		{Name: "test-a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test-a::")},
		{Name: "test-b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test-b::")},
		{Name: "test-c", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test-c::")},
	}
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	m.SetPoster(&msgstest.FakePoster{})
	m.OnResize(120, 40)
	instances := m.instances.GetInstances()
	require.GreaterOrEqual(t, len(instances), 3)
	want := []string{instances[0].CRN, instances[1].CRN}

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	backingPath := filepath.Join(dir, "backing.lognav")
	backingFile, err := os.Create(backingPath) // #nosec G304 -- test-only managed path
	require.NoError(t, err)
	backing := snapshot.NewWriter(backingFile)
	for _, crn := range append(append([]string(nil), want...), instances[2].CRN) {
		require.NoError(t, snapshot.SaveInstanceFrame(backing, crn, nil))
	}
	restored := snapshot.Snapshot{InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{
		{CRN: want[0]}, {CRN: want[1]},
	}}}
	require.NoError(t, snapshot.SaveStateFrame(backing, restored))
	require.NoError(t, backing.Close())
	require.NoError(t, backingFile.Close())

	m.instances.OnSnapshotRestore(msgs.SnapshotRestoreMsg{Snapshot: restored, BackingPath: backingPath})
	m.saveSnapshot("manual.lognav")

	manual, err := snapshot.OpenContainerReadOnly(filepath.Join(dir, "manual.lognav"))
	require.NoError(t, err)
	defer func() { _ = manual.Close() }()
	state, err := snapshot.LoadState(manual)
	require.NoError(t, err)
	gotState := make([]string, 0, len(state.InstancePickerSnapshot.Instances))
	for _, inst := range state.InstancePickerSnapshot.Instances {
		gotState = append(gotState, inst.CRN)
	}
	assert.Equal(t, want, gotState)
	assert.ElementsMatch(t, want, snapshot.InstanceCRNs(manual))
}

// A successful manual save broadcasts snapshots_dirty so peer sessions refresh
// their list (the manual-save path never reaches setBacking/notifyDirty, so it
// must broadcast directly). The worker runs inside FakePoster.Go = synchronously
// here, and the broadcast is off-loop so it needs no poster.Go wrap.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_SaveSnapshot_BroadcastsSnapshotsDirty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedModelWithPoster(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb) // after SetPoster (newSizedModelWithPoster) so the closure binds the poster

	m.saveSnapshot("m_test.lognav")

	assert.Contains(t, rb.recordedMethods(), sessionbus.MethodSnapshotsDirty,
		"a successful manual save must broadcast snapshots_dirty")
}

// A snapshot restore (claim) must self-post SnapshotsDirtyMsg to the local
// loop so the claiming session's own snapshot list recolors immediately (the
// peer broadcast alone is skipped by Broadcast which excludes own PID).
//
// Flow: SetBroadcaster installs the notifyDirty closure on the instancepicker →
// OnSnapshotRestore calls setBacking → setBacking calls notifyDirty → notifyDirty
// runs a poster.Go(PostCritical(SnapshotsDirtyMsg) + Broadcast). FakePoster.Go
// runs its func synchronously, so both effects are observable after the call.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_NotifyDirty_SelfPostsSnapshotsDirtyMsg(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, fp := newSizedModelWithPoster(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb) // installs the notifyDirty closure; must be after SetPoster

	// Trigger notifyDirty via the real claim path (OnSnapshotRestore → setBacking).
	// An empty SnapshotRestoreMsg is safe: default config has instances so the
	// cursor-index access at line 660 of instancepicker.go is in-bounds.
	fp.Posted = nil // clear any startup noise
	m.instances.OnSnapshotRestore(msgs.SnapshotRestoreMsg{})

	var selfPosted bool
	for _, ev := range fp.Posted {
		if _, ok := ev.(msgs.SnapshotsDirtyMsg); ok {
			selfPosted = true
		}
	}
	assert.True(t, selfPosted,
		"notifyDirty must self-post SnapshotsDirtyMsg so the claiming session recolors its own list")
	assert.Contains(t, rb.recordedMethods(), sessionbus.MethodSnapshotsDirty,
		"notifyDirty must also broadcast snapshots_dirty to peers")
}

// BroadcastSnapshotsDirtySync is the shutdown release-notify path: a SYNCHRONOUS
// (no poster.Go) direct Broadcast, safe to call after the poster is drained.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_BroadcastSnapshotsDirtySync_CallsBroadcaster(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedModelWithPoster(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb)

	err := m.BroadcastSnapshotsDirtySync(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{sessionbus.MethodSnapshotsDirty}, rb.recordedMethods(),
		"BroadcastSnapshotsDirtySync must call Broadcast(snapshots_dirty) exactly once, synchronously")
}

// BroadcastSnapshotsDirtySync is nil-broadcaster safe: it no-ops and returns nil.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) + ui.New writes package-level keys state.
func TestUI_BroadcastSnapshotsDirtySync_NilBroadcaster(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, _ := newSizedModelWithPoster(t)
	m.SetBroadcaster(nil)

	assert.NoError(t, m.BroadcastSnapshotsDirtySync(context.Background()),
		"a nil broadcaster must no-op and return nil")
}
