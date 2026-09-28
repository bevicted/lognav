package instancepicker

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
)

// sseDataServer answers the public background data endpoint with the configured
// per-query-ID SSE body. A missing body returns an HTTP error, exercising the
// errored-instance collection path.
func sseDataServer(t *testing.T, byQueryID map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/data"):
			assert.Equal(t, http.MethodGet, r.Method)
			id := queryIDFromDataPath(r.URL.Path)
			sse, ok := byQueryID[id]
			if !ok || sse == "" {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte("query is not completed"))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sse))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func queryIDFromDataPath(requestPath string) string {
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "background_query" || parts[3] != "data" {
		return ""
	}
	return parts[2]
}

const oneRowSSE = `data: {"response":{"results":{"results":[{"metadata":[{"key":"timestamp","value":"2026-06-20T15:04:05.000000"}],"labels":[{"key":"applicationname","value":"app"}],"user_data":"{\"log\":\"hello\"}"}]}}}` + "\n\n"

// queuePoster runs every spawned worker on an explicit drain, FIFO, instead of
// inline. This faithfully reproduces the production ordering the inline fakePoster
// would invert: OnMemberAuthResolved calls StartQuery (which spawns the
// FetchBackgroundData body) and THEN StartTimer (flips the instance to InProgress);
// running the fetch body inline would deliver LogStreamDoneMsg while the instance
// is still AuthInProgress, so the done handler would early-return and never settle.
// Deferring the body until after OnMemberAuthResolved returns matches the real
// async poster. PostCritical records events for assertions.
type queuePoster struct {
	queue  []func(context.Context)
	posted []uv.Event
}

func (q *queuePoster) Go(fn func(context.Context)) { q.queue = append(q.queue, fn) }

func (q *queuePoster) PostCritical(_ context.Context, ev uv.Event) error {
	q.posted = append(q.posted, ev)
	return nil
}

func (q *queuePoster) Post(uv.Event) {}

// drain runs queued workers FIFO until the queue is empty (a worker may enqueue
// more — e.g. the flush worker).
func (q *queuePoster) drain() {
	for len(q.queue) > 0 {
		fn := q.queue[0]
		q.queue = q.queue[1:]
		fn(context.Background())
	}
}

// newCollectModel builds a *Model wired for an end-to-end collect: a bundle, an
// AccountManager (from oidcTokenServer), a queuePoster (defers workers so ordering
// matches production), a send sink that feeds stream events back into the model's
// handlers, a list (finalize -> snapshotMeta reads the cursor), and a fresh authCtx.
func newCollectModel(t *testing.T, am *icl.AccountManager, insts Instances) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	for _, inst := range insts.Configured() {
		bundle.Config.ICL.Instances = append(bundle.Config.ICL.Instances, config.ICLInstanceConfig{
			Name: inst.Name,
			CRN:  config.MustCRNFromString(inst.CRN),
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := &Model{
		bundle:       bundle,
		instances:    insts,
		authManager:  am,
		poster:       &queuePoster{},
		logger:       slog.Default(),
		ctx:          context.Background(),
		authCtx:      ctx,
		cancelAuth:   cancel,
		pendingLoads: map[string]struct{}{},
		list:         list.New(bundle),
	}
	// send feeds streamCallback events straight into the model's on-loop handlers
	// (the runtime would route these via Update). Drives the .wip flush + finalize.
	m.send = func(ev uv.Event) {
		switch e := ev.(type) {
		case *msgs.LogStreamMsg:
			m.OnLogStream(e)
		case *msgs.LogStreamDoneMsg:
			m.OnLogStreamDone(e)
		}
	}
	return m
}

// finalizedCollectSnapshot scans the (XDG-confined) snapshot dir for the single
// finalized auto snapshot a collect produced and returns its opened container.
func finalizedCollectSnapshot(t *testing.T) *snapshot.Container {
	t.Helper()
	entries, err := snapshot.Scan()
	require.NoError(t, err)
	require.Len(t, entries, 1, "collect must finalize exactly one snapshot")
	c, err := snapshot.OpenContainerReadOnly(entries[0].Path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// runCollect drives startCollect end-to-end against the queuePoster. It repeatedly
// (a) drains queued workers (auth-resolve, then each FetchBackgroundData body, then
// the async flush + mark-collected) and (b) dispatches any MemberAuthResolvedMsg the
// resolve workers posted to OnMemberAuthResolved (which spawns the per-instance
// stream). It loops until no new work remains, mirroring the runtime's event loop.
func runCollect(t *testing.T, m *Model, a *archive.Archive) {
	t.Helper()
	require.NoError(t, m.startCollect(a))
	qp, _ := m.poster.(*queuePoster)
	dispatched := 0
	for {
		qp.drain()
		progressed := false
		for dispatched < len(qp.posted) {
			switch ev := qp.posted[dispatched].(type) {
			case MemberAuthResolvedMsg:
				m.OnMemberAuthResolved(ev) // spawns the per-instance stream
				progressed = true
			case InstanceFlushedMsg:
				m.OnInstanceFlushed(ev) // writes the compressed frame, decs pendingFlushes, finalizes
				progressed = true
			}
			dispatched++
		}
		if !progressed && len(qp.queue) == 0 {
			return
		}
	}
}

// TestStartCollect_TwoInstances_FinalizesSnapshotWithBothRows proves the happy
// collect path: a 2-instance ready archive whose /data servers stream SSE
// produces one finalized snapshot carrying both instances' rows, and m.collecting
// is reset after finalize.
func TestStartCollect_TwoInstances_FinalizesSnapshotWithBothRows(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // confine snapshot.Dir() + archive.Dir(); OS-shared state -> no t.Parallel

	dataSrv := sseDataServer(t, map[string]string{
		"qid-a": oneRowSSE,
		"qid-b": oneRowSSE,
	})
	am, closeSrv := oidcTokenServer(t, "inst-a", "inst-b")
	defer closeSrv()

	bundle := depstest.NewTest(t)
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	oldPath := autoSnapshotPath(dir, time.Unix(1, 0))
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o600))
	require.NoError(t, os.Chtimes(oldPath, time.Time{}, time.Unix(1, 0)))
	a := NewInstance(bundle, "inst-a", dataSrv.URL, testCRN("a"), icl.EnvProd, "%.2f")
	b := NewInstance(bundle, "inst-b", dataSrv.URL, testCRN("b"), icl.EnvProd, "%.2f")
	unselected := NewInstance(bundle, "unselected", dataSrv.URL, testCRN("c"), icl.EnvProd, "%.2f")
	unselected.state = status.Disabled
	m := newCollectModel(t, am, Instances{a, b, unselected})
	m.bundle.Config.Core.MaxAutoSnapshots = 1

	arch := &archive.Archive{
		Name:  "arch-1",
		Query: "source logs last 7d",
		Instances: []archive.InstanceEntry{
			{CRN: testCRN("a"), QueryID: "qid-a", State: archive.StateSuccess},
			{CRN: testCRN("b"), QueryID: "qid-b", State: archive.StateSuccess},
		},
	}
	runCollect(t, m, arch)

	assert.False(t, m.collecting, "m.collecting must be reset after finalize")
	assert.False(t, m.fetching, "fetch must be done after finalize")
	assert.NoFileExists(t, oldPath, "collect finalization must retain synchronously")

	c := finalizedCollectSnapshot(t)
	state, err := snapshot.LoadState(c)
	require.NoError(t, err)
	assert.Equal(t, "source logs last 7d", state.Query, "collect snapshot carries the archive query")
	require.Len(t, state.InstancePickerSnapshot.Instances, 2)
	assertContainerMembership(t, c, []string{testCRN("a"), testCRN("b")})

	for _, name := range []string{testCRN("a"), testCRN("b")} {
		logs, err := snapshot.LoadInstanceLogs(c, name)
		require.NoError(t, err, "instance %s must have a log frame", name)
		assert.Len(t, logs, 1, "instance %s streamed one SSE row", name)
		_, wantSize, err := snapshot.PrepareInstanceFrame(logs)
		require.NoError(t, err)
		for _, inst := range state.InstancePickerSnapshot.Instances {
			if inst.CRN == name {
				assert.Equal(t, wantSize, inst.LogsSizeBytes, "TUI collection persists the prepared frame size")
			}
		}
	}
}

// TestStartCollect_AllErrored_StillFinalizesWithMessages proves the force-finalize
// path: when every instance's /data returns a non-success error, the collect is
// NOT discarded (unlike a plain empty fetch) — it saves an error-bearing snapshot
// whose per-instance Message carries the error text.
func TestStartCollect_AllErrored_StillFinalizesWithMessages(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dataSrv := sseDataServer(t, map[string]string{}) // every query ID returns an HTTP error
	am, closeSrv := oidcTokenServer(t, "inst-a", "inst-b")
	defer closeSrv()

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "inst-a", dataSrv.URL, testCRN("a"), icl.EnvProd, "%.2f")
	b := NewInstance(bundle, "inst-b", dataSrv.URL, testCRN("b"), icl.EnvProd, "%.2f")
	unselected := NewInstance(bundle, "unselected", dataSrv.URL, testCRN("c"), icl.EnvProd, "%.2f")
	unselected.state = status.Disabled
	m := newCollectModel(t, am, Instances{a, b, unselected})

	arch := &archive.Archive{
		Name:  "arch-err",
		Query: "q",
		Instances: []archive.InstanceEntry{
			{CRN: testCRN("a"), QueryID: "qid-a", State: archive.StateError},
			{CRN: testCRN("b"), QueryID: "qid-b", State: archive.StateError},
		},
	}

	runCollect(t, m, arch)

	assert.False(t, m.collecting, "m.collecting reset even for an all-errored collect")

	c := finalizedCollectSnapshot(t) // force-finalize: an all-errored collect is still saved
	state, err := snapshot.LoadState(c)
	require.NoError(t, err)
	require.Len(t, state.InstancePickerSnapshot.Instances, 2)
	assertContainerMembership(t, c, []string{testCRN("a"), testCRN("b")})
	for _, is := range state.InstancePickerSnapshot.Instances {
		assert.NotEmpty(t, is.Message, "errored instance %s must carry its error in Message", is.CRN)
		assert.Zero(t, is.LogsSizeBytes, "errored collection member has no streamed output")
	}
}

// TestStartCollect_Cancelled_ResetsCollecting proves an interrupted collect
// (m.fetchCancelled) resets m.collecting so it never leaks into the next plain
// fetch. Collection is not tracked, so re-collect is always available.
func TestStartCollect_Cancelled_ResetsCollecting(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dataSrv := sseDataServer(t, map[string]string{"qid-a": oneRowSSE})
	am, closeSrv := oidcTokenServer(t, "inst-a")
	defer closeSrv()

	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "inst-a", dataSrv.URL, testCRN("a"), icl.EnvProd, "%.2f")
	m := newCollectModel(t, am, Instances{a})

	arch := &archive.Archive{
		Name:      "arch-cancel",
		Query:     "q",
		Instances: []archive.InstanceEntry{{CRN: testCRN("a"), QueryID: "qid-a", State: archive.StateSuccess}},
	}

	require.NoError(t, m.startCollect(arch))
	qp, _ := m.poster.(*queuePoster)
	// Drive auth so the instance reaches InProgress, then dispatch the resolve.
	qp.drain()
	for _, ev := range qp.posted {
		if r, ok := ev.(MemberAuthResolvedMsg); ok {
			m.OnMemberAuthResolved(r) // flips to InProgress; queues the fetch body (NOT run)
		}
	}
	// Cancel mid-collect BEFORE the fetch body streams: cancelAllFetches sets
	// fetchCancelled, flips the in-flight query to Cancelled, and finalizes.
	m.cancelAllFetches()
	qp.drain() // run any trailing flush worker

	assert.False(t, m.collecting, "m.collecting reset after a cancelled collect")
	assert.True(t, m.fetchCancelled, "cancel must set fetchCancelled")
}

// TestSetTruncationWarning_AppendsOrReplaces proves the per-instance 50,000-row
// warning rides into the store message: it replaces an empty message and appends
// (with the stream-error separator) to an existing one.
func TestSetTruncationWarning_AppendsOrReplaces(t *testing.T) {
	t.Parallel()

	empty := newTestInstance(t, "empty-msg")
	setTruncationWarning(empty)
	assert.Equal(t, truncationWarning, empty.Store.GetMessage(),
		"an empty store message is replaced by the truncation warning")

	withErr := newTestInstance(t, "with-msg")
	withErr.Store.SetMessage("prior error")
	setTruncationWarning(withErr)
	got := withErr.Store.GetMessage()
	assert.Contains(t, got, "prior error", "an existing message is preserved")
	assert.Contains(t, got, truncationWarning, "the truncation warning is appended")
}

// TestOnLogStreamDone_TruncationWarning_OnlyWhenCollectingAtLimit proves the
// truncation warning is set into the snapshot's per-instance message ONLY when
// collecting AND the collected count reached the fixed 50,000-row ceiling -
// never for a plain fetch or below that ceiling.
func TestOnLogStreamDone_TruncationWarning_OnlyWhenCollectingAtLimit(t *testing.T) {
	cases := []struct {
		name       string
		collecting bool
		count      int
		wantWarn   bool
	}{
		{"collect at fixed limit warns", true, int(icl.SyncQueryLimit), true},
		{"collect below fixed limit does not warn", true, 2, false},
		{"plain fetch at fixed limit does not warn", false, int(icl.SyncQueryLimit), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newModelWithBackingFile(t)
			m.bundle = depstest.NewTest(t)
			m.collecting = tc.collecting
			f := &fakePoster{}
			m.SetPoster(f)

			inst := newTestInstance(t, "inst")
			inst.state = status.InProgress
			logs := make([]icl.Log, tc.count)
			for i := range logs {
				logs[i] = icl.Log{Data: map[string]any{"msg": "x"}, Metadata: icl.Metadata{TSMicro: int64(i + 1)}}
			}
			inst.Store.SetPoster(f)
			inst.Store.SetLogs(logs)
			require.Equal(t, tc.count, inst.Store.GetLogCount())
			m.instances = Instances{inst}

			m.OnLogStreamDone(&msgs.LogStreamDoneMsg{CRN: inst.CRN})

			// The warning is re-applied AFTER ClearData (the flush wiped the message),
			// so it is on the store now and would ride into SnapshotizeMeta at finalize.
			if tc.wantWarn {
				assert.Equal(t, truncationWarning, inst.Store.GetMessage(),
					"a collect at the fixed ceiling must carry the truncation warning into the snapshot")
			} else {
				assert.Empty(t, inst.Store.GetMessage(),
					"no truncation warning expected (not collecting or below the fixed ceiling)")
			}
		})
	}
}

// TestStartCollect_RefusedWhileBusy proves collect is a peer fetch mode: it is
// refused (errCollectBusy) while a fetch or watch is running.
// TestStartCollect_UnconfiguredCRNIsAtomic proves configured-only collect rejects
// the whole archive before it mutates query/picker state, removes transient rows,
// opens a backing file, or starts auth/data work.
func TestStartCollect_UnconfiguredCRNIsAtomic(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bundle := depstest.NewTest(t)
	configured := NewInstance(bundle, "renamed", "https://configured.invalid", testCRN("configured"), icl.EnvProd, "%.2f")
	readOnly := NewInstance(bundle, "snapshot-only", "https://readonly.invalid", testCRN("readonly"), icl.EnvProd, "%.2f")
	readOnly.readOnly = &snapshot.InstanceSnapshot{CRN: readOnly.CRN}
	m := newCollectModel(t, testAccountManager(), Instances{configured, readOnly})
	m.bundle.State.SetQuery("original query")
	arch := &archive.Archive{
		Query: "archive query",
		Instances: []archive.InstanceEntry{
			{CRN: configured.CRN, QueryID: "configured"},
			{CRN: testCRN("missing"), QueryID: "missing"},
		},
	}

	err := m.startCollect(arch)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "us-south/missing")
	assert.NotContains(t, err.Error(), testCRN("missing"))
	assert.Equal(t, "original query", m.bundle.State.Query(), "query is unchanged")
	assert.Equal(t, Instances{configured, readOnly}, m.instances, "picker rows are unchanged")
	assert.Nil(t, m.backingFile, "no backing file is opened")
	assert.Nil(t, m.backingContainer, "no backing container is created")
	assert.False(t, m.collecting, "collect mode is not entered")
	qp, ok := m.poster.(*queuePoster)
	require.True(t, ok)
	assert.Empty(t, qp.queue, "no auth or data worker is started")
	assert.Empty(t, qp.posted, "no editor mutation is posted")
}

func TestStartCollect_RefusedWhileBusy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	am, closeSrv := oidcTokenServer(t, "inst-a")
	defer closeSrv()
	bundle := depstest.NewTest(t)
	a := NewInstance(bundle, "inst-a", "https://a.invalid", testCRN("a"), icl.EnvProd, "%.2f")
	m := newCollectModel(t, am, Instances{a})

	arch := &archive.Archive{Name: "x", Instances: []archive.InstanceEntry{{CRN: testCRN("a"), QueryID: "q"}}}

	m.fetching = true
	require.ErrorIs(t, m.startCollect(arch), errCollectBusy)
	m.fetching = false
	m.watching = true
	require.ErrorIs(t, m.startCollect(arch), errCollectBusy)
}
