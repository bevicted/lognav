package instancepicker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/openurl"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/logviewer"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/status"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/google/uuid"
	"github.com/rivo/uniseg"
)

type (
	PasscodeSuccessMsg struct{ Env icl.Environment }
	PasscodeErrorMsg   struct {
		Env icl.Environment
		Err error
	}
)
type PasscodeCancelledMsg struct{ Env icl.Environment }

// MemberAuthResolvedMsg carries one instance's resolved bearer token; its query
// is started on receipt.
type MemberAuthResolvedMsg struct {
	CRN            string
	Token          string
	Query          string
	AuthGeneration uint64
}

// MemberAuthFailedMsg marks a single instance's auth as failed (a later member
// whose env credential already proved good — error only this member).
type MemberAuthFailedMsg struct {
	CRN            string
	Err            error
	AuthGeneration uint64
}

// EnvCredFailedMsg marks an env's shared credential as unusable (the first
// member exercised it and failed) — every auth-pending member of that env errors.
type EnvCredFailedMsg struct {
	Env            icl.Environment
	Err            error
	AuthGeneration uint64
}

// EnvAuthCancelledMsg reports that an env's in-flight token resolution was
// cancelled (authCtx cancelled); its auth-pending members go Cancelled. The
// generation prevents an old cancelled worker from settling a later fetch.
type EnvAuthCancelledMsg struct {
	Env            icl.Environment
	AuthGeneration uint64
}

// PasscodeRequiredMsg requests the interactive passcode dialog for an env.
type PasscodeRequiredMsg struct {
	Pr             *icl.PasscodeRequired
	AuthGeneration uint64
}

// WatchTickMsg fires after a watch cooldown elapses, requesting the next watch
// fetch round. Epoch guards against stale ticks from a stopped/restarted watch.
type WatchTickMsg struct{ Epoch uint64 }

// ArchiveDispatchedMsg reports the result of an archive dispatch after the
// off-loop submit fan-out and archive-file write complete. Saved is the archive
// name written to disk (empty when no instance submitted successfully, in which
// case nothing was written). Succeeded/Attempted drive the result notice.
// The archive.Save IO already happened off-loop in the dispatch worker (§13: keep
// network-adjacent IO off the loop); the on-loop handler only renders the notice
// and refreshes the archive list.
type ArchiveDispatchedMsg struct {
	Saved     string // archive name persisted, or "" when all submits failed
	Succeeded int
	Attempted int
}

// DispatchInstanceDoneMsg reports one instance's dispatch SUBMIT outcome so the
// on-loop handler can flip that instance out of its in-progress phase to a
// terminal badge (Success on a clean submit, Error on a resolver/submit failure)
// and freeze its timer. Posted off-loop per instance by the dispatch worker; the
// dispatch drives the same per-instance phase/timer feedback as a normal fetch
// (without streaming logs or opening a .wip).
type DispatchInstanceDoneMsg struct {
	CRN string
	OK  bool
}

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.KeyTarget         = (*Model)(nil)
	_ component.MouseTarget       = (*Model)(nil)
	_ component.DoubleClickTarget = (*Model)(nil)
	_ component.MousePasteTarget  = (*Model)(nil)
	_ component.ScrollTarget      = (*Model)(nil)
	_ component.MouseHoverTarget  = (*Model)(nil)
	_ component.PasteTarget       = (*Model)(nil)
)

var (
	setOwnInUse         = snapshot.SetOwnInUse
	publishAutoSnapshot = snapshot.PublishAutoSnapshot
)

type Model struct {
	ctx context.Context
	// authCtx scopes every GetAuthToken call (token resolution + single-instance
	// retry). It is a child of ctx created in New and cancelled by CancelQueries /
	// Close at teardown. ctx itself is the signal context (cmd.Context), which
	// survives a `q`-quit, so auth calls rooted directly in ctx would block the
	// runtime's poster Wait when the user quits mid-resolution (e.g. during a slow
	// 1Password / IAM call); authCtx gives teardown a cancel handle for them.
	// cancelAllFetches also cancels and re-arms it mid-session. All reads/writes
	// happen on the loop goroutine; code spawning an off-loop GetAuthToken must
	// snapshot authCtx into a local on the loop first (see keybinds.go retry) to
	// avoid racing that re-arm.
	authCtx          context.Context
	cancelAuth       context.CancelFunc
	authGeneration   uint64 // identifies the current authentication worker cohort
	bundle           deps.Bundle
	instances        Instances
	authManager      *icl.AccountManager
	kh               *keys.Handler
	alwaysHandleKH   *keys.Handler
	list             *list.Model
	send             func(uv.Event)
	poster           msgs.Poster
	openBrowser      func(context.Context, string) error
	queryStartTime   time.Time
	logger           *slog.Logger
	backingContainer *snapshot.Container    // .wip file during fetch
	backingFile      *os.File               // underlying file for Close
	backingPath      atomic.Pointer[string] // .wip during fetch, finalized after rename; nil when none
	backingID        atomic.Pointer[string] // stable v4 UUID of the backing snapshot; nil for legacy/no-id backings
	notifyDirty      func()                 // injected by ui; synchronously broadcasts snapshots_dirty on a live backing claim
	onRemoveReadOnly func(crn string)       // injected by ui; clears an active transient store/statusline before removal
	fetching         bool                   // true while any instance is querying
	fetchCancelled   bool                   // set when the user cancels the fetch; suppresses the fetch-done notification
	// firstFetchCandidates scopes a one-shot first-response race. A non-nil map
	// marks an active race; firstFetchWinner is set by the loop before its first
	// non-empty batch enters the store.
	firstFetchCandidates map[string]struct{}
	firstFetchWinner     string
	// collecting is true while a collect (archive -> snapshot) fetch is in flight.
	// It is set by startCollect and read INSIDE maybeFinalizeFetch (force-finalize an
	// all-errored collect so it still saves an error-bearing snapshot), so it must be
	// reset only AFTER maybeFinalizeFetch returns — never before. See startCollect /
	// maybeFinalizeFetch / cancelAllFetches for the load-bearing reset ordering.
	collecting     bool
	pendingFlushes int // number of in-flight async instance flushes, for the CURRENT fetch only
	// fetchEpoch identifies the fetch a flush belongs to. It is bumped by
	// startFetchWithSources (which also resets pendingFlushes and opens a fresh
	// .wip), and stamped into InstanceFlushedMsg on-loop before the compression
	// goroutine spawns. OnInstanceFlushed drops any message whose stamp no longer
	// matches: without it, a flush from an abandoned fetch would write its frame
	// into the new fetch's backing container (stale logs under the new snapshot's
	// name) and decrement the new fetch's pendingFlushes, unblocking finalize early.
	fetchEpoch uint64
	// snapshotMembers is the declared membership of the backing snapshot. It is
	// captured when a fetch starts, or loaded from a restored state frame, so a
	// selection change after either operation cannot change the backing's scope.
	// Entries also preserve restored members no longer present in the current
	// configuration when the snapshot is manually saved again.
	snapshotMembers    []snapshot.InstanceSnapshot
	hasSnapshotMembers bool
	lastSavedSession   map[icl.Environment]string // last successfully written session contents (for dirty check)
	pendingLoads       map[string]struct{}        // instances awaiting their frame flush before lazy load can complete (loop-goroutine only; no lock)
	// watch loop state. A watch is "one long fetch": startFetch opens one .wip
	// held open across rounds; still-empty instances sit in status.Watching during
	// the cooldown gap. watchEpoch guards stale WatchTickMsg from a stopped watch.
	watching    bool
	watchEpoch  uint64
	watchStart  time.Time
	roundCount  int
	nextRoundAt time.Time
	// Frame-invariant instance-row column widths, computed once in New. Both are
	// derived from immutable config — labelWidth from the phase-label set
	// (status.MaxLabelWidth), nameWidth from the configured instance list, which
	// NewInstances builds once and nothing mutates afterwards — so refreshList
	// reads them instead of rescanning every instance on every redraw tick.
	labelWidth int
	nameWidth  int
}

// SetSend sets the send function used to deliver streaming messages from
// query goroutines back to the runtime event loop.
func (m *Model) SetSend(send func(uv.Event)) {
	m.send = send
}

// SetPoster injects the runtime poster used to spawn tracked goroutines (e.g.
// the async instance-log flush) and deliver their results back through the
// loop. It also binds the poster to every instance's LogStore: stores are
// constructed in New() before the runtime supplies a poster and are never
// recreated, and the logviewer is replaced on instance select (it only swaps
// which static store it points to). Wiring here therefore guarantees every
// store has a non-nil poster before any DoSearch/dispatch/re-search runs,
// independent of logviewer replacement.
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	if m.list != nil {
		m.list.SetPoster(p)
	}
	for _, inst := range m.instances {
		if inst.Store != nil {
			inst.Store.SetPoster(p)
		}
	}
}

// SetNotifyDirty injects the callback setBacking fires after a live backing
// claim (path + .inuse write) to tell peer sessions the lock set changed.
// The ui-supplied closure posts the local refresh asynchronously and broadcasts
// to peers directly, so setBacking - which runs ON the loop goroutine - stays
// deadlock-safe. A nil callback disables the claim broadcast. Deliberately NOT
// fired on the shutdown/Close release path
// (closeBackingContainer's raw setBackingPath("")), which would spawn a
// poster.Go after drainPoster/Wait (use-after-drain hazard).
func (m *Model) SetNotifyDirty(fn func()) { m.notifyDirty = fn }

// SetOnRemoveReadOnly injects the root-owned cleanup for a transient row that
// is about to leave the picker. The callback runs on the loop goroutine.
func (m *Model) SetOnRemoveReadOnly(fn func(crn string)) { m.onRemoveReadOnly = fn }

// emit posts ev through msgs.PostAsync: Update/HandleKey/Init all run ON the
// loop goroutine. For Init this is also safe: Init runs before the loop, and
// the post blocks harmlessly until rt.loop() starts draining (see runtime.go
// Run).
func (m *Model) emit(ev uv.Event) {
	msgs.PostAsync(m.poster, ev)
}

// requestQuit requests a clean exit. It is the quit hook wired into the Quit
// keybind; keybind actions run on the loop goroutine, hence msgs.RequestQuit.
// It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

// toggleSelected cycles the status (enable/disable; cancel an in-progress
// fetch or auth) of the instance under the list cursor via Instance.Toggle. It
// is the Select-keybind action and the status-column click action in
// OnMouseClick, so clicking the status label cycles it exactly as the Select
// key does.
func (m *Model) toggleSelected() {
	m.instances[m.list.GetCursor()].Toggle()
}

// emitSelect posts an InstanceSelectMsg (off-loop) for the instance under the
// list cursor, keeping the logviewer adopted store synchronized with that
// selection. It is the single source of the cursor-move select emit shared by
// the key cursor-move path and the mouse-click select path.
func (m *Model) emitSelect() {
	m.emit(selectMsgFor(m.instances[m.list.GetCursor()]))
}

// pickerRegion names the part of the instance picker a pointer landed in, as
// resolved by regionAt. It is the shared vocabulary of the click seams; each
// seam decides on its own what a region means for it.
type pickerRegion int

const (
	// regionNone is anything outside an interactive instance row: below the last
	// item, or an empty list. The row index is meaningless.
	regionNone pickerRegion = iota
	// regionHeader is the fuzzy-input header row. The row index is meaningless.
	regionHeader
	// regionStatus is the status-label columns of an instance row.
	regionStatus
	// regionBody is the name/count/time columns of an instance row.
	regionBody
)

// regionAt resolves a pointer position to the picker region it landed in and,
// for regionStatus/regionBody, the display index of the instance row under y
// (meaningless for regionHeader/regionNone). It is the one place holding the
// picker's pointer geometry — the fuzzy-header test, the row hit-test, the
// empty-list guard, and the status-column arithmetic:
//
// The status label is the leading row segment, left-aligned and padded to
// status.MaxLabelWidth, so an x within [listRect.X, listRect.X+MaxLabelWidth)
// targets the status and anything further right is row body.
func (m *Model) regionAt(x, y int) (pickerRegion, int) {
	tiRect, listRect := m.list.SubRects()
	if tiRect.H >= 1 && y >= tiRect.Y && y < tiRect.Y+tiRect.H {
		return regionHeader, 0
	}

	idx, ok := m.list.RowAtY(y)
	if !ok || len(m.instances) == 0 {
		return regionNone, 0
	}

	if labelWidth := status.MaxLabelWidth(m.bundle); x >= listRect.X && x < listRect.X+labelWidth {
		return regionStatus, idx
	}
	return regionBody, idx
}

// OnMouseClick implements component.MouseTarget. It routes a left click by the
// region regionAt resolves, always placing the cursor on the clicked row first:
//
//   - regionHeader → delegated to the inner list (focus + caret).
//   - regionStatus → place the cursor on the clicked row and cycle the
//     instance's status via toggleSelected (the click analogue of the Select
//     keybind), emitting an InstanceSelectMsg to keep the right-panel preview in
//     sync; stays on the Instances tab regardless of which row was clicked.
//   - regionBody → select-only: every click moves the cursor and emits an
//     InstanceSelectMsg for the preview; activation (opening the instance in the
//     log viewer) requires a double-click via OnMouseDoubleClick.
//   - regionNone → not consumed.
//
// Runs on the loop goroutine; toggleSelected/emitSelect post their follow-ups
// off-loop themselves (via m.emit), so no bare PostCritical is issued here.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	switch region, idx := m.regionAt(x, y); region {
	case regionHeader:
		// Let the list handle focus + caret placement.
		return m.list.OnMouseClick(x, y, btn)
	case regionStatus:
		// Move the cursor first so the cycle hits the clicked instance.
		m.list.SetCursor(idx)
		m.toggleSelected()
		m.emitSelect()
		return true
	case regionBody:
		// Select-only. A single click always moves the cursor and syncs the
		// preview; activation (opening in the log viewer) requires a double-click.
		m.list.SetCursor(idx)
		m.emitSelect()
		return true
	default:
		return false
	}
}

// OnMouseDoubleClick implements component.DoubleClickTarget. It activates the
// clicked instance row, reading the same regionAt result as OnMouseClick:
//
//   - regionHeader → delegated to OnMouseClick (focus + caret; a double-click on
//     a text input should not open anything).
//   - regionStatus → consumed as a no-op: the preceding single-click already
//     cycled the status; a double-click must not cycle it twice or open.
//   - regionBody → place the cursor on the clicked row and open the instance in
//     the log viewer via OpenInLogViewer.
//   - regionNone → not consumed.
//
// Runs on the loop goroutine; OpenInLogViewer posts off-loop via m.emit.
func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	switch region, idx := m.regionAt(x, y); region {
	case regionHeader:
		// Delegate to the single-click handler (focus + caret).
		return m.OnMouseClick(x, y, btn)
	case regionStatus:
		// Consume but do nothing — the preceding single-click already cycled the
		// status; the double must not cycle it a second time.
		return true
	case regionBody:
		// Move the cursor to the clicked row and open it in the log viewer.
		m.list.SetCursor(idx)
		m.OpenInLogViewer()
		return true
	default:
		return false
	}
}

// OnMousePaste implements component.MousePasteTarget: a middle-click on the
// fuzzy-input header forwards to the inner list's paste (focus + caret + insert
// + refilter). Item rows are not text inputs, so clicks there are ignored.
func (m *Model) OnMousePaste(x, y int, content string) bool {
	tiRect, _ := m.list.SubRects()
	if tiRect.H >= 1 && y >= tiRect.Y && y < tiRect.Y+tiRect.H {
		return m.list.OnMousePaste(x, y, content)
	}
	return false
}

// OnMouseScroll implements component.ScrollTarget: a vertical wheel notch pans
// the instance list (drag-at-edge via list.ScrollViewport). A pan that drags the
// selection onto a different instance re-emits InstanceSelectMsg so the
// right-panel preview follows the new selection (the same sync the cursor-move
// key/click paths do); a pure pan that leaves the selection put emits nothing.
// Always consumed.
func (m *Model) OnMouseScroll(_, _, lines int) bool {
	before := m.list.GetCursor()
	m.list.ScrollViewport(lines)
	if m.list.GetCursor() != before {
		m.emitSelect()
	}
	return true
}

// OnMouseHover implements component.MouseHoverTarget: it tints the instance row
// under the pointer by forwarding to the inner list. Hover is visual only — it
// never moves the selection or re-emits InstanceSelectMsg (unlike scroll), so the
// right-panel preview is unaffected. Returns true when the hovered row changed.
func (m *Model) OnMouseHover(x, y int) bool {
	return m.list.OnMouseHover(x, y)
}

// ClearMouseHover drops hover retained by the owned list.
func (m *Model) ClearMouseHover() bool {
	return m.list.ClearMouseHover()
}

func (m *Model) IsFetching() bool {
	return m.fetching
}

// IsAnimating reports whether the picker needs periodic redraws — true while any
// instance query is still InProgress (its live elapsed timer must keep
// advancing). The spine's shared ticker reads this predicate to start/stop the
// redraw ticker, whose cadence is the configured Core.RedrawIntervalMs (33ms by
// default, clamped to 8-1000ms); FETCH-ONLY (no cursor blink, per R3b/D1).
func (m *Model) IsAnimating() bool {
	return !m.instances.AreAllQueriesDone()
}

func (m *Model) GetBackingPath() string {
	if p := m.backingPath.Load(); p != nil {
		return *p
	}
	return ""
}

// HeldManagedBacking reports whether this session currently holds a managed
// (snapshot-dir-resident) backing whose .inuse claim it owns. Used at shutdown
// to decide whether to release-notify peers (an in-place external open holds no
// claim, so it needs no notify). Shutdown callers MUST read it BEFORE Close(),
// which clears the backing path (so a post-Close read always returns false).
func (m *Model) HeldManagedBacking() bool {
	p := m.GetBackingPath()
	return p != "" && isInSnapshotDir(p)
}

// setBackingPath stores the given path and synchronizes this session's
// <pid>.inuse guard file to match (empty string clears both). The .inuse write
// is small, rare local IO done synchronously on the loop goroutine — it is not a
// poster post, so the unbuffered-channel deadlock rule does not apply, and being
// synchronous makes claim ordering (see finalizeBackingContainer) just
// sequential code. A guard-write failure is logged, not fatal: correctness on
// crash comes from pidAlive, not from this write succeeding.
func (m *Model) setBackingPath(path string) {
	if path == "" {
		m.backingPath.Store(nil)
	} else {
		m.backingPath.Store(&path)
	}
	// An empty path ALWAYS clears the guard. A non-empty path is guarded only
	// when it is a managed (dir-resident) file: an in-place external open (e.g.
	// `lognav ~/Downloads/x.lognav`) is not managed — nothing in lognav would
	// delete it, and guarding its basename could falsely mark a same-named
	// managed file in-use.
	if path != "" && !isInSnapshotDir(path) {
		return
	}
	if err := snapshot.SetOwnInUse(path); err != nil {
		m.logger.Warn("failed to update in-use guard", "path", path, logging.KeyError, err)
	}
}

// setBacking updates the backing path (+ .inuse) and records the backing's
// stable UUID for notify-loss recovery. id may be "" (legacy/no-id snapshot).
// After the .inuse write it fires notifyDirty (if set) so peer sessions recolor
// their lists: a claim adds a holder, an explicit-delete evict release ("","")
// drops one. notifyDirty posts the local refresh asynchronously, then broadcasts
// directly; the direct broadcast is safe on the loop because it does not post.
// The shutdown/Close release deliberately bypasses setBacking (raw
// setBackingPath) so the runtime can notify only after Close removes the guard.
func (m *Model) setBacking(path, id string) {
	m.setBackingPath(path)
	if path == "" || id == "" {
		m.backingID.Store(nil)
	} else {
		m.backingID.Store(&id)
	}
	if m.notifyDirty != nil {
		m.notifyDirty()
	}
}

// backingUUID returns the stable UUID of the current backing, or "" for a
// legacy snapshot (no id) or when no backing is held.
func (m *Model) backingUUID() string {
	if p := m.backingID.Load(); p != nil {
		return *p
	}
	return ""
}

// isInSnapshotDir reports whether path lives directly in the managed snapshot dir.
func isInSnapshotDir(path string) bool {
	dir, err := snapshot.Dir()
	if err != nil {
		return false
	}
	return filepath.Dir(filepath.Clean(path)) == dir
}

// EvictIfCurrentBacking clears the backing path (and its .inuse claim) iff the
// given basename is the session's current backing. Called synchronously on the
// loop goroutine from the snapshot browser's explicit-delete path when the user
// deletes their own current backing file: clearing first, on the single-writer
// loop goroutine, before the off-loop os.Remove guarantees no race on
// backingPath and no window where it points at a removed file. Deleting any
// other file leaves the backing untouched. Subsequent lazy loads of
// not-yet-loaded instances hit the graceful ErrNoBackingFile path.
func (m *Model) EvictIfCurrentBacking(basename string) {
	if filepath.Base(m.GetBackingPath()) == basename {
		m.removeReadOnly()
		m.snapshotMembers = nil
		m.hasSnapshotMembers = false
		m.setBacking("", "")
		m.resetInstanceDisplays()
		m.refreshList()
		m.syncInstanceState()
	}
}

// resetInstanceDisplays returns every instance's runtime/display state to its
// never-fetched default — empty store, no flushed log count, and a reset elapsed
// timer — without touching the instance's name or the user's enable/disable
// selection. It is the display half of EvictIfCurrentBacking: when the user
// deletes the snapshot their session is backed by, the loaded logs are gone, so
// the lingering Success/Error badges, counts, and frozen timers are stale and
// must be cleared to the fresh "no snapshot loaded" look.
//
// The reset state mirrors a fresh launch as seen via IsEnabled(): an instance
// the user has selected (anything but Disabled/Cancelled) becomes Enabled; a
// deselected one (Disabled or the terminal Cancelled badge) becomes Disabled.
// Demoting Cancelled to Disabled matches ResolveTokens, which clears the
// one-shot Cancelled badge the same way, and keeps IsEnabled() unchanged.
//
// Pure local state mutation on the loop goroutine (the single-writer invariant):
// it issues no poster post or broadcast. The setBacking("","") in
// EvictIfCurrentBacking already fired notifyDirty for the lock-set change.
func (m *Model) resetInstanceDisplays() {
	for _, inst := range m.instances {
		enabled := inst.IsEnabled()
		inst.ClearStore()
		inst.flushedLogCount = 0
		inst.flushedLogsSize = 0
		inst.ResetTimer()
		if enabled {
			inst.state = status.Enabled
		} else {
			inst.state = status.Disabled
		}
	}
}

// OnSnapshotRenamed reclaims this session's backing when the renamed snapshot is
// the one it holds: it re-points the backing path and swaps the .inuse claim
// from -> to atomically (setBackingPath rewrites the single-basename file). A
// rename of any other snapshot is a no-op here (the list refresh is handled by
// snapshothandler). Runs on the loop goroutine.
func (m *Model) OnSnapshotRenamed(from, to string) {
	if filepath.Base(m.GetBackingPath()) != from {
		return
	}
	dir, err := snapshot.Dir()
	if err != nil {
		m.logger.Warn("snapshot dir lookup failed during rename self-correct", logging.KeyError, err)
		return
	}
	// Full managed path — a bare basename would trip isInSnapshotDir and skip the .inuse write.
	m.setBacking(filepath.Join(dir, to), m.backingUUID())
}

// resolveBackingPath returns the current backing path, recovering it by UUID if
// the recorded path no longer exists (a rename-notify was dropped). On success
// it re-points the backing (path + .inuse) so subsequent reads use the new
// name. ONLY fs.ErrNotExist triggers recovery; genuine decode/corruption errors
// are surfaced by the caller unchanged (jq-error footgun: never swallow a real
// fault). MUST be called off-loop: FindByID scans every managed snapshot.
func (m *Model) resolveBackingPath() (string, error) {
	p := m.GetBackingPath()
	if p == "" {
		return "", ErrNoBackingFile
	}
	if _, err := os.Stat(p); err == nil {
		return p, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	id := m.backingUUID()
	if id == "" {
		return "", ErrNoBackingFile
	}
	e, err := snapshot.FindByID(id)
	if err != nil {
		return "", err // genuine loss
	}
	m.setBacking(e.Path, id) // fires notifyDirty → snapshots_dirty; harmless spurious peer refresh
	m.logger.Info("recovered backing by UUID after dropped rename notify", "path", e.Path)
	return e.Path, nil
}

func (m *Model) GetInstances() Instances {
	return m.instances
}

// removeReadOnly drops snapshot-only rows at backing-replacement chokepoints.
// It deliberately is not called by store-clearing paths: ClearData is also used
// for resident-memory eviction, flush release, retries, and watch rounds while
// the same backing remains active.
func (m *Model) removeReadOnly() {
	configured := m.instances.Configured()
	if len(configured) == len(m.instances) {
		return
	}
	for _, inst := range m.instances {
		if !inst.IsReadOnly() {
			continue
		}
		if m.onRemoveReadOnly != nil {
			m.onRemoveReadOnly(inst.CRN)
		}
		if err := inst.Close(); err != nil {
			m.logger.Error("read-only instance close failed", logging.KeyInstance, inst.Name, logging.KeyError, err)
		}
	}
	m.instances = configured
	m.nameWidth = longestInstanceName(m.instances)
	m.list.WithItems(make([][]list.Segment, len(m.instances)))
}

// appendReadOnlyRows adds snapshot members that do not appear in effective
// config, retaining snapshot order even when compact labels collide.
func (m *Model) appendReadOnlyRows(snaps []snapshot.InstanceSnapshot) error {
	for _, snap := range snaps {
		if m.instances.FindByCRN(snap.CRN) != nil {
			continue
		}
		inst, err := NewReadOnlyInstance(m.bundle, snap)
		if err != nil {
			return err
		}
		m.instances = append(m.instances, inst)
	}
	m.nameWidth = longestInstanceName(m.instances)
	m.list.WithItems(make([][]list.Segment, len(m.instances)))
	return nil
}

// displayNameForCRN resolves a CRN only for cosmetic output. Runtime routing
// must continue to carry the CRN itself.
func (m *Model) displayNameForCRN(crn string) string {
	if inst := m.instances.FindByCRN(crn); inst != nil {
		return inst.Name
	}
	parsed, err := config.CRNFromString(crn)
	if err != nil {
		return "unknown instance"
	}
	return config.DisplayNameForCRN(config.EffectiveInstances(m.bundle.Config), parsed)
}

// visibleInstances returns the subset of instances currently shown in the list:
// the fuzzy-filtered set when the finder is active, or every instance otherwise.
// It maps the list's visible original indices back onto m.instances (the list's
// items are kept 1:1 with m.instances by refreshList), guarding against any
// out-of-range index.
func (m *Model) visibleInstances() Instances {
	idxs := m.list.VisibleIndices()
	subset := make(Instances, 0, len(idxs))
	for _, i := range idxs {
		if i >= 0 && i < len(m.instances) {
			subset = append(subset, m.instances[i])
		}
	}
	return subset
}

// toggleAllVisible toggles unified selection across only the instances currently
// visible under the fuzzy filter (every instance when no filter is active),
// preferring "all selected": if all visible instances are already enabled it
// disables them, otherwise it enables them all. Instances hidden by the filter
// keep their current selection. It is the action bound to the All key.
func (m *Model) toggleAllVisible() {
	visible := m.visibleInstances().Configured()
	visible.SetAll(!visible.AreAllEnabled())
}

func New(ctx context.Context, bundle deps.Bundle) *Model {
	configured := config.EffectiveInstances(bundle.Config)
	instances := newInstances(bundle, configured)

	am := icl.NewAccountManager(bundle.Config.ICL.Environments)

	loadedSession := map[icl.Environment]string{}
	if sessionPath, err := icl.SessionPath(); err == nil {
		if tokens, err := icl.LoadSession(sessionPath); err == nil {
			am.SetRefreshTokens(tokens)
			loadedSession = tokens
		}
	}

	authCtx, cancelAuth := context.WithCancel(ctx)
	m := &Model{
		ctx:              ctx,
		authCtx:          authCtx,
		cancelAuth:       cancelAuth,
		bundle:           bundle,
		list:             list.New(bundle).WithItems(make([][]list.Segment, len(instances))),
		instances:        instances,
		authManager:      am,
		openBrowser:      openurl.Open,
		logger:           slog.Default().With(logging.KeyComponent, "instancepicker"),
		lastSavedSession: loadedSession,
		pendingLoads:     map[string]struct{}{},
		labelWidth:       status.MaxLabelWidth(bundle),
		nameWidth:        longestInstanceName(instances),
	}
	bindKeyhandlersToModel(m)
	m.list.WithFuzzyConfirmAction(m.OpenInLogViewer)

	return m
}

func (m *Model) Init() {
	if len(m.instances) > 0 {
		m.emit(selectMsgFor(m.instances[0]))
	}
	m.syncInstanceState()
}

// OnPaste forwards a bracketed-paste event to the inner list (fuzzy filter)
// only while the list's search input is focused. The root calls it directly.
//
// The focus gate matches HandleKey — where a focused input is the only text
// sink — and filehandler.OnPaste. Forwarding unconditionally let a paste
// silently filter the instance list while the search was closed.
func (m *Model) OnPaste(ev uv.Event) {
	if m.list.IsFocused() {
		m.list.OnPaste(ev)
	}
}

// OnLogStream handles a streaming-log batch on the loop. A first-fetch winner
// is elected before its first log batch reaches the store, and queued loser
// batches are rejected after election.
func (m *Model) OnLogStream(msg *msgs.LogStreamMsg) {
	if m.firstFetchCandidates != nil {
		inst := m.instances.FindByCRN(msg.CRN)
		if _, candidate := m.firstFetchCandidates[msg.CRN]; !candidate ||
			inst == nil || !inst.IsEnabled() || msg.ID != inst.Store.GetQueryID() ||
			(m.firstFetchWinner != "" && m.firstFetchWinner != msg.CRN) {
			return
		}
		if m.firstFetchWinner == "" && len(msg.Logs) > 0 {
			m.electFirstFetchWinner(msg.CRN)
		}
	}
	if inst := m.instances.FindByCRN(msg.CRN); inst != nil {
		inst.handleLogStreamMsg(msg)
	}
	m.syncInstanceState()
}

// OnLogStreamDone handles the stream-done signal for one instance, flushes its
// logs to the backing container, and finalizes the fetch once all queries are
// done.
func (m *Model) OnLogStreamDone(msg *msgs.LogStreamDoneMsg) {
	// The flush still runs for cancelled instances because buffered logs belong
	// in the backing container.
	if inst := m.instances.FindByCRN(msg.CRN); inst != nil {
		inst.handleLogStreamDoneMsg(msg)

		logCount := inst.Store.GetLogCount()
		elapsed := inst.lastUpdateTime.Sub(inst.startTime).Milliseconds()
		m.logger.Info("query done", logging.KeyInstance, inst.Name, "state", inst.state.String(), logging.KeyCount, logCount, logging.KeyDurationMS, elapsed)

		// Truncation warning (collect only): a v1 collect has the same fixed 50,000-row
		// ceiling as a synchronous fetch. Captured BEFORE Snapshotize/ClearData (which
		// wipes the store message) and re-applied AFTER the flush block so it survives
		// into finalize's SnapshotizeMeta read.
		truncated := m.collecting && logCount >= int(icl.SyncQueryLimit)

		// Snapshot logs synchronously (fast copy), compress async to avoid blocking UI.
		if m.backingContainer != nil && logCount > 0 {
			logs, unlock := inst.Store.Snapshotize()
			unlock()
			inst.flushedLogCount = len(logs)
			inst.Store.ClearData()

			m.pendingFlushes++
			crn := inst.CRN
			logsCopy := logs
			// The epoch is read HERE, on the loop, before the closure: a fetch
			// started while this compression runs bumps m.fetchEpoch, and the stamp
			// is what lets OnInstanceFlushed tell this flush from one belonging to
			// the new fetch.
			epoch := m.fetchEpoch
			// Off-loop: compress then deliver the result via PostCritical (a
			// dropped flush would strand pendingFlushes and block finalize). The
			// on-loop prologue above (Snapshotize/unlock/ClearData/pendingFlushes++)
			// stays on the loop goroutine per the single-writer invariant.
			m.poster.Go(func(ctx context.Context) {
				compressed, logsSizeBytes, err := snapshot.CompressInstanceLogs(logsCopy)
				_ = m.poster.PostCritical(ctx, InstanceFlushedMsg{crn: crn, compressed: compressed, logsSizeBytes: logsSizeBytes, err: err, epoch: epoch})
			})
		}
		if truncated {
			// Re-apply AFTER ClearData (which wiped the message). finalize's
			// SnapshotizeMeta reads GetMessage, so this surfaces in the snapshot.
			setTruncationWarning(inst)
		}
	}
	m.settle()
	m.syncInstanceState()
}

// OnInstanceFlushed handles completion of async log compression for an
// instance: writes the compressed frame to the backing container, resolves any
// pending lazy load waiting on it, and finalizes the fetch when all queries are
// done. Mirrors the previous Update arm.
//
// A message stamped with a superseded fetchEpoch is dropped whole: its frame
// belongs to a backing container that is already closed (and unlinked), and its
// pendingFlushes credit was consumed by startFetchWithSources' reset. Any lazy
// load that was waiting on it has already been answered with
// InstanceLoadReadyMsg{Err: ErrFetchAborted} by that same drainPendingLoads, so
// a stale flush must not touch m.pendingLoads either — an entry under this name
// now belongs to the current fetch.
func (m *Model) OnInstanceFlushed(msg InstanceFlushedMsg) {
	if msg.epoch != m.fetchEpoch {
		m.logger.Debug("dropping stale instance flush", logging.KeyInstance, m.displayNameForCRN(msg.crn), "epoch", msg.epoch, "current_epoch", m.fetchEpoch)
		return
	}

	flushSucceeded := true
	if msg.err != nil {
		flushSucceeded = false
		m.logger.Error("failed to compress instance logs", logging.KeyInstance, m.displayNameForCRN(msg.crn), logging.KeyError, msg.err)
	} else if inst := m.instances.FindByCRN(msg.crn); inst == nil {
		flushSucceeded = false
		m.logger.Error("failed to find instance for frame flush", logging.KeyInstance, m.displayNameForCRN(msg.crn))
	} else if err := snapshot.WriteCompressedInstanceFrame(m.backingContainer, inst.CRN, msg.compressed); err != nil {
		flushSucceeded = false
		m.logger.Error("failed to write instance frame", logging.KeyInstance, inst.Name, logging.KeyError, err)
	} else {
		inst.flushedLogsSize = msg.logsSizeBytes
	}
	m.pendingFlushes--

	// Resolve any pending load waiting on this instance.
	_, queued := m.pendingLoads[msg.crn]
	if queued {
		delete(m.pendingLoads, msg.crn)
	}
	if queued {
		if flushSucceeded {
			m.OpenInstance(msg.crn)
		} else {
			m.emit(InstanceLoadReadyMsg{CRN: msg.crn, Err: ErrFetchAborted})
		}
	}

	m.settle()
	m.syncInstanceState()
}

// OnSnapshotRestore restores instance/picker state from a snapshot: restores
// the app state (query/search/jq), resets the list cursor to the top (cursor
// position is no longer persisted in snapshots), restores each instance's
// metadata, sets the backing path, and selects the cursor instance.
// Mirrors the previous Update arm.
func (m *Model) OnSnapshotRestore(msg msgs.SnapshotRestoreMsg) {
	// stop any active watch first — otherwise its dangling m.watching misroutes
	// the next fetch's settle() through evaluateWatchRound instead of finalizing.
	m.stopWatch()
	m.bundle.State.RestoreSnapshot(msg.Snapshot.Query, msg.Snapshot.Search, msg.Snapshot.JQ, msg.Snapshot.Filters)
	m.removeReadOnly()
	m.instances.RestoreSnapshots(msg.Snapshot.InstancePickerSnapshot.Instances)
	if err := m.appendReadOnlyRows(msg.Snapshot.InstancePickerSnapshot.Instances); err != nil {
		m.logger.Error("failed to restore snapshot-only instance", logging.KeyError, err)
	}
	m.setSnapshotMembers(msg.Snapshot.InstancePickerSnapshot.Instances)
	m.setBacking(msg.BackingPath, msg.Snapshot.ID)
	// Cursor position is no longer saved in snapshots; reset to the top.
	m.list.SetCursor(0)
	if len(m.instances) > 0 {
		m.emit(selectMsgFor(m.instances[m.list.GetCursor()]))
	}
	m.syncInstanceState()
}

// OnViewInContext enables only the CRN-matched instance and disables the rest,
// so a follow-up fetch targets just the in-context instance.
func (m *Model) OnViewInContext(msg msgs.ViewInContextMsg) {
	for _, r := range m.instances {
		if r.CRN == msg.CRN {
			r.Enable()
			continue
		}
		r.Disable()
	}
	m.syncInstanceState()
}

// OnMemberAuthResolved starts the resolved instance's query (auth -> fetch). A
// member deselected mid-auth is skipped: Toggle sets Cancelled, the all-selector
// sets Disabled — !IsEnabled() covers both, so a late resolve never starts a
// query on a now-deselected instance. It persists the session because a
// successful refresh-grant / apiKey exchange rotates ea.refreshToken in memory;
// saveSession is dirty-checked (m.lastSavedSession), so the per-member calls only
// write when a token actually changed.
func (m *Model) OnMemberAuthResolved(msg MemberAuthResolvedMsg) {
	if msg.AuthGeneration != m.authGeneration {
		return
	}
	inst := m.instances.FindByCRN(msg.CRN)
	if inst == nil || !inst.IsEnabled() {
		return
	}
	inst.StartQuery(msg.Token, msg.Query, m.bundle.Config.Logs.MaxRows, m.send, m.poster)
	inst.StartTimer() // resets clock for the fetch run, flips to InProgress
	m.saveSession()
	m.syncInstanceState()
}

// OnMemberAuthFailed errors a single instance whose env credential was already
// good, then re-checks finalize (the failed member no longer blocks it).
func (m *Model) OnMemberAuthFailed(msg MemberAuthFailedMsg) {
	if msg.AuthGeneration != m.authGeneration {
		return
	}
	if m.firstFetchCandidates != nil && m.firstFetchWinner != "" {
		if _, candidate := m.firstFetchCandidates[msg.CRN]; candidate && msg.CRN != m.firstFetchWinner {
			m.settle()
			m.syncInstanceState()
			return
		}
	}
	if inst := m.instances.FindByCRN(msg.CRN); inst != nil {
		m.logger.Error("member auth failed", logging.KeyError, msg.Err, logging.KeyInstance, inst.Name)
		inst.lastUpdateTime = time.Now()
		inst.state = status.Error
	}
	m.settle()
	m.syncInstanceState()
}

// OnEnvCredFailed errors every auth-pending member of an env whose shared
// credential failed; the other env is untouched.
func (m *Model) OnEnvCredFailed(msg EnvCredFailedMsg) {
	if msg.AuthGeneration != m.authGeneration {
		return
	}
	m.logger.Error("env credential resolution failed", logging.KeyError, msg.Err, "env", string(msg.Env))
	m.instances.TransitionAuthing(msg.Env, status.Error)
	m.settle()
	m.syncInstanceState()
}

// OnEnvAuthCancelled settles an env's auth-pending members to Cancelled after a
// cancelAllFetches cancelled the in-flight resolution (idempotent — the on-loop
// cancel may have already moved them).
func (m *Model) OnEnvAuthCancelled(msg EnvAuthCancelledMsg) {
	if msg.AuthGeneration != m.authGeneration {
		return
	}
	m.logger.Info("env auth cancelled", "env", string(msg.Env))
	m.instances.TransitionAuthing(msg.Env, status.Cancelled)
	m.settle()
	m.syncInstanceState()
}

// OnPasscodeRequired shows the interactive passcode dialog for an env. That
// env's other auth-pending members stay AuthInProgress until it resolves.
func (m *Model) OnPasscodeRequired(msg PasscodeRequiredMsg) {
	if msg.AuthGeneration != m.authGeneration {
		return
	}
	if m.firstFetchCandidates != nil && m.firstFetchWinner != "" {
		return
	}
	passcodeDialogCmd(m.ctx, m.poster, m.authManager, msg.Pr, m.bundle.Config.Core.OpenBrowser, m.openBrowser)
}

// resumeEnvAuth re-runs per-env resolution for an env's still-AuthInProgress
// members (e.g. after a passcode unlock seeded the refresh token). The other
// env's in-flight or finished queries are untouched.
func (m *Model) resumeEnvAuth(env icl.Environment) {
	var members []string
	for _, inst := range m.instances {
		if inst.env == env && inst.state == status.AuthInProgress {
			members = append(members, inst.CRN)
		}
	}
	if len(members) == 0 {
		m.settle()
		return
	}
	m.instances.resolveEnvMembers(m.authCtx, m.authGeneration, m.authManager, env, members, m.bundle.State.LastFetchedQuery(), m.poster)
}

// OnPasscodeSuccess persists the session and resumes ONLY the unlocked env's
// auth (not a full fetch restart, which would nuke the other env's queries).
func (m *Model) OnPasscodeSuccess(msg PasscodeSuccessMsg) {
	m.saveSession()
	m.logger.Info("passcode auth success", "env", string(msg.Env))
	m.resumeEnvAuth(msg.Env)
	m.syncInstanceState()
}

// OnPasscodeError errors the env's auth-pending members and re-checks finalize.
func (m *Model) OnPasscodeError(msg PasscodeErrorMsg) {
	m.logger.Error("passcode auth failed", logging.KeyError, msg.Err, "env", string(msg.Env))
	m.instances.TransitionAuthing(msg.Env, status.Error)
	m.emit(msgs.ShowDialogMsg{
		Title:   "Authentication Failed",
		Message: msg.Err.Error(),
		Buttons: []msgs.DialogButton{{Label: "OK"}},
	})
	m.settle()
	m.syncInstanceState()
}

// OnPasscodeCancelled returns the env's auth-pending members to Enabled and
// re-checks finalize in case all other queries already completed.
func (m *Model) OnPasscodeCancelled(msg PasscodeCancelledMsg) {
	m.logger.Info("passcode auth cancelled", "env", string(msg.Env))
	m.instances.TransitionAuthing(msg.Env, status.Enabled)
	m.settle()
	m.syncInstanceState()
}

// HandleKey dispatches a key event using instancepicker's three-group
// precedence, faithfully mirroring the former key-event arm:
//  1. alwaysHandleKH is checked first regardless of list focus.
//  2. When the list is focused, all keys route to list.HandleKey
//     (leaf-consumes-all); an InstanceSelectMsg is emitted after to keep the
//     right-panel logviewer in sync with the cursor position.
//  3. kh is tried; on a hit the action runs and KeyHandled is returned.
//  4. Fall-through to list.HandleKey; if instances exist, an InstanceSelectMsg
//     is batched with the list's cmd (mirrors the current arm's unconditional
//     list.Update fall-through + InstanceSelectMsg emission).
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.alwaysHandleKH.Run(ev) {
		return component.KeyHandled
	}
	if m.list.IsFocused() {
		// Focused list swallows all keys (leaf-consumes-all). The
		// InstanceSelectMsg keeping the right-panel logviewer in sync with the
		// cursor is emitted off-loop (HandleKey runs on the loop goroutine).
		m.list.HandleKey(ev)
		m.emit(selectMsgFor(m.instances[m.list.GetCursor()]))
		return component.KeyHandled
	}
	if m.kh.Run(ev) {
		return component.KeyHandled
	}
	// Unfocused default: forward to list; emit InstanceSelectMsg if instances exist.
	m.list.HandleKey(ev)
	if len(m.instances) < 1 {
		return component.KeyHandled
	}
	m.emit(selectMsgFor(m.instances[m.list.GetCursor()]))
	return component.KeyHandled
}

// Component

func (m *Model) GetKeybinds() []keys.Binding {
	return append(m.alwaysHandleKH.GetKeybinds(), append(m.list.GetKeybinds(), m.kh.GetKeybinds()...)...)
}

func (m *Model) SetRect(r component.Rect) {
	m.list.SetRect(r)
}

func (m *Model) Draw(s component.Screen) *component.Cursor {
	m.refreshList()
	return m.list.Draw(s)
}

// custom

// refreshList rebuilds every instance row. It runs on every frame — during a
// fetch that is the shared redraw ticker's cadence plus every stream message —
// so it only computes what actually varies between frames: the elapsed time, and
// the phase label/count that a stream message can change. The two column widths
// that depend solely on config (labelWidth, nameWidth) are hoisted to New.
//
// The elapsed string is formatted ONCE per instance and reused by both the
// column-width pass and the row build; the previous version called rowTimer
// twice per instance per frame. Deliberately no per-row render cache: the timer
// changes on every animated frame (so a cache would never hit) and while idle
// refreshList only runs on real events.
func (m *Model) refreshList() {
	timers := make([]string, len(m.instances))
	longestFormattedTime := 0
	for i, r := range m.instances {
		timers[i] = m.rowTimer(r)
		longestFormattedTime = max(longestFormattedTime, len(timers[i]))
	}

	for i, r := range m.instances {
		m.list.SetItemAtIndex(i, InstanceRowSegments(
			r.state.Label(m.bundle), r.state.LabelStyle(m.bundle), m.labelWidth,
			r.Name, m.nameWidth, r.DisplayLogCount(), longestFormattedTime, timers[i],
		))
	}
}

// longestInstanceName returns the width of the instance-name column: the longest
// configured instance name. It measures with len (bytes) exactly as the per-frame
// scan it replaces did — this is a hoist, not a fix, so the padding of a
// non-ASCII name must not shift. The instance set is built once from config by
// NewInstances and never grows or shrinks during a session, so this is a
// construction-time constant rather than a per-frame scan.
func longestInstanceName(instances Instances) int {
	longest := 0
	for _, r := range instances {
		longest = max(longest, len(r.Name))
	}
	return longest
}

// InstanceStatus returns the picker-owned display value for crn. The logviewer
// calls this directly while drawing its adopted store's contextual row.
func (m *Model) InstanceStatus(crn string) (logviewer.InstanceStatus, bool) {
	r := m.instances.FindByCRN(crn)
	if r == nil {
		return logviewer.InstanceStatus{}, false
	}
	phaseStyle := r.state.LabelStyle(m.bundle)
	phaseStyle.Attrs |= uv.AttrReverse
	return logviewer.InstanceStatus{
		Name:       r.Name,
		Phase:      r.state.Label(m.bundle),
		Timer:      m.rowTimer(r),
		PhaseStyle: phaseStyle,
	}, true
}

// rowTimer renders the time column for one instance row. A Watching instance
// (cooldown gap) shows a live countdown to the next retry; every other state
// shows the normal RenderTimer value.
func (m *Model) rowTimer(r *Instance) string {
	if r.state == status.Watching && !m.nextRoundAt.IsZero() {
		return FormatElapsed(r.timerFormat, max(time.Until(m.nextRoundAt), 0))
	}
	return r.RenderTimer()
}

// InstanceRowSegments formats one instance-table row as cell-native styled
// segments: a phase label (left-aligned, padded to labelWidth, foreground-
// colored by phase via labelStyle), then the space-separated (tab-style)
// instance name (left-aligned), log count, and timer (all unstyled — the list
// renderer reverse-videos the cursor row). Columns are separated by two spaces
// rather than pipe bars. It is shared by the live picker (refreshList) and the
// snapshot preview (snapshothandler/preview.go) so both render the instance
// table identically.
func InstanceRowSegments(label string, labelStyle uv.Style, labelWidth int, name string, nameWidth, logCount, timeWidth int, timeStr string) []list.Segment {
	// Pad the label by display width, not fmt's rune count: a wide grapheme
	// (emoji) occupies 2 cells but counts as 1 rune to "%-*s", so byte/rune
	// padding misaligns the column. labelWidth comes from status.MaxLabelWidth,
	// which measures the same way. max() guards an over-wide label (negative pad).
	labelPad := max(labelWidth-uniseg.StringWidth(label), 0)
	return []list.Segment{
		{Text: label + strings.Repeat(" ", labelPad), Style: labelStyle},
		{Text: fmt.Sprintf("  %-*s  %5d  %*s", nameWidth, name, logCount, timeWidth, timeStr)},
	}
}

// OpenInLogViewer emits an InstanceSelectMsg (with OpenLogViewer set) for the
// instance under the cursor. It is a keys.Action (used as a keybind action and
// as the list's fuzzy-confirm action); both invoke it on the loop goroutine, so
// the message is posted off-loop via m.emit.
func (m *Model) OpenInLogViewer() {
	sel := selectMsgFor(m.instances[m.list.GetCursor()])
	sel.OpenLogViewer = true
	m.emit(sel)
}

// OpenInstance lazily loads the named instance's logs into the viewer,
// delivering an InstanceLoadReadyMsg (or InstanceLoadPendingMsg) via the
// poster. All posts go through emit (msgs.PostAsync). On-loop work (guard
// checks, fd open for durable frames, live-store snapshot, pendingLoads
// mutation) stays on the loop goroutine; only the post moves off-loop. If the
// frame is already durable on disk, the backing fd is opened synchronously on
// the loop goroutine (it survives rename(2)) and the read happens off-loop in
// poster.Go. If the frame is in flight (between LogStreamDoneMsg and
// InstanceFlushedMsg), the request is queued (on-loop) and the caller receives
// an InstanceLoadPendingMsg; the load completes when InstanceFlushedMsg arrives
// for the instance.
func (m *Model) OpenInstance(crn string) {
	backingPath := m.GetBackingPath()
	if backingPath == "" {
		m.emit(InstanceLoadReadyMsg{CRN: crn, Err: ErrNoBackingFile})
		return
	}

	inst := m.instances.FindByCRN(crn)
	if inst == nil {
		m.emit(InstanceLoadReadyMsg{CRN: crn, Err: ErrInstanceFailed})
		return
	}

	// Snapshot-only rows always have a declared frame, including an empty frame
	// whose saved message must remain openable. Other rows need a positive
	// flushed count to distinguish a durable frame from an in-flight flush.
	// Open the fd synchronously on the event-loop goroutine, then read in the
	// poster.Go body; the fd survives rename(2) on Unix.
	if inst.IsReadOnly() || inst.flushedLogCount > 0 {
		m.openDurableFrame(crn, inst.CRN, backingPath)
		return
	}

	// No durable frame: a failed, disabled, or cancelled instance with no
	// flushed logs has nothing to show. Reject before the live/pending paths so
	// these never queue a pending load that would hang. (The durable branch
	// above intentionally runs first so a terminal instance whose logs WERE
	// flushed — e.g. a cancelled instance reloaded from a restored snapshot —
	// still surfaces them.)
	if inst.state == status.Error || inst.state == status.Disabled || inst.state == status.Cancelled {
		m.emit(InstanceLoadReadyMsg{CRN: crn, Err: ErrInstanceFailed})
		return
	}

	// Live store has data (query still streaming). Snapshot on-loop; only the
	// post moves off-loop.
	if inst.Store != nil && inst.Store.GetLogCount() > 0 {
		logs, unlock := inst.Store.Snapshotize()
		unlock()
		m.emit(InstanceLoadReadyMsg{CRN: crn, Logs: logs})
		return
	}

	// Pending: in the flush window between LogStreamDoneMsg and InstanceFlushedMsg.
	// The pendingLoads mutation stays on the loop goroutine (single-writer).
	m.pendingLoads[crn] = struct{}{}
	m.emit(InstanceLoadPendingMsg{CRN: crn})
}

// openDurableFrame opens the durable backing fd synchronously on the loop
// goroutine (the caller, OpenInstance, runs there), then reads + posts off-loop.
// The fd survives rename(2) on Unix, so the happy path is immune to a concurrent
// rename. Do NOT move the OpenContainerReadOnlyWithOpts call into poster.Go — it
// would open after a possible concurrent rename and the load would fail. If the
// open fails with fs.ErrNotExist (a rename-notify was dropped and the file was
// renamed away), recovery is deferred OFF-LOOP via recoverAndLoad — never Scan
// on-loop. Any other open error (decode/corruption) is surfaced unchanged.
func (m *Model) openDurableFrame(crn, frameName, backingPath string) {
	c, err := snapshot.OpenContainerReadOnlyWithOpts(backingPath, snapshot.OpenOpts{Lenient: true})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			m.poster.Go(func(ctx context.Context) { m.recoverAndLoad(ctx, crn, frameName) })
			return
		}
		m.logger.Warn("OpenInstance: open backing failed", logging.KeyInstance, m.displayNameForCRN(crn), logging.KeyError, err)
		m.emit(InstanceLoadReadyMsg{CRN: crn, Err: err})
		return
	}
	m.poster.Go(func(ctx context.Context) {
		defer func() { _ = c.Close() }()
		logs, err := snapshot.LoadInstanceLogs(c, frameName)
		if err != nil {
			m.logger.Warn("OpenInstance: load failed", logging.KeyInstance, m.displayNameForCRN(crn), logging.KeyError, err)
			_ = m.poster.PostCritical(ctx, InstanceLoadReadyMsg{CRN: crn, Err: err})
			return
		}
		_ = m.poster.PostCritical(ctx, InstanceLoadReadyMsg{CRN: crn, Logs: logs})
	})
}

// recoverAndLoad runs OFF-LOOP (inside a poster.Go body): the backing was
// renamed out from under us (dropped notify), so re-resolve it by UUID
// (FindByID scans every managed snapshot — must not run on-loop), re-open the
// recovered path, load, and post. Every exit posts an InstanceLoadReadyMsg.
func (m *Model) recoverAndLoad(ctx context.Context, crn, frameName string) {
	newPath, rerr := m.resolveBackingPath()
	if rerr != nil {
		_ = m.poster.PostCritical(ctx, InstanceLoadReadyMsg{CRN: crn, Err: rerr})
		return
	}
	c, oerr := snapshot.OpenContainerReadOnlyWithOpts(newPath, snapshot.OpenOpts{Lenient: true})
	if oerr != nil {
		m.logger.Warn("recoverAndLoad: open recovered backing failed", logging.KeyInstance, m.displayNameForCRN(crn), logging.KeyError, oerr)
		_ = m.poster.PostCritical(ctx, InstanceLoadReadyMsg{CRN: crn, Err: oerr})
		return
	}
	defer func() { _ = c.Close() }()
	logs, lerr := snapshot.LoadInstanceLogs(c, frameName)
	if lerr != nil {
		m.logger.Warn("recoverAndLoad: load failed", logging.KeyInstance, m.displayNameForCRN(crn), logging.KeyError, lerr)
		_ = m.poster.PostCritical(ctx, InstanceLoadReadyMsg{CRN: crn, Err: lerr})
		return
	}
	_ = m.poster.PostCritical(ctx, InstanceLoadReadyMsg{CRN: crn, Logs: logs})
}

// setSnapshotMembers records the declaration attached to the current backing.
// Fetch start provides selected CRNs; restore provides the state-frame records.
func (m *Model) setSnapshotMembers(members []snapshot.InstanceSnapshot) {
	m.snapshotMembers = append([]snapshot.InstanceSnapshot(nil), members...)
	m.hasSnapshotMembers = true
}

// captureSnapshotMembers records the selected fetch membership before auth can
// change row state. Current metadata is captured only as a fallback for a later
// restore under a configuration that no longer contains the member; snapshotMeta
// refreshes metadata from the live instance at finalization.
func (m *Model) captureSnapshotMembers(crns []string) {
	members := make([]snapshot.InstanceSnapshot, 0, len(crns))
	for _, crn := range crns {
		if inst := m.instances.FindByCRN(crn); inst != nil {
			members = append(members, inst.SnapshotizeMeta())
			continue
		}
		members = append(members, snapshot.InstanceSnapshot{CRN: crn})
	}
	m.setSnapshotMembers(members)
}

func (m *Model) snapshotMeta() snapshot.Snapshot {
	members := m.snapshotMembers
	if !m.hasSnapshotMembers {
		members = make([]snapshot.InstanceSnapshot, 0, len(m.instances))
		for _, inst := range m.instances {
			members = append(members, inst.SnapshotizeMeta())
		}
	}
	snaps := make([]snapshot.InstanceSnapshot, 0, len(members))
	for _, member := range members {
		if inst := m.instances.FindByCRN(member.CRN); inst != nil {
			snaps = append(snaps, inst.SnapshotizeMeta())
			continue
		}
		snaps = append(snaps, member)
	}
	return snapshot.Snapshot{
		Query:   m.bundle.State.LastFetchedQuery(),
		Search:  m.bundle.State.Search(),
		JQ:      m.bundle.State.JQ(),
		Filters: m.bundle.State.Filters(),
		InstancePickerSnapshot: snapshot.InstancePickerSnapshot{
			Instances: snaps,
		},
	}
}

// SnapshotMeta returns the current session metadata (query/search/jq/filters +
// per-instance records) for a manual save. It is the exported view of
// snapshotMeta and must be called on the loop goroutine (it reads m.instances
// and m.bundle.State).
func (m *Model) SnapshotMeta() snapshot.Snapshot {
	return m.snapshotMeta()
}

// startFirstFetch starts a one-shot race among the currently enabled rows. When
// none are enabled, it enables every configured row before capturing membership.
func (m *Model) startFirstFetch() error {
	crns := m.instances.EnabledCRNs()
	if len(crns) == 0 {
		m.instances.SetAll(true)
		crns = m.instances.EnabledCRNs()
	}
	if len(crns) == 0 {
		m.showNotice("First fetch", "No configured instances are available to fetch.")
		return nil
	}

	m.firstFetchCandidates = make(map[string]struct{}, len(crns))
	for _, crn := range crns {
		m.firstFetchCandidates[crn] = struct{}{}
	}
	m.firstFetchWinner = ""
	if err := m.startFetch(); err != nil {
		m.firstFetchCandidates = nil
		return err
	}
	return nil
}

// startFetch creates a new .wip backing container in the snapshot directory,
// stores metadata, and starts the parallel instance query. It replaces any
// previously open backing container. Any pending lazy loads are drained (their
// InstanceLoadReadyMsg{Err} are emitted off-loop). Returns an error if the
// backing container could not be created (no message is emitted in that case).
func (m *Model) startFetch() error {
	m.removeReadOnly()
	// A plain (sync) fetch must always run with empty per-instance sources so it
	// never inherits a stale collect queryId and mis-routes StartQuery to
	// icl.FetchBackgroundData. Collect uses startFetchWithSources, which skips this.
	m.instances.ClearSources()
	m.captureSnapshotMembers(m.instances.EnabledCRNs())
	return m.startFetchWithSources()
}

// startFetchWithSources is the shared fetch body: it opens a fresh .wip backing
// container and resolves tokens (which drives the per-instance StartQuery chain).
// It is the SAME path as startFetch EXCEPT it does NOT reset per-instance sources,
// so a caller (collect) that seeded querySource{queryID} on each instance routes
// those instances through icl.FetchBackgroundData instead of icl.Query. A plain
// fetch reaches it only via startFetch, which clears the sources first.
func (m *Model) startFetchWithSources() error {
	m.drainPendingLoads(ErrFetchAborted)
	// The previous backing is abandoned unfinalized here (a fetch started while
	// the old one was still in flight), so discard it rather than merely closing
	// it: a closed-but-kept .wip is an orphan no later finalize can claim.
	m.discardBackingContainer()
	// Invalidate the abandoned fetch's in-flight flushes: they no longer have a
	// container to write into, and their pendingFlushes credits die with them, so
	// the counter starts this fetch at zero. Both must happen before any early
	// return below — the old container is already gone at this point.
	m.fetchEpoch++
	m.pendingFlushes = 0

	m.queryStartTime = time.Now()
	dir, err := snapshot.Dir()
	if err != nil {
		return err
	}

	f, err := snapshot.CreateWip(dir, os.Getpid())
	if err != nil {
		return err
	}
	path := f.Name()

	m.backingFile = f
	m.setBackingPath(path) // expose wip path so OpenInstance can target it
	m.backingContainer = snapshot.NewWriter(f)
	m.fetching = true
	m.fetchCancelled = false // a fresh fetch may notify on done again
	m.authGeneration++
	m.bundle.State.SetLastFetchedQuery(m.bundle.State.Query())

	m.logger.Info("fetch started", "backing_file", path)
	// ResolveTokens is void: it groups enabled instances by env and self-posts
	// per-member/per-env auth messages from one tracked poster goroutine per env
	// (the on-loop prologue, including flipping enabled instances to
	// AuthInProgress, runs synchronously here before the goroutines spawn).
	m.instances.ResolveTokens(m.authCtx, m.authGeneration, m.authManager, m.bundle.State.LastFetchedQuery(), m.poster)
	return nil
}

// instanceCRNs returns the CRNs of an archive's instances, in archive order.
func instanceCRNs(a *archive.Archive) []string {
	crns := make([]string, 0, len(a.Instances))
	for i := range a.Instances {
		crns = append(crns, a.Instances[i].CRN)
	}
	return crns
}

// startCollect streams a READY archive's results into a snapshot via the normal
// .wip -> finalize path. It mirrors startFetch but, instead of running the live
// sync query, seeds each instance's source with its background queryId so
// StartQuery routes to icl.FetchBackgroundData. It enables EXACTLY the archive's
// instances, sets the query (State frame AND the visible editor — the editor owns
// its own buffer, so SetQuery alone would not show it), flags m.collecting, and
// runs startFetchWithSources (the startFetch variant that does NOT clear the
// seeded sources). Errored/cancelled/not-found instances surface their error via
// cb.OnError -> InstanceSnapshot.Message (no separate error frame); an all-errored
// collect is still saved because maybeFinalizeFetch force-finalizes while
// m.collecting. Refused while a fetch/watch is running (collect is a peer mode).
//
// m.collecting is NOT reset here: it is read inside maybeFinalizeFetch
// (force-finalize), so the reset is deferred to the end of maybeFinalizeFetch
// (and cancelAllFetches) — see those functions.
func (m *Model) startCollect(a *archive.Archive) error {
	if m.fetching || m.watching {
		return errCollectBusy
	}
	if err := m.preflightCollect(a); err != nil {
		return err
	}
	m.removeReadOnly()
	// Set the query for the snapshot's state frame AND the visible editor. The
	// editor owns its own buffer and only pushes to State, so SetQuery alone would
	// leave the displayed query stale; SetEditorQueryMsg replaces the editor buffer
	// (and re-stages State) so the user sees a.Query (spec §5.3).
	m.bundle.State.SetQuery(a.Query)
	m.emit(msgs.SetEditorQueryMsg{Query: a.Query})

	// Enable only the archive's instances and seed each instance's stream source
	// with its background queryId (so startFetchWithSources -> ResolveTokens ->
	// StartQuery routes through icl.FetchBackgroundData, not icl.Query).
	m.instances.EnableCRNs(instanceCRNs(a))
	for i := range a.Instances {
		ie := a.Instances[i]
		m.instances.SetSource(ie.CRN, querySource{queryID: ie.QueryID})
	}

	m.captureSnapshotMembers(instanceCRNs(a))
	m.collecting = true
	if err := m.startFetchWithSources(); err != nil {
		// startFetchWithSources failed before any query ran (it could not open the
		// .wip): no finalize will follow, so reset the collect flag here — the usual
		// reset site (maybeFinalizeFetch) is never reached.
		m.collecting = false
		return err
	}
	return nil
}

// preflightCollect verifies every archive resource has a configured picker row
// before collect mutates query or picker state, opens a backing file, authenticates,
// or requests data. Missing resources use cosmetic labels; only colliding compact
// labels include CRNs as ambiguity diagnostics.
func (m *Model) preflightCollect(a *archive.Archive) error {
	effective := config.EffectiveInstances(m.bundle.Config)
	configured := make(map[string]struct{}, len(effective))
	for _, inst := range effective {
		if inst.CRN != nil {
			configured[inst.CRN.String()] = struct{}{}
		}
	}
	missing := make([]string, 0)
	byLabel := make(map[string][]string)
	for _, entry := range a.Instances {
		if _, ok := configured[entry.CRN]; ok {
			continue
		}
		missing = append(missing, entry.CRN)
		label := m.displayNameForCRN(entry.CRN)
		byLabel[label] = append(byLabel[label], entry.CRN)
	}
	if len(missing) > 0 {
		labels := make([]string, 0, len(missing))
		for _, crn := range missing {
			label := m.displayNameForCRN(crn)
			if crns := byLabel[label]; len(crns) > 1 {
				labels = append(labels, fmt.Sprintf("%s (ambiguous archive CRNs: %s)", label, strings.Join(crns, ", ")))
				continue
			}
			labels = append(labels, label)
		}
		return fmt.Errorf("archive instances are not configured: %s", strings.Join(labels, ", "))
	}
	return nil
}

// StartCollect is the exported root seam over startCollect. The Archive tab's
// ArchiveCollectMsg (a ready archive's Enter) is routed by the root ui.Model to
// this method, which streams the archive's background-query results into a
// snapshot via the normal .wip -> finalize path. It returns errCollectBusy when a
// fetch or watch is already running (the root surfaces that as a notice and stays
// on the Archive tab). Called on the loop goroutine; startFetchWithSources spawns
// its own off-loop workers.
func (m *Model) StartCollect(a *archive.Archive) error { return m.startCollect(a) }

// ResolveInstanceToken is the exported root seam over the store-preserving
// per-instance auth resolver. The archive handler's poll path injects it via
// SetTokenResolver: it resolves one instance's bearer token + base URL using the
// picker's own AccountManager WITHOUT clearing stores or starting fetch timers
// (unlike ResolveTokens). Off-loop only (GetAuthToken makes network calls).
func (m *Model) ResolveInstanceToken(ctx context.Context, crn string) (token, url string, err error) {
	return m.instances.resolveInstanceToken(ctx, m.authManager, crn)
}

// maybeFinalizeFetch finalizes the backing container (if any) once all queries
// are done and no flushes remain in flight, then emits a SaveSnapshotMsg
// off-loop when configured. It self-gates on AreAllQueriesDone: a per-member/
// per-env auth-fail handler may call it while sibling members are still
// AuthInProgress or InProgress (streaming), and finalizing then would rename
// the .wip mid-fetch and orphan a still-running member's logs. Safe to call
// from any settle path (stream-done, instance-flush, and the auth-fail/cancel
// handlers).
//
// When every instance came back empty (no live or flushed logs) the fetch has
// nothing worth persisting: the .wip backing container is discarded (closed and
// unlinked) instead of finalized, and the configured auto-snapshot
// (SaveSnapshotMsg) is suppressed. NotifyOnFetchDone still fires — the fetch did
// complete, an empty result is a legitimate outcome to signal.
//
// The NotifyOnFetchDone bell is suppressed on cancellation (m.fetchCancelled): a
// cancel is a direct user action with the results already on screen, and because
// cancelAllFetches flips every query to a terminal state synchronously, the
// AreAllQueriesDone gate would otherwise let each cancelled region's trailing
// LogStreamDoneMsg re-fire the bell.
func (m *Model) maybeFinalizeFetch() {
	if !m.instances.AreAllQueriesDone() {
		return
	}
	if m.pendingFlushes != 0 {
		return
	}
	hasLogs := m.instances.HasAnyLogs()
	// finalized tracks whether THIS call actually renamed the .wip into a saved
	// snapshot. finalizeBackingContainer nils backingContainer, so a re-entrant
	// call (e.g. a cancelled region's trailing LogStreamDoneMsg, which finds the
	// AreAllQueriesDone gate already open) takes neither branch and finalized
	// stays false — that is what makes the snapshot save fire once per fetch
	// rather than once per region. A FAILED finalize also leaves it false: there is
	// no file to refresh the snapshot list for (see finalizeOrNotify).
	finalized := false
	if m.backingContainer != nil {
		// A collect always finalizes (even all-errored / all-empty) so the user gets
		// an error-bearing snapshot carrying the per-instance error messages; only a
		// plain fetch/watch discards an empty .wip. Guard the discard with
		// !m.collecting (spec §13: force-finalize in collect mode).
		if hasLogs || m.collecting {
			finalized = m.finalizeOrNotify()
		} else {
			m.discardBackingContainer() // no logs: throw the .wip away, nothing to save
		}
	}
	m.emitFetchDoneEffects(finalized)
	// LOAD-BEARING reset ordering: m.collecting is read above (force-finalize gate),
	// so it is reset only HERE, at the very end of maybeFinalizeFetch — never before.
	// A re-entrant call (a cancelled region's trailing LogStreamDoneMsg) re-reaches
	// this with finalized=false and the flag already cleared, which is harmless.
	m.collecting = false
	m.finishFirstFetch()
}

// emitFetchDoneEffects emits the config-gated post-finalize side-effects: the
// snapshot-list refresh (only when a file was actually finalized) and the
// fetch-done bell (suppressed on cancel). Split out of maybeFinalizeFetch to keep
// that function's cyclomatic complexity in budget.
func (m *Model) emitFetchDoneEffects(finalized bool) {
	// The save IS the finalize/rename above; SaveSnapshotMsg only refreshes the
	// snapshot list to show the new file. Gate it on finalized so it fires once
	// (when the file is created) and never for an empty, discarded fetch.
	if m.bundle.Config.Core.SaveSnapshotOnFetchDone && finalized {
		m.emit(msgs.SaveSnapshotMsg{})
	}
	if m.bundle.Config.Core.NotifyOnFetchDone && !m.fetchCancelled {
		m.emit(msgs.NotifyMsg{}) // suppressed on cancel: a direct user action, the results are already on screen
	}
}

// finalizeOrNotify finalizes the backing container and reports whether the auto
// snapshot really landed on disk. On failure it tells the user their results were
// NOT persisted (they still hold the logs in memory, so they can save manually
// before quitting) instead of leaving the fetch looking successful. Split out of
// maybeFinalizeFetch to keep that function's cyclomatic complexity in budget.
func (m *Model) finalizeOrNotify() bool {
	if err := m.finalizeBackingContainer(); err != nil {
		var cleanupErr *snapshot.RenameCleanupError
		if errors.As(err, &cleanupErr) {
			m.showNotice("Snapshot", "The auto-snapshot was published, but its WIP could not be cleaned up.\n\n"+err.Error())
			return true
		}
		m.showNotice("Snapshot", "The fetch results could not be saved to an auto-snapshot and exist in memory only — "+
			"save them manually before quitting or they will be lost.\n\n"+err.Error())
		return false
	}
	return true
}

// truncationWarning is the per-instance message recorded when a collect hits
// the fixed v1 row ceiling and the result is therefore truncated.
var truncationWarning = fmt.Sprintf(
	"collected results truncated at %d rows (the v1 collect ceiling); narrow the query (tighter timeframe or filters) to capture the full result",
	icl.SyncQueryLimit,
)

// setTruncationWarning records the truncation warning on an instance's store so it
// rides into InstanceSnapshot.Message at finalize. It appends to any existing
// store message (an errored-but-truncated instance keeps both), mirroring the
// stream-error append shape.
func setTruncationWarning(inst *Instance) {
	msg := truncationWarning
	if existing := inst.Store.GetMessage(); existing != "" {
		inst.Store.SetMessage(existing + "\n---\n" + msg)
		return
	}
	inst.Store.SetMessage(msg)
}

// settle is the single round-completion gate. When not watching, it gates on
// pendingFlushes and finalizes the fetch (the prior maybeFinalizeFetch behavior).
// While watching it waits for the round's async flushes, then evaluates the round
// (continue or end). It self-gates on AreAllQueriesDone so any settle path
// (stream-done, flush, auth-fail, cancel) can call it safely.
func (m *Model) settle() {
	if !m.instances.AreAllQueriesDone() {
		return
	}
	if m.watching {
		if m.pendingFlushes != 0 {
			return
		}
		m.evaluateWatchRound()
		return
	}
	m.maybeFinalizeFetch()
}

// electFirstFetchWinner records the irrevocable winner, stops pending
// authentication, and cancels live loser streams. It deliberately does not set
// fetchCancelled because this is automatic race coordination, not a user cancel.
func (m *Model) electFirstFetchWinner(winner string) {
	m.firstFetchWinner = winner
	if m.cancelAuth != nil {
		m.cancelAuth()
	}
	m.authGeneration++
	m.authCtx, m.cancelAuth = context.WithCancel(m.ctx)
	for crn := range m.firstFetchCandidates {
		if crn == winner {
			continue
		}
		inst := m.instances.FindByCRN(crn)
		if inst == nil {
			continue
		}
		switch inst.state {
		case status.AuthInProgress:
			inst.lastUpdateTime = time.Now()
			inst.state = status.Cancelled
		case status.InProgress:
			inst.CancelQuery()
		case status.Warning:
			inst.CancelQuery()
			inst.lastUpdateTime = time.Now()
			inst.state = status.Cancelled
		case status.Error:
			inst.CancelQuery()
		}
	}
}

// finishFirstFetch clears the one-shot race after snapshot metadata has been
// written or discarded. Terminal losers are demoted only now so their actual
// terminal state remains in the captured snapshot.
func (m *Model) finishFirstFetch() {
	if m.firstFetchCandidates == nil {
		return
	}
	for crn := range m.firstFetchCandidates {
		if crn == m.firstFetchWinner {
			continue
		}
		if inst := m.instances.FindByCRN(crn); inst != nil {
			inst.lastUpdateTime = time.Now()
			inst.state = status.Cancelled
		}
	}
	noWinner := m.firstFetchWinner == ""
	m.firstFetchCandidates = nil
	m.firstFetchWinner = ""
	if noWinner {
		m.showNotice("First fetch", "No logs were returned by the selected instances.")
	}
}

// cancelAllFetches cancels in-flight authentication and streams, then lets the
// ordinary settle path preserve and finalize any logs already flushed to the
// backing container.
func (m *Model) cancelAllFetches() {
	m.fetchCancelled = true // suppress the fetch-done notification (a cancel is a direct user action)
	m.stopWatch()
	if m.cancelAuth != nil {
		m.cancelAuth()
	}
	m.authGeneration++
	m.authCtx, m.cancelAuth = context.WithCancel(m.ctx)
	m.instances.TransitionAuthing("", status.Cancelled) // auth-pending members (incl. a dispatch's AuthInProgress instances)
	m.instances.CancelQuery()                           // in-flight queries (InProgress)
	m.settle()
	// settle() -> maybeFinalizeFetch() normally resets m.collecting (it reads it
	// inside, so the reset is deferred to its end). But settle() can return early
	// (e.g. queries not yet all-done when cancelAllFetches races a flush), so
	// belt-and-braces reset here too: a cancelled collect must never leak m.collecting
	// into the next plain fetch.
	m.collecting = false
	// Release the fetch-lock for an interrupted DISPATCH: dispatch opens no .wip, so
	// the settle()->finalizeBackingContainer path (which clears m.fetching for a real
	// fetch) never runs for it, leaving m.fetching stuck true. A real fetch already
	// cleared it via finalize; this is idempotent.
	m.fetching = false
}

// showNotice emits a single-button informational dialog off-loop. The picker has
// no msgs.NoticeMsg; the existing ShowDialogMsg single-OK-button form is the
// notice mechanism (mirrors OnPasscodeError). Called from on-loop code, so it
// goes off-loop via m.emit (the unbuffered events channel would deadlock on a
// direct post here).
func (m *Model) showNotice(title, message string) {
	m.emit(msgs.ShowDialogMsg{
		Title:   title,
		Message: message,
		Buttons: []msgs.DialogButton{{Label: "OK"}},
	})
}

// dispatchSubmitFn / dispatchSaveFn are the network/IO seams dispatchArchive runs
// OFF-LOOP. They are package vars (not direct icl./archive. calls) only so tests
// can stub the submit + the archive-file write deterministically — the cross-
// package AccountManager token internals are otherwise unreachable from a test in
// this package. Production wiring is the real icl.SubmitBackgroundQuery and
// archive.Create; never reassign them outside tests.
var (
	dispatchSubmitFn = icl.SubmitBackgroundQuery
	dispatchSaveFn   = archive.Create
)

// dispatchArchive submits the current query as an IBM Cloud Logs background
// (archive) query to every ENABLED instance, then writes one archive file
// recording ONLY the successful submissions. It is the Keys.ArchiveDispatch
// action, so it runs ON the loop goroutine: the prologue (busy/zero
// guards, reading the enabled names + CRNs + query) is on-loop, but the
// per-instance auth-resolve + SubmitBackgroundQuery POSTs AND the archive.Save
// run OFF-loop inside m.poster.Go, which posts the result via PostCritical (a
// direct post is correct OFF-loop). Refused while a fetch/watch runs (archive is
// a peer fetch mode); a zero-enabled dispatch shows a distinct "select instances"
// notice, never the all-submits-failed path.
func (m *Model) dispatchArchive() {
	if m.fetching || m.watching {
		m.showNotice("Archive", "cannot dispatch while a fetch or watch is running")
		return
	}
	enabled := m.instances.EnabledCRNs()
	if len(enabled) == 0 {
		// Distinct from the all-submits-failed case handled in OnArchiveDispatched:
		// nothing was selected, so there is nothing to submit.
		m.showNotice("Archive", "select instances first")
		return
	}
	// Lock out fetches/watches (and a second dispatch) for the duration of the
	// submit batch: the user waits for the atomic batch result even if an instance
	// is slow to answer. Symmetric with the busy guard above (which refuses dispatch
	// while m.fetching/m.watching). Cleared when the last DispatchInstanceDoneMsg
	// lands (OnDispatchInstanceDone) or when an interrupted dispatch is cancelled
	// (cancelAllFetches). Dispatch opens NO .wip and streams NO logs, so the usual
	// finalize path (which clears m.fetching) never runs for it.
	m.fetching = true
	// On-loop prologue: flip the enabled instances to the auth/in-progress phase
	// (padlock + spinner) and start their timer so the dispatch VISIBLY behaves
	// like a fetch in flight. This does NOT clear stores or open a .wip — dispatch
	// submits background queries, it does not stream logs. Each instance is flipped
	// to a terminal Success/Error badge as its submit completes (DispatchInstanceDoneMsg),
	// which freezes the timer and lets IsAnimating settle once all submits finish.
	m.instances.StartDispatchTimers()
	query := m.bundle.State.Query()
	am := m.authManager
	now := time.Now()
	poster := m.poster
	// Govern the submit batch's IO with authCtx (not the long-lived poster ctx) so a user
	// cancel (cancelAllFetches -> m.cancelAuth) aborts in-flight resolves/submits AND stops
	// the loop from dispatching NEW background queries for the rest of the batch. PostCritical
	// stays on the poster ctx (must-deliver); the FinishDispatch cancel guard drops any late
	// done-message for a row cancelAllFetches already froze.
	authCtx := m.authCtx
	m.poster.Go(func(ctx context.Context) {
		results := make([]archive.InstanceEntry, 0, len(enabled))
		for _, crn := range enabled {
			if authCtx.Err() != nil {
				// Cancelled mid-batch: cancelAllFetches already flipped the remaining
				// AuthInProgress rows to Cancelled and released m.fetching. Stop without
				// submitting more background queries or persisting a partial archive.
				return
			}
			token, url, err := m.instances.resolveInstanceToken(authCtx, am, crn)
			if err != nil {
				m.logger.Error("archive dispatch: auth failed", logging.KeyError, err, logging.KeyInstance, m.displayNameForCRN(crn))
				_ = poster.PostCritical(ctx, DispatchInstanceDoneMsg{CRN: crn, OK: false})
				continue // dropped (successful-only); aggregate failure reported in the handler
			}
			id, err := dispatchSubmitFn(authCtx, token, url, query)
			if err != nil {
				m.logger.Error("archive dispatch: submit failed", logging.KeyError, err, logging.KeyInstance, m.displayNameForCRN(crn))
				_ = poster.PostCritical(ctx, DispatchInstanceDoneMsg{CRN: crn, OK: false})
				continue
			}
			_ = poster.PostCritical(ctx, DispatchInstanceDoneMsg{CRN: crn, OK: true})
			results = append(results, archive.InstanceEntry{
				CRN:     crn,
				QueryID: id,
				State:   archive.StateRunning,
			})
		}
		if authCtx.Err() != nil {
			return // cancelled after the last submit but before the write: abort the batch
		}
		msg := ArchiveDispatchedMsg{Succeeded: len(results), Attempted: len(enabled)}
		if len(results) > 0 {
			// Successful-only: atomically reserve and write the archive file off-loop
			// (§13 — keep IO off the loop). The on-loop handler only notices + refreshes.
			a := &archive.Archive{
				Name:        archive.DefaultName(now),
				Query:       query,
				SubmittedAt: now,
				Instances:   results,
			}
			if err := dispatchSaveFn(a); err != nil {
				m.logger.Error("archive dispatch: save failed", logging.KeyError, err)
			} else {
				msg.Saved = a.Name
			}
		}
		_ = m.poster.PostCritical(ctx, msg)
	})
}

// OnArchiveDispatched is the on-loop handler for a completed dispatch: it renders
// the result notice (the archive.Save already ran off-loop). A saved archive
// reports the success/attempt counts; zero successes reports a distinct
// all-submits-failed message; a non-empty result that failed to persist reports
// the write failure.
// On SUCCESS there is NO dialog (the success notice was removed): the root instead
// switches to the Archive tab and selects the new archive row (see the root's
// ArchiveDispatchedMsg handler). Only the two distinct ERROR cases still notice.
func (m *Model) OnArchiveDispatched(msg ArchiveDispatchedMsg) {
	switch {
	case msg.Succeeded == 0:
		m.showNotice("Archive", "all submits failed — no archive written")
	case msg.Saved == "":
		m.showNotice("Archive", "submitted but failed to write the archive file")
	}
}

// OnDispatchInstanceDone is the on-loop handler for one instance's dispatch
// SUBMIT outcome: it flips the instance to its terminal badge (Success/Error),
// freezes its timer, and syncs the State frame. Once every dispatched instance is terminal,
// IsAnimating settles, the redraw ticker stops, AND the dispatch fetch-lock
// (m.fetching, set in dispatchArchive) is released so fetches/watches are allowed
// again — the row reads like a quick fetch that completed. The root dispatches it.
func (m *Model) OnDispatchInstanceDone(msg DispatchInstanceDoneMsg) {
	inst := m.instances.FindByCRN(msg.CRN)
	if inst == nil {
		return
	}
	inst.FinishDispatch(msg.OK)
	m.syncInstanceState()
	// Release the dispatch fetch-lock once the whole submit batch has settled.
	// Dispatch opens no .wip, so the normal finalize path never clears m.fetching;
	// this is the only release on the happy path. AreAllQueriesDone is the same gate
	// IsAnimating uses, so the lock clears exactly when the spinner/timer stop.
	if m.instances.AreAllQueriesDone() {
		m.fetching = false
	}
}

// Close shuts down every instance, flushes and closes the backing snapshot
// container if open, and returns errors.Join of every non-nil error so the
// caller sees all failures (not just the first). Each error is also logged
// at error level via the picker's slog logger. Safe to call multiple times.
// CancelQueries aborts every in-flight ICL operation without closing stores or
// flushing files: it cancels authCtx (aborting any in-flight GetAuthToken — token
// resolution or single-instance retry) and each instance's background-derived
// query ctx. Used by the runtime teardown to abort streaming queries AND auth
// calls before waiting on the poster, so a quit-during-fetch (or
// quit-during-resolution) drains promptly instead of blocking on an uncancelled
// network read. Idempotent: a context CancelFunc is safe to call repeatedly.
func (m *Model) CancelQueries() {
	m.watching = false
	m.watchEpoch++
	if m.cancelAuth != nil {
		m.cancelAuth()
	}
	m.instances.CancelQuery()
}

func (m *Model) Close() error {
	m.watching = false
	m.watchEpoch++
	// Cancel any in-flight auth (token resolution / retry) so Close is a complete
	// teardown even when called without a preceding CancelQueries. Idempotent.
	if m.cancelAuth != nil {
		m.cancelAuth()
	}
	var errs []error
	for _, inst := range m.instances {
		if err := inst.Close(); err != nil {
			wrapped := fmt.Errorf("instance %q close: %w", inst.Name, err)
			m.logger.Error("instance close failed", logging.KeyError, wrapped)
			errs = append(errs, wrapped)
		}
	}
	if err := m.closeBackingContainer(); err != nil {
		m.logger.Error("backing container close failed", logging.KeyError, err)
		errs = append(errs, err)
	}
	// Synchronous best-effort removal of this session's .inuse guard file.
	// Correctness on crash is provided by pidAlive + startup CleanStaleInUse.
	// Unconditional and idempotent: a backstop for the no-backing-open case —
	// SetOwnInUse("") masks ErrNotExist, so a missing guard is not an error.
	if err := snapshot.SetOwnInUse(""); err != nil {
		m.logger.Error("failed to clear in-use guard on close", logging.KeyError, err)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// closeBackingContainer flushes and closes any open backing container and
// file, resets the fetching flag, and clears m.backingPath. The container
// is closed first (finalizes the snapshot stream), then the file is
// fsync'd, then closed. Returns errors.Join of every non-nil error from
// that chain; each error is also logged at error level. Safe to call when
// no container is open (returns nil).
func (m *Model) closeBackingContainer() error {
	var errs []error
	if m.backingContainer != nil {
		if err := m.backingContainer.Close(); err != nil {
			m.logger.Error("backing container close failed", logging.KeyError, err)
			errs = append(errs, err)
		}
		m.backingContainer = nil
	}
	if m.backingFile != nil {
		if err := m.backingFile.Sync(); err != nil {
			m.logger.Error("backing file sync failed", logging.KeyError, err)
			errs = append(errs, err)
		}
		if err := m.backingFile.Close(); err != nil {
			m.logger.Error("backing file close failed", logging.KeyError, err)
			errs = append(errs, err)
		}
		m.backingFile = nil
	}
	m.fetching = false
	// Teardown clears the id inline rather than via setBacking deliberately:
	// setBacking now fires notifyDirty (a poster.Go), and posting after
	// drainPoster/Wait at shutdown would be a use-after-drain hazard. Clearing
	// backingID here preserves the "backingID nil iff backingPath nil" invariant.
	m.setBackingPath("")
	m.backingID.Store(nil)
	return errors.Join(errs...)
}

// discardBackingContainer closes the open .wip backing container and removes it
// from disk without finalizing. Used when a fetch (or watch) completes with no
// logs across any instance, and when a new fetch abandons a still-open one:
// closeBackingContainer alone clears backingPath but does NOT unlink the file,
// so the .wip would otherwise be orphaned (the next startFetch opens a fresh
// timestamped path) until some future process's CleanStaleWip. Safe to call when
// no container is open.
//
// Only a .wip is ever removed: finalizeBackingContainer renames the file and
// nils backingFile, so a finalized snapshot is never a deletion target — the
// suffix check is the explicit guard on that.
func (m *Model) discardBackingContainer() {
	wipPath := ""
	if m.backingFile != nil {
		wipPath = m.backingFile.Name()
	}
	_ = m.closeBackingContainer() // closes fd, clears backingPath + own .inuse
	if !strings.HasSuffix(wipPath, filehandler.WipMarker) {
		return
	}
	if err := os.Remove(wipPath); err != nil && !os.IsNotExist(err) {
		m.logger.Warn("failed to remove discarded backing container", "path", wipPath, logging.KeyError, err)
	}
}

// drainPendingLoads emits InstanceLoadReadyMsg{Err: cause} off-loop for every
// queued pending load and clears the map. The map mutation runs on the loop
// goroutine (single-writer); only the posts move off-loop via m.emit.
func (m *Model) drainPendingLoads(cause error) {
	pending := m.pendingLoads
	m.pendingLoads = map[string]struct{}{}

	for crn := range pending {
		m.emit(InstanceLoadReadyMsg{CRN: crn, Err: cause})
	}
}

// finalizeBackingContainer writes, syncs, and closes the WIP before publishing
// it at a preclaimed automatic destination. Retention completes while the
// destination remains claimed, before setBacking notifies peers.
func (m *Model) finalizeBackingContainer() error {
	// Mint the snapshot's stable UUID here so the value we persist in the state
	// frame is the same one we record as the live backing's id (notify-loss
	// recovery). SaveStateFrame preserves a pre-set ID; passing it explicitly
	// keeps the persisted id reachable at the setBacking call site below.
	meta := m.snapshotMeta()
	if err := snapshot.EnsureInstanceFrames(m.backingContainer, meta.InstancePickerSnapshot.Instances); err != nil {
		m.logger.Error("failed to write empty instance frames", logging.KeyError, err)
		_ = m.closeBackingContainer() // clears backingPath + .inuse
		return fmt.Errorf("write empty instance frames: %w", err)
	}
	meta.ID = uuid.NewString()
	if err := snapshot.SaveStateFrame(m.backingContainer, meta); err != nil {
		m.logger.Error("failed to write state frame", logging.KeyError, err)
		_ = m.closeBackingContainer() // clears backingPath + .inuse
		return fmt.Errorf("write state frame: %w", err)
	}

	wipPath := m.backingFile.Name()
	if err := m.closeBackingContainer(); err != nil {
		return fmt.Errorf("close backing container: %w", err)
	}

	preclaim := func(candidate string) error {
		if !isInSnapshotDir(candidate) {
			return nil
		}
		return setOwnInUse(candidate)
	}
	finalPath, publishErr := publishAutoSnapshot(wipPath, m.queryStartTime, preclaim)
	if finalPath == "" {
		_ = setOwnInUse("") // undo a failed preclaim
		return fmt.Errorf("publish backing container: %w", publishErr)
	}

	// startFetch always creates managed backing files. Keep the guard so a
	// direct, external backing used by tests cannot mutate the managed directory.
	if isInSnapshotDir(finalPath) {
		if _, err := snapshot.RetainAutoSnapshots(m.bundle.Config.Core.MaxAutoSnapshots); err != nil {
			m.logger.Warn("auto snapshot retention failed", logging.KeyError, err)
		}
	}
	m.setBacking(finalPath, meta.ID) // preserves the successful preclaim
	m.logger.Info("backing container finalized", "path", finalPath)
	if publishErr != nil {
		return fmt.Errorf("publish backing container cleanup: %w", publishErr)
	}
	return nil
}

// passcodeDialogCmd builds the passcode-entry dialog and emits the ShowDialogMsg
// off-loop via the poster (p). It runs from Update on the loop goroutine, so the
// outer dialog delivery cannot be a returned cmd and a bare on-loop PostCritical
// would deadlock the unbuffered events channel; the ShowDialogMsg is therefore
// posted from a poster.Go body. The OK button's pr.SetPasscode performs network
// IO, so it runs off-loop via the poster too: the dialog confirm path invokes the
// button Cmd on the loop goroutine, where a blocking network call would stall
// dispatch. Cancellation (button or OnCancel) also runs on the loop when the
// dialog dismisses, so PasscodeCancelledMsg is posted off-loop too.
func passcodeDialogCmd(
	ctx context.Context,
	p msgs.Poster,
	am *icl.AccountManager,
	pr *icl.PasscodeRequired,
	shouldOpenBrowser bool,
	openBrowser func(context.Context, string) error,
) {
	in := uiinput.New()
	in.SetMasked(true)
	in.SetMaxLen(10)

	// The Message stays plain text (no SGR/OSC8 escapes — the dialog renders
	// cell-natively and would print escape bytes literally). LinkURL marks the
	// URL line so the dialog colors it (UrlFg), underlines it, and attaches a
	// real OSC8 hyperlink cell-side, making it look and behave like a link.
	passcodeURL := pr.GetPasscodeURL()

	env := pr.Env()
	// cancel posts PasscodeCancelledMsg. It is used both as the Cancel button's
	// effect and as OnCancel; both run on the loop when the dialog dismisses,
	// hence msgs.PostAsync.
	cancel := func() {
		msgs.PostAsync(p, PasscodeCancelledMsg{Env: env})
	}

	dlg := msgs.ShowDialogMsg{
		Title:   "Passcode Required",
		Message: "Get passcode from:\n" + passcodeURL,
		LinkURL: passcodeURL,
		Input:   in,
		Buttons: []msgs.DialogButton{
			{Label: "OK", Cmd: func(val string) error {
				p.Go(func(gctx context.Context) {
					if err := am.SetPasscode(ctx, env, val); err != nil {
						_ = p.PostCritical(gctx, PasscodeErrorMsg{Env: env, Err: err})
						return
					}
					_ = p.PostCritical(gctx, PasscodeSuccessMsg{Env: env})
				})
				return nil
			}},
			{Label: "Cancel", Cmd: func(string) error { cancel(); return nil }},
		},
		OnCancel: cancel,
	}
	msgs.PostAsync(p, dlg)
	if shouldOpenBrowser && openBrowser != nil && p != nil {
		p.Go(func(openCtx context.Context) {
			if err := openBrowser(openCtx, passcodeURL); err != nil {
				slog.Warn("could not open passcode browser", logging.KeyComponent, "instancepicker", logging.KeyError, err)
			}
		})
	}
}

// watchCapHit reports whether the watch has reached its fetch-count or
// wall-clock cap. A cap of 0 means unlimited for that dimension.
func (m *Model) watchCapHit() bool {
	maxF := int(m.bundle.Config.Core.WatchMaxFetches)
	maxD := time.Duration(m.bundle.Config.Core.WatchMaxDurationSeconds) * time.Second
	return (maxF > 0 && m.roundCount >= maxF) ||
		(maxD > 0 && time.Since(m.watchStart) >= maxD)
}

// isSnapshotMember reports whether name belongs to the current backing's
// declaration. Before a fetch establishes one, every configured row is eligible.
func (m *Model) isSnapshotMember(crn string) bool {
	if !m.hasSnapshotMembers {
		return true
	}
	for _, member := range m.snapshotMembers {
		if member.CRN == crn {
			return true
		}
	}
	return false
}

// watchEmpties returns the enabled, non-errored captured members that have no
// logs. It uses DisplayLogCount(), NOT Store.GetLogCount(): the watch holds the
// backing container open, so a successful instance is flushed and ClearData()'d
// in OnLogStreamDone — its live store reads 0 while DisplayLogCount() (which
// falls back to flushedLogCount) still reports the real count. Detecting via
// GetLogCount() would re-classify every just-succeeded instance as empty and
// loop forever.
func (m *Model) watchEmpties() []*Instance {
	var empties []*Instance
	for _, inst := range m.instances {
		if m.isSnapshotMember(inst.CRN) && inst.IsEnabled() && inst.state != status.Error && inst.DisplayLogCount() == 0 {
			empties = append(empties, inst)
		}
	}
	return empties
}

// evaluateWatchRound runs at round completion (all queries done). It counts the
// round, then: ends the watch if every instance is terminal (logs or error),
// ends it if a cap is hit, otherwise flips the still-empty instances to Watching
// and schedules the next round after the cooldown.
func (m *Model) evaluateWatchRound() {
	m.roundCount++
	empties := m.watchEmpties()
	if len(empties) == 0 {
		m.endWatch("all instances terminal")
		return
	}
	if m.watchCapHit() {
		m.endWatch("cap reached")
		return
	}
	for _, e := range empties {
		e.state = status.Watching
	}
	m.scheduleNextRound()
}

// scheduleNextRound waits the configured cooldown off-loop, then posts a
// WatchTickMsg stamped with the current epoch. The PostCritical runs inside the
// poster.Go body (off-loop), honoring the no-on-loop-post invariant. nextRoundAt
// drives the live countdown on the Watching rows.
func (m *Model) scheduleNextRound() {
	epoch := m.watchEpoch
	cooldown := time.Duration(m.bundle.Config.Core.WatchCooldownSeconds) * time.Second
	m.nextRoundAt = time.Now().Add(cooldown)
	m.poster.Go(func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cooldown):
		}
		_ = m.poster.PostCritical(ctx, WatchTickMsg{Epoch: epoch})
	})
}

// endWatch stops the watch and finalizes the held-open backing container once,
// firing the configured snapshot/notify/progress side-effects (all instances are
// terminal now, so maybeFinalizeFetch's AreAllQueriesDone gate passes). The epoch
// bump invalidates any still-scheduled tick.
func (m *Model) endWatch(reason string) {
	m.watching = false
	m.watchEpoch++
	m.nextRoundAt = time.Time{}
	m.logger.Info("watch ended", "reason", reason, "rounds", m.roundCount)
	m.maybeFinalizeFetch()
}

// startWatch begins a watch: it marks the model watching, resets the round
// counter and clock, bumps the epoch, then runs a normal startFetch (round 1 ==
// a fetch of all enabled instances). The backing container startFetch opens is
// held open across rounds and finalized only at endWatch.
func (m *Model) startWatch() {
	m.watching = true
	m.watchEpoch++
	m.watchStart = time.Now()
	m.roundCount = 0
	m.nextRoundAt = time.Time{}
	if err := m.startFetch(); err != nil {
		m.logger.Error("failed to start watch fetch", logging.KeyError, err)
		// Partial rollback by design: only watching is reset. The bumped
		// watchEpoch, watchStart, and zeroed nextRoundAt are left as benign
		// neutral values — startFetch failed before reaching scheduleNextRound, so
		// there is no scheduled tick to invalidate.
		m.watching = false
	}
}

// stopWatch ends a watch that may be mid-round or mid-cooldown: it clears the
// watching flag, bumps the epoch (so any scheduled tick is dropped), and freezes
// any cooldown-gap (Watching) instances to Cancelled. The caller drives finalize
// via settle().
func (m *Model) stopWatch() {
	if !m.watching {
		return
	}
	m.watching = false
	m.watchEpoch++
	m.nextRoundAt = time.Time{}
	for _, inst := range m.instances {
		if inst.state == status.Watching {
			inst.lastUpdateTime = time.Now()
			inst.state = status.Cancelled
		}
	}
}

// IsWatching reports whether a watch is currently active. Exported so the root
// model / routing tests can observe watch state.
func (m *Model) IsWatching() bool { return m.watching }

// ToggleWatch is the watch keybind action: while watching it stops (and settles,
// finalizing the held-open backing container); otherwise it starts a watch,
// guarded like FetchLogs. The watching check comes BEFORE the AreAllQueriesDone
// guard because a Watching instance counts as not-done and would otherwise block
// the stop.
func (m *Model) ToggleWatch() {
	if m.watching {
		m.stopWatch()
		m.settle()
		return
	}
	if !m.instances.AreAllQueriesDone() {
		m.logger.Warn("watch blocked, queries still in progress")
		return
	}
	m.startWatch()
}

// OnWatchTick handles a cooldown-elapsed tick: it drops stale ticks (wrong epoch
// or watch already stopped), re-checks the duration cap (the cooldown may have
// crossed the deadline), then starts the next fetch round for the still-empty
// (Watching) instances.
func (m *Model) OnWatchTick(msg WatchTickMsg) {
	if !m.watching || msg.Epoch != m.watchEpoch {
		return
	}
	if m.watchCapHit() {
		m.endWatch("cap reached")
		return
	}
	m.startWatchRound()
	m.syncInstanceState()
}

// startWatchRound re-fetches only the Watching instances into the EXISTING
// backing container (it must NOT call startFetch, which would recreate the
// container and lose earlier frames). It mirrors the no-startFetch shape of the
// RetryFailedFetch path, but re-uses LastFetchedQuery (the query the watch was
// started with), not the live State.Query(). flushedLogCount is not reset: a
// Watching instance never flushed, so it is already 0.
func (m *Model) startWatchRound() {
	m.nextRoundAt = time.Time{}
	grouped := make(map[icl.Environment][]string)
	for _, inst := range m.instances {
		if m.isSnapshotMember(inst.CRN) && inst.state == status.Watching {
			inst.ClearStore()
			inst.StartAuthTimer()
			grouped[inst.env] = append(grouped[inst.env], inst.CRN)
		}
	}
	if len(grouped) == 0 {
		m.endWatch("nothing to watch")
		return
	}
	q := m.bundle.State.LastFetchedQuery()
	for env, members := range grouped {
		m.instances.resolveEnvMembers(m.authCtx, m.authGeneration, m.authManager, env, members, q, m.poster)
	}
}

func (m *Model) saveSession() {
	tokens := m.authManager.GetRefreshTokens()
	if maps.Equal(tokens, m.lastSavedSession) {
		return
	}

	// Resolve through the same resolver New() and `lognav logout` use, so the
	// three can never disagree about which file the session lives in. The
	// resolver is pure; SaveSession owns directory preparation and persistence.
	sessionPath, err := icl.SessionPath()
	if err != nil {
		m.logger.Error("failed to get session path", logging.KeyError, err)
		return
	}
	if err := icl.SaveSession(sessionPath, tokens); err != nil {
		m.logger.Error("failed to save session", logging.KeyError, err)
		return
	}
	m.lastSavedSession = maps.Clone(tokens)
}

func (m *Model) SetAPIKey(env icl.Environment, key string) {
	m.authManager.SetAPIKey(env, key)
}

func (m *Model) syncInstanceState() {
	infos := make([]state.InstanceInfo, len(m.instances))
	for i, inst := range m.instances {
		infos[i] = state.InstanceInfo{
			Name:     inst.Name,
			Enabled:  inst.IsEnabled(),
			State:    inst.state.String(),
			LogCount: inst.Store.GetLogCount(),
		}
	}
	m.bundle.State.SetInstances(infos)
}
