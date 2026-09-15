package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/state"
)

// noopSetup returns a fixture bundle from depstest.NewTest, never loading
// real config or constructing real state.
func noopSetup(t *testing.T) func(noConfig bool) (deps.Bundle, error) {
	t.Helper()
	bundle := depstest.NewTest(t)
	return func(noConfig bool) (deps.Bundle, error) { return bundle, nil }
}

// setIsolatedXDG redirects XDG_STATE_HOME and XDG_CONFIG_HOME to per-test
// temp directories so PersistentPostRun (logging.Cleanup) and config
// lookups never touch the real user filesystem.
func setIsolatedXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// Note: these integration tests use t.Setenv to isolate XDG paths. Go 1.25+
// enforces that t.Setenv and t.Parallel are mutually exclusive (env vars are
// process-global). All tests here are sequential as a result.

//nolint:paralleltest // replaces Execute's process-global root seam
func TestExecute_SignalWatcherStops(t *testing.T) {
	oldRoot := newExecuteRoot
	defer func() { newExecuteRoot = oldRoot }()
	newExecuteRoot = func() *cobra.Command {
		root := newRootCmd(noopSetup(t))
		root.SetArgs([]string{"version"})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		return root
	}
	require.NoError(t, Execute())
}

// The ready and committed barriers model the former final context check and
// the selector write. They force both signal orderings in the otherwise tiny
// window between them, without relying on process-signal scheduling.
func TestQueryOutcomeGate_SerializesSignalAndSelectorCommit(t *testing.T) {
	for _, tt := range []struct {
		name         string
		signalFirst  bool
		wantOutput   string
		wantCanceled bool
	}{
		{name: "signal wins after final check", signalFirst: true, wantCanceled: true},
		{name: "selector commit wins before write", wantOutput: "selector\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			baseCtx, cancel := context.WithCancel(t.Context())
			gate := newQueryOutcomeGate(cancel)
			ctx := context.WithValue(baseCtx, queryOutcomeGateContextKey{}, gate)
			ready := make(chan struct{})
			commit := make(chan struct{})
			committed := make(chan struct{})
			write := make(chan struct{})
			wrote := make(chan bool, 1)
			var stdout bytes.Buffer

			go func() {
				if ctx.Err() != nil {
					wrote <- false
					return
				}
				close(ready)
				<-commit
				if !commitQueryOutcome(ctx) {
					wrote <- false
					return
				}
				close(committed)
				<-write
				_, err := io.WriteString(&stdout, "selector\n")
				wrote <- err == nil
			}()

			<-ready
			if tt.signalFirst {
				require.True(t, gate.cancelForSignal())
				close(commit)
			} else {
				close(commit)
				<-committed
				require.False(t, gate.cancelForSignal())
				close(write)
			}

			assert.Equal(t, tt.wantOutput != "", <-wrote)
			assert.Equal(t, tt.wantOutput, stdout.String())
			if tt.wantCanceled {
				assert.ErrorIs(t, ctx.Err(), context.Canceled)
			} else {
				assert.NoError(t, ctx.Err())
			}
		})
	}
}

//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_VersionFlag_PrintsVersionAndExitsZero(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"--version"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "lognav version")
	assert.Empty(t, stderr.String())
}

//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_HelpFlag_PrintsUsage(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"--help"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.NotEmpty(t, stdout.String())
	assert.Empty(t, stderr.String())
}

// Exercises ExitNoInput: a bare name that is not a managed snapshot returns
// ExitNoInput (the root positional arg is now a snapshot name or path).
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_UnknownSnapshotName_ReturnsExitNoInput(t *testing.T) {
	setIsolatedXDG(t)

	var stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"definitely-not-a-snapshot"})
	root.SetOut(io.Discard)
	root.SetErr(&stderr)

	err := renderExit(io.Discard, nil, root.Execute())
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitNoInput, ee.Code,
		"unknown snapshot name flows through resolveLaunchTarget → ExitNoInput")
}

//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize.
func TestExecute_RetiredCommandIsUnknownUsage(t *testing.T) {
	setIsolatedXDG(t)

	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{retiredCommand})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	err := renderExit(io.Discard, nil, root.Execute())
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCode(err))
	assert.EqualError(t, err, `unknown command "`+retiredCommand+`" for "lognav"`)
}

// Exercises typed root positional validation for excess launch arguments.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_TooManyArgs_ReturnsExitUsage(t *testing.T) {
	setIsolatedXDG(t)

	var stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"arg1", "arg2"})
	root.SetOut(io.Discard)
	root.SetErr(&stderr)

	err := renderExit(io.Discard, nil, root.Execute())
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitUsage, ee.Code,
		"too many positional args carry ExitUsage from the validator")
}

// Exercises the SetFlagErrorFunc path for unknown flags.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_UnknownFlag_ReturnsExitUsage(t *testing.T) {
	setIsolatedXDG(t)

	var stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"--definitely-not-a-flag"})
	root.SetOut(io.Discard)
	root.SetErr(&stderr)

	err := root.Execute()
	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, ExitUsage, ee.Code)
}

//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_Docs_PrintsTopicURL(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"docs", "exit-codes", "--no-open"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "/docs/user/exit-codes.md")
	assert.Empty(t, stderr.String())
}

//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_Docs_UnknownTopicIsUsageError(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"docs", "definitely-not-a-topic", "--no-open"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCode(err))
	require.ErrorContains(t, err, "unknown documentation topic")
	assert.NotContains(t, stdout.String(), "https://")
	assert.Empty(t, stderr.String())
}

// TestExecute_CompletionBash_ProducesNonEmptyScript pins the G[P2] D9
// contract: the custom `lognav completion bash` subcommand produces a
// non-empty script without loading configuration.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_CompletionBash_ProducesNonEmptyScript(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{"completion", "bash"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.NotEmpty(t, stdout.String())
}

// failingSetup simulates an invalid/unparseable config file. Explicit bundle
// consumers return its ExitConfig error; independent commands do not invoke it.
func failingSetup() func(noConfig bool) (deps.Bundle, error) {
	return func(noConfig bool) (deps.Bundle, error) {
		return deps.Bundle{}, WithExit(ExitConfig, errors.New("invalid config"))
	}
}

// `config edit` must not be blocked by an invalid config: it only needs the
// file path. EDITOR is set to the no-op `true` binary so exec exits 0.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_ConfigEdit_RunsDespiteConfigLoadFailure(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("EDITOR", "true")

	var stdout, stderr bytes.Buffer
	root := newRootCmd(failingSetup())
	root.SetArgs([]string{"config", "edit"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.Empty(t, stderr.String())
}

// The skip is scoped to `config edit`: sibling subcommands that read the bundle
// still surface the config load failure.
//
//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_InvalidEffectiveInstanceConfig_PreventsCommandAndTUIStartup(t *testing.T) {
	setIsolatedXDG(t)
	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte("version: 1\nicl:\n  instances:\n    - name: missing\n"), 0o600))

	t.Run("config show exits config without output", func(t *testing.T) {
		var stdout bytes.Buffer
		root := newRootCmd(defaultSetup)
		root.SetArgs([]string{"config", "show"})
		root.SetOut(&stdout)
		root.SetErr(io.Discard)

		err := root.Execute()
		require.Error(t, err)
		assert.Equal(t, ExitConfig, ExitCode(err))
		assert.Empty(t, stdout.String())
	})

	t.Run("TUI does not start", func(t *testing.T) {
		root := newRootCmd(defaultSetup)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)

		err := root.Execute()
		require.Error(t, err)
		assert.Equal(t, ExitConfig, ExitCode(err))
	})
}

//nolint:paralleltest // t.Setenv modifies process-global env; cannot parallelize
func TestExecute_ConfigGet_FailsOnConfigLoadFailure(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(failingSetup())
	root.SetArgs([]string{"config", "get"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.Error(t, root.Execute())
}

// Leaf commands that take no positional args must reject junk rather than
// silently swallowing it. A missing NoArgs guard let `logout typo` delete the
// session and `version x` print output, both exiting 0.
//
//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_NoArgsCommands_RejectUnexpectedArgs(t *testing.T) {
	setIsolatedXDG(t)
	for _, args := range [][]string{
		{"version", "junk"},
		{"logout", "junk"},
	} {
		t.Run(args[0], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := newRootCmd(noopSetup(t))
			root.SetArgs(args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)

			err := renderExit(io.Discard, nil, root.Execute())
			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCode(err))
		})
	}
}

// Root --output is intentionally absent. Structured version output belongs
// solely to the dedicated version command.
//
//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_RootOutputFlagIsUsageError(t *testing.T) {
	setIsolatedXDG(t)

	for _, args := range [][]string{{"-o", "json"}, {"--version", "-o", "json"}} {
		var stdout, stderr bytes.Buffer
		root := newRootCmd(noopSetup(t))
		root.SetArgs(args)
		root.SetOut(&stdout)
		root.SetErr(&stderr)

		err := renderExit(io.Discard, nil, root.Execute())
		require.Error(t, err)
		assert.Equal(t, ExitUsage, ExitCode(err))
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_VersionDoesNotLoadConfigAndJSONUsesConstants(t *testing.T) {
	setIsolatedXDG(t)

	for _, tt := range []struct {
		name string
		args []string
		json bool
	}{
		{name: "root flag", args: []string{"--version"}},
		{name: "command json", args: []string{"version", "-o", "json"}, json: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			root := newRootCmd(failingSetup())
			root.SetArgs(tt.args)
			root.SetOut(&stdout)
			root.SetErr(io.Discard)

			require.NoError(t, root.Execute())
			if tt.json {
				assert.Contains(t, stdout.String(), `"config": `+strconv.Itoa(config.CurrentVersion))
				assert.Contains(t, stdout.String(), `"snapshot": `+strconv.FormatUint(uint64(snapshot.CurrentFileVersion), 10))
			} else {
				assert.NotContains(t, stdout.String(), `{`)
				assert.Contains(t, stdout.String(), "config version "+strconv.Itoa(config.CurrentVersion))
			}
		})
	}
}

// Commands that do not consume the loaded config must remain available when
// setup reports an invalid config. Errors from their own filesystem selectors
// are expected, but none may be ExitConfig.
//
//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_ConfigIndependentCommandsBypassBrokenConfig(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("EDITOR", "true")
	missing := filepath.Join(t.TempDir(), "missing.lognav")

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"root version", []string{"--version"}},
		{"version", []string{"version"}},
		{"instruct", []string{"instruct"}},
		{"config group", []string{"config"}},
		{"config set", []string{"config", "set", "core.enableMouse", "false"}},
		{"config unset", []string{"config", "unset", "core.enableMouse"}},
		{"config edit", []string{"config", "edit"}},
		{"docs", []string{"docs", "--no-open"}},
		{"logout", []string{"logout"}},
		{"completion", []string{"completion", "bash"}},
		{"snapshot group", []string{"snapshot"}},
		{"snapshot list", []string{"snapshot", "list"}},
		{"snapshot rm", []string{"snapshot", "rm", "missing"}},
		{"snapshot prune", []string{"snapshot", "prune"}},
		{"snapshot path", []string{"snapshot", "path"}},
		{"snapshot adopt", []string{"snapshot", "adopt", "--force", missing}},
		{"snapshot clip", []string{"snapshot", "clip", "missing"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupCalls := 0
			root := newRootCmd(func(noConfig bool) (deps.Bundle, error) {
				setupCalls++
				return failingSetup()(noConfig)
			})
			root.SetArgs(tt.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			err := root.Execute()
			assert.NotEqual(t, ExitConfig, ExitCode(err))
			assert.Zero(t, setupCalls)
		})
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_ConfigDependentCommandsFailWithBrokenConfig(t *testing.T) {
	setIsolatedXDG(t)

	for _, args := range [][]string{
		{"config", "show"},
		{"config", "get", "core.enableMouse"},
		{"config", "describe"},
		{"query", "--all"},
		{"snapshot", "inspect", "missing"},
		{"snapshot", "logs", "missing"},
		{"__complete", "query", "--instance", ""},
		{"__complete", "snapshot", "logs", "missing", "--instance", ""},
	} {
		setupCalls := 0
		root := newRootCmd(func(noConfig bool) (deps.Bundle, error) {
			setupCalls++
			return failingSetup()(noConfig)
		})
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)

		err := root.Execute()
		require.Error(t, err, strings.Join(args, " "))
		assert.Equal(t, ExitConfig, ExitCode(err), strings.Join(args, " "))
		assert.Equal(t, 1, setupCalls, strings.Join(args, " "))
	}
}

// TestExecute_ConfigBackedCompletionAttachedInstanceFlagsFailWithBrokenConfig
// ensures attached shorthand values reach the completion preflight before Cobra
// invokes a callback that would otherwise suppress the loader error.
func TestExecute_ConfigBackedCompletionAttachedInstanceFlagsFailWithBrokenConfig(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"query equals", []string{"__complete", "query", "-i=prod"}},
		{"query joined", []string{"__complete", "query", "-iprod"}},
		{"snapshot equals", []string{"__complete", "snapshot", "logs", "missing", "-i=prod"}},
		{"snapshot joined", []string{"__complete", "snapshot", "logs", "missing", "-iprod"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupCalls := 0
			root := newRootCmd(func(noConfig bool) (deps.Bundle, error) {
				setupCalls++
				return failingSetup()(noConfig)
			})
			root.SetArgs(tt.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			err := root.Execute()
			require.Error(t, err)
			assert.Equal(t, ExitConfig, ExitCode(err))
			assert.Equal(t, 1, setupCalls)
		})
	}
}

//nolint:paralleltest // completion and snapshots use process-wide XDG paths
func TestExecute_ConfigBackedCompletionLoadsOnce(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := multiQueryTestConfig()

	run := func(args ...string) (string, int) {
		t.Helper()
		setupCalls := 0
		root := newRootCmd(func(bool) (deps.Bundle, error) {
			setupCalls++
			return deps.New(cfg, state.New()), nil
		})
		var out bytes.Buffer
		root.SetArgs(args)
		root.SetOut(&out)
		root.SetErr(io.Discard)
		require.NoError(t, root.Execute())
		return out.String(), setupCalls
	}

	out, setupCalls := run("__complete", "query", "--instance", "prod")
	assert.Contains(t, out, "prod-a")
	assert.Contains(t, out, "prod-b")
	assert.Equal(t, 1, setupCalls)

	seedSnapshot(t, "m_one.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{{
			CRN: cfg.ICL.Instances[0].CRN.String(),
		}}},
	}, map[string][]icl.Log{cfg.ICL.Instances[0].CRN.String(): {{}}})
	out, setupCalls = run("__complete", "snapshot", "logs", "m_one", "--instance", "prod")
	assert.Contains(t, out, "prod-a")
	assert.Equal(t, 1, setupCalls)
}

func TestExecute_NoConfigPassesBuiltInDefaultRequest(t *testing.T) {
	t.Parallel()
	var gotNoConfig bool
	root := newRootCmd(func(noConfig bool) (deps.Bundle, error) {
		gotNoConfig = noConfig
		return deps.New(config.New(), state.New()), nil
	})
	root.SetArgs([]string{"--no-config", "config", "show"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	require.NoError(t, root.Execute())
	assert.True(t, gotNoConfig)
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_ConfigAndSnapshotGroupsPrintHelpAndRejectTypos(t *testing.T) {
	setIsolatedXDG(t)

	for _, topic := range []string{"config", "snapshot"} {
		t.Run(topic, func(t *testing.T) {
			root := newRootCmd(failingSetup())
			var out bytes.Buffer
			root.SetArgs([]string{topic})
			root.SetOut(&out)
			root.SetErr(io.Discard)
			require.NoError(t, root.Execute())
			assert.Contains(t, out.String(), "Usage:")

			root = newRootCmd(failingSetup())
			root.SetArgs([]string{topic, "typo"})
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			err := renderExit(io.Discard, nil, root.Execute())
			require.Error(t, err)
			assert.Equal(t, ExitUsage, ExitCode(err))
		})
	}
}

//nolint:paralleltest // t.Setenv (XDG) forbids t.Parallel
func TestExecute_BrokenConfigCanBeRepairedAndSnapshotNoConfigIsExplicit(t *testing.T) {
	setIsolatedXDG(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	bad := []byte("version: 1\ncore:\n  enableMouse: definitely-not-a-bool\n")
	require.NoError(t, os.WriteFile(p, bad, 0o600))

	run := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		root := newRootCmd(defaultSetup)
		root.SetArgs(args)
		root.SetOut(&out)
		root.SetErr(io.Discard)
		err := root.Execute()
		return out.String(), err
	}

	_, err = run("config", "set", "core.enableMouse", "false")
	require.NoError(t, err)
	_, err = config.LoadConfig()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(p, bad, 0o600))
	_, err = run("config", "unset", "core.enableMouse")
	require.NoError(t, err)
	_, err = config.LoadConfig()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(p, bad, 0o600))
	seedSnapshot(t, "m_one.lognav", snapshot.Snapshot{
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{Instances: []snapshot.InstanceSnapshot{{CRN: "only", LogCount: 1}}},
	}, map[string][]icl.Log{"only": {{Data: map[string]any{"message": "hello"}}}})

	_, err = run("snapshot", "inspect", "m_one")
	require.Error(t, err)
	assert.Equal(t, ExitConfig, ExitCode(err))
	out, err := run("--no-config", "snapshot", "inspect", "m_one")
	require.NoError(t, err)
	assert.Contains(t, out, "us-south/only")

	_, err = run("snapshot", "logs", "m_one")
	require.Error(t, err)
	assert.Equal(t, ExitConfig, ExitCode(err))
	out, err = run("--no-config", "snapshot", "logs", "m_one")
	require.NoError(t, err)
	assert.Contains(t, out, `"message":"hello"`)
}
