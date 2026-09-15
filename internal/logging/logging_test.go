package logging

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	closer, err := Init()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = closer()
		logFile = nil
	})

	entries, err := os.ReadDir(filepath.Join(dir, "lognav", "logs"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	name := entries[0].Name()
	assert.True(t, strings.HasPrefix(name, filePrefix))
	assert.True(t, strings.HasSuffix(name, fileSuffix))

	for _, tc := range []struct {
		name   string
		write  func()
		needle string
	}{
		{"slog", func() { slog.Info("slog test") }, "slog test"},
		{"stdlib log", func() { log.Print("stdlib test") }, "stdlib test"},
	} {
		t.Run(tc.name+" writes to file", func(t *testing.T) {
			tc.write()
			content, err := os.ReadFile(filepath.Join(dir, "lognav", "logs", name)) // #nosec G304 -- test-only path via t.TempDir
			require.NoError(t, err)
			assert.Contains(t, string(content), tc.needle)
		})
	}
}

// Test log filenames used across TestCleanup cases.
const (
	logFile01 = "lognav-2026-01-01T00-00-00.log"
	logFile02 = "lognav-2026-01-02T00-00-00.log"
	logFile03 = "lognav-2026-01-03T00-00-00.log"
	logFile04 = "lognav-2026-01-04T00-00-00.log"
	logFile05 = "lognav-2026-01-05T00-00-00.log"
)

func TestCleanup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files       []string
		extraFiles  []string
		maxLogFiles uint8
		initSession bool
		wantFiles   []string
	}{
		{
			name: "deletes oldest",
			files: []string{
				logFile01,
				logFile02,
				logFile03,
				logFile04,
				logFile05,
			},
			maxLogFiles: 3,
			wantFiles: []string{
				logFile03,
				logFile04,
				logFile05,
			},
		},
		{
			name: "zero disables cleanup",
			files: []string{
				logFile01,
				logFile02,
				logFile03,
			},
			maxLogFiles: 0,
			wantFiles: []string{
				logFile01,
				logFile02,
				logFile03,
			},
		},
		{
			name:        "ignores non-log files",
			files:       []string{logFile01},
			extraFiles:  []string{"other.txt"},
			maxLogFiles: 1,
			wantFiles: []string{
				logFile01,
				"other.txt",
			},
		},
		{
			name:        "under limit is noop",
			files:       []string{logFile01, logFile02},
			maxLogFiles: 5,
			wantFiles:   []string{logFile01, logFile02},
		},
		{
			name: "excludes current session",
			files: []string{
				"lognav-2020-01-01T00-00-00.log",
				"lognav-2020-01-02T00-00-00.log",
				"lognav-2020-01-03T00-00-00.log",
			},
			maxLogFiles: 2,
			initSession: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_STATE_HOME", dir)

			logDir := filepath.Join(dir, "lognav", "logs")
			require.NoError(t, os.MkdirAll(logDir, 0o700))

			for _, f := range append(tc.files, tc.extraFiles...) {
				require.NoError(t, os.WriteFile(filepath.Join(logDir, f), nil, 0o600))
			}

			old := logFile
			logFile = nil
			t.Cleanup(func() { logFile = old })

			var currentName string
			if tc.initSession {
				closer, err := Init()
				require.NoError(t, err)
				t.Cleanup(func() {
					_ = closer()
					logFile = nil
				})
				currentName = filepath.Base(logFile.Name())
			}

			Cleanup(tc.maxLogFiles)

			entries, err := os.ReadDir(logDir)
			require.NoError(t, err)

			remaining := make([]string, 0, len(entries))
			for _, e := range entries {
				remaining = append(remaining, e.Name())
			}

			if tc.initSession {
				assert.Contains(t, remaining, currentName)
				assert.NotContains(t, remaining, tc.files[0])
				assert.Contains(t, remaining, tc.files[1])
				assert.Contains(t, remaining, tc.files[2])
				assert.Len(t, remaining, 3) // 2 old + current
			} else {
				assert.Equal(t, tc.wantFiles, remaining)
			}
		})
	}
}

func TestKeyConstants(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"KeyComponent", KeyComponent, "component"},
		{"KeyAction", KeyAction, "action"},
		{"KeyDurationMS", KeyDurationMS, "duration_ms"},
		{"KeyInstance", KeyInstance, "instance"},
		{"KeyError", KeyError, "error"},
		{"KeyCount", KeyCount, "count"},
		{"KeyType", KeyType, "type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.got)
		})
	}
}

func TestEnableStderr_NilLogFile(t *testing.T) {
	old := logFile
	logFile = nil
	t.Cleanup(func() { logFile = old })

	// Should not panic.
	EnableStderr()
}
