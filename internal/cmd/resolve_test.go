package cmd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
)

func TestIsPathArg(t *testing.T) {
	t.Parallel()
	require.True(t, isPathArg("./foo.lognav"))
	require.True(t, isPathArg("/tmp/foo"))
	require.True(t, isPathArg("foo.lognav")) // .lognav suffix => path
	require.False(t, isPathArg("foo"))       // bare name
	require.False(t, isPathArg("latest"))    // sentinel, not a path
}

func TestResolveNameOrPath_Path(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "shared.lognav")
	c := snapshottest.NewContainerFile(t, path)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "q"}))
	require.NoError(t, c.Close())

	e, err := resolveNameOrPath(path)
	require.NoError(t, err)
	require.Equal(t, path, e.Path)
}

func TestResolveNameOrPath_RejectsWip(t *testing.T) {
	t.Parallel()
	_, err := resolveNameOrPath("/tmp/a.lognav.wip")
	require.Error(t, err)
}

func TestResolveLaunchTarget_InPlacePath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "shared.lognav")
	c := snapshottest.NewContainerFile(t, path)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "q"}))
	require.NoError(t, c.Close())

	got, err := resolveLaunchTarget(path, false)
	require.NoError(t, err)
	require.Equal(t, path, got, "in-place open returns the external path unchanged")
}

func TestResolveLaunchTarget_AdoptRequiresPath(t *testing.T) {
	t.Parallel()
	_, err := resolveLaunchTarget("somename", true)
	require.Error(t, err)
	require.Equal(t, ExitUsage, ExitCode(err))
}
