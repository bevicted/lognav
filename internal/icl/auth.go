package icl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/secret"
	"github.com/bevicted/lognav/internal/xdg"
)

// JSON sites in this file use jsonutil.API (sonic). All call sites are
// one-shot Marshal/Unmarshal over byte slices — safe per F.2 D4 (sonic
// streaming forbidden).

// Environment identifies an IAM endpoint by its configured CRN CName.
type Environment string

const (
	// EnvProd is the public production environment. LOGNAV_IC_API_KEY only
	// overrides credentials for this environment.
	EnvProd Environment = "bluemix"

	// bxbxAuth is base64("bx:bx") — the IBM Cloud public client credentials
	// used for the IAM token endpoint per IBM's documented OAuth flows.
	bxbxAuth        = "Basic Yng6Yng="
	lognavUserAgent = "lognav"

	// SessionFile is the filename for persisted refresh tokens.
	SessionFile = "session.json"

	httpTimeout = 30 * time.Second

	// OIDC discovery is an idempotent GET. Retry one transport timeout so a
	// transient TLS handshake failure does not abort authentication.
	oidcDiscoveryAttempts = 2
	oidcRetryDelay        = 100 * time.Millisecond

	// maxTokenLifetime caps untrusted ExpiresIn values returned by IAM so an
	// out-of-range response cannot overflow time.Duration arithmetic.
	maxTokenLifetime = 24 * time.Hour

	// refreshBuffer is the slack window before a cached access token's expiry
	// during which the token is treated as already expired. Covers clock skew,
	// query round-trip latency, and time spent re-running the auth chain.
	refreshBuffer = 60 * time.Second

	// opRefPrefix is the sole 1Password CLI secret-reference scheme prefix.
	opRefPrefix = "op://"
)

// httpClient is shared across all auth-related HTTP calls. A timeout is set so
// stalled IAM/OIDC endpoints can't hang token resolution indefinitely.
var httpClient = &http.Client{Timeout: httpTimeout}

// SessionPath returns the absolute path to the persisted session file.
func SessionPath() (string, error) {
	statePath, err := xdg.GetStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(statePath, SessionFile), nil
}

// oidcConfig holds the OIDC discovery endpoints used for token and passcode flows.
type oidcConfig struct {
	TokenEndpoint    string `json:"token_endpoint"`
	PasscodeEndpoint string `json:"passcode_endpoint"`
}

// tokenResponse models the subset of IAM token-endpoint fields lognav uses.
type tokenResponse struct {
	AccessToken  secret.String `json:"access_token"`
	RefreshToken secret.String `json:"refresh_token"`
	ExpiresIn    int           `json:"expires_in"` // seconds; may be absent or zero
}

type tokenExchangeKind string

const (
	tokenExchangePasscode tokenExchangeKind = "passcode"
	tokenExchangeAPIKey   tokenExchangeKind = "api_key"
	tokenExchangeRefresh  tokenExchangeKind = "refresh_token"
)

// tokenEntry is a cached IAM access token with its computed expiry.
// expiresAt is the wall-clock time at which the token should be treated as
// expired; the cache-hit predicate adds a buffer on top of this.
type tokenEntry struct {
	token     secret.String
	expiresAt time.Time
}

type envAuth struct {
	mu           sync.Mutex
	iamURL       string
	refreshToken secret.String
	accessTokens map[string]tokenEntry // keyed by account ID (CRN.ScopeID)
	oidcConfig   *oidcConfig
	apiKey       secret.String
	opRef        string
}

// AccountManager resolves bearer tokens for ICL CRNs. Each configured
// environment has independent credentials, sessions, and account token cache.
type AccountManager struct {
	envs    map[Environment]*envAuth
	nowFunc func() time.Time
}

// NewAccountManager builds an AccountManager from configured IAM environments.
func NewAccountManager(environments map[string]config.ICLEnvironmentConfig) *AccountManager {
	envs := make(map[Environment]*envAuth, len(environments))
	for cname, environment := range environments {
		envs[Environment(cname)] = &envAuth{
			iamURL:       environment.IAMURL,
			accessTokens: map[string]tokenEntry{},
			apiKey:       secret.String(environment.APIKey),
			opRef:        environment.APIKeyOpRef,
		}
	}
	return &AccountManager{envs: envs, nowFunc: time.Now}
}

// NewPasscodeAccountManager builds a manager that retains configured IAM URLs
// but deliberately omits configured noninteractive credentials.
func NewPasscodeAccountManager(environments map[string]config.ICLEnvironmentConfig) *AccountManager {
	passcodeEnvironments := make(map[string]config.ICLEnvironmentConfig, len(environments))
	for cname, environment := range environments {
		environment.APIKey = ""
		environment.APIKeyOpRef = ""
		passcodeEnvironments[cname] = environment
	}
	return NewAccountManager(passcodeEnvironments)
}

// now returns the current time via the injected nowFunc (a test seam; the
// constructor always sets it to time.Now).
func (am *AccountManager) now() time.Time {
	return am.nowFunc()
}

// expiresAtFrom converts an IAM expires_in value (seconds) into an absolute
// expiry time. Returns ok=false if the value is non-positive, signalling that
// the caller should not cache the token.
func (am *AccountManager) expiresAtFrom(expiresIn int) (time.Time, bool) {
	if expiresIn <= 0 {
		return time.Time{}, false
	}
	d := time.Duration(expiresIn) * time.Second
	if d > maxTokenLifetime || d < 0 { // d<0 guards against overflow
		d = maxTokenLifetime
	}
	return am.now().Add(d), true
}

// storeAccessToken caches resp.AccessToken under accountID with an expiry
// derived from resp.ExpiresIn. If ExpiresIn is missing or non-positive the
// token is NOT cached and a warning is logged so a misconfigured IAM is
// visible rather than silently re-exchanging forever. The enclosing
// exchange function is still responsible for returning resp.AccessToken
// to its caller.
func (am *AccountManager) storeAccessToken(ea *envAuth, accountID string, resp tokenResponse) {
	expiresAt, ok := am.expiresAtFrom(resp.ExpiresIn)
	if !ok {
		slog.Warn("iam token response missing expires_in",
			"accountID", accountID,
			"iamURL", ea.iamURL)
		return
	}
	if ea.accessTokens == nil {
		ea.accessTokens = map[string]tokenEntry{}
	}
	ea.accessTokens[accountID] = tokenEntry{token: resp.AccessToken, expiresAt: expiresAt}
}

// getOIDCConfig returns the cached OIDC config for the given environment, or fetches and caches it.
func (am *AccountManager) getOIDCConfig(ctx context.Context, env *envAuth) (oidcConfig, error) {
	if env.oidcConfig != nil {
		return *env.oidcConfig, nil
	}
	cfg, err := queryOIDCConfig(ctx, env.iamURL)
	if err != nil {
		return oidcConfig{}, err
	}
	env.oidcConfig = &cfg
	return cfg, nil
}

// queryOIDCConfig fetches the OIDC discovery document from baseURL/.well-known/openid-configuration.
func queryOIDCConfig(ctx context.Context, baseURL string) (oidcConfig, error) {
	logger := slog.Default().With(logging.KeyComponent, "auth")
	endpoint := baseURL + "/.well-known/openid-configuration"

	var resp *http.Response
	for attempt := range oidcDiscoveryAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "request_construction_failed")
			return oidcConfig{}, fmt.Errorf("query OIDC config: build request: %w", err)
		}
		logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "request_constructed")
		req.Header.Add("User-Agent", lognavUserAgent)

		resp, err = httpClient.Do(req)
		if err == nil {
			break
		}
		var netErr net.Error
		if attempt+1 == oidcDiscoveryAttempts || !errors.As(err, &netErr) || !netErr.Timeout() {
			logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "transport_failed")
			return oidcConfig{}, fmt.Errorf("query OIDC config: transport: %w", err)
		}
		timer := time.NewTimer(oidcRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "transport_failed")
			return oidcConfig{}, fmt.Errorf("query OIDC config: transport: %w", ctx.Err())
		case <-timer.C:
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "response_read_failed")
		return oidcConfig{}, fmt.Errorf("query OIDC config: read body: %w", err)
	}
	logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "http_status", "status", resp.StatusCode)

	if isHTTPErr(resp.StatusCode) {
		// The body carries the endpoint's verbose reason for the failure and
		// flows into the TUI-rendered error. It is server-supplied and could
		// carry CRLF / control bytes from a hostile IAM endpoint, so sanitize
		// before surfacing it. Do not log the body: it is untrusted remote text.
		logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "http_rejected", "response_class", "other_error_response", "status", resp.StatusCode)
		if reason := sanitizeErrBody(body); reason != "" {
			return oidcConfig{}, fmt.Errorf("query OIDC config: http %d: %s", resp.StatusCode, reason)
		}
		return oidcConfig{}, fmt.Errorf("query OIDC config: http %d", resp.StatusCode)
	}

	var cfg oidcConfig
	if err := jsonutil.API.Unmarshal(body, &cfg); err != nil {
		logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "success_decode_failed")
		return oidcConfig{}, fmt.Errorf("query OIDC config: parse response: %w", err)
	}
	logger.Debug("IAM OIDC discovery checkpoint", "event", "iam_oidc_discovery", "stage", "success_decoded")
	return cfg, nil
}

func isHTTPErr(status int) bool {
	return status < 200 || status >= 300
}

// IAMError represents a structured error response from the IAM token endpoint.
type IAMError struct {
	Code    string `json:"errorCode"`
	Message string `json:"errorMessage"`
	Details string `json:"errorDetails"`
}

func (e *IAMError) Error() string {
	// Code/Message are server-supplied and render into the TUI; strip control
	// bytes so a hostile IAM endpoint cannot inject CRLF / ANSI sequences.
	return sanitizeErrBody(fmt.Appendf(nil, "%s: %s", e.Code, e.Message))
}

// doTokenFormRequest sends a form-encoded POST to tokenEndpoint with bx:bx auth
// and unmarshals the IAM token response. Passcode exchanges emit only safe
// checkpoints; their form, headers, endpoint, response body, and errors may be
// sensitive.
func doTokenFormRequest(ctx context.Context, tokenEndpoint string, body url.Values, kind tokenExchangeKind) (tokenResponse, error) {
	logger := slog.Default().With(logging.KeyComponent, "auth", "event", "iam_token_exchange", "exchange_kind", string(kind))
	logCheckpoint := func(stage string, args ...any) {
		if kind == tokenExchangePasscode {
			logger.Debug("IAM passcode exchange checkpoint", append([]any{"stage", stage}, args...)...)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(body.Encode()))
	if err != nil {
		logCheckpoint("request_construction_failed")
		return tokenResponse{}, err
	}
	logCheckpoint("request_constructed")

	req.Header.Add("Authorization", bxbxAuth)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("User-Agent", lognavUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		logCheckpoint("transport_failed")
		return tokenResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logCheckpoint("response_read_failed")
		return tokenResponse{}, err
	}
	logCheckpoint("http_status", "status", resp.StatusCode)

	if isHTTPErr(resp.StatusCode) {
		var iamErr IAMError
		if err := jsonutil.API.Unmarshal(respBody, &iamErr); err != nil {
			// HTTP error with a non-IAM-shaped body (e.g. proxy HTML page).
			// Return a plain error so callers that distinguish *IAMError treat
			// this as transient/non-auth rather than a credential rejection.
			// Sanitize: the body is server-supplied and renders into the TUI.
			logCheckpoint("http_rejected", "response_class", "other_error_response", "status", resp.StatusCode)
			if reason := sanitizeErrBody(respBody); reason != "" {
				return tokenResponse{}, errors.New(reason)
			}
			return tokenResponse{}, fmt.Errorf("token request: http %d", resp.StatusCode)
		}
		logCheckpoint("http_rejected", "response_class", "iam_error_response", "status", resp.StatusCode)
		return tokenResponse{}, &iamErr
	}

	var value tokenResponse
	if err := jsonutil.API.Unmarshal(respBody, &value); err != nil {
		logCheckpoint("success_decode_failed")
		return tokenResponse{}, err
	}
	logCheckpoint("success_decoded")
	return value, nil
}

// PasscodeRequired is returned by GetAuthToken when interactive passcode authentication is needed.
//
//nolint:errname // public API; rename would break downstream callers
type PasscodeRequired struct {
	passcodeURL string
	env         Environment
}

// NewPasscodeRequired builds the URL/env-bearing PasscodeRequired error. It is
// pure data: the passcode-for-token exchange lives on
// AccountManager.SetPasscode, which the caller (the picker) already holds.
func NewPasscodeRequired(passcodeURL string, env Environment) *PasscodeRequired {
	return &PasscodeRequired{passcodeURL: passcodeURL, env: env}
}

func (r *PasscodeRequired) Error() string {
	return "passcode required from: " + r.passcodeURL
}

func (r *PasscodeRequired) GetPasscodeURL() string {
	return r.passcodeURL
}

func (r *PasscodeRequired) Env() Environment {
	return r.env
}

// DiscoverPasscodeURL obtains the validated passcode URL for env. It does not
// inspect other credential sources or exchange any tokens.
func (am *AccountManager) DiscoverPasscodeURL(ctx context.Context, env Environment) (string, error) {
	ea, ok := am.envs[env]
	if !ok {
		return "", fmt.Errorf("unsupported IAM environment %q", env)
	}

	ea.mu.Lock()
	defer ea.mu.Unlock()
	oidcCfg, err := am.getOIDCConfig(ctx, ea)
	if err != nil {
		return "", err
	}
	return validatedIAMEndpoint(oidcCfg.PasscodeEndpoint)
}

// validatedIAMEndpoint rejects malformed OIDC endpoint values before they can
// be used in a request or printed to a terminal. IAM discovery is authoritative
// for the host, so both HTTP schemes remain valid for isolated test endpoints.
func validatedIAMEndpoint(endpoint string) (string, error) {
	return config.ValidateIAMURL(endpoint)
}

// SetPasscode exchanges a passcode for the given env's refresh token and stores
// it. Called from the passcode dialog after GetAuthToken returned a
// *PasscodeRequired for env.
func (am *AccountManager) SetPasscode(ctx context.Context, env Environment, passcode string) error {
	logger := slog.Default().With(logging.KeyComponent, "auth", "event", "iam_passcode_exchange")
	ea, ok := am.envs[env]
	logger.Debug("IAM passcode exchange checkpoint", "stage", "environment_lookup", "found", ok)
	if !ok {
		return fmt.Errorf("unsupported IAM environment %q", env)
	}
	if passcode == "" {
		logger.Debug("IAM passcode exchange checkpoint", "stage", "empty_passcode_rejected")
		return errors.New("passcode is required")
	}

	logger.Debug("IAM passcode exchange checkpoint", "stage", "oidc_config_retrieval_started")
	ea.mu.Lock()
	oidcCfg, err := am.getOIDCConfig(ctx, ea)
	ea.mu.Unlock()
	if err != nil {
		logger.Debug("IAM passcode exchange checkpoint", "stage", "oidc_config_retrieval_failed")
		return err
	}
	logger.Debug("IAM passcode exchange checkpoint", "stage", "oidc_config_retrieval_succeeded")
	tokenEndpoint, err := validatedIAMEndpoint(oidcCfg.TokenEndpoint)
	if err != nil {
		logger.Debug("IAM passcode exchange checkpoint", "stage", "token_endpoint_validation_failed")
		return err
	}
	logger.Debug("IAM passcode exchange checkpoint", "stage", "token_endpoint_validation_succeeded")

	logger.Debug("IAM passcode exchange checkpoint", "stage", "exchange_started")
	resp, err := doTokenFormRequest(ctx, tokenEndpoint, url.Values{
		"grant_type":    {"urn:ibm:params:oauth:grant-type:passcode"},
		"passcode":      {passcode},
		"response_type": {"cloud_iam"},
	}, tokenExchangePasscode)
	if err != nil {
		return err
	}
	if resp.RefreshToken == "" {
		logger.Debug("IAM passcode exchange checkpoint", "stage", "missing_refresh_token")
		return errors.New("IAM passcode exchange returned no refresh token")
	}

	ea.mu.Lock()
	ea.refreshToken = resp.RefreshToken
	ea.mu.Unlock()
	logger.Debug("IAM passcode exchange checkpoint", "stage", "refresh_token_stored")
	return nil
}

// exchangeRefreshToken exchanges the current refresh token for an access token.
// A structured IAM rejection is reported separately from transient failures so
// the credential chain can clear the token and continue to bootstrap sources.
func (am *AccountManager) exchangeRefreshToken(ctx context.Context, ea *envAuth, accountID string) (string, bool, error) {
	oidcCfg, err := am.getOIDCConfig(ctx, ea)
	if err != nil {
		return "", false, err
	}

	resp, err := doTokenFormRequest(ctx, oidcCfg.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {string(ea.refreshToken)},
		"response_type": {"cloud_iam"},
	}, tokenExchangeRefresh)
	if err != nil {
		var iamErr *IAMError
		if errors.As(err, &iamErr) {
			return "", true, nil
		}
		return "", false, fmt.Errorf("refresh-grant exchange failed: %w", err)
	}

	am.storeAccessToken(ea, accountID, resp)
	if resp.RefreshToken != "" {
		ea.refreshToken = resp.RefreshToken
	}
	return string(resp.AccessToken), false, nil
}

// exchangeAPIKey exchanges an API key for an access token via IAM, caches the
// access token under accountID, and persists any returned refresh token.
func (am *AccountManager) exchangeAPIKey(ctx context.Context, ea *envAuth, accountID, apiKey string) (string, error) {
	oidcCfg, err := am.getOIDCConfig(ctx, ea)
	if err != nil {
		return "", err
	}

	resp, err := doTokenFormRequest(ctx, oidcCfg.TokenEndpoint, url.Values{
		"grant_type":    {"urn:ibm:params:oauth:grant-type:apikey"},
		"apikey":        {apiKey},
		"response_type": {"cloud_iam"},
	}, tokenExchangeAPIKey)
	if err != nil {
		return "", err
	}

	am.storeAccessToken(ea, accountID, resp)
	if resp.RefreshToken != "" {
		ea.refreshToken = resp.RefreshToken
	}
	return string(resp.AccessToken), nil
}

// HeadlessAuthRequiredError reports that interactive passcode authentication
// would be required. It is deliberately distinct from PasscodeRequired so
// callers that have no UI never start or advertise a passcode flow.
type HeadlessAuthRequiredError struct {
	Env Environment
}

func (e *HeadlessAuthRequiredError) Error() string {
	return fmt.Sprintf("no noninteractive credentials available for %s", e.Env)
}

// GetAuthToken resolves a bearer token for crn. Priority: cached access token
// -> refresh token -> API key -> 1Password -> passcode.
func (am *AccountManager) GetAuthToken(ctx context.Context, crn *config.CRN) (string, error) {
	return am.getAuthToken(ctx, crn, true)
}

// GetAuthTokenNoPasscode resolves a bearer token without initiating the
// interactive passcode path. It is for blocking, headless callers.
func (am *AccountManager) GetAuthTokenNoPasscode(ctx context.Context, crn *config.CRN) (string, error) {
	return am.getAuthToken(ctx, crn, false)
}

// getAuthToken implements the shared credential chain. allowPasscode retains
// the TUI's passcode behavior while headless callers receive HeadlessAuthRequiredError.
//
//nolint:gocyclo // sequential auth chain; each branch handles a distinct credential path.
func (am *AccountManager) getAuthToken(ctx context.Context, crn *config.CRN, allowPasscode bool) (string, error) {
	if crn == nil {
		return "", errors.New("missing instance CRN")
	}
	env := Environment(crn.CName)
	ea, ok := am.envs[env]
	if !ok {
		return "", fmt.Errorf("unsupported ICL environment %q", crn.CName)
	}
	accountID := crn.ScopeID

	ea.mu.Lock()
	defer ea.mu.Unlock()

	if entry, ok := ea.accessTokens[accountID]; ok && entry.expiresAt.Sub(am.now()) > refreshBuffer {
		return string(entry.token), nil
	}

	if ea.refreshToken != "" {
		token, rejected, err := am.exchangeRefreshToken(ctx, ea, accountID)
		if err != nil {
			return "", err
		}
		if !rejected {
			return token, nil
		}
		ea.refreshToken = ""
	}

	if ea.apiKey != "" {
		token, err := am.exchangeAPIKey(ctx, ea, accountID, string(ea.apiKey))
		if err != nil {
			return "", fmt.Errorf("API key token exchange failed: %w", err)
		}
		return token, nil
	}

	if ea.opRef != "" {
		apiKey, opErr := readOnePasswordRef(ctx, ea.opRef)
		if opErr != nil {
			return "", fmt.Errorf("1Password lookup failed: %w", opErr)
		}
		token, err := am.exchangeAPIKey(ctx, ea, accountID, apiKey)
		if err != nil {
			return "", fmt.Errorf("1Password API key token exchange failed: %w", err)
		}
		ea.apiKey = secret.String(apiKey)
		return token, nil
	}

	if !allowPasscode {
		return "", &HeadlessAuthRequiredError{Env: env}
	}
	oidcCfg, err := am.getOIDCConfig(ctx, ea)
	if err != nil {
		return "", err
	}
	return "", NewPasscodeRequired(oidcCfg.PasscodeEndpoint, env)
}

// SetAPIKey updates the API key for an environment at runtime.
// Used when LOGNAV_IC_API_KEY env var arrives after construction.
func (am *AccountManager) SetAPIKey(env Environment, key string) {
	if ea, ok := am.envs[env]; ok {
		ea.mu.Lock()
		defer ea.mu.Unlock()
		ea.apiKey = secret.String(key)
	}
}

// readOnePasswordRef reads a secret value from 1Password via `op read -n`.
// Validates the op:// prefix to fail fast on typoed refs. op:// is the
// sole 1Password CLI secret-reference scheme.
// It is a package var so tests can stub the external `op` invocation.
var readOnePasswordRef = func(ctx context.Context, opRef string) (string, error) {
	if !strings.HasPrefix(opRef, opRefPrefix) {
		return "", fmt.Errorf("invalid 1password reference %q: must start with %s", opRef, opRefPrefix)
	}
	//nolint:gosec // G204: argv-not-shell; opRef validated above
	cmd := exec.CommandContext(ctx, "op", "read", "-n", opRef)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		exitError := &exec.ExitError{}
		if errors.As(err, &exitError) {
			msg := strings.TrimSpace(stderr.String())
			// `op` prefixes stderr with `[ERROR] <timestamp> ` — strip it for readability.
			if rest, ok := strings.CutPrefix(msg, "[ERROR]"); ok {
				msg = strings.TrimSpace(rest)
				if idx := strings.Index(msg, " "); idx > 0 {
					msg = strings.TrimSpace(msg[idx:])
				}
			}
			return "", fmt.Errorf("failed to read token from 1password: %s", msg)
		}
		return "", fmt.Errorf("failed to read token from 1password: %w", err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// LoadSession reads refresh tokens from a JSON file. Returns empty map if file doesn't exist.
func LoadSession(path string) (map[Environment]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- xdg-resolved session path; not user-influenced
	if err != nil {
		if os.IsNotExist(err) {
			return map[Environment]string{}, nil
		}
		return nil, err
	}

	var tokens map[Environment]string
	if err := jsonutil.API.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

// SaveSession writes refresh tokens to an atomically replaced 0600 JSON file.
// Its containing directory is created or repaired to 0700.
func SaveSession(path string, tokens map[Environment]string) error {
	data, err := jsonutil.API.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: directory requires owner-only execute access.
		return fmt.Errorf("secure session directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary session file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary session file: %w", err)
	}
	if n, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write session file: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write session file: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync session file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("finalize session file: %w", err)
	}
	committed = true
	return nil
}

// GetRefreshTokens returns the current refresh tokens for all environments.
func (am *AccountManager) GetRefreshTokens() map[Environment]string {
	tokens := map[Environment]string{}
	for env, ea := range am.envs {
		ea.mu.Lock()
		if ea.refreshToken != "" {
			tokens[env] = string(ea.refreshToken)
		}
		ea.mu.Unlock()
	}
	return tokens
}

// SetOIDCForTest overrides an env's cached OIDC endpoints. The optional
// passcode endpoint avoids real discovery in command tests; existing callers
// that need only token exchange can omit it.
func (am *AccountManager) SetOIDCForTest(env Environment, tokenEndpoint string, passcodeEndpoint ...string) {
	if ea, ok := am.envs[env]; ok {
		ea.mu.Lock()
		ea.oidcConfig = &oidcConfig{TokenEndpoint: tokenEndpoint}
		if len(passcodeEndpoint) > 0 {
			ea.oidcConfig.PasscodeEndpoint = passcodeEndpoint[0]
		}
		ea.mu.Unlock()
	}
}

// SetRefreshTokens loads persisted refresh tokens into the manager.
func (am *AccountManager) SetRefreshTokens(tokens map[Environment]string) {
	for env, token := range tokens {
		if ea, ok := am.envs[env]; ok {
			ea.mu.Lock()
			ea.refreshToken = secret.String(token)
			ea.mu.Unlock()
		}
	}
}
