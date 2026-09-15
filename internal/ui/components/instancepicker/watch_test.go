package instancepicker

import (
	"context"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWatchTestModel builds a bare Model wired with a fakePoster and a fresh
// bundle, with the cooldown defaulted to 0 so future watch scheduling does not
// block the inline fakePoster. watchStart is set to time.Now() so the duration
// cap does not fire spuriously. Tests mutate config caps as needed.
func newWatchTestModel(t *testing.T) (*Model, *fakePoster) {
	t.Helper()
	fp := &fakePoster{}
	m := newTestModel(t, fp)
	m.bundle = depstest.NewTest(t)
	m.bundle.Config.Core.WatchCooldownSeconds = 0
	m.watchStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.authCtx, m.cancelAuth = ctx, cancel
	return m, fp
}

func TestWatchEmpties_UsesDisplayLogCount(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)

	// succeeded then flushed: live store cleared, flushedLogCount > 0 → NOT empty.
	flushed := newTestInstance(t, "flushed")
	flushed.state = status.Success
	flushed.flushedLogCount = 3 // GetLogCount() == 0 but DisplayLogCount() == 3

	// genuinely empty: clean completion, no logs.
	empty := newTestInstance(t, "empty")
	empty.state = status.Success

	// errored: excluded (terminal).
	errored := newTestInstance(t, "errored")
	errored.state = status.Error

	// disabled: excluded (not enabled).
	disabled := newTestInstance(t, "disabled")
	disabled.state = status.Disabled

	m.instances = Instances{flushed, empty, errored, disabled}

	got := m.watchEmpties()
	require.Len(t, got, 1)
	assert.Equal(t, "empty", got[0].Name,
		"only the genuinely-empty enabled instance is an empty; a flushed-successful one is terminal")
}

func TestEvaluateWatchRound_AllTerminal_EndsWatch(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.watching = true
	succeeded := newTestInstance(t, "succeeded")
	succeeded.state = status.Success
	succeeded.flushedLogCount = 2
	m.instances = Instances{succeeded}

	m.evaluateWatchRound()

	assert.False(t, m.watching, "no empties left → watch ends")
	assert.Equal(t, 1, m.roundCount)
}

func TestEvaluateWatchRound_EmptyRemains_SchedulesNextRound(t *testing.T) {
	t.Parallel()
	m, fp := newWatchTestModel(t)
	m.watching = true
	empty := newTestInstance(t, "empty")
	empty.state = status.Success // clean completion, 0 logs
	m.instances = Instances{empty}

	m.evaluateWatchRound()

	// watching was set true in setup; assert evaluateWatchRound did NOT clear it.
	assert.True(t, m.watching, "an empty remains → watch continues")
	assert.Equal(t, status.Watching, empty.state, "empty flips to Watching for the cooldown gap")
	var ticked *WatchTickMsg
	for _, ev := range fp.events() {
		if w, ok := ev.(WatchTickMsg); ok {
			w := w
			ticked = &w
		}
	}
	require.NotNil(t, ticked, "scheduleNextRound must post a WatchTickMsg (cooldown 0)")
	assert.Equal(t, uint64(0), ticked.Epoch, "tick must carry the epoch captured at schedule time")
}

func TestEvaluateWatchRound_CapHit_EndsWatch(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.bundle.Config.Core.WatchMaxFetches = 1
	m.watching = true
	empty := newTestInstance(t, "empty")
	empty.state = status.Success
	m.instances = Instances{empty}

	m.evaluateWatchRound() // roundCount becomes 1 >= maxFetches 1

	assert.False(t, m.watching, "cap hit → watch ends even with an empty remaining")
	assert.NotEqual(t, status.Watching, empty.state, "capped empties are left as-is, not flipped to Watching")
}

func TestOnWatchTick_StaleEpoch_NoOp(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.watching = true
	m.watchEpoch = 7
	w := newTestInstance(t, "w")
	w.state = status.Watching
	m.instances = Instances{w}

	m.OnWatchTick(WatchTickMsg{Epoch: 6}) // stale

	assert.Equal(t, status.Watching, w.state, "stale tick must not start a round")
	assert.True(t, m.watching)
}

func TestOnWatchTick_NotWatching_NoOp(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.watching = false
	m.watchEpoch = 3
	w := newTestInstance(t, "w")
	w.state = status.Watching
	m.instances = Instances{w}

	m.OnWatchTick(WatchTickMsg{Epoch: 3})

	assert.Equal(t, status.Watching, w.state, "tick after stop must not start a round")
}

func TestOnWatchTick_NoWatchingMembers_EndsWatch(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.watching = true
	m.watchEpoch = 2
	done := newTestInstance(t, "done")
	done.state = status.Success
	done.flushedLogCount = 1
	m.instances = Instances{done}

	m.OnWatchTick(WatchTickMsg{Epoch: 2}) // valid tick, but nothing is Watching

	assert.False(t, m.watching, "a valid tick with no Watching members ends the watch")
}

func TestOnWatchTick_CapHitDuringCooldown_EndsWatch(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.bundle.Config.Core.WatchMaxDurationSeconds = 1
	m.watching = true
	m.watchEpoch = 1
	m.watchStart = time.Now().Add(-time.Hour) // cooldown crossed the deadline
	w := newTestInstance(t, "w")
	w.state = status.Watching
	m.instances = Instances{w}

	m.OnWatchTick(WatchTickMsg{Epoch: 1})

	assert.False(t, m.watching, "duration cap re-checked after cooldown ends the watch")
}

func TestWatchCapHit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		maxFetches uint16
		maxSeconds uint16
		roundCount int
		startAgo   time.Duration
		want       bool
	}{
		{"both unlimited never caps", 0, 0, 999, time.Hour, false},
		{"fetch cap not yet hit", 5, 0, 4, 0, false},
		{"fetch cap hit", 5, 0, 5, 0, true},
		{"duration cap not yet hit", 0, 1800, 1, time.Minute, false},
		{"duration cap hit", 0, 1800, 1, 31 * time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWatchTestModel(t)
			m.bundle.Config.Core.WatchMaxFetches = tt.maxFetches
			m.bundle.Config.Core.WatchMaxDurationSeconds = tt.maxSeconds
			m.roundCount = tt.roundCount
			m.watchStart = time.Now().Add(-tt.startAgo)
			assert.Equal(t, tt.want, m.watchCapHit())
		})
	}
}

func TestSettle_NotWatching_Finalizes(t *testing.T) {
	t.Parallel()
	m, fp := newWatchTestModel(t)
	m.list = list.New(m.bundle) // finalizeBackingContainer -> snapshotMeta -> m.list.GetCursor()
	attachBackingFile(t, m)
	// SaveSnapshotOnFetchDone defaults true → maybeFinalizeFetch finalizes the
	// .wip and emits SaveSnapshotMsg. A non-empty result is required: an all-empty
	// fetch discards the .wip and suppresses the snapshot.
	done := newTestInstance(t, "done")
	done.state = status.Success
	require.NoError(t, snapshot.SaveInstanceFrame(m.backingContainer, done.CRN, []icl.Log{
		{Data: map[string]any{msgKey: "x"}, Metadata: icl.Metadata{ID: "1"}},
	}))
	done.flushedLogCount = 1
	done.flushedLogsSize = 1
	m.instances = Instances{done}

	m.settle()

	var saved bool
	for _, ev := range fp.events() {
		if _, ok := ev.(msgs.SaveSnapshotMsg); ok {
			saved = true
		}
	}
	assert.True(t, saved, "not watching + all done → settle finalizes (SaveSnapshotMsg emitted)")
}

func TestSettle_Watching_RoutesToEvaluate(t *testing.T) {
	t.Parallel()
	m, fp := newWatchTestModel(t)
	m.watching = true
	empty := newTestInstance(t, "empty")
	empty.state = status.Success // clean, 0 logs
	m.instances = Instances{empty}

	m.settle()

	assert.Equal(t, status.Watching, empty.state, "watching settle routes to evaluateWatchRound → empty becomes Watching")
	var saved bool
	for _, ev := range fp.events() {
		if _, ok := ev.(msgs.SaveSnapshotMsg); ok {
			saved = true
		}
	}
	assert.False(t, saved, "mid-watch settle must NOT finalize/snapshot")
}

func TestSettle_QueriesInProgress_NoOp(t *testing.T) {
	t.Parallel()
	m, fp := newWatchTestModel(t)
	busy := newTestInstance(t, "busy")
	busy.state = status.InProgress
	m.instances = Instances{busy}

	m.settle()

	assert.Empty(t, fp.events(), "settle is a no-op while a query is in progress")
}

func TestStopWatch_CancelsWatchingAndBumpsEpoch(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.watching = true
	m.watchEpoch = 4
	w := newTestInstance(t, "w")
	w.state = status.Watching
	done := newTestInstance(t, "done")
	done.state = status.Success
	m.instances = Instances{w, done}

	m.stopWatch()

	assert.False(t, m.watching)
	assert.Equal(t, uint64(5), m.watchEpoch, "epoch bump invalidates a scheduled tick")
	assert.Equal(t, status.Cancelled, w.state, "Watching instances are cancelled on stop")
	assert.Equal(t, status.Success, done.state, "terminal instances are left untouched")
}

func TestCancelAllFetches_StopsWatch(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	m.ctx = context.Background()
	m.watching = true
	m.watchEpoch = 1
	w := newTestInstance(t, "w")
	w.state = status.Watching
	m.instances = Instances{w}

	m.cancelAllFetches()

	assert.False(t, m.watching, "shift+c stops an in-progress watch")
	assert.Equal(t, status.Cancelled, w.state)
}

func TestRowTimer_Watching_ShowsCountdown(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)

	w := newTestInstance(t, "w") // timerFormat "%.2f" from newTestInstance
	w.state = status.Watching
	m.nextRoundAt = time.Now().Add(5 * time.Second)
	got := m.rowTimer(w)
	// Countdown ≈ 5s, formatted "%.2f"; allow scheduling slack.
	assert.Regexp(t, `^[45]\.\d{2}$`, got, "Watching row shows a live countdown to next retry")

	// Non-watching falls back to the normal frozen/elapsed timer.
	done := newTestInstance(t, "done")
	done.state = status.Success
	assert.Equal(t, done.RenderTimer(), m.rowTimer(done))
}

func TestRowTimer_Watching_ClampsAtZero(t *testing.T) {
	t.Parallel()
	m, _ := newWatchTestModel(t)
	w := newTestInstance(t, "w")
	w.state = status.Watching
	m.nextRoundAt = time.Now().Add(-2 * time.Second) // deadline passed
	assert.Equal(t, "0.00", m.rowTimer(w), "expired countdown clamps to zero, not negative")
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestWatchAction_StartThenToggleOff(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := New(t.Context(), depstest.NewTest(t))
	m.SetPoster(&fakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	m.instances.SetAll(false) // zero enabled → startFetch's ResolveTokens spawns no auth

	m.startWatch()
	assert.True(t, m.watching, "startWatch begins a watch")
	assert.True(t, m.fetching, "watch opens a backing fetch")

	m.stopWatch()
	assert.False(t, m.watching)
}

//nolint:paralleltest // t.Setenv (XDG_DATA_HOME) forbids t.Parallel
func TestOnSnapshotRestore_StopsActiveWatch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m := New(t.Context(), depstest.NewTest(t))
	m.SetPoster(&fakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	m.instances.SetAll(false)

	m.startWatch()
	require.True(t, m.IsWatching())

	m.OnSnapshotRestore(msgs.SnapshotRestoreMsg{})

	assert.False(t, m.IsWatching(), "a snapshot restore must stop an active watch")
}
