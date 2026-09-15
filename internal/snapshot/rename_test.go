package snapshot_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/snapshot"
)

func TestRenameNoClobber(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	dst := filepath.Join(dir, "b")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o600))

	require.NoError(t, snapshot.RenameNoClobber(src, dst))
	_, err := os.Stat(src)
	assert.True(t, os.IsNotExist(err), "src gone after rename")
	b, _ := os.ReadFile(dst) // #nosec G304 -- test-only path via t.TempDir
	assert.Equal(t, "x", string(b))

	// Collision: dst already exists -> refuse, src untouched.
	require.NoError(t, os.WriteFile(src, []byte("y"), 0o600))
	err = snapshot.RenameNoClobber(src, dst)
	require.Error(t, err)
	assert.True(t, snapshot.IsExistErr(err), "collision must report exists")
	b, _ = os.ReadFile(dst) // #nosec G304 -- test-only path via t.TempDir
	assert.Equal(t, "x", string(b), "existing dst not clobbered")
	sb, _ := os.ReadFile(src) // #nosec G304 -- test-only path via t.TempDir
	assert.Equal(t, "y", string(sb), "src untouched after collision")
}

func TestPublishAutoSnapshot_CollisionAndCallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	startedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	base := filepath.Join(dir, "auto-20260814-120000.lognav")
	require.NoError(t, os.WriteFile(base, []byte("existing"), 0o600))
	wip := writeWip(t, dir, "new")

	var candidates []string
	finalPath, err := snapshot.PublishAutoSnapshot(wip, startedAt, func(candidate string) error {
		candidates = append(candidates, filepath.Base(candidate))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "auto-20260814-120000-2.lognav"), finalPath)
	assert.Equal(t, []string{"auto-20260814-120000.lognav", "auto-20260814-120000-2.lognav"}, candidates)
	assertFileContents(t, base, "existing")
	assertFileContents(t, finalPath, "new")
}

func TestPublishAutoSnapshot_PreAttemptFailureLeavesWip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wip := writeWip(t, dir, "payload")
	wantErr := errors.New("claim failed")
	finalPath, err := snapshot.PublishAutoSnapshot(wip, time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local), func(string) error {
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, finalPath)
	assertFileContents(t, wip, "payload")
}

func TestPublishAutoSnapshot_ConcurrentPublishersDoNotClobber(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	startedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.Local)
	wips := []string{writeWip(t, dir, "first"), writeWip(t, dir, "second")}
	type result struct {
		path string
		err  error
	}
	results := make(chan result, len(wips))
	var wg sync.WaitGroup
	for _, wip := range wips {
		wg.Add(1)
		go func(wip string) {
			defer wg.Done()
			path, err := snapshot.PublishAutoSnapshot(wip, startedAt, nil)
			results <- result{path: path, err: err}
		}(wip)
	}
	wg.Wait()
	close(results)
	var paths []string
	for result := range results {
		require.NoError(t, result.err)
		paths = append(paths, result.path)
	}
	assert.NotEqual(t, paths[0], paths[1])
	assert.ElementsMatch(t,
		[]string{filepath.Join(dir, "auto-20260814-120000.lognav"), filepath.Join(dir, "auto-20260814-120000-2.lognav")},
		paths,
	)
	assert.ElementsMatch(t, []string{"first", "second"}, []string{readFile(t, paths[0]), readFile(t, paths[1])})
}

func writeWip(t *testing.T, dir, payload string) string {
	t.Helper()
	wip, err := snapshot.CreateWip(dir, os.Getpid())
	require.NoError(t, err)
	_, err = wip.WriteString(payload)
	require.NoError(t, err)
	require.NoError(t, wip.Sync())
	require.NoError(t, wip.Close())
	return wip.Name()
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	assert.Equal(t, want, readFile(t, path))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	return string(got)
}
