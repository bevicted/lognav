package logviewer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/ui/components/component"
)

// TestScrollViewport_DragAtEdge verifies the wheel-scroll viewport pan. With a
// rect of H=20 the logs area is H=20 and the default scrolloff is 7, so the
// top margin sits at row 7 and the bottom margin at row 20-1-7 = 12.
// The logs are single-line (collapsed), so log index k renders one row per step.
// Cases are set up white-box by parking the cursor at a known (log, screen-row)
// and the expectations are hand-derived from placeLogs' split-around-cursor layout.
//
//   - phase 1: the pan keeps the cursor on its logical line and only moves its
//     screen row.
//   - phase 2: the pan would push the cursor past the scrolloff margin, so the
//     cursor is dragged onto the line now sitting at the margin.
//   - clamp: panning past the buffer end pins to top/bottom.
//   - boundary: further events are ignored once an end is visible.
func TestScrollViewport_DragAtEdge(t *testing.T) {
	t.Parallel()
	const (
		scrollOff    = 7
		topMargin    = scrollOff // 7
		bottomMargin = 20 - 1 - scrollOff
	)
	tests := []struct {
		name     string
		startLog int
		startY   int
		scroll   int // signed display rows: + down (later), - up (earlier)
		wantLog  int
		wantY    int
	}{
		{
			name:     "phase1 down keeps logical line, rides cursor up",
			startLog: 20, startY: 15, scroll: 5,
			wantLog: 20, wantY: 10,
		},
		{
			name:     "phase2 down drags cursor onto top-margin line",
			startLog: 20, startY: 10, scroll: 5,
			wantLog: 22, wantY: topMargin,
		},
		{
			name:     "phase1 up keeps logical line, rides cursor down",
			startLog: 20, startY: 3, scroll: -5,
			wantLog: 20, wantY: 8,
		},
		{
			name:     "phase2 up drags cursor onto bottom-margin line",
			startLog: 20, startY: 8, scroll: -5,
			wantLog: 19, wantY: bottomMargin,
		},
		{
			name:     "large down pan pins to bottom",
			startLog: 20, startY: 10, scroll: 20,
			wantLog: 39, wantY: 19,
		},
		{
			name:     "large up pan pins to top",
			startLog: 20, startY: 8, scroll: -20,
			wantLog: 0, wantY: 0,
		},
		{
			name:     "down with end visible does not overscroll",
			startLog: 38, startY: 10, scroll: 5,
			wantLog: 38, wantY: 10,
		},
		{
			name:     "up with start visible does not overscroll",
			startLog: 1, startY: 8, scroll: -5,
			wantLog: 1, wantY: 8,
		},
		{
			name:     "down at bottom remains pinned",
			startLog: 39, startY: 19, scroll: 5,
			wantLog: 39, wantY: 19,
		},
		{
			name:     "up at top remains pinned",
			startLog: 0, startY: 0, scroll: -5,
			wantLog: 0, wantY: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newViewerModel(t, 40)
			_, logsR, _ := m.subRects()
			require.Equal(t, 20, logsR.H, "setup: logs area is full height")

			m.cursor.log, m.cursor.logLine, m.cursor.y = tt.startLog, 0, tt.startY
			m.ScrollViewport(tt.scroll)

			assert.Equal(t, tt.wantLog, m.cursor.log, "cursor log")
			assert.Equal(t, tt.wantY, m.cursor.y, "cursor screen row")
		})
	}
}

// logviewer.Model implements the viewport-scroll seam.
var _ component.ScrollTarget = (*Model)(nil)

// TestOnMouseScroll_DelegatesToScrollViewport verifies a vertical wheel notch
// pans the viewport via ScrollViewport and is consumed.
func TestOnMouseScroll_DelegatesToScrollViewport(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 40)
	m.cursor.log, m.cursor.logLine, m.cursor.y = 20, 0, 10

	ok := m.OnMouseScroll(5, 5, 5) // wheel down 5
	assert.True(t, ok, "a wheel notch over the log area must be consumed")
	assert.Equal(t, 22, m.cursor.log, "wheel down must pan the viewport (drag cursor at margin)")
}

// TestOnMouseScroll_TimelineActive_NotConsumed verifies that while the timeline
// overlay is active a wheel notch is not consumed (it falls back to the synthetic
// arrow path, which the timeline handles), leaving the log cursor untouched.
func TestOnMouseScroll_TimelineActive_NotConsumed(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 40)
	m.cursor.log, m.cursor.logLine, m.cursor.y = 20, 0, 10
	m.timelineActive = true

	ok := m.OnMouseScroll(5, 5, 5)
	assert.False(t, ok, "timeline-active wheel must not be consumed by the log viewport")
	assert.Equal(t, 20, m.cursor.log, "timeline-active wheel must not pan the log viewport")
}

// TestScrollViewport_NoStore verifies ScrollViewport is a no-op without a store.
func TestScrollViewport_NoStore(t *testing.T) {
	t.Parallel()
	m, _ := newViewerModel(t, 0)
	m.store = nil
	m.ScrollViewport(5) // must not panic
}
