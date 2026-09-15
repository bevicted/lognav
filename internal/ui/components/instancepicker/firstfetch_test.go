package instancepicker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

func newFirstFetchModel(t *testing.T, instances Instances) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	m := newTestModel(t, &fakePoster{})
	m.bundle = bundle
	m.instances = instances
	m.authCtx = ctx
	m.cancelAuth = cancel
	m.list = list.New(bundle)
	m.SetPoster(m.poster)
	return m
}

func TestFirstFetch_SelectsAllWhenNothingEnabled(t *testing.T) {
	// startFirstFetch creates a PID-named WIP in the shared snapshot directory.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := newFirstFetchModel(t, Instances{
		newTestInstance(t, "one"),
		newTestInstance(t, "two"),
	})
	m.SetPoster(&queuePoster{}) // keep auth workers queued; this test only checks the on-loop prologue.

	require.NoError(t, m.startFirstFetch())
	assert.Len(t, m.firstFetchCandidates, 2)
	members := m.snapshotMeta().InstancePickerSnapshot.Instances
	require.Len(t, members, 2)
	assert.Equal(t, testCRN("one"), members[0].CRN)
	assert.Equal(t, testCRN("two"), members[1].CRN)
	assert.Equal(t, status.AuthInProgress, m.instances[0].state)
	assert.Equal(t, status.AuthInProgress, m.instances[1].state)

	m.discardBackingContainer()
}

func TestFirstFetch_ElectsWinnerAndDropsQueuedLoserLogs(t *testing.T) {
	t.Parallel()
	winner := newTestInstance(t, "winner")
	winner.StartTimer()
	loser := newTestInstance(t, "loser")
	loser.StartTimer()
	pending := newTestInstance(t, "pending")
	pending.StartAuthTimer()
	winner.Store.StartStream(10)
	loser.Store.StartStream(10)
	m := newFirstFetchModel(t, Instances{winner, loser, pending})
	authCtx := m.authCtx
	streamCancelled := false
	loser.cancelQuery = func() { streamCancelled = true }
	m.firstFetchCandidates = map[string]struct{}{winner.CRN: {}, loser.CRN: {}, pending.CRN: {}}

	m.OnLogStream(&msgs.LogStreamMsg{CRN: loser.CRN, ID: loser.Store.GetQueryID(), Warns: []string{"not a log"}})
	assert.Empty(t, m.firstFetchWinner, "warning-only batch cannot win")
	m.OnLogStream(&msgs.LogStreamMsg{CRN: winner.CRN, ID: winner.Store.GetQueryID(), Logs: []icl.Log{{Metadata: icl.Metadata{ID: "winner"}}}})

	assert.Equal(t, winner.CRN, m.firstFetchWinner)
	assert.Equal(t, status.Cancelled, loser.state)
	assert.Equal(t, status.Cancelled, pending.state)
	assert.True(t, streamCancelled, "winner election must cancel a live loser stream")
	require.ErrorIs(t, authCtx.Err(), context.Canceled, "winner election must cancel pending authentication")
	assert.Equal(t, 1, winner.Store.GetLogCount())

	m.OnLogStream(&msgs.LogStreamMsg{CRN: loser.CRN, ID: loser.Store.GetQueryID(), Logs: []icl.Log{{Metadata: icl.Metadata{ID: "loser"}}}})
	assert.Zero(t, loser.Store.GetLogCount(), "a queued loser batch must not enter its store")
}

// TestFirstFetch_StaleAuthCancellationCannotCancelNextFetch exercises the
// delayed EnvAuthCancelledMsg path without timing: the first winner invalidates
// its auth generation, then a new fetch marks its rows AuthInProgress before the
// old cancellation message is delivered.
func TestFirstFetch_StaleAuthCancellationCannotCancelNextFetch(t *testing.T) {
	// Cannot use t.Parallel: t.Setenv changes the process-wide snapshot location.
	// startFetchWithSources creates a PID-named WIP in the shared snapshot directory.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	winner := newTestInstance(t, "winner")
	winner.StartTimer()
	winner.Store.StartStream(10)
	pending := newTestInstance(t, "pending")
	pending.StartAuthTimer()
	m := newFirstFetchModel(t, Instances{winner, pending})
	staleGeneration := m.authGeneration
	m.firstFetchCandidates = map[string]struct{}{winner.CRN: {}, pending.CRN: {}}

	m.OnLogStream(&msgs.LogStreamMsg{
		CRN:  winner.CRN,
		ID:   winner.Store.GetQueryID(),
		Logs: []icl.Log{{Metadata: icl.Metadata{ID: "winner"}}},
	})
	m.finishFirstFetch()
	winner.Enable()
	pending.Enable()
	m.SetPoster(&queuePoster{}) // retain new auth workers; only their on-loop prologue matters here.

	require.NoError(t, m.startFetchWithSources())
	require.Equal(t, status.AuthInProgress, winner.state)
	require.Equal(t, status.AuthInProgress, pending.state)

	m.OnEnvAuthCancelled(EnvAuthCancelledMsg{Env: winner.env, AuthGeneration: staleGeneration})

	assert.Equal(t, status.AuthInProgress, winner.state)
	assert.Equal(t, status.AuthInProgress, pending.state)
	m.discardBackingContainer()
}

func TestFirstFetch_IgnoresCancelledAndStaleCandidateEvents(t *testing.T) {
	t.Parallel()
	cancelled := newTestInstance(t, "cancelled")
	cancelled.StartTimer()
	cancelled.Store.StartStream(10)
	cancelled.cancelQuery = func() {}
	cancelled.CancelQuery()
	require.Equal(t, status.Cancelled, cancelled.state)
	stale := newTestInstance(t, "stale")
	stale.StartTimer()
	stale.Store.StartStream(10)
	staleID := stale.Store.GetQueryID()
	stale.Store.StartStream(10)
	require.NotEqual(t, staleID, stale.Store.GetQueryID())
	active := newTestInstance(t, "active")
	active.StartTimer()
	active.Store.StartStream(10)
	activeCancelled := false
	active.cancelQuery = func() { activeCancelled = true }
	m := newFirstFetchModel(t, Instances{cancelled, stale, active})
	m.firstFetchCandidates = map[string]struct{}{cancelled.CRN: {}, stale.CRN: {}, active.CRN: {}}

	m.OnLogStream(&msgs.LogStreamMsg{CRN: cancelled.CRN, ID: cancelled.Store.GetQueryID(), Logs: []icl.Log{{Metadata: icl.Metadata{ID: "cancelled"}}}})
	m.OnLogStream(&msgs.LogStreamMsg{CRN: stale.CRN, ID: staleID, Logs: []icl.Log{{Metadata: icl.Metadata{ID: "stale"}}}})

	assert.Empty(t, m.firstFetchWinner)
	assert.False(t, activeCancelled, "invalid candidate events must not cancel active candidates")
	assert.Zero(t, cancelled.Store.GetLogCount())
	assert.Zero(t, stale.Store.GetLogCount())
}

func TestFirstFetch_ElectionCancelsErrorLoserStream(t *testing.T) {
	t.Parallel()
	winner := newTestInstance(t, "winner")
	winner.StartTimer()
	winner.Store.StartStream(10)
	errored := newTestInstance(t, "errored")
	errored.StartTimer()
	errored.Store.StartStream(10)
	errored.state = status.Error
	errorStreamCancelled := false
	errored.cancelQuery = func() { errorStreamCancelled = true }
	m := newFirstFetchModel(t, Instances{winner, errored})
	m.firstFetchCandidates = map[string]struct{}{winner.CRN: {}, errored.CRN: {}}

	m.OnLogStream(&msgs.LogStreamMsg{CRN: winner.CRN, ID: winner.Store.GetQueryID(), Logs: []icl.Log{{Metadata: icl.Metadata{ID: "winner"}}}})

	assert.Equal(t, winner.CRN, m.firstFetchWinner)
	assert.True(t, errorStreamCancelled, "winner election must cancel an errored loser stream before it closes")
	assert.Equal(t, status.Error, errored.state)
}

func TestFirstFetch_WinnerRemainsSelectedAfterStreamFailure(t *testing.T) {
	t.Parallel()
	winner := newTestInstance(t, "winner")
	winner.StartTimer()
	winner.Store.StartStream(10)
	loser := newTestInstance(t, "loser")
	loser.StartTimer()
	loser.cancelQuery = func() {}
	m := newFirstFetchModel(t, Instances{winner, loser})
	m.firstFetchCandidates = map[string]struct{}{winner.CRN: {}, loser.CRN: {}}

	m.OnLogStream(&msgs.LogStreamMsg{CRN: winner.CRN, ID: winner.Store.GetQueryID(), Logs: []icl.Log{{Metadata: icl.Metadata{ID: "winner"}}}})
	m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: winner.CRN, ID: winner.Store.GetQueryID(), Errs: []string{"stream failed"}})

	assert.Equal(t, status.Error, winner.state)
	assert.True(t, winner.IsEnabled())
	assert.Equal(t, status.Cancelled, loser.state)
}

func TestFirstFetch_FinalizesBeforeDeselectingLosers(t *testing.T) {
	// Finalization writes a PID-named .inuse guard.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	winner := newTestInstance(t, "winner")
	winner.state = status.Success
	winner.flushedLogCount = 1
	winner.flushedLogsSize = 1
	loser := newTestInstance(t, "loser")
	loser.state = status.Error
	m := newFirstFetchModel(t, Instances{winner, loser})
	m.captureSnapshotMembers([]string{winner.CRN, loser.CRN})
	attachBackingFile(t, m)
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, winner.CRN, []icl.Log{{Metadata: icl.Metadata{ID: "winner"}}}))
	m.firstFetchCandidates = map[string]struct{}{winner.CRN: {}, loser.CRN: {}}
	m.firstFetchWinner = winner.CRN

	m.maybeFinalizeFetch()

	require.NotEmpty(t, m.GetBackingPath())
	c, err := snapshot.OpenContainerReadOnly(m.GetBackingPath())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	state, err := snapshot.LoadState(c)
	require.NoError(t, err)
	require.Len(t, state.InstancePickerSnapshot.Instances, 2)
	assert.Equal(t, int(status.Error), state.InstancePickerSnapshot.Instances[1].State)
	assert.Equal(t, status.Cancelled, loser.state)
	assert.True(t, winner.IsEnabled())
	assert.Nil(t, m.firstFetchCandidates)
}

func TestFirstFetch_NoWinnerDiscardsSnapshotAndCancelsCandidates(t *testing.T) {
	// Discarding the WIP clears this process's PID-named .inuse guard.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	one := newTestInstance(t, "one")
	one.state = status.Success
	two := newTestInstance(t, "two")
	two.state = status.Error
	m := newFirstFetchModel(t, Instances{one, two})
	wipPath := attachBackingFile(t, m)
	m.firstFetchCandidates = map[string]struct{}{one.CRN: {}, two.CRN: {}}

	m.maybeFinalizeFetch()

	assert.Empty(t, m.GetBackingPath())
	assert.NoFileExists(t, wipPath)
	assert.Equal(t, status.Cancelled, one.state)
	assert.Equal(t, status.Cancelled, two.state)
	assert.Nil(t, m.firstFetchCandidates)
	poster, ok := m.poster.(*fakePoster)
	require.True(t, ok)
	var notice bool
	for _, ev := range poster.events() {
		if _, ok := ev.(msgs.ShowDialogMsg); ok {
			notice = true
		}
	}
	assert.True(t, notice, "no-result first fetch must show a notice")
}
