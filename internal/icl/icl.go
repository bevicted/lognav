package icl

import (
	"context"
	"fmt"
	"time"

	"github.com/bevicted/lognav/internal/config"
)

// SyncQueryLimit is the default row cap for synchronous /v1/query requests.
const SyncQueryLimit = 50000

// SyncQueryRequestLimit maps zero to the default synchronous request limit and
// preserves every positive configured value for ICL to validate.
func SyncQueryRequestLimit(maxRows uint32) uint32 {
	if maxRows == 0 {
		return SyncQueryLimit
	}
	return maxRows
}

// BackgroundQueryMaxRows is the ICL server-side row cap for a finished background
// (archive) query result. Used as the store pre-size bound when Logs.MaxRows==0
// (unbounded), since a result can never exceed it.
const BackgroundQueryMaxRows = 1_000_000

// queryTimeout is a defense-in-depth cap on a single ICL Query invocation.
// The caller's ctx still cancels earlier if it has a shorter deadline.
const queryTimeout = 5 * time.Minute

// clampCtx wraps ctx with a queryTimeout-bounded deadline. The returned
// cancel must always be called to release the underlying timer (defer
// cancel() at the call site).
func clampCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, queryTimeout)
}

// backgroundDataTimeout bounds a single FetchBackgroundData download. The server caps
// background execution at 30 min and a finished result can be large (up to the 1M server
// row cap), so the 5-min queryTimeout used by Query is too tight here. The caller's ctx
// still cancels earlier (Toggle/CancelQuery/cancelAllFetches).
const backgroundDataTimeout = 30 * time.Minute

// clampCtxBackground wraps ctx with the background-data deadline.
func clampCtxBackground(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, backgroundDataTimeout)
}

// GetURLFromCRN builds the ICL service URL for an instance from its CRN.
func GetURLFromCRN(crn *config.CRN) string {
	return fmt.Sprintf("https://%s.api.%s.logs.cloud.ibm.com", crn.InstanceID, crn.Location)
}
