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

// queryHTTPClient serves every ICL data-plane POST (sync query, background submit /
// status / cancel, background data download). It has NO overall Timeout — an SSE stream
// and a 1M-row NDJSON download are both long-lived — so cancellation comes solely from
// the request context (clampCtx / clampCtxBackground, plus any shorter caller deadline).
var queryHTTPClient = &http.Client{}

// errBodyReadLimit caps how much of a non-2xx response body postJSON reads to build the
// error reason. It is deliberately generous: errReasonFromBody parses the body as ICL's
// {"errors":[...]} envelope, and a limit that cuts a valid envelope in half would degrade
// a readable multi-line compile error into a truncated JSON blob.
const errBodyReadLimit = 1 << 16

// httpStatusError is the typed error postJSON returns for a non-2xx response. It carries
// the HTTP status so callers can distinguish a genuine 404 (not-found/expired) from a
// transient 4xx/5xx WITHOUT substring-matching the reason text. Callers wrap it with %w,
// which keeps both their user-facing prefix and errors.As reachability.
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

// postJSON is the single JSON POST path of the ICL client: marshal -> request -> standard
// headers (auth included) -> status check -> error body. accept selects the response
// encoding the caller wants (text/event-stream, application/json, application/x-ndjson).
//
// Body ownership: on success the response is returned with its body still OPEN and the
// CALLER must close it. On every failure — marshal, request build, transport, non-2xx —
// the returned response is nil and postJSON has already closed the body itself (it reads
// the body to build the error reason). Callers therefore neither leak a body nor
// double-close one; the shape is always
//
//	resp, err := postJSON(...)
//	if err != nil { return err }
//	defer resp.Body.Close()
func postJSON(ctx context.Context, token, fullURL, accept string, body any) (*http.Response, error) {
	payload, err := jsonutil.API.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", lognavUserAgent)

	resp, err := queryHTTPClient.Do(req) // no client Timeout; ctx governs
	if err != nil {
		return nil, err
	}
	if !isHTTPErr(resp.StatusCode) {
		return resp, nil
	}

	// The body carries ICL's verbose reason for the rejection (e.g. the Dataprime compile
	// error) and flows into the TUI-rendered error. It is server-supplied and could carry
	// CRLF / ANSI control bytes from a hostile or buggy endpoint, so errReasonFromBody
	// sanitizes control runes before it is surfaced; the raw body is Debug-logged here for
	// diagnostics.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyReadLimit))
	_ = resp.Body.Close()
	slog.Default().With(logging.KeyComponent, "icl").
		Debug("non-2xx body", "url", fullURL, "status", resp.StatusCode, "body", string(snippet))
	return nil, &httpStatusError{status: resp.StatusCode, reason: errReasonFromBody(snippet)}
}
