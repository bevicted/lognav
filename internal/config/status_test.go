package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestInspectStatus_StatesPathsAndEffectiveValidation(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	packagePath := usePackageConfig(t, "version: 1\ncore:\n  enableMouse: false\n")
	writeSystemConfig(t, "\n\t")
	userPath := writeUserConfig(t, "version: 1\nicl:\n  instances:\n    - name: missing-environment\n      crn: 'crn:v1:test-cloud:public:logs:eu-de:a/account:one::'\n")
	before, err := os.ReadFile(userPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)

	report := InspectStatus()
	require.Len(t, report.Files, 3)
	assert.Equal(t, []string{"homebrew", "system", "user"}, []string{report.Files[0].Type, report.Files[1].Type, report.Files[2].Type})
	assert.Equal(t, statusValid, report.Files[0].State)
	assert.Equal(t, packagePath, *report.Files[0].Path)
	assert.Equal(t, 1, *report.Files[0].Keys)
	assert.Equal(t, statusValid, report.Files[1].State)
	assert.Equal(t, 0, *report.Files[1].Keys)
	assert.Equal(t, statusValid, report.Files[2].State)
	assert.Equal(t, 1, *report.Files[2].Keys)
	assert.Equal(t, statusError, report.Effective.State)
	assert.Equal(t, []string{"invalid effective configuration"}, report.Effective.Errors)
	assert.True(t, report.HasErrors())
	after, err := os.ReadFile(userPath) // #nosec G304 -- test fixture path
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestInspectStatus_FileErrorsDoNotHideEachOtherOrSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	usePackageConfig(t, "version: 2\nunknownSecret: fake-package-credential\n")
	writeSystemConfig(t, "not: valid: yaml: fake-system-credential\n")
	writeUserConfig(t, "version: 1\ncore:\n  enableMouse: fake-user-credential\n")

	report := InspectStatus()
	require.Len(t, report.Files, 3)
	for _, file := range report.Files {
		assert.Equal(t, statusError, file.State, file.Type)
		for _, diagnostic := range file.Errors {
			assert.NotContains(t, diagnostic, "fake-")
		}
	}
	assert.NotNil(t, report.Files[0].Keys)
	assert.Nil(t, report.Files[1].Keys, "malformed documents have no key count")
	assert.NotNil(t, report.Files[2].Keys)
	assert.Equal(t, statusError, report.Effective.State)
	assert.Equal(t, []string{"one or more file layers could not be inspected"}, report.Effective.Errors)
}

//nolint:paralleltest // changes XDG_CONFIG_HOME and PackageConfigPath.
func TestInspectStatus_MissingAndLeafCounts(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	oldPackagePath := PackageConfigPath
	PackageConfigPath = ""
	t.Cleanup(func() { PackageConfigPath = oldPackagePath })

	report := InspectStatus()
	for _, file := range report.Files {
		assert.Equal(t, statusMissing, file.State, file.Type)
		assert.Equal(t, 0, *file.Keys, file.Type)
		assert.Empty(t, file.Errors)
	}
	assert.Nil(t, report.Files[0].Path)
	assert.Equal(t, filepath.Join(xdgHome, "lognav", systemFileName), *report.Files[1].Path)
	assert.Equal(t, filepath.Join(xdgHome, "lognav", configFileName), *report.Files[2].Path)
	assert.Equal(t, statusValid, report.Effective.State)

	writeUserConfig(t, "version: 1\ncore:\n  enableMouse: false\n  maxLogFiles: 0\nstyle:\n  elapsedFetchTimeFormat: ''\nkeys:\n  accept: []\nicl:\n  instances: []\n  environments: {}\nunknown: ignored\n")
	report = InspectStatus()
	assert.Equal(t, statusError, report.Files[2].State, "unknown keys still do not count")
	assert.Equal(t, 6, *report.Files[2].Keys, "lists and maps each count once")
}

//nolint:paralleltest // changes config path resolver seam.
func TestInspectStatus_PathResolutionFailure(t *testing.T) {
	oldConfigPath := xdgConfigPath
	xdgConfigPath = func() (string, error) { return "", errors.New("fixture failure") }
	t.Cleanup(func() { xdgConfigPath = oldConfigPath })
	oldPackagePath := PackageConfigPath
	PackageConfigPath = ""
	t.Cleanup(func() { PackageConfigPath = oldPackagePath })

	report := InspectStatus()
	require.Len(t, report.Files, 3)
	assert.Equal(t, statusMissing, report.Files[0].State)
	for _, file := range report.Files[1:] {
		assert.Equal(t, statusError, file.State)
		assert.Nil(t, file.Path)
		assert.Nil(t, file.Keys)
		assert.Equal(t, []string{"could not resolve configuration path"}, file.Errors)
	}
}
