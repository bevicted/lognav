package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
)

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeeStreamsNativeNDJSONFromLocalSSE(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldURL, oldResolver := queryStream, queryURL, resolveQueryToken
	defer func() { queryStream, queryURL, resolveQueryToken = oldStream, oldURL, oldResolver }()
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = icl.Query
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/query", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"result":{"results":[{"user_data":"{\"message\":\"from-sse\"}"}]}}`+"\n\n")
	}))
	defer server.Close()
	queryURL = func(*config.CRN) string { return server.URL }

	stdout, stderr, err := runQueryCommand(t, cfg, "q", "query", "--instance", "test", "--tee")
	require.NoError(t, err)
	assertNDJSONMessages(t, stdout, []string{"from-sse"})
	assert.NotContains(t, stdout, ".tmp")
	assert.NotContains(t, stderr, ".tmp")
	assert.Contains(t, stderr, "snapshot retained")
}

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeeMultiInstanceUsesConfiguredRequestLimit(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
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
			setQueryTestXDG(t)
			cfg := multiQueryTestConfig()
			cfg.Logs.MaxRows = tc.maxRows
			instances := []config.ICLInstanceConfig{cfg.ICL.Instances[0], cfg.ICL.Instances[2]}
			oldStream, oldURL, oldResolver := queryStream, queryURL, resolveQueryToken
			t.Cleanup(func() { queryStream, queryURL, resolveQueryToken = oldStream, oldURL, oldResolver })
			resolveQueryToken = teeTokenResolver
			queryStream = icl.Query

			var mu sync.Mutex
			var decodeErr error
			var limits []int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Metadata struct {
						Limit int64 `json:"limit"`
					} `json:"metadata"`
				}
				err := json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					decodeErr = err
					return
				}
				limits = append(limits, body.Metadata.Limit)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"result":{"results":[{"user_data":"{\"message\":\"from-sse\"}"}]}}`+"\n\n")
			}))
			t.Cleanup(srv.Close)
			queryURL = func(*config.CRN) string { return srv.URL }

			var stdout bytes.Buffer
			require.NoError(t, runQueryWithOptions(t.Context(), &stdout, io.Discard, cfg, instances, "q", queryRunOptions{tee: true}))
			assertNDJSONMessages(t, stdout.String(), []string{"from-sse", "from-sse"})
			require.NoError(t, decodeErr)
			assert.ElementsMatch(t, []int64{tc.want, tc.want}, limits)
		})
	}
}

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeeStreamsNativeNDJSONBeforeCompletion(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = teeTokenResolver
	release := make(chan struct{})
	queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
		cb.OnData(teeLog("live"))
		<-release
		cb.OnClose()
		return nil
	}

	out := newTeeCaptureWriter()
	root := queryRoot(cfg)
	root.SetArgs([]string{"query", "--instance", "test", "--tee"})
	root.SetIn(strings.NewReader("q"))
	root.SetOut(out)
	root.SetErr(io.Discard)
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	<-out.wrote
	assertNDJSONMessages(t, out.String(), []string{"live"})
	select {
	case err := <-done:
		t.Fatalf("query completed before its live stream completed: %v", err)
	default:
	}
	close(release)
	require.NoError(t, <-done)
}

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeeSynchronizesConcurrentNativeRecords(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := multiQueryTestConfig()
	instances := []config.ICLInstanceConfig{cfg.ICL.Instances[0], cfg.ICL.Instances[2]}
	oldStream, oldResolver := queryStream, resolveQueryToken
	defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
	resolveQueryToken = teeTokenResolver
	ready := make(chan string, len(instances))
	release := make(chan struct{})
	queryStream = func(_ context.Context, token, _, _ string, _ uint32, cb icl.QueryCallback) error {
		ready <- token
		<-release
		for _, message := range []string{token + "-1", token + "-2"} {
			cb.OnData(teeLog(message))
		}
		cb.OnClose()
		return nil
	}

	out := &teeAtomicWriter{}
	done := make(chan error, 1)
	go func() {
		done <- runQueryWithOptions(t.Context(), out, io.Discard, cfg, instances, "q", queryRunOptions{tee: true})
	}()
	for range instances {
		<-ready
	}
	close(release)
	require.NoError(t, <-done)
	messages := assertNDJSONMessages(t, out.String(), nil)
	assert.Equal(t, []string{"prod-a-1", "prod-a-2"}, messagesFor(messages, "prod-a"))
	assert.Equal(t, []string{"stage-a-1", "stage-a-2"}, messagesFor(messages, "stage-a"))
	assert.Equal(t, 4, out.writes)
}

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeePartialAndRetentionOutcomesSuppressSelectors(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	t.Run("partial result retains without printing selector", func(t *testing.T) {
		setQueryTestXDG(t)
		cfg := multiQueryTestConfig()
		instances := []config.ICLInstanceConfig{cfg.ICL.Instances[0], cfg.ICL.Instances[2]}
		oldStream, oldResolver := queryStream, resolveQueryToken
		defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
		resolveQueryToken = teeTokenResolver
		queryStream = func(_ context.Context, token, _, _ string, _ uint32, cb icl.QueryCallback) error {
			if token == "prod-a" {
				cb.OnData(teeLog("kept"))
			} else {
				cb.OnError(errors.New("later target failed"))
			}
			cb.OnClose()
			return nil
		}

		var stdout, stderr bytes.Buffer
		err := runQueryWithOptions(t.Context(), &stdout, &stderr, cfg, instances, "q", queryRunOptions{tee: true})
		require.Error(t, err)
		assert.Equal(t, ExitGeneral, ExitCode(err))
		assertNDJSONMessages(t, stdout.String(), []string{"kept"})
		assert.NotContains(t, stderr.String(), ".tmp")
		assert.Contains(t, stderr.String(), "snapshot retained")
		dir, dirErr := snapshot.Dir()
		require.NoError(t, dirErr)
		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		require.Len(t, entries, 1)
	})

	t.Run("all empty creates no snapshot", func(t *testing.T) {
		setQueryTestXDG(t)
		cfg := queryTestConfig()
		oldStream, oldResolver := queryStream, resolveQueryToken
		defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
		resolveQueryToken = teeTokenResolver
		queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
			cb.OnClose()
			return nil
		}

		var stdout, stderr bytes.Buffer
		require.NoError(t, runQueryWithOptions(t.Context(), &stdout, &stderr, cfg, cfg.ICL.Instances, "q", queryRunOptions{tee: true}))
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "0 logs; no snapshot created")
		dir, dirErr := snapshot.Dir()
		require.NoError(t, dirErr)
		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	})

	t.Run("retention zero leaves streamed output only", func(t *testing.T) {
		setQueryTestXDG(t)
		cfg := queryTestConfig()
		cfg.Core.MaxAutoSnapshots = 0
		oldStream, oldResolver := queryStream, resolveQueryToken
		defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
		resolveQueryToken = teeTokenResolver
		queryStream = queryOneLog

		var stdout, stderr bytes.Buffer
		require.NoError(t, runQueryWithOptions(t.Context(), &stdout, &stderr, cfg, cfg.ICL.Instances, "q", queryRunOptions{tee: true}))
		assertNDJSONMessages(t, stdout.String(), []string{"ok"})
		assert.Contains(t, stderr.String(), "snapshot created but not retained")
		assert.NotContains(t, stderr.String(), ".tmp")
	})
}

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeeBackpressuresCallbacksAndCancelsOnOutputFailure(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	t.Run("blocking stdout blocks the callback", func(t *testing.T) {
		setQueryTestXDG(t)
		cfg := queryTestConfig()
		oldStream, oldResolver := queryStream, resolveQueryToken
		defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
		resolveQueryToken = teeTokenResolver
		callbackReturned := make(chan struct{})
		queryStream = func(_ context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
			cb.OnData(teeLog("blocked"))
			close(callbackReturned)
			cb.OnClose()
			return nil
		}
		out := &teeBlockingWriter{reached: make(chan struct{}), release: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- runQueryWithOptions(t.Context(), out, io.Discard, cfg, cfg.ICL.Instances, "q", queryRunOptions{tee: true})
		}()
		<-out.reached
		select {
		case <-callbackReturned:
			t.Fatal("stream callback returned despite blocked stdout")
		default:
		}
		close(out.release)
		require.NoError(t, <-done)
	})

	t.Run("first output error cancels and joins every worker without finalizing", func(t *testing.T) {
		setQueryTestXDG(t)
		cfg := multiQueryTestConfig()
		instances := []config.ICLInstanceConfig{cfg.ICL.Instances[0], cfg.ICL.Instances[2]}
		oldStream, oldResolver := queryStream, resolveQueryToken
		defer func() { queryStream, resolveQueryToken = oldStream, oldResolver }()
		resolveQueryToken = teeTokenResolver
		started := make(chan struct{}, len(instances))
		stopped := make(chan struct{}, len(instances))
		release := make(chan struct{})
		queryStream = func(ctx context.Context, _, _, _ string, _ uint32, cb icl.QueryCallback) error {
			started <- struct{}{}
			<-release
			cb.OnData(teeLog("lost"))
			<-ctx.Done()
			stopped <- struct{}{}
			return ctx.Err()
		}
		out := &teeFailWriter{err: errors.New("closed stdout")}
		done := make(chan error, 1)
		go func() {
			done <- runQueryWithOptions(t.Context(), out, io.Discard, cfg, instances, "q", queryRunOptions{tee: true})
		}()
		for range instances {
			<-started
		}
		close(release)
		err := <-done
		require.Error(t, err)
		assert.Equal(t, ExitGeneral, ExitCode(err))
		require.ErrorContains(t, err, "write live query output: closed stdout")
		assert.EqualValues(t, 1, out.writes.Load(), "only the first failed write reaches stdout")
		for range instances {
			<-stopped
		}
		dir, dirErr := snapshot.Dir()
		require.NoError(t, dirErr)
		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	})
}

// These tests replace command-wide stream/auth seams and XDG paths.
func TestQuery_TeeSignalAfterOutcomeCommitKeepsFinalizedSnapshot(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldStream, oldResolver, oldCommitted := queryStream, resolveQueryToken, queryOutcomeCommitted
	defer func() { queryStream, resolveQueryToken, queryOutcomeCommitted = oldStream, oldResolver, oldCommitted }()
	resolveQueryToken = teeTokenResolver
	queryStream = queryOneLog

	baseCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gate := newQueryOutcomeGate(cancel)
	ctx := context.WithValue(baseCtx, queryOutcomeGateContextKey{}, gate)
	committed := make(chan struct{})
	release := make(chan struct{})
	queryOutcomeCommitted = func() {
		close(committed)
		<-release
	}
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runQueryWithOptions(ctx, &stdout, io.Discard, cfg, cfg.ICL.Instances, "q", queryRunOptions{tee: true})
	}()

	<-committed
	assert.False(t, gate.cancelForSignal(), "a signal after outcome commit must not produce exit 130")
	require.NoError(t, ctx.Err())
	close(release)
	require.NoError(t, <-done)
	assertNDJSONMessages(t, stdout.String(), []string{"ok"})
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func teeTokenResolver(_ context.Context, _ *icl.AccountManager, crn *config.CRN) (string, error) {
	return map[string]string{
		"account":   "test",
		"account-a": "prod-a",
		"account-b": "prod-b",
		"account-s": "stage-a",
	}[crn.ScopeID], nil
}

func teeLog(message string) *icl.StreamItem {
	data := `{"message":"` + message + `"}`
	return &icl.StreamItem{Result: &icl.DataprimeResult{Results: []icl.DataprimeResults{{UserData: &data}}}}
}

type teeCaptureWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	wrote chan struct{}
	once  sync.Once
}

func newTeeCaptureWriter() *teeCaptureWriter {
	return &teeCaptureWriter{wrote: make(chan struct{})}
}

func (w *teeCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once.Do(func() { close(w.wrote) })
	return w.buf.Write(p)
}

func (w *teeCaptureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type teeAtomicWriter struct {
	buf    bytes.Buffer
	active atomic.Int32
	writes int
}

func (w *teeAtomicWriter) Write(p []byte) (int, error) {
	if !w.active.CompareAndSwap(0, 1) {
		return 0, errors.New("concurrent NDJSON write")
	}
	defer w.active.Store(0)
	w.writes++
	return w.buf.Write(p)
}

func (w *teeAtomicWriter) String() string { return w.buf.String() }

type teeBlockingWriter struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *teeBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.reached) })
	<-w.release
	return len(p), nil
}

type teeFailWriter struct {
	err    error
	writes atomic.Int32
}

func (w *teeFailWriter) Write([]byte) (int, error) {
	w.writes.Add(1)
	return 0, w.err
}

func assertNDJSONMessages(t *testing.T, output string, want []string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if output == "" {
		lines = nil
	}
	messages := make([]string, 0, len(lines))
	for _, line := range lines {
		var record struct {
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &record), "record must be native parseable NDJSON: %q", line)
		messages = append(messages, record.Data.Message)
	}
	if want != nil {
		assert.Equal(t, want, messages)
	}
	return messages
}

func messagesFor(messages []string, prefix string) []string {
	var result []string
	for _, message := range messages {
		if strings.HasPrefix(message, prefix) {
			result = append(result, message)
		}
	}
	return result
}
