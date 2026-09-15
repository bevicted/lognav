package cmd

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/snapshot/snapshottest"
)

func TestClipFileObjectPassesPathAsArg(t *testing.T) {
	var gotName string
	var gotArgs []string
	origRun, origUseFile := runClipCommand, clipUsesFileObject
	runClipCommand = func(name string, args ...string) error { gotName, gotArgs = name, args; return nil }
	clipUsesFileObject = func() bool { return true }
	defer func() { runClipCommand, clipUsesFileObject = origRun, origUseFile }()

	tricky := filepath.Join(t.TempDir(), `evil" do shell script "touch pwned`+".lognav")
	require.NoError(t, clipFileObject(tricky))

	require.Equal(t, "osascript", gotName)
	require.Equal(t, tricky, gotArgs[len(gotArgs)-1], "path must be the trailing argv, never interpolated into -e script source")
	for _, a := range gotArgs[:len(gotArgs)-1] {
		require.NotContains(t, a, "pwned", "the path must not appear inside any -e script fragment")
	}
}

func TestClipPathFallback(t *testing.T) {
	// Not parallel: clip resolves a managed NAME (clipping a raw path is out of
	// scope). Seed the managed dir under an isolated XDG_DATA_HOME.
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := snapshot.Dir()
	require.NoError(t, err)
	path := filepath.Join(dir, "shared.lognav")
	c := snapshottest.NewContainerFile(t, path)
	require.NoError(t, snapshot.SaveStateFrame(c, snapshot.Snapshot{Query: "q"}))
	require.NoError(t, c.Close())

	var copied string
	origWrite, origUseFile := clipboardWrite, clipUsesFileObject
	clipboardWrite = func(s string) error { copied = s; return nil }
	clipUsesFileObject = func() bool { return false }
	defer func() { clipboardWrite, clipUsesFileObject = origWrite, origUseFile }()

	cmd := newSnapshotClip()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"shared"}) // managed NAME, not a raw path
	require.NoError(t, cmd.Execute())
	require.Equal(t, path, copied) // clip copies the managed file's absolute path
	require.Contains(t, out.String(), "(path)")
}
