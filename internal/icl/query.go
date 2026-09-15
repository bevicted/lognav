package icl

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
)

// queryBody is the JSON request body for POST /v1/query.
type queryBody struct {
	Query    string        `json:"query"`
	Metadata QueryMetadata `json:"metadata"`
}

// Query streams Dataprime results for query to cb. The caller's ctx is wrapped
// with a 5-minute cap (clampCtx); a shorter caller deadline still applies.
//
// Error contract (matches the retired SDK + the StartQuery caller in
// instancepicker, which does `if err := icl.Query(...); err != nil { cb.OnError(err) }`):
//   - Setup failures (marshal, request build, transport, non-2xx) are RETURNED
//     and NOT delivered to cb here — the caller surfaces them via cb.OnError.
//   - Stream-phase receive/parse errors are delivered via cb.OnError (inside
//     streamEvents); Query then returns nil.
//   - cb.OnClose fires exactly once, after the stream loop, on the streaming
//     path only. (On a returned setup error, the caller's cb.OnError carries
//     the terminal signal; streamCallback dedups via sync.Once regardless.)
func Query(ctx context.Context, token, url, query string, maxRows uint32, cb QueryCallback) error {
	limit := SyncQueryRequestLimit(maxRows)
	logger := slog.Default().With(logging.KeyComponent, "icl")
	start := time.Now()
	logger.Info("query start", "url", url, "query", query, "limit", limit)
	defer func() {
		logger.Info("query done", logging.KeyDurationMS, time.Since(start).Milliseconds())
	}()

	ctx, cancel := clampCtx(ctx)
	defer cancel()

	now := time.Now()
	// The "query: " prefix is the user-facing one the TUI error frame shows; %w keeps
	// postJSON's *httpStatusError (and any transport error) reachable underneath it.
	resp, err := postJSON(ctx, token, url+"/v1/query", "text/event-stream", queryBody{
		Query: query,
		Metadata: QueryMetadata{
			DefaultSource: "logs",
			StartDate:     formatICLTime(now.Add(-1 * time.Hour)),
			EndDate:       formatICLTime(now),
			Syntax:        "dataprime",
			Tier:          "archive",
			Limit:         int64(limit),
		},
	})
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer resp.Body.Close()

	streamEvents(ctx, resp.Body, cb)
	cb.OnClose()
	return nil
}

// errReasonFromBody extracts a human-readable reason from an ICL non-2xx body.
// The body uses the same envelope as a stream error item ({"errors":[...]}), so
// decode it and reuse DecodeStreamItem's formatting — that pulls the verbose
// message out of the JSON and turns its escaped "\n" into real newlines, so the
// error renders multi-line (like an in-stream error) instead of a single
// truncated JSON blob. Falls back to the sanitized raw body when the payload is
// not the expected shape. Either path is sanitized against CRLF / ANSI injection.
func errReasonFromBody(body []byte) string {
	var item StreamItem
	if err := jsonutil.API.Unmarshal(body, &item); err == nil {
		if _, _, errs := DecodeStreamItem(&item); len(errs) > 0 {
			return sanitizeErrBody([]byte(strings.Join(errs, "\n\n")))
		}
	}
	return sanitizeErrBody(body)
}

// sanitizeErrBody trims a server-supplied error body and strips control runes
// (keeping newlines and tabs) so a hostile or buggy ICL endpoint cannot inject
// CRLF / ANSI escape sequences into the TUI-rendered error. Returns "" when
// nothing printable remains.
func sanitizeErrBody(b []byte) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, string(b))
	return strings.TrimSpace(cleaned)
}
