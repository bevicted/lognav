package instancepicker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

// newDispatchModel builds a *Model wired with what dispatchArchive needs: a
// bundle (for State.Query), an AccountManager, a poster, instances, a logger, and
// a send sink. The caller supplies the poster so it can pick inline (fakePoster)
// vs deferred (deferredPoster) goroutine execution.
func newDispatchModel(t *testing.T, poster msgs.Poster, am *icl.AccountManager, insts Instances) *Model {
	t.Helper()
	return &Model{
		bundle:       depstest.NewTest(t),
		instances:    insts,
		authManager:  am,
		poster:       poster,
		send:         func(uv.Event) {},
		logger:       slog.Default(),
		ctx:          context.Background(),
		authCtx:      context.Background(), // production New always sets this; dispatch governs IO with it
		pendingLoads: map[string]struct{}{},
	}
}

// dispatchNotices extracts the message text of every ShowDialogMsg the model
// posted (the picker's notice mechanism).
func dispatchNotices(events []uv.Event) []string {
	var out []string
	for _, ev := range events {
		if d, ok := ev.(msgs.ShowDialogMsg); ok {
			out = append(out, d.Message)
		}
	}
	return out
}

// TestDispatchArchive_ZeroEnabled_DistinctNotice_NoFile proves the zero-enabled
// guard: shift+f with nothing selected shows a distinct "select instances" notice
// (NOT the all-submits-failed path) and writes no archive file.
func TestDispatchArchive_ZeroEnabled_DistinctNotice_NoFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // confine archive.Dir() to a tempdir

	bundle := depstest.NewTest(t)
	inst := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	inst.Disable()
	f := &fakePoster{}
	m := newDispatchModel(t, f, testAccountManager(), Instances{inst})

	m.dispatchArchive()

	notices := dispatchNotices(f.events())
	require.Len(t, notices, 1, "exactly one notice")
	assert.Equal(t, "select instances first", notices[0],
		"zero-enabled must be the distinct select-instances notice, not all-submits-failed")

	entries, err := archive.Scan()
	require.NoError(t, err)
	assert.Empty(t, entries, "zero-enabled dispatch must write no archive file")
}

// TestDispatchArchive_RefusedWhileBusy proves dispatch is mutually exclusive with
// fetch and watch (archive is a peer fetch mode): a busy model shows the busy
// notice, never resolves auth, and writes no file.
func TestDispatchArchive_RefusedWhileBusy(t *testing.T) {
	cases := []struct {
		name     string
		fetching bool
		watching bool
	}{
		{"fetching", true, false},
		{"watching", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())

			bundle := depstest.NewTest(t)
			inst := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
			inst.Enable()
			f := &fakePoster{}
			m := newDispatchModel(t, f, testAccountManager(), Instances{inst})
			m.fetching = tc.fetching
			m.watching = tc.watching

			m.dispatchArchive()

			notices := dispatchNotices(f.events())
			require.Len(t, notices, 1)
			assert.Contains(t, notices[0], "cannot dispatch while a fetch or watch is running")

			entries, err := archive.Scan()
			require.NoError(t, err)
			assert.Empty(t, entries, "a refused dispatch must write no archive file")
		})
	}
}

// TestDispatchArchive_OnLoopEntry_DoesNotPostDirectly is the deadlock-safety
// guard: dispatchArchive runs ON the loop goroutine (it is a keybind action), so
// the unbuffered events channel would self-deadlock if it posted directly. With a
// deferredPoster (which captures the worker closure WITHOUT running it), the
// on-loop prologue must spawn exactly one poster.Go and post nothing — the result
// PostCritical happens only inside the deferred off-loop worker.
func TestDispatchArchive_OnLoopEntry_DoesNotPostDirectly(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	inst := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	inst.Enable()
	dp := &deferredPoster{}
	m := newDispatchModel(t, dp, testAccountManager(), Instances{inst})

	m.dispatchArchive()

	require.Len(t, dp.spawned, 1, "dispatch must spawn exactly one off-loop worker")
	assert.Empty(t, dp.posted, "the on-loop entry must NOT post directly (deadlock-safety)")
}

// oidcTokenServer returns an httptest server that answers the IAM apikey token
// exchange with a valid access token, plus the AccountManager wired to use it for
// EnvProd. The instance names must match the configured ICLInstanceConfig so
// GetAuthToken resolves them.
func oidcTokenServer(t *testing.T, _ ...string) (*icl.AccountManager, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok-ok","expires_in":3600}`))
	}))
	am := testAccountManagerWithAPIKey("api-key-value")
	am.SetOIDCForTest(icl.EnvProd, srv.URL)
	return am, srv.Close
}

// TestDispatchArchive_SuccessfulSubmits_WritesArchiveWithOnlySuccessful proves
// the happy path: two instances resolve auth and submit; the stubbed submit
// returns ids for both, so the archive file records both. A third instance whose
// submit fails is dropped (successful-only). The on-loop handler then notices.
func TestDispatchArchive_SuccessfulSubmits_WritesArchiveWithOnlySuccessful(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// Stub the submit seam: a / b succeed; c errors. Route by the CRN-derived
	// service URL so the assertion is order-independent.
	prev := dispatchSubmitFn
	t.Cleanup(func() { dispatchSubmitFn = prev })
	dispatchSubmitFn = func(_ context.Context, token, url, query string) (string, error) {
		assert.Equal(t, "tok-ok", token, "submit must receive the resolved bearer token")
		assert.Equal(t, "source logs last 7d", query, "submit must receive the current query")
		switch url {
		case "https://a.api.us-south.logs.cloud.ibm.com":
			return "qid-a", nil
		case "https://b.api.us-south.logs.cloud.ibm.com":
			return "qid-b", nil
		default:
			return "", errors.New("submit rejected")
		}
	}

	am, closeSrv := oidcTokenServer(t, "ok-a", "ok-b", "fail-c")
	defer closeSrv()

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "ok-a", "https://ok-a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	b := NewInstance(bundle, "ok-b", "https://ok-b.invalid", testCRN("b"), icl.EnvProd, "%.2f")
	c := NewInstance(bundle, "fail-c", "https://fail-c.invalid", testCRN("c"), icl.EnvProd, "%.2f")
	a.Enable()
	b.Enable()
	c.Enable()

	f := &fakePoster{} // runs Go inline -> the off-loop worker (submit + save) runs here
	m := newDispatchModel(t, f, am, Instances{a, b, c})
	m.bundle.State.SetQuery("source logs last 7d")

	m.dispatchArchive()

	// The worker posts an ArchiveDispatchedMsg; drive the on-loop handler.
	var dispatched *ArchiveDispatchedMsg
	for _, ev := range f.events() {
		if d, ok := ev.(ArchiveDispatchedMsg); ok {
			dispatched = &d
		}
	}
	require.NotNil(t, dispatched, "dispatch worker must post ArchiveDispatchedMsg")
	assert.Equal(t, 2, dispatched.Succeeded, "only the two successful submits count")
	assert.Equal(t, 3, dispatched.Attempted)
	assert.NotEmpty(t, dispatched.Saved, "a non-empty result must persist an archive")

	// The archive file on disk records ONLY the successful instances.
	entries, err := archive.Scan()
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one archive file written")
	got := entries[0].Archive
	assert.Equal(t, "source logs last 7d", got.Query)
	require.Len(t, got.Instances, 2, "only successful submits recorded")
	byCRN := map[string]archive.InstanceEntry{}
	for _, ie := range got.Instances {
		byCRN[ie.CRN] = ie
	}
	require.Contains(t, byCRN, testCRN("a"))
	require.Contains(t, byCRN, testCRN("b"))
	assert.NotContains(t, byCRN, testCRN("c"), "a failed submit must be dropped (successful-only)")
	assert.Equal(t, "qid-a", byCRN[testCRN("a")].QueryID)
	assert.Equal(t, archive.StateRunning, byCRN[testCRN("a")].State)

	// On a SUCCESSFUL dispatch the handler shows NO dialog (the success notice was
	// removed; the root switches to the Archive tab and selects the new row instead).
	before := len(f.events())
	m.OnArchiveDispatched(*dispatched)
	assert.Empty(t, dispatchNotices(f.events()[before:]),
		"a successful dispatch posts no notice (success dialog removed)")
}

// TestDispatchArchive_FlipsEnabledToInProgress proves the dispatch prologue drives
// the per-instance phase/timer machinery like a fetch: every ENABLED instance is
// flipped to AuthInProgress (padlock/spinner) with a started timer, a disabled
// instance is untouched, and the picker reports IsAnimating so the redraw ticker
// runs. Uses a deferredPoster so the off-loop submit fan-out does NOT run — only
// the on-loop prologue is exercised. Dispatch must NOT clear the displayed store.
func TestDispatchArchive_FlipsEnabledToInProgress(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	b := NewInstance(bundle, "b", "https://b.invalid", testCRN("b"), icl.EnvProd, "%.2f")
	a.Enable()
	b.Disable()
	// Seed a's store so the test can prove dispatch does not wipe displayed logs.
	a.Store.SetLogs([]icl.Log{{Data: map[string]any{"msg": "x"}, Metadata: icl.Metadata{TSMicro: 1}}})
	before := a.Store.GetLogCount()
	require.Positive(t, before, "setup: a has displayed logs")

	dp := &deferredPoster{}
	m := newDispatchModel(t, dp, testAccountManager(), Instances{a, b})
	m.dispatchArchive()

	assert.Equal(t, status.AuthInProgress, a.state, "enabled instance flips to auth/in-progress")
	assert.False(t, a.startTime.IsZero(), "enabled instance starts its timer")
	assert.Equal(t, status.Disabled, b.state, "disabled instance is untouched")
	assert.True(t, m.IsAnimating(), "the picker animates while a dispatch is in flight")
	assert.Equal(t, before, a.Store.GetLogCount(), "dispatch must NOT clear the displayed store")
	assert.Nil(t, m.backingContainer, "dispatch must NOT open a .wip backing container")
}

// TestDispatchArchive_PerInstanceBadges proves each instance flips to a terminal
// badge as its submit completes: a successful submit -> Success, a failed one ->
// Error, and once all are terminal the picker settles (AreAllQueriesDone, not
// animating). Drives the off-loop worker inline (fakePoster) and applies each
// posted DispatchInstanceDoneMsg through the on-loop handler.
func TestDispatchArchive_PerInstanceBadges(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	prev := dispatchSubmitFn
	t.Cleanup(func() { dispatchSubmitFn = prev })
	dispatchSubmitFn = func(_ context.Context, _, url, _ string) (string, error) {
		if url == "https://ok.api.us-south.logs.cloud.ibm.com" {
			return "qid-ok", nil
		}
		return "", errors.New("submit rejected")
	}

	am, closeSrv := oidcTokenServer(t, "ok", "fail")
	defer closeSrv()

	bundle := depstest.NewTest(t)
	ok := NewInstance(bundle, "ok", "https://ok.invalid", testCRN("ok"), icl.EnvProd, "%.2f")
	fail := NewInstance(bundle, "fail", "https://fail.invalid", testCRN("fail"), icl.EnvProd, "%.2f")
	ok.Enable()
	fail.Enable()

	f := &fakePoster{} // runs Go inline -> the worker (resolve + submit) runs here
	m := newDispatchModel(t, f, am, Instances{ok, fail})
	m.bundle.State.SetQuery("q")

	m.dispatchArchive()

	// Apply each per-instance done message through the on-loop handler.
	var sawOK, sawFail bool
	for _, ev := range f.events() {
		if d, isDone := ev.(DispatchInstanceDoneMsg); isDone {
			m.OnDispatchInstanceDone(d)
			switch d.CRN {
			case testCRN("ok"):
				sawOK = d.OK
			case testCRN("fail"):
				sawFail = !d.OK
			}
		}
	}
	require.True(t, sawOK, "the ok instance posts a successful DispatchInstanceDoneMsg")
	require.True(t, sawFail, "the fail instance posts a failed DispatchInstanceDoneMsg")

	assert.Equal(t, status.Success, ok.state, "a clean submit shows a Success badge")
	assert.Equal(t, status.Error, fail.state, "a failed submit shows an Error badge")
	assert.True(t, m.instances.AreAllQueriesDone(), "all dispatched instances are terminal")
	assert.False(t, m.IsAnimating(), "the picker settles once every submit completes")
	// Timers are frozen (terminal phases freeze lastUpdateTime).
	assert.False(t, ok.lastUpdateTime.IsZero(), "the success timer is frozen at completion")
}

// TestDispatchArchive_LocksFetchesWhileInFlight proves Fix D: dispatch flips
// m.fetching so a fetch/watch (and a second dispatch) is refused while the submit
// batch is in flight. A deferredPoster holds the worker so the dispatch stays
// in-flight. The lock is released on settle (see ReleasesLockOnSettle) or cancel.
func TestDispatchArchive_LocksFetchesWhileInFlight(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	a.Enable()
	dp := &deferredPoster{}
	m := newDispatchModel(t, dp, testAccountManager(), Instances{a})

	require.False(t, m.IsFetching(), "setup: not fetching before dispatch")
	m.dispatchArchive()

	assert.True(t, m.IsFetching(), "dispatch flips m.fetching so fetches are locked out")
	assert.False(t, m.instances.AreAllQueriesDone(),
		"the FetchLogs keybind's !AreAllQueriesDone() guard also blocks a fetch in-flight")

	// A second dispatch while one is in flight is refused at the busy guard: it shows
	// the busy notice (a poster.Go the deferredPoster captures) and spawns NO new
	// submit worker. Run only the closures the SECOND dispatch captured (running the
	// first dispatch's submit worker would hit the network).
	spawnedBefore := len(dp.spawned)
	m.dispatchArchive()
	for _, fn := range dp.spawned[spawnedBefore:] {
		fn(context.Background())
	}
	notices := dispatchNotices(dp.posted)
	require.NotEmpty(t, notices, "the refused second dispatch shows a notice")
	assert.Contains(t, notices[len(notices)-1], "cannot dispatch while a fetch or watch is running")
}

// TestDispatchArchive_ReleasesLockOnSettle proves the fetch-lock is released once
// the whole submit batch settles (the last DispatchInstanceDoneMsg), so a fetch is
// allowed again. Drives the worker inline (fakePoster) and applies every per-
// instance done message.
func TestDispatchArchive_ReleasesLockOnSettle(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	prev := dispatchSubmitFn
	t.Cleanup(func() { dispatchSubmitFn = prev })
	dispatchSubmitFn = func(_ context.Context, _, _, _ string) (string, error) { return "qid", nil }

	am, closeSrv := oidcTokenServer(t, "a", "b")
	defer closeSrv()
	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	b := NewInstance(bundle, "b", "https://b.invalid", testCRN("b"), icl.EnvProd, "%.2f")
	a.Enable()
	b.Enable()
	f := &fakePoster{}
	m := newDispatchModel(t, f, am, Instances{a, b})
	m.bundle.State.SetQuery("q")

	m.dispatchArchive()
	require.True(t, m.IsFetching(), "locked during the batch")

	// Apply the per-instance done messages; the lock must NOT release until the LAST.
	var doneMsgs []DispatchInstanceDoneMsg
	for _, ev := range f.events() {
		if d, ok := ev.(DispatchInstanceDoneMsg); ok {
			doneMsgs = append(doneMsgs, d)
		}
	}
	require.Len(t, doneMsgs, 2, "one done message per enabled instance")
	m.OnDispatchInstanceDone(doneMsgs[0])
	assert.True(t, m.IsFetching(), "lock held while a sibling submit is still in flight")
	m.OnDispatchInstanceDone(doneMsgs[1])
	assert.False(t, m.IsFetching(), "lock released once the whole batch settles")
	assert.True(t, m.instances.AreAllQueriesDone(), "a subsequent fetch is now allowed")
}

// TestDispatchArchive_CancelReleasesLock proves an interrupted dispatch
// (cancelAllFetches) releases the fetch-lock — dispatch opens no .wip, so the
// normal finalize path never clears m.fetching.
func TestDispatchArchive_CancelReleasesLock(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	a.Enable()
	dp := &deferredPoster{} // hold the worker so the dispatch stays in flight
	m := newDispatchModel(t, dp, testAccountManager(), Instances{a})
	m.authCtx, m.cancelAuth = context.WithCancel(context.Background())

	m.dispatchArchive()
	require.True(t, m.IsFetching(), "locked during the batch")

	m.cancelAllFetches()
	assert.False(t, m.IsFetching(), "cancelling an interrupted dispatch releases the fetch-lock")
}

// TestDispatchArchive_AllSubmitsFail_NoFile_DistinctNotice proves the all-failed
// path is distinct from zero-enabled: instances are enabled but every submit
// errors, so no file is written and the handler reports all-submits-failed.
func TestDispatchArchive_AllSubmitsFail_NoFile_DistinctNotice(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	prev := dispatchSubmitFn
	t.Cleanup(func() { dispatchSubmitFn = prev })
	dispatchSubmitFn = func(_ context.Context, _, _, _ string) (string, error) {
		return "", errors.New("submit rejected")
	}

	am, closeSrv := oidcTokenServer(t, "a")
	defer closeSrv()

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	a.Enable()
	f := &fakePoster{}
	m := newDispatchModel(t, f, am, Instances{a})
	m.bundle.State.SetQuery("q")

	m.dispatchArchive()

	var dispatched *ArchiveDispatchedMsg
	for _, ev := range f.events() {
		if d, ok := ev.(ArchiveDispatchedMsg); ok {
			dispatched = &d
		}
	}
	require.NotNil(t, dispatched)
	assert.Equal(t, 0, dispatched.Succeeded)
	assert.Empty(t, dispatched.Saved, "no archive written when every submit failed")

	entries, err := archive.Scan()
	require.NoError(t, err)
	assert.Empty(t, entries, "all-submits-failed must write no archive file")

	m.OnArchiveDispatched(*dispatched)
	notices := dispatchNotices(f.events())
	require.NotEmpty(t, notices)
	assert.Contains(t, notices[len(notices)-1], "all submits failed",
		"all-submits-failed notice is distinct from zero-enabled")
}
