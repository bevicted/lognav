package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultSetup_NoConfig_UsesDefaults guards the regression where --no-config
// returned an empty Bundle (nil Config), so ui.New -> keys.KeyBindsFrom
// dereferenced a nil cfg.Keys and panicked. --no-config must use the built-in
// defaults, not nil.
func TestPrintVersion_TextJSONAndWriterFailures(t *testing.T) {
	t.Parallel()

	var text bytes.Buffer
	require.NoError(t, printVersion(&text, outputText))
	version := currentVersion()
	assert.Equal(t, "lognav version "+version.Lognav+"\n"+
		"config version "+strconv.Itoa(version.Config)+"\n"+
		"snapshot version "+strconv.FormatUint(uint64(version.Snapshot), 10)+"\n", text.String())

	var jsonOut bytes.Buffer
	require.NoError(t, printVersion(&jsonOut, outputJSON))
	var got versionInfo
	require.NoError(t, json.Unmarshal(jsonOut.Bytes(), &got))
	assert.Equal(t, version, got)

	require.Error(t, printVersion(failingWriter{}, outputText))
	require.Error(t, printVersion(failingWriter{}, outputJSON))
}

func TestDefaultSetup_NoConfig_UsesDefaults(t *testing.T) {
	t.Parallel()
	b, err := defaultSetup(true)
	require.NoError(t, err)
	require.NotNil(t, b.Config, "--no-config must supply a default Config, not nil")
	require.NotNil(t, b.State, "--no-config must supply a state.Manager, not nil")
	assert.NotEmpty(t, b.Config.Keys.Quit, "default Config must have populated keybinds")
}

func TestRenderExit_PrintsErrorOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := renderExit(&buf, nil, WithExit(ExitConfig, errors.New("boom")))
	assert.Equal(t, ExitConfig, ExitCode(err))
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("Error:")))
	assert.Contains(t, buf.String(), "boom")
	assert.NotContains(t, buf.String(), "Usage:")
}

func TestRenderExit_PreservesMultilineErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	message := "load config: parse body: [4:3] unknown field \"typo\"\n  3 | icl:\n> 4 |   typo: value\n        ^"

	err := renderExit(&buf, nil, WithExit(ExitConfig, errors.New(message)))

	require.Error(t, err)
	assert.Equal(t, "Error: "+message+"\n", buf.String())
}

func TestUsageArgs_TagsPlainValidatorErrors(t *testing.T) {
	t.Parallel()
	validator := usageArgs(cobra.ExactArgs(1))
	err := validator(&cobra.Command{}, nil)
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCode(err))
	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

func TestRenderExit_UsageIncludesSelectedCommandUsage(t *testing.T) {
	t.Parallel()
	root := newRootCmd(noopSetup(t))
	cmd, _, findErr := root.Find([]string{"snap", "inspect"})
	require.NoError(t, findErr)
	var buf bytes.Buffer

	err := cmd.Args(cmd, nil)
	err = renderExit(&buf, cmd, err)

	assert.Equal(t, ExitUsage, ExitCode(err))
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("Error:")))
	assert.Contains(t, buf.String(), "Usage:\n  lognav snapshot inspect")
}

func TestRenderExit_NonUsageErrorsHaveNoUsage(t *testing.T) {
	t.Parallel()
	for _, code := range []int{ExitGeneral, ExitData, ExitNoInput, ExitUnavailable, ExitConfig, 130, 143} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var buf bytes.Buffer
			err := renderExit(&buf, nil, WithExit(code, errors.New("boom")))
			assert.Equal(t, code, ExitCode(err))
			assert.Equal(t, "Error: boom\n", buf.String())
		})
	}
}

func TestExecuteRoot_RendersUsageForTypedFailures(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		args      []string
		wantError string
		wantUsage string
	}{
		{name: "unknown flag", args: []string{"--bad-flag"}, wantError: "unknown flag", wantUsage: "lognav [snapshot-name|path]"},
		{name: "unknown shorthand", args: []string{"-z"}, wantError: "unknown shorthand flag", wantUsage: "lognav [snapshot-name|path]"},
		{name: "invalid flag value", args: []string{"--no-config=bad"}, wantError: "invalid argument", wantUsage: "lognav [snapshot-name|path]"},
		{name: "version argument", args: []string{"version", "junk"}, wantError: "unknown command", wantUsage: "lognav version"},
		{name: "invalid output", args: []string{"version", "-o", "bad"}, wantError: "invalid --output", wantUsage: "lognav version"},
		{name: "query selector", args: []string{"query"}, wantError: "at least one target selector", wantUsage: "lognav query"},
		{name: "query argument", args: []string{"query", "junk"}, wantError: "query accepts no positional arguments", wantUsage: "lognav query"},
		{name: "inspect missing argument", args: []string{"snap", "inspect"}, wantError: "accepts 1 arg(s)", wantUsage: "lognav snapshot inspect"},
		{name: "inspect excess arguments", args: []string{"snapshot", "inspect", "one", "two"}, wantError: "accepts 1 arg(s)", wantUsage: "lognav snapshot inspect"},
		{name: "config typo", args: []string{"config", "typo"}, wantError: "unknown command", wantUsage: "lognav config"},
		{name: "snapshot typo", args: []string{"snapshot", "typo"}, wantError: "unknown command", wantUsage: "lognav snapshot"},
		{name: "completion argument", args: []string{"completion", "bash", "junk"}, wantError: "unknown command", wantUsage: "lognav completion bash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd(noopSetup(t))
			var stdout, stderr bytes.Buffer
			root.SetArgs(tt.args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)

			cmd, err := executeRoot(t.Context(), root)
			err = renderExit(&stderr, cmd, err)

			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCode(err))
			assert.Empty(t, stdout.String())
			assert.Equal(t, 1, bytes.Count(stderr.Bytes(), []byte("Error:")))
			assert.Contains(t, stderr.String(), tt.wantError)
			assert.Contains(t, stderr.String(), "Usage:\n  "+tt.wantUsage)
		})
	}
}

func TestExecuteRoot_EmptyHiddenCompletionRequestsAreUsageErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		args         []string
		wantCalledAs string
	}{
		{name: "with descriptions", args: []string{"__complete"}, wantCalledAs: "__complete"},
		{name: "without descriptions", args: []string{"__completeNoDesc"}, wantCalledAs: "__completeNoDesc"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd(noopSetup(t))
			var stdout, stderr bytes.Buffer
			root.SetArgs(tt.args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)

			cmd, err := executeRoot(t.Context(), root)
			err = renderExit(&stderr, cmd, err)

			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCode(err))
			require.NotNil(t, cmd)
			assert.Equal(t, tt.wantCalledAs, cmd.CalledAs())
			assert.Empty(t, stdout.String())
			assert.Equal(t, 1, bytes.Count(stderr.Bytes(), []byte("Error:")))
			assert.Contains(t, stderr.String(), "requires at least 1 arg(s)")
			assert.Contains(t, stderr.String(), "Usage:\n  lognav __complete [command-line]")
		})
	}
}

func TestExecuteRoot_HelpAndGroupsWriteOnlyStdout(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--help"}, {"config"}, {"snapshot"}} {
		root := newRootCmd(noopSetup(t))
		var stdout, stderr bytes.Buffer
		root.SetArgs(args)
		root.SetOut(&stdout)
		root.SetErr(&stderr)

		_, err := executeRoot(t.Context(), root)

		require.NoError(t, err)
		assert.Contains(t, stdout.String(), "Usage:")
		assert.Empty(t, stderr.String())
	}
}
