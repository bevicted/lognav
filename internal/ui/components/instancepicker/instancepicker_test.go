package instancepicker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

// fakePoster is a test double for msgs.Poster. Go runs fn inline (no real
// goroutine, so goleak stays clean), and PostCritical records every delivered
// event for later assertions.
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

// newModelWithBackingFile constructs a *Model with a real backing container
// opened on a temp file inside t.TempDir(). It returns the model and the
// path to the wip backing file so tests can re-open it after Close.
// attachBackingFile gives m an open .wip backing container (as startFetch would),
// so maybeFinalizeFetch can take its finalize branch. Returns the .wip path.
func attachBackingFile(t *testing.T, m *Model) string {
	t.Helper()
	dir := t.TempDir()
	f, err := snapshot.CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	m.backingContainer = snapshot.NewWriter(f)
	m.backingFile = f
	m.setBackingPath(f.Name())
	m.fetching = true
	m.queryStartTime = time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	return f.Name()
}

func autoSnapshotPath(dir string, startedAt time.Time) string {
	return filepath.Join(dir, "auto-"+startedAt.Format("20060102-150405")+snapshot.FileExt)
}

func newModelWithBackingFile(t *testing.T) (*Model, string) {
	t.Helper()
	m := newTestModel(t, &fakePoster{})
	return m, attachBackingFile(t, m)
}

func TestModel_Close_FlushesBackingFile(t *testing.T) {
	t.Parallel()
	m, wipPath := newModelWithBackingFile(t)

	require.NoError(t, m.Close())
	assert.Nil(t, m.backingFile)
	assert.Nil(t, m.backingContainer)
	assert.False(t, m.fetching)

	// File on disk must be readable as a snapshot container — i.e. the
	// container was finalized cleanly (footer written, no truncation).
	c, err := snapshot.OpenContainerReadOnly(wipPath)
	require.NoError(t, err)
	require.NoError(t, c.Close())

	t.Run("second_call_is_noop", func(t *testing.T) {
		require.NoError(t, m.Close())
		assert.Nil(t, m.backingFile)
	})
}

func TestModel_Close_IteratesAllInstances(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := newTestModel(t, &fakePoster{})

	inst1 := NewInstance(bundle, "inst-1", "url-1", testCRN("inst-1"), icl.EnvProd, "%.2fs")
	inst2 := NewInstance(bundle, "inst-2", "url-2", testCRN("inst-2"), icl.EnvProd, "%.2fs")
	inst1.cancelQuery = func() {}
	inst2.cancelQuery = func() {}
	m.instances = Instances{inst1, inst2}

	require.NoError(t, m.Close())
	assert.Nil(t, inst1.cancelQuery, "inst1.cancelQuery should be nil after Close (proves Close reached it)")
	assert.Nil(t, inst2.cancelQuery, "inst2.cancelQuery should be nil after Close (proves Close reached it)")

	t.Run("second_call_is_noop", func(t *testing.T) {
		require.NoError(t, m.Close())
	})
}

// TestModel_Flush_PostsFlushedViaPoster proves the R4 conversion of the async
// instance-log flush from a tea.Cmd closure to a poster.Go spawn: when a
// LogStreamDoneMsg arrives for an instance with a backing container and buffered
// logs, the on-loop prologue runs (pendingFlushes incremented) and the flush
// result is delivered as an InstanceFlushedMsg via the poster (not via cmds).
func TestModel_Flush_PostsFlushedViaPoster(t *testing.T) {
	t.Parallel()
	m, _ := newModelWithBackingFile(t)
	m.bundle = depstest.NewTest(t) // Update -> syncInstanceState needs bundle.State

	const instName = "inst-flush"
	inst := newTestInstance(t, instName)
	inst.state = status.InProgress
	// Buffer logs so GetLogCount() > 0 triggers the flush branch.
	inst.Store.SetLogs([]icl.Log{
		{Data: map[string]any{"msg": "a"}, Metadata: icl.Metadata{TSMicro: 1}},
		{Data: map[string]any{"msg": "b"}, Metadata: icl.Metadata{TSMicro: 2}},
	})
	require.Positive(t, inst.Store.GetLogCount(), "logs must be buffered to exercise the flush branch")
	m.instances = Instances{inst}

	f := &fakePoster{}
	m.SetPoster(f)

	m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: inst.CRN})

	// (a) on-loop prologue ran on the loop goroutine.
	assert.Equal(t, 1, m.pendingFlushes, "pendingFlushes must be incremented on-loop")
	assert.Equal(t, 2, inst.flushedLogCount, "flushedLogCount must record the snapshot size")
	assert.Equal(t, 0, inst.Store.GetLogCount(), "store data must be cleared after snapshot")

	// (b) the flush result was delivered via the poster as an InstanceFlushedMsg.
	var found *InstanceFlushedMsg
	for _, ev := range f.events() {
		if fm, ok := ev.(InstanceFlushedMsg); ok && fm.crn == inst.CRN {
			found = &fm
			break
		}
	}
	require.NotNil(t, found, "an InstanceFlushedMsg for the instance must be posted via the poster")
	require.NoError(t, found.err, "compression should succeed")
	assert.NotEmpty(t, found.compressed, "compressed frame must be non-empty")
}

// TestModel_CancelAllFetches_PreservesAlreadyFlushedLogs is a regression test for
// the multi-instance cancel bug: when one instance has already finished and
// flushed its logs into the shared backing container, cancelling the remaining
// in-flight fetch(es) must NOT discard those logs. The cancel keybind used to
// call closeBackingContainer() directly, which cleared backingPath (so OpenInstance
// returned ErrNoBackingFile) and nilled backingContainer (so the cancelled query's
// LogStreamDoneMsg could no longer finalize the .wip). The finished instance then
// showed the empty "forgot to fetch" hint with its logs stranded in an orphaned
// .wip. cancelAllFetches must only cancel the queries and let the normal
// LogStreamDoneMsg -> maybeFinalizeFetch path finalize the container, keeping the
// flushed instance loadable.
func TestModel_CancelAllFetches_PreservesAlreadyFlushedLogs(t *testing.T) {
	t.Parallel()
	m, _ := newModelWithBackingFile(t)
	bundle := depstest.NewTest(t)
	m.bundle = bundle         // syncInstanceState / snapshotMeta need bundle.State
	m.list = list.New(bundle) // finalizeBackingContainer -> snapshotMeta -> m.list.GetCursor()

	// au-syd: finished, logs already flushed to the backing container (frame on
	// disk), in-memory store cleared. Mirrors OnLogStreamDone + OnInstanceFlushed.
	const flushedName = "au-syd"
	auLogs := []icl.Log{
		{Data: map[string]any{msgKey: "a"}, Metadata: icl.Metadata{ID: "1"}},
		{Data: map[string]any{msgKey: "b"}, Metadata: icl.Metadata{ID: "2"}},
	}
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, testCRN(flushedName), auLogs))
	au := newTestInstance(t, flushedName)
	au.state = status.Success
	au.flushedLogCount = len(auLogs)
	au.flushedLogsSize = 1

	// us-east: still in flight when the user cancels.
	const cancelName = "us-east"
	us := newTestInstance(t, cancelName)
	us.state = status.InProgress
	us.cancelQuery = func() {} // so CancelQuery transitions it InProgress -> Cancelled

	m.instances = Instances{au, us}

	f := &fakePoster{}
	m.SetPoster(f) // after m.instances so every store shares the inline poster

	// User presses CancelAllFetches, then the cancelled query's stream-done signal
	// arrives (the real query goroutine returns context.Canceled and posts it).
	m.cancelAllFetches()
	m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: us.CRN})

	// The container must have been finalized (not discarded): backingPath points at
	// a real, readable file, so the flushed instance stays reachable.
	require.NotEmpty(t, m.GetBackingPath(),
		"cancel must finalize the backing container, not clear backingPath")

	// Opening the finished instance must load its flushed logs, not ErrNoBackingFile.
	m.OpenInstance(au.CRN)

	var ready *InstanceLoadReadyMsg
	for _, ev := range f.events() {
		if r, ok := ev.(InstanceLoadReadyMsg); ok && r.CRN == au.CRN {
			ready = &r
			break
		}
	}
	require.NotNil(t, ready, "OpenInstance must post an InstanceLoadReadyMsg for the flushed instance")
	require.NoError(t, ready.Err, "flushed instance must load, not fail with ErrNoBackingFile")
	assert.Len(t, ready.Logs, len(auLogs), "all flushed logs must be recovered")
}

// TestModel_CancelAllFetches_CancelsInFlightAuth verifies the cancel keybind
// aborts in-flight token resolution (GetAuthToken runs under authCtx) and then
// re-arms authCtx so a later fetch can still resolve tokens. Cancelling the
// shared authCtx without re-arming would poison every future ResolveTokens.
func TestModel_CancelAllFetches_CancelsInFlightAuth(t *testing.T) {
	t.Parallel()
	m := New(t.Context(), depstest.NewTest(t))
	m.SetPoster(&fakePoster{})
	oldAuthCtx := m.authCtx
	require.NoError(t, oldAuthCtx.Err(), "authCtx must be live before cancel")

	m.cancelAllFetches()

	require.ErrorIs(t, oldAuthCtx.Err(), context.Canceled,
		"cancel must abort the in-flight authCtx so a slow GetAuthToken stops")
	require.NotNil(t, m.authCtx)
	require.NoError(t, m.authCtx.Err(),
		"authCtx must be re-armed (live) so a subsequent fetch can resolve tokens")
}

// TestModel_OnEnvAuthCancelled_ScopesToEnv verifies that an env-cancel message
// settles only that env's auth-pending members to Cancelled, leaving the other
// env untouched (the global on-loop cancel in cancelAllFetches is what flips all
// AuthInProgress members; the async EnvAuthCancelledMsg is per-env and
// idempotent).
func TestModel_OnEnvAuthCancelled_ScopesToEnv(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, &fakePoster{})
	m.bundle = depstest.NewTest(t)
	m.list = list.New(m.bundle)

	a := newTestInstanceEnv(t, "au-syd", icl.EnvProd)
	a.state = status.AuthInProgress
	b := newTestInstanceEnv(t, "us-east", icl.Environment("test-cloud"))
	b.state = status.AuthInProgress
	m.instances = Instances{a, b}

	m.OnEnvAuthCancelled(EnvAuthCancelledMsg{Env: icl.EnvProd})

	assert.Equal(t, status.Cancelled, a.state, "prod env auth-pending member cancelled")
	assert.Equal(t, status.AuthInProgress, b.state, "staging untouched")
}

func TestModel_Close_PropagatesBackingErrors(t *testing.T) {
	t.Parallel()
	m, _ := newModelWithBackingFile(t)

	// Pre-close the file behind the model's back so subsequent Sync/Close
	// from inside closeBackingContainer fail with os.ErrClosed.
	require.NoError(t, m.backingFile.Close())

	err := m.Close()
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrClosed, "Close should propagate the file-closed error from Sync or Close")
}

// TestModel_IsAnimating verifies the picker animation predicate: true while any
// instance query is InProgress, false once all queries are done. This is the
// predicate the spine's shared ticker reads to start/stop redraws.
func TestModel_IsAnimating(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m := newTestModel(t, &fakePoster{})

	inst := NewInstance(bundle, "anim", "url", "crn", icl.EnvProd, "%.2fs")
	m.instances = Instances{inst}

	inst.state = status.InProgress
	assert.True(t, m.IsAnimating(), "in-progress query => animating")

	inst.state = status.Success
	assert.False(t, m.IsAnimating(), "no in-progress query => not animating")
}

// TestModel_CancelQueries_CancelsAuthContext verifies CancelQueries cancels the
// auth context so an in-flight GetAuthToken (token resolution or single-instance
// retry) aborts at teardown. Without this, auth rooted in the signal context
// survives a `q`-quit and blocks the runtime's poster Wait for the full timeout.
func TestModel_CancelQueries_CancelsAuthContext(t *testing.T) {
	t.Parallel()
	m := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, m.authCtx.Err(), "authCtx must be live after construction")

	m.CancelQueries()
	assert.ErrorIs(t, m.authCtx.Err(), context.Canceled,
		"CancelQueries must cancel authCtx so in-flight GetAuthToken aborts at teardown")
}

// TestModel_Close_CancelsAuthContext verifies Close also cancels the auth context
// (the backstop path when Close is called without a preceding CancelQueries) and
// tolerates being called more than once.
func TestModel_Close_CancelsAuthContext(t *testing.T) {
	t.Parallel()
	m := New(t.Context(), depstest.NewTest(t))
	require.NoError(t, m.authCtx.Err(), "authCtx must be live after construction")

	require.NoError(t, m.Close())
	require.ErrorIs(t, m.authCtx.Err(), context.Canceled, "Close must cancel authCtx")
	require.NoError(t, m.Close(), "Close must be idempotent")
}

// authStubManager builds an AccountManager wired to a stub IAM token endpoint.
// Configure a refresh-token env and OIDC override so GetAuthToken takes the
// refresh-grant branch. Failures return a non-IAM body so GetAuthToken yields a
// plain error rather than requesting a passcode.
func authStubManager(t *testing.T, _ []config.ICLInstanceConfig, successfulRequests int) *icl.AccountManager {
	t.Helper()
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts <= successfulRequests {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	t.Cleanup(srv.Close)
	am := testAccountManager() // no apiKey / opRef
	am.SetRefreshTokens(map[icl.Environment]string{icl.EnvProd: "refresh-tok"})
	am.SetOIDCForTest(icl.EnvProd, srv.URL)
	return am
}

func TestResolveEnvMembers_FirstMemberCredFail_ErrorsEnv(t *testing.T) {
	t.Parallel()
	insts := []config.ICLInstanceConfig{
		{Name: "a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctA:instA::")},
		{Name: "b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctB:instB::")},
	}
	am := authStubManager(t, insts, 0) // both fail
	poster := &fakePoster{}
	Instances{}.resolveEnvMembers(t.Context(), 0, am, icl.EnvProd, []string{insts[0].CRN.String(), insts[1].CRN.String()}, "q", poster)
	require.Len(t, poster.posted, 1)
	_, ok := poster.posted[0].(EnvCredFailedMsg)
	assert.True(t, ok, "got %T", poster.posted[0])
}

func TestResolveEnvMembers_LaterMemberFails_IsolatesMember(t *testing.T) {
	t.Parallel()
	insts := []config.ICLInstanceConfig{
		{Name: "a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctA:instA::")},
		{Name: "b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctB:instB::")},
	}
	am := authStubManager(t, insts, 1) // a ok, b fails
	poster := &fakePoster{}
	Instances{}.resolveEnvMembers(t.Context(), 0, am, icl.EnvProd, []string{insts[0].CRN.String(), insts[1].CRN.String()}, "q", poster)
	require.Len(t, poster.posted, 2)
	_, okA := poster.posted[0].(MemberAuthResolvedMsg)
	failB, okB := poster.posted[1].(MemberAuthFailedMsg)
	assert.True(t, okA, "first should resolve, got %T", poster.posted[0])
	assert.True(t, okB, "second should fail only itself, got %T", poster.posted[1])
	if okB {
		assert.Equal(t, insts[1].CRN.String(), failB.CRN)
	}
}

func TestResolveEnvMembers_PasscodeRequired_OneDialogForEnv(t *testing.T) {
	t.Parallel()
	insts := []config.ICLInstanceConfig{
		{Name: "a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctA:instA::")},
		{Name: "b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctB:instB::")},
	}
	am := testAccountManager() // no creds
	am.SetOIDCForTest(icl.EnvProd, "http://unused.invalid")
	poster := &fakePoster{}
	Instances{}.resolveEnvMembers(t.Context(), 0, am, icl.EnvProd, []string{insts[0].CRN.String(), insts[1].CRN.String()}, "q", poster)
	require.Len(t, poster.posted, 1, "passcode short-circuits the env after one dialog")
	_, ok := poster.posted[0].(PasscodeRequiredMsg)
	assert.True(t, ok, "got %T", poster.posted[0])
}

func TestResolveEnvMembers_ContextCanceled_EmitsEnvCancelled(t *testing.T) {
	t.Parallel()
	insts := []config.ICLInstanceConfig{
		{Name: "a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctA:instA::")},
	}
	am := authStubManager(t, insts, 0) // would otherwise fail with a cred error
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancel so GetAuthToken's HTTP call returns context.Canceled
	poster := &fakePoster{}

	Instances{}.resolveEnvMembers(ctx, 0, am, icl.EnvProd, []string{insts[0].CRN.String()}, "q", poster)

	require.Len(t, poster.posted, 1)
	_, ok := poster.posted[0].(EnvAuthCancelledMsg)
	assert.True(t, ok, "a cancel must emit EnvAuthCancelledMsg (Cancelled, not Error), got %T", poster.posted[0])
}

// TestMaybeFinalizeFetch_SkipsWhileQueryInProgress proves the self-gate: a
// per-member/per-env auth-fail handler may call maybeFinalizeFetch while a
// sibling member is still streaming (InProgress). Finalizing then would rename
// the .wip mid-fetch and orphan the running member's logs, so it must be a
// no-op until AreAllQueriesDone. Asserted on the config-gated SaveSnapshotMsg
// (durable across later tasks that may drop the authStatus emission).
func TestMaybeFinalizeFetch_SkipsWhileQueryInProgress(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Core.SaveSnapshotOnFetchDone = true
	m := New(t.Context(), bundle)
	m.bundle = bundle
	poster := &fakePoster{}
	m.poster = poster
	inst := newTestInstance(t, "a")
	inst.state = status.InProgress // a still streaming
	m.instances = Instances{inst}

	m.maybeFinalizeFetch() // must be a no-op

	for _, ev := range poster.posted {
		_, ok := ev.(msgs.SaveSnapshotMsg)
		assert.False(t, ok, "must not finalize while a query is InProgress")
	}
}

// TestMaybeFinalizeFetch_PostsWhenAllQueriesDone is the positive counterpart:
// once every instance has settled (here all Error), maybeFinalizeFetch posts the
// config-gated SaveSnapshotMsg.
func TestMaybeFinalizeFetch_PostsWhenAllQueriesDone(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	bundle.Config.Core.SaveSnapshotOnFetchDone = true
	m := New(t.Context(), bundle)
	m.bundle = bundle
	m.list = list.New(bundle) // finalizeBackingContainer -> snapshotMeta -> m.list.GetCursor()
	poster := &fakePoster{}
	m.poster = poster
	attachBackingFile(t, m)
	inst := newTestInstance(t, "a")
	inst.state = status.Success // settled (no query running)
	// non-empty: a flushed frame on disk + flushedLogCount so finalize runs (an
	// empty fetch discards the .wip and suppresses the snapshot).
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, inst.CRN, []icl.Log{
		{Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"}},
	}))
	inst.flushedLogCount = 1
	inst.flushedLogsSize = 1
	m.instances = Instances{inst}

	m.maybeFinalizeFetch()

	var found bool
	for _, ev := range poster.posted {
		if _, ok := ev.(msgs.SaveSnapshotMsg); ok {
			found = true
		}
	}
	assert.True(t, found, "all queries done with logs -> SaveSnapshotMsg must be posted")
}

// TestMaybeFinalizeFetch_RetainsBeforeDirtyNotification proves normal-fetch
// retention completes on the finalization path before that path returns and
// signals the snapshot browser.
//
//nolint:paralleltest // t.Setenv and the PID-named .inuse guard are process-wide
func TestMaybeFinalizeFetch_RetainsBeforeDirtyNotification(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.Core.MaxAutoSnapshots = 1
	poster := &fakePoster{}
	m := newTestModel(t, poster)
	m.bundle = bundle
	m.list = list.New(bundle)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	now := time.Now()
	oldPath := autoSnapshotPath(dir, time.Unix(1, 0))
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o600))
	require.NoError(t, os.Chtimes(oldPath, time.Time{}, time.Unix(1, 0)))
	f, err := snapshot.CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	wipPath := f.Name()
	m.backingFile = f
	m.backingContainer = snapshot.NewWriter(f)
	m.setBackingPath(wipPath)
	m.fetching = true
	m.queryStartTime = now

	inst := newTestInstance(t, "a")
	inst.state = status.Success
	inst.flushedLogCount = 1
	inst.flushedLogsSize = 1
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, inst.CRN, []icl.Log{{
		Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"},
	}}))
	m.instances = Instances{inst}

	var dirty bool
	m.SetNotifyDirty(func() {
		dirty = true
		assert.NoFileExists(t, oldPath, "retention must finish before dirty notification")
	})
	m.maybeFinalizeFetch()

	assert.True(t, dirty)
	assert.NoFileExists(t, oldPath, "retention must finish before finalization returns")
	assert.FileExists(t, autoSnapshotPath(dir, now))
}

//nolint:paralleltest // snapshot claims and XDG state are process-global
func TestFinalizeBackingContainer_CollisionKeepsSelectedDestinationClaimed(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.Core.MaxAutoSnapshots = 2
	m := newTestModel(t, &fakePoster{})
	m.bundle = bundle
	m.list = list.New(bundle)
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	startedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	base := autoSnapshotPath(dir, startedAt)
	require.NoError(t, os.WriteFile(base, []byte("existing"), 0o600))
	file, err := snapshot.CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	m.backingFile = file
	m.backingContainer = snapshot.NewWriter(file)
	m.setBackingPath(file.Name())
	m.fetching = true
	m.queryStartTime = startedAt
	inst := newTestInstance(t, "a")
	inst.state = status.Success
	inst.flushedLogCount = 1
	inst.flushedLogsSize = 1
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, inst.CRN, []icl.Log{{
		Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"},
	}}))
	m.instances = Instances{inst}
	selected := filepath.Join(dir, "auto-20260814-120000-2.lognav")
	m.SetNotifyDirty(func() {
		inUse, inUseErr := snapshot.InUse(selected)
		require.NoError(t, inUseErr)
		assert.True(t, inUse, "retention and notification run while selected destination is claimed")
	})

	m.maybeFinalizeFetch()

	assert.Equal(t, selected, m.GetBackingPath())
	contents, err := os.ReadFile(base) // #nosec G304 -- test-only snapshot-dir path
	require.NoError(t, err)
	assert.Equal(t, "existing", string(contents))
	assert.FileExists(t, selected)
}

//nolint:paralleltest // publishAutoSnapshot and XDG state are process-global test seams
func TestFinalizeBackingContainer_CleanupFailureKeepsPublishedBacking(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := newTestModel(t, &fakePoster{})
	m.bundle = bundle
	m.list = list.New(bundle)
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	file, err := snapshot.CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	wipPath := file.Name()
	m.backingFile = file
	m.backingContainer = snapshot.NewWriter(file)
	m.setBackingPath(wipPath)
	m.fetching = true
	m.queryStartTime = time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	inst := newTestInstance(t, "a")
	inst.state = status.Success
	inst.flushedLogCount = 1
	inst.flushedLogsSize = 1
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, inst.CRN, []icl.Log{{
		Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"},
	}}))
	m.instances = Instances{inst}

	selected := autoSnapshotPath(dir, m.queryStartTime)
	origPublish := publishAutoSnapshot
	publishAutoSnapshot = func(wip string, _ time.Time, before func(string) error) (string, error) {
		require.NoError(t, before(selected))
		require.NoError(t, os.Link(wip, selected))
		return selected, &snapshot.RenameCleanupError{Err: errors.New("unlink failed")}
	}
	t.Cleanup(func() { publishAutoSnapshot = origPublish })

	assert.True(t, m.finalizeOrNotify())
	assert.Equal(t, selected, m.GetBackingPath())
	assert.FileExists(t, selected)
	assert.FileExists(t, wipPath)
	inUse, err := snapshot.InUse(selected)
	require.NoError(t, err)
	assert.True(t, inUse)
}

//nolint:paralleltest // setOwnInUse and XDG state are process-global test seams
func TestFinalizeBackingContainer_ClaimFailureDoesNotPublishOrRetain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.Core.MaxAutoSnapshots = 0
	poster := &fakePoster{}
	m := newTestModel(t, poster)
	m.bundle = bundle
	m.list = list.New(bundle)
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	file, err := snapshot.CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	wipPath := file.Name()
	m.backingFile = file
	m.backingContainer = snapshot.NewWriter(file)
	m.setBackingPath(wipPath)
	m.fetching = true
	m.queryStartTime = time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	inst := newTestInstance(t, "a")
	inst.state = status.Success
	inst.flushedLogCount = 1
	inst.flushedLogsSize = 1
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, inst.CRN, []icl.Log{{
		Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"},
	}}))
	m.instances = Instances{inst}

	claimErr := errors.New("claim failed")
	var claims []string
	origSetOwnInUse := setOwnInUse
	setOwnInUse = func(path string) error {
		claims = append(claims, path)
		if path != "" {
			return claimErr
		}
		return nil
	}
	t.Cleanup(func() { setOwnInUse = origSetOwnInUse })

	m.maybeFinalizeFetch()

	finalPath := autoSnapshotPath(dir, m.queryStartTime)
	assert.Equal(t, []string{finalPath, ""}, claims)
	assert.NoFileExists(t, finalPath)
	assert.FileExists(t, wipPath)
	entries, err := snapshot.Scan()
	require.NoError(t, err)
	assert.Empty(t, entries, "claim failure must not run zero-retention publication")
	assert.Empty(t, m.GetBackingPath())
}

// TestMaybeFinalizeFetch_EmptyResult_SkipsAutoSnapshot verifies that a fetch
// returning no logs for any instance discards its .wip backing container (closed
// and unlinked) instead of finalizing it, and suppresses the configured auto
// SaveSnapshotMsg. A non-empty result finalizes the .wip into an auto snapshot
// and posts the message. This keeps empty auto snapshots out of the snapshot dir.
func TestMaybeFinalizeFetch_EmptyResult_SkipsAutoSnapshot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		logCount      int
		wantSnapshot  bool
		wantFinalized bool
	}{
		{name: "empty result discards wip and skips snapshot", logCount: 0, wantSnapshot: false, wantFinalized: false},
		{name: "non-empty result finalizes and posts snapshot", logCount: 2, wantSnapshot: true, wantFinalized: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			wipPath := filepath.Join(dir, "test.lognav.wip")
			f, err := os.Create(wipPath) // #nosec G304 -- test-only path via t.TempDir
			require.NoError(t, err)

			bundle := depstest.NewTest(t)
			bundle.Config.Core.SaveSnapshotOnFetchDone = true
			poster := &fakePoster{}
			m := newTestModel(t, poster)
			m.bundle = bundle
			m.list = list.New(bundle) // finalizeBackingContainer -> snapshotMeta -> m.list.GetCursor()
			m.backingContainer = snapshot.NewWriter(f)
			m.backingFile = f
			m.setBackingPath(wipPath)
			m.fetching = true
			m.queryStartTime = time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)

			inst := newTestInstance(t, "a")
			inst.state = status.Success
			if tt.logCount > 0 {
				logs := make([]icl.Log, tt.logCount)
				for i := range logs {
					logs[i] = icl.Log{Data: map[string]any{msgKey: strconv.Itoa(i)}, Metadata: icl.Metadata{ID: strconv.Itoa(i)}}
				}
				require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, inst.CRN, logs))
				inst.flushedLogCount = tt.logCount
				inst.flushedLogsSize = 1
			}
			m.instances = Instances{inst}

			m.maybeFinalizeFetch()

			var posted bool
			for _, ev := range poster.events() {
				if _, ok := ev.(msgs.SaveSnapshotMsg); ok {
					posted = true
				}
			}
			assert.Equal(t, tt.wantSnapshot, posted, "SaveSnapshotMsg emission must track whether the fetch had logs")

			finalPath := autoSnapshotPath(dir, m.queryStartTime)
			if tt.wantFinalized {
				assert.NotEmpty(t, m.GetBackingPath(), "finalized fetch keeps a backing path")
				assert.FileExists(t, finalPath, "non-empty fetch finalizes the .wip into an auto snapshot")
			} else {
				assert.Empty(t, m.GetBackingPath(), "discarded fetch clears the backing path")
				assert.NoFileExists(t, wipPath, "empty fetch removes the .wip from disk")
				assert.NoFileExists(t, finalPath, "empty fetch creates no auto snapshot")
			}
		})
	}
}

// errWriter is a backing-container sink whose every flush fails, so
// snapshot.SaveStateFrame (which flushes each appended frame) errors
// deterministically without depending on OS-level fd behaviour.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("disk went away") }

// TestMaybeFinalizeFetch_FinalizeFailure_SuppressesSnapshotAndNotifies is a
// regression test for the "my data evaporated on restart" bug: a failed state-frame
// write or a failed rename used to be logged only, so the fetch looked saved
// (SaveSnapshotMsg fired for a snapshot that does not exist) while the logs
// survived in memory only. A failed finalize must suppress the finalized-effects
// and surface a user-visible notice — while the fetch-done bell still fires, since
// the fetch itself did complete.
func TestMaybeFinalizeFetch_FinalizeFailure_SuppressesSnapshotAndNotifies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		breakIt func(t *testing.T, m *Model, dir, wipPath string)
	}{
		{
			name: "state frame write fails",
			breakIt: func(_ *testing.T, m *Model, _, _ string) {
				m.backingContainer = snapshot.NewWriter(errWriter{})
			},
		},
		{
			name: "rename fails",
			breakIt: func(t *testing.T, _ *Model, dir, _ string) {
				t.Helper()
				// Read-only directory: the state frame still lands (the fd is open),
				// but os.Rename of the .wip cannot create the destination entry.
				require.NoError(t, os.Chmod(dir, 0o500)) // #nosec G302 -- test-only dir mode; the point is to make it unwritable
				// Restore before t.TempDir's own cleanup (LIFO) tries to remove dir.
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the t.TempDir mode so cleanup can unlink
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			wipPath := filepath.Join(dir, "test.lognav.wip")
			f, err := os.Create(wipPath) // #nosec G304 -- test-only path via t.TempDir
			require.NoError(t, err)

			bundle := depstest.NewTest(t)
			bundle.Config.Core.SaveSnapshotOnFetchDone = true
			bundle.Config.Core.NotifyOnFetchDone = true
			poster := &fakePoster{}
			m := newTestModel(t, poster)
			m.bundle = bundle
			m.list = list.New(bundle) // finalizeBackingContainer -> snapshotMeta -> m.list.GetCursor()
			m.backingContainer = snapshot.NewWriter(f)
			m.backingFile = f
			m.setBackingPath(wipPath)
			m.fetching = true
			m.queryStartTime = time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)

			inst := newTestInstance(t, "a")
			inst.state = status.Success
			inst.flushedLogCount = 1 // non-empty: takes the finalize branch, not the discard branch
			inst.flushedLogsSize = 1
			m.instances = Instances{inst}

			tt.breakIt(t, m, dir, wipPath)

			m.maybeFinalizeFetch()

			var saved, notified bool
			var notice *msgs.ShowDialogMsg
			for _, ev := range poster.events() {
				switch e := ev.(type) {
				case msgs.SaveSnapshotMsg:
					saved = true
				case msgs.NotifyMsg:
					notified = true
				case msgs.ShowDialogMsg:
					notice = &e
				}
			}
			assert.False(t, saved, "a failed finalize must NOT fire the snapshot-list refresh")
			assert.True(t, notified, "NotifyOnFetchDone still fires — the fetch itself completed")
			require.NotNil(t, notice, "a failed finalize must surface a user-visible notice")
			assert.Equal(t, "Snapshot", notice.Title)
			assert.Contains(t, notice.Message, "in memory only")
			assert.NoFileExists(t, autoSnapshotPath(dir, m.queryStartTime),
				"no auto snapshot exists when finalize failed")
		})
	}
}

// TestMaybeFinalizeFetch_NotifyGate proves the NotifyOnFetchDone gate: once every
// instance has settled, maybeFinalizeFetch posts a NotifyMsg iff the toggle is on
// (the runtime turns that event into a terminal bell). The runtime, not the
// picker, owns the screen, so the trigger only emits the mechanism-agnostic event.
func TestMaybeFinalizeFetch_NotifyGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		enable bool
		want   bool
	}{
		{name: "enabled posts NotifyMsg", enable: true, want: true},
		{name: "disabled posts nothing", enable: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := depstest.NewTest(t)
			bundle.Config.Core.NotifyOnFetchDone = tt.enable
			bundle.Config.Core.SaveSnapshotOnFetchDone = false
			m := New(t.Context(), bundle)
			m.bundle = bundle
			poster := &fakePoster{}
			m.poster = poster
			inst := newTestInstance(t, "a")
			inst.state = status.Success // settled (no query running)
			m.instances = Instances{inst}

			m.maybeFinalizeFetch()

			var found bool
			for _, ev := range poster.posted {
				if _, ok := ev.(msgs.NotifyMsg); ok {
					found = true
				}
			}
			assert.Equal(t, tt.want, found, "NotifyMsg posted must track NotifyOnFetchDone")
		})
	}
}

// TestModel_CancelAllFetches_SuppressesNotify is a regression test: cancelling a
// fetch is a direct user action (the user is already looking at the picker), so
// the fetch-done terminal notification must NOT fire on cancel. It previously
// fired once per cancelled region — cancelAllFetches flips every in-flight query
// to Cancelled synchronously, so the direct settle() AND each trailing
// LogStreamDoneMsg re-ran maybeFinalizeFetch with the AreAllQueriesDone gate
// already open, each emitting a NotifyMsg.
func TestModel_CancelAllFetches_SuppressesNotify(t *testing.T) {
	t.Parallel()
	m, _ := newModelWithBackingFile(t)
	bundle := depstest.NewTest(t)
	bundle.Config.Core.NotifyOnFetchDone = true
	m.bundle = bundle
	m.list = list.New(bundle) // finalizeBackingContainer -> snapshotMeta -> m.list.GetCursor()

	// Two in-flight instances: the old per-region multiplication would fire one
	// NotifyMsg per cancelled region.
	mk := func(name string) *Instance {
		inst := newTestInstance(t, name)
		inst.state = status.InProgress
		inst.cancelQuery = func() {} // so CancelQuery transitions InProgress -> Cancelled
		return inst
	}
	m.instances = Instances{mk("a"), mk("b")}

	f := &fakePoster{}
	m.SetPoster(f)

	m.cancelAllFetches()
	// Trailing stream-done signals: each cancelled query goroutine returns
	// context.Canceled and posts its LogStreamDoneMsg through the normal path.
	m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: "a"})
	m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: "b"})

	for _, ev := range f.events() {
		if _, ok := ev.(msgs.NotifyMsg); ok {
			t.Fatalf("cancel must not emit NotifyMsg, got %#v", f.events())
		}
	}
}

// TestModel_CancelAllFetches_SavesSnapshotOnce is a regression test: the
// fetch-done snapshot save (SaveSnapshotMsg, which refreshes the snapshot list
// after the .wip is finalized) must fire exactly once per fetch, even on cancel.
// It previously fired once per cancelled region because the emit was decoupled
// from finalizeBackingContainer — every trailing LogStreamDoneMsg re-ran the
// gate. The actual auto-save (finalize/rename) happens once, so the list refresh
// must too.
func TestModel_CancelAllFetches_SavesSnapshotOnce(t *testing.T) {
	t.Parallel()
	m, _ := newModelWithBackingFile(t)
	bundle := depstest.NewTest(t)
	bundle.Config.Core.SaveSnapshotOnFetchDone = true
	m.bundle = bundle
	m.list = list.New(bundle)

	// au-syd: finished, logs already flushed into the backing container (so the
	// fetch has logs and the .wip is finalized — and thus auto-saved — on cancel).
	const flushedName = "au-syd"
	auLogs := []icl.Log{{Data: map[string]any{msgKey: "a"}, Metadata: icl.Metadata{ID: "1"}}}
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, testCRN(flushedName), auLogs))
	au := newTestInstance(t, flushedName)
	au.state = status.Success
	au.flushedLogCount = len(auLogs)
	au.flushedLogsSize = 1

	// us-east: still in flight when the user cancels.
	const cancelName = "us-east"
	us := newTestInstance(t, cancelName)
	us.state = status.InProgress
	us.cancelQuery = func() {} // so CancelQuery transitions InProgress -> Cancelled

	m.instances = Instances{au, us}

	f := &fakePoster{}
	m.SetPoster(f)

	m.cancelAllFetches()
	m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: us.CRN})

	var saves int
	for _, ev := range f.events() {
		if _, ok := ev.(msgs.SaveSnapshotMsg); ok {
			saves++
		}
	}
	assert.Equal(t, 1, saves, "fetch-done snapshot save must fire exactly once, not per cancelled region")
}

// TestMaybeFinalizeFetch_NoSaveWhenEmpty pins that a fetch returning no logs
// throws the .wip away and does NOT emit a snapshot save, even with the toggle
// on: there is nothing worth persisting.
func TestMaybeFinalizeFetch_NoSaveWhenEmpty(t *testing.T) {
	t.Parallel()
	m, _ := newModelWithBackingFile(t)
	bundle := depstest.NewTest(t)
	bundle.Config.Core.SaveSnapshotOnFetchDone = true
	m.bundle = bundle
	m.list = list.New(bundle)

	inst := newTestInstance(t, "a")
	inst.state = status.Success // settled, but no logs
	m.instances = Instances{inst}

	f := &fakePoster{}
	m.SetPoster(f)

	m.maybeFinalizeFetch()

	for _, ev := range f.events() {
		if _, ok := ev.(msgs.SaveSnapshotMsg); ok {
			t.Fatalf("empty fetch must not save a snapshot, got %#v", f.events())
		}
	}
}

func TestOnEnvCredFailed_DoesNotTouchOtherEnv(t *testing.T) {
	t.Parallel()
	prod := newTestInstanceEnv(t, "p", icl.EnvProd)
	stage := newTestInstanceEnv(t, "s", icl.Environment("test-cloud"))
	prod.state, stage.state = status.AuthInProgress, status.AuthInProgress
	m := New(t.Context(), depstest.NewTest(t))
	m.bundle = depstest.NewTest(t)
	m.poster = &fakePoster{}
	m.instances = Instances{prod, stage}
	m.OnEnvCredFailed(EnvCredFailedMsg{Env: icl.EnvProd, Err: errors.New("boom")})
	assert.Equal(t, status.Error, prod.state, "prod errored")
	assert.Equal(t, status.AuthInProgress, stage.state, "staging untouched -> no hang")
}

func TestOnMemberAuthResolved_SkipsDeselected(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")
	inst.StartAuthTimer()        // AuthInProgress
	inst.state = status.Disabled // deselected mid-auth via the all-selector
	m := New(t.Context(), depstest.NewTest(t))
	m.bundle = depstest.NewTest(t)
	m.poster = &fakePoster{}
	m.instances = Instances{inst}

	m.OnMemberAuthResolved(MemberAuthResolvedMsg{CRN: "a", Token: "t", Query: "q"})

	// A late resolve must not start a query on a deselected instance: StartTimer
	// would have flipped it to InProgress.
	assert.Equal(t, status.Disabled, inst.state, "deselected instance must not start a query")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestSetBackingPath_SyncsInUseFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	backing := filepath.Join(dir, "auto-20260814-120000.lognav")

	m.setBackingPath(backing)
	inUse, err := snapshot.InUse(backing)
	require.NoError(t, err)
	assert.True(t, inUse, "setBackingPath must claim the file")

	m.setBackingPath("")
	inUse, err = snapshot.InUse(backing)
	require.NoError(t, err)
	assert.False(t, inUse, "clearing backing must release the claim")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestEvictIfCurrentBacking(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	backing := filepath.Join(dir, "m_cur.lognav")
	m.setBackingPath(backing)

	m.EvictIfCurrentBacking("m_other.lognav")
	assert.Equal(t, backing, m.GetBackingPath())

	m.EvictIfCurrentBacking("m_cur.lognav")
	assert.Empty(t, m.GetBackingPath())

	inUse, err := snapshot.InUse(backing)
	require.NoError(t, err)
	assert.False(t, inUse, "evicting current backing must release the .inuse claim")
}

// TestHeldManagedBacking proves the shutdown release-notify probe: a managed
// (snapshot-dir-resident) backing is held (true); an in-place external open
// outside the dir holds no claim (false); no backing is false.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestHeldManagedBacking(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	// No backing: nothing held.
	assert.False(t, m.HeldManagedBacking(), "no backing must report not held")

	// Managed backing (inside the snapshot dir): held.
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	m.setBackingPath(filepath.Join(dir, "m_cur.lognav"))
	assert.True(t, m.HeldManagedBacking(), "a snapshot-dir-resident backing must report held")

	// In-place external open (outside the snapshot dir): not held (no .inuse claim).
	m.setBackingPath(filepath.Join(t.TempDir(), "external.lognav"))
	assert.False(t, m.HeldManagedBacking(), "an out-of-dir external open must report not held")
}

// TestEvictIfCurrentBacking_ResetsInstanceDisplays proves that deleting the
// snapshot the session is currently backed by not only clears the backing path
// but also resets every instance's runtime/display state to its never-fetched
// default (fresh, no stale Success/Error badge, no log counts, reset timer,
// empty store) while preserving each instance's enable/disable selection.
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestEvictIfCurrentBacking_ResetsInstanceDisplays(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	bundle.Config.ICL.Instances = []config.ICLInstanceConfig{
		{Name: "one", CRN: config.MustCRNFromString(testCRN("one"))},
		{Name: "two", CRN: config.MustCRNFromString(testCRN("two"))},
		{Name: "three", CRN: config.MustCRNFromString(testCRN("three"))},
		{Name: "four", CRN: config.MustCRNFromString(testCRN("four"))},
	}
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	require.GreaterOrEqual(t, len(m.instances), 4, "test needs at least four instances")

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	backing := filepath.Join(dir, "m_cur.lognav")
	m.setBackingPath(backing)

	// Put the instances into a "post-restore" display state: terminal badges,
	// non-zero counts, a started timer, and a non-empty live store.
	//   inst0: enabled, Success, flushed count + live logs.
	//   inst1: enabled, Error, flushed count.
	//   inst2: explicitly disabled by the user (selection must survive the reset).
	//   inst3: terminal Cancelled badge + flushed count (must demote to Disabled,
	//          mirroring ResolveTokens, with IsEnabled() staying false).
	inst0 := m.instances[0]
	inst0.StartTimer()           // sets startTime/lastUpdateTime non-zero (and InProgress)
	inst0.state = status.Success // overwrite to a terminal post-restore badge
	inst0.flushedLogCount = 7
	inst0.flushedLogsSize = 1
	inst0.Store.SetLogs([]icl.Log{
		{Data: map[string]any{"msg": "a"}, Metadata: icl.Metadata{TSMicro: 1}},
		{Data: map[string]any{"msg": "b"}, Metadata: icl.Metadata{TSMicro: 2}},
	})
	require.Positive(t, inst0.Store.GetLogCount())

	inst1 := m.instances[1]
	inst1.StartTimer()
	inst1.state = status.Error
	inst1.flushedLogCount = 3
	inst1.flushedLogsSize = 1

	inst2 := m.instances[2]
	inst2.Disable()

	inst3 := m.instances[3]
	inst3.StartTimer()
	inst3.state = status.Cancelled
	inst3.flushedLogCount = 5
	inst3.flushedLogsSize = 1
	require.False(t, inst3.IsEnabled(), "Cancelled instance must start not-enabled")

	// Deleting a NON-current backing must NOT reset anything.
	m.EvictIfCurrentBacking("m_other.lognav")
	assert.Equal(t, backing, m.GetBackingPath(), "non-current delete must not clear the backing")
	assert.Equal(t, status.Success, inst0.state, "non-current delete must not reset instance state")
	assert.Equal(t, 7, inst0.flushedLogCount, "non-current delete must not reset flushed counts")

	// Deleting the CURRENT backing clears it and resets every instance display.
	m.EvictIfCurrentBacking("m_cur.lognav")
	assert.Empty(t, m.GetBackingPath(), "current delete must clear the backing")

	// Enabled instances reset to the never-fetched enabled display.
	for _, inst := range []*Instance{inst0, inst1} {
		assert.Equal(t, status.Enabled, inst.state, "enabled instance must reset to Enabled")
		assert.Equal(t, 0, inst.flushedLogCount, "flushedLogCount must reset to 0")
		assert.Zero(t, inst.flushedLogsSize, "flushed native NDJSON size must reset to 0")
		assert.Equal(t, 0, inst.DisplayLogCount(), "DisplayLogCount must reset to 0")
		assert.Equal(t, 0, inst.Store.GetLogCount(), "store must be empty after reset")
		assert.True(t, inst.startTime.IsZero(), "timer startTime must be reset")
		assert.True(t, inst.lastUpdateTime.IsZero(), "timer lastUpdateTime must be reset")
	}

	// The disabled instance keeps its disabled selection.
	assert.Equal(t, status.Disabled, inst2.state, "user's disabled selection must be preserved")
	assert.False(t, inst2.IsEnabled(), "disabled instance must stay disabled")

	// The Cancelled instance demotes to Disabled (the one non-identity mapping):
	// IsEnabled() stays false, and the terminal Cancelled badge + counts/timer
	// are cleared just like every other reset instance.
	assert.Equal(t, status.Disabled, inst3.state, "Cancelled must demote to Disabled (ResolveTokens precedent)")
	assert.False(t, inst3.IsEnabled(), "demoted Cancelled instance must stay not-enabled")
	assert.Equal(t, 0, inst3.flushedLogCount, "Cancelled instance flushedLogCount must reset to 0")
	assert.Zero(t, inst3.flushedLogsSize, "Cancelled instance size must reset to 0")
	assert.Equal(t, 0, inst3.DisplayLogCount(), "Cancelled instance DisplayLogCount must reset to 0")
	assert.Equal(t, 0, inst3.Store.GetLogCount(), "Cancelled instance store must be empty after reset")
	assert.True(t, inst3.startTime.IsZero(), "Cancelled instance timer startTime must be reset")
	assert.True(t, inst3.lastUpdateTime.IsZero(), "Cancelled instance timer lastUpdateTime must be reset")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestOnSnapshotRenamedReclaimsBacking(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	const knownID = "550e8400-e29b-41d4-a716-446655440000"
	m.setBacking(filepath.Join(dir, "old.lognav"), knownID)

	m.OnSnapshotRenamed("old.lognav", "new.lognav")

	assert.Equal(t, filepath.Join(dir, "new.lognav"), m.GetBackingPath())
	assert.Equal(t, knownID, m.backingUUID(), "UUID is preserved across the rename")
	held, err := snapshot.Holders(filepath.Join(dir, "new.lognav"))
	require.NoError(t, err)
	assert.Contains(t, held, os.Getpid(), "this session now holds new.lognav")
	heldOld, err := snapshot.Holders(filepath.Join(dir, "old.lognav"))
	require.NoError(t, err)
	assert.NotContains(t, heldOld, os.Getpid(), "old claim dropped")

	// A rename of a snapshot we do NOT hold leaves backing untouched.
	m.OnSnapshotRenamed("unrelated.lognav", "other.lognav")
	assert.Equal(t, filepath.Join(dir, "new.lognav"), m.GetBackingPath())
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestSetBacking_LiveClaimFiresNotifyDirty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	var dirty int
	m.SetNotifyDirty(func() { dirty++ })

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// A live backing claim fires notifyDirty exactly once.
	m.setBacking(filepath.Join(dir, "cur.lognav"), "550e8400-e29b-41d4-a716-446655440000")
	assert.Equal(t, 1, dirty, "a live backing claim must fire notifyDirty once")

	// The explicit-delete evict release (setBacking("","")) also fires it: a
	// holder dropped is a list-coloring change peers should learn.
	m.EvictIfCurrentBacking("cur.lognav")
	assert.Equal(t, 2, dirty, "the explicit-delete evict release must fire notifyDirty")
}

// TestOnSnapshotRenamed_MatchingBackingFiresNotifyDirty proves a rename of the
// held snapshot re-points the backing via setBacking, which fires notifyDirty
// once. Consequence (intentional, idempotent): the ORIGINATING session emits
// BOTH snapshot_renamed (from snapshothandler's OnRenamed hook) AND
// snapshots_dirty (from this setBacking → notifyDirty). Peers dedup harmlessly
// (the dirty just re-lists; the rename re-points). A rename of an unheld
// snapshot does NOT fire notifyDirty (setBacking is never reached).
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestOnSnapshotRenamed_MatchingBackingFiresNotifyDirty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	m.setBacking(filepath.Join(dir, "old.lognav"), "550e8400-e29b-41d4-a716-446655440000")

	// Inject the counter AFTER the claim so we count only the rename's call.
	var dirty int
	m.SetNotifyDirty(func() { dirty++ })

	// A rename of the held snapshot re-points the backing via setBacking.
	m.OnSnapshotRenamed("old.lognav", "new.lognav")
	assert.Equal(t, 1, dirty, "renaming the held snapshot must fire notifyDirty once")
	assert.Equal(t, filepath.Join(dir, "new.lognav"), m.GetBackingPath())

	// A rename of a snapshot we do NOT hold never reaches setBacking.
	m.OnSnapshotRenamed("unrelated.lognav", "other.lognav")
	assert.Equal(t, 1, dirty, "renaming an unheld snapshot must NOT fire notifyDirty")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestCloseRelease_DoesNotFireNotifyDirty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	wipPath := filepath.Join(dir, "test.lognav.wip")
	f, err := os.Create(wipPath) // #nosec G304 -- test-only path via t.TempDir-isolated Dir()
	require.NoError(t, err)
	m.backingContainer = snapshot.NewWriter(f)
	m.backingFile = f
	m.setBackingPath(wipPath)
	m.fetching = true

	// Inject the counter AFTER the claim so we observe only the teardown path.
	var dirty int
	m.SetNotifyDirty(func() { dirty++ })

	// The shutdown/Close release goes through raw setBackingPath(""), NOT
	// setBacking, precisely so it never spawns a notifyDirty (poster.Go) after
	// drainPoster/Wait at shutdown (use-after-drain hazard).
	require.NoError(t, m.Close())
	assert.Zero(t, dirty, "Close-path release must NOT fire notifyDirty")
}

// TestOnSnapshotRenamed_ConcurrentConvergesToLastTo delivers two renames so the
// backing converges to the final name, and exercises concurrent reads of
// GetBackingPath against OnSnapshotRenamed's atomic writes (run under -race).
//
//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; PID-named .inuse files are OS-shared state
func TestOnSnapshotRenamed_ConcurrentConvergesToLastTo(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)
	const id = "550e8400-e29b-41d4-a716-446655440000"
	m.setBacking(filepath.Join(dir, "a.lognav"), id)

	// Concurrent readers race the atomic backingPath writes; -race proves the
	// atomic.Pointer access is data-race-free.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = m.GetBackingPath()
			}
		})
	}

	// Two renames back-to-back: a -> b -> c. The chained from/to must match each
	// current backing, so the backing converges to the last "to".
	m.OnSnapshotRenamed("a.lognav", "b.lognav")
	m.OnSnapshotRenamed("b.lognav", "c.lognav")
	wg.Wait()

	assert.Equal(t, filepath.Join(dir, "c.lognav"), m.GetBackingPath(),
		"two chained renames converge to the last to")
	assert.Equal(t, id, m.backingUUID(), "UUID preserved across both renames")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; touches snapshot.Dir() (OS-shared state)
func TestBackingReadMissReResolvesByUUID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// 1. Write a REAL finalized snapshot into the isolated Dir() so Summarize can
	//    read its UUID (NewWriter+SaveStateFrame pattern from TestFindByID).
	oldPath := filepath.Join(dir, "old.lognav")
	c := snapshottest.NewContainerFile(t, oldPath)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "source logs"}))
	require.NoError(t, c.Close())

	// 2. Summarize it to get its UUID, then record it as the backing.
	sum, err := snapshot.Summarize(oldPath)
	require.NoError(t, err)
	require.NotEmpty(t, sum.ID)
	m.setBacking(oldPath, sum.ID)

	// 3. Rename the file on disk WITHOUT notifying the session (dropped notify).
	newPath := filepath.Join(dir, "renamed.lognav")
	require.NoError(t, os.Rename(oldPath, newPath))

	// 4. resolveBackingPath must recover the file by UUID and re-point the backing.
	got, err := m.resolveBackingPath()
	require.NoError(t, err)
	assert.Equal(t, newPath, got)
	assert.Equal(t, newPath, m.GetBackingPath(), "backing re-pointed")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; touches snapshot.Dir() (OS-shared state)
func TestBackingReadMissNoMatchingFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// Backing path is gone and no managed file carries the UUID: genuine loss.
	m.setBacking(filepath.Join(dir, "gone.lognav"), "550e8400-e29b-41d4-a716-446655440000")

	_, err = m.resolveBackingPath()
	require.ErrorIs(t, err, snapshot.ErrSnapshotNotFound)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel; touches snapshot.Dir() (OS-shared state)
func TestBackingReadMissEmptyUUID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	m := New(t.Context(), bundle)
	t.Cleanup(func() { _ = m.Close() })

	dir, err := snapshot.Dir()
	require.NoError(t, err)

	// Legacy backing (no UUID) whose file is gone: nothing to recover by.
	m.setBacking(filepath.Join(dir, "gone.lognav"), "")

	_, err = m.resolveBackingPath()
	require.ErrorIs(t, err, ErrNoBackingFile)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestIsInSnapshotDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	require.True(t, isInSnapshotDir(filepath.Join(dir, "a.lognav")))
	require.False(t, isInSnapshotDir("/tmp/elsewhere/a.lognav"))
}

func TestModel_SnapshotMeta_ListsInstancesAndQuery(t *testing.T) {
	m := newTestModel(t, &fakePoster{})
	m.bundle = depstest.NewTest(t)
	m.bundle.State.SetLastFetchedQuery("source logs")
	m.instances = Instances{newTestInstance(t, "br-sao"), newTestInstance(t, "ca-tor")}

	snap := m.SnapshotMeta()

	assert.Equal(t, "source logs", snap.Query)
	names := make([]string, 0, len(snap.InstancePickerSnapshot.Instances))
	for _, inst := range snap.InstancePickerSnapshot.Instances {
		names = append(names, inst.CRN)
	}
	assert.Equal(t, []string{testCRN("br-sao"), testCRN("ca-tor")}, names)
}

// TestSaveSession_RoundTripsThroughSessionPath proves the write path resolves
// its file through icl.SessionPath — the same resolver New() loads from and
// `lognav logout` deletes — and that it creates the state directory itself
// (SessionPath stays pure, so nothing else will).
//
// Not parallel: it drives XDG_STATE_HOME, which is process-global.
func TestSaveSession_RoundTripsThroughSessionPath(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	wantPath, err := icl.SessionPath()
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Dir(wantPath), "state dir must not exist yet, so the save path has to create it")

	bundle := depstest.NewTest(t)
	bundle.Config.ICL.Environments["test-cloud"] = config.ICLEnvironmentConfig{IAMURL: "https://iam.example/test"}
	m := New(t.Context(), bundle)
	tokens := map[icl.Environment]string{icl.EnvProd: "prod-refresh", icl.Environment("test-cloud"): "test-refresh"}
	m.authManager.SetRefreshTokens(tokens)

	m.saveSession()

	require.FileExists(t, wantPath, "session must land exactly where SessionPath resolves (the file logout removes)")
	if runtime.GOOS != "windows" {
		dirInfo, statErr := os.Stat(filepath.Dir(wantPath))
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
		fileInfo, statErr := os.Stat(wantPath)
		require.NoError(t, statErr)
		assert.True(t, fileInfo.Mode().IsRegular())
		assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
	}
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(wantPath), ".session-*.tmp"))
	require.NoError(t, globErr)
	assert.Empty(t, matches, "session save must leave no temporary file")
	loaded, err := icl.LoadSession(wantPath)
	require.NoError(t, err)
	assert.Equal(t, tokens, loaded, "saved session must round-trip through the load path")
	assert.Equal(t, tokens, m.lastSavedSession, "dirty-check state must record what was written")

	// The load side of New() must pick the same file back up.
	bundle2 := depstest.NewTest(t)
	bundle2.Config.ICL.Environments["test-cloud"] = config.ICLEnvironmentConfig{IAMURL: "https://iam.example/test"}
	m2 := New(t.Context(), bundle2)
	assert.Equal(t, tokens, m2.authManager.GetRefreshTokens(), "New must load the session the save path wrote")

	// What `lognav logout` does: remove the SessionPath file.
	require.NoError(t, os.Remove(wantPath))
	m3 := New(t.Context(), depstest.NewTest(t))
	assert.Empty(t, m3.authManager.GetRefreshTokens(), "removing the SessionPath file must clear the session")
}

// TestSessionPath_IsPure guards the invariant that the resolver has no
// directory-creating side effect: logout and the load path both call it, and
// neither should conjure a state dir.
func TestSessionPath_IsPure(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	p, err := icl.SessionPath()
	require.NoError(t, err)
	for range 3 {
		p2, err := icl.SessionPath()
		require.NoError(t, err)
		assert.Equal(t, p, p2)
	}
	assert.NoDirExists(t, filepath.Dir(p), "SessionPath must not create the state directory")
}
