package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
)

// LoadConfig tests that change the process-global XDG_CONFIG_HOME env var must
// run sequentially.

func TestLoadConfig_MissingFile_ReturnsDefaults(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := config.LoadConfig()
	require.NoError(t, err, "missing config file should not be an error")
	require.NotNil(t, cfg)
	assert.Equal(t, config.CurrentVersion, cfg.Version)
}

func TestLoadConfig_Version1MinimalFileLoads(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte("version: 1\n"), 0o600))

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 1, config.CurrentVersion)
	assert.Equal(t, config.CurrentVersion, cfg.Version)
}

func TestLoadConfig_InvalidYAML_WrapsError(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lognav"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lognav", "config.yaml"), []byte("not: valid: yaml: ::: garbage"), 0o600))

	_, err := config.LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config:")
}

// A genuinely-unknown key (a typo) fails the strict load via
// DisallowUnknownField rather than silently degrading.
//
//nolint:paralleltest // t.Setenv modifies process-global env
func TestLoadConfig_FirstFetchKeyLoads(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte("version: 1\nkeys:\n  firstFetch: [shift+x]\n"), 0o600))

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, config.KeyBind{"shift+x"}, cfg.Keys.FirstFetch)
}

func TestLoadConfig_RejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	body := "version: 1\nlogs:\n  totallyNotAField: 7\n"

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	_, err = config.LoadConfig()
	require.Error(t, err, "an unknown key must fail")
	assert.ErrorContains(t, err, "unknown field")
}

func TestLoadConfig_RejectsRetiredStatusRowKeys(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	for _, key := range []string{"status" + "lineBg", "status" + "lineFg", "status" + "lineTimestampFormat"} {
		t.Run(key, func(t *testing.T) {
			require.NoError(t, os.WriteFile(p, []byte("version: 1\nstyle:\n  "+key+": removed\n"), 0o600))

			_, err := config.LoadConfig()
			require.Error(t, err)
			require.ErrorContains(t, err, "unknown field")
			assert.ErrorContains(t, err, key)
		})
	}
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestLoadConfig_AcceptsEmptyInstancesAndRejectsNull(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte("version: 1\nicl:\n  instances: []\n"), 0o600))

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg.ICL.Instances)
	assert.Empty(t, cfg.ICL.Instances)

	require.NoError(t, os.WriteFile(p, []byte("version: 1\nicl:\n  instances: null\n"), 0o600))
	_, err = config.LoadConfig()
	require.ErrorContains(t, err, "icl.instances must be a list")
}

func TestLoadConfig_MissingHeaderLoadsWithoutRewrite(t *testing.T) {
	// Cannot parallelize: shares XDG_CONFIG_HOME env var.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lognav"), 0o700))
	cfgPath := filepath.Join(dir, "lognav", "config.yaml")
	original := []byte("icl:\n  instances: []\n")
	require.NoError(t, os.WriteFile(cfgPath, original, 0o640)) //nolint:gosec // load must not alter an existing file.

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, config.CurrentVersion, cfg.Version)
	written, err := os.ReadFile(cfgPath) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	assert.Equal(t, original, written)
	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestLoadConfig_RejectsVersion2(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte("version: 2\n"), 0o600))

	_, err = config.LoadConfig()
	require.ErrorContains(t, err, "load config: invalid version 2")
}

// A user opts into experimental features by setting core.enableExperimental:
// true; LoadConfig must overlay it onto the (disabled-by-default) base config.
//
//nolint:paralleltest // t.Setenv modifies process-global env
func TestLoadConfig_CoreEnableExperimental_OptIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	body := "version: 1\ncore:\n  enableExperimental: true\n"
	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	assert.True(t, cfg.Core.EnableExperimental, "core.enableExperimental: true must be honored")
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestLoadConfig_RejectsMissingEffectiveInstanceCRN(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	body := "version: 1\nicl:\n  instances:\n    - name: missing\n"
	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	_, err = config.LoadConfig()
	require.ErrorContains(t, err, `load config: validate: effective instance "missing" is missing a CRN`)
}

//nolint:paralleltest // t.Setenv modifies process-global env
func TestLoadConfig_RejectsDuplicateEffectiveInstanceNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	body := "version: 1\nicl:\n  environments:\n    bluemix:\n      iamURL: https://iam.cloud.ibm.com/identity\n    test-cloud:\n      iamURL: https://iam.example/test\n  instances:\n    - name: duplicate\n      crn: 'crn:v1:bluemix:public:logs:us-south:a/account:first::'\n    - name: duplicate\n      crn: 'crn:v1:test-cloud:public:logs:eu-de:a/account:second::'\n"
	p, err := config.GetConfigPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	_, err = config.LoadConfig()
	require.ErrorContains(t, err, `load config: validate: duplicate effective instance name "duplicate"`)
}
