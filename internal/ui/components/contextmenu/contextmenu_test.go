package contextmenu

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/component/componenttest"
	"github.com/bevicted/lognav/internal/ui/keys/keystest"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// closeRec stands in for the root's close seam, recording every call and the
// releaseFocus decision the menu made.
type closeRec struct {
	releases []bool
}

func (c *closeRec) onClose(releaseFocus bool) { c.releases = append(c.releases, releaseFocus) }
func (c *closeRec) closed() bool              { return len(c.releases) > 0 }

func newTestModel(t *testing.T, hits *[]string) (*Model, *closeRec) {
	t.Helper()
	bundle := depstest.NewTest(t)
	items := []msgs.ContextMenuItem{
		{Label: "copy field", Action: func() { *hits = append(*hits, "copy field") }},
		{Label: "copy log", Action: func() { *hits = append(*hits, "copy log") }},
		{Label: "toggle log mark", Action: func() { *hits = append(*hits, "toggle log mark") }},
	}
	m := New(bundle, items)
	m.SetPoster(&msgstest.FakePoster{})
	cr := &closeRec{}
	m.SetOnClose(cr.onClose)
	return m, cr
}

// TestCloseSeam_ReleaseFocusDecision pins who releases focus on close:
// suppression applies to an item activation only, never to a bare dismiss.
func TestCloseSeam_ReleaseFocusDecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		suppress    bool
		dismiss     bool // true = Esc dismiss, false = activate the focused item
		wantRelease bool
	}{
		{name: "activate/no suppress", wantRelease: true},
		{name: "activate/suppress", suppress: true, wantRelease: false},
		{name: "dismiss/no suppress", dismiss: true, wantRelease: true},
		{name: "dismiss/suppress always releases", suppress: true, dismiss: true, wantRelease: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hits []string
			m, cr := newTestModel(t, &hits)
			m.WithSuppressCloseRelease(tt.suppress)
			m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 8})

			if tt.dismiss {
				m.HandleKey(keystest.PressKeyUV(t, uv.KeyEscape))
			} else {
				m.HandleKey(keystest.PressKeyUV(t, uv.KeyEnter))
			}

			assert.Equal(t, []bool{tt.wantRelease}, cr.releases)
		})
	}
}

// TestClose_NilSeam_NoPanic verifies a menu closed before the root wired its
// seam is a silent no-op rather than a panic.
func TestClose_NilSeam_NoPanic(t *testing.T) {
	t.Parallel()
	m := New(depstest.NewTest(t), []msgs.ContextMenuItem{{Label: "x"}})
	m.SetPoster(&msgstest.FakePoster{})
	assert.NotPanics(t, m.Dismiss)
}

func TestNew_BuildsListParallelToItems(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	require.NotNil(t, m.list)
	assert.Len(t, m.items, 3)
	assert.Equal(t, 3, m.list.Len())
}

func TestPreferredSize_ContentFitAndShrinksUnderFilter(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 80, H: 24})

	const items = 3
	wFull, hFull := m.PreferredSize(80, 24)
	assert.Less(t, wFull, 80)
	assert.GreaterOrEqual(t, wFull, len("toggle log mark"))
	assert.Equal(t, fuzzyH+items+2*borderW, hFull)

	// Filter down to the single "toggle log mark" item; the height must shrink
	// to fuzzyH + 1 match + border.
	m.Focus()
	for _, r := range "toggle" {
		m.HandleKey(keystest.PressRuneUV(t, r))
	}
	require.Equal(t, 1, m.list.VisibleLen(), "fuzzy filter must narrow to one match")

	_, hFiltered := m.PreferredSize(80, 24)
	assert.Equal(t, fuzzyH+1+2*borderW, hFiltered)
	assert.Less(t, hFiltered, hFull, "height must shrink as fewer items match")
}

func TestHandleKey_Cancel_ClosesNoAction(t *testing.T) {
	t.Parallel()
	var hits []string
	bundle := depstest.NewTest(t)
	m := New(bundle, []msgs.ContextMenuItem{{Label: "copy log", Action: func() { hits = append(hits, "copy log") }}})
	m.SetPoster(&msgstest.FakePoster{})
	cr := &closeRec{}
	m.SetOnClose(cr.onClose)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 8})

	res := m.HandleKey(keystest.PressKeyUV(t, uv.KeyEscape))

	assert.Equal(t, component.KeyHandled, res)
	assert.True(t, cr.closed(), "Esc must call the host close seam")
	assert.Empty(t, hits, "Esc must not run any item action")
}

func TestActivate_Enter_RunsFocusedActionAndCloses(t *testing.T) {
	t.Parallel()
	var hits []string
	m, cr := newTestModel(t, &hits)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 8})

	m.HandleKey(keystest.PressKeyUV(t, uv.KeyEnter))

	assert.Equal(t, []string{"copy field"}, hits)
	assert.True(t, cr.closed())
}

func TestOnMouseClick_ItemRow_SingleClickActivates(t *testing.T) {
	t.Parallel()
	var hits []string
	m, cr := newTestModel(t, &hits)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 8})

	consumed := m.OnMouseClick(5, 3, uv.MouseLeft)

	assert.True(t, consumed)
	assert.Equal(t, []string{"copy field"}, hits)
	assert.True(t, cr.closed())
}

func TestOnMouseClick_FuzzyBarRow_FocusesNoActivate(t *testing.T) {
	t.Parallel()
	var hits []string
	m, cr := newTestModel(t, &hits)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 8})

	consumed := m.OnMouseClick(5, 1, uv.MouseLeft)

	assert.True(t, consumed)
	assert.Empty(t, hits, "clicking the fuzzy bar must not activate an item")
	assert.False(t, cr.closed(), "clicking the fuzzy bar must not close the menu")
}

func TestOnMouseClick_BelowLastItem_ConsumedNoOp(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	m.SetRect(component.Rect{X: 0, Y: 0, W: 40, H: 12})

	consumed := m.OnMouseClick(5, 9, uv.MouseLeft)

	assert.True(t, consumed)
	assert.Empty(t, hits)
}

func TestDraw_NoPanicAcrossRectSizes(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	componenttest.DrawMatrix(t, m.SetRect, m.Draw)
}

func TestPosition_NotAnchored_Centers(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	outer := component.Rect{X: 0, Y: 0, W: 80, H: 24}

	got := m.Position(outer, 20, 6)

	assert.Equal(t, component.CenterRect(outer, 20, 6), got,
		"without an anchor the menu must stay centered")
}

func TestPosition_Anchored_TopLeftAtAnchor(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	m.WithAnchor(10, 5)
	outer := component.Rect{X: 0, Y: 0, W: 80, H: 24}

	got := m.Position(outer, 20, 6)

	assert.Equal(t, component.Rect{X: 10, Y: 5, W: 20, H: 6}, got,
		"anchored menu's top-left corner must sit at the anchor")
}

func TestPosition_OverflowRight_ShiftsLeft(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	m.WithAnchor(70, 5)
	outer := component.Rect{X: 0, Y: 0, W: 80, H: 24}

	got := m.Position(outer, 20, 6)

	// 70+20=90 > 80 → shift left so the right edge lands at the screen edge.
	assert.Equal(t, 60, got.X, "overflow right must shift the box left to fit")
	assert.Equal(t, 5, got.Y, "vertical anchor is unaffected by a horizontal shift")
}

func TestPosition_OverflowBottom_FlipsAbove(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	// AnchorY is the row just BELOW the selected line; on overflow the menu flips
	// to sit ABOVE the line (y = anchorY - 1 - h).
	m.WithAnchor(10, 22)
	outer := component.Rect{X: 0, Y: 0, W: 80, H: 24}

	got := m.Position(outer, 20, 6)

	// 22+6=28 > 24 → flip: y = 22 - 1 - 6 = 15.
	assert.Equal(t, 15, got.Y, "overflow bottom must flip the menu above the line")
	assert.Equal(t, 10, got.X)
}

func TestPosition_FlipUnderflow_ClampsToTop(t *testing.T) {
	t.Parallel()
	var hits []string
	m, _ := newTestModel(t, &hits)
	m.WithAnchor(10, 2)
	outer := component.Rect{X: 0, Y: 0, W: 80, H: 9}

	got := m.Position(outer, 20, 8)

	// 2+8=10 > 9 → flip y = 2-1-8 = -7 → clamp to the top edge.
	assert.Equal(t, 0, got.Y, "a flip that underflows the top must clamp to outer.Y")
}
