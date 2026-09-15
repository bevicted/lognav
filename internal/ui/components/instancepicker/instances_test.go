package instancepicker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAccountManager() *icl.AccountManager {
	return icl.NewAccountManager(config.New().ICL.Environments)
}

func testAccountManagerWithAPIKey(key string) *icl.AccountManager {
	environments := config.New().ICL.Environments
	production := environments[string(icl.EnvProd)]
	production.APIKey = key
	environments[string(icl.EnvProd)] = production
	return icl.NewAccountManager(environments)
}

func newTestInstance(t *testing.T, name string) *Instance {
	t.Helper()
	return NewInstance(depstest.NewTest(t), name, "https://example.invalid", testCRN(name), icl.Environment("test"), "%.2f")
}

func newTestInstanceEnv(t *testing.T, name string, env icl.Environment) *Instance {
	t.Helper()
	return NewInstance(depstest.NewTest(t), name, "https://example.invalid", testCRN(name), env, "%.2f")
}

// TestPasscodeDialog_PlainURL_NoANSIEscapes guards the regression where the
// passcode dialog built an SGR+OSC8-wrapped URL string and stuffed it into the
// dialog Message. The dialog renders cell-natively (no SGR/OSC8 decode), so the
// escapes printed as literal garbage. The Message must carry the plain URL.
func TestPasscodeDialog_PlainURL_NoANSIEscapes(t *testing.T) {
	t.Parallel()
	const url = "https://example.invalid/passcode?token=abc"
	pr := icl.NewPasscodeRequired(url, icl.EnvProd)
	f := &fakePoster{} // runs Go inline, records PostCritical
	// am is unused by this test (it never clicks OK), but the signature needs it.
	passcodeDialogCmd(t.Context(), f, testAccountManager(), pr)

	var dlg msgs.ShowDialogMsg
	var found bool
	for _, ev := range f.events() {
		if d, ok := ev.(msgs.ShowDialogMsg); ok {
			dlg, found = d, true
		}
	}
	require.True(t, found, "passcodeDialogCmd must post a ShowDialogMsg")
	assert.Contains(t, dlg.Message, url, "dialog must show the plain passcode URL")
	assert.NotContains(t, dlg.Message, "\x1b",
		"dialog Message must contain no ANSI escape bytes (cell-native dialog renders them literally)")
	assert.Equal(t, url, dlg.LinkURL,
		"LinkURL must mark the URL line so the dialog renders it as a colored hyperlink")
}

// TestInstanceUpdateStateTransitions covers the chunk/done error and warning
// paths. Regression guard: chunk-level errors (server failures returned inside
// a successful HTTP response) must mark the instance as Error rather than
// being silently overwritten to Success when the stream closes cleanly.
func TestInstanceUpdateStateTransitions(t *testing.T) {
	t.Parallel()

	// The two concrete handlers are called directly (the generic Instance.Update
	// type switch is gone), so a step carries the call rather than a message.
	chunk := func(msg *msgs.LogStreamMsg) func(*Instance) {
		return func(i *Instance) { i.handleLogStreamMsg(msg) }
	}
	streamDone := func(msg *msgs.LogStreamDoneMsg) func(*Instance) {
		return func(i *Instance) { i.handleLogStreamDoneMsg(msg) }
	}

	type step struct {
		apply func(*Instance)
		want  status.Phase
	}

	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "chunk_error_then_clean_close_stays_error",
			steps: []step{
				{chunk(&msgs.LogStreamMsg{Errs: []string{"boom"}}), status.Error},
				{streamDone(&msgs.LogStreamDoneMsg{}), status.Error},
			},
		},
		{
			name: "chunk_warn_then_clean_close_stays_warning",
			steps: []step{
				{chunk(&msgs.LogStreamMsg{Warns: []string{"slow"}}), status.Warning},
				{streamDone(&msgs.LogStreamDoneMsg{}), status.Warning},
			},
		},
		{
			name: "chunk_error_not_downgraded_by_later_warn",
			steps: []step{
				{chunk(&msgs.LogStreamMsg{Errs: []string{"boom"}}), status.Error},
				{chunk(&msgs.LogStreamMsg{Warns: []string{"slow"}}), status.Error},
				{streamDone(&msgs.LogStreamDoneMsg{}), status.Error},
			},
		},
		{
			name: "done_error_sets_error",
			steps: []step{
				{chunk(&msgs.LogStreamMsg{}), status.InProgress},
				{streamDone(&msgs.LogStreamDoneMsg{Errs: []string{"transport"}}), status.Error},
			},
		},
		{
			name: "clean_stream_succeeds",
			steps: []step{
				{chunk(&msgs.LogStreamMsg{}), status.InProgress},
				{streamDone(&msgs.LogStreamDoneMsg{}), status.Success},
			},
		},
		{
			name: "cancelled_state_unchanged_by_chunk_or_done",
			steps: []step{
				{chunk(&msgs.LogStreamMsg{Errs: []string{"boom"}}), status.Cancelled},
				{streamDone(&msgs.LogStreamDoneMsg{Errs: []string{"transport"}}), status.Cancelled},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inst := newTestInstance(t, tc.name)
			if tc.name == "cancelled_state_unchanged_by_chunk_or_done" {
				inst.state = status.Cancelled
			} else {
				inst.state = status.InProgress
			}
			for i, s := range tc.steps {
				s.apply(inst)
				assert.Equal(t, s.want, inst.state, "step %d", i)
			}
		})
	}
}

// TestInstance_FinishDispatch_DoesNotResurrectCancelled guards the regression where a late
// DispatchInstanceDoneMsg flipped a row that cancelAllFetches had already frozen to
// Cancelled back to a terminal Success/Error badge (mirrors the handleLogStreamDoneMsg
// cancel guard). A non-cancelled row must still transition normally.
func TestInstance_FinishDispatch_DoesNotResurrectCancelled(t *testing.T) {
	t.Parallel()

	cancelled := newTestInstance(t, "cancelled")
	cancelled.state = status.Cancelled
	cancelled.FinishDispatch(true)
	assert.Equal(t, status.Cancelled, cancelled.state, "a cancelled dispatch row must not be resurrected on a late submit-done")

	live := newTestInstance(t, "live")
	live.state = status.AuthInProgress
	live.FinishDispatch(true)
	assert.Equal(t, status.Success, live.state, "a non-cancelled row must still flip to its terminal badge")
}

func TestInstance_Close_CancelsQueryAndTransitionsToCancelled(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	inst := NewInstance(bundle, "test-inst", "https://example.invalid", testCRN("test"), icl.EnvProd, "%.2fs")

	// Synthesize an InProgress state with a known cancel hook.
	cancelled := false
	inst.cancelQuery = func() { cancelled = true }
	inst.state = status.InProgress

	require.NoError(t, inst.Close())
	assert.True(t, cancelled, "cancelQuery should have been invoked")
	assert.Nil(t, inst.cancelQuery, "cancelQuery should be nil after Close")
	assert.Equal(t, status.Cancelled, inst.state)

	t.Run("second_call_is_noop", func(t *testing.T) {
		require.NoError(t, inst.Close())
		assert.Nil(t, inst.cancelQuery)
		// State stays Cancelled, not retransitioned.
		assert.Equal(t, status.Cancelled, inst.state)
	})
}

// deferredPoster captures spawned goroutine bodies instead of running them
// inline, so a test can assert how many goroutines were spawned (and the
// on-loop prologue effects) without executing the off-loop body (which in
// StartQuery would attempt a real network call via icl.Query). PostCritical
// records delivered events for assertions.
type deferredPoster struct {
	spawned []func(context.Context)
	posted  []uv.Event
}

func (d *deferredPoster) Go(fn func(context.Context)) {
	d.spawned = append(d.spawned, fn)
}

func (d *deferredPoster) PostCritical(_ context.Context, ev uv.Event) error {
	d.posted = append(d.posted, ev)
	return nil
}

func (d *deferredPoster) Post(uv.Event) {}

// TestInstance_StartQuery_RunsPrologueOnLoopAndSpawnsOneGoroutine proves the R4
// conversion of MakeQueryCMD -> StartQuery: the single-writer prologue (cancel
// handle set, StartStream's queryID bump) runs synchronously on the loop
// goroutine before any goroutine spawns, and the query body is dispatched via
// exactly one poster.Go.
func TestInstance_StartQuery_RunsPrologueOnLoopAndSpawnsOneGoroutine(t *testing.T) {
	t.Parallel()

	inst := newTestInstance(t, "q-inst")
	beforeID := inst.Store.GetQueryID()

	dp := &deferredPoster{}
	inst.StartQuery("token", "source logs", 0, func(uv.Event) {}, dp)

	// On-loop prologue effects are visible synchronously (before the goroutine runs).
	assert.NotNil(t, inst.cancelQuery, "cancelQuery must be set on-loop")
	assert.Equal(t, beforeID+1, inst.Store.GetQueryID(), "StartStream must bump queryID on-loop")

	// The query body was dispatched via exactly one tracked goroutine.
	require.Len(t, dp.spawned, 1, "StartQuery must spawn exactly one poster.Go")
	// Nothing is posted synchronously; the body (not run here) self-delivers via the callback.
	assert.Empty(t, dp.posted, "StartQuery must not post synchronously")
}

// TestInstances_ResolveTokens_PrologueOnLoop covers the on-loop prologue of the
// ResolveTokensCMD -> ResolveTokens conversion: enabled instances flip to
// AuthInProgress (so AreAllQueriesDone reads them as busy) and disabled instances
// have their timers reset — all synchronously, before the auth goroutine spawns.
// Two enabled instances share one env, so exactly one resolveEnvMembers goroutine
// is spawned.
func TestInstances_ResolveTokens_PrologueOnLoop(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	enabled := NewInstance(bundle, "enabled", "https://example.invalid", testCRN("e"), icl.EnvProd, "%.2fs")
	disabled := NewInstance(bundle, "disabled", "https://example.invalid", testCRN("d"), icl.EnvProd, "%.2fs")
	enabled.Enable()
	enabled.flushedLogCount = 1
	enabled.flushedLogsSize = 42
	disabled.Disable()
	insts := Instances{enabled, disabled}

	am := testAccountManager()
	dp := &deferredPoster{}
	insts.ResolveTokens(t.Context(), 0, am, "source logs", dp)

	// On-loop prologue: enabled -> AuthInProgress, disabled -> not busy.
	assert.Equal(t, status.AuthInProgress, enabled.state, "enabled instance must be AuthInProgress on-loop")
	assert.False(t, insts.AreAllQueriesDone(), "AreAllQueriesDone must see the AuthInProgress instance")
	assert.NotEqual(t, status.AuthInProgress, disabled.state, "disabled instance must not be AuthInProgress")
	assert.Zero(t, enabled.flushedLogsSize, "a new fetch must clear the previous durable size")

	// One env (EnvProd) -> exactly one tracked goroutine; nothing posted yet.
	require.Len(t, dp.spawned, 1, "ResolveTokens must spawn one poster.Go per env")
	assert.Empty(t, dp.posted, "ResolveTokens must not post synchronously")
}

// TestInstances_ResolveTokens_DemotesCancelledToDisabled covers the cancelled
// edge of the instance selection state machine: a fetch (the ResolveTokens
// prologue) demotes a Cancelled instance to Disabled ("cancelled -fetch> not
// selected") and never re-queries it. The cancelled badge is one-shot — it
// survives a single fetch cycle then clears to a plain disabled row, while a
// genuinely Enabled instance still flips to InProgress.
func TestInstances_ResolveTokens_DemotesCancelledToDisabled(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	cancelled := NewInstance(bundle, "cancelled", "https://example.invalid", testCRN("c"), icl.EnvProd, "%.2fs")
	enabled := NewInstance(bundle, "enabled", "https://example.invalid", testCRN("e"), icl.EnvProd, "%.2fs")
	cancelled.state = status.Cancelled
	enabled.Enable()
	insts := Instances{cancelled, enabled}

	am := testAccountManager()
	dp := &deferredPoster{}
	insts.ResolveTokens(t.Context(), 0, am, "source logs", dp)

	assert.Equal(t, status.Disabled, cancelled.state, "fetch must demote a Cancelled instance to Disabled")
	assert.Equal(t, status.AuthInProgress, enabled.state, "enabled instance must still flip to AuthInProgress")
}

// TestInstances_ResolveTokens_ResetsStaleTimerOnRefetch covers the timer-jump
// regression: on a re-fetch an enabled instance still holds the startTime from
// its PREVIOUS fetch. The ResolveTokens prologue flips it to AuthInProgress
// on-loop via StartAuthTimer, which records a FRESH startTime so the auth-run
// elapsed counts from zero — not time.Since(old_startTime), a large elapsed
// (seconds-to-thousands) that visibly flashes during a slow refresh-token auth.
func TestInstances_ResolveTokens_ResetsStaleTimerOnRefetch(t *testing.T) {
	t.Parallel()

	bundle := depstest.NewTest(t)
	enabled := NewInstance(bundle, "enabled", "https://example.invalid", testCRN("e"), icl.EnvProd, "%.2f")
	enabled.Enable()
	// Simulate a prior fetch: a startTime well in the past, as StartTimer left it.
	enabled.startTime = time.Now().Add(-5 * time.Second)
	enabled.lastUpdateTime = enabled.startTime
	insts := Instances{enabled}

	am := testAccountManager()
	dp := &deferredPoster{}
	insts.ResolveTokens(t.Context(), 0, am, "source logs", dp)

	// Prologue marks it AuthInProgress with a fresh startTime: the rendered
	// elapsed must be ~0, not the stale ~5s carried over from the previous fetch.
	require.Equal(t, status.AuthInProgress, enabled.state)
	assert.InDelta(t, 0, parseElapsed(t, enabled.RenderTimer()), 0.1,
		"re-fetch must reset the stale startTime so the auth-run window renders ~0, not the prior fetch's elapsed")
}

// TestInstance_Toggle_SelectionStates locks the selection half of the cancelled
// state machine: Enabled<->Disabled round-trips, and a Cancelled instance
// toggles back to Enabled ("cancelled -select> selected").
func TestInstance_Toggle_SelectionStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		from status.Phase
		want status.Phase
	}{
		{"enabled_toggles_to_disabled", status.Enabled, status.Disabled},
		{"disabled_toggles_to_enabled", status.Disabled, status.Enabled},
		{"cancelled_toggles_to_enabled", status.Cancelled, status.Enabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inst := newTestInstance(t, tc.name)
			inst.state = tc.from
			inst.Toggle()
			assert.Equal(t, tc.want, inst.state)
		})
	}
}

// TestInstance_RenderTimer_LiveWhileInProgress_FreezesOnDone proves the R4 timer
// model: while InProgress the rendered elapsed is live (time.Since(startTime),
// strictly advancing across reads), and after a clean stream-done it freezes at
// lastUpdateTime-startTime (stable across reads). The %.2f format yields seconds.
func TestInstance_RenderTimer_LiveWhileInProgress_FreezesOnDone(t *testing.T) {
	t.Parallel()

	inst := newTestInstance(t, "timer")
	inst.state = status.InProgress
	// Anchor startTime far in the past so both live reads are clearly nonzero
	// and monotonically advancing without relying on sleeps.
	inst.startTime = time.Now().Add(-5 * time.Second)

	first := inst.RenderTimer()
	assert.NotEqual(t, "0.00", first, "live timer should be nonzero with a past startTime")
	firstSecs := parseElapsed(t, first)

	// Two reads must advance (time.Since keeps growing). EventuallyWithT polls
	// until a strictly later read is observed (no fixed Sleep). Compare the parsed
	// float seconds, not the %.2f strings — string comparison is only equivalent
	// to numeric within a single digit-count band (9.99 -> 10.00 would invert).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Greater(c, parseElapsed(t, inst.RenderTimer()), firstSecs, "live timer must advance across reads")
	}, 2*time.Second, 5*time.Millisecond)

	// Clean stream-done freezes the timer at the true final elapsed.
	inst.handleLogStreamDoneMsg(&msgs.LogStreamDoneMsg{})
	require.Equal(t, status.Success, inst.state)

	frozen := inst.RenderTimer()
	// Frozen value is stable: rendering again (after wall-clock advances) returns
	// the same string because it is no longer InProgress.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, frozen, inst.RenderTimer(), "frozen timer must be stable across reads")
	}, 100*time.Millisecond, 5*time.Millisecond)
	// And it equals the captured lastUpdateTime - startTime.
	assert.Equal(t, FormatElapsed(inst.timerFormat, inst.lastUpdateTime.Sub(inst.startTime)), frozen)
}

// parseElapsed parses a RenderTimer()/FormatElapsed seconds string (e.g. "5.00")
// back into a float so numeric comparisons are not subject to the %.2f string's
// digit-count band ("9.99" < "10.00" numerically but not lexicographically).
func parseElapsed(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	require.NoError(t, err, "RenderTimer output %q must parse as a float", s)
	return f
}

// TestInstance_RenderTimer_InProgressBeforeStartTimer_RendersZero guards the
// pre-start window regression: an instance can be InProgress before StartTimer
// has recorded a startTime. In that window startTime is still the zero value, so
// a live time.Since(startTime) saturates to math.MaxInt64 ns and renders the
// 9223372036.85 garbage. The fix guards the live branch on !startTime.IsZero(),
// falling through to the frozen formula (zero.Sub(zero) == 0) and rendering zero
// elapsed.
func TestInstance_RenderTimer_InProgressBeforeStartTimer_RendersZero(t *testing.T) {
	t.Parallel()

	inst := newTestInstance(t, "pre-start")
	// State as ResolveTokens leaves it before StartTimer runs: InProgress with
	// both time fields at the zero value.
	inst.state = status.InProgress
	inst.startTime = time.Time{}
	inst.lastUpdateTime = time.Time{}

	got := inst.RenderTimer()
	assert.NotContains(t, got, "9223372036", "must not flash the MaxInt64-ns saturated elapsed")
	assert.InDelta(t, 0, parseElapsed(t, got), 0.001, "pre-start elapsed must render as zero")
}

// TestInstance_RenderTimer_FreezesOnCancel proves the cancel-freeze regression
// fix: an InProgress instance with a past startTime, when CancelQuery transitions
// it to Cancelled, freezes RenderTimer at the elapsed-until-cancel (a non-"0.00"
// value) and that frozen value is stable across reads. Before the fix the
// LogStreamDoneMsg path early-returned on Cancelled, leaving lastUpdateTime ==
// startTime so RenderTimer rendered "0.00".
func TestInstance_RenderTimer_FreezesOnCancel(t *testing.T) {
	t.Parallel()

	inst := newTestInstance(t, "cancel-timer")
	inst.state = status.InProgress
	inst.startTime = time.Now().Add(-3 * time.Second)
	inst.lastUpdateTime = inst.startTime // as StartTimer leaves it
	inst.cancelQuery = func() {}         // satisfy CancelQuery's non-nil guard

	inst.CancelQuery()
	require.Equal(t, status.Cancelled, inst.state)

	frozen := inst.RenderTimer()
	assert.NotEqual(t, "0.00", frozen, "cancelled timer must freeze at elapsed-until-cancel, not 0.00")
	assert.Greater(t, parseElapsed(t, frozen), 0.0, "frozen elapsed must be positive")

	// Stable across reads (no longer InProgress, so wall-clock advance is ignored).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, frozen, inst.RenderTimer(), "frozen cancel timer must be stable across reads")
	}, 100*time.Millisecond, 5*time.Millisecond)
}

// TestInstances_AreAllQueriesDone verifies the predicate underpinning IsAnimating:
// false while any instance is InProgress, true when none are.
func TestInstances_AreAllQueriesDone(t *testing.T) {
	t.Parallel()

	a := newTestInstance(t, "a")
	b := newTestInstance(t, "b")
	insts := Instances{a, b}

	a.state = status.InProgress
	b.state = status.Enabled
	assert.False(t, insts.AreAllQueriesDone(), "in-progress instance => not done")

	a.state = status.Success
	assert.True(t, insts.AreAllQueriesDone(), "no in-progress instance => all done")
}

// TestInstances_ResolveTokens_PostsResults exercises the off-loop per-env
// delivery via PostCritical for two outcomes: all-disabled (no env groups, no
// goroutine, nothing posted) and an unconfigured first member (plain error ->
// EnvCredFailedMsg, since the first member exercises the shared env credential).
func TestInstances_ResolveTokens_PostsResults(t *testing.T) {
	t.Parallel()

	t.Run("no_enabled_pairs_posts_nothing", func(t *testing.T) {
		t.Parallel()
		bundle := depstest.NewTest(t)
		disabled := NewInstance(bundle, "disabled", "https://example.invalid", testCRN("d"), icl.EnvProd, "%.2fs")
		disabled.Disable()
		insts := Instances{disabled}

		am := testAccountManager()
		f := &fakePoster{} // runs Go inline, records PostCritical
		insts.ResolveTokens(t.Context(), 0, am, "source logs", f)

		assert.Empty(t, f.events(), "no enabled instances -> no env groups -> nothing posted")
	})

	t.Run("first_member_without_credentials_posts_PasscodeRequired", func(t *testing.T) {
		t.Parallel()
		bundle := depstest.NewTest(t)
		// A CRN without any configured credentials must reach the interactive
		// passcode path without performing real IAM discovery.
		enabled := NewInstance(bundle, "unconfigured-name", "https://example.invalid", testCRN("e"), icl.EnvProd, "%.2fs")
		enabled.Enable()
		insts := Instances{enabled}

		am := testAccountManager()
		am.SetOIDCForTest(icl.EnvProd, "http://unused.invalid/token")
		f := &fakePoster{}
		insts.ResolveTokens(t.Context(), 0, am, "source logs", f)

		evs := f.events()
		require.Len(t, evs, 1)
		_, ok := evs[0].(PasscodeRequiredMsg)
		require.True(t, ok, "expected PasscodeRequiredMsg, got %T", evs[0])
	})
}

func TestInstance_StartAuthTimer_LiveRenders(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")

	inst.StartAuthTimer()
	assert.Equal(t, status.AuthInProgress, inst.state)
	assert.False(t, inst.startTime.IsZero(), "auth timer should record startTime")

	// RenderTimer must treat AuthInProgress as live (non-zero elapsed possible),
	// not frozen-at-zero.
	got := inst.RenderTimer()
	assert.NotEmpty(t, got)
}

func TestInstances_AreAllQueriesDone_CountsAuthInProgress(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")
	inst.state = status.AuthInProgress
	insts := Instances{inst}
	assert.False(t, insts.AreAllQueriesDone(), "AuthInProgress must count as busy")
}

func TestInstance_RestoreSnapshot_DemotesAuthInProgress(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")
	inst.RestoreSnapshot(snapshot.InstanceSnapshot{State: int(status.AuthInProgress)})
	assert.Equal(t, status.Enabled, inst.state, "restored AuthInProgress must demote to Enabled")
}

func TestInstance_Toggle_AuthInProgress_Cancels(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")
	inst.StartAuthTimer() // AuthInProgress
	inst.Toggle()
	assert.Equal(t, status.Cancelled, inst.state)
	assert.False(t, inst.lastUpdateTime.IsZero(), "cancel must freeze the timer")
}

func TestInstances_AreAllQueriesDone_CountsWatching(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")
	inst.state = status.Watching
	insts := Instances{inst}
	assert.False(t, insts.AreAllQueriesDone(),
		"a Watching instance must count as a query in progress (keeps finalize/notify suppressed)")
}

// TestInstance_SnapshotRoundtrip_PreservesFetchErrorMessage proves that an
// errored instance's fetch-error text (shown by the logviewer when the store
// has no logs) is captured into the snapshot and restored back into the store's
// message field, so reopening a restored errored instance surfaces the error.
func TestInstance_SnapshotRoundtrip_PreservesFetchErrorMessage(t *testing.T) {
	t.Parallel()
	const msg = "query failed: 401 unauthorized (token expired)"

	src := newTestInstance(t, "a")
	src.state = status.Error
	src.flushedLogsSize = 42
	src.Store.SetMessage(msg)

	snap := src.SnapshotizeMeta()
	require.Equal(t, msg, snap.Message, "SnapshotizeMeta must capture the store's fetch-error message")
	assert.Equal(t, uint64(42), snap.LogsSizeBytes)

	dst := newTestInstance(t, "a")
	dst.RestoreSnapshot(snap)
	assert.Equal(t, status.Error, dst.state)
	assert.Equal(t, uint64(42), dst.flushedLogsSize)
	assert.Equal(t, msg, dst.Store.GetMessage(),
		"RestoreSnapshot must put the fetch error back into the logviewer message field")
}

func TestInstance_RestoreSnapshot_DemotesWatching(t *testing.T) {
	t.Parallel()
	inst := newTestInstance(t, "a")
	inst.RestoreSnapshot(snapshot.InstanceSnapshot{CRN: "a", State: int(status.Watching)})
	assert.Equal(t, status.Enabled, inst.state,
		"a snapshot captured mid-watch must restore as Enabled, not stuck in Watching")
}

// bgDataEvent is one public /data SSE event carrying a single result row.
const bgDataEvent = `data: {"response":{"results":{"results":[{"metadata":[{"key":"timestamp","value":"2026-06-20T15:04:05.000000"}],"labels":[{"key":"applicationname","value":"app"}],"user_data":"{\"log\":\"hi\"}"}]}}}` + "\n\n"

// TestInstance_StartQuery_SyncSourceUnchanged proves the empty (zero-value)
// querySource keeps the sync icl.Query routing: every current caller passes the
// empty source, so the off-loop body hits the example.invalid URL (icl.Query) and
// reports a transport error via the callback — NOT FetchBackgroundData.
func TestInstance_StartQuery_SyncSourceUnchanged(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The sync (empty-source) path must hit the sync /v1/query endpoint, never
		// the background /data endpoint.
		assert.Contains(t, r.URL.Path, "/v1/query")
		assert.NotContains(t, r.URL.Path, "background_query")
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	// StartQuery uses icl's shared HTTP client. Disable keep-alives so this
	// test's connection is closed rather than retained by that client.
	srv.Config.SetKeepAlivesEnabled(false)
	t.Cleanup(srv.Close)

	inst := newTestInstance(t, "sync")
	inst.url = srv.URL
	// Zero-value source => sync icl.Query routing (default for every current caller).
	require.Equal(t, querySource{}, inst.source)
	var done bool
	send := func(ev uv.Event) {
		if _, ok := ev.(*msgs.LogStreamDoneMsg); ok {
			done = true
		}
	}
	inst.StartQuery("tok", "source logs", 0, send, &fakePoster{}) // fakePoster runs Go inline
	assert.True(t, done, "sync source must complete the stream via icl.Query (/v1/query)")
}

// TestInstance_StartQuery_SyncSourceHonorsMaxRows verifies Logs.MaxRows limits
// normal synchronous fetch storage even though ICL returns up to 50,000 rows.
func TestInstance_StartQuery_SyncSourceHonorsMaxRows(t *testing.T) {
	t.Parallel()
	const sse = "data: {\"result\":{\"results\":[{\"user_data\":\"{}\"},{\"user_data\":\"{}\"}]}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)

	bundle := depstest.NewTest(t)
	bundle.Config.Logs.MaxRows = 1
	inst := NewInstance(bundle, "sync-capped", srv.URL, testCRN("sync-capped"), icl.EnvProd, "%.2f")
	inst.Store.SetPoster(&fakePoster{})
	send := func(ev uv.Event) {
		switch msg := ev.(type) {
		case *msgs.LogStreamMsg:
			inst.Store.HandleLogStreamMsg(msg)
		case *msgs.LogStreamDoneMsg:
			inst.Store.HandleLogStreamDoneMsg(msg)
		}
	}

	inst.StartQuery("tok", "source logs", bundle.Config.Logs.MaxRows, send, &fakePoster{})

	assert.Equal(t, 1, inst.Store.GetLogCount(), "normal fetch must retain Logs.MaxRows rows")
}

func TestInstance_StartQuery_SyncSourceUsesConfiguredRequestLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		maxRows uint32
		want    int64
	}{
		{name: "zero uses default", maxRows: 0, want: icl.SyncQueryLimit},
		{name: "lower positive", maxRows: 42, want: 42},
		{name: "above synchronous ceiling", maxRows: icl.SyncQueryLimit + 1, want: icl.SyncQueryLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Metadata struct {
					Limit int64 `json:"limit"`
				} `json:"metadata"`
			}
			var decodeErr error
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				decodeErr = json.NewDecoder(r.Body).Decode(&got)
				w.Header().Set("Content-Type", "text/event-stream")
			}))
			t.Cleanup(srv.Close)

			inst := newTestInstance(t, "sync-limit")
			inst.url = srv.URL
			inst.StartQuery("tok", "source logs", tc.maxRows, func(uv.Event) {}, &fakePoster{})
			require.NoError(t, decodeErr)
			assert.Equal(t, tc.want, got.Metadata.Limit)
		})
	}
}

// TestInstance_StartQuery_BackgroundSourceRoutesToFetchBackgroundData proves a
// non-empty querySource{queryID} reroutes the StartQuery off-loop body to the
// public background data SSE endpoint and delivers rows via QueryCallback.
func TestInstance_StartQuery_BackgroundSourceRoutesToFetchBackgroundData(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "background_query/qid-1234/data",
			"a background source must hit the /data endpoint, not /v1/query")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(bgDataEvent))
	}))
	// StartQuery uses icl's shared HTTP client. Disable keep-alives so this
	// test's connection is closed rather than retained by that client.
	srv.Config.SetKeepAlivesEnabled(false)
	t.Cleanup(srv.Close)

	inst := newTestInstance(t, "bg")
	inst.url = srv.URL
	inst.source = querySource{queryID: "qid-1234"} // non-empty => background routing

	var rows int
	var sawErr bool
	send := func(ev uv.Event) {
		if m, ok := ev.(*msgs.LogStreamMsg); ok {
			rows += len(m.Logs)
			if len(m.Errs) > 0 {
				sawErr = true
			}
		}
	}
	inst.StartQuery("tok", "ignored-for-background", 0, send, &fakePoster{})
	assert.False(t, sawErr, "background SSE must decode without a stream error")
	assert.Equal(t, 1, rows, "the single SSE event's row must be delivered via OnData")
}

// TestInstance_StartQuery_BackgroundSourceCapsAtFixedCollectLimit verifies that
// v1 collect retains its 50,000-row ceiling even though the server can retain
// more. A full batch followed by another row exercises both StartQuery's fixed
// fetch cap and the pre-sized LogStore hard cap.
func TestInstance_StartQuery_BackgroundSourceCapsAtFixedCollectLimit(t *testing.T) {
	t.Parallel()

	const (
		row    = `{"user_data":"{}"}`
		prefix = `data: {"response":{"results":{"results":[`
		suffix = `]}}}`
	)
	body := prefix + strings.TrimSuffix(strings.Repeat(row+",", int(icl.SyncQueryLimit)), ",") + suffix + "\n\n" +
		prefix + row + suffix + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	inst := newTestInstance(t, "bg-capped")
	inst.url = srv.URL
	inst.source = querySource{queryID: "qid-capped"}
	inst.Store.SetPoster(&fakePoster{})
	delivered := 0
	send := func(ev uv.Event) {
		switch msg := ev.(type) {
		case *msgs.LogStreamMsg:
			delivered += len(msg.Logs)
			inst.Store.HandleLogStreamMsg(msg)
		case *msgs.LogStreamDoneMsg:
			inst.Store.HandleLogStreamDoneMsg(msg)
		}
	}

	inst.StartQuery("tok", "ignored-for-background", 1, send, &fakePoster{})

	assert.Equal(t, int(icl.SyncQueryLimit), delivered,
		"collect must stop background delivery at the fixed 50,000-row limit")
	assert.Equal(t, int(icl.SyncQueryLimit), inst.Store.GetLogCount(),
		"collect must retain exactly the fixed 50,000-row limit")
}

// TestInstances_ClearSources_ResetsToSyncDefault proves ClearSources zeroes every
// instance's source so a sync fetch after a collect never inherits a stale
// queryId (which would mis-route StartQuery to FetchBackgroundData).
func TestInstances_ClearSources_ResetsToSyncDefault(t *testing.T) {
	t.Parallel()
	a := newTestInstance(t, "a")
	b := newTestInstance(t, "b")
	a.source = querySource{queryID: "stale-a"}
	b.source = querySource{queryID: "stale-b"}
	insts := Instances{a, b}

	insts.ClearSources()
	assert.Equal(t, querySource{}, a.source, "ClearSources must reset instance a")
	assert.Equal(t, querySource{}, b.source, "ClearSources must reset instance b")
}

// TestInstances_SetSource_SeedsByName proves SetSource targets one instance by
// name and is a no-op for unknown names.
func TestInstances_SetSource_SeedsByName(t *testing.T) {
	t.Parallel()
	a := newTestInstance(t, "a")
	b := newTestInstance(t, "b")
	insts := Instances{a, b}

	insts.SetSource(a.CRN, querySource{queryID: "qid-a"})
	insts.SetSource("missing", querySource{queryID: "noop"})
	assert.Equal(t, "qid-a", a.source.queryID)
	assert.Equal(t, querySource{}, b.source, "unrelated instance untouched")
}

// TestInstances_EnabledNames_And_CRNByName proves the dispatch input helpers:
// EnabledNames returns only enabled instances in order; CRNByName maps every
// instance name to its (already-string) CRN.
func TestInstances_EnabledNames_And_CRNByName(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	b := NewInstance(bundle, "b", "https://b.invalid", testCRN("b"), icl.EnvProd, "%.2f")
	c := NewInstance(bundle, "c", "https://c.invalid", testCRN("c"), icl.EnvProd, "%.2f")
	a.Enable()
	b.Disable()
	c.Enable()
	insts := Instances{a, b, c}

	assert.Equal(t, []string{testCRN("a"), testCRN("c")}, insts.EnabledCRNs(), "only enabled instances, in order")
}

// TestModel_UsesEffectiveInstancesForRowsAndAuth keeps the TUI rows and auth
// map on the exact same effective configuration, including built-in suppression.
//
//nolint:paralleltest // t.Setenv modifies process-global XDG paths
func TestModel_UsesEffectiveInstancesForRowsAndAuth(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := &config.Config{ICL: config.ICL{Instances: []config.ICLInstanceConfig{{
		Name: "configured",
		CRN:  config.MustCRNFromString("crn:v1:staging:public:logs:eu-de:a/account:configured::"),
	}}}}
	m := New(t.Context(), deps.New(cfg, state.New()))
	t.Cleanup(func() { require.NoError(t, m.Close()) })

	require.Len(t, m.instances, 1)
	assert.Equal(t, "configured", m.instances[0].Name)

	assert.Equal(t, cfg.ICL.Instances[0].CRN.String(), m.instances[0].CRN)
	assert.NotNil(t, m.authManager, "auth is configured independently of display names")
}

// TestInstances_ResolveInstanceToken_DoesNotClearStore proves the store-preserving
// auth resolver: unlike ResolveTokens it never Store.Clear()s, so a displayed log
// count survives a token resolution (used by dispatch/collect which must not
// disturb displayed logs). An unconfigured AccountManager makes GetAuthToken
// fail, but the store must be untouched either way.
func TestInstances_ResolveInstanceToken_DoesNotClearStore(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	inst := NewInstance(bundle, "keep", "https://keep.invalid", testCRN("keep"), icl.EnvProd, "%.2f")
	// Seed a flushed log count to stand in for "displayed logs".
	inst.flushedLogCount = 7
	insts := Instances{inst}

	am := testAccountManager()
	am.SetOIDCForTest(icl.EnvProd, "http://unused.invalid")
	_, _, err := insts.resolveInstanceToken(t.Context(), am, inst.CRN)
	require.Error(t, err, "unconfigured instance must error (proves no Store.Clear side effect masked it)")
	assert.Equal(t, 7, inst.DisplayLogCount(), "resolveInstanceToken must not clear the store")

	_, _, err = insts.resolveInstanceToken(t.Context(), am, "unknown")
	require.Error(t, err, "malformed CRN must error")
}

// TestInstances_ResolveInstanceToken_UnconfiguredCRN proves archive polling can
// authenticate and derive a service URL for a valid resource with no picker row.
func TestInstances_ResolveInstanceToken_UnconfiguredCRN(t *testing.T) {
	t.Parallel()
	am, closeServer := oidcTokenServer(t)
	t.Cleanup(closeServer)
	crn := testCRN("unconfigured")

	token, url, err := (Instances{}).resolveInstanceToken(t.Context(), am, crn)

	require.NoError(t, err)
	assert.Equal(t, "tok-ok", token)
	assert.Equal(t, "https://unconfigured.api.us-south.logs.cloud.ibm.com", url)
}
