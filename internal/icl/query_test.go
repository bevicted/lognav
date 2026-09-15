package icl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/jsonutil"
)

func TestQuery_HappyPathStreamsAndCloses(t *testing.T) {
	t.Parallel()
	const sse = "data: {\"result\":{\"results\":[{\"user_data\":\"{}\",\"metadata\":[{\"key\":\"logid\",\"value\":\"l1\"}]}]}}\n\n" +
		"data: {\"result\":{\"results\":[{\"user_data\":\"{}\"}]}}\n\n"

	var gotReq *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	cb := &recordCB{}
	err := Query(t.Context(), "tok", srv.URL, "source logs", 0, cb)
	require.NoError(t, err)
	assert.Equal(t, 2, cb.dataLen())
	assert.Equal(t, 1, cb.closedCount())
	assert.Equal(t, 0, cb.errCount())

	// request shape
	assert.Equal(t, http.MethodPost, gotReq.Method)
	assert.Equal(t, "/v1/query", gotReq.URL.Path)
	assert.Equal(t, "text/event-stream", gotReq.Header.Get("Accept"))
	assert.Equal(t, "application/json", gotReq.Header.Get("Content-Type"))
	assert.Equal(t, "Bearer tok", gotReq.Header.Get("Authorization"))
	assert.Equal(t, lognavUserAgent, gotReq.Header.Get("User-Agent"))

	// body shape: dates are millis-UTC and span ~1h
	var body queryBody
	require.NoError(t, jsonutil.API.Unmarshal(gotBody, &body))
	assert.Equal(t, "source logs", body.Query)
	assert.Equal(t, int64(SyncQueryLimit), body.Metadata.Limit)
	start, err := time.Parse(iclTimeLayout, body.Metadata.StartDate)
	require.NoError(t, err)
	end, err := time.Parse(iclTimeLayout, body.Metadata.EndDate)
	require.NoError(t, err)
	assert.InDelta(t, time.Hour.Seconds(), end.Sub(start).Seconds(), 2.0)
	assert.Equal(t, "Z", body.Metadata.StartDate[len(body.Metadata.StartDate)-1:])
}

func TestSyncQueryRequestLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		maxRows uint32
		want    uint32
	}{
		{name: "zero uses default", maxRows: 0, want: SyncQueryLimit},
		{name: "lower positive is preserved", maxRows: 42, want: 42},
		{name: "above ceiling is preserved", maxRows: SyncQueryLimit + 1, want: SyncQueryLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SyncQueryRequestLimit(tc.maxRows))
		})
	}
}

func TestQuery_Non2xxReturnsErrorWithoutCallbacks(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied")
	}))
	defer srv.Close()

	cb := &recordCB{}
	err := Query(t.Context(), "tok", srv.URL, "q", 0, cb)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "denied") // server-supplied reason is surfaced
	// setup errors are the caller's to surface; Query does not touch cb here.
	assert.Equal(t, 0, cb.closedCount())
	assert.Equal(t, 0, cb.errCount())
	assert.Equal(t, 0, cb.dataLen())
}

func TestQuery_Non2xxSurfacesSanitizedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Hostile-ish body: ANSI escape + CRLF wrapping the real reason.
		_, _ = io.WriteString(w, "\x1b[31mbad\r\ndataprime: unexpected token 'x'\x1b[0m")
	}))
	defer srv.Close()

	err := Query(t.Context(), "tok", srv.URL, "q", 0, &recordCB{})
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "400")
	assert.Contains(t, msg, "dataprime: unexpected token 'x'") // verbose reason restored
	assert.NotContains(t, msg, "\r")                           // CRLF stripped
	assert.NotContains(t, msg, "\x1b")                         // ANSI escape stripped
}

func TestQuery_Non2xxExtractsStructuredReason(t *testing.T) {
	t.Parallel()
	// Real ICL 400 shape: the {"errors":[...]} envelope with the compile error
	// in message, JSON-escaped newline and all.
	const body = `{"errors":[{"code":"bad_request_or_unspecified","message":"Compilation errors:\n - expected token 'x'"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	err := Query(t.Context(), "tok", srv.URL, "q", 0, &recordCB{})
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "400")
	assert.Contains(t, msg, "bad_request_or_unspecified")   // code surfaced
	assert.Contains(t, msg, "Compilation errors:")          // verbose reason surfaced
	assert.Contains(t, msg, "expected token 'x'")           // full message, not truncated JSON
	assert.Contains(t, msg, "Compilation errors:\n - expe") // escaped \n decoded to a real newline
	assert.NotContains(t, msg, `\n`)                        // no literal backslash-n left
	assert.NotContains(t, msg, `{"errors"`)                 // raw JSON envelope not dumped
}

func TestQuery_CtxCancelMidStreamSurfacesErrorThenCloses(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"result\":{\"results\":[{\"user_data\":\"{}\"}]}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release // hold the connection open so the client cancels mid-stream
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cb := &recordCB{}
	done := make(chan error, 1)
	go func() { done <- Query(ctx, "tok", srv.URL, "q", 0, cb) }()

	require.Eventually(t, func() bool { return cb.dataLen() >= 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.Eventually(t, func() bool { return cb.closedCount() == 1 }, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, <-done) // stream-phase errors go through cb, not the return
	assert.GreaterOrEqual(t, cb.errCount(), 1)
	assert.Equal(t, 1, cb.closedCount())
}
