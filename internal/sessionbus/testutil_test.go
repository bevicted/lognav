package sessionbus

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/jsonutil"
)

// recvNotification represents a notification delivered to a mockPeer.
type recvNotification struct {
	Method string
	Body   []byte
}

// mockPeer creates a fake listener at <dir>/<pid>.sock that accepts HTTP
// POSTs and forwards them (method + body) to the returned channel. The
// returned cleanup func must be deferred by the caller.
func mockPeer(t *testing.T, dir string, pid int) (<-chan recvNotification, func()) {
	t.Helper()

	sockPath := filepath.Join(dir, intToSockName(pid))
	require.NoError(t, os.MkdirAll(dir, 0o700))

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sockPath)
	require.NoError(t, err)

	ch := make(chan recvNotification, 16)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var req Request
			_ = jsonutil.API.Unmarshal(body, &req)
			ch <- recvNotification{Method: req.Method, Body: body}
			_, _ = w.Write([]byte(`{"result":"ok"}` + "\n"))
		}),
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		_ = srv.Serve(ln)
	}()

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = ln.Close()
	}
	return ch, cleanup
}

// intToSockName matches the production convention `<pid>.sock`.
func intToSockName(pid int) string {
	return strconv.Itoa(pid) + ".sock"
}

// shortTempDir creates a temp dir rooted at /tmp to stay within the 104-byte
// Unix socket path limit on macOS. The dir is registered for cleanup via
// t.Cleanup. Use instead of t.TempDir() when the result feeds into XDG_STATE_HOME
// and the path will be extended with "/lognav/sessionbus/<pid>.sock".
func shortTempDir(t *testing.T) string {
	t.Helper()
	// t.TempDir() on macOS generates paths under /var/folders/… that exceed the
	// 104-byte sockaddr_un.sun_path limit once appended with "/lognav/sessionbus/<pid>.sock".
	dir, err := os.MkdirTemp("/tmp", "lognav-test-*") //nolint:usetesting // see comment above
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
