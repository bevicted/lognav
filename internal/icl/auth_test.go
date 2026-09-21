package icl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/secret"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testInstanceDev        = "dev"
	testCRNString          = "crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"
	testPasscodeURLFixture = "https://identity-1.us-south.iam.cloud.ibm.com/identity/passcode" //nolint:gosec // test fixture URL, not a credential
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type timeoutError struct{}

func (timeoutError) Error() string { return "test timeout" }
func (timeoutError) Timeout() bool { return true }

func testCRN() *config.CRN {
	return config.MustCRNFromString(testCRNString)
}

func testAccountManager(prodKey, prodRef, testKey, testRef string) *AccountManager {
	return NewAccountManager(map[string]config.ICLEnvironmentConfig{
		string(EnvProd): {IAMURL: "https://iam.example/identity", APIKey: prodKey, APIKeyOpRef: prodRef},
		"test-cloud":    {IAMURL: "https://iam.example/test", APIKey: testKey, APIKeyOpRef: testRef},
	})
}

func newTestAccountManager(_ []config.ICLInstanceConfig, prodKey, prodRef, testKey, testRef string) *AccountManager {
	return testAccountManager(prodKey, prodRef, testKey, testRef)
}

func TestNewAccountManager(t *testing.T) {
	am := testAccountManager("prod-key", "", "", "")
	assert.Equal(t, secret.String("prod-key"), am.envs[EnvProd].apiKey)
	assert.Empty(t, am.envs[EnvProd].accessTokens)
	assert.Empty(t, am.envs[Environment("test-cloud")].accessTokens)
}

func TestAPIKeyFromEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "unset", env: map[string]string{}, want: ""},
		{name: "IBM Cloud variable", env: map[string]string{"IC_API_KEY": "standard-key"}, want: "standard-key"},
		{name: "legacy variable", env: map[string]string{"LOGNAV_IC_API_KEY": "legacy-key"}, want: "legacy-key"},
		{
			name: "IBM Cloud variable takes precedence",
			env:  map[string]string{"IC_API_KEY": "standard-key", "LOGNAV_IC_API_KEY": "legacy-key"},
			want: "standard-key",
		},
		{
			name: "empty IBM Cloud variable falls back to legacy",
			env:  map[string]string{"IC_API_KEY": "", "LOGNAV_IC_API_KEY": "legacy-key"},
			want: "legacy-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, APIKeyFromEnvironment(func(name string) string { return tt.env[name] }))
		})
	}
}

func TestQueryOIDCConfig_InvalidURL(t *testing.T) {
	_, err := queryOIDCConfig(t.Context(), "http://localhost:1/nonexistent")
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
}

func TestQueryOIDCConfig_RetriesTransportTimeout(t *testing.T) { //nolint:paralleltest // replaces package HTTP client
	original := httpClient
	t.Cleanup(func() { httpClient = original })
	var attempts int
	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, timeoutError{}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(
				`{"token_endpoint":"https://iam.example/token","passcode_endpoint":"https://iam.example/passcode"}`,
			)),
			Request: req,
		}, nil
	})}

	cfg, err := queryOIDCConfig(t.Context(), "https://iam.example/identity")
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, "https://iam.example/token", cfg.TokenEndpoint)
}

func TestPasscodeRequired_ImplementsError(t *testing.T) {
	pr := &PasscodeRequired{
		passcodeURL: testPasscodeURLFixture,
		env:         EnvProd,
	}

	var err error = pr
	if err.Error() == "" {
		t.Fatal("expected non-empty error string")
	}
	if pr.GetPasscodeURL() != testPasscodeURLFixture {
		t.Errorf("passcode URL = %q", pr.GetPasscodeURL())
	}
	if pr.Env() != EnvProd {
		t.Errorf("env = %q, want %q", pr.Env(), EnvProd)
	}
}

func TestGetAuthToken_CachedToken(t *testing.T) {
	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "", "", "", "")
	// Far-future expiry so cache hit is unambiguous once the predicate lands.
	am.envs[EnvProd].accessTokens["acct123"] = tokenEntry{
		token:     "cached-bearer-token",
		expiresAt: time.Now().Add(1 * time.Hour),
	}

	token, err := am.GetAuthToken(t.Context(), testCRN())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "cached-bearer-token" {
		t.Errorf("token = %q, want cached-bearer-token", token)
	}
}

func TestGetAuthToken_PrefersPersistedRefreshToken(t *testing.T) { //nolint:paralleltest // replaces the package 1Password seam
	original := readOnePasswordRef
	t.Cleanup(func() { readOnePasswordRef = original })
	readOnePasswordRef = func(context.Context, string) (string, error) {
		t.Fatal("1Password must not be read while a persisted refresh token is available")
		return "", errors.New("unexpected 1Password read")
	}

	callers := []struct {
		name string
		get  func(context.Context, *AccountManager) (string, error)
	}{
		{
			name: "TUI",
			get: func(ctx context.Context, am *AccountManager) (string, error) {
				return am.GetAuthToken(ctx, testCRN())
			},
		},
		{
			name: "headless query",
			get: func(ctx context.Context, am *AccountManager) (string, error) {
				return am.GetAuthTokenNoPasscode(ctx, testCRN())
			},
		},
	}

	for _, tt := range callers {
		t.Run(tt.name, func(t *testing.T) {
			var grant, apiKey, account string
			srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, r.ParseForm())
				grant = r.Form.Get("grant_type")
				apiKey = r.Form.Get("apikey")
				account = r.Form.Get("account")
				_, _ = io.WriteString(w, `{"access_token":"refreshed-access","refresh_token":"rotated-refresh","expires_in":3600}`)
			})

			am := testAccountManager("configured-key", "op://vault/item/field", "", "")
			am.SetOIDCForTest(EnvProd, srv.URL)
			am.SetRefreshTokens(map[Environment]string{EnvProd: "persisted-refresh"})

			token, err := tt.get(t.Context(), am)
			require.NoError(t, err)
			assert.Equal(t, "refreshed-access", token)
			assert.Equal(t, "refresh_token", grant)
			assert.Empty(t, apiKey)
			assert.Empty(t, account)
			assert.Equal(t, "rotated-refresh", am.GetRefreshTokens()[EnvProd])
		})
	}
}

func TestGetAuthToken_UsesUnconfiguredCRN(t *testing.T) {
	am := testAccountManager("", "", "", "")
	am.envs[EnvProd].accessTokens["acct123"] = tokenEntry{token: "cached", expiresAt: time.Now().Add(time.Hour)}

	token, err := am.GetAuthToken(t.Context(), testCRN())
	require.NoError(t, err)
	assert.Equal(t, "cached", token)
}

func TestGetAuthToken_PasscodeRequired(t *testing.T) {
	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "", "", "", "")
	// Pre-populate OIDC config to avoid network call.
	am.envs[EnvProd].oidcConfig = &oidcConfig{ //nolint:gosec // test fixture URLs, not credentials
		TokenEndpoint:    "https://iam.cloud.ibm.com/identity/token",
		PasscodeEndpoint: testPasscodeURLFixture,
	}

	_, err := am.GetAuthToken(t.Context(), testCRN())
	if err == nil {
		t.Fatal("expected error")
	}

	var pr *PasscodeRequired
	if !errors.As(err, &pr) {
		t.Fatalf("expected *PasscodeRequired, got %T: %v", err, err)
	}
	if pr.Env() != EnvProd {
		t.Errorf("env = %q, want %q", pr.Env(), EnvProd)
	}
	if pr.GetPasscodeURL() != testPasscodeURLFixture {
		t.Errorf("passcodeURL = %q", pr.GetPasscodeURL())
	}
}

func TestGetAuthTokenNoPasscode_DoesNotDiscoverOrPrompt(t *testing.T) {
	instances := []config.ICLInstanceConfig{{
		Name: testInstanceDev,
		CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
	}}
	am := newTestAccountManager(instances, "", "", "", "")
	_, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN())
	var required *HeadlessAuthRequiredError
	require.ErrorAs(t, err, &required)
	assert.Equal(t, EnvProd, required.Env)
	assert.Nil(t, am.envs[EnvProd].oidcConfig, "headless auth must not discover a passcode endpoint")
}

func TestSessionPersistence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", SessionFile)
	tokens := map[Environment]string{
		EnvProd:                   "prod-refresh",
		Environment("test-cloud"): "stage-refresh",
	}

	require.NoError(t, SaveSession(path, tokens))

	loaded, err := LoadSession(path)
	require.NoError(t, err)
	assert.Equal(t, tokens, loaded)

	data, err := os.ReadFile(path) // #nosec G304 -- test-only path via t.TempDir
	require.NoError(t, err)
	var raw map[string]string
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, map[string]string{"bluemix": "prod-refresh", "test-cloud": "stage-refresh"}, raw)
	assertSessionPermissions(t, path)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".session-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, matches, "successful save must remove its temporary file")
}

func TestSaveSession_ReplacesPermissiveFileAndDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(dir, 0o755)) //nolint:gosec // G301: test requires a permissive directory.
	require.NoError(t, os.Chmod(dir, 0o755))    //nolint:gosec // G302: test requires a permissive directory.
	path := filepath.Join(dir, SessionFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"bluemix":"old"}`), 0o644)) //nolint:gosec // G306: test requires a permissive file.
	require.NoError(t, os.Chmod(path, 0o644))                                  //nolint:gosec // G302: test requires a permissive file.

	want := map[Environment]string{EnvProd: "new", Environment("test-cloud"): "stage"}
	require.NoError(t, SaveSession(path, want))
	got, err := LoadSession(path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assertSessionPermissions(t, path)
}

func TestSaveSession_RenameFailureCleansTemporaryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, SessionFile)
	require.NoError(t, os.Mkdir(path, 0o700))
	marker := filepath.Join(path, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o600))

	err := SaveSession(path, map[Environment]string{EnvProd: "refresh"})
	require.Error(t, err)
	require.ErrorContains(t, err, "finalize session file")
	assert.FileExists(t, marker, "failed replacement must preserve the destination")
	matches, globErr := filepath.Glob(filepath.Join(dir, ".session-*.tmp"))
	require.NoError(t, globErr)
	assert.Empty(t, matches, "failed save must remove its temporary file")
}

func TestLoadSession_FileNotExist(t *testing.T) {
	t.Parallel()
	tokens, err := LoadSession(filepath.Join(t.TempDir(), "missing", SessionFile))
	require.NoError(t, err)
	assert.Empty(t, tokens)
}

func assertSessionPermissions(t *testing.T, path string) {
	t.Helper()
	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, fileInfo.Mode().IsRegular())
	if runtime.GOOS == "windows" {
		return
	}
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

func TestAccountManager_now_DefaultsToTimeNow(t *testing.T) {
	am := newTestAccountManager(nil, "", "", "", "")
	before := time.Now()
	got := am.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("am.now() = %v, want between %v and %v", got, before, after)
	}
}

func TestAccountManager_now_RespectsNowFunc(t *testing.T) {
	fixed := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	am := newTestAccountManager(nil, "", "", "", "")
	am.nowFunc = func() time.Time { return fixed }
	if got := am.now(); !got.Equal(fixed) {
		t.Fatalf("am.now() = %v, want %v", got, fixed)
	}
}

func TestExpiresAtFrom(t *testing.T) {
	fixed := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	am := newTestAccountManager(nil, "", "", "", "")
	am.nowFunc = func() time.Time { return fixed }

	tests := []struct {
		name       string
		expiresIn  int
		wantOK     bool
		wantExpiry time.Time
	}{
		{
			name:       "positive_within_range",
			expiresIn:  3600,
			wantOK:     true,
			wantExpiry: fixed.Add(1 * time.Hour),
		},
		{
			name:      "zero_treated_as_missing",
			expiresIn: 0,
			wantOK:    false,
		},
		{
			name:      "negative_treated_as_missing",
			expiresIn: -5,
			wantOK:    false,
		},
		{
			name:       "huge_capped_at_24h",
			expiresIn:  int(time.Hour.Seconds() * 48), // 48h requested
			wantOK:     true,
			wantExpiry: fixed.Add(24 * time.Hour),
		},
		{
			name:       "max_int_capped_at_24h",
			expiresIn:  1<<31 - 1,
			wantOK:     true,
			wantExpiry: fixed.Add(24 * time.Hour),
		},
		{
			name:       "overflow_wrapped_capped_at_24h",
			expiresIn:  1 << 34, // ~1.7e10 seconds; multiplied by time.Second overflows int64 and wraps negative
			wantOK:     true,
			wantExpiry: fixed.Add(24 * time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := am.expiresAtFrom(tt.expiresIn)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && !got.Equal(tt.wantExpiry) {
				t.Fatalf("expiry = %v, want %v", got, tt.wantExpiry)
			}
		})
	}
}

// newIAMStubServer returns an httptest.Server that mimics the IAM token
// endpoint, returning a JSON tokenResponse. The handler can be overridden via
// the responder field for failure-case tests.
func newIAMStubServer(t *testing.T, responder func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(responder))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetAuthTokenNoPasscode_APIKey_StoresExpiresAt(t *testing.T) {
	fixed := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	var hits atomic.Int64
	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-token","expires_in":3600}`))
	})

	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "api-key-value", "", "", "")
	am.nowFunc = func() time.Time { return fixed }
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}
	// Stale entry forces the chain to re-run.
	am.envs[EnvProd].accessTokens["acct123"] = tokenEntry{
		token:     "stale",
		expiresAt: fixed.Add(-1 * time.Hour),
	}

	token, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "new-token" {
		t.Errorf("token = %q, want new-token", token)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("IAM hits = %d, want 1", got)
	}

	entry := am.envs[EnvProd].accessTokens["acct123"]
	if entry.token != "new-token" {
		t.Errorf("stored token = %q, want new-token", entry.token)
	}
	wantExpiry := fixed.Add(1 * time.Hour)
	if !entry.expiresAt.Equal(wantExpiry) {
		t.Errorf("stored expiresAt = %v, want %v", entry.expiresAt, wantExpiry)
	}

	// Second call should be a cache hit (no additional IAM hit).
	if _, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN()); err != nil {
		t.Fatalf("second call unexpected error: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("IAM hits after second call = %d, want 1", got)
	}
}

func TestGetAuthToken_Expired_APIKey_ZeroExpiresIn_NotCached(t *testing.T) {
	// Capture slog output.
	var logBuf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"no-expiry-token"}`)) // expires_in absent
	})

	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "api-key-value", "", "", "")
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}

	token, err := am.GetAuthToken(t.Context(), testCRN())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "no-expiry-token" { //nolint:gosec // test fixture token literal, not a credential
		t.Errorf("token = %q, want no-expiry-token", token)
	}
	if _, ok := am.envs[EnvProd].accessTokens["acct123"]; ok {
		t.Errorf("expected acct123 NOT cached when expires_in is zero")
	}
	if !strings.Contains(logBuf.String(), "iam token response missing expires_in") {
		t.Errorf("expected slog.Warn about missing expires_in, got: %s", logBuf.String())
	}
}

func TestGetAuthToken_ConcurrentCallers_SingleIAMHit(t *testing.T) {
	var hits atomic.Int64
	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Tiny sleep so concurrent callers actually overlap on the chain.
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"shared-token","expires_in":3600}`))
	})

	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "api-key-value", "", "", "")
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if _, err := am.GetAuthToken(t.Context(), testCRN()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GetAuthToken err: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("IAM hits = %d, want 1 (mutex should serialize refresh)", got)
	}
}

func TestDoTokenFormRequest_Non2xxNonIAMBodySanitized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "\x1b[31m<html>proxy\r\nerror</html>\x1b[0m")
	}))
	defer srv.Close()

	_, err := doTokenFormRequest(t.Context(), srv.URL, url.Values{}, tokenExchangeAPIKey)
	require.Error(t, err)
	var iamErr *IAMError
	assert.NotErrorAs(t, err, &iamErr) // non-IAM body stays a plain error
	msg := err.Error()
	assert.Contains(t, msg, "proxy")
	assert.NotContains(t, msg, "\r")
	assert.NotContains(t, msg, "\x1b")
}

func TestIAMError_ErrorSanitizesControlBytes(t *testing.T) {
	t.Parallel()
	e := &IAMError{Code: "BX\r\nM001", Message: "bad\x1b[31mtoken"}
	got := e.Error()
	assert.Contains(t, got, "token")
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\x1b")
}

func TestGetAuthToken_RefreshGrant_IAMRejection_ClearsRefreshToken(t *testing.T) {
	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorCode":"BXNIM0407E","errorMessage":"Refresh token expired"}`))
	})

	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "", "", "", "")
	am.envs[EnvProd].oidcConfig = &oidcConfig{ //nolint:gosec // test fixture URLs, not credentials
		TokenEndpoint:    srv.URL,
		PasscodeEndpoint: "https://example.invalid/passcode",
	}
	am.envs[EnvProd].refreshToken = "expired-refresh"

	_, err := am.GetAuthToken(t.Context(), testCRN())
	var pr *PasscodeRequired
	if !errors.As(err, &pr) {
		t.Fatalf("expected *PasscodeRequired on IAM rejection, got %T: %v", err, err)
	}
	if am.envs[EnvProd].refreshToken != "" {
		t.Errorf("refresh token NOT cleared after IAM rejection: %q", am.envs[EnvProd].refreshToken)
	}
}

func TestGetAuthTokenNoPasscode_RefreshRejectionReturnsHeadlessAuthRequired(t *testing.T) {
	srv := newIAMStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorCode":"BXNIM0407E","errorMessage":"Refresh token expired"}`))
	})
	instances := []config.ICLInstanceConfig{{
		Name: testInstanceDev,
		CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
	}}
	am := newTestAccountManager(instances, "", "", "", "")
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}
	am.envs[EnvProd].refreshToken = "expired-refresh"

	_, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN())
	var required *HeadlessAuthRequiredError
	require.ErrorAs(t, err, &required)
	assert.Equal(t, EnvProd, required.Env)
	assert.Empty(t, am.envs[EnvProd].refreshToken)
	assert.Empty(t, am.envs[EnvProd].oidcConfig.PasscodeEndpoint, "headless rejection must not discover or advertise a passcode")
}

func TestGetAuthToken_RefreshRejectionFallsBackToOnePassword(t *testing.T) { //nolint:paralleltest // replaces the package 1Password seam
	original := readOnePasswordRef
	t.Cleanup(func() { readOnePasswordRef = original })
	var opReads atomic.Int64
	readOnePasswordRef = func(context.Context, string) (string, error) {
		opReads.Add(1)
		return "replacement-api-key", nil
	}

	var grants []string
	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		grant := r.Form.Get("grant_type")
		grants = append(grants, grant)
		if grant == "refresh_token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errorCode":"BXNIM0407E","errorMessage":"Refresh token expired"}`)
			return
		}
		assert.Equal(t, "replacement-api-key", r.Form.Get("apikey"))
		_, _ = io.WriteString(w, `{"access_token":"replacement-access","refresh_token":"replacement-refresh","expires_in":3600}`)
	})

	am := testAccountManager("", "op://vault/item/field", "", "")
	am.SetOIDCForTest(EnvProd, srv.URL)
	am.SetRefreshTokens(map[Environment]string{EnvProd: "expired-refresh"})

	token, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN())
	require.NoError(t, err)
	assert.Equal(t, "replacement-access", token)
	assert.Equal(t, []string{"refresh_token", "urn:ibm:params:oauth:grant-type:apikey"}, grants)
	assert.Equal(t, int64(1), opReads.Load())
	assert.Equal(t, "replacement-refresh", am.GetRefreshTokens()[EnvProd])
}

func TestGetAuthToken_RefreshGrant_TransportError_PreservesRefreshToken(t *testing.T) {
	// Server that hijacks and closes the connection mid-handshake to produce
	// a transport error (not a structured IAMError).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	am := newTestAccountManager(instances, "", "", "", "")
	am.envs[EnvProd].oidcConfig = &oidcConfig{ //nolint:gosec // test fixture URLs, not credentials
		TokenEndpoint:    srv.URL,
		PasscodeEndpoint: "https://example.invalid/passcode",
	}
	am.envs[EnvProd].refreshToken = "valid-refresh"

	_, err := am.GetAuthToken(t.Context(), testCRN())
	if err == nil {
		t.Fatal("expected error from transport failure")
	}
	var pr *PasscodeRequired
	if errors.As(err, &pr) {
		t.Errorf("transport error should NOT surface as *PasscodeRequired: %v", err)
	}
	if am.envs[EnvProd].refreshToken != "valid-refresh" {
		t.Errorf("refresh token CLEARED on transport error: %q", am.envs[EnvProd].refreshToken)
	}
}

func TestGetAuthToken_CacheHitPredicate(t *testing.T) {
	fixed := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}

	tests := []struct {
		name      string
		expiresAt time.Time
		wantHit   bool
	}{
		{name: "fresh_61s", expiresAt: fixed.Add(61 * time.Second), wantHit: true},
		{name: "boundary_60s_exact", expiresAt: fixed.Add(60 * time.Second), wantHit: false},
		{name: "boundary_59s", expiresAt: fixed.Add(59 * time.Second), wantHit: false},
		{name: "already_past", expiresAt: fixed.Add(-1 * time.Second), wantHit: false},
		{name: "zero_value", expiresAt: time.Time{}, wantHit: false},
		{name: "far_future", expiresAt: fixed.Add(1 * time.Hour), wantHit: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am := newTestAccountManager(instances, "", "", "", "")
			am.nowFunc = func() time.Time { return fixed }
			am.envs[EnvProd].accessTokens["acct123"] = tokenEntry{
				token:     "cached",
				expiresAt: tt.expiresAt,
			}
			// Pre-populate OIDC so the miss path returns *PasscodeRequired instead of hitting the network.
			//nolint:gosec // test fixture URL, not a credential
			am.envs[EnvProd].oidcConfig = &oidcConfig{
				TokenEndpoint:    "https://example.invalid/token",
				PasscodeEndpoint: "https://example.invalid/passcode",
			}

			token, err := am.GetAuthToken(t.Context(), testCRN())
			if tt.wantHit {
				if err != nil {
					t.Fatalf("expected cache hit, got err: %v", err)
				}
				if token != "cached" {
					t.Errorf("token = %q, want cached", token)
				}
			} else {
				// miss → falls through, no creds → PasscodeRequired
				var pr *PasscodeRequired
				if !errors.As(err, &pr) {
					t.Fatalf("expected miss to fall through to *PasscodeRequired, got token=%q err=%v", token, err)
				}
			}
		})
	}
}

func TestGetAuthTokenNoPasscode_RefreshGrant(t *testing.T) {
	fixed := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	var hits atomic.Int64
	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"rt-token","expires_in":3600}`))
	})

	instances := []config.ICLInstanceConfig{
		{
			Name: testInstanceDev,
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acct123:inst456::"),
		},
	}
	// No API key, no 1Password → falls to refresh-grant.
	am := newTestAccountManager(instances, "", "", "", "")
	am.nowFunc = func() time.Time { return fixed }
	am.envs[EnvProd].refreshToken = "valid-refresh"
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}
	// Stale entry forces the chain to re-run.
	am.envs[EnvProd].accessTokens["acct123"] = tokenEntry{
		token:     "stale",
		expiresAt: fixed.Add(-1 * time.Hour),
	}

	token, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "rt-token" {
		t.Errorf("token = %q, want rt-token", token)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("IAM hits = %d, want 1", got)
	}

	entry := am.envs[EnvProd].accessTokens["acct123"]
	if entry.token != "rt-token" {
		t.Errorf("stored token = %q, want rt-token", entry.token)
	}
	wantExpiry := fixed.Add(1 * time.Hour)
	if !entry.expiresAt.Equal(wantExpiry) {
		t.Errorf("stored expiresAt = %v, want %v", entry.expiresAt, wantExpiry)
	}

	// Second call should be a cache hit (no additional IAM hit).
	if _, err := am.GetAuthTokenNoPasscode(t.Context(), testCRN()); err != nil {
		t.Fatalf("second call unexpected error: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("IAM hits after second call = %d, want 1", got)
	}
}

func TestGetAuthToken_MultiAccountIsolation(t *testing.T) {
	fixed := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	var hits atomic.Int64
	srv := newIAMStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-A","expires_in":3600}`))
	})

	instances := []config.ICLInstanceConfig{
		{
			Name: "devA",
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctA:instA::"),
		},
		{
			Name: "devB",
			CRN:  config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctB:instB::"),
		},
	}
	am := newTestAccountManager(instances, "api-key-value", "", "", "")
	am.nowFunc = func() time.Time { return fixed }
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}

	// acctA stale, acctB fresh
	am.envs[EnvProd].accessTokens["acctA"] = tokenEntry{
		token:     "stale-A",
		expiresAt: fixed.Add(-1 * time.Hour),
	}
	am.envs[EnvProd].accessTokens["acctB"] = tokenEntry{
		token:     "fresh-B",
		expiresAt: fixed.Add(1 * time.Hour),
	}

	// Resolving acctA must re-exchange.
	tokenA, err := am.GetAuthToken(t.Context(), instances[0].CRN)
	if err != nil {
		t.Fatalf("devA: %v", err)
	}
	if tokenA != "fresh-A" {
		t.Errorf("devA token = %q, want fresh-A", tokenA)
	}

	// Resolving acctB must hit the cache, NOT call IAM.
	tokenB, err := am.GetAuthToken(t.Context(), instances[1].CRN)
	if err != nil {
		t.Fatalf("devB: %v", err)
	}
	if tokenB != "fresh-B" {
		t.Errorf("devB token = %q, want fresh-B (cache untouched)", tokenB)
	}

	if got := hits.Load(); got != 1 {
		t.Errorf("IAM hits = %d, want 1 (only acctA should re-exchange)", got)
	}

	// acctB's cached entry should still be intact.
	if entry := am.envs[EnvProd].accessTokens["acctB"]; entry.token != "fresh-B" {
		t.Errorf("acctB entry mutated: %+v", entry)
	}
}

func TestQueryOIDCConfig_HTTPErrorSurfacesSanitizedBody(t *testing.T) { //nolint:paralleltest // touches global httpClient
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// Verbose reason wrapped in ANSI escape + CRLF from a buggy/hostile proxy.
		_, _ = w.Write([]byte("\x1b[31m<html>proxy\r\nupstream timeout</html>\x1b[0m"))
	}))
	t.Cleanup(srv.Close)

	_, err := queryOIDCConfig(t.Context(), srv.URL)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "query OIDC config")
	assert.Contains(t, msg, "http 500")
	assert.Contains(t, msg, "upstream timeout") // verbose reason surfaced
	assert.NotContains(t, msg, "\r")            // CRLF stripped
	assert.NotContains(t, msg, "\x1b")          // ANSI escape stripped
}

func TestQueryOIDCConfig_TransportErrorWrapsContext(t *testing.T) { //nolint:paralleltest // touches global httpClient
	// Listen on a port and immediately close so the dial fails reliably.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	_, err = queryOIDCConfig(t.Context(), "http://"+addr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query OIDC config: transport")
}

func TestSecretString_AuthFieldCoverage(t *testing.T) {
	t.Parallel()
	env := &envAuth{ //nolint:gosec // test fixture credential literals, not real secrets
		refreshToken: "real-refresh-token-DO-NOT-LEAK",
		apiKey:       "real-api-key-DO-NOT-LEAK",
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	// Log individual secret fields via slog.Any so LogValue is honoured.
	// Logging the whole struct would use fmt reflect formatting which bypasses
	// LogValue for unexported fields; individual-field logging tests the
	// actual protection boundary.
	logger.Info("ctx",
		"refreshToken", env.refreshToken,
		"apiKey", env.apiKey,
	)
	out := buf.String()
	assert.NotContains(t, out, "real-refresh-token-DO-NOT-LEAK")
	assert.NotContains(t, out, "real-api-key-DO-NOT-LEAK")
}

func TestReadOnePasswordRef_RejectsNonOpScheme(t *testing.T) {
	t.Parallel()
	_, err := readOnePasswordRef(t.Context(), "opt://typo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must start with op://")
}

//nolint:paralleltest // mutates PATH via t.Setenv
func TestReadOnePasswordRef_AcceptsOpPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATHEXT lookup differs on windows")
	}
	t.Setenv("PATH", t.TempDir()) // no `op` available
	_, err := readOnePasswordRef(t.Context(), "op://test/item/field")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "must start with op://")
}

func TestDiscoverPasscodeURL_EnvironmentScopedValidation(t *testing.T) {
	t.Parallel()
	var discoveries atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveries.Add(1)
		_, _ = io.WriteString(w, `{"token_endpoint":"https://iam.example/token","passcode_endpoint":"https://iam.example/passcode"}`)
	}))
	t.Cleanup(srv.Close)

	am := testAccountManager("", "", "", "")
	am.envs[EnvProd].iamURL = srv.URL
	got, err := am.DiscoverPasscodeURL(t.Context(), EnvProd)
	require.NoError(t, err)
	assert.Equal(t, "https://iam.example/passcode", got)
	assert.Equal(t, int64(1), discoveries.Load())

	_, err = am.DiscoverPasscodeURL(t.Context(), Environment("unknown"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported IAM environment")

	am.envs[Environment("test-cloud")].oidcConfig = &oidcConfig{PasscodeEndpoint: "not a URL"}
	_, err = am.DiscoverPasscodeURL(t.Context(), Environment("test-cloud"))
	assert.EqualError(t, err, "invalid IAM endpoint")
}

func TestSetPasscode_RequiresValidatedEnvironmentAndRefreshToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		env          Environment
		response     string
		endpoint     string
		wantErr      string
		wantToken    string
		wantPasscode bool
	}{
		{
			name:         "stores nonempty refresh token",
			env:          EnvProd,
			response:     `{"refresh_token":"new-refresh"}`,
			wantToken:    "new-refresh",
			wantPasscode: true,
		},
		{
			name:     "rejects missing refresh token",
			env:      EnvProd,
			response: `{}`,
			wantErr:  "IAM passcode exchange returned no refresh token",
		},
		{
			name:     "rejects malformed token endpoint",
			env:      EnvProd,
			endpoint: "not a URL",
			wantErr:  "invalid IAM endpoint",
		},
		{
			name:    "rejects unknown environment",
			env:     Environment("unknown"),
			wantErr: `unsupported IAM environment "unknown"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPasscode string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				gotPasscode = r.Form.Get("passcode")
				_, _ = io.WriteString(w, tt.response)
			}))
			t.Cleanup(srv.Close)

			am := testAccountManager("", "", "", "")
			if tt.env == EnvProd {
				endpoint := srv.URL
				if tt.endpoint != "" {
					endpoint = tt.endpoint
				}
				am.SetOIDCForTest(EnvProd, endpoint)
			}
			err := am.SetPasscode(t.Context(), tt.env, "passcode-value")
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Empty(t, am.GetRefreshTokens())
				return
			}
			require.NoError(t, err)
			if tt.wantPasscode {
				assert.Equal(t, "passcode-value", gotPasscode)
			}
			assert.Equal(t, map[Environment]string{EnvProd: tt.wantToken}, am.GetRefreshTokens())
		})
	}
}

// TestSetPasscode_DiagnosticLogsAreSecretSafe verifies that passcode exchange
// diagnostics classify outcomes without recording credential or remote text.
func TestSetPasscode_DiagnosticLogsAreSecretSafe(t *testing.T) { //nolint:paralleltest // swaps the process-wide slog default
	var logBuf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	const (
		passcode      = "canary-passcode-do-not-log"
		accessToken   = "canary-access-token-do-not-log"
		refreshToken  = "canary-refresh-token-do-not-log"
		remoteMessage = "canary raw IAM message do not log"
		remoteDetails = "canary raw IAM details do not log"
	)

	t.Run("IAM-shaped HTTP rejection", func(t *testing.T) {
		logBuf.Reset()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errorCode":"CANARY-IAM-CODE","errorMessage":"`+remoteMessage+`","errorDetails":"`+remoteDetails+`"}`)
		}))
		t.Cleanup(srv.Close)

		am := testAccountManager("", "", "", "")
		am.SetOIDCForTest(EnvProd, srv.URL)
		err := am.SetPasscode(t.Context(), EnvProd, passcode)
		require.Error(t, err)

		logs := logBuf.String()
		assert.Contains(t, logs, `"event":"iam_token_exchange"`)
		assert.Contains(t, logs, `"stage":"http_status"`)
		assert.Contains(t, logs, `"status":400`)
		assert.Contains(t, logs, `"stage":"http_rejected"`)
		assert.Contains(t, logs, `"response_class":"iam_error_response"`)
		for _, secret := range []string{passcode, accessToken, refreshToken, "CANARY-IAM-CODE", remoteMessage, remoteDetails} {
			assert.NotContains(t, logs, secret)
		}
	})

	t.Run("malformed token endpoint", func(t *testing.T) {
		logBuf.Reset()
		const malformedEndpoint = "not-a-url-canary-token-endpoint-do-not-log"

		am := testAccountManager("", "", "", "")
		am.SetOIDCForTest(EnvProd, malformedEndpoint)
		err := am.SetPasscode(t.Context(), EnvProd, passcode)
		require.EqualError(t, err, "invalid IAM endpoint")

		logs := logBuf.String()
		assert.Contains(t, logs, `"event":"iam_passcode_exchange"`)
		assert.Contains(t, logs, `"stage":"environment_lookup","found":true`)
		assert.Contains(t, logs, `"stage":"oidc_config_retrieval_started"`)
		assert.Contains(t, logs, `"stage":"oidc_config_retrieval_succeeded"`)
		assert.Contains(t, logs, `"stage":"token_endpoint_validation_failed"`)
		assert.NotContains(t, logs, `"stage":"exchange_started"`)
		assert.NotContains(t, logs, `"event":"iam_token_exchange"`)
		for _, secret := range []string{passcode, malformedEndpoint, accessToken, refreshToken, remoteMessage, remoteDetails} {
			assert.NotContains(t, logs, secret)
		}
	})

	t.Run("successful response missing refresh token", func(t *testing.T) {
		logBuf.Reset()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"access_token":"`+accessToken+`"}`)
		}))
		t.Cleanup(srv.Close)

		am := testAccountManager("", "", "", "")
		am.SetOIDCForTest(EnvProd, srv.URL)
		err := am.SetPasscode(t.Context(), EnvProd, passcode)
		require.EqualError(t, err, "IAM passcode exchange returned no refresh token")

		logs := logBuf.String()
		assert.Contains(t, logs, `"stage":"success_decoded"`)
		assert.Contains(t, logs, `"stage":"missing_refresh_token"`)
		for _, secret := range []string{passcode, accessToken, refreshToken, remoteMessage, remoteDetails} {
			assert.NotContains(t, logs, secret)
		}
	})
}

func TestGetAuthTokenNoPasscode_OpRef_CachesAPIKey_SingleOpRead(t *testing.T) {
	var opReads atomic.Int64
	orig := readOnePasswordRef
	t.Cleanup(func() { readOnePasswordRef = orig })
	readOnePasswordRef = func(context.Context, string) (string, error) {
		opReads.Add(1)
		return "op-derived-key", nil
	}

	srv := newIAMStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	})

	// Two prod instances, DIFFERENT accountIDs, env uses opRef (no apiKey).
	instances := []config.ICLInstanceConfig{
		{Name: "a", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctA:instA::")},
		{Name: "b", CRN: config.MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/acctB:instB::")},
	}
	am := newTestAccountManager(instances, "", "op://vault/item/field", "", "")
	am.envs[EnvProd].oidcConfig = &oidcConfig{TokenEndpoint: srv.URL}

	_, err := am.GetAuthTokenNoPasscode(t.Context(), instances[0].CRN)
	require.NoError(t, err)
	_, err = am.GetAuthTokenNoPasscode(t.Context(), instances[1].CRN)
	require.NoError(t, err)

	assert.Equal(t, int64(1), opReads.Load(), "op must be read once; second account uses the cached apiKey")
	assert.Equal(t, secret.String("op-derived-key"), am.envs[EnvProd].apiKey, "op-read key must be cached on the env")
}
