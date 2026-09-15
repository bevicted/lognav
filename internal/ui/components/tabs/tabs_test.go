package tabs

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/stretchr/testify/assert"
)

// stubComponent is a no-op component used only to populate Tab slots in tests.
type stubComponent struct{}

func (stubComponent) Init()                                   {}
func (stubComponent) SetRect(component.Rect)                  {}
func (stubComponent) Draw(component.Screen) *component.Cursor { return nil }
func (stubComponent) GetKeybinds() []keys.Binding             { return nil }

func newTestTabs(t *testing.T) *Model {
	t.Helper()
	return New(
		depstest.NewTest(t),
		Tab{Title: "a", Component: stubComponent{}},
		Tab{Title: "b", Component: stubComponent{}},
		Tab{Title: "c", Component: stubComponent{}},
	)
}

func TestOnLeaveFiresOnTabChange(t *testing.T) {
	t.Parallel()
	m := newTestTabs(t)

	var gotFrom, gotTo int
	var calls int
	m.WithOnLeave(func(from, to int) {
		gotFrom, gotTo = from, to
		calls++
	})

	// activeTab starts at 0. Select(2) should fire onLeave with (0, 2).
	m.Select(2)
	assert.Equal(t, 1, calls, "onLeave should fire once")
	assert.Equal(t, 0, gotFrom)
	assert.Equal(t, 2, gotTo)
}

func TestOnLeaveDoesNotFireOnSameTab(t *testing.T) {
	t.Parallel()
	m := newTestTabs(t)

	var calls int
	m.WithOnLeave(func(from, to int) { calls++ })

	// activeTab is 0. Select(0) is a no-op — onLeave should NOT fire.
	m.Select(0)
	assert.Equal(t, 0, calls, "onLeave must not fire when active tab does not change")
}

func TestOnLeaveFiresAfterClampToSameTab(t *testing.T) {
	t.Parallel()
	m := newTestTabs(t)

	var calls int
	m.WithOnLeave(func(from, to int) { calls++ })

	// activeTab is 0. Select(-1) clamps to 0. onLeave should NOT fire because
	// the active tab is unchanged after clamping.
	m.Select(-1)
	assert.Equal(t, 0, calls)

	// activeTab is 0. Select(99) clamps to 2. onLeave SHOULD fire.
	m.Select(99)
	assert.Equal(t, 1, calls)
}

func TestSelectWithoutOnLeaveIsSafe(t *testing.T) {
	t.Parallel()
	m := newTestTabs(t)
	// No onLeave registered — should not panic.
	m.Select(1)
	assert.Equal(t, 1, m.activeTab)
}
