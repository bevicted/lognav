package icl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
)

const (
	bgSubmitPath = "/api/v1/dataprime/background-query"
	bgStatusPath = "/api/v1/dataprime/background-query/status"
	bgDataPath   = "/api/v1/dataprime/background-query/data"
	bgCancelPath = "/api/v1/dataprime/background-query/cancel"
)

// bgBodyReadLimit caps the 2xx control-plane body bgPost buffers. These responses are
// small JSON documents; the cap is defensive only, and a truncated read is left for the
// caller's unmarshal to reject (same as before this was a shared helper).
const bgBodyReadLimit = 1 << 16

// bgPost runs a one-shot JSON POST to a background endpoint and returns the whole 2xx
// body. The caller decides how to interpret it.
//
// Errors come back from postJSON UNPREFIXED and unwrapped: unlike Query and
// FetchBackgroundData, the background control-plane callers (Submit/Status/Cancel)
// surface icl errors without a prefix, so adding one here would change user-visible text
// for no gain. The *httpStatusError is therefore reachable both directly and, for callers
// that do wrap, through %w — GetBackgroundQueryStatus's 404 check uses errors.As either
// way.
func bgPost(ctx context.Context, token, fullURL string, body any) ([]byte, error) {
	resp, err := postJSON(ctx, token, fullURL, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, bgBodyReadLimit))
	return b, nil
}

// SubmitBackgroundQuery POSTs a background query and returns its server queryId.
// query carries its own timeframe; the start/end fallback window is now-7d..now.
func SubmitBackgroundQuery(ctx context.Context, token, baseURL, query string) (string, error) {
	logger := slog.Default().With(logging.KeyComponent, "icl")
	ctx, cancel := clampCtx(ctx)
	defer cancel()
	now := time.Now()
	body, err := bgPost(ctx, token, baseURL+bgSubmitPath, backgroundSubmitBody{
		Query:     query,
		Syntax:    backgroundSyntaxDataprime,
		StartDate: formatICLTime(now.Add(-7 * 24 * time.Hour)),
		EndDate:   formatICLTime(now),
	})
	if err != nil {
		return "", err
	}
	var resp backgroundSubmitResponse
	if err := jsonutil.API.Unmarshal(body, &resp); err != nil || resp.QueryID == "" {
		return "", fmt.Errorf("submit: unexpected response: %s", sanitizeErrBody(body))
	}
	logger.Info("background query submitted", "query_id", resp.QueryID)
	return resp.QueryID, nil
}

// isNotFoundBody detects the server's not-found/expired sentinel text on a 2xx status body
// ("Query not found", "does not exist"). The error (non-2xx) path keys off the HTTP 404
// status code instead (see GetBackgroundQueryStatus): substring-scanning a non-2xx reason
// would prematurely freeze a still-running query whose transient 4xx/5xx body merely mentions the
// phrase.
func isNotFoundBody(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "not found") || strings.Contains(l, "does not exist")
}

// GetBackgroundQueryStatus returns the decoded status. A not-found body maps to
// BackgroundNotFound (the authoritative expiry signal).
func GetBackgroundQueryStatus(ctx context.Context, token, baseURL, queryID string) (BackgroundStatus, error) {
	ctx, cancel := clampCtx(ctx)
	defer cancel()
	body, err := bgPost(ctx, token, baseURL+bgStatusPath, map[string]string{"queryId": queryID})
	if err != nil {
		// A genuine HTTP 404 is the authoritative not-found/expiry signal. Any other
		// non-2xx (or transport error) is transient and must propagate as an error —
		// freezing a still-running query to StateExpired just because a 5xx body happens
		// to contain "not found" would kill a query that is still fetchable.
		var he *httpStatusError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			return BackgroundStatus{State: BackgroundNotFound}, nil
		}
		return BackgroundStatus{}, err
	}
	if isNotFoundBody(string(body)) {
		return BackgroundStatus{State: BackgroundNotFound}, nil
	}
	var w backgroundStatusWire
	if err := jsonutil.API.Unmarshal(body, &w); err != nil {
		return BackgroundStatus{}, fmt.Errorf("status: %s", sanitizeErrBody(body))
	}
	st := BackgroundStatus{SubmittedAt: w.SubmittedAt}
	switch {
	case w.Terminated != nil && w.Terminated.Success != nil:
		st.State = BackgroundSuccess
	case w.Terminated != nil: // cancelled or any other non-success terminal
		st.State = BackgroundError
	default:
		st.State = BackgroundRunning
	}
	return st, nil
}

// CancelBackgroundQuery cancels a running background query (data is then deleted server-side).
func CancelBackgroundQuery(ctx context.Context, token, baseURL, queryID string) error {
	ctx, cancel := clampCtx(ctx)
	defer cancel()
	_, err := bgPost(ctx, token, baseURL+bgCancelPath, map[string]string{"queryId": queryID})
	return err
}

// FetchBackgroundData streams a finished background query's NDJSON result to cb, reusing
// the QueryCallback contract (the same callback the sync /v1/query path uses). Each NDJSON
// line is one {response:{results:{results:[...]}}} batch; it is decoded, mapped into a
// StreamItem, and delivered via cb.OnData so the unchanged DecodeStreamItem consumes it.
//
// maxRows caps the number of delivered rows (0 = unbounded, bounded only by the ICL server
// ~1M cap). When the cap is reached, decoding stops and cb.OnClose fires (success path) —
// a capped fetch is NOT an error. Do NOT cancel the ctx to bound rows; ctx-cancel makes
// dec.Decode return an error, skips OnClose, and turns a capped success into a stream error.
//
// The cap is enforced before each cb.OnData call. A final oversized batch is trimmed to the
// remaining rows, so callers never receive more than maxRows rows.
//
// Decoder: encoding/json.NewDecoder. bufio.Scanner is unsuitable because batches can
// exceed its 64KB token limit.
//
// Error contract mirrors Query: setup/non-2xx failures are RETURNED (caller does
// cb.OnError); a non-success-terminal /data returns a plain error string (not NDJSON) which
// surfaces either as a non-2xx (returned) or as a decode error (returned). cb.OnClose fires
// exactly once at the end of the success path — mirroring Query, which self-closes — so the
// caller stays symmetric with the sync branch (`if err := ...; err != nil { cb.OnError(err) }`)
// and never has to close itself. On a returned error OnClose is skipped (the early returns
// fire before it) and the caller's cb.OnError carries the terminal signal; streamCallback
// dedups via sync.Once regardless.
func FetchBackgroundData(ctx context.Context, token, baseURL, queryID string, cb QueryCallback, maxRows uint32) error {
	logger := slog.Default().With(logging.KeyComponent, "icl")
	ctx, cancel := clampCtxBackground(ctx)
	defer cancel()

	// The "data: " prefix is the user-facing one (a 404 here is the expired-result
	// message the Archive tab shows); %w keeps postJSON's *httpStatusError underneath.
	resp, err := postJSON(ctx, token, baseURL+bgDataPath, "application/x-ndjson", map[string]string{"queryId": queryID})
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	defer resp.Body.Close()

	// Streaming decode uses encoding/json. The optimized jsonutil API decodes
	// complete byte slices rather than an io.Reader.
	dec := json.NewDecoder(resp.Body)
	var rows uint64
	capped := false
	for dec.More() {
		var env backgroundDataEnvelope
		if err := dec.Decode(&env); err != nil {
			// A non-success terminal returns a plain error STRING, not NDJSON; decode fails
			// here and we surface it as an error (caller -> cb.OnError -> error frame), never
			// silently emitting zero rows.
			return fmt.Errorf("data: decode: %w", err)
		}
		if item := env.toStreamItem(); item != nil {
			if maxRows > 0 && item.Result != nil {
				remaining := uint64(maxRows) - rows
				if uint64(len(item.Result.Results)) > remaining {
					item.Result.Results = item.Result.Results[:remaining]
				}
			}
			if item.Result != nil && len(item.Result.Results) > 0 {
				cb.OnData(item)
				rows += uint64(len(item.Result.Results))
			}
		}
		if maxRows > 0 && rows >= uint64(maxRows) {
			capped = true
			break // capped: fall through to cb.OnClose() so this is a SUCCESS, not an error
		}
	}
	logger.Info("background data fetched", "query_id", queryID, "rows", rows, "capped", capped)
	cb.OnClose() // success path only; the early returns above fire before this on any error
	return nil
}
