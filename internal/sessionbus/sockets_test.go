package sessionbus

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statForOwner is the test seam introduced for D1 coverage.
// Defined as a package-internal var in sockets.go; default uses os.Stat.

func TestSocket_IsSessionAlive_OwnerCheckPasses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pid := os.Getpid()
	path := filepath.Join(dir, strconv.Itoa(pid)+".sock")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	s := Socket{Path: path, PID: pid}
	assert.True(t, s.IsSessionAlive())
}

// TestSocket_IsSessionAlive_ForeignUidFails mutates the package-level
// statForOwner seam to simulate a foreign-owned socket. It cannot run in
// parallel with the other IsSessionAlive tests because they share that var.
//
//nolint:paralleltest // mutates package-level statForOwner; not parallel-safe.
func TestSocket_IsSessionAlive_ForeignUidFails(t *testing.T) {
	dir := t.TempDir()
	pid := os.Getpid()
	path := filepath.Join(dir, strconv.Itoa(pid)+".sock")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	// Substitute statForOwner to return a foreign uid.
	prev := statForOwner
	statForOwner = func(p string) (uint32, error) { return 65534, nil }
	t.Cleanup(func() { statForOwner = prev })
	s := Socket{Path: path, PID: pid}
	assert.False(t, s.IsSessionAlive())
}

func TestSocket_IsSessionAlive_DeadPID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pid := math.MaxInt32
	path := filepath.Join(dir, strconv.Itoa(pid)+".sock")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	s := Socket{Path: path, PID: pid}
	assert.False(t, s.IsSessionAlive())
}

func TestGetSocketFromPath_RequiresSocketSuffixAndPositivePID(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"socket", "0.sock", "-1.sock", "01.sock", "not-a-pid.sock"} {
		t.Run(path, func(t *testing.T) {
			_, err := getSocketFromPath(filepath.Join(t.TempDir(), path))
			assert.Error(t, err)
		})
	}
}

// Root-only chown variant. Runs only on linux when uid==0.
//
//nolint:paralleltest // runtime.GOOS-gated; not parallelizable on shared FS state.
func TestSocket_IsSessionAlive_ForeignUidFails_RootChown(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getuid() != 0 {
		t.Skip("requires linux + root for chown")
	}
	dir := t.TempDir()
	pid := os.Getpid()
	path := filepath.Join(dir, strconv.Itoa(pid)+".sock")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, syscall.Chown(path, 65534, 65534))
	s := Socket{Path: path, PID: pid}
	assert.False(t, s.IsSessionAlive())
}
