package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // renameRemove is a package-level filesystem seam
func TestPublishAutoSnapshot_SourceCleanupFailureKeepsPublishedDestination(t *testing.T) {
	dir := t.TempDir()
	wip, err := CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	wipPath := wip.Name()
	_, err = wip.WriteString("payload")
	require.NoError(t, err)
	require.NoError(t, wip.Close())

	origRemove := renameRemove
	renameRemove = func(path string) error {
		if path == wipPath {
			return errors.New("unlink failed")
		}
		return os.Remove(path)
	}
	t.Cleanup(func() { renameRemove = origRemove })

	finalPath, err := PublishAutoSnapshot(wipPath, time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local), nil)
	var cleanupErr *RenameCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	assert.Equal(t, filepath.Join(dir, "auto-20260814-120000.lognav"), finalPath)
	assert.FileExists(t, wipPath)
	assert.FileExists(t, finalPath)
	payload, readErr := os.ReadFile(finalPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, readErr)
	assert.Equal(t, "payload", string(payload))
}
