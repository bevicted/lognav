package icl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
)

const bgSubmitPath = "/v1/background_query"

func bgStatusPath(queryID string) string {
	return bgSubmitPath + "/" + url.PathEscape(queryID) + "/status"
}

func bgDataPath(queryID string) string {
	return bgSubmitPath + "/" + url.PathEscape(queryID) + "/data"
}

func bgCancelPath(queryID string) string {
	return bgSubmitPath + "/" + url.PathEscape(queryID) + "/cancel"
}

// bgBodyReadLimit caps buffered control-plane responses. These are small JSON
// documents; truncating an oversized response makes its unmarshal fail safely.
const bgBodyReadLimit = 1 << 16

func readBackgroundResponse(resp *http.Response) ([]byte, error) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, bgBodyReadLimit))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close response: %w", closeErr)
	}
	return body, nil
}

func bgPost(ctx context.Context, token, fullURL string, body any) ([]byte, error) {
	resp, err := postJSON(ctx, token, fullURL, "application/json", body)
	if err != nil {
		return nil, fmt.Errorf("background POST: %w", err)
	}
	responseBody, err := readBackgroundResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("background POST: %w", err)
	}
	return responseBody, nil
}

func bgRequestNoBody(ctx context.Context, method, token, fullURL string) ([]byte, error) {
	resp, err := requestNoBody(ctx, method, token, fullURL, "application/json")
	if err != nil {
		return nil, fmt.Errorf("background %s: %w", method, err)
	}
	responseBody, err := readBackgroundResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("background %s: %w", method, err)
	}
	return responseBody, nil
}

// SubmitBackgroundQuery submits a public IBM Cloud Logs background query and returns
// its server query ID. The query carries its own timeframe; start and end provide a
// seven-day fallback window.
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
		return "", fmt.Errorf("submit: %w", err)
	}
	var response backgroundSubmitResponse
	if err := jsonutil.API.Unmarshal(body, &response); err != nil || response.QueryID == "" {
		return "", fmt.Errorf("submit: unexpected response: %s", sanitizeErrBody(body))
	}
	logger.Info("background query submitted", "query_id", response.QueryID)
	return response.QueryID, nil
}

// GetBackgroundQueryStatus returns the decoded public background-query status. HTTP
// 404 is the authoritative expired or unknown signal; other failures remain retryable.
func GetBackgroundQueryStatus(ctx context.Context, token, baseURL, queryID string) (BackgroundStatus, error) {
	ctx, cancel := clampCtx(ctx)
	defer cancel()

	body, err := bgRequestNoBody(ctx, http.MethodGet, token, baseURL+bgStatusPath(queryID))
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
			return BackgroundStatus{State: BackgroundNotFound}, nil
		}
		return BackgroundStatus{}, fmt.Errorf("status: %w", err)
	}

	var wire backgroundStatusWire
	if err := jsonutil.API.Unmarshal(body, &wire); err != nil {
		return BackgroundStatus{}, fmt.Errorf("status: %s", sanitizeErrBody(body))
	}
	status := BackgroundStatus{SubmittedAt: wire.SubmittedAt}
	switch {
	case wire.Terminated != nil && wire.Terminated.Success != nil:
		status.State = BackgroundSuccess
	case wire.Terminated != nil:
		status.State = BackgroundError
	default:
		status.State = BackgroundRunning
	}
	return status, nil
}

// CancelBackgroundQuery cancels a running background query.
func CancelBackgroundQuery(ctx context.Context, token, baseURL, queryID string) error {
	ctx, cancel := clampCtx(ctx)
	defer cancel()
	body, err := bgRequestNoBody(ctx, http.MethodPost, token, baseURL+bgCancelPath(queryID))
	if err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	slog.Default().With(logging.KeyComponent, "icl").Debug("background query cancelled", "response_bytes", len(body))
	return nil
}

// FetchBackgroundData streams a finished background query's public SSE result into the
// same callback contract used by synchronous queries. maxRows limits delivered rows;
// reaching it is successful completion.
func FetchBackgroundData(ctx context.Context, token, baseURL, queryID string, cb QueryCallback, maxRows uint32) error {
	logger := slog.Default().With(logging.KeyComponent, "icl")
	ctx, cancel := clampCtxBackground(ctx)
	defer cancel()

	resp, err := requestNoBody(ctx, http.MethodGet, token, baseURL+bgDataPath(queryID), "text/event-stream")
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	defer resp.Body.Close()

	var rows uint64
	var capped bool
	err = readSSEEvents(ctx, resp.Body, func(data []byte) (bool, error) {
		var envelope backgroundDataEnvelope
		if err := jsonutil.API.Unmarshal(data, &envelope); err != nil {
			return false, fmt.Errorf("decode: %w", err)
		}
		item := envelope.toStreamItem()
		if item == nil || item.Result == nil {
			return true, nil
		}

		if maxRows > 0 {
			remaining := uint64(maxRows) - rows
			if uint64(len(item.Result.Results)) > remaining {
				item.Result.Results = item.Result.Results[:remaining]
			}
		}
		if len(item.Result.Results) > 0 {
			cb.OnData(item)
			rows += uint64(len(item.Result.Results))
		}
		if maxRows > 0 && rows >= uint64(maxRows) {
			capped = true
			return false, nil
		}
		return true, nil
	}, cb.OnKeepAlive)
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if err := ctx.Err(); err != nil && !capped {
		return fmt.Errorf("data: %w", err)
	}

	logger.Info("background data fetched", "query_id", queryID, "rows", rows, "capped", capped)
	cb.OnClose()
	return nil
}
