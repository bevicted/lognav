// Package snapshottest provides test helpers for building real snapshot
// containers on disk.
package snapshottest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/snapshot"
)

// NewContainerFile creates a file at path and returns a writable container for
// it — the same shape as the production save path (create the file, wrap it in
// snapshot.NewWriter). The caller closes the container; the backing file is
// closed on test cleanup.
func NewContainerFile(t *testing.T, path string) *snapshot.Container {
	t.Helper()
	f, err := os.Create(path) // #nosec G304 -- test-only path
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return snapshot.NewWriter(f)
}
