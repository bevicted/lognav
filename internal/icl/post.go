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

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
)

// queryHTTPClient serves every ICL data-plane request. It has no overall timeout because
// SSE streams can be long-lived; cancellation comes from the request context.
var queryHTTPClient = &http.Client{}

// errBodyReadLimit caps how much of a non-2xx response body postJSON reads to build the
// error reason. It is deliberately generous: errReasonFromBody parses the body as ICL's
// {"errors":[...]} envelope, and a limit that cuts a valid envelope in half would degrade
// a readable multi-line compile error into a truncated JSON blob.
const errBodyReadLimit = 1 << 16

// httpStatusError is the typed error returned for a non-2xx response. It carries the HTTP
// status so callers can distinguish a genuine 404 from transient failures. Callers wrap
// it with %w, preserving errors.As reachability.
type httpStatusError struct {
	status int
	reason string
}

func (e *httpStatusError) Error() string {
	if e.reason != "" {
		return fmt.Sprintf("http %d: %s", e.status, e.reason)
	}
	return fmt.Sprintf("http %d", e.status)
}

// IsQueryDataRejection reports whether err is a Dataprime syntax or semantic
// rejection (the HTTP statuses ICL uses for submitted-query failures).
func IsQueryDataRejection(err error) bool {
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && (statusErr.status == http.StatusBadRequest || statusErr.status == http.StatusUnprocessableEntity)
}

// IsQueryServiceUnavailable reports only query failures that are reliably
// attributable to authentication, the remote service, or the network. Protocol
// and decoding failures are intentionally not included: callers must surface
// those as general errors rather than claiming the service was unavailable.
func IsQueryServiceUnavailable(err error) bool {
	var authErr *HeadlessAuthRequiredError
	if errors.As(err, &authErr) {
		return true
	}

	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusUnauthorized ||
			statusErr.status == http.StatusForbidden ||
			statusErr.status == http.StatusRequestTimeout ||
			statusErr.status == http.StatusTooManyRequests ||
			statusErr.status >= http.StatusInternalServerError
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// postJSON sends an authenticated JSON POST. On success the caller owns the response body;
// on failure the response body has already been closed.
func postJSON(ctx context.Context, token, fullURL, accept string, body any) (*http.Response, error) {
	payload, err := jsonutil.API.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := doQueryRequest(ctx, http.MethodPost, token, fullURL, accept, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return resp, nil
}

// requestNoBody sends an authenticated request without a body. On success the caller owns
// the response body; on failure the response body has already been closed.
func requestNoBody(ctx context.Context, method, token, fullURL, accept string) (*http.Response, error) {
	resp, err := doQueryRequest(ctx, method, token, fullURL, accept, "", nil)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return resp, nil
}

func doQueryRequest(ctx context.Context, method, token, fullURL, accept, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", lognavUserAgent)

	resp, err := queryHTTPClient.Do(req) // no client timeout; ctx governs
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	if !isHTTPErr(resp.StatusCode) {
		return resp, nil
	}

	// The body carries ICL's verbose reason for the rejection (e.g. the Dataprime compile
	// error) and flows into the TUI-rendered error. It is server-supplied and could carry
	// CRLF / ANSI control bytes from a hostile or buggy endpoint, so errReasonFromBody
	// sanitizes control runes before it is surfaced; the raw body is Debug-logged here for
	// diagnostics.
	snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, errBodyReadLimit))
	closeErr := resp.Body.Close()
	slog.Default().With(logging.KeyComponent, "icl").
		Debug("non-2xx body", "url", fullURL, "status", resp.StatusCode, "body", string(snippet),
			"read_error", readErr, "close_error", closeErr)
	return nil, &httpStatusError{status: resp.StatusCode, reason: errReasonFromBody(snippet)}
}
