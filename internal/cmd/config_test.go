package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/state"
)

const (
	configCmd         = "config"
	enableMousePath   = "core.enableMouse"
	enableMouseDollar = "$.core.enableMouse"
)

func configReadCommand(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd(func(bool) (deps.Bundle, error) {
		return deps.New(cfg, state.New()), nil
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{configCmd}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestConfigShowAndGet_EffectiveRedactedView(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	// #nosec G101 -- synthetic credentials exercise configuration redaction.
	cfg.ICL.Environments = map[string]config.ICLEnvironmentConfig{
		"bluemix":    {IAMURL: "https://iam.example/identity", APIKey: "prod-secret", APIKeyOpRef: "op://vault/prod"},
		"test-cloud": {IAMURL: "https://iam.example/test", APIKey: "test-secret"},
	}
	cfg.ICL.Instances = []config.ICLInstanceConfig{{
		Name: "one", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:one::"),
	}, {
		Name: "two", CRN: config.MustCRNFromString("crn:v1:test-cloud:public:logs:eu-de:a/account:two::"),
	}}

	text, err := configReadCommand(t, cfg, "show")
	require.NoError(t, err)
	assert.Contains(t, text, "instances:")
	assert.Contains(t, text, "name: one")
	assert.Contains(t, text, "name: two")
	assert.Contains(t, text, "apiKey: redacted")
	assert.Contains(t, text, "op://vault/prod")
	assert.NotContains(t, text, "prod-secret")
	assert.NotContains(t, text, "test-secret")

	output, err := configReadCommand(t, cfg, "show", "-o", "json")
	require.NoError(t, err)
	var view map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &view))
	icl, ok := view["icl"].(map[string]any)
	require.True(t, ok)
	environments, ok := icl["environments"].(map[string]any)
	require.True(t, ok)
	bluemix, ok := environments["bluemix"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "redacted", bluemix["apiKey"])
	instances, ok := icl["instances"].([]any)
	require.True(t, ok)
	require.Len(t, instances, 2)
	assert.Equal(t, map[string]any{
		"name": "one", "crn": "crn:v1:bluemix:public:logs:us-south:a/account:one::",
	}, instances[0])

	value, err := configReadCommand(t, cfg, "get", enableMousePath)
	require.NoError(t, err)
	assert.Equal(t, "true\n", value)

	value, err = configReadCommand(t, cfg, "get", "icl.environments")
	require.NoError(t, err)
	assert.Contains(t, value, "apiKey: redacted")
	value, err = configReadCommand(t, cfg, "get", "icl.environments", "-o", "json")
	require.NoError(t, err)
	assert.Contains(t, value, `"apiKey": "redacted"`)
	value, err = configReadCommand(t, cfg, "describe", "icl.environments")
	require.NoError(t, err)
	assert.Contains(t, value, "IAM environments")
	assert.NotContains(t, value, "prod-secret")

	output, err = configReadCommand(t, cfg, "get", "icl.instances", "-o", "json")
	require.NoError(t, err)
	var gotInstances []map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &gotInstances))
	assert.Len(t, gotInstances, 2)
}

func TestConfigGet_InstancesHonorEffectiveSetAndStayArrays(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	cfg.ICL.Instances = []config.ICLInstanceConfig{{
		Name: "extra", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:eu-de:a/account:extra::"),
	}}

	output, err := configReadCommand(t, cfg, "get", "icl.instances", "-o", "json")
	require.NoError(t, err)
	assert.JSONEq(t, `[{"name":"extra","crn":"crn:v1:bluemix:public:logs:eu-de:a/account:extra::"}]`, output)

	cfg.ICL.Instances = []config.ICLInstanceConfig{}
	output, err = configReadCommand(t, cfg, "get", "icl.instances", "-o", "json")
	require.NoError(t, err)
	assert.Equal(t, "[]\n", output)
}

func TestConfigGet_RequiresOneSimpleDottedKey(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	for _, args := range [][]string{
		{"get"},
		{"get", "$.core.enableMouse"},
		{"get", ".core.enableMouse"},
		{"get", "core..enableMouse"},
		{"get", "core.enableMouse", "extra"},
	} {
		_, err := configReadCommand(t, cfg, args...)
		require.Error(t, err, strings.Join(args, " "))
		assert.Equal(t, ExitUsage, ExitCode(err))
	}

	_, err := configReadCommand(t, cfg, "get", "core.notAKey")
	require.Error(t, err)
	assert.Equal(t, ExitNoInput, ExitCode(err))
	assert.EqualError(t, err, `unknown configuration key "core.notAKey"`)
}

func TestConfigKeyCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		prefix   string
		want     []string
		dontWant string
	}{
		{name: "get", command: "get", prefix: "icl.in", want: []string{"icl.instances\t"}},
		{name: "describe", command: "describe", prefix: "core", want: []string{"core\t", "core.enableMouse\t"}},
		{name: "set", command: "set", prefix: "icl.in", want: []string{"icl.instances\t"}},
		{name: "unset", command: "unset", prefix: "icl.in", want: []string{"icl.instances\t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := newRootCmd(failingSetup())
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"__complete", configCmd, tt.command, tt.prefix})

			require.NoError(t, root.Execute())
			for _, want := range tt.want {
				assert.Contains(t, output.String(), want)
			}
			if tt.dontWant != "" {
				assert.NotContains(t, output.String(), tt.dontWant)
			}
			assert.Contains(t, output.String(), ":4")
		})
	}

	got, directive := completeWritableConfigKeys(nil, []string{enableMousePath}, "")
	assert.Empty(t, got, "config set values do not have finite completion candidates")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

func TestConfigDescribeHelp_UsesDottedKeys(t *testing.T) {
	t.Parallel()
	output, err := configReadCommand(t, config.New(), "describe", "--help")
	require.NoError(t, err)
	assert.Contains(t, output, "describe [dotted-key]")
	assert.Contains(t, output, "Output includes types, defaults, and current values")
	assert.Contains(t, output, "lognav config describe core")
	assert.NotContains(t, output, "yamlpath")
	assert.NotContains(t, output, "$.core")
}

func TestConfigDescribeAndEdits_InstancesAreWritable(t *testing.T) {
	t.Parallel()
	cfg := config.New()
	cfg.ICL.Instances = []config.ICLInstanceConfig{{
		Name: "test",
		CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:test::"),
	}}
	output, err := configReadCommand(t, cfg, "describe", "icl.instances")
	require.NoError(t, err)
	assert.NotContains(t, output, "computed")
	assert.Contains(t, output, "[]ICLInstanceConfig")

	output, err = configReadCommand(t, cfg, "describe", "icl.instances", "-o", "json")
	require.NoError(t, err)
	var fields []config.FieldMeta
	require.NoError(t, json.Unmarshal([]byte(output), &fields))
	require.Len(t, fields, 1)
	assert.Equal(t, "$.icl.instances", fields[0].YAMLPath)
	assert.False(t, fields[0].Computed)
	assert.False(t, fields[0].ReadOnly)
	assert.Equal(t, []any{map[string]any{
		"name": "test", "crn": "crn:v1:bluemix:public:logs:us-south:a/account:test::",
	}}, fields[0].Current)

	out, err := config.SetValue([]byte("version: 1\n# keep\ncore:\n  enableMouse: true\n"), "icl.instances", "[{name: test, crn: 'crn:v1:bluemix:public:logs:us-south:a/account:test::'}]")
	require.NoError(t, err)
	assert.Contains(t, string(out), "# keep")
	assert.Contains(t, string(out), "name: test")
	out, err = config.UnsetValue(out, "icl.instances")
	require.NoError(t, err)
	assert.NotContains(t, string(out), "instances")
}

// TestConfigEditEditorHelper records the argv received from config edit's
// child process. It only runs in the subprocess started by the parent test.
func TestConfigEditEditorHelper(t *testing.T) { //nolint:paralleltest // subprocess helper reads process-global env
	if os.Getenv("GO_WANT_CONFIG_EDIT_HELPER") != "1" {
		return
	}
	require.NoError(t, os.WriteFile(os.Getenv("CONFIG_EDIT_ARGS_FILE"), []byte(strings.Join(os.Args[1:], "\n")), 0o600)) // #nosec G703 -- test parent supplies an isolated path
	os.Exit(0)
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigEdit_UsesSharedEditorCommand(t *testing.T) {
	setIsolatedXDG(t)
	argsFile := filepath.Join(t.TempDir(), "editor-args")
	t.Setenv("GO_WANT_CONFIG_EDIT_HELPER", "1")
	t.Setenv("CONFIG_EDIT_ARGS_FILE", argsFile)
	t.Setenv("EDITOR", os.Args[0]+" -test.run=^TestConfigEditEditorHelper$")

	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "edit"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	require.NoError(t, root.Execute())

	got, err := os.ReadFile(argsFile) // #nosec G304 -- test temp path
	require.NoError(t, err)
	path, err := config.GetConfigPath()
	require.NoError(t, err)
	assert.Equal(t, []string{"-test.run=^TestConfigEditEditorHelper$", path}, strings.Split(strings.TrimSpace(string(got)), "\n"))
}

func TestConfigCommandCompletion_ExcludesInstance(t *testing.T) {
	t.Parallel()
	root := newRootCmd(noopSetup(t))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"__complete", ""})
	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "config\tManage configuration")
	assert.NotContains(t, out.String(), "instance\t")
}

//nolint:paralleltest // modifies XDG_CONFIG_HOME.
func TestConfigStatus(t *testing.T) {
	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		var stdout bytes.Buffer
		root := newRootCmd(failingSetup())
		root.SetArgs(append([]string{configCmd, "status"}, args...))
		root.SetOut(&stdout)
		root.SetErr(&bytes.Buffer{})
		err := root.Execute()
		return stdout.String(), err
	}

	t.Run("all missing is read-only and bypasses runtime loading", func(t *testing.T) {
		xdgHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdgHome)

		got, err := run(t)
		require.NoError(t, err)
		assert.Contains(t, got, "type       state    keys  path")
		assert.Contains(t, got, "homebrew   missing  0     -")
		assert.Contains(t, got, "system     missing  0     "+filepath.Join(xdgHome, "lognav", "system.yaml"))
		assert.Contains(t, got, "user       missing  0     "+filepath.Join(xdgHome, "lognav", "user.yaml"))
		assert.Contains(t, got, "effective  valid    -     -")
		_, err = os.Stat(filepath.Join(xdgHome, "lognav"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("invalid file returns complete safe JSON report", func(t *testing.T) {
		xdgHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdgHome)
		userPath := filepath.Join(xdgHome, "lognav", "user.yaml")
		invalid := []byte("version: 1\ncore:\n  enableMouse: fake-credential\n")
		require.NoError(t, os.MkdirAll(filepath.Dir(userPath), 0o700))
		require.NoError(t, os.WriteFile(userPath, invalid, 0o600))

		got, err := run(t, "-o", "json")
		require.Error(t, err)
		assert.Equal(t, ExitConfig, ExitCode(err))
		assert.NotContains(t, got, "fake-credential")
		var report map[string]any
		require.NoError(t, json.Unmarshal([]byte(got), &report))
		files, ok := report["files"].([]any)
		require.True(t, ok)
		require.Len(t, files, 3)
		userFile, ok := files[2].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "user", userFile["type"])
		assert.Equal(t, "error", userFile["state"])
		assert.NotContains(t, userFile, "exists")
		assert.NotContains(t, userFile, "valid")
		assert.IsType(t, []any{}, userFile["errors"])
		effective, ok := report["effective"].(map[string]any)
		require.True(t, ok)
		assert.IsType(t, []any{}, effective["errors"])
		text, textErr := run(t)
		require.Error(t, textErr)
		assert.Equal(t, ExitConfig, ExitCode(textErr))
		assert.Less(t, strings.Index(text, "effective  error"), strings.Index(text, "user: invalid configuration"))
		assert.NotContains(t, text, "fake-credential")

		actual, err := os.ReadFile(userPath) // #nosec G304 -- test fixture path
		require.NoError(t, err)
		assert.Equal(t, invalid, actual)
	})

	t.Run("invalid output and output writer follow normal errors", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		got, err := run(t, "--output", "bad")
		require.Error(t, err)
		assert.Equal(t, ExitUsage, ExitCode(err))
		assert.Empty(t, got)

		root := newRootCmd(failingSetup())
		root.SetArgs([]string{configCmd, "status"})
		root.SetOut(failingWriter{})
		root.SetErr(&bytes.Buffer{})
		require.Error(t, root.Execute())
	})

	root := newRootCmd(noopSetup(t))
	var help bytes.Buffer
	root.SetArgs([]string{configCmd, "status", "--help"})
	root.SetOut(&help)
	root.SetErr(&bytes.Buffer{})
	require.NoError(t, root.Execute())
	assert.Contains(t, help.String(), "Inspect Homebrew, system, and user configuration files")

	root = newRootCmd(noopSetup(t))
	var completion bytes.Buffer
	root.SetArgs([]string{"__complete", configCmd, ""})
	root.SetOut(&completion)
	root.SetErr(&bytes.Buffer{})
	require.NoError(t, root.Execute())
	assert.Contains(t, completion.String(), "status\tDiagnose configuration file layers")
	assert.NotContains(t, completion.String(), "path\tPrint the configuration file path")
}

func TestNormalizeYAMLPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no_args", nil, "$"},
		{"empty", []string{""}, "$"},
		{"dot", []string{"."}, "$"},
		{"dollar", []string{"$"}, "$"},
		{"dollar_dot", []string{"$."}, "$"},
		{"leading_dot", []string{".keys.accept"}, "$.keys.accept"},
		{"already_dollar", []string{enableMouseDollar}, enableMouseDollar},
		{"bare_dotted", []string{enableMousePath}, enableMouseDollar},
		{"bare_single", []string{"version"}, "$.version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalizeYAMLPath(tt.args))
		})
	}
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigSet_WritesFile(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "set", enableMousePath, "false"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "set core.enableMouse")

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	b, err := os.ReadFile(p) //nolint:gosec // test temp path
	require.NoError(t, err)
	assert.Contains(t, string(b), "enableMouse: false")
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigSet_EnvironmentsMapWritesFile(t *testing.T) {
	setIsolatedXDG(t)

	var stdout, stderr bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "set", "icl.environments", "{bluemix: {iamURL: https://iam.example/identity}, test-cloud: {iamURL: https://iam.example/test}}"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "set icl.environments")

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	b, err := os.ReadFile(p) //nolint:gosec // test temp path
	require.NoError(t, err)
	assert.Contains(t, string(b), "environments:")
	assert.Contains(t, string(b), "test-cloud:")
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigSet_InvalidValue_Errors(t *testing.T) {
	setIsolatedXDG(t)

	var stdout bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "set", "style.errorColor", "notacolor"})
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})

	require.Error(t, root.Execute())
	assert.Empty(t, stdout.String())
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigSet_ExtraInstanceMissingCRN_ErrorsWithoutWriting(t *testing.T) {
	setIsolatedXDG(t)

	var stdout bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "set", "icl.instances", "[{name: missing}]"})
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.ErrorContains(t, err, `effective instance "missing" is missing a CRN`)
	assert.Empty(t, stdout.String())

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	_, err = os.Stat(p)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigUnset_RemovesField(t *testing.T) {
	setIsolatedXDG(t)

	require.NoError(t, config.SetConfig(enableMousePath, "false"))

	var stdout bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "unset", enableMousePath})
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})

	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "unset core.enableMouse")

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	b, err := os.ReadFile(p) //nolint:gosec // test temp path
	require.NoError(t, err)
	assert.NotContains(t, string(b), "enableMouse")
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestConfigUnset_AbsentField_NoopSucceeds(t *testing.T) {
	setIsolatedXDG(t)

	var stdout bytes.Buffer
	root := newRootCmd(noopSetup(t))
	root.SetArgs([]string{configCmd, "unset", enableMousePath})
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})

	require.NoError(t, root.Execute())
	assert.Contains(t, stdout.String(), "unset core.enableMouse")
}
