package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

type peerEvents struct {
	mu     sync.Mutex
	events []uv.Event
}

func (p *peerEvents) send(event uv.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *peerEvents) snapshot() []uv.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uv.Event(nil), p.events...)
}

//nolint:paralleltest // command seams, XDG_STATE_HOME, and PID-named sockets are process-global.
func TestCLIBroadcaster_NotifiesGenericPeersAndExcludesSender(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lognav-sessionbus-*") //nolint:usetesting // socket path must fit sockaddr_un.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	oldStream, oldTTY, oldResolver := queryStream, isTTY, resolveQueryToken
	t.Cleanup(func() { queryStream, isTTY, resolveQueryToken = oldStream, oldTTY, oldResolver })
	isTTY = func() bool { return false }
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = queryOneLog

	selfCapture := &peerEvents{}
	self := sessionbus.NewServer(selfCapture.send)
	peerCapture := &peerEvents{}
	peerPID := os.Getppid()
	peer := sessionbus.NewServer(peerCapture.send, sessionbus.Socket{
		Path: filepath.Join(dir, "lognav", "sessionbus", fmt.Sprintf("%d.sock", peerPID)),
		PID:  peerPID,
	})
	peer.Start(t.Context())
	self.Start(t.Context())
	t.Cleanup(func() { _ = peer.Close(context.Background()) })
	t.Cleanup(func() { _ = self.Close(context.Background()) })
	require.Eventually(t, func() bool {
		_, err := os.Stat(peerSocketPath(dir, peerPID))
		return err == nil
	}, time.Second, 10*time.Millisecond)

	stdout, _, err := runQueryCommand(t, queryTestConfig(), "source logs", "query", "--instance", "test")
	require.NoError(t, err)
	selector := strings.TrimSpace(stdout)
	require.NotEmpty(t, selector)
	entry, err := snapshot.Resolve(selector)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(peerCapture.snapshot()) == 1 }, time.Second, 10*time.Millisecond)

	_, _, err = runSnapshot(t, "rm", selector)
	require.NoError(t, err)
	assert.NoFileExists(t, entry.Path)
	require.Eventually(t, func() bool { return len(peerCapture.snapshot()) == 2 }, time.Second, 10*time.Millisecond)

	// Snapshot rename has no CLI command, so exercise its real broadcaster.
	require.NoError(t, sessionbus.NewClient().Broadcast(t.Context(), sessionbus.MethodSnapshotRenamed,
		sessionbus.SnapshotRenamedParams{From: "before.lognav", To: "after.lognav"}))
	require.Eventually(t, func() bool { return len(peerCapture.snapshot()) == 3 }, time.Second, 10*time.Millisecond)

	events := peerCapture.snapshot()
	for i := range 2 {
		_, ok := events[i].(msgs.SnapshotsDirtyMsg)
		assert.True(t, ok)
	}
	rename, ok := events[2].(msgs.SnapshotRenamedMsg)
	require.True(t, ok)
	assert.Equal(t, "before.lognav", rename.From)
	assert.Equal(t, "after.lognav", rename.To)
	assert.Empty(t, selfCapture.snapshot(), "the broadcaster must exclude its own PID")

	stale := filepath.Join(dir, "lognav", "sessionbus", "999999.sock")
	require.NoError(t, os.WriteFile(stale, nil, 0o600))
	require.NoError(t, sessionbus.NewClient().Broadcast(t.Context(), sessionbus.MethodSnapshotsDirty, sessionbus.SnapshotsDirtyParams{}))
	require.Eventually(t, func() bool { return len(peerCapture.snapshot()) == 4 }, time.Second, 10*time.Millisecond)
	assert.NoFileExists(t, stale, "discovery removes dead-PID sockets")

	require.NoError(t, self.Close(t.Context()))
	require.NoError(t, self.Close(t.Context()))
	assert.NoFileExists(t, selfSocketPath(dir))
}

func peerSocketPath(stateHome string, pid int) string {
	return filepath.Join(stateHome, "lognav", "sessionbus", fmt.Sprintf("%d.sock", pid))
}

func selfSocketPath(stateHome string) string {
	return peerSocketPath(stateHome, os.Getpid())
}
