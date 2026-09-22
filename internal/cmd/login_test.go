package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/state"
)

// loginTestManager returns a manager whose cached OIDC endpoints use srv for
// both selected environments. The command never receives configured API keys.
func loginTestEnvironments() map[string]config.ICLEnvironmentConfig {
	return map[string]config.ICLEnvironmentConfig{
		"bluemix":    {IAMURL: "https://iam.example/identity"},
		"test-cloud": {IAMURL: "https://iam.example/test"},
	}
}

func loginTestManager(srv *httptest.Server) *icl.AccountManager {
	manager := icl.NewAccountManager(loginTestEnvironments())
	manager.SetOIDCForTest(icl.EnvProd, srv.URL, srv.URL)
	manager.SetOIDCForTest(icl.Environment("test-cloud"), srv.URL, srv.URL)
	return manager
}

// setLoginSeams replaces the command's process-wide seams. Login command tests
// are serial because these seams and isTTY are package globals.
func setLoginSeams(t *testing.T) {
	t.Helper()
	oldManager := newLoginAccountManager
	oldPath := loginSessionPath
	oldLoad := loadLoginSession
	oldSave := saveLoginSession
	oldReader := readLoginPasscode
	oldTTY := isTTY
	oldBrowser := openBrowser
	t.Cleanup(func() {
		newLoginAccountManager = oldManager
		loginSessionPath = oldPath
		loadLoginSession = oldLoad
		saveLoginSession = oldSave
		readLoginPasscode = oldReader
		isTTY = oldTTY
		openBrowser = oldBrowser
	})
}

func runLoginCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cfg := config.New()
	cfg.ICL.Environments = loginTestEnvironments()
	return runLoginCommandWithConfig(t, cfg, args...)
}

func runLoginCommandWithConfig(t *testing.T, cfg *config.Config, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd(func(bool) (deps.Bundle, error) { return deps.New(cfg, state.New()), nil })
	var stdout, stderr bytes.Buffer
	root.SetArgs(append([]string{"login"}, args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestLogin_DefaultAndRepeatedEnvironments(t *testing.T) { //nolint:paralleltest // replaces command seams
	t.Run("defaults to production", func(t *testing.T) {
		setLoginSeams(t)
		var grants int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			grants++
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			assert.Equal(t, "urn:ibm:params:oauth:grant-type:passcode", r.Form.Get("grant_type"))
			_, _ = io.WriteString(w, `{"refresh_token":"prod-refresh"}`)
		}))
		t.Cleanup(srv.Close)
		newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
		loginSessionPath = func() (string, error) { return "session", nil }
		loadLoginSession = func(string) (map[icl.Environment]string, error) {
			return map[icl.Environment]string{icl.Environment("test-cloud"): "existing-stage-refresh"}, nil
		}
		saved := map[icl.Environment]string{}
		saveLoginSession = func(_ string, tokens map[icl.Environment]string) error {
			saved = make(map[icl.Environment]string, len(tokens))
			maps.Copy(saved, tokens)
			return nil
		}
		isTTY = func() bool { return true }
		readLoginPasscode = func(context.Context) ([]byte, error) { return []byte("prod-passcode"), nil }
		browserCalls := 0
		openBrowser = func(context.Context, string) error { browserCalls++; return nil }

		stdout, stderr, err := runLoginCommand(t, "--no-open")
		require.NoError(t, err)
		assert.Empty(t, stderr)
		assert.Equal(t, 1, grants)
		assert.Zero(t, browserCalls)
		assert.Equal(t, map[icl.Environment]string{icl.EnvProd: "prod-refresh", icl.Environment("test-cloud"): "existing-stage-refresh"}, saved)
		assert.Contains(t, stdout, "Logged in to bluemix.")
		assert.NotContains(t, stdout, "prod-passcode")
	})

	t.Run("deduplicates in first-seen order", func(t *testing.T) {
		setLoginSeams(t)
		var passcodes []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			passcode := r.Form.Get("passcode")
			passcodes = append(passcodes, passcode)
			switch passcode {
			case "stage-code":
				_, _ = io.WriteString(w, `{"refresh_token":"refresh-stage-code"}`)
			case "prod-code":
				_, _ = io.WriteString(w, `{"refresh_token":"refresh-prod-code"}`)
			default:
				t.Errorf("unexpected passcode")
			}
		}))
		t.Cleanup(srv.Close)
		newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
		loginSessionPath = func() (string, error) { return "session", nil }
		loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
		var saves []map[icl.Environment]string
		saveLoginSession = func(_ string, tokens map[icl.Environment]string) error {
			saved := make(map[icl.Environment]string, len(tokens))
			maps.Copy(saved, tokens)
			saves = append(saves, saved)
			return nil
		}
		isTTY = func() bool { return true }
		codes := [][]byte{[]byte("stage-code"), []byte("prod-code")}
		readLoginPasscode = func(context.Context) ([]byte, error) {
			code := codes[0]
			codes = codes[1:]
			return code, nil
		}

		stdout, _, err := runLoginCommand(t, "--environment", "test-cloud", "--environment", "bluemix", "--environment", "test-cloud", "--no-open")
		require.NoError(t, err)
		assert.Equal(t, []string{"stage-code", "prod-code"}, passcodes)
		require.Len(t, saves, 2)
		assert.Equal(t, map[icl.Environment]string{icl.Environment("test-cloud"): "refresh-stage-code"}, saves[0])
		assert.Equal(t, map[icl.Environment]string{icl.Environment("test-cloud"): "refresh-stage-code", icl.EnvProd: "refresh-prod-code"}, saves[1])
		assert.Less(t, bytes.Index([]byte(stdout), []byte("Logged in to test-cloud.")), bytes.Index([]byte(stdout), []byte("Logged in to bluemix.")))
		assert.NotContains(t, stdout, "stage-passcode")
		assert.NotContains(t, stdout, "prod-passcode")
	})
}

func TestLogin_BrowserConfigAndNoOpen(t *testing.T) { //nolint:paralleltest // replaces command seams
	setLoginSeams(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"refresh_token":"refresh"}`)
	}))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
	saveLoginSession = func(string, map[icl.Environment]string) error { return nil }
	readLoginPasscode = func(context.Context) ([]byte, error) { return []byte("passcode"), nil }
	isTTY = func() bool { return true }

	browserCalls := 0
	openBrowser = func(context.Context, string) error { browserCalls++; return nil }
	for _, tt := range []struct {
		name        string
		configValue bool
		args        []string
		wantCalls   int
	}{
		{name: "config enabled", configValue: true, wantCalls: 1},
		{name: "config disabled", configValue: false, wantCalls: 0},
		{name: "no-open overrides config", configValue: true, args: []string{"--no-open"}, wantCalls: 0},
	} {
		cfg := config.New()
		cfg.ICL.Environments = loginTestEnvironments()
		cfg.Core.OpenBrowser = tt.configValue
		before := browserCalls
		_, _, err := runLoginCommandWithConfig(t, cfg, tt.args...)
		require.NoError(t, err, tt.name)
		assert.Equal(t, tt.wantCalls, browserCalls-before, tt.name)
	}
}

// TestLogin_DiagnosticLogsCheckpoints verifies standalone checkpoint logging
// includes the session save and never includes login credentials.
func TestLogin_DiagnosticLogsCheckpoints(t *testing.T) { //nolint:paralleltest // replaces command seams and the process-wide slog default
	setLoginSeams(t)
	var logBuf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	const (
		passcode = "canary-login-passcode-do-not-log" //nolint:gosec // test canary, not a credential
		refresh  = "canary-login-refresh-token-do-not-log"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"refresh_token":"`+refresh+`"}`)
	}))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
	saveLoginSession = func(string, map[icl.Environment]string) error { return nil }
	isTTY = func() bool { return true }
	readLoginPasscode = func(context.Context) ([]byte, error) { return []byte(passcode), nil }

	_, _, err := runLoginCommand(t, "--no-open")
	require.NoError(t, err)
	logs := logBuf.String()
	assert.Contains(t, logs, `"event":"standalone_login"`)
	assert.Contains(t, logs, `"environment":"bluemix"`)
	assert.Contains(t, logs, `"operation":"passcode_discovery","stage":"started"`)
	assert.Contains(t, logs, `"operation":"passcode_exchange","stage":"succeeded"`)
	assert.Contains(t, logs, `"operation":"session_save","stage":"succeeded"`)
	assert.NotContains(t, logs, passcode)
	assert.NotContains(t, logs, refresh)
}

func TestLogin_EmptyNormalizedPasscodeDiagnosticLogsAreSecretSafe(t *testing.T) { //nolint:paralleltest // replaces command seams and the process-wide slog default
	setLoginSeams(t)
	var logBuf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	tokenEndpointCanary := t.Name()
	tokenEndpoint := "https://iam.example/" + tokenEndpointCanary
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager {
		manager := icl.NewAccountManager(loginTestEnvironments())
		manager.SetOIDCForTest(icl.EnvProd, tokenEndpoint, "https://iam.example/passcode")
		return manager
	}
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
	isTTY = func() bool { return true }
	readLoginPasscode = func(context.Context) ([]byte, error) {
		return append(append([]byte{}, loginPasteStart...), loginPasteEnd...), nil
	}

	_, _, err := runLoginCommand(t, "--no-open")
	require.Error(t, err)
	require.Equal(t, ExitUnavailable, ExitCode(err))

	logs := logBuf.String()
	assert.Contains(t, logs, `"operation":"passcode_input","stage":"raw_received","byte_length":12,"rune_length":12`)
	assert.Contains(t, logs, `"operation":"passcode_input","stage":"normalized","byte_length":0,"rune_length":0,"bracketed_framing_removed":true`)
	assert.Contains(t, logs, `"event":"iam_passcode_exchange"`)
	assert.Contains(t, logs, `"stage":"environment_lookup","found":true`)
	assert.Contains(t, logs, `"stage":"empty_passcode_rejected"`)
	assert.NotContains(t, logs, `"stage":"oidc_config_retrieval_started"`)
	assert.NotContains(t, logs, `"stage":"exchange_started"`)
	assert.NotContains(t, logs, `"event":"iam_token_exchange"`)
	assert.NotContains(t, logs, tokenEndpointCanary)
	assert.NotContains(t, logs, tokenEndpoint)
}

func TestLogin_PreflightRejectsInvalidEnvironmentAndNonTTY(t *testing.T) { //nolint:paralleltest // replaces command seams
	t.Run("invalid environment", func(t *testing.T) {
		setLoginSeams(t)
		called := false
		newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { called = true; return nil }
		isTTY = func() bool { return true }

		_, _, err := runLoginCommand(t, "--environment", "development")
		require.Error(t, err)
		assert.Equal(t, ExitUsage, ExitCode(err))
		assert.False(t, called)
	})

	t.Run("non tty has no side effects", func(t *testing.T) {
		setLoginSeams(t)
		var manager, session, reader, browser bool
		newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { manager = true; return nil }
		loginSessionPath = func() (string, error) { session = true; return "", nil }
		readLoginPasscode = func(context.Context) ([]byte, error) { reader = true; return nil, nil }
		openBrowser = func(context.Context, string) error { browser = true; return nil }
		isTTY = func() bool { return false }

		stdout, stderr, err := runLoginCommand(t)
		require.Error(t, err)
		assert.Equal(t, ExitUsage, ExitCode(err))
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.False(t, manager || session || reader || browser)
	})
}

func TestLogin_BrowserFallbackCheckpointAndSecretFreeErrors(t *testing.T) { //nolint:paralleltest // replaces command seams
	setLoginSeams(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if calls == 1 {
			_, _ = io.WriteString(w, `{"refresh_token":"stage-refresh"}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errorCode":"bad","errorMessage":"prod-passcode must not escape"}`)
	}))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) {
		return map[icl.Environment]string{icl.EnvProd: "previous-prod-refresh"}, nil
	}
	var saves []map[icl.Environment]string
	saveLoginSession = func(_ string, tokens map[icl.Environment]string) error {
		saved := make(map[icl.Environment]string, len(tokens))
		maps.Copy(saved, tokens)
		saves = append(saves, saved)
		return nil
	}
	isTTY = func() bool { return true }
	codes := [][]byte{[]byte("stage-passcode"), []byte("prod-passcode")}
	readLoginPasscode = func(context.Context) ([]byte, error) {
		code := codes[0]
		codes = codes[1:]
		return code, nil
	}
	openBrowser = func(context.Context, string) error { return errors.New("browser URL error") }

	stdout, stderr, err := runLoginCommand(t, "--environment", "test-cloud", "--environment", "bluemix")
	require.Error(t, err)
	assert.Equal(t, ExitUnavailable, ExitCode(err))
	require.EqualError(t, err, "IAM passcode exchange failed")
	assert.Contains(t, stderr, "Warning: could not open browser; open the URL manually.")
	require.Equal(t, 2, calls)
	require.Len(t, saves, 1, "the successful custom-environment login must checkpoint before production fails")
	assert.Equal(t, map[icl.Environment]string{icl.EnvProd: "previous-prod-refresh", icl.Environment("test-cloud"): "stage-refresh"}, saves[0])
	for _, secret := range []string{"stage-passcode", "prod-passcode", "stage-refresh", "previous-prod-refresh", "browser URL error"} {
		assert.NotContains(t, stdout, secret)
		assert.NotContains(t, stderr, secret)
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestLogin_UnframesBracketedPastePasscode(t *testing.T) { //nolint:paralleltest // replaces command seams
	setLoginSeams(t)
	const passcode = "fresh-code"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.NoError(t, r.ParseForm()) {
			return
		}
		assert.Equal(t, passcode, r.Form.Get("passcode"))
		_, _ = io.WriteString(w, `{"refresh_token":"fresh-refresh"}`)
	}))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
	saveLoginSession = func(string, map[icl.Environment]string) error { return nil }
	isTTY = func() bool { return true }
	readLoginPasscode = func(context.Context) ([]byte, error) {
		return []byte("\x1b[200~" + passcode + "\x1b[201~"), nil
	}

	stdout, stderr, err := runLoginCommand(t, "--no-open")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Logged in to bluemix.")
	assert.NotContains(t, stdout, passcode)
}

func TestLogin_PasscodeValueMatchesTUIInput(t *testing.T) { //nolint:paralleltest // replaces command seams
	setLoginSeams(t)
	const passcode = "0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.NoError(t, r.ParseForm()) {
			return
		}
		assert.Equal(t, passcode, r.Form.Get("passcode"))
		_, _ = io.WriteString(w, `{"refresh_token":"fresh-refresh"}`)
	}))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
	saveLoginSession = func(string, map[icl.Environment]string) error { return nil }
	isTTY = func() bool { return true }
	readLoginPasscode = func(context.Context) ([]byte, error) {
		return []byte(passcode + " copied trailing text"), nil
	}

	_, _, err := runLoginCommand(t, "--no-open")
	require.NoError(t, err)
}

func TestLogin_MaskedReaderErrorDoesNotExchange(t *testing.T) { //nolint:paralleltest // replaces command seams
	setLoginSeams(t)
	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { exchanges++ }))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	loginSessionPath = func() (string, error) { return "session", nil }
	loadLoginSession = func(string) (map[icl.Environment]string, error) { return map[icl.Environment]string{}, nil }
	saveLoginSession = func(string, map[icl.Environment]string) error { return nil }
	isTTY = func() bool { return true }
	readLoginPasscode = func(context.Context) ([]byte, error) { return nil, errors.New("reader included a passcode") }

	stdout, stderr, err := runLoginCommand(t, "--no-open")
	require.Error(t, err)
	require.EqualError(t, err, "read passcode from terminal")
	assert.Zero(t, exchanges)
	assert.Empty(t, stderr)
	assert.NotContains(t, stdout, "reader included a passcode")
	assert.NotContains(t, err.Error(), "reader included a passcode")
}

// TestLogin_PersistsSessionWithHardenedSavePath exercises the command's real
// SessionPath, LoadSession, and hardened SaveSession integration.
func TestLogin_PersistsSessionWithHardenedSavePath(t *testing.T) { //nolint:paralleltest // replaces command seams and XDG environment
	setLoginSeams(t)
	setQueryTestXDG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"refresh_token":"new-prod-refresh"}`)
	}))
	t.Cleanup(srv.Close)
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager { return loginTestManager(srv) }
	isTTY = func() bool { return true }
	readLoginPasscode = func(context.Context) ([]byte, error) { return []byte("prod-passcode"), nil }

	path, err := icl.SessionPath()
	require.NoError(t, err)
	require.NoError(t, icl.SaveSession(path, map[icl.Environment]string{icl.Environment("test-cloud"): "existing-stage-refresh"}))
	_, _, err = runLoginCommand(t, "--no-open")
	require.NoError(t, err)
	got, err := icl.LoadSession(path)
	require.NoError(t, err)
	assert.Equal(t, map[icl.Environment]string{icl.EnvProd: "new-prod-refresh", icl.Environment("test-cloud"): "existing-stage-refresh"}, got)
}

// TestExecute_LoginSignalHelper is the subprocess side of the real Execute
// signal test. It waits at the masked-reader seam without discovering IAM or
// saving a session.
func TestExecute_LoginSignalHelper(t *testing.T) { //nolint:paralleltest // subprocess mutates command seams
	if os.Getenv("LOGNAV_LOGIN_SIGNAL_HELPER") == "" {
		return
	}
	ready := os.Getenv("LOGNAV_LOGIN_SIGNAL_READY")
	isTTY = func() bool { return true }
	newLoginAccountManager = func(_ map[string]config.ICLEnvironmentConfig) *icl.AccountManager {
		manager := icl.NewAccountManager(loginTestEnvironments())
		manager.SetOIDCForTest(icl.EnvProd, "https://iam.example/token", "https://iam.example/passcode")
		return manager
	}
	readLoginPasscode = func(ctx context.Context) ([]byte, error) {
		require.NoError(t, os.WriteFile(ready, []byte("ready"), 0o600)) // #nosec G703 -- test parent provides an isolated path
		wait := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { close(wait) })
		defer stop()
		<-wait
		return nil, ctx.Err()
	}
	newExecuteRoot = func() *cobra.Command {
		root := newRootCmd(func(bool) (deps.Bundle, error) { return deps.New(config.New(), state.New()), nil })
		root.SetArgs([]string{"login", "--no-open"})
		root.SetIn(os.Stdin)
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		return root
	}
	os.Exit(ExitCode(Execute()))
}

func TestExecute_LoginSignalCancellation(t *testing.T) { //nolint:paralleltest // spawns signal subprocesses
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics differ on Windows")
	}
	for _, tc := range []struct {
		name string
		sig  os.Signal
		code int
	}{
		{name: "SIGINT", sig: os.Interrupt, code: 130},
		{name: "SIGTERM", sig: syscall.SIGTERM, code: 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestExecute_LoginSignalHelper$") // #nosec G204,G702 -- invokes the current test binary
			child.Env = append(os.Environ(),
				"LOGNAV_LOGIN_SIGNAL_HELPER=1",
				"LOGNAV_LOGIN_SIGNAL_READY="+ready,
				"XDG_STATE_HOME="+t.TempDir(),
				"XDG_CONFIG_HOME="+t.TempDir(),
			)
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			require.NoError(t, child.Start())
			require.Eventually(t, func() bool {
				_, err := os.Stat(ready)
				return err == nil
			}, time.Second, 10*time.Millisecond)
			require.NoError(t, child.Process.Signal(tc.sig))
			require.Error(t, child.Wait())
			assert.Equal(t, tc.code, child.ProcessState.ExitCode())
			assert.NotContains(t, stdout.String(), "Logged in")
			assert.NotContains(t, stderr.String(), "passcode")
		})
	}
}
