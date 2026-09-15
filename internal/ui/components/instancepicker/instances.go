package instancepicker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/components/logviewer"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
	uv "github.com/charmbracelet/ultraviolet"
)

// CursorState holds logviewer cursor position for persistence across evictions.
type CursorState struct {
	X       int
	Y       int
	Log     int
	LogLine int
	XOffset int
}

// querySource selects how an instance's stream is produced after auth resolves.
// The zero value (empty queryID) is the sync default: StartQuery runs icl.Query
// (a live SSE fetch). A non-empty queryID routes StartQuery to
// icl.FetchBackgroundData, streaming a finished background (archive) query's data
// through the same QueryCallback contract. Seeded by collect (startCollect) and
// reset to the zero value by startFetch via Instances.ClearSources so a normal
// fetch never inherits a stale collect queryId.
type querySource struct {
	queryID string
}

type Instance struct {
	Name            string
	Store           *logviewer.LogStore
	url             string
	CRN             string
	env             icl.Environment
	state           status.Phase
	cancelQuery     context.CancelFunc
	startTime       time.Time
	lastUpdateTime  time.Time
	logger          *slog.Logger
	flushedLogCount int                        // log count preserved after store flush to disk
	flushedLogsSize uint64                     // native NDJSON byte count preserved after store flush to disk
	savedCursor     CursorState                // cursor position preserved across store evictions
	timerFormat     string                     // ElapsedFetchTimeFormat from config, captured at construction
	source          querySource                // stream source: empty => sync icl.Query; non-empty queryID => icl.FetchBackgroundData (collect)
	readOnly        *snapshot.InstanceSnapshot // non-nil for a snapshot-only runtime row; preserves serialized metadata
}

// NewInstance constructs an Instance and its backing LogStore. The store is
// created without a width source — the logviewer binds one via SetWidthSource
// when the store is adopted (see logviewer.SetStore).
func NewInstance(bundle deps.Bundle, name, url, crn string, env icl.Environment, timerFormat string) *Instance {
	inst := &Instance{
		Name:        name,
		url:         url,
		CRN:         crn,
		env:         env,
		timerFormat: timerFormat,
		Store:       logviewer.NewLogStoreWithIdentity(bundle, nil, crn, name),
		logger:      slog.Default().With(logging.KeyComponent, "instancepicker", logging.KeyInstance, name),
	}
	return inst
}

// NewReadOnlyInstance constructs a transient row for a snapshot member absent
// from effective config. Its saved metadata is retained separately because the
// runtime ReadOnly phase must never replace what a later manual save writes.
func NewReadOnlyInstance(bundle deps.Bundle, snap snapshot.InstanceSnapshot) (*Instance, error) {
	crn, err := config.CRNFromString(snap.CRN)
	if err != nil {
		return nil, fmt.Errorf("parse snapshot instance CRN: %w", err)
	}
	name := config.DisplayNameForCRN(config.EffectiveInstances(bundle.Config), crn)
	inst := NewInstance(bundle, name, icl.GetURLFromCRN(crn), snap.CRN, icl.Environment(crn.CName), bundle.Config.Style.ElapsedFetchTimeFormat)
	inst.readOnly = &snap
	inst.state = status.ReadOnly
	inst.startTime = time.UnixMicro(snap.StartTimeMicro)
	inst.lastUpdateTime = time.UnixMicro(snap.LastUpdateTimeMicro)
	inst.flushedLogCount = snap.LogCount
	inst.flushedLogsSize = snap.LogsSizeBytes
	inst.Store.SetMessage(snap.Message)
	return inst, nil
}

// handleLogStreamMsg folds one streamed batch into the instance. Contextual
// status reads this live state directly from the picker on the next draw.
func (i *Instance) handleLogStreamMsg(msg *msgs.LogStreamMsg) {
	if i.state == status.Cancelled {
		return
	}

	for _, err := range msg.Errs {
		i.logger.Error("stream chunk error", logging.KeyError, err)
	}
	for _, wrn := range msg.Warns {
		i.logger.Warn("stream chunk warning", "warning", wrn)
	}

	if len(msg.Errs) > 0 {
		i.state = status.Error
	} else if len(msg.Warns) > 0 && i.state != status.Error {
		i.state = status.Warning
	}

	// HandleLogStreamMsg spawns its jq/search chunk workers via the poster.
	i.Store.HandleLogStreamMsg(msg)
}

// handleLogStreamDoneMsg settles the instance's terminal state when its stream
// ends. The frozen timer is read by the contextual provider on redraw.
func (i *Instance) handleLogStreamDoneMsg(msg *msgs.LogStreamDoneMsg) {
	if i.state == status.Cancelled {
		return
	}

	for _, err := range msg.Errs {
		i.logger.Error("stream done error", logging.KeyError, err)
	}

	// Freeze the elapsed timer at the true final value. With the per-instance
	// tick removed, nothing else advances lastUpdateTime; capturing it here (on
	// both the error and success transitions) makes the frozen RenderTimer show
	// the real total fetch duration.
	i.lastUpdateTime = time.Now()

	switch {
	case len(msg.Errs) > 0:
		i.state = status.Error
	case i.state == status.InProgress:
		i.state = status.Success
	}

	// HandleLogStreamDoneMsg dispatches remaining batches via the poster.
	i.Store.HandleLogStreamDoneMsg(msg)
}

func (r *Instance) Toggle() {
	if r.readOnly != nil {
		return
	}
	oldState := r.state
	switch r.state {
	case status.Disabled, status.Cancelled:
		r.state = status.Enabled
	case status.AuthInProgress:
		// Auth is running but no query has started (no cancelQuery yet); freeze
		// the timer and demote to Cancelled. A late MemberAuthResolvedMsg is a
		// no-op because OnMemberAuthResolved skips Cancelled instances.
		r.lastUpdateTime = time.Now()
		r.state = status.Cancelled
	case status.InProgress:
		r.CancelQuery()
	default:
		r.state = status.Disabled
	}
	r.logger.Debug("instance toggled", logging.KeyInstance, r.Name, "from", oldState, "to", r.state)
}

// StartQuery starts the ICL query for this instance on a tracked poster
// goroutine. The on-loop prologue (the per-query cancel handle, StartStream's
// queryID bump, and the stream callback bound to that queryID) runs
// synchronously on the loop goroutine before the goroutine spawns, preserving
// the single-writer and epoch invariants. Only icl.Query / FetchBackgroundData
// runs off-loop. The per-query ctx (from context.WithCancel(context.Background()))
// governs the query lifetime so Toggle -> CancelQuery still cancels exactly as
// before; the poster-supplied ctx is intentionally unused here.
//
// maxRows controls the normal synchronous request and store ceiling. A zero
// value requests the default 50,000 rows; an oversized value leaves the local
// store at that ceiling. Collect ignores maxRows and uses its fixed 50,000-row
// fetch/store ceiling.
func (r *Instance) StartQuery(token, query string, maxRows uint32, send func(uv.Event), poster msgs.Poster) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelQuery = cancel
	src := r.source // snapshot on-loop; the off-loop body must not read the mutable field
	limit := icl.SyncQueryRequestLimit(maxRows)
	capacity := int(icl.SyncQueryLimit)
	if src.queryID == "" && limit < icl.SyncQueryLimit {
		capacity = int(limit)
	}
	r.Store.StartStream(capacity)
	cb := msgs.NewStreamCallback(r.CRN, r.Name, r.Store.GetQueryID(), send)
	poster.Go(func(context.Context) {
		var qerr error
		if src.queryID == "" {
			qerr = icl.Query(ctx, token, r.url, query, limit, cb)
		} else {
			// Collect: stream the finished background query's NDJSON data through the
			// same QueryCallback the sync path uses (FetchBackgroundData -> cb.OnData).
			// Both icl.Query and FetchBackgroundData self-close on their success path, so
			// this branch stays symmetric with the sync one: only surface a returned error.
			qerr = icl.FetchBackgroundData(ctx, token, r.url, src.queryID, cb, icl.SyncQueryLimit)
		}
		if qerr != nil {
			cb.OnError(qerr)
		}
		cancel()
	})
}

// CancelQuery cancels any in-flight ICL query goroutine and transitions
// the instance state to Cancelled if it was InProgress. Nils r.cancelQuery
// after invoking it so the cancel is idempotent at the field level. On the
// InProgress->Cancelled transition it freezes the elapsed timer at the
// time-until-cancel (RenderTimer's frozen branch reads lastUpdateTime-startTime);
// without this a cancelled instance would render "0.00" since the LogStreamDoneMsg
// path early-returns on Cancelled and never sets lastUpdateTime.
func (r *Instance) CancelQuery() {
	if r.cancelQuery != nil {
		r.cancelQuery()
		r.cancelQuery = nil
		if r.state == status.InProgress {
			r.lastUpdateTime = time.Now()
			r.state = status.Cancelled
		}
	}
}

// Close cancels the instance's in-flight ICL query (if any) and closes its
// backing LogStore. After the first call, i.cancelQuery is nil so subsequent
// Close calls only re-invoke Store.Close (also idempotent). Returns the
// store's Close error; CancelQuery never errors.
func (i *Instance) Close() error {
	i.CancelQuery()
	if i.Store != nil {
		return i.Store.Close()
	}
	return nil
}

func (r *Instance) ResetTimer() {
	r.startTime = time.Time{}
	r.lastUpdateTime = time.Time{}
}

// StartTimer marks the instance InProgress and records the fetch start. The
// elapsed value is rendered live by RenderTimer; redraws are driven by the
// shared spine ticker, so StartTimer no longer schedules a per-instance tick.
func (r *Instance) StartTimer() {
	r.startTime = time.Now()
	r.lastUpdateTime = r.startTime
	r.state = status.InProgress
}

// StartAuthTimer marks the instance AuthInProgress and starts the auth-run
// elapsed clock. The auth run is distinct from the fetch run: when the query
// later starts, StartTimer resets the clock so the fetch run counts from zero.
// Redraws are driven by the shared spine ticker (gated on AreAllQueriesDone,
// which now counts AuthInProgress), so this schedules no per-instance tick.
func (r *Instance) StartAuthTimer() {
	r.startTime = time.Now()
	r.lastUpdateTime = r.startTime
	r.state = status.AuthInProgress
}

// FinishDispatch flips a dispatching instance to a terminal badge and freezes its
// timer: Success on a clean background-query submit, Error on a resolver/submit
// failure. Mirrors handleLogStreamDoneMsg's freeze (lastUpdateTime = now), but for
// the dispatch path, which submits a background query instead of streaming logs.
//
// A Cancelled instance is left untouched: cancelAllFetches froze the row mid-dispatch, so a
// late DispatchInstanceDoneMsg for an already-cancelled submit must NOT resurrect it to a
// terminal Success/Error badge (mirrors the handleLogStreamDoneMsg cancel guard).
func (r *Instance) FinishDispatch(ok bool) {
	if r.state == status.Cancelled {
		return
	}
	r.lastUpdateTime = time.Now()
	if ok {
		r.state = status.Success
	} else {
		r.state = status.Error
	}
}

// StartDispatchTimers flips every ENABLED instance to AuthInProgress and starts
// its auth-run clock, mirroring ResolveTokens' on-loop prologue but WITHOUT
// clearing stores or resetting timers — dispatch must not disturb the displayed
// logs. Used by dispatchArchive so a dispatch shows the same per-instance
// padlock/spinner + live timer as a fetch in flight. Returns the enabled names in
// instance order so the caller can drive the off-loop submit fan-out over the
// exact set it flipped.
func (i Instances) StartDispatchTimers() []string {
	var names []string
	for _, instance := range i {
		if instance.IsEnabled() {
			instance.StartAuthTimer() // AuthInProgress + auth-run clock
			names = append(names, instance.Name)
		}
	}
	return names
}

// RenderTimer renders the instance's elapsed time. While the instance is
// InProgress or AuthInProgress AND startTime has been set, it is live
// (time.Since(startTime)); the shared spine ticker (runtime) drives the redraws
// that advance the live value. Otherwise it is frozen at the final elapsed
// captured in lastUpdateTime (set in handleLogStreamDoneMsg). ResolveTokens now
// records a real startTime up front (StartAuthTimer flips the instance to
// AuthInProgress AND stamps startTime), so the live branch fires immediately for
// the auth run. The !startTime.IsZero() guard is therefore defensive: it covers
// any transient window where state is InProgress/AuthInProgress but startTime is
// still the zero value (e.g. a directly-mutated instance in a test), where a live
// time.Since(zero) would saturate to math.MaxInt64 ns and flash garbage; the
// frozen formula (zero.Sub(zero)) renders zero elapsed instead.
func (r *Instance) RenderTimer() string {
	if (r.state == status.InProgress || r.state == status.AuthInProgress) && !r.startTime.IsZero() {
		return FormatElapsed(r.timerFormat, time.Since(r.startTime))
	}
	return FormatElapsed(r.timerFormat, r.lastUpdateTime.Sub(r.startTime))
}

// FormatElapsed renders an elapsed duration as seconds using the given
// fetch-time format (Styles.ElapsedFetchTimeFormat). Shared by the live picker
// timer (RenderTimer) and the snapshot preview so both honor the user's
// configured format and never drift.
func FormatElapsed(format string, d time.Duration) string {
	return fmt.Sprintf(format, d.Seconds())
}

// DisplayLogCount returns the log count for display, falling back to
// flushedLogCount when the store has been cleared after a disk flush.
func (i *Instance) DisplayLogCount() int {
	if c := i.Store.GetLogCount(); c > 0 {
		return c
	}
	return i.flushedLogCount
}

// ClearStore empties the instance's log store AND drops the cursor position
// saved for it. The two must always move together: savedCursor is a row index
// into the logs being discarded, and the root restores it verbatim after a lazy
// reload (SetLogsRaw + SetCursor). Left behind, a re-fetch reopens the instance
// at a row belonging to the PREVIOUS query, so search jumps (n/N) scan forward
// from a meaningless offset. Every caller that empties a store is replacing
// (fetch, watch round, retry, snapshot restore) or dropping (snapshot eviction)
// those rows, so they all go through here rather than Store.Clear() directly.
func (i *Instance) ClearStore() {
	i.Store.Clear()
	i.savedCursor = CursorState{}
}

func (i *Instance) SaveCursor(x, y, log, logLine, xOffset int) {
	i.savedCursor = CursorState{X: x, Y: y, Log: log, LogLine: logLine, XOffset: xOffset}
}

func (i *Instance) GetSavedCursor() CursorState {
	return i.savedCursor
}

func (i *Instance) SnapshotizeMeta() snapshot.InstanceSnapshot {
	if i.readOnly != nil {
		return *i.readOnly
	}
	return snapshot.InstanceSnapshot{
		CRN:                 i.CRN,
		State:               int(i.state),
		LogCount:            i.DisplayLogCount(),
		LogsSizeBytes:       i.flushedLogsSize,
		StartTimeMicro:      i.startTime.UnixMicro(),
		LastUpdateTimeMicro: i.lastUpdateTime.UnixMicro(),
		Message:             i.Store.GetMessage(),
	}
}

// RestoreSnapshot restores instance metadata from a snapshot.
// Log data is not loaded here; it is deferred to lazy loading when the user views the instance.
func (i *Instance) RestoreSnapshot(s snapshot.InstanceSnapshot) {
	i.ClearStore()
	i.state = status.Phase(s.State)
	// No query is running after a snapshot restore; clear any transient
	// querying/auth state so AreAllQueriesDone() doesn't block subsequent fetches.
	if i.state == status.InProgress || i.state == status.AuthInProgress || i.state == status.Watching {
		i.state = status.Enabled
	}
	i.startTime = time.UnixMicro(s.StartTimeMicro)
	i.lastUpdateTime = time.UnixMicro(s.LastUpdateTimeMicro)
	i.flushedLogCount = s.LogCount
	i.flushedLogsSize = s.LogsSizeBytes
	// Restore the fetch-error text into the (just-cleared) store so reopening a
	// restored errored instance shows why it failed. Only displayed by the
	// logviewer when the store has no logs, matching the live error path.
	i.Store.SetMessage(s.Message)
}

func (r *Instance) IsEnabled() bool {
	return r.readOnly == nil && r.state != status.Disabled && r.state != status.Cancelled
}

func (r *Instance) Disable() {
	if r.readOnly == nil {
		r.state = status.Disabled
	}
}

func (r *Instance) Enable() {
	if r.readOnly == nil {
		r.state = status.Enabled
	}
}

func (r *Instance) IsReadOnly() bool { return r.readOnly != nil }

// RestoreReadOnlyMessage reapplies the saved zero-log message after a lazy
// load clears the store's transient display state.
func (r *Instance) RestoreReadOnlyMessage() {
	if r.readOnly != nil {
		r.Store.SetMessage(r.readOnly.Message)
	}
}

// ResetSnapshotDisplay clears a configured row before snapshot metadata is
// applied. A snapshot that omits the configured CRN therefore cannot leave
// stale logs, messages, counts, or timers from a prior session.
func (r *Instance) ResetSnapshotDisplay() {
	r.CancelQuery()
	r.ClearStore()
	r.flushedLogCount = 0
	r.flushedLogsSize = 0
	r.ResetTimer()
	r.state = status.Disabled
}

type Instances []*Instance

// FindByCRN returns the instance with the given full CRN, or nil if not found.
func (i Instances) FindByCRN(crn string) *Instance {
	for _, inst := range i {
		if inst.CRN == crn {
			return inst
		}
	}
	return nil
}

// FindByName resolves a display name for cosmetic callers. Runtime routing must
// use FindByCRN.
func (i Instances) FindByName(name string) *Instance {
	for _, inst := range i {
		if inst.Name == name {
			return inst
		}
	}
	return nil
}

// NewInstances constructs the Instances slice from the shared effective
// instance configuration.
func NewInstances(bundle deps.Bundle) Instances {
	return newInstances(bundle, config.EffectiveInstances(bundle.Config))
}

func newInstances(bundle deps.Bundle, configured []config.ICLInstanceConfig) Instances {
	timerFormat := bundle.Config.Style.ElapsedFetchTimeFormat
	if len(configured) == 0 {
		return nil
	}

	instances := make(Instances, 0, len(configured))
	for _, i := range configured {
		instances = append(instances, NewInstance(bundle, i.Name, icl.GetURLFromCRN(i.CRN), i.CRN.String(), icl.Environment(i.CRN.CName), timerFormat))
	}

	return instances
}

// RestoreSnapshots resets every configured row, then restores metadata by CRN.
// Snapshot-only rows are constructed by Model, which owns the configured slice
// and the list lifecycle.
func (i Instances) RestoreSnapshots(snaps []snapshot.InstanceSnapshot) {
	byCRN := make(map[string]snapshot.InstanceSnapshot, len(snaps))
	for _, snap := range snaps {
		byCRN[snap.CRN] = snap
	}
	for _, instance := range i {
		if instance.IsReadOnly() {
			continue
		}
		instance.ResetSnapshotDisplay()
		if snap, ok := byCRN[instance.CRN]; ok {
			instance.RestoreSnapshot(snap)
		}
	}
}

func (i Instances) AreAllQueriesDone() bool {
	for _, instance := range i {
		if instance.state == status.InProgress || instance.state == status.AuthInProgress || instance.state == status.Watching {
			return false
		}
	}
	return true
}

// HasAnyLogs reports whether any instance currently holds logs (a live store or
// a flushed count via DisplayLogCount). Used to suppress empty auto-snapshots: a
// fetch or watch that returned nothing for every instance has nothing worth
// persisting.
func (i Instances) HasAnyLogs() bool {
	for _, instance := range i {
		if instance.DisplayLogCount() > 0 {
			return true
		}
	}
	return false
}

func (i Instances) AreAllEnabled() bool {
	for _, instance := range i {
		if !instance.IsReadOnly() && !instance.IsEnabled() {
			return false
		}
	}
	return true
}

// Configured returns the mutable configured rows, preserving their order.
func (i Instances) Configured() Instances {
	configured := make(Instances, 0, len(i))
	for _, instance := range i {
		if !instance.IsReadOnly() {
			configured = append(configured, instance)
		}
	}
	return configured
}

func (i Instances) EnableCRNs(crns []string) {
	for _, instance := range i {
		if slices.Contains(crns, instance.CRN) {
			instance.Enable()
			continue
		}
		instance.Disable()
	}
}

// EnabledCRNs returns the full CRNs of every currently-enabled instance, in
// slice order.
func (i Instances) EnabledCRNs() []string {
	crns := make([]string, 0, len(i))
	for _, instance := range i {
		if instance.IsEnabled() {
			crns = append(crns, instance.CRN)
		}
	}
	return crns
}

// SetSource seeds one instance's stream source by CRN. Collect uses it to
// route an instance through icl.FetchBackgroundData(queryID); a no-op when the
// CRN is unknown.
func (i Instances) SetSource(crn string, src querySource) {
	if inst := i.FindByCRN(crn); inst != nil {
		inst.source = src
	}
}

// ClearSources resets every instance's stream source to the sync default. Called
// by startFetch so a normal fetch never inherits a stale collect queryId (which
// would mis-route StartQuery to icl.FetchBackgroundData against a stale id).
func (i Instances) ClearSources() {
	for _, instance := range i {
		instance.source = querySource{}
	}
}

func (i Instances) SetAll(b bool) {
	for _, instance := range i {
		if instance.IsReadOnly() {
			continue
		}
		e := instance.IsEnabled()
		if !b && e {
			instance.Disable()
			continue
		}
		if b && !e {
			instance.Enable()
		}
	}
}

// TransitionAuthing flips instances currently AuthInProgress to `to`, scoped to
// env when non-empty. Terminal targets (Error/Cancelled) freeze the elapsed
// timer; Enabled (passcode cancel) resets it. AuthInProgress is the pre-query
// placeholder set by ResolveTokens; this settles it once auth resolves/fails.
func (i Instances) TransitionAuthing(env icl.Environment, to status.Phase) {
	for _, instance := range i {
		if instance.state != status.AuthInProgress {
			continue
		}
		if env != "" && instance.env != env {
			continue
		}
		switch to {
		case status.Error, status.Cancelled:
			instance.lastUpdateTime = time.Now()
		case status.Enabled:
			instance.ResetTimer()
		}
		instance.state = to
	}
}

// resolveEnvMembers resolves bearer tokens for one environment's CRN members
// on a tracked poster goroutine. The first member exercises the shared
// environment credential, so its failure errors the whole environment; a later
// failure errors only that member.
func (i Instances) resolveEnvMembers(ctx context.Context, authGeneration uint64, authManager *icl.AccountManager, env icl.Environment, members []string, query string, poster msgs.Poster) {
	poster.Go(func(gctx context.Context) {
		for idx, crnString := range members {
			crn, err := config.CRNFromString(crnString)
			var token string
			if err == nil {
				token, err = authManager.GetAuthToken(ctx, crn)
			}
			if err != nil {
				var pr *icl.PasscodeRequired
				switch {
				case errors.As(err, &pr):
					_ = poster.PostCritical(gctx, PasscodeRequiredMsg{Pr: pr, AuthGeneration: authGeneration})
					return
				case errors.Is(err, context.Canceled):
					_ = poster.PostCritical(gctx, EnvAuthCancelledMsg{Env: env, AuthGeneration: authGeneration})
					return
				case idx == 0:
					_ = poster.PostCritical(gctx, EnvCredFailedMsg{Env: env, Err: err, AuthGeneration: authGeneration})
					return
				default:
					_ = poster.PostCritical(gctx, MemberAuthFailedMsg{CRN: crnString, Err: err, AuthGeneration: authGeneration})
					continue
				}
			}
			_ = poster.PostCritical(gctx, MemberAuthResolvedMsg{CRN: crnString, Token: token, Query: query, AuthGeneration: authGeneration})
		}
	})
}

// ResolveTokens marks every enabled instance AuthInProgress, then spawns one
// resolveEnvMembers goroutine per environment.
func (i Instances) ResolveTokens(ctx context.Context, authGeneration uint64, authManager *icl.AccountManager, query string, poster msgs.Poster) {
	grouped := make(map[icl.Environment][]string)
	for _, instance := range i {
		if instance.IsReadOnly() {
			continue
		}
		instance.ClearStore()
		instance.flushedLogCount = 0
		instance.flushedLogsSize = 0
		instance.ResetTimer()
		if instance.IsEnabled() {
			instance.StartAuthTimer()
			grouped[instance.env] = append(grouped[instance.env], instance.CRN)
		} else if instance.state == status.Cancelled {
			instance.state = status.Disabled
		}
	}
	for env, members := range grouped {
		i.resolveEnvMembers(ctx, authGeneration, authManager, env, members, query, poster)
	}
}

// resolveInstanceToken resolves one instance's bearer token and base URL directly
// from its CRN without touching any store or fetch timer. It serves dispatch,
// collect, and archive polling, including an archive CRN absent from the picker.
func (i Instances) resolveInstanceToken(ctx context.Context, am *icl.AccountManager, crn string) (token, url string, err error) {
	parsed, err := config.CRNFromString(crn)
	if err != nil {
		return "", "", fmt.Errorf("parse instance CRN: %w", err)
	}
	tok, err := am.GetAuthToken(ctx, parsed)
	if err != nil {
		return "", "", err
	}
	return tok, icl.GetURLFromCRN(parsed), nil
}

func (i Instances) CancelQuery() {
	for _, instance := range i {
		instance.CancelQuery()
	}
}
