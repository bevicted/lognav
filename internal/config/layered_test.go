package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usePackageConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "defaults.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o400))
	old := PackageConfigPath
	PackageConfigPath = p
	t.Cleanup(func() { PackageConfigPath = old })
	return p
}

func writeSystemConfig(t *testing.T, body string) string {
	t.Helper()
	p, err := getSystemConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func writeUserConfig(t *testing.T, body string) string {
	t.Helper()
	p, err := GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestLoadConfig_LayersMappingsAndLeaves(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	packagePath := usePackageConfig(t, `version: 1
keys:
  accept: [package-enter]
style:
  errorColor: red
icl:
  environments:
    test-cloud:
      iamURL: https://iam.example/test
  instances:
    - name: package
      crn: 'crn:v1:test-cloud:public:logs:eu-de:a/account:package::'
core:
  enableMouse: true
  maxLogFiles: 7
  includeDefaultSnippets: true
`)
	systemPath := writeSystemConfig(t, `version: 1
keys:
  accept: [system-enter]
style:
  errorColor: green
icl:
  environments:
    test-cloud:
      apiKey: system-key
  instances:
    - name: system
      crn: 'crn:v1:test-cloud:public:logs:eu-de:a/account:system::'
core:
  maxLogFiles: 3
  redrawIntervalMs: 19
`)
	userPath := writeUserConfig(t, `# sparse user overrides
keys:
  accept: []
style:
  errorColor: blue
  elapsedFetchTimeFormat: ""
icl:
  environments:
    test-cloud:
      apiKey: user-key
  instances: []
core:
  enableMouse: false
  maxLogFiles: 0
  includeDefaultSnippets: false
`)
	beforeUser, err := os.ReadFile(userPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	beforeSystem, err := os.ReadFile(systemPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	beforePackage, err := os.ReadFile(packagePath) // #nosec G304 -- test reads a package path under t.TempDir.
	require.NoError(t, err)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Empty(t, cfg.Keys.Accept, "an explicit user list replaces all lower lists")
	assert.Equal(t, "blue", cfg.Style.ErrorColor.String())
	assert.Empty(t, cfg.Style.ElapsedFetchTimeFormat)
	assert.False(t, cfg.Core.EnableMouse)
	assert.Zero(t, cfg.Core.MaxLogFiles)
	assert.Equal(t, uint16(19), cfg.Core.RedrawIntervalMs, "an omitted user leaf inherits the system layer")
	assert.False(t, cfg.Core.IncludeDefaultSnippets)
	assert.Empty(t, cfg.ICL.Instances, "[] disables all inherited instances")
	assert.Equal(t, "https://iam.example/test", cfg.ICL.Environments["test-cloud"].IAMURL)
	assert.Equal(t, "user-key", cfg.ICL.Environments["test-cloud"].APIKey)

	afterUser, err := os.ReadFile(userPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	afterSystem, err := os.ReadFile(systemPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	afterPackage, err := os.ReadFile(packagePath) // #nosec G304 -- test reads a package path under t.TempDir.
	require.NoError(t, err)
	assert.Equal(t, beforeUser, afterUser)
	assert.Equal(t, beforeSystem, afterSystem)
	assert.Equal(t, beforePackage, afterPackage)
	info, err := os.Stat(userPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestConfigFromLayers_EnvironmentAPIKeyEnvVarDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	packageLayer := configLayer{name: "package defaults", bytes: []byte(`icl:
  environments:
    custom:
      iamURL: https://iam.example/custom
      apiKeyEnvVar: PACKAGE_API_KEY
    defaulted:
      iamURL: https://iam.example/defaulted
`)}
	systemLayer := configLayer{name: systemConfigName, bytes: []byte(`icl:
  environments:
    system:
      iamURL: https://iam.example/system
      apiKeyEnvVar: SYSTEM_API_KEY
`)}
	userLayer := configLayer{name: userConfigName, bytes: []byte(`icl:
  environments:
    custom:
      apiKeyEnvVar: ""
`)}

	cfg, err := configFromLayers(packageLayer, systemLayer, userLayer)
	require.NoError(t, err)
	assert.Empty(t, cfg.ICL.Environments["custom"].APIKeyEnvVar, "explicit empty selector disables lookup")
	assert.Equal(t, "IC_API_KEY", cfg.ICL.Environments["defaulted"].APIKeyEnvVar)
	assert.Equal(t, "SYSTEM_API_KEY", cfg.ICL.Environments["system"].APIKeyEnvVar)
	assert.Equal(t, "IC_API_KEY", cfg.ICL.Environments["bluemix"].APIKeyEnvVar)

	cfg, err = configFromLayers(packageLayer, systemLayer)
	require.NoError(t, err)
	assert.Equal(t, "PACKAGE_API_KEY", cfg.ICL.Environments["custom"].APIKeyEnvVar, "removing an override restores the lower selector")
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestLoadConfig_InstanceMergeUsesOneFinalPolicy(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	usePackageConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: package
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:package::'
`)
	writeSystemConfig(t, `icl:
  instancesMerge: replace
  instances:
    - name: system
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:system::'
`)
	writeUserConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: user
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:user::'
`)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"package", "system", "user"}, instanceNames(cfg.ICL.Instances),
		"the final append policy must reinterpret every supplied list, not append only after the system replace")

	writeUserConfig(t, `icl:
  instancesMerge: replace
  instances:
    - name: user
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:user::'
`)
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"user"}, instanceNames(cfg.ICL.Instances))
}

func instanceNames(instances []ICLInstanceConfig) []string {
	names := make([]string, 0, len(instances))
	for _, instance := range instances {
		names = append(names, instance.Name)
	}
	return names
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestLoadConfig_InstanceMergeModeOnlyEmptyAndLowerChanges(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	usePackageConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: package
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:package::'
`)
	writeSystemConfig(t, `icl:
  instances:
    - name: system
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:system::'
`)
	writeUserConfig(t, "icl:\n  instancesMerge: append\n  instances: []\n")

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"package", "system"}, instanceNames(cfg.ICL.Instances))

	writeUserConfig(t, "icl:\n  instancesMerge: replace\n  instances: []\n")
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg.ICL.Instances)
	assert.Empty(t, cfg.ICL.Instances)

	writeSystemConfig(t, `icl:
  instances:
    - name: updated-system
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:updated-system::'
`)
	writeUserConfig(t, "icl:\n  instancesMerge: append\n")
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"package", "updated-system"}, instanceNames(cfg.ICL.Instances))
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestLoadConfig_InstanceMergeSystemAndUserWithoutPackage(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := PackageConfigPath
	PackageConfigPath = ""
	t.Cleanup(func() { PackageConfigPath = old })
	writeSystemConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: system
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:system::'
`)
	writeUserConfig(t, `icl:
  instances:
    - name: user
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:user::'
`)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"system", "user"}, instanceNames(cfg.ICL.Instances))

	writeSystemConfig(t, "icl:\n  instancesMerge: append\n  instances: []\n")
	writeUserConfig(t, "icl:\n  instances: []\n")
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg.ICL.Instances)
	assert.Empty(t, cfg.ICL.Instances)
}

func TestLoadConfig_AbsentAndEmptyLayersAreOptional(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	old := PackageConfigPath
	PackageConfigPath = filepath.Join(t.TempDir(), "missing-defaults.yaml")
	t.Cleanup(func() { PackageConfigPath = old })

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, CurrentVersion, cfg.Version)
	_, err = os.Stat(filepath.Join(xdgHome, "lognav"))
	require.ErrorIs(t, err, os.ErrNotExist, "normal reads must not create the config directory")

	usePackageConfig(t, " \n\t")
	writeSystemConfig(t, "\n")
	writeUserConfig(t, "  \n")
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, CurrentVersion, cfg.Version)
}

func TestLoadConfig_RejectsInvalidLayerBeforeMerge(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Run("package top-level null", func(t *testing.T) {
		usePackageConfig(t, "null\n")
		_, err := LoadConfig()
		require.ErrorContains(t, err, "package defaults: configuration document must be a map")
	})
	t.Run("system invalid despite valid user", func(t *testing.T) {
		usePackageConfig(t, "version: 1\n")
		writeSystemConfig(t, "version: 2\n")
		writeUserConfig(t, "version: 1\ncore:\n  enableMouse: false\n")
		_, err := LoadConfig()
		require.ErrorContains(t, err, "system config: invalid version 2")
	})
	t.Run("user top-level null", func(t *testing.T) {
		usePackageConfig(t, "version: 1\n")
		writeSystemConfig(t, "version: 1\n")
		writeUserConfig(t, "null\n")
		_, err := LoadConfig()
		require.ErrorContains(t, err, "configuration document must be a map")
	})
	t.Run("package null", func(t *testing.T) {
		usePackageConfig(t, "version: 1\nicl:\n  instances: null\n")
		_, err := LoadConfig()
		require.ErrorContains(t, err, "package defaults: icl.instances must be a list")
	})
	t.Run("malformed system", func(t *testing.T) {
		usePackageConfig(t, "version: 1\n")
		writeSystemConfig(t, "not: valid: yaml: ::: garbage\n")
		_, err := LoadConfig()
		require.Error(t, err)
		require.ErrorContains(t, err, "system config")
	})
	t.Run("system null", func(t *testing.T) {
		usePackageConfig(t, "version: 1\n")
		writeSystemConfig(t, "version: 1\nicl:\n  instances: null\n")
		_, err := LoadConfig()
		require.ErrorContains(t, err, "system config: icl.instances must be a list")
	})
	t.Run("unreadable system", func(t *testing.T) {
		usePackageConfig(t, "version: 1\n")
		p, err := getSystemConfigPath()
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(p))
		require.NoError(t, os.Mkdir(p, 0o700))
		writeUserConfig(t, "version: 1\ncore:\n  enableMouse: false\n")
		_, err = LoadConfig()
		require.ErrorContains(t, err, "read system config")
	})
}

func TestConfigFromLayers_ValidatesEverySuppliedLayer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		layers []configLayer
		want   string
	}{
		{
			name: "unknown package key cannot be hidden",
			layers: []configLayer{
				{name: "package defaults", bytes: []byte(`version: 1
core:
  unknown: true
`)},
				{name: "user config", bytes: []byte(`version: 1
core:
  enableMouse: false
`)},
			},
			want: "unknown field",
		},
		{
			name: "unknown system key cannot be hidden",
			layers: []configLayer{
				{name: systemConfigName, bytes: []byte(`version: 1
core:
  unknown: true
`)},
				{name: "user config", bytes: []byte(`version: 1
core:
  enableMouse: false
`)},
			},
			want: "unknown field",
		},
		{
			name: "unknown user key",
			layers: []configLayer{
				{name: "user config", bytes: []byte(`version: 1
core:
  unknown: true
`)},
			},
			want: "unknown field",
		},
		{
			name: "unsupported explicit user version",
			layers: []configLayer{
				{name: "user config", bytes: []byte(`version: 2
core:
  enableMouse: false
`)},
			},
			want: "invalid version 2",
		},
		{
			name: "null environment record",
			layers: []configLayer{
				{name: "package defaults", bytes: []byte(`version: 1
icl:
  environments:
    test-cloud: null
`)},
			},
			want: `icl.environments."test-cloud" must be a record`,
		},
		{
			name: "invalid package mode cannot be hidden by user",
			layers: []configLayer{
				{name: "package defaults", bytes: []byte("icl:\n  instancesMerge: invalid\n")},
				{name: "user config", bytes: []byte("icl:\n  instancesMerge: append\n")},
			},
			want: `icl.instancesMerge must be "replace" or "append"`,
		},
		{
			name: "explicit nonstring modes are rejected",
			layers: []configLayer{
				{name: "user config", bytes: []byte("icl:\n  instancesMerge: 1\n")},
			},
			want: `icl.instancesMerge must be "replace" or "append"`,
		},
		{
			name: "explicit empty string mode is rejected",
			layers: []configLayer{
				{name: "user config", bytes: []byte("icl:\n  instancesMerge: ''\n")},
			},
			want: `icl.instancesMerge must be "replace" or "append"`,
		},
		{
			name: "explicit null mode is rejected",
			layers: []configLayer{
				{name: "user config", bytes: []byte("icl:\n  instancesMerge: \n")},
			},
			want: `icl.instancesMerge must be "replace" or "append"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := configFromLayers(tt.layers...)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestLoadConfig_InstanceMergeValidatesAdditiveCollisions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	usePackageConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: duplicate
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:package::'
`)
	writeSystemConfig(t, `icl:
  instances:
    - name: duplicate
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:system::'
`)
	writeUserConfig(t, "icl:\n  instancesMerge: append\n")

	cfg, err := LoadConfig()
	require.ErrorContains(t, err, `duplicate effective instance name "duplicate"`)
	assert.Nil(t, cfg)

	writeSystemConfig(t, `icl:
  instances:
    - name: distinct
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:package::'
`)
	cfg, err = LoadConfig()
	require.ErrorContains(t, err, "duplicate effective instance CRN")
	assert.Nil(t, cfg)

	writeUserConfig(t, `icl:
  instancesMerge: replace
  instances:
    - name: user
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:user::'
`)
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"user"}, instanceNames(cfg.ICL.Instances))
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestSetAndUnsetConfig_InstanceMergePreservesUserContribution(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	packagePath := usePackageConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: package
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:package::'
`)
	systemPath := writeSystemConfig(t, `icl:
  instances:
    - name: system
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:system::'
`)
	userPath := writeUserConfig(t, `# retain this comment
icl:
  instancesMerge: append
  environments:
    bluemix:
      apiKey: user-key
  instances:
    - name: user
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:user::'
`)
	packageBefore, err := os.ReadFile(packagePath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	systemBefore, err := os.ReadFile(systemPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)

	require.NoError(t, SetConfig("icl.instances", "[{name: replacement, crn: 'crn:v1:bluemix:public:logs:us-south:a/account:replacement::'}]"))
	user, err := os.ReadFile(userPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Contains(t, string(user), "# retain this comment")
	assert.Contains(t, string(user), "apiKey: user-key")
	assert.Contains(t, string(user), "name: replacement")
	assert.NotContains(t, string(user), "name: package")
	assert.NotContains(t, string(user), "name: system")
	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"package", "system", "replacement"}, instanceNames(cfg.ICL.Instances))

	require.NoError(t, UnsetConfig("icl.instances"))
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"package", "system"}, instanceNames(cfg.ICL.Instances))
	packageAfter, err := os.ReadFile(packagePath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	systemAfter, err := os.ReadFile(systemPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, packageBefore, packageAfter)
	assert.Equal(t, systemBefore, systemAfter)
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestSetAndUnsetConfig_InstanceMergeRejectsInvalidUnsetAndCanRepair(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	usePackageConfig(t, `icl:
  instancesMerge: append
  instances:
    - name: duplicate
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:package::'
`)
	writeSystemConfig(t, `icl:
  instances:
    - name: duplicate
      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:system::'
`)
	userPath := writeUserConfig(t, "# retain this comment\nicl:\n  instancesMerge: append\n")

	cfg, err := LoadConfig()
	require.ErrorContains(t, err, `duplicate effective instance name "duplicate"`)
	assert.Nil(t, cfg)
	require.NoError(t, SetConfig("icl.instancesMerge", "replace"))
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, []string{"duplicate"}, instanceNames(cfg.ICL.Instances))

	before, err := os.ReadFile(userPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	err = UnsetConfig("icl.instancesMerge")
	require.ErrorContains(t, err, `duplicate effective instance name "duplicate"`)
	after, err := os.ReadFile(userPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestSetAndUnsetConfig_UsePackageAndSystemBase(t *testing.T) {
	// PackageConfigPath and XDG_CONFIG_HOME are process-global.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	packagePath := usePackageConfig(t, `version: 1
icl:
  instances:
    - name: package
      crn: 'crn:v1:test-cloud:public:logs:eu-de:a/account:package::'
core:
  maxLogFiles: 7
`)
	systemPath := writeSystemConfig(t, `version: 1
icl:
  environments:
    test-cloud:
      iamURL: https://iam.example/test
  instances:
    - name: inherited
      crn: 'crn:v1:test-cloud:public:logs:eu-de:a/account:inherited::'
core:
  maxLogFiles: 8
`)
	userPath := writeUserConfig(t, `# retain this comment
icl:
  environments:
    test-cloud:
      apiKey: user-key
core:
  enableMouse: not-a-bool
`)
	packageBefore, err := os.ReadFile(packagePath) // #nosec G304 -- test reads a package path under t.TempDir.
	require.NoError(t, err)
	systemBefore, err := os.ReadFile(systemPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)

	require.NoError(t, SetConfig("core.enableMouse", "false"))
	user, err := os.ReadFile(userPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	assert.Contains(t, string(user), "# retain this comment")
	assert.Contains(t, string(user), "apiKey: user-key")
	assert.Contains(t, string(user), "enableMouse: false")

	require.NoError(t, SetConfig("core.maxLogFiles", "0"))
	require.NoError(t, UnsetConfig("core.maxLogFiles"))
	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, uint8(8), cfg.Core.MaxLogFiles)

	require.NoError(t, SetConfig("icl.instances", "[]"))
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Empty(t, cfg.ICL.Instances)
	require.NoError(t, UnsetConfig("icl.instances"))
	cfg, err = LoadConfig()
	require.NoError(t, err)
	require.Len(t, cfg.ICL.Instances, 1)
	assert.Equal(t, "inherited", cfg.ICL.Instances[0].Name)

	beforeRejected, err := os.ReadFile(userPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	require.Error(t, SetConfig("style.errorColor", "not-a-color"))
	afterRejected, err := os.ReadFile(userPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	assert.Equal(t, beforeRejected, afterRejected)

	packageAfter, err := os.ReadFile(packagePath) // #nosec G304 -- test reads a package path under t.TempDir.
	require.NoError(t, err)
	systemAfter, err := os.ReadFile(systemPath) // #nosec G304 -- test reads an XDG path under t.TempDir.
	require.NoError(t, err)
	assert.Equal(t, packageBefore, packageAfter)
	assert.Equal(t, systemBefore, systemAfter)
}
