package logviewer

import (
	"testing"

	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetKeybinds_CuratesLogAndTimelineHints verifies the active log subview
// owns the exact hint sequence, including the logviewer's snapshot action.
func TestGetKeybinds_CuratesLogAndTimelineHints(t *testing.T) {
	t.Parallel()
	m, poster := newViewerModel(t, 2)

	assertLogHints(t, m.GetKeybinds(), []string{"expand", "all", "search", "jq", "filter", "timeline", "save"}, []int{2, 3, 4, 5, 6, 7, 8})

	openTimeline := func() {
		m.timeline.SetRect(m.drawRect)
		m.timeline.Open(m.store, 1000, true)
		m.timelineActive = true
	}
	openTimeline()
	bindings := m.GetKeybinds()
	assertLogHints(t, bindings, []string{"jump", "close", "save"}, []int{2, 3, 4})

	var jump, cancel, closeBinding keys.Binding
	for _, binding := range bindings {
		switch binding.Context {
		case "jump to selected bucket":
			jump = binding
		case "close timeline":
			if binding.Priority == 0 {
				cancel = binding
			} else {
				closeBinding = binding
			}
		}
	}
	assert.Equal(t, []string(m.bundle.Config.Keys.Cancel), cancel.Keys, "esc remains in complete Help")
	assert.Equal(t, []string(m.bundle.Config.Keys.Timeline), closeBinding.Keys, "configured Timeline key owns the close hint")
	assert.Equal(t, "close", closeBinding.Label)

	poster.Posted = nil
	for _, hint := range keys.HintBindings(bindings) {
		if hint.Label == "save" {
			hint.Action()
		}
	}
	require.Len(t, poster.Posted, 1)
	assert.IsType(t, msgs.ManualSaveSnapshotMsg{}, poster.Posted[0], "timeline save uses the existing logviewer snapshot action")

	m.cursor.log = 1
	jump.Action()
	assert.False(t, m.timelineActive, "jump hint closes the timeline")
	assert.Equal(t, 0, m.cursor.log, "jump hint centers the selected bucket's first log")
	assertLogHints(t, m.GetKeybinds(), []string{"expand", "all", "search", "jq", "filter", "timeline", "save"}, []int{2, 3, 4, 5, 6, 7, 8})

	openTimeline()
	m.cursor.log = 1
	closeBinding.Action()
	assert.False(t, m.timelineActive, "configured close binding closes the timeline")
	assert.Equal(t, 1, m.cursor.log, "configured close binding does not jump")
	assertLogHints(t, m.GetKeybinds(), []string{"expand", "all", "search", "jq", "filter", "timeline", "save"}, []int{2, 3, 4, 5, 6, 7, 8})

	openTimeline()
	m.cursor.log = 1
	cancel.Action()
	assert.False(t, m.timelineActive, "Cancel binding closes the timeline")
	assert.Equal(t, 1, m.cursor.log, "Cancel binding does not jump")
	assertLogHints(t, m.GetKeybinds(), []string{"expand", "all", "search", "jq", "filter", "timeline", "save"}, []int{2, 3, 4, 5, 6, 7, 8})

	openTimeline()
	m.HandleKey(keystest.PressRuneUV(t, 't'))
	assert.False(t, m.timelineActive, "configured Timeline key closes the timeline")
	assertLogHints(t, m.GetKeybinds(), []string{"expand", "all", "search", "jq", "filter", "timeline", "save"}, []int{2, 3, 4, 5, 6, 7, 8})
}

func assertLogHints(t *testing.T, bindings []keys.Binding, wantLabels []string, wantPriorities []int) {
	t.Helper()
	hints := keys.HintBindings(bindings)
	require.Len(t, hints, len(wantLabels))
	labels := make([]string, len(hints))
	priorities := make([]int, len(hints))
	for i, hint := range hints {
		labels[i] = hint.Label
		priorities[i] = hint.Priority
	}
	assert.Equal(t, wantLabels, labels)
	assert.Equal(t, wantPriorities, priorities)
}
