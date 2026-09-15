// Package logging sets up structured logging for lognav using log/slog.
//
// All log output goes to a timestamped file under the XDG state directory.
// Logging is always on at debug level.
package logging

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bevicted/lognav/internal/xdg"
)

const (
	filePrefix = "lognav-"
	fileSuffix = ".log"
	timeFormat = "2006-01-02T15-04-05"
)

// Log key constants for structured logging. Use these instead of string literals
// to keep key names consistent across the codebase.
const (
	KeyComponent  = "component"
	KeyAction     = "action"
	KeyDurationMS = "duration_ms"
	KeyInstance   = "instance"
	KeyError      = "error"
	KeyCount      = "count"
	KeyType       = "type"
)

var logFile *os.File

// Init creates a session log file and configures slog as the default logger.
// Returns a function to close the log file.
func Init() (func() error, error) {
	dir, err := logDir()
	if err != nil {
		return nil, err
	}

	filename := fmt.Sprintf("%s%s%s", filePrefix, time.Now().Format(timeFormat), fileSuffix)
	logFile, err = os.Create(filepath.Join(dir, filename)) // #nosec G304 -- xdg-resolved state dir + generated filename
	if err != nil {
		return nil, err
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(logFile)

	return logFile.Close, nil
}

// Cleanup removes old log files when the count exceeds maxLogFiles.
// A maxLogFiles of 0 disables cleanup. Best-effort: deletion errors are
// logged as warnings.
func Cleanup(maxLogFiles uint8) {
	if maxLogFiles == 0 {
		return
	}

	dir, err := logDir()
	if err != nil {
		slog.Warn("log cleanup failed to resolve directory", "error", err)
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("log cleanup failed to read directory", "error", err)
		return
	}

	var logFiles []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) {
			logFiles = append(logFiles, name)
		}
	}

	// Files sort chronologically by name due to the timestamp format.
	slices.Sort(logFiles)

	// Exclude the current session's file.
	if logFile != nil {
		currentName := filepath.Base(logFile.Name())
		logFiles = slices.DeleteFunc(logFiles, func(s string) bool {
			return s == currentName
		})
	}

	for len(logFiles) > int(maxLogFiles) {
		p := filepath.Join(dir, logFiles[0])
		if err := os.Remove(p); err != nil {
			slog.Warn("log cleanup failed to delete file", "path", p, "error", err)
		}
		logFiles = logFiles[1:]
	}
}

// EnableStderr adds stderr as an additional log output alongside the file.
func EnableStderr() {
	if logFile == nil {
		return
	}
	w := io.MultiWriter(logFile, os.Stderr)
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(w)
}

func logDir() (string, error) {
	statePath, err := xdg.GetStatePath()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(statePath, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
