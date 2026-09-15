package logviewer

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// Hover benches cover the two code paths added by the mouse/cursor branch that
// run at frame / motion-event frequency:
//
//   - the Step 3.5 hover tint inside renderLine (per-frame, in the Draw cell-blit
//     phase — NOT the getRenderCacheEntry path the other render benches exercise);
//   - OnMouseHover, the redraw-gate handler called per mouse-motion event under
//     any-event tracking (?1003), whose false return averts a whole Draw.
//
// Both paths are net-new on this branch — there is no dev counterpart to A/B
// against, so these are absolute characterizations:
//   - DrawNoHover vs DrawHover isolates the incremental per-frame tint cost.
//   - OnMouseHover_SameRow (gate no-op) measured against DrawNoHover is the
//     justification for the gate: the no-op must be far cheaper than the Draw it
//     skips.

// benchHoverRow is the visible log row tinted by the hover benches. With the
// cursor parked at log 0 it is never the cursor row, so the tint block actually
// runs (isUnderHover && !isUnderCursor), and it sits inside the benchScrollViewport
// window so the row is drawn.
const benchHoverRow = 5

// BenchmarkRender_DrawNoHover renders the full viewport with hover inactive. It
// is the baseline for the tint cost and also captures the cost of the new
// isUnderHover field threaded through placeLogs/renderLine on the common
// (no-pointer) path.
func BenchmarkRender_DrawNoHover(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchScrollViewport)
	s := uv.NewScreenBuffer(120, benchScrollViewport)
	m.Draw(s) // settle layout/cursor before timing

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.Draw(s)
	}
}

// BenchmarkRender_DrawHover renders the same viewport with the hover tint active
// on a single visible non-cursor row. The delta against DrawNoHover is the
// per-frame cost of the Step 3.5 cell wash (one row of Bg writes). HoverRowBg
// defaults to BrightBlack, so IsSet() is true and the loop runs.
func BenchmarkRender_DrawHover(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchScrollViewport)
	s := uv.NewScreenBuffer(120, benchScrollViewport)
	m.Draw(s) // settle layout/cursor before timing

	// Park the hover on a visible non-cursor row so the tint block fires.
	m.hasHover, m.hoverLog, m.hoverLine = true, benchHoverRow, 0

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.Draw(s)
	}
}

// BenchmarkHover_OnMouseHover_SameRow measures the gate no-op: the pointer moves
// but stays on the same log row, so OnMouseHover runs the hit-test
// (subRects + logIdxAtRow) and early-returns false without mutating state. This
// is the cost the gate pays on every same-row motion event to avoid a redraw;
// compare it against BenchmarkRender_DrawNoHover to value the gate.
func BenchmarkHover_OnMouseHover_SameRow(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchScrollViewport)
	s := uv.NewScreenBuffer(120, benchScrollViewport)
	m.Draw(s)

	_, logsR, _ := m.subRects()
	x, y := logsR.X+1, logsR.Y+benchHoverRow
	m.OnMouseHover(x, y) // prime hover so the loop hits the same-row early return

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m.OnMouseHover(x, y)
	}
}

// BenchmarkHover_OnMouseHover_Move measures the state-changing path: the pointer
// alternates between two adjacent log rows so every call re-runs the hit-test and
// updates the hover row, returning true (the caller would then mark the frame
// dirty). This is the upper-bound per-event handler cost.
func BenchmarkHover_OnMouseHover_Move(b *testing.B) {
	store := newBenchStore(b, benchLogCount)
	m := buildBenchLogviewer(b, store, benchScrollViewport)
	s := uv.NewScreenBuffer(120, benchScrollViewport)
	m.Draw(s)

	_, logsR, _ := m.subRects()
	x := logsR.X + 1

	b.ResetTimer()
	b.ReportAllocs()
	row := 0
	for b.Loop() {
		row ^= 1
		m.OnMouseHover(x, logsR.Y+benchHoverRow+row)
	}
}
