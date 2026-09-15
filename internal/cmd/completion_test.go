package cmd

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps"
)

func TestDetectCompletionShell(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		shell string
		want  completionShell
		valid bool
	}{
		{name: "bash", shell: "/bin/bash", want: completionBash, valid: true},
		{name: "zsh", shell: "/bin/zsh", want: completionZsh, valid: true},
		{name: "fish", shell: "/opt/homebrew/bin/fish", want: completionFish, valid: true},
		{name: "empty"},
		{name: "unsupported", shell: "/bin/sh"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := detectCompletionShell(func(string) string { return tt.shell })
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "bash, zsh, fish")
		})
	}
}

func TestResolveCompletionProfile(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		shell   completionShell
		profile string
		env     map[string]string
		home    string
		homeErr error
		want    string
		wantErr string
	}{
		{name: "profile override", shell: completionPowerShell, profile: "/tmp/a profile", want: "/tmp/a profile"},
		{name: "bash home", shell: completionBash, env: map[string]string{"HOME": "/home/me"}, want: "/home/me/.bashrc"},
		{name: "zsh zdotdir skips failing home resolver", shell: completionZsh, env: map[string]string{"ZDOTDIR": "/state/zsh"}, homeErr: errors.New("home resolver must not run"), want: "/state/zsh/.zshrc"},
		{name: "zsh home", shell: completionZsh, env: map[string]string{"HOME": "/home/me"}, want: "/home/me/.zshrc"},
		{name: "fish xdg skips failing home resolver", shell: completionFish, env: map[string]string{"XDG_CONFIG_HOME": "/state/config"}, homeErr: errors.New("home resolver must not run"), want: "/state/config/fish/config.fish"},
		{name: "fish home", shell: completionFish, env: map[string]string{"HOME": "/home/me"}, want: "/home/me/.config/fish/config.fish"},
		{name: "home fallback", shell: completionBash, home: "/resolved/home", want: "/resolved/home/.bashrc"},
		{name: "powershell requires profile", shell: completionPowerShell, wantErr: "requires --profile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			system := completionSystem{
				getenv:  func(key string) string { return tt.env[key] },
				homeDir: func() (string, error) { return tt.home, tt.homeErr },
			}
			got, err := resolveCompletionProfile(tt.shell, tt.profile, system)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, ExitUsage, ExitCode(err))
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := resolveCompletionProfile(completionBash, "", completionSystem{
		getenv:  func(string) string { return "" },
		homeDir: func() (string, error) { return "", errors.New("unavailable") },
	})
	require.ErrorContains(t, err, "resolve home directory")
}

func TestCompletionBlocks(t *testing.T) {
	t.Parallel()

	for _, shell := range []completionShell{completionBash, completionZsh, completionFish, completionPowerShell} {
		t.Run(string(shell), func(t *testing.T) {
			t.Parallel()
			block := completionBlock(shell)
			assert.Equal(t, 1, strings.Count(block, completionStartMarker))
			assert.Equal(t, 1, strings.Count(block, completionEndMarker))
			assert.True(t, strings.HasSuffix(block, "\n"))
			assert.Contains(t, block, completionCommand(shell))
			assert.Contains(t, block, "lognav completion")
			assert.NotContains(t, block, os.Args[0])
		})
	}

	zsh := completionBlock(completionZsh)
	assert.Contains(t, zsh, "if ! (( $+functions[compdef] )); then")
	assert.NotContains(t, zsh, "\ncompinit\n")
}

func TestInstallCompletionProfile(t *testing.T) {
	t.Parallel()

	t.Run("creates profile and parent with private modes", func(t *testing.T) {
		t.Parallel()
		profile := filepath.Join(t.TempDir(), "nested", ".bashrc")
		installed, err := installCompletionProfile(defaultProfileFilesystem, profile, completionBlock(completionBash), completionCommand(completionBash))
		require.NoError(t, err)
		assert.True(t, installed)
		info, err := os.Stat(profile)
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
		parent, err := os.Stat(filepath.Dir(profile))
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o700), parent.Mode().Perm())
		contents, err := os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Equal(t, completionBlock(completionBash), string(contents))
	})

	t.Run("preserves content modes and is idempotent", func(t *testing.T) {
		t.Parallel()
		profile := filepath.Join(t.TempDir(), ".zshrc")
		original := []byte("export PATH=/usr/local/bin:$PATH")
		require.NoError(t, os.WriteFile(profile, original, 0o640)) // #nosec G306 -- preserves an existing group-readable profile mode
		require.NoError(t, os.Chmod(profile, 0o640))               // #nosec G302 -- preserves an existing group-readable profile mode
		block := completionBlock(completionZsh)
		installed, err := installCompletionProfile(defaultProfileFilesystem, profile, block, completionCommand(completionZsh))
		require.NoError(t, err)
		assert.True(t, installed)
		want := string(original) + "\n" + block
		contents, err := os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Equal(t, want, string(contents))
		info, err := os.Stat(profile)
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o640), info.Mode().Perm())

		installed, err = installCompletionProfile(defaultProfileFilesystem, profile, block, completionCommand(completionZsh))
		require.NoError(t, err)
		assert.False(t, installed)
		contents, err = os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Equal(t, want, string(contents))
	})

	t.Run("unmarked integration and partial markers do not mutate", func(t *testing.T) {
		t.Parallel()
		profile := filepath.Join(t.TempDir(), ".config", "fish", "config.fish")
		require.NoError(t, os.MkdirAll(filepath.Dir(profile), 0o700))
		unmarked := "set -gx EDITOR vim\n" + completionCommand(completionFish) + "\n"
		require.NoError(t, os.WriteFile(profile, []byte(unmarked), 0o600))
		installed, err := installCompletionProfile(defaultProfileFilesystem, profile, completionBlock(completionFish), completionCommand(completionFish))
		require.NoError(t, err)
		assert.False(t, installed)
		contents, err := os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Equal(t, unmarked, string(contents))

		comment := "# " + completionCommand(completionFish) + " is installed by the package manager\n"
		require.NoError(t, os.WriteFile(profile, []byte(comment), 0o600))
		installed, err = installCompletionProfile(defaultProfileFilesystem, profile, completionBlock(completionFish), completionCommand(completionFish))
		require.NoError(t, err)
		assert.True(t, installed)
		contents, err = os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Equal(t, comment+completionBlock(completionFish), string(contents))

		partial := completionStartMarker + "\n"
		require.NoError(t, os.WriteFile(profile, []byte(partial), 0o600))
		installed, err = installCompletionProfile(defaultProfileFilesystem, profile, completionBlock(completionFish), completionCommand(completionFish))
		require.Error(t, err)
		assert.False(t, installed)
		contents, err = os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Equal(t, partial, string(contents))
	})

	t.Run("follows symlink without replacing it", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		profile := filepath.Join(dir, ".bashrc")
		require.NoError(t, os.WriteFile(target, []byte("# existing\n"), 0o640)) // #nosec G306 -- verifies preservation of an existing profile mode
		require.NoError(t, os.Symlink(target, profile))
		installed, err := installCompletionProfile(defaultProfileFilesystem, profile, completionBlock(completionBash), completionCommand(completionBash))
		require.NoError(t, err)
		assert.True(t, installed)
		info, err := os.Lstat(profile)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&os.ModeSymlink)
		contents, err := os.ReadFile(target) // #nosec G304 -- test-only symlink target via t.TempDir
		require.NoError(t, err)
		assert.Contains(t, string(contents), completionBlock(completionBash))
	})
}

type failingProfileFile struct {
	writeErr error
	syncErr  error
	closeErr error
	closed   bool
}

func (f *failingProfileFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *failingProfileFile) Sync() error {
	return f.syncErr
}

func (f *failingProfileFile) Close() error {
	f.closed = true
	return f.closeErr
}

func TestInstallCompletionProfileReturnsWriteSyncAndCloseFailures(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		file failingProfileFile
		want error
	}{
		{name: "write", file: failingProfileFile{writeErr: errors.New("write")}, want: errors.New("write")},
		{name: "sync", file: failingProfileFile{syncErr: errors.New("sync")}, want: errors.New("sync")},
		{name: "close", file: failingProfileFile{closeErr: errors.New("close")}, want: errors.New("close")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			file := tt.file
			filesystem := profileFilesystem{
				readFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
				mkdirAll: func(string, fs.FileMode) error { return nil },
				openFile: func(string, int, fs.FileMode) (profileFile, error) { return &file, nil },
				chmod:    func(string, fs.FileMode) error { return nil },
			}
			installed, err := installCompletionProfile(filesystem, "/profile", completionBlock(completionBash), completionCommand(completionBash))
			require.Error(t, err)
			assert.False(t, installed)
			assert.True(t, file.closed)
			assert.ErrorContains(t, err, tt.want.Error())
		})
	}
}

func TestCompletionCommandGenerationAndInstallation(t *testing.T) {
	for _, shell := range []completionShell{completionBash, completionZsh, completionFish, completionPowerShell} {
		t.Run("generates "+string(shell), func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			root := newRootCmd(func(bool) (deps.Bundle, error) {
				calls++
				return deps.Bundle{}, errors.New("config must not load")
			})
			root.SetArgs([]string{"completion", string(shell), "--no-descriptions"})
			root.SetOut(&output)
			root.SetErr(io.Discard)
			require.NoError(t, root.Execute())
			assert.NotEmpty(t, output.String())
			assert.Zero(t, calls)
		})
	}

	t.Run("explicit install ignores shell detection", func(t *testing.T) {
		profile := filepath.Join(t.TempDir(), "profile with spaces")
		var output bytes.Buffer
		root := newRootCmd(failingSetup())
		root.SetArgs([]string{"completion", "powershell", "--install", "--profile", profile})
		root.SetOut(&output)
		root.SetErr(io.Discard)
		require.NoError(t, root.Execute())
		assert.Contains(t, output.String(), "powershell")
		assert.Contains(t, output.String(), profile)
		contents, err := os.ReadFile(profile) // #nosec G304 -- test-only path via t.TempDir
		require.NoError(t, err)
		assert.Contains(t, string(contents), completionCommand(completionPowerShell))
	})
}

func TestCompletionCommandDetectedInstallAndUsageErrors(t *testing.T) { //nolint:paralleltest // t.Setenv changes process-global environment
	profileDir := t.TempDir()
	t.Setenv("HOME", profileDir)
	t.Setenv("SHELL", "/bin/bash")

	var output bytes.Buffer
	root := newRootCmd(failingSetup())
	root.SetArgs([]string{"completion", "--install"})
	root.SetOut(&output)
	root.SetErr(io.Discard)
	require.NoError(t, root.Execute())
	assert.Contains(t, output.String(), "bash")
	contents, err := os.ReadFile(filepath.Join(profileDir, ".bashrc")) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	assert.Contains(t, string(contents), completionCommand(completionBash))

	t.Setenv("SHELL", "/bin/sh")
	root = newRootCmd(failingSetup())
	root.SetArgs([]string{"completion", "--install"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err = root.Execute()
	require.Error(t, err)
	assert.Equal(t, ExitUsage, ExitCode(err))
	assert.ErrorContains(t, err, "bash, zsh, fish")
}
