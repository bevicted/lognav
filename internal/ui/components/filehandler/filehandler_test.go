package filehandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/component/componenttest"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// fakePoster is a test double for msgs.Poster. Go runs fn inline (no real
// goroutine, so goleak stays clean) and PostCritical records every delivered
// event in order for later assertions. Mirrors the instancepicker fakePoster.
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

// events returns a copy of all events posted via PostCritical, in order.
func (f *fakePoster) events() []uv.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uv.Event(nil), f.posted...)
}

// readCapture records the last path dispatched to the ReadF stub.
type readCapture struct {
	Path string
}

// writeCapture records whether the WriteF stub was called.
type writeCapture struct {
	Called bool
}

// newTestModel returns a *Model wired with an inline fakePoster (so IO methods
// run synchronously and goleak stays clean), ReadF/WriteF stubs, and the
// capture structs that record what they were called with. The fakePoster
// records every event the IO methods post via PostCritical.
func newTestModel(t *testing.T, dir string) (*Model, *fakePoster, *readCapture, *writeCapture) {
	t.Helper()
	bundle := depstest.NewTest(t)

	rc := &readCapture{}
	wc := &writeCapture{}

	// ReadF stub: records the path and returns a typed fakeReadMsg or nil.
	// Per spec D14: stubs return typed msg or nil; never panic.
	readF := func(r io.Reader, path string) uv.Event {
		if r != nil {
			_, _ = io.ReadAll(r)
		}
		rc.Path = path
		return readResultMsg{path: path}
	}

	// WriteF stub: records the call and returns nil.
	writeF := func(w io.Writer) uv.Event {
		wc.Called = true
		if w != nil {
			_, _ = w.Write([]byte("written"))
		}
		return writeResultMsg{}
	}

	opts := FileOpts{
		Dir: dir,
		Ext: ".dat",
	}
	m := New(bundle, opts, readF, writeF)
	fp := &fakePoster{}
	m.SetPoster(fp)
	return m, fp, rc, wc
}

// readResultMsg is the typed read-result the ReadF stub returns; it stands in
// for a real cross-component read-result (e.g. queryeditor's QueryLoadMsg) so
// tests can assert the ReadFile ordering (read-result posted before ReadOP).
type readResultMsg struct{ path string }

// writeResultMsg is the typed write-result the WriteF stub returns.
type writeResultMsg struct{}

// firstIOMsg returns the first IOMsg in events with the given op (and true), or
// the zero IOMsg (and false) if none match.
func firstIOMsg(events []uv.Event, op Operation) (IOMsg, bool) {
	for _, ev := range events {
		if io, ok := ev.(IOMsg); ok && io.op == op {
			return io, true
		}
	}
	return IOMsg{}, false
}

func TestNew_BindsKeyhandlersAndList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	require.NotNil(t, m)
	assert.NotNil(t, m.list)
	assert.NotNil(t, m.kh)
}

func TestInit_TriggersListFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	m.Init()
	// ListFiles ran inline on the fake poster and posted a ListOP IOMsg.
	_, ok := firstIOMsg(fp.events(), ListOP)
	assert.True(t, ok, "Init must post a ListOP IOMsg via the poster")
}

func TestListFiles_PostsListMsg_PopulatedFromTempDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"alpha.dat", "beta.dat", "gamma.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)
	m.ListFiles()
	ioMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok, "ListFiles must post a ListOP IOMsg")
	assert.Equal(t, m.id, ioMsg.id, "ListOP IOMsg must carry the model's id")
	assert.Len(t, ioMsg.files, 3)
}

// TestPreview_NoPreviewFn_FallsBackToRawContent guards the regression where the
// raw-content fallback (taken when FileOpts.PreviewFn is nil — the query file
// browser's case) wrote msg.payload instead of msg.preview, so OnFileIO stored a
// blank preview and the preview pane showed nothing.
func TestPreview_NoPreviewFn_FallsBackToRawContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alpha.dat"), []byte("line one\nline two"), 0o600))

	m, fp, _, _ := newTestModel(t, dir) // newTestModel sets no PreviewFn
	m.preview("alpha")

	pv, ok := firstIOMsg(fp.events(), Preview)
	require.True(t, ok, "preview must post a Preview IOMsg")
	require.NotNil(t, pv.preview, "raw fallback must populate msg.preview (not msg.payload)")
	require.Len(t, pv.preview, 2, "two content lines -> two preview lines")
	assert.Equal(t, "line one", pv.preview[0][0].Text)
	assert.Equal(t, "line two", pv.preview[1][0].Text)

	// End-to-end: OnFileIO stores the preview under the filename key Draw looks up.
	m.OnFileIO(pv)
	assert.Equal(t, pv.preview, m.previews["alpha"])
}

// TestInvalidatePreview_ForcesRecomputeOnNextList is a regression test for the
// stale snapshot-preview bug. A manual save overwrites a snapshot file via
// os.Rename, bypassing the filehandler's own WriteOP that would have dropped the
// cached preview. The cache is basename-keyed with no freshness check, so the
// next ListFiles keeps the stale entry (ListOP only computes previews for
// basenames NOT already cached) and the preview pane shows the PREVIOUS file's
// content until lognav restarts. InvalidatePreview drops the entry (stripping the
// configured extension to match the basename key) so the next list recomputes it.
func TestInvalidatePreview_ForcesRecomputeOnNextList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "alpha.dat")
	require.NoError(t, os.WriteFile(path, []byte("OLD"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)

	// Prime the cache: preview "alpha" -> ["OLD"] (raw-content fallback).
	m.preview("alpha")
	pv, ok := firstIOMsg(fp.events(), Preview)
	require.True(t, ok)
	m.OnFileIO(pv)
	require.Equal(t, "OLD", m.previews["alpha"][0][0].Text)

	// The file is overwritten on disk behind the filehandler's back (manual save).
	require.NoError(t, os.WriteFile(path, []byte("NEW"), 0o600))

	// Drop the stale entry (ext stripped to match the basename cache key).
	m.InvalidatePreview("alpha.dat")
	_, cached := m.previews["alpha"]
	require.False(t, cached, "InvalidatePreview must drop the cached entry")

	// Re-list: the now-missing preview is recomputed from the new file on disk.
	fp2 := &fakePoster{}
	m.SetPoster(fp2)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp2.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg) // ListOP recomputes missing previews inline via the poster

	recompute, ok := firstIOMsg(fp2.events(), Preview)
	require.True(t, ok, "a missing preview must be recomputed on the next list")
	m.OnFileIO(recompute)
	assert.Equal(t, "NEW", m.previews["alpha"][0][0].Text, "preview must reflect the overwritten file")
}

// TestListFiles_ReturnsAllFilesNewestFirst verifies generic file browsing
// lists every matching file without applying snapshot retention.
func TestListFiles_ReturnsAllFilesNewestFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create 12 .dat files with distinct mtimes ordered oldest→newest.
	for i := range 12 {
		name := filepath.Join(dir, fmt.Sprintf("f%d.dat", i))
		require.NoError(t, os.WriteFile(name, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(name, time.Time{}, time.Unix(int64(i*100), 0)))
	}
	m, _, _, _ := newTestModel(t, dir)
	files, err := m.getFiles()
	require.NoError(t, err)
	assert.Len(t, files, 12)
	// Confirm files are sorted newest-first (ModTime descending).
	for i := range len(files) - 1 {
		assert.False(t, files[i].ModTime().Before(files[i+1].ModTime()),
			"files must be sorted newest-first")
	}
}

// TestListFiles_SkipsWipMarker confirms that files named <name>.dat.wip are
// excluded from getFiles. The WipMarker suffix is appended after the ext.
func TestListFiles_SkipsWipMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "valid.dat"), []byte("x"), 0o600))
	// WIP temp file: the wip suffix is appended after the ext (.dat.wip).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft.dat"+WipMarker), []byte("x"), 0o600))

	m, _, _, _ := newTestModel(t, dir)
	files, err := m.getFiles()
	require.NoError(t, err)
	assert.Len(t, files, 1, "only the valid .dat file should appear")
	for _, f := range files {
		assert.NotContains(t, f.Name(), WipMarker, "WIP-marked files must be skipped")
	}
}

// TestListFiles_KeepsTempNamedFiles verifies generic file browsing does not
// apply snapshot-specific retention rules.
func TestListFiles_KeepsTempNamedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "valid.dat"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "snapshot.tmp.dat"), []byte("x"), 0o600))

	m, _, _, _ := newTestModel(t, dir)
	files, err := m.getFiles()
	require.NoError(t, err)
	assert.Len(t, files, 2, "generic file browser must retain temp-named files")
}

func TestReadFile_DispatchesReadF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "x.dat")
	require.NoError(t, os.WriteFile(p, []byte("body"), 0o600))

	m, _, rc, _ := newTestModel(t, dir)
	// ReadFile runs inline on the fake poster and invokes the ReadF stub.
	m.ReadFile("x.dat")
	assert.Equal(t, p, rc.Path)
}

func TestReadFileUnderCursor_DispatchesReadF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("body"), 0o600))

	m, fp, rc, _ := newTestModel(t, dir)
	// Populate the list by running ListFiles and processing the posted IOMsg.
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	m.ReadFileUnderCursor()
	assert.NotEmpty(t, rc.Path)
}

// TestSelectByNameAfterRefresh_LandsCursorOnRowOnceListed proves the deferred
// name selection: SelectByNameAfterRefresh requests a refresh and the cursor is
// moved onto the named row only when the resulting ListOP populates the list (the
// list is empty until OnFileIO applies the ListOP). The name is the ext-stripped
// basename, matching how rows are stored.
func TestSelectByNameAfterRefresh_LandsCursorOnRowOnceListed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, n := range []string{"aaa.dat", "bbb.dat", "ccc.dat"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600))
	}
	m, fp, _, _ := newTestModel(t, dir)

	// Request a deferred selection of the (not-yet-listed) "bbb" row.
	m.SelectByNameAfterRefresh("bbb")

	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok, "SelectByNameAfterRefresh triggers a ListOP refresh")
	m.OnFileIO(listMsg) // applies the rows AND consumes the pending selection

	assert.Equal(t, "bbb", m.GetItemUnderCursor(),
		"the cursor lands on the requested row once the refresh populates the list")
	assert.Empty(t, m.pendingSelect, "the pending selection is consumed once")
}

func TestWriteFile_AtomicRenameOnSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)

	// WriteFile runs inline on the fake poster: writeAtomic creates and renames
	// the temp file.
	m.WriteFile("new.dat")
	// The final .dat file must exist after the atomic rename.
	final := filepath.Join(dir, "new.dat")
	_, err := os.Stat(final)
	assert.NoError(t, err, "WriteFile must complete the atomic rename")
}

// TestWriteFile_NoTempFilesAfterCompletion verifies that the writeAtomic temp
// file (pattern: .write-*.tmp) does not linger after a write error.
// The writeF stub closes the writer but returns nil — we cause a Rename
// failure by making the dir read-only after temp-file creation. This is
// tricky to arrange portably, so instead we verify the happy-path cleanup:
// after a successful WriteFile, no .write-*.tmp files remain.
func TestWriteFile_NoTempFilesAfterCompletion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)

	m.WriteFile("check.dat")

	// After completion (success or failure), no writeAtomic temp files should linger.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".write-", "writeAtomic temp file must be cleaned up")
	}
}

func TestWriteFileUnderCursor_DispatchesWriteF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))

	m, fp, _, wc := newTestModel(t, dir)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	m.WriteFileUnderCursor()
	assert.True(t, wc.Called, "WriteF stub must be invoked by WriteFileUnderCursor")
}

func TestDeleteFile_RemovesFromDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "doomed.dat")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))

	m, _, _, _ := newTestModel(t, dir)
	m.DeleteFile("doomed.dat") // void: runs inline on the fake poster

	_, err := os.Stat(p)
	assert.True(t, os.IsNotExist(err), "DeleteFile must remove the file from disk")
}

func TestDeleteFileUnderCursor_RemovesAndRefreshes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "x.dat")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	m.DeleteFileUnderCursor()
	// File must be gone after the delete runs inline on the fake poster.
	_, err := os.Stat(p)
	assert.True(t, os.IsNotExist(err), "DeleteFileUnderCursor must remove the file")
}

func TestRenameFile_MovesOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.dat"), []byte("x"), 0o600))

	m, _, _, _ := newTestModel(t, dir)
	m.RenameFile("old.dat", "new.dat") // void: runs inline on the fake poster

	_, err := os.Stat(filepath.Join(dir, "new.dat"))
	require.NoError(t, err, "new file must exist after rename")
	_, err = os.Stat(filepath.Join(dir, "old.dat"))
	assert.True(t, os.IsNotExist(err), "old file must be gone after rename")
}

// TestGetFiles_StableOnEqualModTimes pins the sort's stability: files sharing a
// ModTime must come back in the order readdir returned them, so a
// coarse-timestamp filesystem cannot reshuffle rows between listings.
func TestGetFiles_StableOnEqualModTimes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Two mtime buckets, interleaved and large enough that an unstable sort
	// really partitions instead of falling back to its small-input insertion
	// sort. Within a bucket every entry ties.
	newest := map[string]bool{}
	for i := range 64 {
		name := fmt.Sprintf("f%d.dat", i)
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		mtime := time.Unix(1000, 0)
		if i%2 == 0 {
			mtime = time.Unix(2000, 0)
			newest[name] = true
		}
		require.NoError(t, os.Chtimes(p, time.Time{}, mtime))
	}
	m, _, _, _ := newTestModel(t, dir)

	// Baseline: raw readdir order of the same (unmodified) directory, newer
	// bucket first — each bucket must keep its readdir order.
	d, err := os.Open(dir) //nolint:gosec // dir is t.TempDir(), not user input
	require.NoError(t, err)
	names, err := d.Readdirnames(-1)
	require.NoError(t, d.Close())
	require.NoError(t, err)
	var want []string
	for _, n := range names {
		if newest[n] {
			want = append(want, n)
		}
	}
	for _, n := range names {
		if !newest[n] {
			want = append(want, n)
		}
	}

	files, err := m.getFiles()
	require.NoError(t, err)
	got := make([]string, len(files))
	for i, f := range files {
		got[i] = f.Name()
	}
	assert.Equal(t, want, got, "equal ModTimes must keep readdir order")
}

func TestIsFocused_TogglesViaListFocus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	// Initial state: list not focused.
	assert.False(t, m.IsFocused())
	// Focus the list.
	m.list.Focus()
	assert.True(t, m.IsFocused(), "IsFocused must reflect list focus state")
}

func TestGetItemUnderCursor_ReturnsCurrentName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "only.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	// Populate the list so there is an item under the cursor.
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)

	item := m.GetItemUnderCursor()
	assert.NotEmpty(t, item, "GetItemUnderCursor must return the item name when list is populated")
}

func TestSetRect_PropagatesToList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	want := component.Rect{X: 0, Y: 0, W: 80, H: 24}
	m.SetRect(want)
	assert.Equal(t, want, m.drawRect, "SetRect must store the rect in drawRect")
}

func TestModel_Draw_DoesNotPanicAcrossRectSizes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _, _, _ := newTestModel(t, dir)
	componenttest.DrawMatrix(t, m.SetRect, m.Draw)
}

// TestUpdate_IOMsg_WriteOP_RefreshesList verifies that receiving an IOMsg for a
// completed WriteOP triggers a ListFiles refresh. ListFiles is now void (it
// spawns a tracked poster goroutine), so the refresh manifests as a fresh
// ListOP IOMsg posted via the poster rather than a returned tea.Cmd.
// The IOMsg must carry the model's own id so the Update handler processes it.
func TestUpdate_IOMsg_WriteOP_RefreshesList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fp, _, _ := newTestModel(t, dir)

	// Construct an IOMsg that simulates a completed write for this model.
	// We're in package filehandler so unexported fields are accessible.
	msg := IOMsg{
		id:    m.id,
		op:    WriteOP,
		files: []string{"x.dat"},
	}

	m.OnFileIO(msg)
	_, ok := firstIOMsg(fp.events(), ListOP)
	assert.True(t, ok, "a WriteOP IOMsg must trigger a ListFiles refresh (ListOP posted via poster)")
}

// TestWriteFile_PostsResultThenWriteOP guards the WriteFile ordering invariant:
// writeAtomic runs first (file on disk), then the write-result is posted, then
// the terminal WriteOP IOMsg (which drives the ListFiles refresh) is posted —
// so the refresh only runs after the file exists on disk.
func TestWriteFile_PostsResultThenWriteOP(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fp, _, _ := newTestModel(t, dir)

	m.WriteFile("new.dat")

	// writeAtomic must have run (file on disk) before the terminal post.
	_, statErr := os.Stat(filepath.Join(dir, "new.dat"))
	require.NoError(t, statErr, "writeAtomic must run before the WriteOP IOMsg is posted")

	events := fp.events()
	require.Len(t, events, 2, "WriteFile must post exactly [write-result, WriteOP]")
	_, ok := events[0].(writeResultMsg)
	assert.True(t, ok, "first post must be the write-result")
	io, ok := events[1].(IOMsg)
	require.True(t, ok, "second post must be the terminal WriteOP IOMsg")
	assert.Equal(t, WriteOP, io.op)
	assert.Equal(t, m.id, io.id, "terminal WriteOP IOMsg must carry the model's id")
}

// TestReadFile_PostsResultThenReadOP_LoadingSetOnLoop guards the ReadFile
// ordering invariant: m.loading is set on-loop (synchronously, before the
// goroutine returns) and stays set until the read completes; the read-result is
// posted first, then the terminal ReadOP IOMsg (which clears loading). With the
// inline fake poster the goroutine runs synchronously, so after the call loading
// is back to false and the posts are in order.
func TestReadFile_PostsResultThenReadOP_LoadingSetOnLoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "x.dat")
	require.NoError(t, os.WriteFile(p, []byte("body"), 0o600))

	m, fp, rc, _ := newTestModel(t, dir)
	m.ReadFile("x.dat")

	require.Equal(t, p, rc.Path, "ReadF must have run")

	events := fp.events()
	require.Len(t, events, 2, "ReadFile must post exactly [read-result, ReadOP]")
	res, ok := events[0].(readResultMsg)
	require.True(t, ok, "first post must be the read-result (no id; cross-component)")
	assert.Equal(t, p, res.path)
	io, ok := events[1].(IOMsg)
	require.True(t, ok, "second post must be the terminal ReadOP IOMsg")
	assert.Equal(t, ReadOP, io.op)
	assert.Equal(t, m.id, io.id, "terminal ReadOP IOMsg must carry the model's id")

	// The terminal ReadOP, fed through Update, clears m.loading.
	require.True(t, m.loading, "loading stays set until ReadOP is processed")
	m.OnFileIO(io)
	assert.False(t, m.loading, "ReadOP IOMsg must clear m.loading")
}

// TestReadFile_GetReaderError_ClearsLoading guards the getReader-error path of
// ReadFile (load-bearing "always clear loading" behavior): reading a
// NONEXISTENT file makes getReader fail, so ReadFile posts the ioErr IOMsg
// (carrying the read error and the model's id) followed by the terminal ReadOP
// IOMsg — and feeding that terminal IOMsg through Update clears m.loading. The
// read-result is NOT posted on this path (the ReadF stub never runs).
func TestReadFile_GetReaderError_ClearsLoading(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	m, fp, rc, _ := newTestModel(t, dir)
	// "missing.dat" does not exist, so getReader (os.OpenFile) returns an error.
	m.ReadFile("missing.dat")

	assert.Empty(t, rc.Path, "ReadF stub must NOT run on the getReader-error path")

	events := fp.events()
	require.Len(t, events, 2, "getReader-error path must post exactly [ioErr, ReadOP]")

	ioErr, ok := events[0].(IOMsg)
	require.True(t, ok, "first post must be the ioErr IOMsg")
	require.Error(t, ioErr.err, "ioErr IOMsg must carry the read error")
	assert.Equal(t, m.id, ioErr.id, "ioErr IOMsg must carry the model's id")

	term, ok := events[1].(IOMsg)
	require.True(t, ok, "second post must be the terminal ReadOP IOMsg")
	assert.Equal(t, ReadOP, term.op)
	assert.Equal(t, m.id, term.id, "terminal ReadOP IOMsg must carry the model's id")

	// The terminal ReadOP, fed through Update, clears m.loading even on the error
	// path (loading must never stay stuck true).
	require.True(t, m.loading, "loading stays set until ReadOP is processed")
	m.OnFileIO(term)
	assert.False(t, m.loading, "ReadOP IOMsg must clear m.loading on the error path")
}

// TestUpdate_IOMsg_ForeignID_Ignored proves the uuid-epoch filter: an IOMsg
// carrying a different id is dropped by Update (no state change, no refresh).
func TestUpdate_IOMsg_ForeignID_Ignored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, fp, _, _ := newTestModel(t, dir)

	// A ReadOP IOMsg with a foreign id must not clear loading or do anything.
	m.loading = true
	foreign := IOMsg{id: m.id + 1, op: ReadOP, files: []string{"x.dat"}}
	m.OnFileIO(foreign)
	assert.True(t, m.loading, "foreign-id ReadOP must be ignored (loading stays set)")

	// A WriteOP IOMsg with a foreign id must not trigger a ListFiles refresh.
	before := len(fp.events())
	foreignWrite := IOMsg{id: m.id + 1, op: WriteOP, files: []string{"x.dat"}}
	m.OnFileIO(foreignWrite)
	assert.Len(t, fp.events(), before, "foreign-id WriteOP must not post a refresh")
}

// cellContent returns the Content string of the cell at (x, y), or "" if nil.
func cellContent(canvas uv.ScreenBuffer, x, y int) string {
	if c := canvas.CellAt(x, y); c != nil {
		return c.Content
	}
	return ""
}

// TestDraw_CellNative_SeparatorAndPreviewText asserts that Draw places the │
// separator glyph at the boundary column (r.X + listW) for every row of the
// component, and that preview text begins one column to the right of it.
func TestDraw_CellNative_SeparatorAndPreviewText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alpha.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	// Set a preview for "alpha" so we can assert text cells.
	m.previews["alpha"] = [][]list.Segment{list.PlainItem("hello")}

	// Populate the list so GetItemUnderCursor returns "alpha".
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.Equal(t, "alpha", m.GetItemUnderCursor())

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	listW := rect.W / 2 // 20
	sepCol := rect.X + listW

	// The left edge of prevRect is at sepCol. DrawBox({Left:"│"}) places │ at
	// column sepCol for every row in [rect.Y, rect.Y+rect.H).
	for row := rect.Y; row < rect.Y+rect.H; row++ {
		assert.Equal(t, "│", cellContent(canvas, sepCol, row),
			"separator must be │ at col %d row %d", sepCol, row)
	}

	// Preview text "hello" starts at inner.X = sepCol+1 (after Pad left:1),
	// row rect.Y+0 = 0.
	innerX := sepCol + 1
	assert.Equal(t, "h", cellContent(canvas, innerX, rect.Y), "preview text first char")
	assert.Equal(t, "e", cellContent(canvas, innerX+1, rect.Y), "preview text second char")
}

// TestDraw_PreviewStyledSegment asserts that a styled preview segment renders
// cell-native: the segment's text appears as plain glyphs (no escape bytes) and
// the segment's uv.Style is applied to those cells. This is how snapshot
// previews color instance names (snapshothandler/preview.go builds segments via
// instancepicker.InstanceRowSegments).
func TestDraw_PreviewStyledSegment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alpha.dat"), []byte("x"), 0o600))

	m, fp, _, _ := newTestModel(t, dir)
	// A single bold segment "US" — stands in for a state-styled instance name.
	m.previews["alpha"] = [][]list.Segment{{{Text: "US", Style: uv.Style{Attrs: uv.AttrBold}}}}

	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.Equal(t, "alpha", m.GetItemUnderCursor())

	rect := component.Rect{X: 0, Y: 0, W: 40, H: 10}
	m.SetRect(rect)
	canvas := uv.NewScreenBuffer(rect.W, rect.H)
	_ = m.Draw(canvas)

	innerX := rect.X + rect.W/2 + 1 // sepCol + 1
	// The visible glyphs are the segment text, drawn cell-by-cell.
	assert.Equal(t, "U", cellContent(canvas, innerX, rect.Y))
	assert.Equal(t, "S", cellContent(canvas, innerX+1, rect.Y))
	// And the segment's style is applied to the cells (not dropped).
	assert.NotZero(t, canvas.CellAt(innerX, rect.Y).Style.Attrs&uv.AttrBold,
		"segment style must be applied to its cells")
}

//nolint:paralleltest // depstest seeds a global state seam; setup precedes any t.Parallel.
func TestDeleteFileUnderCursor_RefusedWhenOnDeleteErrors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "m_held.dat"), []byte("x"), 0o600))
	bundle := depstest.NewTest(t)
	onDelete := func(_ string) error { return errors.New("in use by another lognav") }
	m := New(bundle, FileOpts{Dir: dir, Ext: ".dat", OnDelete: onDelete}, nil, nil)
	fp := &fakePoster{}
	m.SetPoster(fp)
	m.list.WithItems([][]list.Segment{list.PlainItem("m_held")}) // one item under cursor

	m.DeleteFileUnderCursor()

	require.Never(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "m_held.dat"))
		return os.IsNotExist(err)
	}, 100*time.Millisecond, 20*time.Millisecond) // file must survive (delete refused)

	// The refusal posts a ShowDialogMsg (synchronously, since the fake poster runs
	// Go inline). Assert it was recorded.
	var found bool
	for _, ev := range fp.events() {
		if dlg, ok := ev.(msgs.ShowDialogMsg); ok && dlg.Title == "Cannot delete" {
			found = true
			break
		}
	}
	assert.True(t, found, "refused delete must post a 'Cannot delete' ShowDialogMsg")
}

// TestKeybinds_ConfirmInvokesReadFileUnderCursor exercises the confirm/accept
// keybinding by sending the default accept key (Enter) to Update and
// verifying that ReadF was actually invoked with a non-empty path.
func TestKeybinds_ConfirmInvokesReadFileUnderCursor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.dat"), []byte("x"), 0o600))

	m, fp, rc, _ := newTestModel(t, dir)
	// Populate the list so the cursor points at a file.
	m.ListFiles()
	listMsg, ok := firstIOMsg(fp.events(), ListOP)
	require.True(t, ok)
	m.OnFileIO(listMsg)
	require.NotEmpty(t, m.GetItemUnderCursor(), "list must be non-empty before driving Enter")

	// Drive the confirm (accept) keybinding via Enter key press through
	// HandleKey (the root routes keys via tabs.HandleKey post-R2 cutover).
	// ReadFileUnderCursor is void (runs ReadFile inline on the fake poster); ReadF
	// is invoked synchronously.
	m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	assert.NotEmpty(t, rc.Path, "confirm keybind must invoke ReadF with the file path under the cursor")
}
