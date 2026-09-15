package xdg

import (
	"os"
	"path"
)

const (
	lognav             = "lognav"
	dataEnv            = "XDG_DATA_HOME"
	configEnv          = "XDG_CONFIG_HOME"
	stateEnv           = "XDG_STATE_HOME"
	fallbackDataPath   = ".local/share"
	fallbackConfigPath = ".config"
	fallbackStatePath  = ".local/state"
)

type (
	getEnvF      func(key string) string
	userHomeDirF func() (string, error)
)

// envOrFallback resolves the lognav subdir of the given XDG env var, falling
// back to homeDir/fallbackPath/lognav when the var is unset. getenv and
// userHomeDir are injected so tests can drive both branches (the
// UserHomeDir-error path is not reachable via t.Setenv); the real callers pass
// os.Getenv / os.UserHomeDir.
func envOrFallback(getenv getEnvF, userHomeDir userHomeDirF, envVar, fallbackPath string) (string, error) {
	if p := getenv(envVar); p != "" {
		return path.Join(p, lognav), nil
	}

	home, err := userHomeDir()
	if err != nil {
		return "", err
	}

	return path.Join(home, fallbackPath, lognav), nil
}

// GetDataPath resolves the XDG_DATA_HOME lognav path.
func GetDataPath() (string, error) {
	return envOrFallback(os.Getenv, os.UserHomeDir, dataEnv, fallbackDataPath)
}

// GetConfigPath resolves the XDG_CONFIG_HOME lognav path.
func GetConfigPath() (string, error) {
	return envOrFallback(os.Getenv, os.UserHomeDir, configEnv, fallbackConfigPath)
}

// GetStatePath resolves the XDG_STATE_HOME lognav path.
func GetStatePath() (string, error) {
	return envOrFallback(os.Getenv, os.UserHomeDir, stateEnv, fallbackStatePath)
}
