package snapshothandler

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/component/componenttest"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// recordedBroadcast captures one Broadcast call's method + payload.
type recordedBroadcast struct {
	method  string
	payload any
}

// recordingBroadcaster is a sessionbus.Broadcaster test double that records
// every Broadcast call instead of dialing real sockets.
type recordingBroadcaster struct {
	mu    sync.Mutex
	calls []recordedBroadcast
}

func (r *recordingBroadcaster) Broadcast(_ context.Context, method string, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedBroadcast{method: method, payload: payload})
	return nil
}

func (r *recordingBroadcaster) recorded() []recordedBroadcast {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedBroadcast(nil), r.calls...)
}

// fakePoster is a test double for msgs.Poster: Go runs fn inline (goleak-clean)
// and PostCritical records delivered events for later assertions.
type fakePoster struct {
	mu     sync.Mutex
	posted []uv.Event
}

func (f *fakePoster) Go(fn func(context.Context)) { fn(context.Background()) }

func (f *fakePoster) PostCritical(_ context.Context, ev uv.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, ev)
	return nil
}

func (f *fakePoster) Post(uv.Event) {}

func (f *fakePoster) events() []uv.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uv.Event(nil), f.posted...)
}

// newTestModel creates a *Model for tests, wired with an inline fakePoster (so
// the filehandler's IO methods run synchronously and goleak stays clean). It
// sets XDG_DATA_HOME so that snapshot.Dir() (called inside New) does not read
// the real user home directory. All tests that call New must go through this
// helper.
func newTestModel(t *testing.T) (*Model, *fakePoster) {
	t.Helper()
	// snapshot.Dir() calls xdg.GetDataPath() which reads XDG_DATA_HOME.
	// Point it at a per-test temp dir so tests do not touch the real data path.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(bundle)
	fp := &fakePoster{}
	m.SetPoster(fp)
	return m, fp
}

// hasListOP reports whether any posted event is a filehandler ListOP IOMsg.
// Because filehandler.IOMsg is unexported, we detect the refresh by checking
// that the poster received at least one event after a ListFiles-triggering
// message (ListFiles posts exactly one ListOP IOMsg per call).
func hasListOP(events []uv.Event) bool {
	return len(events) > 0
}

func TestSnapshotRenameValidator(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "descriptive", input: "incident.lognav"},
		{name: "latest", input: "latest.lognav", wantErr: true},
		{name: "automatic namespace", input: "auto-incident.lognav", wantErr: true},
		{name: "canonical automatic name", input: "auto-20260814-120000.lognav", wantErr: true},
		{name: "separator", input: "sub/incident.lognav", wantErr: true},
		{name: "backslash", input: `sub\\incident.lognav`, wantErr: true},
		{name: "traversal", input: "../incident.lognav", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSnapshotRename(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestNew_BuildsFileHandlerWithSnapshotDir verifies that New constructs a
// Model with a non-nil filehandler, confirming that snapshot.Dir() resolved
// successfully and was wired into the filehandler.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestNew_BuildsFileHandlerWithSnapshotDir(t *testing.T) {
	m, _ := newTestModel(t)
	require.NotNil(t, m)
	assert.NotNil(t, m.fh)
}

// TestOnSaveSnapshot_ListsFiles verifies that OnSaveSnapshot refreshes the file
// list. fh.ListFiles is void (it spawns a tracked poster goroutine), so the
// refresh manifests as an event posted via the poster.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestOnSaveSnapshot_ListsFiles(t *testing.T) {
	m, fp := newTestModel(t)
	m.OnSaveSnapshot(msgs.SaveSnapshotMsg{})
	assert.True(t, hasListOP(fp.events()), "OnSaveSnapshot must trigger a ListFiles refresh via the poster")
}

// TestOnManualSaveDone_ListsFiles verifies that OnManualSaveDone refreshes the
// file list via the poster.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestOnManualSaveDone_ListsFiles(t *testing.T) {
	m, fp := newTestModel(t)
	// Name carries the saved filename so the tab drops any stale cached preview
	// for it before re-listing (the overwrite bypasses the filehandler WriteOP).
	m.OnManualSaveDone(msgs.ManualSaveDoneMsg{Name: "m_test" + snapshot.FileExt})
	assert.True(t, hasListOP(fp.events()), "OnManualSaveDone must trigger a ListFiles refresh via the poster")
}

// TestModel_Draw_DoesNotPanicAcrossRectSizes drives Draw against the
// canonical rect matrix (zero, tiny, normal, oversized) and asserts no panic
// at any size.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestModel_Draw_DoesNotPanicAcrossRectSizes(t *testing.T) {
	m, _ := newTestModel(t)
	componenttest.DrawMatrix(t, m.SetRect, m.Draw)
}

// TestGetKeybinds_CuratesFileAndSnapshotHints verifies that Snapshots opts in
// to copied filehandler hints plus its own snapshot-save action.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestGetKeybinds_CuratesFileAndSnapshotHints(t *testing.T) {
	m, _ := newTestModel(t)
	hints := keys.HintBindings(m.GetKeybinds())
	require.Len(t, hints, 4)
	assert.Equal(t, []string{"open", "rename", "delete", "save"}, []string{
		hints[0].Label,
		hints[1].Label,
		hints[2].Label,
		hints[3].Label,
	})
	assert.Equal(t, []int{2, 3, 4, 5}, []int{
		hints[0].Priority,
		hints[1].Priority,
		hints[2].Priority,
		hints[3].Priority,
	})
}

// TestInit_PopulatesFileListViaPoster verifies that Init kicks off the
// filehandler's initial ListFiles call. ListFiles is now void (it spawns a
// tracked poster goroutine), so Init returns nil and the listing is posted via
// the poster.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestInit_PopulatesFileListViaPoster(t *testing.T) {
	m, fp := newTestModel(t)
	m.Init()
	assert.True(t, hasListOP(fp.events()), "Init must populate the file list via the poster")
}

// writeForeignHolder writes a <ppid>.inuse file into the snapshot dir listing
// basename, simulating that a different LIVE process (our parent, the go test
// runner) holds basename. os.Getppid() is alive during the test and != our own
// PID, so InUse/InUseByOther count it as a foreign holder without any seam.
func writeForeignHolder(t *testing.T, basename string) {
	t.Helper()
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	path := filepath.Join(dir, strconv.Itoa(os.Getppid())+".inuse")
	require.NoError(t, os.WriteFile(path, []byte(basename+"\n"), 0o600))
}

// TestGuardDelete_RefusesOtherEvictsSelfPermitsFree exercises the real
// explicit-delete gate: a file held by another live instance is refused (and the
// evictor is not called), while a free file is permitted (and evicted).
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestGuardDelete_RefusesOtherEvictsSelfPermitsFree(t *testing.T) {
	m, _ := newTestModel(t)
	var evicted []string
	m.SetEvictor(func(b string) { evicted = append(evicted, b) })

	writeForeignHolder(t, "held.lognav")

	// (a) held by another -> refused, evict NOT called.
	err := m.guardDelete("held.lognav")
	require.Error(t, err)
	assert.Empty(t, evicted)

	// (b) free file -> permitted, evict called.
	err = m.guardDelete("free.lognav")
	require.NoError(t, err)
	assert.Equal(t, []string{"free.lognav"}, evicted)
}

// TestSetEvictor_StoresCallback verifies that SetEvictor stores the injected
// callback and that the callback is invoked when m.evict is called directly.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestSetEvictor_StoresCallback(t *testing.T) {
	m, _ := newTestModel(t)

	var evicted string
	m.SetEvictor(func(base string) { evicted = base })
	m.evict("m_x.lognav")
	assert.Equal(t, "m_x.lognav", evicted)
}

// TestModel_ImplementsContextMenuSeams asserts snapshothandler opts into both the
// keyboard (ContextMenuOpener) and right-click (SecondaryMouseTarget) context
// menu seams so the global `c` keybind and right-click reach its filehandler.
func TestModel_ImplementsContextMenuSeams(t *testing.T) {
	t.Parallel()
	var _ component.ContextMenuOpener = (*Model)(nil)
	var _ component.SecondaryMouseTarget = (*Model)(nil)
}

// findBroadcast returns the first recorded broadcast with the given method.
func findBroadcast(calls []recordedBroadcast, method string) (recordedBroadcast, bool) {
	for _, c := range calls {
		if c.method == method {
			return c, true
		}
	}
	return recordedBroadcast{}, false
}

// TestOnRenamedHook_BroadcastsAndSelfPosts proves the fhOpts.OnRenamed hook both
// broadcasts snapshot_renamed to peers AND self-posts SnapshotRenamedMsg so the
// originating session self-corrects through the same loop handler remote sessions
// use. The fakePoster runs RenameFile's poster.Go body inline, so the hook fires
// synchronously here.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestOnRenamedHook_BroadcastsAndSelfPosts(t *testing.T) {
	m, fp := newTestModel(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	// A real source file must exist for the no-clobber rename to succeed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old"+snapshot.FileExt), []byte("x"), 0o600))

	m.fh.RenameFile("old", "new")

	bc, ok := findBroadcast(rb.recorded(), sessionbus.MethodSnapshotRenamed)
	require.True(t, ok, "rename must broadcast snapshot_renamed")
	p, ok := bc.payload.(sessionbus.SnapshotRenamedParams)
	require.True(t, ok, "payload must be SnapshotRenamedParams")
	assert.Equal(t, "old"+snapshot.FileExt, p.From)
	assert.Equal(t, "new"+snapshot.FileExt, p.To)

	// The self-post: a SnapshotRenamedMsg delivered via the poster.
	var got *msgs.SnapshotRenamedMsg
	for _, ev := range fp.events() {
		if rn, ok := ev.(msgs.SnapshotRenamedMsg); ok {
			got = &rn
		}
	}
	require.NotNil(t, got, "rename must self-post SnapshotRenamedMsg")
	assert.Equal(t, "old"+snapshot.FileExt, got.From)
	assert.Equal(t, "new"+snapshot.FileExt, got.To)
}

// TestSnapshotRenameRejectsUnsafeNameWithoutNotification verifies invalid
// dialog input does not move a file or notify another session.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestSnapshotRenameRejectsUnsafeNameWithoutNotification(t *testing.T) {
	m, fp := newTestModel(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	oldPath := filepath.Join(dir, "old"+snapshot.FileExt)
	require.NoError(t, os.WriteFile(oldPath, []byte("x"), 0o600))

	m.fh.ListFiles()
	for _, ev := range fp.events() {
		if ioMsg, ok := ev.(filehandler.IOMsg); ok {
			m.OnFileIO(ioMsg)
		}
	}
	m.fh.RenameUnderCursor()

	var dlg msgs.ShowDialogMsg
	for _, ev := range fp.events() {
		if got, ok := ev.(msgs.ShowDialogMsg); ok && got.Title == "Rename" {
			dlg = got
		}
	}
	require.NotNil(t, dlg.Input)
	require.NotEmpty(t, dlg.Buttons)
	require.Error(t, dlg.Buttons[0].Cmd("../escape"))

	assert.FileExists(t, oldPath)
	assert.NoFileExists(t, filepath.Join(filepath.Dir(dir), "escape.lognav"))
	_, renamed := findBroadcast(rb.recorded(), sessionbus.MethodSnapshotRenamed)
	assert.False(t, renamed)
	for _, ev := range fp.events() {
		_, selfPosted := ev.(msgs.SnapshotRenamedMsg)
		assert.False(t, selfPosted)
	}
}

// TestOnRenamedHook_CollisionDoesNotBroadcast proves a colliding rename (dest
// exists) errors and fires neither the broadcast nor the self-post.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestOnRenamedHook_CollisionDoesNotBroadcast(t *testing.T) {
	m, fp := newTestModel(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old"+snapshot.FileExt), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "taken"+snapshot.FileExt), []byte("y"), 0o600))

	m.fh.RenameFile("old", "taken")

	_, ok := findBroadcast(rb.recorded(), sessionbus.MethodSnapshotRenamed)
	assert.False(t, ok, "a colliding rename must not broadcast")
	for _, ev := range fp.events() {
		_, isRn := ev.(msgs.SnapshotRenamedMsg)
		assert.False(t, isRn, "a colliding rename must not self-post")
	}
}

// TestOnDeletedHook_BroadcastsDirty proves the fhOpts.OnDeleted hook broadcasts
// snapshots_dirty after a successful explicit delete so peers drop the file.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestOnDeletedHook_BroadcastsDirty(t *testing.T) {
	m, _ := newTestModel(t)
	rb := &recordingBroadcaster{}
	m.SetBroadcaster(rb)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gone"+snapshot.FileExt), []byte("x"), 0o600))

	m.fh.DeleteFile("gone")

	_, ok := findBroadcast(rb.recorded(), sessionbus.MethodSnapshotsDirty)
	assert.True(t, ok, "a successful delete must broadcast snapshots_dirty")
}

// TestOnSnapshotsDirty_ListsFiles verifies the cross-session dirty handler
// refreshes the list via the poster.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestOnSnapshotsDirty_ListsFiles(t *testing.T) {
	m, fp := newTestModel(t)
	m.OnSnapshotsDirty(msgs.SnapshotsDirtyMsg{})
	assert.True(t, hasListOP(fp.events()), "OnSnapshotsDirty must trigger a ListFiles refresh via the poster")
}

// TestOnSnapshotRenamed_ListsFiles verifies the cross-session rename handler
// refreshes the list (the backing self-correction is instancepicker's job).
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestOnSnapshotRenamed_ListsFiles(t *testing.T) {
	m, fp := newTestModel(t)
	m.OnSnapshotRenamed(msgs.SnapshotRenamedMsg{From: "a" + snapshot.FileExt, To: "b" + snapshot.FileExt})
	assert.True(t, hasListOP(fp.events()), "OnSnapshotRenamed must trigger a ListFiles refresh via the poster")
}

// TestSnapshotRowStyle_Own verifies that the built row styler returns the success
// (green) style when this process's .inuse file lists the snapshot, and the zero
// style when no session holds it.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) + OS-level .inuse files forbid t.Parallel
func TestSnapshotRowStyle_Own(t *testing.T) {
	m, _ := newTestModel(t) // sets XDG_DATA_HOME to a temp dir via t.Setenv

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// Write our own .inuse file claiming "own.lognav".
	require.NoError(t, snapshot.SetOwnInUse(filepath.Join(dir, "own"+snapshot.FileExt)))
	t.Cleanup(func() { _ = snapshot.SetOwnInUse("") }) // clear after the test

	rowStyle := m.newSnapshotRowStyler()
	require.NotNil(t, rowStyle)
	assert.Equal(t, uv.Style{Fg: m.bundle.Config.Style.SuccessColor.Color}, rowStyle("own"+snapshot.FileExt),
		"a snapshot held by this process must be success (green) colored")
	assert.Equal(t, uv.Style{}, rowStyle("free"+snapshot.FileExt),
		"a snapshot no session holds must be unstyled")
}

// TestSnapshotRowStyle_Locked verifies that the built row styler returns the
// warning (yellow) style when another live process holds the snapshot and this
// process does not.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) + OS-level .inuse files forbid t.Parallel
func TestSnapshotRowStyle_Locked(t *testing.T) {
	m, _ := newTestModel(t) // sets XDG_DATA_HOME to a temp dir via t.Setenv

	// writeForeignHolder writes a ppid.inuse file claiming "held.lognav".
	writeForeignHolder(t, "held"+snapshot.FileExt)

	rowStyle := m.newSnapshotRowStyler()
	require.NotNil(t, rowStyle)
	assert.Equal(t, uv.Style{Fg: m.bundle.Config.Style.WarningColor.Color}, rowStyle("held"+snapshot.FileExt),
		"a snapshot only another live session holds must be warning (yellow) colored")
	assert.Equal(t, uv.Style{}, rowStyle("free"+snapshot.FileExt),
		"a snapshot no session holds must be unstyled")
}

// TestSnapshotRowStyle_TwoSessionsDifferentSnapshots is the multi-row case the
// single-scan index has to get right: this session holds one file, another live
// session holds a DIFFERENT one, and both rows are colored off ONE index build.
// A per-row scan and a single indexed scan must agree here — the index must not
// smear one session's claim across the other's row.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) + OS-level .inuse files forbid t.Parallel
func TestSnapshotRowStyle_TwoSessionsDifferentSnapshots(t *testing.T) {
	m, _ := newTestModel(t)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.NoError(t, snapshot.SetOwnInUse(filepath.Join(dir, "mine"+snapshot.FileExt)))
	t.Cleanup(func() { _ = snapshot.SetOwnInUse("") })
	writeForeignHolder(t, "theirs"+snapshot.FileExt)

	rowStyle := m.newSnapshotRowStyler()
	require.NotNil(t, rowStyle)
	assert.Equal(t, uv.Style{Fg: m.bundle.Config.Style.SuccessColor.Color}, rowStyle("mine"+snapshot.FileExt),
		"our own claim must stay green")
	assert.Equal(t, uv.Style{Fg: m.bundle.Config.Style.WarningColor.Color}, rowStyle("theirs"+snapshot.FileExt),
		"the other session's claim must stay yellow, not inherit our green")
	assert.Equal(t, uv.Style{}, rowStyle("free"+snapshot.FileExt),
		"an unheld snapshot must stay unstyled")
}

// TestSnapshotRowStyle_SelfPriorityWhenBoth verifies the self-priority invariant:
// when BOTH this process and another live session hold the snapshot, the built
// row styler returns the success (green) style — self wins over locked.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) + OS-level .inuse files forbid t.Parallel
func TestSnapshotRowStyle_SelfPriorityWhenBoth(t *testing.T) {
	m, _ := newTestModel(t) // sets XDG_DATA_HOME to a temp dir via t.Setenv

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// Both this process AND a foreign live process (os.Getppid) hold the SAME file.
	require.NoError(t, snapshot.SetOwnInUse(filepath.Join(dir, "shared"+snapshot.FileExt)))
	t.Cleanup(func() { _ = snapshot.SetOwnInUse("") }) // clear after the test
	writeForeignHolder(t, "shared"+snapshot.FileExt)

	rowStyle := m.newSnapshotRowStyler()
	require.NotNil(t, rowStyle)
	assert.Equal(t, uv.Style{Fg: m.bundle.Config.Style.SuccessColor.Color}, rowStyle("shared"+snapshot.FileExt),
		"self holding the snapshot must win (green) even when others also hold it")
}

// TestSnapshotRowStyle_ColorsAreDistinct guards the mapping against a palette
// where success/warning collapse into each other or into "unstyled" — without
// it the three assertions above could all pass on a degenerate theme.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME via newTestModel) forbids t.Parallel
func TestSnapshotRowStyle_ColorsAreDistinct(t *testing.T) {
	m, _ := newTestModel(t)
	style := m.bundle.Config.Style
	assert.NotNil(t, style.SuccessColor.Color, "success color must be set")
	assert.NotNil(t, style.WarningColor.Color, "warning color must be set")
	assert.NotEqual(t, style.SuccessColor.Color, style.WarningColor.Color,
		"own vs locked rows must be visually distinguishable")
}

// Compile-time assertion: snapshot.FileExt is used to keep the import live.
var _ = filepath.Join("a", snapshot.FileExt)
