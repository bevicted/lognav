package timeline

import (
	"testing"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMultiBucketModel builds a timeline over a store with three logs at
// well-separated timestamps so Build() yields three non-empty buckets. With
// the default TimelineBuckets=100 clamped to logCount=3, the span [0,300] is
// split into three step-100 buckets ([0,100),[100,200),[200,301]) each holding
// one log, so visibleIndices == [0,1,2] and every row is non-empty regardless
// of TimelineHideEmptyBuckets. firstLogIdx is 0, 1, 2 for the three buckets.
//
// With SetRect(Y=0, H=24) and yOff=0, the Draw mapping
//
//	screen-y = rect.Y + (visibleIndices-index - yOff)   (render.go placeOnScreen)
//
// places visibleIndices[0]→y=0, [1]→y=1, [2]→y=2. Rows y>=3 are below the last
// bucket and out of range. There is no header row: bucket rows start at rect.Y.
func newMultiBucketModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	store := newFakeStore([]LogMeta{
		{Idx: 0, TSMicro: 0, Severity: icl.SeverityInfo},
		{Idx: 1, TSMicro: 100, Severity: icl.SeverityWarning},
		{Idx: 2, TSMicro: 200, Severity: icl.SeverityError},
	})
	m.Open(store, 0, false)
	// Sanity: three visible bucket rows, cursor parked at the top.
	require.Equal(t, []int{0, 1, 2}, m.visibleIndices)
	require.Equal(t, 0, m.cursor)
	require.Equal(t, 0, m.yOff)
	return m
}

// timeline implements the hover seam.
var _ component.MouseHoverTarget = (*Model)(nil)

// TestTimelineModel_OnMouseHover_SetsHoverNoCursorMove verifies hovering a bucket
// row records it as the hover row without moving the cursor or requesting a jump,
// and reports a change only on a row move.
func TestTimelineModel_OnMouseHover_SetsHoverNoCursorMove(t *testing.T) {
	t.Parallel()
	m := newMultiBucketModel(t)
	require.Equal(t, 0, m.cursor, "setup: cursor at bucket 0")

	changed := m.OnMouseHover(5, 2) // bucket row idx 2 (non-cursor)

	assert.True(t, changed, "hovering a bucket row reports a change")
	assert.Equal(t, 2, m.hoverIdx, "hover records the bucket under the pointer")
	assert.Equal(t, 0, m.cursor, "hover must not move the cursor")
	_, _, ok := m.PendingJump()
	assert.False(t, ok, "hover must not request a jump")
	assert.False(t, m.OnMouseHover(6, 2), "re-hovering the same row reports no change")
}

// TestTimelineModel_OnMouseHover_OffRows_Clears verifies moving below the last
// bucket clears an active hover.
func TestTimelineModel_OnMouseHover_OffRows_Clears(t *testing.T) {
	t.Parallel()
	m := newMultiBucketModel(t)
	require.True(t, m.OnMouseHover(5, 2), "setup: hover a bucket")

	changed := m.OnMouseHover(5, 50) // below the last bucket

	assert.True(t, changed, "clearing an active hover reports a change")
	assert.Equal(t, -1, m.hoverIdx, "moving off the buckets clears the hover")
}

// TestTimelineModel_HoverRow_TintsBucket verifies the hovered (non-cursor) bucket
// row is tinted with the configured hover background while the cursor row is not.
func TestTimelineModel_HoverRow_TintsBucket(t *testing.T) {
	t.Parallel()
	m := newMultiBucketModel(t)
	require.True(t, m.bundle.Config.Style.HoverRowBg.IsSet(), "setup: hover bg has a default color")
	want := m.bundle.Config.Style.HoverRowBg.Color
	require.True(t, m.OnMouseHover(5, 2), "setup: hover bucket idx 2 (non-cursor)")

	s := uv.NewScreenBuffer(80, 24)
	m.Draw(s)

	const probe = 2 // inside the always-present timestamp label
	assert.Equal(t, want, s.CellAt(probe, 2).Style.Bg, "hovered bucket row must be tinted")
	assert.NotEqual(t, want, s.CellAt(probe, 0).Style.Bg, "cursor row must NOT be tinted")
	assert.NotEqual(t, want, s.CellAt(probe, 1).Style.Bg, "a non-hovered, non-cursor row must not be tinted")
}

func TestTimelineModel_OnMouseClick_NonCursorRow_MovesCursorNoJump(t *testing.T) {
	t.Parallel()
	m := newMultiBucketModel(t)

	// visibleIndices[2] renders at screen-y = rect.Y + (2 - yOff) = 0 + 2 = 2.
	const targetIdx = 2
	const y = 0 + targetIdx // rect.Y=0, yOff=0

	consumed := m.OnMouseClick(5, y, uv.MouseLeft)
	assert.True(t, consumed, "in-bucket click must be consumed")
	assert.Equal(t, targetIdx, m.cursor, "cursor must move to the clicked bucket row")

	_, _, ok := m.PendingJump()
	assert.False(t, ok, "clicking a non-cursor row must not request a jump")
	assert.False(t, m.WantsClose(), "moving the cursor must not request close")
}

func TestTimelineModel_OnMouseClick_CursorRow_RequestsJump(t *testing.T) {
	t.Parallel()
	m := newMultiBucketModel(t)

	// Cursor sits on visibleIndices[0], which renders at screen-y=0.
	const y = 0

	consumed := m.OnMouseClick(5, y, uv.MouseLeft)
	assert.True(t, consumed, "in-bucket click must be consumed")

	logIdx, logLine, ok := m.PendingJump()
	require.True(t, ok, "clicking the cursor bucket must request a jump")
	assert.Equal(t, 0, logIdx, "jump target is the focused bucket's first log index")
	assert.Equal(t, 0, logLine)
	// Mirror the Accept key exactly: a populated-bucket jump sets PendingJump
	// but leaves WantsClose false — the logviewer closes the overlay when it
	// reads the pending jump (see TestTimelineJumpOnAccept).
	assert.False(t, m.WantsClose(), "WantsClose stays false when a jump is pending, like Accept")

	// Sibling assertion: a fresh model jumped via the Accept key produces the
	// identical pending-jump + close state when the cursor is on the same row.
	km := newMultiBucketModel(t)
	res := km.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	require.Equal(t, component.KeyHandled, res)
	kIdx, kLine, kOK := km.PendingJump()
	assert.Equal(t, ok, kOK)
	assert.Equal(t, logIdx, kIdx)
	assert.Equal(t, logLine, kLine)
	assert.Equal(t, m.WantsClose(), km.WantsClose())
}

func TestTimelineModel_OnMouseClick_OutsideBuckets_Ignored(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		y    int
	}{
		{name: "above first bucket row", y: -1},
		{name: "below last bucket row", y: 3}, // only y=0,1,2 hold buckets
		{name: "far below in empty viewport", y: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMultiBucketModel(t)

			consumed := m.OnMouseClick(5, tt.y, uv.MouseLeft)
			assert.False(t, consumed, "click outside bucket rows must not be consumed")
			assert.Equal(t, 0, m.cursor, "cursor must not move on an out-of-range click")

			_, _, ok := m.PendingJump()
			assert.False(t, ok, "out-of-range click must not request a jump")
			assert.False(t, m.WantsClose(), "out-of-range click must not request close")
		})
	}
}
