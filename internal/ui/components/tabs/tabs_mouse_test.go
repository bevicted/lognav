package tabs

import (
	"testing"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hoverStub is a content component that records hover delegations.
type hoverStub struct {
	stubComponent
	hits         int
	lastX, lastY int
}

func (h *hoverStub) OnMouseHover(x, y int) bool {
	h.hits++
	h.lastX, h.lastY = x, y
	return true
}

func (*hoverStub) ClearMouseHover() bool { return false }

// tabs implements the hover seam.
var _ component.MouseHoverTarget = (*Model)(nil)

// TestTabs_OnMouseHover_BarRow_HighlightsTab verifies a hover on the bar row
// records the tab under the pointer, reports a change only on a tab move, and
// clears when the pointer leaves the strip.
func TestTabs_OnMouseHover_BarRow_HighlightsTab(t *testing.T) {
	t.Parallel()
	m := newMouseTabs(t, 0)

	changed := m.OnMouseHover(13, 0) // Logs chunk [10..17], y=0 bar row

	assert.True(t, changed, "hovering a new tab on the bar reports a change")
	assert.Equal(t, 1, m.hoverTab, "bar hover records the tab under the pointer")
	assert.False(t, m.OnMouseHover(14, 0), "re-hovering the same tab reports no change")
	assert.True(t, m.OnMouseHover(70, 0), "moving off the tab strip clears the highlight")
	assert.Equal(t, -1, m.hoverTab)
}

// TestTabs_OnMouseHover_ContentRow_ClearsTabAndDelegates verifies a hover in the
// content area clears the tab highlight and forwards to the active component.
func TestTabs_OnMouseHover_ContentRow_ClearsTabAndDelegates(t *testing.T) {
	t.Parallel()
	m := newMouseTabs(t, 0)
	stub := &hoverStub{}
	m.ReplaceComponent(0, stub) // active tab 0
	require.True(t, m.OnMouseHover(13, 0), "setup: hover a tab on the bar")
	require.Equal(t, 1, m.hoverTab)

	changed := m.OnMouseHover(5, 5) // content area

	assert.True(t, changed, "moving from bar to content clears the tab highlight (a change)")
	assert.Equal(t, -1, m.hoverTab, "content hover clears the tab highlight")
	assert.Equal(t, 1, stub.hits, "content hover delegates to the active component")
	assert.Equal(t, 5, stub.lastY, "delegated coordinates are forwarded")
}

// TestTabs_HoverTab_TintsBar verifies the hovered, non-active tab is tinted with
// the configured hover background while the active tab is not.
func TestTabs_HoverTab_TintsBar(t *testing.T) {
	t.Parallel()
	m := newMouseTabs(t, 0)
	require.True(t, m.bundle.Config.Style.HoverRowBg.IsSet(), "setup: hover bg has a default color")
	want := m.bundle.Config.Style.HoverRowBg.Color
	require.True(t, m.OnMouseHover(13, 0), "hover the non-active Logs tab")

	s := uv.NewScreenBuffer(80, 24)
	_ = m.Draw(s)

	assert.Equal(t, want, s.CellAt(13, 0).Style.Bg, "hovered non-active tab must be tinted")
	assert.NotEqual(t, want, s.CellAt(3, 0).Style.Bg, "the active tab must not be hover-tinted")
}

// newMouseTabs builds a left-anchored ("Query" | "Logs") tab model whose bar
// sits at absolute (barX, 0) with width 80, so spans are deterministic:
// barX=0  → Query [0..8],  sep 9,  Logs [10..17];
// barX=10 → Query [10..18], sep 19, Logs [20..27].
func newMouseTabs(t *testing.T, barX int) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	bundle.Config.Style.TablineAlign = 0 // left-anchored → deterministic spans
	m := New(bundle,
		Tab{Title: "Query", Component: stubComponent{}},
		Tab{Title: "Logs", Component: stubComponent{}},
	)
	m.SetRect(component.Rect{X: barX, Y: 0, W: 80, H: 24})
	return m
}

func TestTabs_TabAtX_MapsColumnToTab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		barX int
		x    int
		want int
	}{
		{name: "tab 0 chunk", barX: 0, x: 3, want: 0},
		{name: "tab 1 chunk", barX: 0, x: 13, want: 1},
		{name: "past the strip", barX: 0, x: 70, want: -1},
		{name: "on the separator", barX: 0, x: 9, want: -1},
		{name: "tab 0 chunk offset by barX", barX: 10, x: 13, want: 0},
		{name: "tab 1 chunk offset by barX", barX: 10, x: 23, want: 1},
		{name: "separator offset by barX", barX: 10, x: 19, want: -1},
		{name: "left of the strip", barX: 10, x: 5, want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMouseTabs(t, tt.barX)
			assert.Equal(t, tt.want, m.TabAtX(tt.x))
		})
	}
}

func TestTabs_OnMouseClick_BarRowSelectsTab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		barX         int
		x            int
		wantConsumed bool
		wantTitle    string
	}{
		{name: "bar row selects tab 1", barX: 0, x: 13, wantConsumed: true, wantTitle: "Logs"},
		{name: "bar row selects tab 1 offset by barX", barX: 10, x: 23, wantConsumed: true, wantTitle: "Logs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMouseTabs(t, tt.barX)
			consumed := m.OnMouseClick(tt.x, 0, uv.MouseButton(0), false) // bar row y=0
			assert.Equal(t, tt.wantConsumed, consumed)
			assert.Equal(t, tt.wantTitle, m.GetActiveTab().GetTitle())
		})
	}
}

// doubleClickStub is a content component implementing both MouseTarget and
// DoubleClickTarget; it records which method was called last.
type doubleClickStub struct {
	stubComponent
	singleHits int
	doubleHits int
	lastX      int
	lastY      int
}

func (d *doubleClickStub) OnMouseClick(x, y int, _ uv.MouseButton) bool {
	d.singleHits++
	d.lastX, d.lastY = x, y
	return true
}

func (d *doubleClickStub) OnMouseDoubleClick(x, y int, _ uv.MouseButton) bool {
	d.doubleHits++
	d.lastX, d.lastY = x, y
	return true
}

// singleOnlyStub implements only MouseTarget (not DoubleClickTarget), so a
// double-click with isDouble=true must fall back to OnMouseClick.
type singleOnlyStub struct {
	stubComponent
	hits int
}

func (s *singleOnlyStub) OnMouseClick(_, _ int, _ uv.MouseButton) bool {
	s.hits++
	return true
}

// TestTabs_OnMouseClick_ContentArea_DoubleClick verifies that:
//   - isDouble=false → OnMouseClick is called on the active component
//   - isDouble=true  → OnMouseDoubleClick is called when the component implements DoubleClickTarget
//   - isDouble=true  → falls back to OnMouseClick when the component is NOT a DoubleClickTarget
//   - bar-row click with isDouble=true still selects the tab (no double routing on the bar)
func TestTabs_OnMouseClick_ContentArea_DoubleClick(t *testing.T) {
	t.Parallel()

	t.Run("single click routes to OnMouseClick", func(t *testing.T) {
		t.Parallel()
		m := newMouseTabs(t, 0)
		stub := &doubleClickStub{}
		m.ReplaceComponent(0, stub)

		consumed := m.OnMouseClick(5, 5, uv.MouseLeft, false)

		assert.True(t, consumed, "single click must be consumed")
		assert.Equal(t, 1, stub.singleHits, "single click must call OnMouseClick")
		assert.Equal(t, 0, stub.doubleHits, "single click must NOT call OnMouseDoubleClick")
	})

	t.Run("double click routes to OnMouseDoubleClick when component implements DoubleClickTarget", func(t *testing.T) {
		t.Parallel()
		m := newMouseTabs(t, 0)
		stub := &doubleClickStub{}
		m.ReplaceComponent(0, stub)

		consumed := m.OnMouseClick(5, 5, uv.MouseLeft, true)

		assert.True(t, consumed, "double click must be consumed")
		assert.Equal(t, 0, stub.singleHits, "double click must NOT call OnMouseClick")
		assert.Equal(t, 1, stub.doubleHits, "double click must call OnMouseDoubleClick")
		assert.Equal(t, 5, stub.lastX, "x coordinate must be forwarded")
		assert.Equal(t, 5, stub.lastY, "y coordinate must be forwarded")
	})

	t.Run("double click falls back to OnMouseClick when component is not a DoubleClickTarget", func(t *testing.T) {
		t.Parallel()
		m := newMouseTabs(t, 0)
		stub := &singleOnlyStub{}
		m.ReplaceComponent(0, stub)

		consumed := m.OnMouseClick(5, 5, uv.MouseLeft, true)

		assert.True(t, consumed, "fallback single click must be consumed")
		assert.Equal(t, 1, stub.hits, "isDouble=true with non-DoubleClickTarget must fall back to OnMouseClick")
	})

	t.Run("bar row click with isDouble=true still selects the tab", func(t *testing.T) {
		t.Parallel()
		m := newMouseTabs(t, 0)
		stub := &doubleClickStub{}
		m.ReplaceComponent(1, stub)

		consumed := m.OnMouseClick(13, 0, uv.MouseLeft, true) // bar row y=0, Logs tab

		assert.True(t, consumed, "bar row double-click must be consumed")
		assert.Equal(t, "Logs", m.GetActiveTab().GetTitle(), "bar row double-click must select the tab")
		assert.Equal(t, 0, stub.singleHits, "bar row click must not forward to content component")
		assert.Equal(t, 0, stub.doubleHits, "bar row click must not forward to content component")
	})
}
