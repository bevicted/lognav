package instancepicker

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/keyhintline"
	"github.com/bevicted/lognav/internal/ui/keys"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetKeybinds_CuratesStableInstanceHints keeps the hint row concise while
// preserving all instance-picker bindings in complete Help.
func TestGetKeybinds_CuratesStableInstanceHints(t *testing.T) {
	t.Parallel()
	m := New(t.Context(), depstest.NewTest(t))

	wantLabels := []string{"select", "fetch", "load", "all", "first", "watch", "cancel all", "save"}
	wantPriorities := []int{2, 3, 4, 5, 6, 7, 8, 9}
	assertInstanceHints(t, m, wantLabels, wantPriorities)

	m.fetching = true
	m.watching = true
	assertInstanceHints(t, m, wantLabels, wantPriorities)
}

// TestInstanceHintActionsRetainFetchWatchGuards proves mouse activation keeps
// using the existing guarded actions rather than a separate mouse-only path.
func TestInstanceHintActionsRetainFetchWatchGuards(t *testing.T) {
	t.Parallel()
	m := New(t.Context(), depstest.NewTest(t))
	busy := newTestInstance(t, "busy")
	busy.StartTimer()
	m.instances = Instances{busy}

	activateInstanceHint(t, m, "fetch")
	activateInstanceHint(t, m, "first")
	activateInstanceHint(t, m, "watch")

	assert.False(t, m.fetching, "guarded fetch must not start while a query is in progress")
	assert.False(t, m.watching, "guarded watch must not start while a query is in progress")
}

func activateInstanceHint(t *testing.T, m *Model, label string) {
	t.Helper()
	for _, hint := range keys.HintBindings(m.GetKeybinds()) {
		if hint.Label != label {
			continue
		}
		line := keyhintline.New(m.bundle)
		line.SetRect(component.Rect{W: 80, H: 1})
		line.SetBindings([]keys.Binding{hint})
		require.True(t, line.OnMouseClick(0, 0, uv.MouseLeft))
		return
	}
	t.Fatalf("hint %q not found", label)
}

func assertInstanceHints(t *testing.T, m *Model, wantLabels []string, wantPriorities []int) {
	t.Helper()
	hints := keys.HintBindings(m.GetKeybinds())
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
