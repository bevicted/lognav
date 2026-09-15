package sessionbus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSockets_RemovesDeadPIDSocket(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", shortTempDir(t))
	dir, err := SocketDir()
	require.NoError(t, err)
	deadPath := filepath.Join(dir, "999999.sock")
	require.NoError(t, os.WriteFile(deadPath, nil, 0o600))

	sockets, err := ListSockets()
	require.NoError(t, err)
	assert.Empty(t, sockets)
	assert.NoFileExists(t, deadPath)
}

func TestListSockets_ReturnsLivePeer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", shortTempDir(t))
	dir, err := SocketDir()
	require.NoError(t, err)
	pid := os.Getppid()
	_, cleanup := mockPeer(t, dir, pid)
	defer cleanup()

	sockets, err := ListSockets()
	require.NoError(t, err)
	require.Len(t, sockets, 1)
	assert.Equal(t, pid, sockets[0].PID)
	assert.Equal(t, filepath.Join(dir, intToSockName(pid)), sockets[0].Path)
}

func TestListSockets_RemovesSocketEntrySymlinkWithoutTouchingTarget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", shortTempDir(t))
	dir, err := SocketDir()
	require.NoError(t, err)

	protected := filepath.Join(t.TempDir(), "protected")
	require.NoError(t, os.WriteFile(protected, []byte("keep"), 0o600))
	link := filepath.Join(dir, "999999.sock")
	require.NoError(t, os.Symlink(protected, link))

	sockets, err := ListSockets()
	require.NoError(t, err)
	assert.Empty(t, sockets)
	assert.NoFileExists(t, link)
	contents, err := os.ReadFile(protected) // #nosec G304 -- protected is this test's temp fixture.
	require.NoError(t, err)
	assert.Equal(t, []byte("keep"), contents)
}

//nolint:paralleltest // mutates the package-level statForOwner seam.
func TestListSockets_RemovesForeignOwnerSocket(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", shortTempDir(t))
	dir, err := SocketDir()
	require.NoError(t, err)
	path := filepath.Join(dir, intToSockName(os.Getpid()))
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	previous := statForOwner
	statForOwner = func(string) (uint32, error) { return 65534, nil }
	t.Cleanup(func() { statForOwner = previous })

	sockets, err := ListSockets()
	require.NoError(t, err)
	assert.Empty(t, sockets)
	assert.NoFileExists(t, path)
}
