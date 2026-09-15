package logviewer

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"slices"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/timeline"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/numeric"
	uv "github.com/charmbracelet/ultraviolet"
	lru "github.com/hashicorp/golang-lru/v2"
)

const clippy = `╭──╮     ╭─────────────────────────────────────────────────────────────╮
│  │     │ It looks like you forgot to fetch logs.                     │
@  @  ╭  │ You can do that in the instances tab by pressing f.         │
││ ││ │  │ Need help? Press '?', check out the docs in the lognav repo │
││ ││ ╯  │ or just ask in the #lognav-users slack channel.             │
│╰─╯│    │                                                             │
╰───╯    ╰─────────────────────────────────────────────────────────────╯`

type indexMap map[int]bool

func (im indexMap) Set(i int, b bool) {
	switch {
	case !b && im[i]:
		delete(im, i)
	case b && !im[i]:
		im[i] = true
	}
}

type State struct {
	expanded indexMap
	marked   indexMap
	filtered indexMap
	search   SearchMap
}

func newState() State {
	return State{
		expanded: make(indexMap),
		marked:   make(indexMap),
		filtered: make(indexMap),
		search:   make(SearchMap),
	}
}

// sidebarWidth is the fixed width of the gutter rendered to the left of each
// log row. It hosts the per-log severity indicator.
const sidebarWidth = 1

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.KeyTarget            = (*Model)(nil)
	_ component.MouseTarget          = (*Model)(nil)
	_ component.SecondaryMouseTarget = (*Model)(nil)
	_ component.DoubleClickTarget    = (*Model)(nil)
	_ component.MousePasteTarget     = (*Model)(nil)
	_ component.ScrollTarget         = (*Model)(nil)
	_ component.MouseHoverTarget     = (*Model)(nil)
	_ component.PasteTarget          = (*Model)(nil)
	_ component.FocusReleaser        = (*Model)(nil)
	_ component.ContextMenuOpener    = (*Model)(nil)
	_ component.StatusProvider       = (*Model)(nil)
)

type Model struct {
	bundle         deps.Bundle
	drawRect       component.Rect
	kh             *keys.Handler
	alwaysHandleKH *keys.Handler
	logger         *slog.Logger

	store *LogStore

	cursor       *logsCursor
	xOffset      int
	currentMatch int // 1-based pos of the last n/N jump; 0 before any jump
	// storeGen is the store queryID the row-indexing view state (cursor,
	// currentMatch) was last aligned to. LogStore bumps queryID on every
	// Clear/ClearData/StartStream, so a store whose generation moved since the
	// last adoption holds an entirely different set of rows and every row index
	// the viewer carries is stale. Compared in SetStore; see the reset there.
	storeGen uint64

	// hover is the log row currently under the mouse pointer (experimental,
	// gated by Core.EnableHover). When hasHover is set the matching row is tinted
	// with Style.HoverRowBg in the render pass; the cursor row is never tinted.
	hoverLog, hoverLine int
	hasHover            bool

	ruleEngine *colorRuleEngine // per-Model color rule engine built from bundle.Config

	renderCache *lru.Cache[int, []tokenizedLine]

	timeline       *timeline.Model
	timelineActive bool

	poster msgs.Poster

	// statusLookup resolves the picker-owned presentation state for the
	// currently adopted store. It is called synchronously during drawing.
	statusLookup func(string) (InstanceStatus, bool)
}

const defaultRenderCacheSize = 128

func New(bundle deps.Bundle) *Model {
	m := &Model{
		bundle: bundle,
		cursor: &logsCursor{},
		logger: slog.Default().With(logging.KeyComponent, "logviewer"),
	}

	// Build the color rule engine from this bundle's config (per-Model, not a
	// process-global singleton).
	m.ruleEngine = newColorRuleEngine(resolveColorRules(
		bundle.Config.Logs.DefaultColorRules,
		bundle.Config.Logs.ExtraColorRules,
		bundle.Config.Logs.IncludeDefaultColorRules,
	))

	size := bundle.Config.Logs.RenderCacheSize
	if size <= 0 {
		if size < 0 {
			m.logger.Warn("renderCacheSize <= 0, using default",
				"configured", size, "default", defaultRenderCacheSize)
		}
		size = defaultRenderCacheSize
	}
	renderCache, _ := lru.New[int, []tokenizedLine](size)
	m.renderCache = renderCache

	bundle.State.SetJQ(bundle.Config.Logs.JqDefaultQuery)

	m.timeline = timeline.New(bundle)
	bindKeyhandlersToModel(m)
	return m
}

// SetPoster injects the runtime poster used to post events off-loop (R5a).
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.timeline.SetPoster(p)
}

// ReleaseFocus implements component.FocusReleaser. The logviewer no longer owns
// focusable inline input (search/jq now open as overlay dialogs); there is
// nothing to blur, so this is a no-op kept only to satisfy the interface.
func (m *Model) ReleaseFocus() {}

// requestQuit requests a clean exit. It is the quit hook wired into the Quit
// keybind; keybind actions run on the loop goroutine, hence msgs.RequestQuit.
// It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

func (m *Model) Init() {}

// OnJQChunk applies a jq chunk result to the store: invalidates affected render
// cache entries, clamps the cursor line when the cursor's log shrank, and
// refreshes the display. Cursor eviction now hangs solely off the filter chunk
// path (HandleFilterChunk). No-op when no store is set. Mirrors the previous
// Update arm; the root calls it directly (B1 direct-dispatch).
func (m *Model) OnJQChunk(msg JQChunkMsg) {
	if m.store == nil {
		return
	}
	result := m.store.HandleJQChunk(msg)
	for _, idx := range result.CacheInvalidated {
		m.renderCache.Remove(idx)
	}

	// log under cursor was affected
	offsetIdx := result.ChunkOffset * int(m.bundle.Config.Logs.BatchOpChunkSize)
	if offsetIdx <= m.cursor.log && offsetIdx+result.ResultCount > m.cursor.log {
		lines := m.getRenderCacheEntry(m.cursor.log)
		// cursor now overflows
		if len(lines) <= m.cursor.logLine {
			m.cursor.logLine = len(lines) - 1
		}
	}

	m.updateDisplay()
}

// OnSearchChunk applies a search chunk result to the store. HandleSearchChunk
// reports whether the chunk was processed; only then is the display refreshed.
// Cursor eviction now hangs solely off the filter chunk path
// (HandleFilterChunk). No-op when no store is set. Mirrors the previous Update
// arm.
func (m *Model) OnSearchChunk(msg SearchChunkMsg) {
	if m.store == nil {
		return
	}
	if !m.store.HandleSearchChunk(msg) {
		return
	}
	m.updateDisplay()
}

// HandleFilterChunk applies one filter chunk's hide decisions to s.state.filtered,
// moves the cursor off any newly-hidden log, and refreshes the display. It DROPS
// the chunk when msg.ID != s.queryID (the store was cleared — the documented
// jqchunk stale-chunk panic guard) OR when msg.Epoch != s.filterEpoch (a jq
// re-run or filter recompute superseded the worker's data/rules without bumping
// queryID), and bounds-checks before indexing. No-op when no store is set. This
// keeps the raw cursor valid for the contextual row after a filter changes.
func (m *Model) HandleFilterChunk(msg FilterChunkMsg) {
	if m.store == nil {
		return
	}
	s := m.store
	if msg.Instance != s.identity {
		return
	}
	// Stale-chunk guard: ClearData bumps queryID and nils s.logs; a worker that
	// finished against pre-Clear data carries the old queryID and would index the
	// emptied slice (the jqchunk stale-chunk panic class).
	if msg.ID != s.queryID {
		return
	}
	// Epoch guard: a jq re-run or a filter recompute bumps filterEpoch without
	// changing queryID, so a worker that computed against superseded data/rules
	// carries a stale epoch and must be dropped.
	if msg.Epoch != s.filterEpoch {
		return
	}
	if msg.Err != nil {
		if errors.Is(msg.Err, context.Canceled) || errors.Is(msg.Err, context.DeadlineExceeded) {
			s.logger.Debug("filter chunk cancelled", logging.KeyInstance, s.displayName, logging.KeyError, msg.Err)
		} else {
			s.logger.Error("filter chunk failed", logging.KeyInstance, s.displayName, logging.KeyError, msg.Err)
		}
		return
	}
	offsetIdx := msg.Offset * int(m.bundle.Config.Logs.BatchOpChunkSize)
	// Defense-in-depth bounds check (mirrors HandleJQChunk): s.logs stays
	// full-length (== capacity), so bound against logCount (the populated range),
	// not len(s.logs), so a misdispatched chunk cannot touch a zero-value tail
	// entry.
	logCount := int(s.logCount.Load())
	if offsetIdx+len(msg.Hidden) > logCount {
		s.logger.Warn("filter chunk out of range; dropped",
			"offset", msg.Offset, logging.KeyCount, len(msg.Hidden), "logs", logCount)
		return
	}
	for i, hide := range msg.Hidden {
		s.state.filtered.Set(offsetIdx+i, hide)
	}
	s.markFilteredTotalDirty()
	// Cursor eviction remains on the FILTER path, not jq or search.
	m.moveCursorOutOfFilter()
	m.updateDisplay()
}

// OnFilterApplied runs the full filter recompute (filter-menu Apply / direct
// context-menu rule append). It rebuilds s.state.filtered from state.Filters()
// and evicts the cursor off any now-hidden log. No-op when no store is set.
func (m *Model) OnFilterApplied() {
	if m.store == nil {
		return
	}
	m.store.ApplyFilters()
	// moveCursorOutOfFilter keeps the raw cursor valid after the filter changes.
	m.moveCursorOutOfFilter()
	m.updateDisplay()
}

// OnSnapshotRestore is a no-op because the contextual provider reads
// search/jq/filters directly from state; restored values appear on the next Draw.
func (m *Model) OnSnapshotRestore(_ msgs.SnapshotRestoreMsg) {}

// OnViewInContext sets the context root log id on the store so the in-context
// query anchors on it. No-op when no store is set. Mirrors the previous Update
// arm.
func (m *Model) OnViewInContext(msg msgs.ViewInContextMsg) {
	if m.store != nil {
		m.store.SetContextRootLogID(msg.LogID)
	}
}

// OnPaste is a no-op: the inline search/jq bars are gone; overlay dialogs handle
// bracketed paste themselves.
func (m *Model) OnPaste(_ uv.Event) {}

// HandleKey dispatches a key event through logviewer's precedence chain:
// alwaysHandleKH → (when timelineActive) timeline swallows all keys →
// regular kh.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.alwaysHandleKH.Run(ev) {
		return component.KeyHandled
	}
	if m.timelineActive {
		m.timeline.HandleKey(ev)
		m.applyTimelineAction()
		return component.KeyHandled
	}
	if m.kh.Run(ev) {
		return component.KeyHandled
	}
	return component.KeyIgnored
}

// applyTimelineAction reads the timeline's PendingJump/WantsClose (set during
// the timeline's last key, mouse, or composed hint action, valid only
// immediately after) and acts on it: a pending jump centers the log cursor on
// the target and closes the overlay; a bare close request just closes it.
// Shared by the key path (HandleKey), mouse path (OnMouseClick), and composed
// hint bindings so each performs the same complete timeline action.
func (m *Model) applyTimelineAction() {
	if idx, line, ok := m.timeline.PendingJump(); ok {
		if m.store != nil && idx >= 0 && idx < m.store.GetLogCount() {
			m.Center(idx, line, 0)
		}
		m.CloseTimeline()
	} else if m.timeline.WantsClose() {
		m.CloseTimeline()
	}
}

// OnMouseClick implements component.MouseTarget.
// Runs on the loop goroutine. Returns true when the click was consumed.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	if m.timelineActive {
		consumed := m.timeline.OnMouseClick(x, y, btn)
		m.applyTimelineAction()
		return consumed
	}

	_, logsR, _ := m.subRects()
	if btn != uv.MouseLeft && btn != uv.MouseRight {
		return false
	}

	if y >= logsR.Y && y < logsR.Y+logsR.H {
		return m.onLogRowClick(x, y, y-logsR.Y, btn)
	}
	return false
}

// OnMouseHover implements component.MouseHoverTarget: it records the log row
// under the pointer so the render pass can tint it (experimental hover feedback,
// gated by Core.EnableHover at the dispatch site). It maps the pointer row to a
// (log, line) with the same viewport model as clicks (logIdxAtRow); moving off
// the logs area — or onto an empty row, or while the timeline overlay is up —
// clears the hover. x is ignored: a whole text row is tinted. Returns true when
// the hovered row (or the has-hover state) actually changed, so the caller may
// gate redraw work to real moves.
func (m *Model) OnMouseHover(x, y int) bool {
	if m.timelineActive {
		// Drop any stale log-row tint hidden behind the overlay, then let the
		// timeline tint the bucket under the pointer.
		m.hasHover = false
		return m.timeline.OnMouseHover(x, y)
	}
	_, logsR, _ := m.subRects()
	if y < logsR.Y || y >= logsR.Y+logsR.H {
		return m.ClearMouseHover()
	}
	logIdx, logLine, ok := m.logIdxAtRow(y - logsR.Y)
	if !ok {
		return m.ClearMouseHover()
	}
	if m.hasHover && m.hoverLog == logIdx && m.hoverLine == logLine {
		return false
	}
	m.hasHover, m.hoverLog, m.hoverLine = true, logIdx, logLine
	return true
}

// ClearMouseHover drops pointer-derived highlights from the log rows and the
// owned timeline. It returns true only when visible hover state changed.
func (m *Model) ClearMouseHover() bool {
	changed := false
	if m.hasHover {
		m.hasHover = false
		changed = true
	}
	if m.timeline.ClearMouseHover() {
		changed = true
	}
	return changed
}

// onLogRowClick handles a left- or right-click on the logs area, where (x, y) is
// the absolute pointer cell and relY is the click row relative to the logs
// sub-rect. A right-click moves the cursor onto the clicked row and opens the
// context menu (regardless of expansion), anchored at the pointer. A left-click
// always moves the cursor in-place onto the clicked row; expansion toggling is a
// double-click action (OnMouseDoubleClick). Returns true when the row mapped to a
// log (and was thus consumed).
func (m *Model) onLogRowClick(x, y, relY int, btn uv.MouseButton) bool {
	logIdx, logLine, ok := m.logIdxAtRow(relY)
	if !ok {
		return false
	}
	if btn == uv.MouseRight {
		// Right-click is the mouse analogue of the context-menu keybind: move the
		// cursor onto the clicked field in place (never toggling expansion), then
		// open the context menu regardless of expansion (it shows the applicable
		// subset — the field items appear only when the log is expanded and the
		// cursor resolves a field). Unlike the `c` keybind, it anchors at the
		// pointer cell, not below the field.
		m.moveCursorToRow(logIdx, logLine, relY)
		m.showContextMenuAt(x, y)
		return true
	}
	// In-place select: move the cursor onto the clicked row (see moveCursorToRow).
	m.moveCursorToRow(logIdx, logLine, relY)
	return true
}

// moveCursorToRow moves the cursor onto (logIdx, logLine) currently shown at
// screen-relative row relY WITHOUT recentering: the clicked row already sits at
// relY, so assigning cursor.y = relY keeps it put (mirrors CursorToTop/
// CursorToBottom, which set cursor.y directly then updateDisplay). Horizontal
// scroll (cursor.x / xOffset) is preserved.
func (m *Model) moveCursorToRow(logIdx, logLine, relY int) {
	m.cursor.log = logIdx
	m.cursor.logLine = logLine
	m.cursor.y = relY
	m.updateDisplay()
}

// OnMouseRight implements component.SecondaryMouseTarget: a right-click is the
// mouse analogue of the context-menu keybind. It reuses the OnMouseClick
// right-button path (move the cursor onto the clicked field in place, then open
// the context menu). Returns true when consumed.
func (m *Model) OnMouseRight(x, y int) bool {
	return m.OnMouseClick(x, y, uv.MouseRight)
}

// OpenContextMenu implements component.ContextMenuOpener: open the per-log
// context menu at the keybind anchor (below the selected field). No-op when no
// logs are loaded. The global ContextMenu key (`c`) is dispatched to this by the
// root; it replaces the former logviewer-local `c` keybind. No-op while the
// timeline overlay is active (it owns all input, mirroring HandleKey).
func (m *Model) OpenContextMenu() {
	if m.timelineActive {
		return
	}
	if m.store != nil && m.store.GetLogCount() > 0 {
		m.showContextMenu()
	}
}

// OnMouseDoubleClick implements component.DoubleClickTarget: a double-click in
// the logs area moves the cursor onto the clicked row and toggles that log's
// expansion (the ACTIVATE action — expand if collapsed, collapse if expanded).
// While the timeline overlay is active, the double-click falls back to
// single-click behavior. Returns true when consumed.
func (m *Model) OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool {
	if m.timelineActive {
		return m.OnMouseClick(x, y, uv.MouseLeft)
	}
	_, logsR, _ := m.subRects()
	if y >= logsR.Y && y < logsR.Y+logsR.H {
		relY := y - logsR.Y
		logIdx, logLine, ok := m.logIdxAtRow(relY)
		if !ok {
			return false
		}
		m.moveCursorToRow(logIdx, logLine, relY)
		m.SetExpand(logIdx, !m.store.state.expanded[logIdx])
		return true
	}
	// Bar row or outside the logs area: fall back to single-click.
	return m.OnMouseClick(x, y, uv.MouseLeft)
}

// OnMouseScroll implements component.ScrollTarget: a vertical wheel notch pans
// the log viewport via ScrollViewport (scroll the view, not the cursor). While
// the timeline overlay is active it declines (returns false) so the root falls
// back to the synthetic arrow-key path the timeline navigates with. lines is
// signed: positive scrolls toward later logs, negative toward earlier ones.
func (m *Model) OnMouseScroll(_, _, lines int) bool {
	if m.timelineActive {
		return false
	}
	m.ScrollViewport(lines)
	return true
}

// OnMousePaste implements component.MousePasteTarget. The inline search/jq bars
// are gone; overlay dialogs handle their own paste. Always returns false.
func (m *Model) OnMousePaste(_, _ int, _ string) bool {
	return false
}

// logIdxAtRow maps a logs-area row (relative to logsR.Y, i.e. 0..logsR.H-1) to
// the log index and the line-within-that-log rendered on that row. It walks the
// exact same viewport model placeLogs draws with: the cursor's line
// (cursor.log/cursor.logLine) sits at row cursor.y, lines are laid out downward
// from there and upward above it, and filtered logs are skipped (IterLogs). It
// returns ok=false when the row is empty (past the last/first rendered line) or
// no store is set. logLine is 0 for a collapsed log (single line).
//
// This intentionally reuses placeLogs's split-around-cursor structure rather
// than inventing a parallel layout, so the mapping can never drift from Draw.
func (m *Model) logIdxAtRow(relY int) (logIdx, logLine int, ok bool) {
	if m.store == nil || m.store.GetLogCount() < 1 {
		return 0, 0, false
	}
	_, logsR, _ := m.subRects()
	if relY < 0 || relY >= logsR.H {
		return 0, 0, false
	}
	return m.logIdxAtVirtualRow(relY)
}

// logIdxAtVirtualRow maps a row in the viewport's logical extension. Unlike
// logIdxAtRow, relY may be above or below the visible rectangle. ScrollViewport
// uses this to determine whether a pan would expose empty space at either end.
func (m *Model) logIdxAtVirtualRow(relY int) (logIdx, logLine int, ok bool) {
	if m.store == nil || m.store.GetLogCount() < 1 {
		return 0, 0, false
	}
	if relY >= m.cursor.y {
		// Downward walk (mirrors placeLogs' "add lines needed down" loop).
		return m.scanLogRows(relY, m.cursor.y, 1)
	}
	// Upward walk (mirrors placeLogs' "add lines needed up" loop).
	return m.scanLogRows(relY, m.cursor.y-1, -1)
}

// scanLogRows walks the rendered log lines in one direction (step +1 down, -1 up)
// starting at virtual screen row start, advancing one row per log line, and
// returns the log index and line-within-log whose line lands on target. The
// per-log start line matches placeLogs exactly, but the walk is not clipped to
// the visible rectangle.
func (m *Model) scanLogRows(target, start, step int) (logIdx, logLine int, ok bool) {
	linesIdx := start
	for i := range m.IterLogs(m.cursor.log, step) {
		logLines := m.getRenderCacheEntry(i)
		from := logRowStart(i, m.cursor.log, m.cursor.logLine, len(logLines), step)
		for j := from; j >= 0 && j < len(logLines); j += step {
			if linesIdx == target {
				return i, j, true
			}
			linesIdx += step
		}
	}
	return 0, 0, false
}

// logRowStart returns the first line index to render for log i when walking in
// direction step, mirroring placeLogs' `from` computation: downward the cursor
// log starts at cursor.logLine and others at 0; upward the cursor log starts at
// cursor.logLine-1 and others at the last line.
func logRowStart(i, cursorLog, cursorLogLine, n, step int) int {
	if step > 0 {
		if i == cursorLog {
			return min(cursorLogLine, n-1)
		}
		return 0
	}
	if i == cursorLog {
		return min(cursorLogLine-1, n-1)
	}
	return n - 1
}

// Component

func (m *Model) GetKeybinds() []keys.Binding {
	if m.timelineActive {
		timelineBindings := slices.Clone(m.timeline.GetKeybinds())
		for i := range timelineBindings {
			if timelineBindings[i].Context != "jump to selected bucket" && timelineBindings[i].Context != "close timeline" {
				continue
			}
			action := timelineBindings[i].Action
			timelineBindings[i].Action = func() {
				action()
				m.applyTimelineAction()
			}
		}

		snapshot := slices.Clone(m.alwaysHandleKH.GetKeybinds())
		for i := range snapshot {
			if snapshot[i].Context == "save snapshot" {
				snapshot[i] = snapshot[i].WithHint("save", 4)
			}
		}
		return slices.Concat(timelineBindings, snapshot)
	}
	return slices.Concat(
		m.alwaysHandleKH.GetKeybinds(),
		m.kh.GetKeybinds(),
	)
}

// logsWidth returns the current width of the logs sub-rect. It is the
// width source handed to LogStore so expanded entries wrap to the correct
// column count, tracking changes as SetRect updates drawRect.
func (m *Model) logsWidth() int {
	_, logsR, _ := m.subRects()
	return logsR.W
}

// subRects subdivides drawRect into the severity sidebar and full-height log
// content. The third return is retained as an empty rect for package-internal
// callers during the pillbar removal; no logviewer row is reserved for status.
func (m *Model) subRects() (sidebarR, logsR, statusR component.Rect) {
	r := m.drawRect
	sidebarR = component.Rect{X: r.X, Y: r.Y, W: sidebarWidth, H: r.H}
	logsR = component.Rect{X: r.X + sidebarWidth, Y: r.Y, W: r.W - sidebarWidth, H: r.H}
	return sidebarR, logsR, component.Rect{}
}

func (m *Model) SetRect(r component.Rect) {
	prev := m.drawRect
	m.drawRect = r

	if m.timelineActive {
		m.timeline.SetRect(r)
	}

	if prev.W != r.W {
		m.renderCache.Purge()
		if m.store != nil {
			m.store.RefreshExpandedSearch()
		}
		m.logger.Debug("render cache purged on width change")
	}

	m.updateDisplay()
}

func (m *Model) Draw(s component.Screen) *component.Cursor {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return nil
	}

	if m.store == nil || (m.store.GetLogCount() == 0 && m.store.GetMessage() == "") {
		uicanvas.PlaceVCentered(s, m.drawRect, clippy, uv.Style{})
		return nil
	}
	if msg := m.store.GetMessage(); msg != "" && m.store.GetLogCount() == 0 {
		uicanvas.PlaceVCentered(s, m.drawRect, msg, uv.Style{})
		return nil
	}

	if m.timelineActive {
		return m.timeline.Draw(s)
	}

	_, logsR, _ := m.subRects()
	m.placeLogs(s, logsR, sidebarWidth)
	return nil
}

// custom

func (m *Model) IterLogs(from, step int) iter.Seq[int] {
	if m.store == nil {
		return func(func(int) bool) {}
	}
	return m.store.IterLogs(from, step)
}

func (m *Model) GetLogCount() int {
	if m.store == nil {
		return 0
	}
	return m.store.GetLogCount()
}

func (m *Model) GetSearchMatchCount() int {
	if m.store == nil {
		return 0
	}
	var c int
	for _, v := range m.store.state.search {
		c += len(v)
	}
	return c
}

func (m *Model) IsMarked(idx int) bool {
	if m.store == nil {
		return false
	}
	return m.store.state.marked[idx]
}

func (m *Model) updateDisplay() {
	_, logsR, _ := m.subRects()
	h := logsR.H

	m.xOffset = max(m.xOffset, 0)
	m.cursor.y = numeric.Clamp(m.cursor.y, 0, h-1)

	if m.store == nil || m.store.GetLogCount() < 1 {
		return
	}

	// handle scrolloff
	scrollOff := max(int(m.bundle.Config.Logs.Scrolloff), 0)

	checkScrollOff := func(start, step, cursor int) {
		for logIndex := range m.IterLogs(m.cursor.log+step, step) {
			if start > scrollOff {
				break
			}

			entry := m.getRenderCacheEntry(logIndex)
			start += len(entry)
		}

		if start >= scrollOff {
			m.cursor.y = cursor
		}
	}

	if m.cursor.y > h-scrollOff-1 {
		currentEntry := m.getRenderCacheEntry(m.cursor.log)
		linesAfter := len(currentEntry) - m.cursor.logLine - 1
		checkScrollOff(linesAfter, 1, h-scrollOff-1)
	}

	if m.cursor.y < scrollOff {
		linesBefore := m.cursor.logLine
		checkScrollOff(linesBefore, -1, scrollOff)
	}
}

// applyJQAndRefresh re-runs jq from state and, on the empty-query path (which
// restores raw data synchronously and posts no JQChunkMsg), purges the render
// cache and refreshes so cleared jq shows raw logs again.
func (m *Model) applyJQAndRefresh(cleared bool) {
	// m.store is nil until SetStore (editing jq over an empty store is
	// spec-allowed). State already holds the expr and SetStore's jq-mismatch arm
	// reapplies it once a store loads, so a nil store is a no-op here.
	if m.store == nil {
		return
	}
	m.store.ApplyJQ()
	if cleared {
		m.renderCache.Purge()
		m.updateDisplay()
	}
}

// getRenderCacheEntry returns a slice of tokenizedLine representing the lines of a marshalled log line
// at the specified index. If the log line is found in the cache, it is returned directly
// from the cache. Otherwise, the function marshals the log line, tokenizes it, stores it in the cache,
// and then returns it.
// If the specified log index is out of range, a nil slice will be returned.
func (m *Model) getRenderCacheEntry(index int) []tokenizedLine {
	if m.store == nil || index < 0 || index >= m.store.GetLogCount() {
		return nil
	}

	entry, ok := m.renderCache.Get(index)
	if !ok {
		b := m.store.logs[index].Bytes(m.store.state.expanded[index])
		entry = buildTokenizedLines(b, tokenize(b), m.ruleEngine)
		_ = m.renderCache.Add(index, entry)
	}
	return entry
}

// CloseTimeline closes the timeline overlay if it is currently active.
// No-op otherwise. Intended to be called when the logviewer's tab loses
// focus (e.g., user switches to a different tab).
func (m *Model) CloseTimeline() {
	if !m.timelineActive {
		return
	}
	m.timelineActive = false
	m.timeline.Close()
}

func (m *Model) Clear() {
	if m.store != nil {
		m.store.Clear()
	}
	m.cursor = &logsCursor{}
	m.renderCache.Purge()
	m.updateDisplay()
}

// GetStore returns the current LogStore.
func (m *Model) GetStore() *LogStore {
	return m.store
}

// adoptRowState reconciles the view state that indexes log rows (the cursor and
// the n/N jump counter) with the store being adopted.
//
// It drops that state when the store identity changes OR the same store moved to
// a new query generation: both mean the rows the viewer last saw are gone. Store
// identity alone is insufficient — a re-fetch reuses the instance's store, so
// cursor.log would still point at the previous query's row and `n` would scan
// forward from a meaningless offset. Two distinct stores can share a queryID, so
// identity is checked too. Otherwise the cursor is only clamped into range.
func (m *Model) adoptRowState(prev, s *LogStore) {
	var gen uint64
	if s != nil {
		gen = s.GetQueryID()
	}
	newGeneration := prev != s || gen != m.storeGen
	m.storeGen = gen

	switch {
	case newGeneration, s == nil, s.GetLogCount() == 0:
		m.cursor = &logsCursor{}
		if newGeneration {
			m.currentMatch = 0
		}
	case m.cursor.log >= s.GetLogCount():
		m.cursor.log = s.GetLogCount() - 1
		m.cursor.logLine = 0
	}
}

// SetStore swaps the logviewer's backing data store. It cancels any in-flight
// operations on the old store, purges the render cache, resets/clamps the
// row-indexing state (adoptRowState), and checks for jq/search mismatches to
// re-apply if needed. ApplyJQ/DoSearch run for effect via the poster.
func (m *Model) SetStore(s *LogStore) {
	prev := m.store
	if m.store != nil {
		m.store.CancelAll()
	}
	m.store = s
	m.adoptRowState(prev, s)
	m.renderCache.Purge()
	m.xOffset = 0
	if s != nil {
		s.SetWidthSource(m.logsWidth)
	}
	if s == nil {
		return
	}
	jqMismatch := s.appliedJQ != m.bundle.State.JQ()
	searchMismatch := s.appliedSearch != m.bundle.State.Search()
	filterMismatch := !slices.Equal(s.appliedFilters, m.bundle.State.Filters())
	if searchMismatch {
		// A fresh re-search produces a new match set; no prior jump is valid.
		// Reset before the jq arm below returns early: with the default jq
		// (Logs.JqDefaultQuery) a cleared store always mismatches on jq too, so
		// a reset inside the search arm alone would never run on a re-fetch.
		m.currentMatch = 0
	}
	if filterMismatch {
		s.ApplyFilters()
	}
	if jqMismatch {
		s.ApplyJQ()
		return
	}
	if searchMismatch {
		s.DoSearch()
	}
}

// GetCursor returns the current cursor position and horizontal scroll offset.
func (m *Model) GetCursor() (x, y, log, logLine, xOffset int) {
	return m.cursor.x, m.cursor.y, m.cursor.log, m.cursor.logLine, m.xOffset
}

// SetCursor restores the cursor position and horizontal scroll offset.
func (m *Model) SetCursor(x, y, log, logLine, xOffset int) {
	m.cursor.x = x
	m.cursor.y = y
	m.cursor.log = log
	m.cursor.logLine = logLine
	m.xOffset = xOffset
}

func (m *Model) GetCurrentLine() string {
	if m.store == nil || m.store.GetLogCount() < 1 {
		return ""
	}
	return string(m.store.logs[m.cursor.log].ByteLines(m.store.state.expanded[m.cursor.log])[m.cursor.logLine])
}

// GetCurrentValue returns the field value for the cursor line by extracting
// the key path from tokens and navigating the entry's data. This returns the
// original unwrapped value, not the display-wrapped text.
// Returns empty string if no value is found.
func (m *Model) GetCurrentValue() string {
	if m.store == nil || m.store.GetLogCount() < 1 {
		return ""
	}
	lines := m.getRenderCacheEntry(m.cursor.log)
	path := extractKeyPath(lines, m.cursor.logLine)
	if len(path) == 0 {
		return ""
	}
	data, modified, unlock := m.store.logs[m.cursor.log].GetData()
	defer unlock()
	var current any = data
	if modified != nil {
		current = modified
	}
	// Navigate the path to the target value
	for _, key := range path {
		d, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = d[key]
		if !ok {
			return ""
		}
	}
	switch val := current.(type) {
	case string:
		return val
	default:
		b, err := jsonutil.API.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// NOTE: ANYTHING can happen in logs, since this garbage breaks a lot of logs into two
