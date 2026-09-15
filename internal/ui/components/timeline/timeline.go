package timeline

import (
	"slices"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
)

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.KeyTarget        = (*Model)(nil)
	_ component.MouseTarget      = (*Model)(nil)
	_ component.MouseHoverTarget = (*Model)(nil)
)

// Model is the timeline view. It is constructed once by the logviewer and
// kept dormant until opened. Open() snapshots the store into local buckets
// and positions the cursor; Close() drops the snapshot.
type Model struct {
	bundle   deps.Bundle
	drawRect component.Rect

	kh *keys.Handler

	buckets        []bucket
	visibleIndices []int // indices into buckets of the rows actually rendered
	maxTotal       int   // largest bucket.total across buckets; used for flamegraph scaling
	cursor         int   // index into visibleIndices, NOT buckets
	yOff           int
	hoverIdx       int // visibleIndices index under the mouse pointer, -1 = none

	poster msgs.Poster

	// Accessor state set by HandleKey; valid only immediately after the
	// HandleKey call that set them. The logviewer reads and acts on these
	// after delegating a key (A3/R5a).
	pendingJumpIdx  int
	pendingJumpLine int
	pendingJumpOK   bool
	wantClose       bool
}

// New constructs a dormant Model.
func New(bundle deps.Bundle) *Model {
	m := &Model{bundle: bundle, hoverIdx: -1}
	bindKeyhandlersToModel(m)
	return m
}

// SetPoster injects the runtime poster used to post events off-loop (R5a).
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
}

// requestQuit requests a clean exit. It is the quit hook wired into the Quit
// keybind; keybind actions run on the loop goroutine, hence msgs.RequestQuit.
// It stays a method because the binding needs a func value.
func (m *Model) requestQuit() {
	msgs.RequestQuit(m.poster)
}

// Init implements component.Component. The timeline has no startup work.
func (m *Model) Init() {}

// HandleKey dispatches a key event through the timeline's single key-handler
// group. Returns KeyHandled when an action ran, KeyIgnored otherwise.
//
// The accessor state (PendingJump / WantsClose) is cleared at the top of
// every call and set only by the action that ran — it is valid only
// immediately after this call returns, before the next HandleKey.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	m.pendingJumpOK = false
	m.wantClose = false
	if m.kh.Run(ev) {
		return component.KeyHandled
	}
	return component.KeyIgnored
}

// requestJump sets the pending-jump accessor state for the bucket under the
// cursor. It is the shared action behind both the Accept keybind and a click
// on the already-focused bucket row. An empty timeline or a bucket with no
// logs (firstLogIdx < 0) requests close instead of a jump, matching the
// keyboard behaviour exactly.
func (m *Model) requestJump() {
	if len(m.visibleIndices) == 0 {
		m.wantClose = true
		return
	}
	b := m.buckets[m.visibleIndices[m.cursor]]
	if b.firstLogIdx < 0 {
		m.wantClose = true
		return
	}
	m.pendingJumpIdx = b.firstLogIdx
	m.pendingJumpLine = 0
	m.pendingJumpOK = true
}

// setCursor moves the cursor to a visibleIndices index and reclamps the scroll
// window, mirroring what the keyboard move actions do (assign m.cursor, then
// clampScroll). Called by mouse clicks that select a bucket row.
func (m *Model) setCursor(idx int) {
	m.cursor = idx
	m.clampScroll()
}

// OnMouseClick implements component.MouseTarget. The timeline is a read-only
// overlay owned by the logviewer, which routes clicks here after hit-testing
// its own region. A click inside a rendered bucket row either selects that row
// (cursor move) or, if the row is already the cursor, triggers the same jump
// the Accept key produces.
//
// y maps to a visibleIndices index using the inverse of the Draw layout in
// render.go placeOnScreen:
//
//	placeRow(..., rect.Y+(i-m.yOff), ...)  ⇒  i = m.yOff + (y - m.drawRect.Y)
//
// There is no header row, so bucket rows begin at drawRect.Y. x is unused —
// rows are full-width. Returns true when the click lands on a bucket row.
func (m *Model) OnMouseClick(_, y int, _ uv.MouseButton) bool {
	m.pendingJumpOK = false
	m.wantClose = false

	idx := m.yOff + (y - m.drawRect.Y)
	if idx < 0 || idx >= len(m.visibleIndices) {
		return false
	}
	if idx == m.cursor {
		m.requestJump()
	} else {
		m.setCursor(idx)
	}
	return true
}

// OnMouseHover implements component.MouseHoverTarget: it records the bucket row
// under the pointer (same y→index map as OnMouseClick) so placeOnScreen can tint
// it. Moving off the bucket rows clears the hover. x is ignored (rows are
// full-width) and the cursor is never moved — hover is purely visual. Returns
// true when the hovered row (or has-hover state) changed.
func (m *Model) OnMouseHover(_, y int) bool {
	idx := m.yOff + (y - m.drawRect.Y)
	if idx < 0 || idx >= len(m.visibleIndices) {
		return m.ClearMouseHover()
	}
	if m.hoverIdx == idx {
		return false
	}
	m.hoverIdx = idx
	return true
}

// ClearMouseHover drops any active hover, returning true only when it actually
// cleared.
func (m *Model) ClearMouseHover() bool {
	if m.hoverIdx < 0 {
		return false
	}
	m.hoverIdx = -1
	return true
}

// PendingJump reports a requested jump target set by the last HandleKey call.
// logIdx is the first log index in the focused bucket, logLine is always 0
// (reserved for future sub-line granularity). ok is true only when a jump was
// requested; false means no jump is pending. Valid only immediately after the
// HandleKey call that set it — the logviewer reads-and-acts each key cycle.
func (m *Model) PendingJump() (logIdx, logLine int, ok bool) {
	return m.pendingJumpIdx, m.pendingJumpLine, m.pendingJumpOK
}

// WantsClose reports whether the last HandleKey call requested closing the
// overlay. Valid only immediately after the HandleKey call that set it.
func (m *Model) WantsClose() bool {
	return m.wantClose
}

// SetRect stores the assigned rect that Draw renders into and reclamps the
// scroll state so the cursor and yOff stay in range when the viewport shrinks.
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
	m.clampScroll()
}

// Draw writes the timeline's rows onto the parent screen at drawRect.
func (m *Model) Draw(s component.Screen) *component.Cursor {
	m.placeOnScreen(s, m.drawRect)
	return nil
}

// GetKeybinds implements component.Component.
func (m *Model) GetKeybinds() []keys.Binding {
	return m.kh.GetKeybinds()
}

// Open snapshots the store into local buckets and positions the cursor on
// the bucket whose time range contains currentTS. Called by the logviewer
// when the user presses Timeline. If haveCurrent is false, the cursor starts
// at the top.
//
// The bucket count is clamped to at most logCount so sparse stores don't get
// an overstretched timeline with mostly-empty rows. maxTotal is cached for
// the flamegraph scale factor used in render. If HideEmptyBuckets is set
// (the default), zero-total buckets are filtered out of visibleIndices so
// quiet time slices collapse and the cursor skips over them.
func (m *Model) Open(store Store, currentTS int64, haveCurrent bool) {
	n := m.timelineBuckets()
	if count := store.GetLogCount(); count > 0 && count < n {
		n = count
	}
	m.buckets = Build(store, n)
	m.visibleIndices = computeVisibleIndices(m.buckets, m.bundle.Config.Logs.TimelineHideEmptyBuckets)
	m.hoverIdx = -1

	m.maxTotal = 0
	for _, b := range m.buckets {
		if b.total > m.maxTotal {
			m.maxTotal = b.total
		}
	}

	m.cursor = 0
	if haveCurrent {
		rawIdx := bucketIndexForTS(m.buckets, currentTS)
		if i := slices.Index(m.visibleIndices, rawIdx); i >= 0 {
			m.cursor = i
		}
	}
	m.yOff = 0
	m.clampScroll()
}

// Close drops the snapshot and transient action state so GC can reclaim it
// when the logviewer flips its active flag off.
func (m *Model) Close() {
	m.buckets = nil
	m.visibleIndices = nil
	m.maxTotal = 0
	m.cursor = 0
	m.yOff = 0
	m.pendingJumpOK = false
	m.wantClose = false
}

// bucketIndexForTS returns the index of the bucket whose time range contains
// the given timestamp. Returns 0 if no bucket matches (defensive fallback).
func bucketIndexForTS(buckets []bucket, ts int64) int {
	for i, b := range buckets {
		if b.startMicro <= ts && ts < b.endMicro {
			return i
		}
	}
	return 0
}
