package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/bevicted/lognav/internal/archive"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/sessionbus"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/archivehandler"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/contextmenu"
	"github.com/bevicted/lognav/internal/ui/components/dialog"
	"github.com/bevicted/lognav/internal/ui/components/filehandler"
	"github.com/bevicted/lognav/internal/ui/components/filtermenu"
	"github.com/bevicted/lognav/internal/ui/components/helpoverlay"
	"github.com/bevicted/lognav/internal/ui/components/instancepicker"
	"github.com/bevicted/lognav/internal/ui/components/keyhintline"
	"github.com/bevicted/lognav/internal/ui/components/logviewer"
	"github.com/bevicted/lognav/internal/ui/components/queryeditor"
	"github.com/bevicted/lognav/internal/ui/components/snapshothandler"
	"github.com/bevicted/lognav/internal/ui/components/statusline"
	"github.com/bevicted/lognav/internal/ui/components/tabs"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	QueryTab = iota
	InstancesTab
	LogsTab
	SnapshotTab
	ArchiveTab
	numTabs
)

type Model struct {
	ctx    context.Context
	bundle deps.Bundle

	screenW, screenH int
	kh               *keyHandlers

	tabs         *tabs.Model
	isFocusTaken bool
	overlay      component.Component

	instances        *instancepicker.Model
	logviewer        *logviewer.Model
	queryeditor      *queryeditor.Model
	snapshots        *snapshothandler.Model
	archives         *archivehandler.Model
	resumePath       string
	loadedInstance   string // CRN of instance whose logs are currently loaded in memory
	intendedInstance string // CRN of instance the user has most recently selected; loads not matching this CRN are stale

	statusLine  *statusline.StatusLine
	keyHintLine *keyhintline.Model
	logger      *slog.Logger
	poster      msgs.Poster
	broadcaster sessionbus.Broadcaster // single owner; forwarded to snapshothandler + instancepicker notifyDirty

	wheel axisLock // vertical-biased mouse-wheel axis lock

	// double-click tracking
	now           func() time.Time // wall clock; overridable in tests
	lastClickX    int
	lastClickY    int
	lastClickAt   time.Time
	lastWasDouble bool
}

// Mouse-wheel axis-lock tuning. The lock is biased toward vertical because the
// log viewer scrolls vertically far more often than horizontally: a stray
// sideways notch must not hijack a vertical scroll, and a sideways lock must be
// cheap to escape. axisLock.score is a running vertical(+)/horizontal(-) tally
// over the current gesture. vertLockDepth caps how firmly a vertical lock holds
// (how many counter-axis notches it shrugs off); the horizontal side is floored
// at -horizLockThreshold, so horizontal locks only once it clearly dominates and
// a single vertical notch always lifts the score back above the threshold to
// reclaim vertical control.
const (
	vertLockDepth      = 3
	horizLockThreshold = 2
)

// defaultWheelScrollLines is the rows-per-vertical-notch used when
// Core.WheelScrollLines is 0 (unset).
const defaultWheelScrollLines = 5

// axisLock implements a vertical-biased directional lock with override for
// mouse-wheel scrolling. A scroll "gesture" is a continuous burst of wheel
// events — including macOS momentum/inertial events, which keep arriving for up
// to a second or two after the fingers lift and carry no phase flag over the
// terminal — and after gap of inactivity the tally resets. Within a gesture
// each event nudges score toward its axis; the event is dispatched only if its
// axis matches the currently intended axis (vertical unless horizontal has
// dominated past -horizLockThreshold). This holds a diagonal swipe on one axis
// while letting sustained counter-axis intent reclaim control — e.g. scrolling
// vertically out of a horizontal lock even while momentum events still arrive,
// which is what otherwise leaves the view "stuck" against a boundary. A gap of
// 0 disables locking entirely (every event passes), restoring raw scroll.
type axisLock struct {
	gap      time.Duration
	score    int
	lastSeen time.Time
}

// allow reports whether a wheel event on the given axis should be dispatched,
// updating the lock tally. now must be non-decreasing across calls (it is, on
// the single loop goroutine that owns OnMouse).
func (a *axisLock) allow(vert bool, now time.Time) bool {
	if a.gap <= 0 { // locking disabled
		return true
	}
	if now.Sub(a.lastSeen) > a.gap {
		a.score = 0 // inactivity gap -> new gesture
	}
	a.lastSeen = now

	if vert {
		a.score = min(a.score+1, vertLockDepth)
	} else {
		a.score = max(a.score-1, -horizLockThreshold)
	}

	intendedVert := a.score > -horizLockThreshold
	return vert == intendedVert
}

func New(ctx context.Context, bundle deps.Bundle) (*Model, error) {
	m := &Model{
		ctx:         ctx,
		bundle:      bundle,
		statusLine:  statusline.New(bundle),
		keyHintLine: keyhintline.New(bundle),
		logger:      slog.Default().With(logging.KeyComponent, "ui"),
		wheel:       axisLock{gap: time.Duration(bundle.Config.Core.ScrollAxisLockMs) * time.Millisecond},
		now:         time.Now,
	}
	bindKeyhandlersToModel(m)

	instances := instancepicker.New(ctx, bundle)
	m.instances = instances
	viewer := logviewer.New(bundle)
	m.logviewer = viewer
	viewer.SetInstanceStatusLookup(instances.InstanceStatus)
	qe, err := queryeditor.New(ctx, bundle, &queryeditor.EventHandler{
		OnConfirmPressed: func() {
			m.tabs.Select(InstancesTab)
		},
	})
	if err != nil {
		return nil, err
	}
	m.queryeditor = qe
	sh := snapshothandler.New(bundle)
	m.snapshots = sh
	sh.SetEvictor(instances.EvictIfCurrentBacking)
	instances.SetOnRemoveReadOnly(func(crn string) {
		if m.intendedInstance == crn {
			m.intendedInstance = ""
		}
		if m.loadedInstance == crn {
			m.loadedInstance = ""
			m.logviewer.SetStore(nil)
		}
	})

	ah := archivehandler.New(ctx, bundle)
	m.archives = ah
	// Inject the store-preserving auth resolver so the archive poll path can
	// resolve per-instance tokens via the picker's own AccountManager without
	// disturbing displayed logs (a nil resolver would disable polling).
	ah.SetTokenResolver(instances.ResolveInstanceToken)

	tabList := []tabs.Tab{
		{
			Component: qe,
			TitleFunc: func() string {
				if qe.ShowingFiles() {
					return "query files"
				}
				if qe.ShowingSnippets() {
					return "query snippets"
				}
				return "query"
			},
		},
		{
			Title:     "instances",
			Component: instances,
		},
		{
			Title:     "logs",
			Component: viewer,
		},
		{
			Title:     "snapshots",
			Component: sh,
		},
	}
	// The Archive tab is experimental and opt-in: it appears only when
	// core.enableExperimental is set. It must stay LAST so the other tab
	// indices (and the *Tab constants) are unaffected by its absence. The
	// archive handler itself is always constructed so archive message routing
	// (dead when the feature is off) never nil-derefs.
	if bundle.Config.Core.EnableExperimental {
		tabList = append(tabList, tabs.Tab{
			Title:     "archive",
			Component: ah,
		})
	}

	m.tabs = tabs.New(bundle, tabList...).WithOnLeave(func(from, to int) {
		// Close the timeline overlay whenever the logs tab loses focus so
		// returning to it starts from the normal log view.
		if from == LogsTab {
			viewer.CloseTimeline()
		}
		// Refresh the archive list on entry so coloring reflects any cross-session
		// staleness (another session may have advanced/collected an archive).
		if to == ArchiveTab {
			ah.ListFiles()
		}
		m.handleResize()
	})

	// One-shot input-seam inventory: one debug line per registered tab recording
	// which optional seams it opts into and which it does not. A tab that forgot
	// to opt into a seam (the wrappers HOLD their filehandler rather than
	// embedding it, so every seam is a deliberate per-tab decision) is otherwise
	// invisible — the dispatch-site type assertions just fall through. Logging it
	// here rather than at those sites is deliberate: hover and scroll fire per
	// mouse event, so a log there would spam continuously for every component
	// that legitimately does not implement them.
	for _, t := range tabList {
		component.LogSeams(m.logger, t.GetTitle(), t.Component)
	}

	return m, nil
}

// applyOverlayDim dims cells outside the active overlay's rect and clears
// cells inside it, so the overlay's Draw starts from a blank slate (cells
// the overlay doesn't write — e.g. trailing space past short list items —
// don't leak styles from the underlying tab content). Full-area overlays
// skip the prep entirely.
func (m *Model) applyOverlayDim(s component.Screen, w, h int) {
	if _, sized := m.overlay.(component.Sizer); !sized {
		return
	}
	rect, ok := m.overlayRect()
	if !ok {
		return
	}
	dimOutsideRect(s, rect, w, h)
	clearRect(s, rect)
}

// clearRect resets every cell inside rect to a blank cell, wiping any styles
// the underlying tab content wrote there. Called before a modal overlay's
// Draw so the overlay paints onto a clean canvas region.
func clearRect(s component.Screen, rect component.Rect) {
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			s.SetCell(x, y, nil)
		}
	}
}

// dimOutsideRect mutates every cell on the screen outside rect to apply the
// faint attribute, signalling that the area is inactive while a modal overlay
// is shown above it. Walks four perimeter bands instead of the full canvas to
// keep per-frame cost proportional to the perimeter, not area.
func dimOutsideRect(s component.Screen, rect component.Rect, w, h int) {
	rx0 := max(rect.X, 0)
	ry0 := max(rect.Y, 0)
	rx1 := min(rect.X+rect.W, w)
	ry1 := min(rect.Y+rect.H, h)

	dimBand := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				cell := s.CellAt(x, y)
				if cell == nil {
					continue
				}
				cell.Style.Attrs |= uv.AttrFaint
			}
		}
	}

	dimBand(0, 0, w, ry0)     // top
	dimBand(0, ry1, w, h)     // bottom
	dimBand(0, ry0, rx0, ry1) // left
	dimBand(rx1, ry0, w, ry1) // right
}

// overlayRect returns the rect the current overlay should occupy. Sizer-
// implementing overlays negotiate a smaller size; Positioner-implementing
// overlays anchor explicitly (top-aligned for help, etc.) while others
// fall back to CenterRect. Non-Sizer overlays take the full content area.
func (m *Model) overlayRect() (component.Rect, bool) {
	if m.overlay == nil {
		return component.Rect{}, false
	}
	contentR := component.Rect{X: 0, Y: 0, W: m.screenW, H: m.screenH - m.footerRows()}
	s, sized := m.overlay.(component.Sizer)
	if !sized {
		return contentR, true
	}
	w, h := s.PreferredSize(contentR.W, contentR.H)
	if p, ok := m.overlay.(component.Positioner); ok {
		return p.Position(contentR, w, h), true
	}
	return component.CenterRect(contentR, w, h), true
}

// assignOverlayRect computes and applies the rect for the current overlay.
func (m *Model) assignOverlayRect() {
	rect, ok := m.overlayRect()
	if !ok {
		return
	}
	m.overlay.SetRect(rect)
}

// statusProvider returns the active tab's optional contextual-row provider.
func (m *Model) statusProvider() (component.StatusProvider, bool) {
	provider, ok := m.tabs.GetActiveComponent().(component.StatusProvider)
	return provider, ok
}

// hasStatusLine reports whether the active provider owns the only available
// row when the terminal is undersized.
func (m *Model) hasStatusLine() bool {
	_, ok := m.statusProvider()
	return ok && m.screenH >= 1
}

// hasKeyHintLine applies the status-before-hints contract at height one.
func (m *Model) hasKeyHintLine() bool {
	if !m.bundle.Config.Core.ShowKeyHints || m.screenH < 1 {
		return false
	}
	return !m.hasStatusLine() || m.screenH >= 2
}

// footerRows returns the number of root-owned rows below tabs.
func (m *Model) footerRows() int {
	rows := 0
	if m.hasStatusLine() {
		rows++
	}
	if m.hasKeyHintLine() {
		rows++
	}
	return rows
}

// footerTop returns the first root-owned footer row. It is also the boundary
// below which tab input must not be routed.
func (m *Model) footerTop() int {
	return m.screenH - m.footerRows()
}

// handleResize propagates the new screen dimensions to the always-drawn tabs,
// key-hint line, and statusline, plus the active overlay if present (e.g. the
// logviewer's cache invalidation on width change runs inside its SetRect).
// Called only on a resize, not every frame.
func (m *Model) handleResize() {
	w, h := m.screenW, m.screenH
	if w < 1 || h < 1 {
		m.tabs.SetRect(component.Rect{})
		m.keyHintLine.SetRect(component.Rect{})
		m.statusLine.SetRect(component.Rect{})
		return
	}

	footerRows := m.footerRows()
	m.tabs.SetRect(component.Rect{X: 0, Y: 0, W: w, H: h - footerRows})
	if m.hasStatusLine() {
		m.statusLine.SetRect(component.Rect{X: 0, Y: h - footerRows, W: w, H: 1})
	} else {
		m.statusLine.SetRect(component.Rect{})
	}
	if m.hasKeyHintLine() {
		m.keyHintLine.SetRect(component.Rect{X: 0, Y: h - 1, W: w, H: 1})
	} else {
		m.keyHintLine.SetRect(component.Rect{})
	}
	if m.overlay != nil {
		m.assignOverlayRect()
	}
}

// SetSend wires the runtime's send function into the instance picker for
// streaming callbacks. It is the uv-spine replacement for SetProgram; both
// exist during the strangler (SetProgram is removed at the R1 cutover).
func (m *Model) SetSend(send func(uv.Event)) {
	m.instances.SetSend(send)
}

// SetPoster injects the runtime poster and forwards it to children that spawn
// async work. Called once at startup from runtime.Run. The queryeditor and
// snapshothandler forward it to their owned filehandler, whose IO methods spawn
// tracked poster goroutines.
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.instances.SetPoster(p)
	m.queryeditor.SetPoster(p)
	m.snapshots.SetPoster(p)
	m.logviewer.SetPoster(p)
	m.archives.SetPoster(p)
}

// SetBroadcaster wires the cross-session notifier. ui.Model is its single owner:
// it forwards the broadcaster to snapshothandler (rename/delete hooks) and the
// manual-save path, and builds the instancepicker's notifyDirty closure. It
// broadcasts synchronously so a backing claim or finalization is visible to
// peers before that mutation path returns. The closure reads
// m.broadcaster/m.poster LAZILY at call time (always well after startup), so
// SetBroadcaster/SetPoster wiring order does not matter — a future mis-order
// cannot silently drop claim broadcasts. The local refresh is posted
// asynchronously because setBacking can run on the loop goroutine; Broadcast
// skips own PID, so the peer broadcast alone never refreshes this session.
// A nil broadcaster disables all cross-session notification (every site no-ops).
func (m *Model) SetBroadcaster(b sessionbus.Broadcaster) {
	m.broadcaster = b
	m.snapshots.SetBroadcaster(b)
	if b == nil {
		m.instances.SetNotifyDirty(nil)
		return
	}
	m.instances.SetNotifyDirty(func() {
		// Local recolor: the claiming session recomputes its own list coloring so
		// a just-loaded snapshot shows as "own" immediately. This can run on the
		// loop, so PostAsync owns the required off-loop post.
		msgs.PostAsync(m.poster, msgs.SnapshotsDirtyMsg{})
		if m.broadcaster != nil {
			_ = m.broadcaster.Broadcast(context.Background(), sessionbus.MethodSnapshotsDirty, sessionbus.SnapshotsDirtyParams{})
		}
	})
}

// requestQuit requests a clean exit. It is the quit hook wired into the root
// ForceQuit keybind; keybind actions run on the loop goroutine, hence the
// msgs.RequestQuit helper. It stays a method because the keybind registration
// needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

// releaseFocus posts FocusMsg{GrabFocus:false}. Callers run on the loop
// goroutine (keybind actions), hence msgs.PostAsync.
func (m *Model) releaseFocus() {
	msgs.PostAsync(m.poster, msgs.FocusMsg{GrabFocus: false})
}

// openActiveContextMenu opens the active component's context menu if it
// implements component.ContextMenuOpener; otherwise it is a no-op. Wired to the
// global ContextMenu (`c`) keybind in the regular handler. Runs on the loop; the
// opener posts ShowContextMenuMsg off-loop itself.
func (m *Model) openActiveContextMenu() {
	if o, ok := m.tabs.GetActiveComponent().(component.ContextMenuOpener); ok {
		o.OpenContextMenu()
	}
}

// SetResumePath sets a snapshot file path to restore when the TUI starts.
func (m *Model) SetResumePath(path string) {
	m.resumePath = path
}

// Close releases TUI-owned resources: cancels in-flight ICL queries, closes
// every LogStore, and flushes the open backing snapshot file (if any).
// Called by cmd.runTUI after tui.Run() returns. Safe to call multiple times.
func (m *Model) Close() error {
	return m.instances.Close()
}

// HeldManagedBacking reports whether the session holds a managed backing claim
// (delegates to the instancepicker). Read at shutdown before Close releases it.
func (m *Model) HeldManagedBacking() bool { return m.instances.HeldManagedBacking() }

// BroadcastSnapshotsDirtySync synchronously notifies peers that the .inuse set
// changed. Used at shutdown AFTER the guard is released and the poster is drained:
// a direct blocking Broadcast (NOT a poster.Go) is the only safe way to notify
// then — a poster.Go after drainPoster/Wait would be a use-after-drain hazard.
// A nil broadcaster no-ops.
func (m *Model) BroadcastSnapshotsDirtySync(ctx context.Context) error {
	if m.broadcaster == nil {
		return nil
	}
	return m.broadcaster.Broadcast(ctx, sessionbus.MethodSnapshotsDirty, sessionbus.SnapshotsDirtyParams{})
}

// CancelQueries aborts every in-flight ICL operation (streaming queries and
// in-flight auth / token resolution) without closing stores or flushing files.
// The runtime calls it at teardown BEFORE waiting on the poster so those
// goroutines abort and the wait drains promptly; the heavier Close (store close +
// backing-file flush) then runs after the drain, with no in-flight writers left
// to race the file close.
func (m *Model) CancelQueries() {
	m.instances.CancelQueries()
}

// IsAnimating reports whether the model needs periodic redraws. The spine's
// shared ticker reads this predicate (after each drain and once at startup) to
// start/stop the 100ms redraw ticker. It is FETCH-ONLY: true while any instance
// query is in progress so the live elapsed timers advance. There is no cursor
// blink (R3b/D1), so input focus never makes the model animate.
func (m *Model) IsAnimating() bool {
	return m.instances.IsAnimating()
}

func (m *Model) Init() {
	_ = snapshot.CleanStaleWip()
	_ = snapshot.CleanStaleInUse() // sweep dead-PID .inuse files
	if m.bundle.Config.Core.EnableExperimental {
		// Only touch the archive registry dir when the feature is on; when off, the
		// archive subsystem stays fully dormant.
		_ = archive.CleanStaleExpired(time.Now()) // sweep past-TTL archive registry files
	}
	_ = snapshot.InitOwn() // reset any stale file from a reused PID
	m.logger.Debug("main ui initialized")
	// loadSnapshot does its IO off-loop via the poster; call it for effect.
	if m.resumePath != "" {
		m.loadSnapshot(m.resumePath)
	}
	m.tabs.Init()
}

// loadSnapshot opens and decodes the snapshot container at path off-loop via the
// poster, then posts a SnapshotRestoreMsg with the loaded state (or the error).
func (m *Model) loadSnapshot(path string) {
	if m.poster == nil {
		return
	}
	m.poster.Go(func(ctx context.Context) {
		c, err := snapshot.OpenContainerReadOnly(path)
		if err != nil {
			_ = m.poster.PostCritical(ctx, msgs.SnapshotRestoreMsg{Err: err})
			return
		}
		defer func() { _ = c.Close() }()
		s, err := snapshot.LoadState(c)
		if err != nil {
			_ = m.poster.PostCritical(ctx, msgs.SnapshotRestoreMsg{Err: err})
			return
		}
		if err := snapshot.ValidateInstanceSnapshotCRNs(s.InstancePickerSnapshot.Instances); err != nil {
			_ = m.poster.PostCritical(ctx, msgs.SnapshotRestoreMsg{Err: err})
			return
		}
		m.logger.Info("snapshot resumed", "path", path)
		_ = m.poster.PostCritical(ctx, msgs.SnapshotRestoreMsg{Snapshot: s, BackingPath: path})
	})
}

// ApplyEnv ingests the process environment at startup (the seed replacement for
// the old environment event arm). It picks up the ICL API key so a remote-session
// environment (e.g. SSH) reaches the config and the instance picker.
func (m *Model) ApplyEnv(env []string) {
	if v := icl.APIKeyFromEnvironment(uv.Environ(env).Getenv); v != "" {
		m.logger.Debug("set production config api key via env", "secret length", len(v))
		if production, ok := m.bundle.Config.ICL.Environments[string(icl.EnvProd)]; ok {
			production.APIKey = v
			m.bundle.Config.ICL.Environments[string(icl.EnvProd)] = production
		}
		m.instances.SetAPIKey(icl.EnvProd, v)
	}
}

// OnResize records the new screen dimensions and re-sizes the always-drawn tabs
// and statusline (plus the active overlay). It is the uv-native replacement for
// the old window-size event arm; the runtime calls it directly.
func (m *Model) OnResize(w, h int) {
	m.screenW, m.screenH = w, h
	m.handleResize()
}

// OnMouse handles a uv mouse event. A vertical wheel notch pans the viewport via
// the ScrollTarget seam (axis-locked, synthetic-arrow fallback); a horizontal
// notch stays on synthetic Left/Right. Left clicks route through onMouseClick;
// right clicks route through onMouseContext (the SecondaryMouseTarget seam, which
// components use to open context menus or context actions). Motion events route
// through onMouseHover only when Core.EnableHover is on; release is dropped. It
// returns whether the event needs a redraw: clicks/wheel always do, a motion only
// when the hover actually changed (so an idle pointer sweep within one row does
// not repaint), and a dropped event does not. The runtime calls it on the loop
// goroutine and uses the result to gate the frame.
func (m *Model) OnMouse(ev uv.Event) bool {
	switch e := ev.(type) {
	case uv.MouseWheelEvent:
		m.onMouseWheel(e)
		if m.bundle.Config.Core.EnableHover {
			mo := e.Mouse()
			m.onMouseHover(mo.X, mo.Y)
		}
		return true
	case uv.MouseClickEvent:
		mo := e.Mouse()
		switch mo.Button {
		case uv.MouseLeft:
			m.onMouseClick(mo.X, mo.Y)
		case uv.MouseRight:
			m.onMouseContext(mo.X, mo.Y)
		case uv.MouseMiddle:
			m.onMousePaste(mo.X, mo.Y)
		}
		if m.bundle.Config.Core.EnableHover {
			m.onMouseHover(mo.X, mo.Y)
		}
		return true
	case uv.MouseMotionEvent:
		if m.bundle.Config.Core.EnableHover {
			mo := e.Mouse()
			return m.onMouseHover(mo.X, mo.Y)
		}
		return false
	}
	return false
}

// onMouseHover routes a pointer-motion event (hover feedback) to the
// MouseHoverTarget under the pointer, mirroring the click precedence WITHOUT any
// dismiss/select side effects: an active overlay consumes the motion (forwarding
// to it when inside its rect and it opts into MouseHoverTarget; never dismissing —
// a motion is not a cancel), the footer/statusline row is swallowed, otherwise it
// goes to tabs.OnMouseHover (which highlights the hovered tab on the bar row and
// forwards content-area motion to the active component). Returns whether the
// hover changed visible state (so the caller can gate the redraw). Only reached
// when Core.EnableHover is on.
func (m *Model) onMouseHover(x, y int) bool {
	if m.overlay != nil {
		changed := m.tabs.ClearMouseHover()
		ht, hasHover := m.overlay.(component.MouseHoverTarget)
		if rect, ok := m.overlayRect(); ok && inRect(x, y, rect) {
			if hasHover && ht.OnMouseHover(x, y) {
				changed = true
			}
			return changed
		}
		if hasHover && ht.ClearMouseHover() {
			changed = true
		}
		return changed
	}
	if y >= m.footerTop() { // footer rows
		return m.tabs.ClearMouseHover()
	}
	return m.tabs.OnMouseHover(x, y)
}

// onMouseWheel routes a wheel notch. Wheel events first pass through a
// gesture-scoped axis lock (m.wheel) so a diagonal touchpad swipe — which the
// terminal reports as rapidly interleaved vertical/horizontal one-notch wheel
// events — scrolls along a single axis instead of jittering between both
// (disabled when Core.ScrollAxisLockMs is 0). A VERTICAL notch becomes a
// viewport pan via the component.ScrollTarget seam (routeScroll), falling back to
// a single synthetic Up/Down arrow for components that do not opt in (so the
// cursor still moves where there is no separate viewport). A HORIZONTAL notch
// stays on the synthetic Left/Right arrow path unchanged (horizontal viewport
// panning is out of scope for the scroll seam).
//
// A wheel scroll also resets double-click tracking: panning the viewport moves a
// different logical row under a fixed screen cell, so a left-click before the
// scroll and one after on the same screen cell must not count as a double-click.
func (m *Model) onMouseWheel(we uv.MouseWheelEvent) {
	// A scroll between two clicks must not count toward a double-click.
	m.resetClickTracking()
	mo := we.Mouse()
	btn := mo.Button
	vert := btn == uv.MouseWheelUp || btn == uv.MouseWheelDown
	if !m.wheel.allow(vert, time.Now()) {
		return
	}
	if vert {
		lines := m.scrollLines()
		key := uv.KeyDown
		if btn == uv.MouseWheelUp {
			lines, key = -lines, uv.KeyUp
		}
		if m.routeScroll(mo.X, mo.Y, lines) {
			return
		}
		m.HandleKey(uv.KeyPressEvent{Code: key}) // fallback: non-scrollable component
		return
	}
	switch btn {
	case uv.MouseWheelRight:
		m.HandleKey(uv.KeyPressEvent{Code: uv.KeyRight})
	case uv.MouseWheelLeft:
		m.HandleKey(uv.KeyPressEvent{Code: uv.KeyLeft})
	}
}

// scrollLines returns the rows a vertical wheel notch pans, from
// Core.WheelScrollLines (0 → defaultWheelScrollLines).
func (m *Model) scrollLines() int {
	if n := int(m.bundle.Config.Core.WheelScrollLines); n > 0 {
		return n
	}
	return defaultWheelScrollLines
}

// routeScroll dispatches a vertical wheel notch (signed lines: + down/later,
// - up/earlier) to the component.ScrollTarget under the pointer, mirroring the
// click precedence: an active overlay consumes the notch (scrolling it when it
// opts into ScrollTarget — e.g. the help overlay — swallowing it otherwise, since
// a wheel is not an overlay-dismiss affordance); the footer/statusline row is
// swallowed; otherwise the active tab component scrolls. Returns true when the
// notch was handled (no arrow fallback should run). A false return — active
// component is not a ScrollTarget, or it declined (e.g. the query editor in text
// mode) — lets the caller fall back to a synthetic arrow.
func (m *Model) routeScroll(x, y, lines int) bool {
	if m.overlay != nil {
		if st, ok := m.overlay.(component.ScrollTarget); ok {
			st.OnMouseScroll(x, y, lines)
		}
		return true // overlay swallows the notch (no tab scroll, no dismiss)
	}
	if y >= m.footerTop() { // footer rows
		return true
	}
	if st, ok := m.tabs.GetActiveComponent().(component.ScrollTarget); ok {
		return st.OnMouseScroll(x, y, lines)
	}
	return false
}

// clampDoubleClickMs clamps a non-zero DoubleClickMs value into [50, 2000].
// Callers must guard ms > 0 before calling (0 disables double-click entirely).
func clampDoubleClickMs(ms uint16) uint16 {
	const minMs, maxMs uint16 = 50, 2000
	if ms < minMs {
		return minMs
	}
	if ms > maxMs {
		return maxMs
	}
	return ms
}

// isDoubleClick reports whether a left-click at (x, y) at time now is the second
// click of a double-click: the same cell as the previous left-click, within the
// configured window (Core.DoubleClickMs; 0 disables, non-zero clamped to
// [50, 2000] ms), and not itself the tail of a prior double (so a third rapid
// click is a fresh single click, not another double). It records this click's
// cell and time as the new "previous", and remembers whether this was a double
// so the next click can break the chain. Must be called exactly once per left-click.
func (m *Model) isDoubleClick(x, y int, now time.Time) bool {
	ms := m.bundle.Config.Core.DoubleClickMs
	double := false
	if ms > 0 && !m.lastWasDouble && !m.lastClickAt.IsZero() &&
		x == m.lastClickX && y == m.lastClickY {
		window := time.Duration(clampDoubleClickMs(ms)) * time.Millisecond
		double = now.Sub(m.lastClickAt) <= window
	}
	m.lastClickX, m.lastClickY, m.lastClickAt = x, y, now
	m.lastWasDouble = double
	return double
}

// resetClickTracking clears the double-click tracking state so a left-click
// following a non-left click on the same cell is not treated as a double.
func (m *Model) resetClickTracking() {
	m.lastClickAt = time.Time{}
	m.lastWasDouble = false
}

// onMouseClick routes a left click through the precedence chain: overlay
// (inside -> forward to MouseTarget; outside -> dismiss) -> footer (ignored) ->
// tabs (bar row selects, content delegates to the active component).
func (m *Model) onMouseClick(x, y int) {
	// isDoubleClick must be called for every left-click to keep tracking state
	// up to date, even when the click is consumed by the overlay or footer.
	isDouble := m.isDoubleClick(x, y, m.now())
	if m.overlay != nil {
		// overlayRect returns ok=false only when overlay is nil, which is guarded above.
		if rect, ok := m.overlayRect(); ok && inRect(x, y, rect) {
			if mt, ok := m.overlay.(component.MouseTarget); ok {
				mt.OnMouseClick(x, y, uv.MouseLeft)
			}
			return
		}
		m.dismissOverlay()
		return
	}
	if m.hasStatusLine() && y == m.screenH-m.footerRows() {
		m.statusLine.OnMouseClick(x, y, uv.MouseLeft)
		return
	}
	if m.hasKeyHintLine() && y == m.screenH-1 {
		m.keyHintLine.OnMouseClick(x, y, uv.MouseLeft)
		return
	}
	if y >= m.footerTop() { // footer rows
		return
	}
	// A bar-row click can switch tabs while an input on the leaving tab still
	// holds focus. Unlike the keyboard path (keys are swallowed by the focused
	// input, so a tab can't be switched while focused), the mouse path races
	// past the blur — leaving m.isFocusTaken set on the new tab, where it
	// swallows the tab's keybinds. Detect a focus-time tab change and blur the
	// leaving component, then drop the gate. We're on the loop goroutine, so the
	// gate is set directly (no FocusMsg post, which would deadlock mid-dispatch).
	prev := m.tabs.GetActiveComponent()
	m.tabs.OnMouseClick(x, y, uv.MouseLeft, isDouble)
	if m.isFocusTaken && m.tabs.GetActiveComponent() != prev {
		if fr, ok := prev.(component.FocusReleaser); ok {
			fr.ReleaseFocus()
		}
		m.isFocusTaken = false
	}
}

// onMouseContext routes a right click (the secondary/context button) to the
// active component when it opts into the SecondaryMouseTarget seam. Overlay and
// footer rows are ignored: right-click is not an overlay dismiss affordance, and
// the statusline has no context action.
func (m *Model) onMouseContext(x, y int) {
	// A right-click in the same cell must not be counted toward a double-click.
	m.resetClickTracking()
	if m.overlay != nil {
		return
	}
	if y >= m.footerTop() { // footer rows
		return
	}
	if smt, ok := m.tabs.GetActiveComponent().(component.SecondaryMouseTarget); ok {
		smt.OnMouseRight(x, y)
	}
}

// clipboardRead reads the system clipboard. It is a package var so tests can
// substitute a hermetic reader instead of shelling out to pbpaste/xclip.
var (
	clipboardRead         = clipboard.ReadAll
	manualRenameNoClobber = snapshot.RenameNoClobber
)

// onMousePaste routes a middle click (the X11 paste button) by reading the
// clipboard once and handing the text to the MousePasteTarget under the pointer,
// which focuses that input and inserts the text at the click. An empty or
// unreadable clipboard short-circuits. Overlay precedence mirrors onMouseClick —
// inside the overlay forwards, outside is dropped — except a middle click never
// dismisses (it is a paste, not a cancel). The footer row has no input.
//
// On macOS there is no X11 PRIMARY selection, and atotto/clipboard reads the
// system clipboard on every platform, so this pastes the clipboard (the portable
// analogue of the middle-click PRIMARY selection).
func (m *Model) onMousePaste(x, y int) {
	// A middle-click in the same cell must not be counted toward a double-click.
	m.resetClickTracking()
	content, err := clipboardRead()
	if err != nil || content == "" {
		return
	}
	if m.overlay != nil {
		if rect, ok := m.overlayRect(); ok && inRect(x, y, rect) {
			if pt, ok := m.overlay.(component.MousePasteTarget); ok {
				pt.OnMousePaste(x, y, content)
			}
		}
		return
	}
	if y >= m.footerTop() { // footer rows
		return
	}
	if pt, ok := m.tabs.GetActiveComponent().(component.MousePasteTarget); ok {
		pt.OnMousePaste(x, y, content)
	}
}

// dismissOverlay closes the current overlay via its real cancel path so side
// effects fire. Dialog/context-menu/filter-menu Dismiss() run their cancel
// side effects and then call back into the close seam this Model injected on
// open, which clears the slot synchronously; the help overlay has no such seam,
// so the slot is cleared here (mirroring the showHelp Cancel action).
func (m *Model) dismissOverlay() {
	switch o := m.overlay.(type) {
	case *dialog.Model:
		o.Dismiss()
	case *helpoverlay.Model:
		m.overlay = nil
		m.logger.Debug("help dismissed")
		m.releaseFocus()
	case *contextmenu.Model:
		o.Dismiss()
	case *filtermenu.Model:
		o.Dismiss()
	}
}

// inRect reports whether (x, y) lies inside r.
func inRect(x, y int, r component.Rect) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// OnPaste routes a bracketed-paste event to the active overlay (if any),
// otherwise to the active tab's component when it consumes paste. It is the
// uv-native replacement for the old paste event arm. The runtime calls it
// directly.
//
// Paste is delivered to exactly one target, never broadcast: routing through
// the active tab mirrors the key path (handleKey -> tabs.HandleKey) and
// onMousePaste, both of which resolve GetActiveComponent(). Broadcasting fed
// every tab's text input at once, so a paste meant for the query editor also
// landed in the instance picker's search bar.
func (m *Model) OnPaste(ev uv.Event) {
	if m.overlay != nil {
		if p, ok := m.overlay.(interface{ OnPaste(uv.Event) }); ok {
			p.OnPaste(ev)
		}
		return
	}
	if p, ok := m.tabs.GetActiveComponent().(interface{ OnPaste(uv.Event) }); ok {
		p.OnPaste(ev)
	}
}

// HandleKey routes a key event through the precedence chain and reports whether
// it was consumed. It is the uv-native public entry the runtime calls; it wraps
// the private handleKey (the precedence chain) and always reports KeyHandled at
// the root (the root is the terminal handler — there is nowhere to fall through).
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	m.clearMouseHover()
	m.handleKey(ev)
	return component.KeyHandled
}

// clearMouseHover switches visual input modality back to the keyboard by
// dropping pointer-derived highlights from both the overlay and tab hierarchy.
func (m *Model) clearMouseHover() bool {
	changed := m.tabs.ClearMouseHover()
	if ht, ok := m.overlay.(component.MouseHoverTarget); ok && ht.ClearMouseHover() {
		changed = true
	}
	return changed
}

// Update is the typed dispatcher for POSTED APP EVENTS only (the runtime's
// default arm routes them here). Terminal input — resize, key, mouse, paste,
// env — has its own typed entry point on the runtime spine (OnResize/HandleKey/
// OnMouse/OnPaste/ApplyEnv), so Update no longer switches on any terminal-input
// type. It mutates state directly; effects post off-loop via the poster.
//
// A message type is named exactly once on its way to a handler. Events with
// root-side logic get a case here; everything else falls to the default arm,
// which offers the event to the four bool-returning family sub-dispatchers
// (snapshot / instance / logviewer / overlay). The root does not restate their
// types, and anything no family claims is logged rather than swallowed.
//
//nolint:gocyclo // root posted-event dispatch; cases route messages to tabs/components.
func (m *Model) Update(ev uv.Event) {
	m.logger.Debug("msg routed", logging.KeyType, fmt.Sprintf("%T", ev))

	switch ev := ev.(type) {
	case filehandler.IOMsg:
		// id-guard inside each filehandler makes a non-matching IOMsg a no-op,
		// so broadcasting to all consumers is safe.
		m.queryeditor.OnFileIO(ev)
		m.snapshots.OnFileIO(ev)
		m.archives.OnFileIO(ev)

	case queryeditor.QueryLoadMsg:
		m.queryeditor.OnQueryLoaded(ev)

	case queryeditor.EditorFinishedMsg:
		// Both the exec hand-back (runtime.runExec dispatches Done()'s result
		// straight here, on the loop) and openEditor's temp-file error posts.
		m.queryeditor.OnEditorFinished(ev)

	case archivehandler.ArchiveCollectMsg:
		m.handleArchiveCollect(ev)

	case archivehandler.ArchivePollDoneMsg:
		m.archives.OnArchivePollDone(ev)

	case msgs.SnapshotsDirtyMsg:
		// instancepicker is not called here: its in-use coloring refreshes
		// indirectly via snapshothandler's ListFiles, which rebuilds the holders
		// index for the listing.
		m.snapshots.OnSnapshotsDirty(ev)

	case msgs.SnapshotRenamedMsg:
		m.snapshots.OnSnapshotRenamed(ev)
		m.instances.OnSnapshotRenamed(ev.From, ev.To)

	case msgs.FocusMsg:
		if ev.GrabFocus {
			m.logger.Debug("focus taken")
		} else {
			m.logger.Debug("focus released")
		}
		m.isFocusTaken = ev.GrabFocus

	case msgs.FilterAppliedMsg:
		m.logviewer.OnFilterApplied()

	default:
		// Family sub-dispatchers. Each names its message types exactly once and
		// reports whether it claimed the event; the type is not restated here.
		// The families are disjoint, so the short-circuit order is arbitrary.
		// Anything no family claims is a genuinely unrouted message and is
		// logged rather than swallowed.
		if m.handleSnapshot(ev) || m.handleInstance(ev) ||
			m.handleLogviewer(ev) || m.handleOverlay(ev) {
			return
		}
		m.logger.Debug("unhandled msg", logging.KeyType, fmt.Sprintf("%T", ev))
	}
}

// handleSnapshot routes snapshot-related messages: manual save (with
// fetch-in-progress guard and filename dialog), save completion, and
// snapshot restore (which clears the loaded/intended instance state).
// It reports whether the event belonged to this family.
func (m *Model) handleSnapshot(ev uv.Event) bool {
	switch ev := ev.(type) {
	case msgs.ManualSaveSnapshotMsg:
		if m.instances.IsFetching() {
			m.logger.Warn("manual save blocked during fetch")
			dlg := msgs.ShowDialogMsg{
				Title:   "Cannot save snapshot",
				Message: "A fetch is in progress. Wait for it to finish before saving.",
				Buttons: []msgs.DialogButton{{Label: "OK"}},
			}
			msgs.PostAsync(m.poster, dlg)
			return true
		}
		in := uiinput.New()
		dlg := msgs.ShowDialogMsg{
			Title:   "Save Snapshot",
			Message: "Enter filename:",
			Input:   in,
			Buttons: []msgs.DialogButton{
				{Label: "OK", Cmd: m.confirmSave},
				{Label: "Cancel"},
			},
		}
		msgs.PostAsync(m.poster, dlg)

	case msgs.SaveSnapshotMsg:
		m.snapshots.OnSaveSnapshot(ev)

	case msgs.ManualSaveDoneMsg:
		m.snapshots.OnManualSaveDone(ev)

	case msgs.SnapshotRestoreMsg:
		if ev.Err != nil {
			m.logger.Error("snapshot restore failed", logging.KeyError, ev.Err)
			return true
		}
		m.loadedInstance = ""
		m.intendedInstance = ""
		m.logviewer.SetStore(nil)
		// Fan out to the three consumers; snapshothandler is not a consumer.
		m.queryeditor.OnSnapshotRestore(ev)
		m.instances.OnSnapshotRestore(ev)
		m.logviewer.OnSnapshotRestore(ev)
		m.tabs.Select(QueryTab)

	default:
		return false
	}
	return true
}

// handleArchiveCollect routes ArchiveCollectMsg (Enter on a ready, not-expired
// archive in the Archive tab). It triggers the instancepicker collect (which
// streams the archive's background-query results into a snapshot via the .wip ->
// finalize path) and, on success, switches to the Instances tab so the user sees
// per-instance progress. This arm runs ON the loop goroutine; StartCollect spawns
// its own off-loop workers, and the busy-notice dialog is posted off-loop via the
// poster to avoid a self-deadlock on the unbuffered events channel.
func (m *Model) handleArchiveCollect(ev archivehandler.ArchiveCollectMsg) {
	if err := m.instances.StartCollect(ev.Archive); err != nil {
		m.logger.Warn("archive collect refused", logging.KeyError, err)
		msgs.PostAsync(m.poster, msgs.ShowDialogMsg{
			Title:   "Collect",
			Message: "cannot collect right now: " + err.Error(),
			Buttons: []msgs.DialogButton{{Label: "OK"}},
		})
		return
	}
	m.tabs.Select(InstancesTab)
}

// handleInstance routes instance-picker and log-stream messages. Owns
// cursor-save on instance switch, lazy-load stale-drop, store eviction with
// debug.FreeOSMemory, and contextual-row state. It reports whether the event
// belonged to this family;
// the pure pass-through half of the family lives in delegateInstance, which
// the default arm consults before declaring the event unclaimed.
//
//nolint:gocyclo // instance-picker sub-dispatch; each case is a distinct message type with no shared logic to extract.
func (m *Model) handleInstance(ev uv.Event) bool {
	switch ev := ev.(type) {
	case instancepicker.InstanceSelectMsg:
		// Save cursor to the currently loaded instance before switching stores,
		// because SetStore resets the cursor when the new store is empty.
		if m.loadedInstance != "" && m.loadedInstance != ev.CRN {
			if prev := m.instances.GetInstances().FindByCRN(m.loadedInstance); prev != nil {
				x, y, log, logLine, xOffset := m.logviewer.GetCursor()
				prev.SaveCursor(x, y, log, logLine, xOffset)
			}
		}
		// Track the user's current selection so a late-arriving lazy load for a
		// previously selected instance can be discarded instead of clobbering
		// the visible state.
		m.intendedInstance = ev.CRN
		// Only swap logviewer store if the store has data (live streaming)
		// or the user explicitly opened the logviewer. Scrolling through
		// instances in the picker with empty (flushed) stores should not
		// clear the current logviewer display.
		if ev.Store.GetLogCount() > 0 || ev.OpenLogViewer {
			m.logviewer.SetStore(ev.Store)
			if ev.Line > 0 {
				m.logviewer.Center(ev.Line, 0, 0)
			}
		}
		if ev.OpenLogViewer {
			m.tabs.Select(LogsTab)
			if ev.Store.GetLogCount() == 0 {
				m.instances.OpenInstance(ev.CRN)
			}
		}

	case instancepicker.InstanceLoadReadyMsg:
		// Drop stale loads: user has selected a different instance since this
		// load was dispatched. A dropped load is still a routed message.
		if ev.CRN != m.intendedInstance {
			return true
		}
		if ev.Err != nil {
			if !errors.Is(ev.Err, instancepicker.ErrNoBackingFile) &&
				!errors.Is(ev.Err, instancepicker.ErrFetchAborted) {
				if inst := m.instances.GetInstances().FindByCRN(ev.CRN); inst != nil {
					m.logger.Warn("instance load failed", logging.KeyInstance, inst.Name, logging.KeyError, ev.Err)
				}
			}
			return true
		}
		instances := m.instances.GetInstances()
		// Evict previously loaded instance's store to free memory.
		if m.loadedInstance != "" && m.loadedInstance != ev.CRN {
			if prev := instances.FindByCRN(m.loadedInstance); prev != nil {
				prev.Store.ClearData()
			}
			debug.FreeOSMemory()
		}
		m.loadedInstance = ev.CRN
		if inst := instances.FindByCRN(ev.CRN); inst != nil {
			inst.Store.SetLogsRaw(ev.Logs)
			inst.RestoreReadOnlyMessage()
			saved := inst.GetSavedCursor()
			m.logviewer.SetCursor(saved.X, saved.Y, saved.Log, saved.LogLine, saved.XOffset)
		}
		if m.logger.Enabled(context.Background(), slog.LevelDebug) {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			m.logger.Debug("heap stats after instance load",
				"heapInuse_MB", ms.HeapInuse/1024/1024,
				"heapIdle_MB", ms.HeapIdle/1024/1024,
				"heapReleased_MB", ms.HeapReleased/1024/1024,
				"sys_MB", ms.Sys/1024/1024,
			)
		}

	case instancepicker.InstanceLoadPendingMsg:
		if ev.CRN != m.intendedInstance {
			return true
		}
		if inst := m.instances.GetInstances().FindByCRN(ev.CRN); inst != nil {
			m.logger.Info("instance load pending", logging.KeyInstance, inst.Name)
		}

	default:
		// Pass-through half of the same family (no root-side logic). It stays a
		// separate function because inlining it would blow the funlen statement
		// cap on handleInstance, not as removable indirection.
		return m.delegateInstance(ev)
	}
	return true
}

// delegateInstance routes the instance-picker messages that are pure delegations
// (no root-side logic) to their On* handler. Split out of handleInstance so that
// function stays under the funlen statement cap; behavior is identical to inlined
// per-type cases. It reports whether the event was one of them.
//
//nolint:gocyclo // flat instance-picker sub-dispatch; each case is a distinct message type delegating to one On* handler, with no shared logic to extract.
func (m *Model) delegateInstance(ev uv.Event) bool {
	switch ev := ev.(type) {
	case *msgs.LogStreamMsg:
		m.instances.OnLogStream(ev)

	case *msgs.LogStreamDoneMsg:
		m.instances.OnLogStreamDone(ev)

	case instancepicker.InstanceFlushedMsg:
		m.instances.OnInstanceFlushed(ev)

	case instancepicker.MemberAuthResolvedMsg:
		m.instances.OnMemberAuthResolved(ev)

	case instancepicker.MemberAuthFailedMsg:
		m.instances.OnMemberAuthFailed(ev)

	case instancepicker.EnvCredFailedMsg:
		m.instances.OnEnvCredFailed(ev)

	case instancepicker.EnvAuthCancelledMsg:
		m.instances.OnEnvAuthCancelled(ev)

	case instancepicker.PasscodeRequiredMsg:
		m.instances.OnPasscodeRequired(ev)

	case instancepicker.PasscodeSuccessMsg:
		m.instances.OnPasscodeSuccess(ev)

	case instancepicker.PasscodeErrorMsg:
		m.instances.OnPasscodeError(ev)

	case instancepicker.PasscodeCancelledMsg:
		m.instances.OnPasscodeCancelled(ev)

	case instancepicker.WatchTickMsg:
		m.instances.OnWatchTick(ev)

	case instancepicker.DispatchInstanceDoneMsg:
		m.instances.OnDispatchInstanceDone(ev)

	case instancepicker.ArchiveDispatchedMsg:
		// The picker handles only the two distinct ERROR notices (zero-success /
		// write-failed); the SUCCESS dialog was removed. On success the root instead
		// switches to the Archive tab and lands the cursor on the just-created
		// archive row. This is unconditional: the user always wants to see the
		// new archive selected.
		m.instances.OnArchiveDispatched(ev)
		if ev.Saved != "" && ev.Succeeded > 0 {
			m.tabs.Select(ArchiveTab)
			// SelectArchive refreshes the list and defers the cursor move until the
			// rows are populated (ListFiles is async), then selects ev.Saved.
			m.archives.SelectArchive(ev.Saved)
		}

	default:
		return false
	}
	return true
}

// handleLogviewer routes logviewer-domain messages: JQ/search chunk
// results and view-in-context (which switches to the QueryTab after
// dispatching). Timeline jump/close no longer round-trip as messages —
// the logviewer consumes them via the timeline accessor seam in HandleKey.
// It reports whether the event belonged to this family.
func (m *Model) handleLogviewer(ev uv.Event) bool {
	switch ev := ev.(type) {
	case logviewer.JQChunkMsg:
		m.logviewer.OnJQChunk(ev)

	case logviewer.SearchChunkMsg:
		m.logviewer.OnSearchChunk(ev)

	case logviewer.FilterChunkMsg:
		m.logviewer.HandleFilterChunk(ev)

	case msgs.ViewInContextMsg:
		// The three real consumers (queryeditor/instances/logviewer); the
		// snapshothandler does not consume ViewInContextMsg.
		m.queryeditor.OnViewInContext(ev)
		m.instances.OnViewInContext(ev)
		m.logviewer.OnViewInContext(ev)
		m.tabs.Select(QueryTab)

	case msgs.InjectQueryMsg:
		m.queryeditor.OnInjectQuery(ev)
		m.tabs.Select(QueryTab)

	case msgs.SetEditorQueryMsg:
		// Collect sets the editor to the archive's stored query. It does NOT switch
		// tabs here: collect drives its own tab focus (Instances) separately.
		m.queryeditor.OnSetEditorQuery(ev)

	default:
		return false
	}
	return true
}

// handleOverlay manages the dialog and context-menu overlay lifecycle:
// it instantiates the overlay on Show* messages (sizing it via
// assignOverlayRect) and wires each one's close seam to the matching
// close<Overlay> teardown. Teardown is no longer message-driven: the overlays
// call back synchronously on the loop. It reports whether the event belonged to
// this family.
func (m *Model) handleOverlay(ev uv.Event) bool {
	switch ev := ev.(type) {
	case msgs.ShowDialogMsg:
		d := dialog.New(m.bundle, ev)
		d.SetOnClose(m.closeDialog)
		m.overlay = d
		m.assignOverlayRect()

	case msgs.ShowContextMenuMsg:
		cm := contextmenu.New(m.bundle, ev.Items)
		if ev.Anchored {
			cm.WithAnchor(ev.AnchorX, ev.AnchorY)
		}
		cm.WithSuppressCloseRelease(ev.SuppressCloseRelease)
		cm.SetPoster(m.poster)
		cm.SetOnClose(m.closeContextMenu)
		m.overlay = cm
		cm.Focus()
		m.assignOverlayRect()

	case msgs.ShowFilterMenuMsg:
		fm := filtermenu.New(m.bundle, ev.Rules)
		fm.SetPoster(m.poster)
		fm.SetOnClose(m.closeFilterMenu)
		m.overlay = fm
		fm.Focus()
		m.assignOverlayRect()

	default:
		return false
	}
	return true
}

// closeDialog is the dialog's close seam: it clears the overlay slot. Focus is
// not released — the dialog never grabbed it (it drives input via the overlay
// path).
func (m *Model) closeDialog() { m.overlay = nil }

// closeContextMenu is the context menu's close seam: it clears the overlay slot
// and, unless the menu asked otherwise, releases the focus the menu grabbed on
// open. The menu decides releaseFocus (see contextmenu.activateFocused).
//
// Clearing is unconditional: the close runs synchronously inside the menu's own
// key/mouse dispatch, so nothing can have replaced the slot in between. A menu
// item that opens a dialog posts ShowDialogMsg off-loop, so that dialog lands in
// a later dispatch and installs itself after this teardown.
func (m *Model) closeContextMenu(releaseFocus bool) {
	m.overlay = nil
	if releaseFocus {
		m.releaseFocus()
	}
}

// closeFilterMenu is the filter menu's close seam: it clears the overlay slot
// and releases the focus the menu grabbed on open.
func (m *Model) closeFilterMenu() {
	m.overlay = nil
	m.releaseFocus()
}

// handleKey routes keyboard input through the precedence chain: if an overlay
// is open, overlay dismiss and overlay key handling come first; otherwise
// focus-taken sends keys straight to the active tab, while default flow tries
// tab-selector and regular handlers before falling through to the active tab.
//
// There is no always-handled group here: the only root binding that ever wanted
// one is ForceQuit, which is display-only (m.kh.displayOnly) because the exit
// gesture is counted on the runtime loop above every model keybind.
//
// Actions are void (they run side effects directly and post off-loop via the
// poster); HandleKey on the overlay/tabs returns just a KeyResult (R5a B2c).
func (m *Model) handleKey(ev uv.KeyPressEvent) {
	if m.overlay != nil {
		if m.runOverlayDismissKey(ev) {
			return
		}
		if kt, ok := m.overlay.(component.KeyTarget); ok {
			kt.HandleKey(ev)
		}
		return
	}

	switch {
	case m.isFocusTaken:
		m.tabs.HandleKey(ev)
	default:
		if m.kh.tabselector.Run(ev) {
			return
		}
		if m.kh.regular.Run(ev) {
			return
		}
		m.tabs.HandleKey(ev)
	}
}

// DrawTo composes the model's tabs, statusline, and active overlay into the
// caller-owned screen and returns the cursor, with no string serialization.
// It is the pure draw pass used by the ui/runtime spine. Returns nil when the
// screen size is not yet known (no resize seen yet). The runtime applies
// AltScreen / Bg / Fg / MouseMode at screen init, so DrawTo carries none of
// that view metadata (View() was removed in R5a B2c).
func (m *Model) DrawTo(s component.Screen) *component.Cursor {
	w, h := m.screenW, m.screenH
	if w < 1 || h < 1 {
		return nil
	}

	// The terminal retains cells omitted from a fresh compose buffer. Seed every
	// cell so a tab immediately repaints rows reclaimed from a status provider.
	for y := range h {
		canvas.FillRow(s, y, w, uv.Style{})
	}

	cursor := m.tabs.Draw(s)
	if m.hasStatusLine() {
		provider, _ := m.statusProvider()
		m.statusLine.Draw(s, provider.StatusVariants())
	}
	if m.hasKeyHintLine() {
		m.keyHintLine.SetBindings(m.GetKeybinds())
		m.keyHintLine.Draw(s)
	}

	if m.overlay != nil {
		// Re-apply rect each frame so Sizer-driven layout tracks content
		// changes between resizes; SetRect is layout-only here.
		m.assignOverlayRect()
		m.applyOverlayDim(s, w, h)
		cursor = m.overlay.Draw(s)
	}

	return cursor
}

// GetKeybinds collects the active tab's bindings plus the display-only and
// regular root bindings, then groups them into the sectioned help-menu order
// (Local, Global, Navigation) via keys.GroupByCategory. The grouped slice
// stays index-aligned with the help overlay's rows, so doKeybindAction resolves
// a focused row by the same index; the help overlay renders each row's Category
// as a leading colored column.
func (m *Model) GetKeybinds() []keys.Binding {
	b := append(m.tabs.GetActiveComponent().GetKeybinds(), m.kh.displayOnly...)
	b = append(b, m.kh.regular.GetKeybinds()...)
	return keys.GroupByCategory(b)
}

// doKeybindAction resolves the help overlay's currently focused entry to its
// action and runs it (for effect; keys.Action is void), then dismisses the
// overlay and releases focus off-loop. The fuzzy cursor lives on the inner list
// (exposed via helpoverlay.Model.GetCursor); GetKeybinds returns the same slice
// the help overlay was populated from.
func (m *Model) doKeybindAction() {
	if h, ok := m.overlay.(*helpoverlay.Model); ok {
		if a := m.GetKeybinds()[h.GetCursor()].Action; a != nil {
			a()
		}
	}
	m.overlay = nil
	// Release focus off-loop: doKeybindAction runs on the loop (via a keybind
	// action), so a bare PostCritical would deadlock the unbuffered events
	// channel mid-dispatch.
	m.releaseFocus()
}

// runOverlayDismissKey runs the help overlay's own key group when the help
// overlay is open (showHelp maps Cancel to hide and Accept to run the focused
// binding), reporting whether an action ran. Dialog handles ESC inside its own
// HandleKey via its alwaysHandle, so dialog overlays report false here.
func (m *Model) runOverlayDismissKey(ev uv.KeyPressEvent) bool {
	if _, isHelp := m.overlay.(*helpoverlay.Model); !isHelp {
		return false
	}
	return m.kh.showHelp.Run(ev)
}

// validateSaveFilename reduces a manual-save filename (free text from the save
// dialog) to a safe basename inside the managed snapshot dir, normalizes it to
// end in .lognav, and rejects the reserved names "latest" (which Resolve
// treats as a sentinel) and "auto-" (for generated automatic snapshots).
// Without the basename check a name like "../foo" or "sub/dir" would make the
// final rename land outside the snapshot dir.
func validateSaveFilename(name string) (string, error) {
	base, ok := sessionbus.SanitizeBasename(strings.TrimSpace(name))
	if !ok {
		return "", errors.New("enter a plain filename (no /, \\ or ..)")
	}
	if !strings.HasSuffix(base, snapshot.FileExt) {
		base += snapshot.FileExt
	}
	if base == "latest"+snapshot.FileExt {
		return "", errors.New(`"latest" is a reserved snapshot name`)
	}
	if strings.HasPrefix(base, "auto-") {
		return "", errors.New(`names beginning with "auto-" are reserved for automatic snapshots`)
	}
	return base, nil
}

// checkSaveTarget refuses a manual save that would destroy an existing
// snapshot. v1 has no overwrite-confirmation flow: an occupied name is simply
// refused and the dialog stays open for a different one. This includes the
// caller's own backing file, which is an existing file like any other.
//
// Runs on the loop goroutine from confirmSave: one HoldersIndex scan plus one
// Stat of the snapshot dir, once per save — cheap enough not to need the poster.
func checkSaveTarget(name string) error {
	dir, err := snapshot.Dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	// Held-by-a-peer is reported first: it is the more specific reason, and it
	// also covers a claim whose file a peer is mid-rename on.
	if ix, ierr := snapshot.HoldersIndex(); ierr == nil && ix.InUseByOther(path) {
		return fmt.Errorf("snapshot %s is in use by another session", name)
	}
	if _, serr := os.Stat(path); serr != nil {
		if errors.Is(serr, fs.ErrNotExist) {
			return nil
		}
		return serr
	}
	return fmt.Errorf("snapshot %s already exists — choose another name", name)
}

// confirmSave is the manual-save dialog's OK action. A fetch can begin while
// the dialog is open; saving against its live backing .wip would write a
// partial, inconsistent snapshot. A non-nil error keeps the dialog open with
// the message.
func (m *Model) confirmSave(filename string) error {
	if m.instances.IsFetching() {
		return errors.New("a fetch started while the dialog was open; wait for it to finish")
	}
	normalized, err := validateSaveFilename(filename)
	if err != nil {
		return err
	}
	if err := checkSaveTarget(normalized); err != nil {
		return err
	}
	m.saveSnapshot(normalized)
	return nil
}

// saveSnapshot performs the manual-snapshot file IO off-loop via the poster and
// posts ManualSaveDoneMsg on success. The snapshot button Cmd that calls this
// runs on the loop (inside the dialog confirm path), so the IO must move into a
// poster.Go body to avoid blocking dispatch.
//
//nolint:gocyclo // Frame copy, durable publication, and cleanup outcomes share one worker lifecycle.
func (m *Model) saveSnapshot(filename string) {
	if m.poster == nil {
		return
	}
	// Capture loop-owned state synchronously (saveSnapshot runs on the loop via
	// the dialog OK Cmd). The IO — open backing, copy frames, fsync, rename — runs
	// off-loop. No store locks are taken: the durable backing frames are
	// authoritative, so the path never touches in-memory stores.
	snap := m.instances.SnapshotMeta()
	backingPath := m.instances.GetBackingPath()

	m.poster.Go(func(ctx context.Context) {
		snapshotDir, err := snapshot.Dir()
		if err != nil {
			m.logger.Error("snapshot save failed", logging.KeyError, err)
			return
		}

		// CreateWip exclusively creates a hidden PID-stamped file so concurrent
		// saves cannot truncate each other and stale cleanup can identify its owner.
		wip, err := snapshot.CreateWip(snapshotDir, os.Getpid())
		if err != nil {
			m.logger.Error("snapshot save failed", logging.KeyError, err)
			return
		}
		wipPath := wip.Name()
		fail := func(err error) {
			_ = wip.Close()
			_ = os.Remove(wipPath)
			m.logger.Error("snapshot save failed", logging.KeyError, err)
		}

		// Source the durable log frames from the finalized backing container. If it
		// is absent (no fetch / all-empty fetch discarded) or fails to open (e.g.
		// the backing was deleted concurrently), degrade to empty frames via a nil
		// src rather than losing the save entirely.
		var src *snapshot.Container
		if backingPath != "" {
			if opened, oerr := snapshot.OpenContainerReadOnly(backingPath); oerr == nil {
				src = opened
				defer func() { _ = src.Close() }()
			} else {
				m.logger.Warn("manual save: backing open failed, degrading to empty frames", "path", backingPath, logging.KeyError, oerr)
			}
		}

		c := snapshot.NewWriter(wip)
		if err := snapshot.CopyWithState(c, src, snap); err != nil {
			fail(err)
			return
		}
		if err := c.Close(); err != nil { // flush bufio (NewWriter holds no fd to fsync)
			fail(err)
			return
		}
		if err := wip.Sync(); err != nil { // durability: data on disk before the dir entry flips
			fail(err)
			return
		}
		if err := wip.Close(); err != nil {
			fail(err)
			return
		}

		// RenameNoClobber, not os.Rename: os.Rename would silently destroy a
		// snapshot that appeared under this name since confirmSave's check —
		// possibly one another live session is reading. This is the race backstop
		// for that on-loop check, so the collision is surfaced, not just logged.
		finalPath := filepath.Join(snapshotDir, filename)
		if err := manualRenameNoClobber(wipPath, finalPath); err != nil {
			var cleanupErr *snapshot.RenameCleanupError
			if !errors.As(err, &cleanupErr) {
				_ = os.Remove(wipPath)
				m.logger.Error("snapshot save failed", logging.KeyError, err)
				if snapshot.IsExistErr(err) {
					// Off-loop (inside poster.Go): a direct post is correct here.
					_ = m.poster.PostCritical(ctx, msgs.ShowDialogMsg{
						Title:   "Cannot save snapshot",
						Message: filename + " appeared while saving — nothing was overwritten. Save again under another name.",
						Buttons: []msgs.DialogButton{{Label: "OK"}},
					})
				}
				return
			}
			m.logger.Error("manual snapshot saved but WIP cleanup failed", logging.KeyError, cleanupErr)
		}

		m.logger.Info("manual snapshot saved", "filename", filename)
		_ = m.poster.PostCritical(ctx, msgs.ManualSaveDoneMsg{Name: filename})
		// Manual save creates a brand-new file via copy+rename and never touches
		// setBacking, so it does not reach the claim broadcast — yet spec §2
		// requires snapshots_dirty after a save. This worker is OFF-LOOP (inside
		// m.poster.Go), so a direct broadcast needs no poster.Go wrap.
		if m.broadcaster != nil {
			_ = m.broadcaster.Broadcast(ctx, sessionbus.MethodSnapshotsDirty, sessionbus.SnapshotsDirtyParams{})
		}
	})
}
