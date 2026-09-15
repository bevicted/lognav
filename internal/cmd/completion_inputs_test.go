package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/snapshot"
)

// TestNoArgumentCompletionSuppressesFiles keeps no-argument leaves from
// falling back to shell filesystem completion.
func TestNoArgumentCompletionSuppressesFiles(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	root := newRootCmd(noopSetup(t))
	for _, path := range [][]string{
		{"version"},
		{"login"},
		{"logout"},
		{"query"},
		{"snapshot", "list"},
		{"snapshot", "prune"},
		{"config", "show"},
		{"config", "path"},
		{"config", "edit"},
	} {
		cmd := root
		if len(path) > 0 {
			var err error
			cmd, _, err = root.Find(path)
			require.NoError(t, err, strings.Join(path, " "))
		}
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			require.NotNil(t, cmd.ValidArgsFunction)
			got, directive := cmd.ValidArgsFunction(cmd, nil, "")
			assert.Empty(t, got)
			assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
		})
	}
}

// TestSnapshotSelectorCompletionMatchesAcceptedInputs checks selectors,
// external path fallback, and positional cardinality without invoking a TUI.
func TestSnapshotSelectorCompletionMatchesAcceptedInputs(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const id = "abcd1234-0000-4000-8000-000000000001"
	seedSnapshot(t, "incident.lognav", snapshot.Snapshot{ID: id}, nil)

	got, directive := completeNameOrPath(nil, nil, "")
	assert.Contains(t, got, "incident")
	assert.Equal(t, cobra.ShellCompDirectiveDefault, directive)
	got, directive = completeNameOrPath(nil, []string{"incident"}, "")
	assert.Empty(t, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	inspect := newSnapshotInspect()
	got, directive = completeSnapshotNames(inspect, nil, "")
	assert.Contains(t, got, "incident")
	assert.Contains(t, got, id)
	assert.Contains(t, got, latestSentinel)
	assert.Equal(t, cobra.ShellCompDirectiveDefault, directive)
	got, directive = completeSnapshotNames(inspect, []string{"incident"}, "")
	assert.Empty(t, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	logs := newSnapshotLogs()
	_, directive = completeSnapshotNames(logs, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveDefault, directive)
	for _, cmd := range []*cobra.Command{newSnapshotPath(), newSnapshotClip()} {
		got, directive = completeSnapshotNames(cmd, []string{"incident"}, "")
		assert.Empty(t, got, cmd.Name())
		assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, cmd.Name())
	}

	rm := newSnapshotRm()
	got, directive = completeSnapshotNames(rm, []string{id}, "")
	assert.NotContains(t, got, "incident")
	assert.NotContains(t, got, id)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

// TestSnapshotInstanceCompletionSupportsExternalPaths checks that --instance
// follows the command's name-or-path selector semantics and filters its prefix.
func TestSnapshotSelectorCompletionOmitsAmbiguousIDs(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const id = "abcd1234-0000-4000-8000-000000000001"
	seedSnapshot(t, "original.lognav", snapshot.Snapshot{ID: id}, nil)
	seedSnapshot(t, "adopted-copy.lognav", snapshot.Snapshot{ID: id}, nil)

	for _, tt := range []struct {
		cmd       *cobra.Command
		directive cobra.ShellCompDirective
	}{
		{newSnapshotInspect(), cobra.ShellCompDirectiveDefault},
		{newSnapshotLogs(), cobra.ShellCompDirectiveDefault},
		{newSnapshotRm(), cobra.ShellCompDirectiveNoFileComp},
		{newSnapshotPath(), cobra.ShellCompDirectiveNoFileComp},
		{newSnapshotClip(), cobra.ShellCompDirectiveNoFileComp},
	} {
		t.Run(tt.cmd.Name(), func(t *testing.T) {
			got, directive := completeSnapshotNames(tt.cmd, nil, "")
			assert.Contains(t, got, "original")
			assert.Contains(t, got, "adopted-copy")
			assert.NotContains(t, got, id)
			assert.Equal(t, tt.directive, directive)
		})
	}
}

func TestSnapshotSelectorCompletionSuppressesScanAndMetadataFailures(t *testing.T) {
	t.Run("scan", func(t *testing.T) {
		setIsolatedXDG(t)
		dataHome := filepath.Join(t.TempDir(), "data-home")
		require.NoError(t, os.WriteFile(dataHome, nil, 0o600))
		t.Setenv("XDG_DATA_HOME", dataHome)

		got, directive := completeSnapshotNames(newSnapshotInspect(), nil, "")
		assert.Empty(t, got)
		assert.Equal(t, cobra.ShellCompDirectiveDefault, directive)
	})
	t.Run("metadata", func(t *testing.T) {
		setIsolatedXDG(t)
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		const id = "abcd1234-0000-4000-8000-000000000001"
		seedSnapshot(t, "readable.lognav", snapshot.Snapshot{ID: id}, nil)
		dir, err := snapshot.Dir()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.lognav"), []byte("not a snapshot"), 0o600))

		got, directive := completeSnapshotNames(newSnapshotInspect(), nil, "")
		assert.Contains(t, got, "readable")
		assert.Contains(t, got, id)
		assert.Contains(t, got, "broken")
		assert.Equal(t, cobra.ShellCompDirectiveDefault, directive)
	})
}

func TestSnapshotInstanceCompletionSupportsExternalPaths(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	external := filepath.Join(t.TempDir(), "external.lognav")
	writeContainerAt(t, external, snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{{CRN: "alpha"}}},
	}, nil)

	got, directive := completeInstanceNames(newSnapshotLogs(), []string{external}, "us-south/a")
	assert.Equal(t, []string{"us-south/alpha"}, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	got, directive = completeInstanceNames(newSnapshotLogs(), []string{filepath.Join(t.TempDir(), "missing.lognav")}, "")
	assert.Empty(t, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

// TestCompletionProtocolSnapshotSelectors exercises selector candidates and
// directives through Cobra's shell-facing protocol.
func TestCompletionProtocolSnapshotSelectors(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const id = "abcd1234-0000-4000-8000-000000000001"
	seedSnapshot(t, "incident.lognav", snapshot.Snapshot{ID: id}, nil)

	for _, tt := range []struct {
		name  string
		args  []string
		want  []string
		exact bool
	}{
		{"root", []string{""}, []string{"latest", "incident", ":0"}, false},
		{"inspect", []string{"snapshot", "inspect", ""}, []string{"latest", "incident", id, ":0"}, true},
		{"logs", []string{"snapshot", "logs", ""}, []string{"latest", "incident", id, ":0"}, true},
		{"path", []string{"snapshot", "path", ""}, []string{"latest", "incident", id, ":4"}, true},
		{"clip", []string{"snapshot", "clip", ""}, []string{"latest", "incident", id, ":4"}, true},
		{"rm excludes selected ID", []string{"snapshot", "rm", id, ""}, []string{"latest", ":4"}, true},
		{"inspect max args", []string{"snapshot", "inspect", "incident", ""}, []string{":4"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd(noopSetup(t))
			var out bytes.Buffer
			root.SetArgs(append([]string{"__complete"}, tt.args...))
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			require.NoError(t, root.Execute())
			if tt.exact {
				assert.Equal(t, strings.Join(tt.want, "\n")+"\n", out.String())
				return
			}
			for _, want := range tt.want {
				assert.Contains(t, out.String(), want+"\n")
			}
		})
	}
}

// TestCompletionProtocolDirectives exercises Cobra's shell-facing protocol for
// scalar values and the path-accepting completion cases.
func TestCompletionProtocolDirectives(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"prune duration", []string{"snapshot", "prune", "--older-than", ""}, ":4\n"},
		{"prune count", []string{"snapshot", "prune", "--keep", ""}, ":4\n"},
		{"adopt name", []string{"snapshot", "adopt", "--name", ""}, ":4\n"},
		{"completion profile path", []string{"completion", "bash", "--profile", ""}, ":0\n"},
		{"adopt path", []string{"snapshot", "adopt", ""}, ":0\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd(noopSetup(t))
			var out bytes.Buffer
			root.SetArgs(append([]string{"__complete"}, tt.args...))
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			require.NoError(t, root.Execute())
			assert.Equal(t, tt.want, out.String())
		})
	}
}
