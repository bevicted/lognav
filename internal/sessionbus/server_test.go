package sessionbus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/msgs"
)

type captureSend struct {
	mu     sync.Mutex
	events []uv.Event
}

func (c *captureSend) send(ev uv.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *captureSend) snapshot() []uv.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uv.Event(nil), c.events...)
}

func TestServerDispatchesOnlySnapshotNotifications(t *testing.T) {
	t.Parallel()
	capture := &captureSend{}
	s := NewServer(capture.send)
	params, err := json.Marshal(SnapshotRenamedParams{From: "old.lognav", To: "new.lognav"})
	require.NoError(t, err)
	assert.Empty(t, s.dispatch(Request{Method: MethodSnapshotRenamed, Params: params}).Error)
	assert.Empty(t, s.dispatch(Request{Method: MethodSnapshotsDirty}).Error)
	assert.Contains(t, s.dispatch(Request{Method: "status"}).Error, "unknown method")

	events := capture.snapshot()
	require.Len(t, events, 2)
	rename, ok := events[0].(msgs.SnapshotRenamedMsg)
	require.True(t, ok)
	assert.Equal(t, "old.lognav", rename.From)
	assert.Equal(t, "new.lognav", rename.To)
	_, ok = events[1].(msgs.SnapshotsDirtyMsg)
	assert.True(t, ok)
}

func TestServerRejectsUnsafeRename(t *testing.T) {
	t.Parallel()
	capture := &captureSend{}
	s := NewServer(capture.send)
	params, err := json.Marshal(SnapshotRenamedParams{From: "../bad", To: "new.lognav"})
	require.NoError(t, err)
	assert.Equal(t, "invalid rename notify", s.dispatch(Request{Method: MethodSnapshotRenamed, Params: params}).Error)
	assert.Empty(t, capture.snapshot())
}

func TestServerRejectsMalformedHTTPRequests(t *testing.T) {
	t.Parallel()
	s := NewServer(func(uv.Event) {})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	request.Body = io.NopCloser(strings.NewReader("not json"))
	response := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	response = httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, response.Code)
}

func TestServerClose_FirstErrorThenNil(t *testing.T) {
	t.Parallel()
	s := newServerWithNonEmptySocketDir(t)
	err := s.Close(t.Context())
	require.Error(t, err, "the first close reports socket removal failure")
	assert.NoError(t, s.Close(t.Context()), "later closes are no-ops")
}

func TestServerClose_ConcurrentCallsOnlyOneOwnsCleanup(t *testing.T) {
	t.Parallel()
	s := newServerWithNonEmptySocketDir(t)
	s.closeMu.Lock()
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { errs <- s.Close(t.Context()) })
	}
	s.closeMu.Unlock()
	wg.Wait()
	close(errs)

	var errorCount int
	for err := range errs {
		if err != nil {
			errorCount++
		}
	}
	assert.Equal(t, 1, errorCount)
	assert.NoError(t, s.Close(t.Context()))
}

func newServerWithNonEmptySocketDir(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep"), []byte("keep"), 0o600))
	s := NewServer(func(uv.Event) {})
	s.socket.Path = dir
	return s
}

//nolint:paralleltest // XDG_STATE_HOME and PID-named socket are process-global.
func TestServerIntegration_ServesSnapshotNotificationsAndClosesIdempotently(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lognav-session-*") //nolint:usetesting // socket path must fit sockaddr_un.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)

	capture := &captureSend{}
	s := NewServer(capture.send)
	s.Start(t.Context())
	require.NotEmpty(t, s.socket.Path)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	require.Eventually(t, func() bool { _, err := os.Stat(s.socket.Path); return err == nil }, time.Second, 10*time.Millisecond)
	assert.Contains(t, s.socket.Path, filepath.Join("lognav", socketDir))

	_, err = CallSession(t.Context(), s.socket.Path, MethodSnapshotRenamed, SnapshotRenamedParams{From: "old.lognav", To: "new.lognav"})
	require.NoError(t, err)
	_, err = CallSession(t.Context(), s.socket.Path, MethodSnapshotsDirty, SnapshotsDirtyParams{})
	require.NoError(t, err)

	events := capture.snapshot()
	require.Len(t, events, 2)
	_, ok := events[0].(msgs.SnapshotRenamedMsg)
	assert.True(t, ok)
	_, ok = events[1].(msgs.SnapshotsDirtyMsg)
	assert.True(t, ok)

	require.NoError(t, s.Close(t.Context()))
	require.NoError(t, s.Close(t.Context()))
	assert.NoFileExists(t, s.socket.Path)
}
