package icl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmitBackgroundQuery_OK(t *testing.T) {
	t.Parallel()
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"queryId":"abc-123","warnings":[]}`))
	}))
	defer srv.Close()
	id, err := SubmitBackgroundQuery(context.Background(), "tok", srv.URL, "source logs last 7d | count")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", id)
	assert.Equal(t, bgSubmitPath, gotPath)
	assert.Contains(t, gotBody, `"syntax":"QUERY_SYNTAX_DATAPRIME"`)
	assert.Contains(t, gotBody, `"startDate"`)
	assert.Contains(t, gotBody, `"endDate"`)
}

func TestSubmitBackgroundQuery_CompileError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Compilation errors:\n - bad"))
	}))
	defer srv.Close()
	_, err := SubmitBackgroundQuery(context.Background(), "tok", srv.URL, "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Compilation errors")
}

func TestGetBackgroundQueryStatus_States(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		code int
		want BackgroundState
	}{
		{"running", `{"running":{}}`, 200, BackgroundRunning},
		{"success", `{"terminated":{"success":{}},"submittedAt":"2026-06-20T15:04:05Z"}`, 200, BackgroundSuccess},
		{"cancelled", `{"terminated":{"cancelled":{}}}`, 200, BackgroundError},
		{"notfound-4xx", `Query not found`, 404, BackgroundNotFound},
		{"notfound-2xx", `Query not found`, 200, BackgroundNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, bgStatusPath, r.URL.Path)
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			st, err := GetBackgroundQueryStatus(context.Background(), "tok", srv.URL, "q")
			require.NoError(t, err)
			assert.Equal(t, tc.want, st.State)
		})
	}
}

// TestGetBackgroundQueryStatus_TransientErrorNotExpired guards the regression where a
// transient non-404 whose body merely contains "not found" was mis-classified as
// BackgroundNotFound (terminal StateExpired), killing a still-running query. Only a genuine
// 404 is the not-found/expiry signal; a 5xx must surface as a transient error.
func TestGetBackgroundQueryStatus_TransientErrorNotExpired(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream backend not found: retry later"))
	}))
	defer srv.Close()
	st, err := GetBackgroundQueryStatus(context.Background(), "tok", srv.URL, "q")
	require.Error(t, err, "a transient 5xx must propagate as an error, not freeze the query")
	assert.NotEqual(t, BackgroundNotFound, st.State, "a non-404 must never map to not-found/expired")
}

func TestCancelBackgroundQuery_OK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, bgCancelPath, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	require.NoError(t, CancelBackgroundQuery(context.Background(), "tok", srv.URL, "q"))
}

type captureCB struct {
	items  []*StreamItem
	errs   []error
	closed bool
}

func (c *captureCB) OnData(s *StreamItem) { c.items = append(c.items, s) }
func (c *captureCB) OnClose()             { c.closed = true }
func (c *captureCB) OnError(e error)      { c.errs = append(c.errs, e) }
func (c *captureCB) OnKeepAlive()         {}

func TestFetchBackgroundData_StreamsNDJSON(t *testing.T) {
	t.Parallel()
	fixture, err := os.ReadFile("testdata/background_data.ndjson")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, bgDataPath, r.URL.Path)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	cb := &captureCB{}
	require.NoError(t, FetchBackgroundData(context.Background(), "tok", srv.URL, "q", cb, 0))
	// Two batches -> two OnData calls, each one row; camelCase userData must decode (not nil).
	require.Len(t, cb.items, 2)
	logs, _, errs := DecodeStreamItem(cb.items[0]) // DecodeStreamItem returns (logs, warns, errs)
	assert.Empty(t, errs, "camelCase userData must decode without a nil-UserData error")
	assert.Len(t, logs, 1)
	assert.Empty(t, cb.errs)
	assert.True(t, cb.closed, "FetchBackgroundData self-closes on the success path (mirrors Query)")
}

// TestFetchBackgroundData_TrimsOversizedFirstBatch proves the delivery cap,
// rather than the downstream LogStore cap, bounds a 50,001-row first batch.
func TestFetchBackgroundData_TrimsOversizedFirstBatch(t *testing.T) {
	t.Parallel()
	const row = `{"userData":"{}"}`
	body := `{"response":{"results":{"results":[` +
		strings.TrimSuffix(strings.Repeat(row+",", int(SyncQueryLimit)+1), ",") +
		`]}}}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cb := &captureCB{}
	require.NoError(t, FetchBackgroundData(context.Background(), "tok", srv.URL, "q", cb, SyncQueryLimit))
	require.Len(t, cb.items, 1)
	assert.Len(t, cb.items[0].Result.Results, int(SyncQueryLimit),
		"the callback must not receive the oversized row")
	assert.True(t, cb.closed)
}

func TestFetchBackgroundData_NonSuccessBodyIsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Query is not completed: query cancelled"))
	}))
	defer srv.Close()
	cb := &captureCB{}
	err := FetchBackgroundData(context.Background(), "tok", srv.URL, "q", cb, 0)
	require.Error(t, err) // surfaced, not silently zero rows
	assert.Empty(t, cb.items)
	assert.False(t, cb.closed, "on a returned error OnClose is skipped; the caller's cb.OnError carries the terminal signal")
}

func TestFetchBackgroundData_StopsAtMaxRows(t *testing.T) {
	t.Parallel()
	// Build a server that streams 10 single-row NDJSON batches.
	row := `{"response":{"results":{"results":[{"metadata":[{"key":"timestamp","value":"2026-06-20T15:04:05.000000"}],"labels":[],"userData":"{}"}]}}}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for range 10 {
			_, _ = w.Write([]byte(row))
		}
	}))
	defer srv.Close()
	cb := &captureCB{}
	err := FetchBackgroundData(context.Background(), "tok", srv.URL, "q", cb, 4)
	require.NoError(t, err, "a maxRows cap must be a success, not an error")
	require.True(t, cb.closed, "OnClose must fire on the capped success path")
	rows := 0
	for _, it := range cb.items {
		if it.Result != nil {
			rows += len(it.Result.Results)
		}
	}
	require.Equal(t, 4, rows, "exactly maxRows rows must be delivered")
}
