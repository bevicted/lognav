package timeline

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	m := New(depstest.NewTest(t))
	m.SetRect(component.Rect{X: 0, Y: 0, W: 80, H: 24})
	return m
}

// newPopulatedFakeStore returns a store whose log at index 0 has TSMicro=50
// in a time range [0, 100], so Build() produces buckets with one populated
// bucket and Open() can resolve the cursor via the passed-in timestamp.
func newPopulatedFakeStore() *fakeStore {
	return &fakeStore{
		earliest: 0,
		latest:   100,
		logs: []LogMeta{
			{Idx: 0, TSMicro: 50, Severity: icl.SeverityInfo},
		},
	}
}

func TestTimelineCloseOnCancel(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	m.Open(newPopulatedFakeStore(), 50, true)

	result := m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEsc})
	assert.Equal(t, component.KeyHandled, result)
	assert.True(t, m.WantsClose(), "WantsClose must be true after Cancel")
}

func TestTimelineHintBindings(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)

	hints := keys.HintBindings(m.GetKeybinds())
	require.Len(t, hints, 2)
	assert.Equal(t, []string{"jump", "close"}, []string{hints[0].Label, hints[1].Label})
	assert.Equal(t, []int{2, 3}, []int{hints[0].Priority, hints[1].Priority})
	assert.Equal(t, []string(m.bundle.Config.Keys.Timeline), hints[1].Keys, "only Timeline gets the close hint")

	var cancelFound bool
	for _, binding := range m.GetKeybinds() {
		if binding.Context == "close timeline" && binding.Keys[0] == m.bundle.Config.Keys.Cancel[0] {
			cancelFound = true
			assert.Zero(t, binding.Priority, "Cancel remains in complete Help only")
		}
	}
	assert.True(t, cancelFound, "Cancel remains a complete Help binding")
}

func TestTimelineJumpOnAccept(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	m.Open(newPopulatedFakeStore(), 50, true)

	result := m.HandleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	assert.Equal(t, component.KeyHandled, result)

	logIdx, logLine, ok := m.PendingJump()
	require.True(t, ok, "PendingJump must be set after Accept on a populated bucket")
	assert.Equal(t, 0, logIdx)
	assert.Equal(t, 0, logLine)
	assert.False(t, m.WantsClose(), "WantsClose must be false when a jump is pending")
}

// TestHandleKey_BoundKey_ReturnsHandled verifies that a key bound in the
// timeline's handler returns KeyHandled with a nil cmd (R5a A3: actions set
// accessor state instead of returning cmds).
func TestHandleKey_BoundKey_ReturnsHandled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ev         uv.KeyPressEvent
		wantResult component.KeyResult
	}{
		{
			name:       "cancel_esc_returns_handled",
			ev:         keystest.PressKeyUV(t, uv.KeyEsc),
			wantResult: component.KeyHandled,
		},
		{
			name:       "accept_enter_returns_handled",
			ev:         keystest.PressKeyUV(t, uv.KeyEnter),
			wantResult: component.KeyHandled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel(t)
			m.Open(newPopulatedFakeStore(), 50, true)
			result := m.HandleKey(tt.ev)
			assert.Equal(t, tt.wantResult, result)
		})
	}
}

// TestHandleKey_UnboundKey_ReturnsIgnored verifies that a key not bound in the
// timeline's handler returns KeyIgnored with a nil cmd.
func TestHandleKey_UnboundKey_ReturnsIgnored(t *testing.T) {
	t.Parallel()
	m := newTestModel(t)
	m.Open(newPopulatedFakeStore(), 50, true)

	// 'z' is not bound in the timeline key handler.
	result := m.HandleKey(keystest.PressRuneUV(t, 'z'))
	assert.Equal(t, component.KeyIgnored, result)
}
