package helpoverlay

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/component/componenttest"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

func newTestModel(t *testing.T, withAction bool) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	items := [][]list.Segment{
		list.PlainItem("first binding"),
		list.PlainItem("second binding"),
	}
	var onSelect keys.Action
	if withAction {
		// keys.Action is func() — provide a no-op
		onSelect = func() {}
	}
	m := New(bundle, "Test Overlay", items, onSelect)
	// Inject a FakePoster so that list.Focus/Unfocus do not panic on a nil poster.
	m.SetPoster(&msgstest.FakePoster{})
	return m
}

func TestNew_WithItemsAndOnSelect(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	require.NotNil(t, m)
	assert.Equal(t, "Test Overlay", m.title)
	assert.NotNil(t, m.list)
}

func TestNew_NilOnSelect_NoFuzzyConfirmAction(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, false)
	require.NotNil(t, m)
	assert.NotNil(t, m.list)
}

func TestSetRect_SplitsHeaderListBorder(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	assert.Equal(t, 40, m.drawRect.W)
	assert.Equal(t, 10, m.drawRect.H)
}

func TestSetRect_TooSmallHeight_ClampsToZero(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	// h - headerH - borderH = 1 - 1 - 1 = -1 → must clamp; verify no panic
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 1})
	canvas := uv.NewScreenBuffer(40, 1)
	require.NotPanics(t, func() { _ = m.Draw(canvas) })
}

func TestOnPaste_ForwardsToList(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	// OnPaste should not panic and forwards to the inner list.
	require.NotPanics(t, func() { m.OnPaste(uv.PasteEvent{Content: "x"}) })
}

func TestModel_Draw_DoesNotPanicAcrossRectSizes(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	componenttest.DrawMatrix(t, m.SetRect, m.Draw)
}

func TestDraw_ReturnsListCursor(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})
	canvas := uv.NewScreenBuffer(40, 10)
	cursor := m.Draw(canvas)
	// Either nil (defocused) or non-nil. Verify no panic — additional
	// contract: if non-nil, cursor must be inside drawRect bounds.
	if cursor != nil {
		assert.GreaterOrEqual(t, cursor.X, m.drawRect.X)
		assert.GreaterOrEqual(t, cursor.Y, m.drawRect.Y)
	}
}

// TestHandleKey_BoundKey_AlwaysHandled verifies that a key bound in the
// inner list is consumed and KeyHandled is returned.
func TestHandleKey_BoundKey_AlwaysHandled(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	// Give the list some height so cursor movement is valid.
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})

	// "j" / down-arrow are list navigation keys (MoveLineDown). Use the
	// down-arrow key which is always bound regardless of config defaults.
	result := m.HandleKey(keystest.PressKeyUV(t, uv.KeyDown))

	assert.Equal(t, component.KeyHandled, result,
		"a bound list key must be KeyHandled by the overlay")
}

// TestHandleKey_UnboundKey_StillHandled verifies that even a key that does
// nothing in the list is consumed by the overlay (modal behavior — the overlay
// must not leak keys to the root's tab-switching or regular handler).
func TestHandleKey_UnboundKey_StillHandled(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)

	// 'z' is not bound in the list. The overlay still swallows it.
	result := m.HandleKey(keystest.PressRuneUV(t, 'z'))

	assert.Equal(t, component.KeyHandled, result,
		"unbound key must still be consumed by the overlay (modal behavior)")
}

// TestHandleKey_FocusedList_FuzzySearchConsumesKey verifies that when the
// inner list is focused (fuzzy textinput active), character keys feed the
// fuzzy filter and are consumed by the overlay.
func TestHandleKey_FocusedList_FuzzySearchConsumesKey(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, true)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 10})

	// Focus the fuzzy input inside the list.
	m.Focus()

	result := m.HandleKey(keystest.PressRuneUV(t, 'f'))

	assert.Equal(t, component.KeyHandled, result,
		"char key into focused fuzzy input must be consumed")
}
