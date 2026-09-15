package ui

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/dialog"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// spyOverlay is a minimal component.Component + component.MouseTarget used to
// assert that an inside-overlay click is forwarded to the overlay's MouseTarget
// (and the overlay is NOT dismissed). It records the last click it received.
type spyOverlay struct {
	rect      component.Rect
	clicked   bool
	clickX    int
	clickY    int
	clickBtn  uv.MouseButton
	clickHits int
}

func (s *spyOverlay) Init()                                   {}
func (s *spyOverlay) SetRect(r component.Rect)                { s.rect = r }
func (s *spyOverlay) Draw(component.Screen) *component.Cursor { return nil }
func (s *spyOverlay) GetKeybinds() []keys.Binding             { return nil }

func (s *spyOverlay) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	s.clicked = true
	s.clickX, s.clickY, s.clickBtn = x, y, btn
	s.clickHits++
	return true
}

// plainOverlay is a minimal component.Component that deliberately does NOT
// implement component.MouseTarget. It exercises the swallow path: an inside
// click on a non-MouseTarget overlay must be consumed (no dismiss, no forward).
type plainOverlay struct{ rect component.Rect }

func (p *plainOverlay) Init()                                   {}
func (p *plainOverlay) SetRect(r component.Rect)                { p.rect = r }
func (p *plainOverlay) Draw(component.Screen) *component.Cursor { return nil }
func (p *plainOverlay) GetKeybinds() []keys.Binding             { return nil }

// focusableTab is a minimal component.Component + component.FocusReleaser used
// as a stand-in tab component to assert that switching tabs via a bar-row click
// while an input is focused releases the leaving component's focus.
type focusableTab struct{ released int }

func (f *focusableTab) Init()                                   {}
func (f *focusableTab) SetRect(component.Rect)                  {}
func (f *focusableTab) Draw(component.Screen) *component.Cursor { return nil }
func (f *focusableTab) GetKeybinds() []keys.Binding             { return nil }
func (f *focusableTab) ReleaseFocus()                           { f.released++ }

// TestModel_OnMouseClick_TabSwitchReleasesFocus verifies that clicking another
// tab on the bar row while an input is focused blurs the leaving component
// (FocusReleaser.ReleaseFocus) and drops the model's focus gate — otherwise the
// gate stays set and the new tab's keybinds are swallowed.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseClick_TabSwitchReleasesFocus(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &focusableTab{}
	m.tabs.ReplaceComponent(0, spy) // query tab now the focusable spy
	require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(), "setup: query tab active")

	// Simulate an input on the query tab holding focus.
	m.isFocusTaken = true

	// x=15 lands inside the instances chunk on the bar row (y=0).
	clickLeft(m, 15, 0)

	assert.Equal(t, "instances", m.tabs.GetActiveTab().GetTitle(),
		"bar-row click must switch tabs")
	assert.Equal(t, 1, spy.released,
		"switching tabs while focused must release the leaving component's focus")
	assert.False(t, m.isFocusTaken,
		"switching tabs while focused must drop the focus gate")
}

// clickLeft drives a left mouse click at (x, y) through the model's OnMouse
// entry, constructing the event the same way the runtime does.
func clickLeft(m *Model, x, y int) {
	m.OnMouse(uv.MouseClickEvent{X: x, Y: y, Button: uv.MouseLeft})
}

// rightClickSpy is a minimal component.Component + component.SecondaryMouseTarget
// used as a stand-in active tab component to assert that right-clicks are
// forwarded to the active component's OnMouseRight.
type rightClickSpy struct {
	hits         int
	lastX, lastY int
}

func (s *rightClickSpy) Init()                                   {}
func (s *rightClickSpy) SetRect(component.Rect)                  {}
func (s *rightClickSpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *rightClickSpy) GetKeybinds() []keys.Binding             { return nil }
func (s *rightClickSpy) OnMouseRight(x, y int) bool {
	s.hits++
	s.lastX, s.lastY = x, y
	return true
}

// TestModel_OnMouse_RightClick_ForwardsToActiveComponent verifies a right-click
// in the content area forwards to the active component's OnMouseRight (the
// SecondaryMouseTarget seam) without switching tabs.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_RightClick_ForwardsToActiveComponent(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &rightClickSpy{}
	m.tabs.ReplaceComponent(0, spy) // active tab (query) -> spy

	m.OnMouse(uv.MouseClickEvent{X: 5, Y: 5, Button: uv.MouseRight})

	assert.Equal(t, 1, spy.hits, "right-click must forward to the active component's OnMouseRight")
	assert.Equal(t, 5, spy.lastX, "forwarded X must match")
	assert.Equal(t, 5, spy.lastY, "forwarded Y must match")
	assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(), "right-click must not switch tabs")
}

// TestModel_OnMouse_RightClick_FooterIgnored verifies a right-click on the
// footer/statusline row is dropped (not forwarded).
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_RightClick_FooterIgnored(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &rightClickSpy{}
	m.tabs.ReplaceComponent(0, spy)

	m.OnMouse(uv.MouseClickEvent{X: 5, Y: m.screenH - 1, Button: uv.MouseRight})

	assert.Equal(t, 0, spy.hits, "right-click on the footer row must not be forwarded")
}

// TestModel_OnMouse_RightClick_OverlayIgnored verifies a right-click while an
// overlay is present is dropped (right-click is not an overlay dismiss/forward
// affordance in v1).
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_RightClick_OverlayIgnored(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &rightClickSpy{}
	m.tabs.ReplaceComponent(0, spy)
	m.overlay = &plainOverlay{}

	m.OnMouse(uv.MouseClickEvent{X: 5, Y: 5, Button: uv.MouseRight})

	assert.Equal(t, 0, spy.hits, "right-click must not reach a tab component while an overlay is present")
}

// TestModel_OnMouseClick_TabBar verifies tab-bar clicks, footer clicks, and
// non-left buttons over the bar, with no overlay present.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseClick_TabBar(t *testing.T) {
	t.Run("bar row click switches to a non-active tab", func(t *testing.T) {
		m := newLeftTabsModel(t)
		require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"setup: active tab is query")

		// With left-anchored spans at width 80 the instances chunk is
		// [10, 23); x=15 lands inside it on the bar row (y=0).
		clickLeft(m, 15, 0)
		assert.Equal(t, "instances", m.tabs.GetActiveTab().GetTitle(),
			"clicking the instances chunk on the bar row must switch tabs")
	})

	t.Run("footer/statusline row click is ignored", func(t *testing.T) {
		m := newLeftTabsModel(t)
		require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"setup: active tab is query")

		assert.NotPanics(t, func() { clickLeft(m, 15, m.screenH-1) })
		assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"a click on the footer row must not change the active tab")
	})

	t.Run("non-left button over a tab is ignored", func(t *testing.T) {
		m := newLeftTabsModel(t)
		require.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"setup: active tab is query")

		m.OnMouse(uv.MouseClickEvent{X: 15, Y: 0, Button: uv.MouseRight})
		assert.Equal(t, "query", m.tabs.GetActiveTab().GetTitle(),
			"a right-click over a tab must not switch tabs")
	})
}

// pasteSpy is a minimal component.Component + component.MousePasteTarget used as
// a stand-in active tab component (or overlay) to assert middle-click paste
// routing delivers the clipboard content to the right place.
type pasteSpy struct {
	rect         component.Rect
	hits         int
	lastX, lastY int
	lastContent  string
}

func (s *pasteSpy) Init()                                   {}
func (s *pasteSpy) SetRect(r component.Rect)                { s.rect = r }
func (s *pasteSpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *pasteSpy) GetKeybinds() []keys.Binding             { return nil }
func (s *pasteSpy) OnMousePaste(x, y int, content string) bool {
	s.hits++
	s.lastX, s.lastY, s.lastContent = x, y, content
	return true
}

// setClipboard swaps the package clipboard reader for the test and restores it
// on cleanup, so paste routing can be exercised without touching the real OS
// clipboard.
func setClipboard(t *testing.T, val string, err error) {
	t.Helper()
	prev := clipboardRead
	clipboardRead = func() (string, error) { return val, err }
	t.Cleanup(func() { clipboardRead = prev })
}

// TestModel_OnMouse_MiddleClick_PastesToActiveComponent verifies a middle-click
// reads the clipboard and forwards the content to the active component's
// OnMousePaste at the clicked coordinates.
//
//nolint:paralleltest // mutates package-level clipboardRead + keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_MiddleClick_PastesToActiveComponent(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &pasteSpy{}
	m.tabs.ReplaceComponent(0, spy)
	setClipboard(t, "clip", nil)

	m.OnMouse(uv.MouseClickEvent{X: 7, Y: 6, Button: uv.MouseMiddle})

	assert.Equal(t, 1, spy.hits, "middle-click must forward to the active component's OnMousePaste")
	assert.Equal(t, "clip", spy.lastContent, "the clipboard content must be handed down")
	assert.Equal(t, 7, spy.lastX, "forwarded X must match")
	assert.Equal(t, 6, spy.lastY, "forwarded Y must match")
}

// TestModel_OnMouse_MiddleClick_EmptyClipboard_NoForward verifies that an empty
// (or unreadable) clipboard short-circuits before any component is touched.
//
//nolint:paralleltest // mutates package-level clipboardRead + keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_MiddleClick_EmptyClipboard_NoForward(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &pasteSpy{}
	m.tabs.ReplaceComponent(0, spy)
	setClipboard(t, "", nil)

	m.OnMouse(uv.MouseClickEvent{X: 7, Y: 6, Button: uv.MouseMiddle})

	assert.Equal(t, 0, spy.hits, "an empty clipboard must not forward a paste")
}

// TestModel_OnMouse_MiddleClick_FooterIgnored verifies a middle-click on the
// footer row is dropped.
//
//nolint:paralleltest // mutates package-level clipboardRead + keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_MiddleClick_FooterIgnored(t *testing.T) {
	m := newLeftTabsModel(t)
	spy := &pasteSpy{}
	m.tabs.ReplaceComponent(0, spy)
	setClipboard(t, "clip", nil)

	m.OnMouse(uv.MouseClickEvent{X: 7, Y: m.screenH - 1, Button: uv.MouseMiddle})

	assert.Equal(t, 0, spy.hits, "middle-click on the footer row must not forward a paste")
}

// TestModel_OnMouse_MiddleClick_Overlay verifies overlay precedence: a
// middle-click inside the overlay forwards to its OnMousePaste; a click outside
// is dropped without dismissing the overlay (unlike a left click).
//
//nolint:paralleltest // mutates package-level clipboardRead + keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_MiddleClick_Overlay(t *testing.T) {
	t.Run("inside-overlay middle-click pastes into the overlay", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m, err := New(t.Context(), bundle)
		require.NoError(t, err)
		t.Cleanup(func() { _ = m.Close() })
		m.SetPoster(&msgstest.FakePoster{})
		m.OnResize(80, 24)
		setClipboard(t, "clip", nil)

		spy := &pasteSpy{}
		m.overlay = spy
		rect, ok := m.overlayRect()
		require.True(t, ok, "overlay must have a rect")
		require.True(t, inRect(10, 5, rect), "setup: (10,5) inside the overlay rect")

		m.OnMouse(uv.MouseClickEvent{X: 10, Y: 5, Button: uv.MouseMiddle})

		assert.Equal(t, 1, spy.hits, "inside middle-click must paste into the overlay")
		assert.Equal(t, "clip", spy.lastContent)
		assert.Same(t, component.Component(spy), m.overlay, "overlay must not be dismissed")
	})

	t.Run("outside-overlay middle-click is dropped without dismiss", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m, err := New(t.Context(), bundle)
		require.NoError(t, err)
		t.Cleanup(func() { _ = m.Close() })
		fp := &msgstest.FakePoster{}
		m.SetPoster(fp)
		m.OnResize(80, 24)
		setClipboard(t, "clip", nil)

		// A centered dialog (Sizer) leaves the corners outside its rect — unlike a
		// full-area non-Sizer overlay — so (0,0) is a genuine outside click.
		// Open through the real Show path so the root wires the close seam.
		m.Update(msgs.ShowDialogMsg{
			Title:   "Confirm",
			Message: "Are you sure?",
			Buttons: []msgs.DialogButton{{Label: "OK"}, {Label: "Cancel"}},
		})
		dlg, ok := m.overlay.(*dialog.Model)
		require.True(t, ok, "setup: overlay must be the dialog")
		rect, ok := m.overlayRect()
		require.True(t, ok, "overlay must have a rect")
		require.False(t, inRect(0, 0, rect), "setup: (0,0) outside the dialog rect")

		m.OnMouse(uv.MouseClickEvent{X: 0, Y: 0, Button: uv.MouseMiddle})

		assert.Same(t, component.Component(dlg), m.overlay,
			"an outside middle-click must NOT dismiss the overlay")
	})
}

// TestModel_OnMouseClick_Overlay verifies overlay precedence: an outside click
// dismisses a dialog (clearing the overlay slot and running OnCancel), while an
// inside click forwards to the overlay's MouseTarget without dismissing.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseClick_Overlay(t *testing.T) {
	t.Run("outside-overlay click dismisses a dialog", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m, err := New(t.Context(), bundle)
		require.NoError(t, err)
		t.Cleanup(func() { _ = m.Close() })
		fp := &msgstest.FakePoster{}
		m.SetPoster(fp)
		m.OnResize(80, 24)

		cancelled := false
		// Open through the real Show path so the root wires the close seam.
		m.Update(msgs.ShowDialogMsg{
			Title:    "Confirm",
			Message:  "Are you sure?",
			Buttons:  []msgs.DialogButton{{Label: "OK"}, {Label: "Cancel"}},
			OnCancel: func() { cancelled = true },
		})
		require.NotNil(t, m.overlay, "setup: the dialog must be open")

		rect, ok := m.overlayRect()
		require.True(t, ok, "overlay must have a rect")
		require.Positive(t, rect.W, "overlay rect must be non-empty")

		// (0,0) is the top-left corner, which the centered dialog never occupies
		// at this size — gate the precondition before driving the click.
		require.False(t, inRect(0, 0, rect), "setup: (0,0) must be outside the dialog rect")
		clickLeft(m, 0, 0)

		assert.True(t, cancelled, "dismissing the dialog must run its OnCancel")
		assert.Nil(t, m.overlay, "outside-overlay click must clear the overlay slot")
	})

	t.Run("inside-overlay click forwards to the MouseTarget and does not dismiss", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m, err := New(t.Context(), bundle)
		require.NoError(t, err)
		t.Cleanup(func() { _ = m.Close() })
		fp := &msgstest.FakePoster{}
		m.SetPoster(fp)
		m.OnResize(80, 24)

		spy := &spyOverlay{}
		m.overlay = spy
		// Non-Sizer overlay → overlayRect returns the full content area.
		rect, ok := m.overlayRect()
		require.True(t, ok, "overlay must have a rect")
		require.True(t, inRect(10, 5, rect), "setup: (10,5) must be inside the overlay rect")

		clickLeft(m, 10, 5)

		assert.True(t, spy.clicked, "inside click must be forwarded to the overlay MouseTarget")
		assert.Equal(t, 10, spy.clickX, "forwarded click X must match")
		assert.Equal(t, 5, spy.clickY, "forwarded click Y must match")
		assert.Equal(t, uv.MouseLeft, spy.clickBtn, "forwarded button must be left")
		assert.Same(t, component.Component(spy), m.overlay,
			"the overlay must NOT be dismissed on an inside click")
	})

	t.Run("inside click on a non-MouseTarget overlay is swallowed without dismiss", func(t *testing.T) {
		bundle := depstest.NewTest(t)
		m, err := New(t.Context(), bundle)
		require.NoError(t, err)
		t.Cleanup(func() { _ = m.Close() })
		fp := &msgstest.FakePoster{}
		m.SetPoster(fp)
		m.OnResize(80, 24)

		plain := &plainOverlay{}
		m.overlay = plain
		m.assignOverlayRect()
		// Non-Sizer overlay → overlayRect returns the full content area.
		rect, ok := m.overlayRect()
		require.True(t, ok, "overlay must have a rect")
		require.True(t, inRect(10, 5, rect), "setup: (10,5) must be inside the overlay rect")

		clickLeft(m, 10, 5)

		assert.Same(t, component.Component(plain), m.overlay,
			"an inside click on a non-MouseTarget overlay must be swallowed (slot unchanged)")
	})
}

// scrollSpy is a component.Component implementing BOTH component.ScrollTarget and
// component.KeyTarget, used to assert vertical-wheel routing: it records the
// OnMouseScroll arguments and (when it declines the scroll) the synthetic arrow
// the root falls back to. consume controls the OnMouseScroll return value.
type scrollSpy struct {
	consume      bool
	scrollHits   int
	lastLines    int
	lastX, lastY int
	keyHits      int
	lastKeyCode  rune
}

func (s *scrollSpy) Init()                                   {}
func (s *scrollSpy) SetRect(component.Rect)                  {}
func (s *scrollSpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *scrollSpy) GetKeybinds() []keys.Binding             { return nil }
func (s *scrollSpy) OnMouseScroll(x, y, lines int) bool {
	s.scrollHits++
	s.lastX, s.lastY, s.lastLines = x, y, lines
	return s.consume
}

func (s *scrollSpy) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	s.keyHits++
	s.lastKeyCode = ev.Code
	return component.KeyHandled
}

// keyOnlySpy is a component.KeyTarget that is NOT a component.ScrollTarget, used
// to assert the synthetic-arrow fallback for a non-scrollable active component.
type keyOnlySpy struct {
	keyHits     int
	lastKeyCode rune
}

func (s *keyOnlySpy) Init()                                   {}
func (s *keyOnlySpy) SetRect(component.Rect)                  {}
func (s *keyOnlySpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *keyOnlySpy) GetKeybinds() []keys.Binding             { return nil }
func (s *keyOnlySpy) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	s.keyHits++
	s.lastKeyCode = ev.Code
	return component.KeyHandled
}

// wheel drives a wheel notch at (x, y) through the model's OnMouse entry.
func wheel(m *Model, x, y int, btn uv.MouseButton) {
	m.OnMouse(uv.MouseWheelEvent{X: x, Y: y, Button: btn})
}

// TestModel_OnMouseWheel_VerticalScrollsActiveComponent verifies a vertical wheel
// notch over a ScrollTarget active component pans it via OnMouseScroll (signed by
// direction, magnitude = Core.WheelScrollLines default 5) and does NOT fall back
// to a synthetic arrow.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseWheel_VerticalScrollsActiveComponent(t *testing.T) {
	t.Run("wheel down pans toward later content (+lines)", func(t *testing.T) {
		m := newLeftTabsModel(t)
		spy := &scrollSpy{consume: true}
		m.tabs.ReplaceComponent(0, spy)

		wheel(m, 5, 5, uv.MouseWheelDown)

		assert.Equal(t, 1, spy.scrollHits, "vertical wheel must reach the active component's OnMouseScroll")
		assert.Equal(t, 5, spy.lastLines, "wheel down must pan +WheelScrollLines (default 5)")
		assert.Equal(t, 5, spy.lastX, "forwarded X must match")
		assert.Equal(t, 5, spy.lastY, "forwarded Y must match")
		assert.Zero(t, spy.keyHits, "a consumed scroll must NOT fall back to a synthetic arrow")
	})

	t.Run("wheel up pans toward earlier content (-lines)", func(t *testing.T) {
		m := newLeftTabsModel(t)
		spy := &scrollSpy{consume: true}
		m.tabs.ReplaceComponent(0, spy)

		wheel(m, 5, 5, uv.MouseWheelUp)

		assert.Equal(t, 1, spy.scrollHits)
		assert.Equal(t, -5, spy.lastLines, "wheel up must pan -WheelScrollLines")
	})
}

// TestModel_OnMouseWheel_MagnitudeFromConfig verifies Core.WheelScrollLines sets
// the pan magnitude.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseWheel_MagnitudeFromConfig(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.WheelScrollLines = 7
	spy := &scrollSpy{consume: true}
	m.tabs.ReplaceComponent(0, spy)

	wheel(m, 5, 5, uv.MouseWheelDown)

	assert.Equal(t, 7, spy.lastLines, "pan magnitude must come from Core.WheelScrollLines")
}

// TestModel_OnMouseWheel_FallbackToArrow verifies the synthetic-arrow fallback
// when the active component does not scroll: a non-ScrollTarget component, and a
// ScrollTarget that declines (returns false).
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseWheel_FallbackToArrow(t *testing.T) {
	t.Run("non-ScrollTarget active component gets a synthetic Down arrow", func(t *testing.T) {
		m := newLeftTabsModel(t)
		spy := &keyOnlySpy{}
		m.tabs.ReplaceComponent(0, spy)

		wheel(m, 5, 5, uv.MouseWheelDown)

		assert.Equal(t, 1, spy.keyHits, "a non-scrollable component must receive the synthetic arrow")
		assert.Equal(t, uv.KeyDown, spy.lastKeyCode, "wheel down → KeyDown")
	})

	t.Run("declining ScrollTarget falls back to a synthetic arrow", func(t *testing.T) {
		m := newLeftTabsModel(t)
		spy := &scrollSpy{consume: false}
		m.tabs.ReplaceComponent(0, spy)

		wheel(m, 5, 5, uv.MouseWheelUp)

		assert.Equal(t, 1, spy.scrollHits, "the scroll seam is still offered the notch")
		assert.Equal(t, 1, spy.keyHits, "a declined scroll must fall back to a synthetic arrow")
		assert.Equal(t, uv.KeyUp, spy.lastKeyCode, "wheel up → KeyUp")
	})
}

// TestModel_OnMouseWheel_Horizontal verifies a horizontal wheel notch stays on
// the synthetic Left/Right arrow path and does NOT reach the scroll seam.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseWheel_Horizontal(t *testing.T) {
	m := newLeftTabsModel(t)
	m.wheel = axisLock{} // disable axis lock so a single sideways notch is not dropped
	spy := &scrollSpy{consume: true}
	m.tabs.ReplaceComponent(0, spy)

	wheel(m, 5, 5, uv.MouseWheelRight)

	assert.Zero(t, spy.scrollHits, "horizontal wheel must NOT use the vertical scroll seam")
	assert.Equal(t, 1, spy.keyHits, "horizontal wheel stays on the synthetic arrow path")
	assert.Equal(t, uv.KeyRight, spy.lastKeyCode, "wheel right → KeyRight")
}

// TestModel_OnMouseWheel_OverlayAndFooter verifies overlay/footer precedence: a
// ScrollTarget overlay swallows+scrolls the notch (active tab untouched); a
// non-ScrollTarget overlay swallows it silently; a notch on the footer row is
// swallowed.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseWheel_OverlayAndFooter(t *testing.T) {
	t.Run("ScrollTarget overlay scrolls, active tab untouched", func(t *testing.T) {
		m := newLeftTabsModel(t)
		tab := &scrollSpy{consume: true}
		m.tabs.ReplaceComponent(0, tab)
		ov := &scrollSpy{consume: true}
		m.overlay = ov

		wheel(m, 5, 5, uv.MouseWheelDown)

		assert.Equal(t, 1, ov.scrollHits, "an overlay ScrollTarget must receive the wheel")
		assert.Zero(t, tab.scrollHits, "the active tab must not scroll while an overlay is present")
	})

	t.Run("non-ScrollTarget overlay swallows the notch", func(t *testing.T) {
		m := newLeftTabsModel(t)
		tab := &scrollSpy{consume: true}
		m.tabs.ReplaceComponent(0, tab)
		m.overlay = &plainOverlay{}

		wheel(m, 5, 5, uv.MouseWheelDown)

		assert.Zero(t, tab.scrollHits, "an overlay must block the wheel from the active tab")
		assert.Zero(t, tab.keyHits, "an overlay must block the synthetic-arrow fallback too")
	})

	t.Run("footer row swallows the notch", func(t *testing.T) {
		m := newLeftTabsModel(t)
		tab := &scrollSpy{consume: true}
		m.tabs.ReplaceComponent(0, tab)

		wheel(m, 5, m.screenH-1, uv.MouseWheelDown)

		assert.Zero(t, tab.scrollHits, "a wheel on the footer row must be swallowed")
		assert.Zero(t, tab.keyHits, "a footer wheel must not fall back to an arrow")
	})
}

// newLeftTabsModel builds a sized model with left-anchored tab spans (so bar
// hit-test columns are deterministic) and a FakePoster wired in.
func newLeftTabsModel(t *testing.T) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	bundle.Config.Style.TablineAlign = 0 // left-anchored → deterministic spans
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	m.OnResize(80, 24)
	return m
}

// hoverSpy is a minimal component.Component + component.MouseHoverTarget used as
// a stand-in tab component or overlay to assert pointer-motion routing.
type hoverSpy struct {
	hits         int
	clears       int
	hovered      bool
	lastX, lastY int
}

func (s *hoverSpy) Init()                                   {}
func (s *hoverSpy) SetRect(component.Rect)                  {}
func (s *hoverSpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *hoverSpy) GetKeybinds() []keys.Binding             { return nil }
func (s *hoverSpy) OnMouseHover(x, y int) bool {
	s.hits++
	s.hovered = true
	s.lastX, s.lastY = x, y
	return true
}

func (s *hoverSpy) ClearMouseHover() bool {
	s.clears++
	if !s.hovered {
		return false
	}
	s.hovered = false
	return true
}

// hoverMove drives a mouse-motion event at (x, y) through the model's OnMouse
// entry, constructing the event the same way the runtime does, and returns
// OnMouse's redraw-needed result.
func hoverMove(m *Model, x, y int) bool {
	return m.OnMouse(uv.MouseMotionEvent{X: x, Y: y})
}

// hoverRetSpy is a hover target whose OnMouseHover return value is configurable,
// used to exercise the redraw gate (OnMouse returns the hover-changed result).
type hoverRetSpy struct {
	ret  bool
	hits int
}

func (s *hoverRetSpy) Init()                                   {}
func (s *hoverRetSpy) SetRect(component.Rect)                  {}
func (s *hoverRetSpy) Draw(component.Screen) *component.Cursor { return nil }
func (s *hoverRetSpy) GetKeybinds() []keys.Binding             { return nil }
func (s *hoverRetSpy) OnMouseHover(int, int) bool              { s.hits++; return s.ret }
func (*hoverRetSpy) ClearMouseHover() bool                     { return false }

// TestModel_OnMouse_Motion_GateOff_NotRouted verifies that with Core.EnableHover
// explicitly off a motion event is dropped — the active component's
// MouseHoverTarget is never called and no redraw is requested.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_Motion_GateOff_NotRouted(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.EnableHover = false
	spy := &hoverSpy{}
	m.tabs.ReplaceComponent(0, spy)

	redraw := hoverMove(m, 5, 5)

	assert.Zero(t, spy.hits, "motion must be dropped when EnableHover is off")
	assert.False(t, redraw, "a dropped motion must not request a redraw")
}

// TestModel_OnMouse_Motion_GatesRedrawOnHoverChange verifies OnMouse returns
// whether the hover changed: true (redraw) when the active component reports a
// change, false (no redraw) when it does not — so an idle pointer sweep within
// one row does not repaint.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_Motion_GatesRedrawOnHoverChange(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.EnableHover = true
	spy := &hoverRetSpy{ret: true}
	m.tabs.ReplaceComponent(0, spy)

	assert.True(t, hoverMove(m, 5, 5), "a hover that changed the row must request a redraw")
	spy.ret = false
	assert.False(t, hoverMove(m, 5, 5), "a hover that did not change must not request a redraw")
	assert.Equal(t, 2, spy.hits, "both motions are routed; only the gate (return) differs")
}

// TestModel_OnMouse_Motion_GateOn_ForwardsToActiveComponent verifies that with
// Core.EnableHover on a content-area motion event forwards to the active
// component's OnMouseHover with the absolute coordinates.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_Motion_GateOn_ForwardsToActiveComponent(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.EnableHover = true
	spy := &hoverSpy{}
	m.tabs.ReplaceComponent(0, spy)

	hoverMove(m, 7, 5)

	assert.Equal(t, 1, spy.hits, "a content motion must reach the active component")
	assert.Equal(t, 7, spy.lastX)
	assert.Equal(t, 5, spy.lastY)
}

// TestModel_OnMouse_Motion_OverlayPrecedence verifies a motion event over a
// present overlay forwards to the overlay's MouseHoverTarget (never the active
// tab) and never dismisses it.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_Motion_OverlayPrecedence(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.EnableHover = true
	tab := &hoverSpy{}
	m.tabs.ReplaceComponent(0, tab)
	ov := &hoverSpy{}
	m.overlay = ov

	hoverMove(m, 5, 5)

	assert.Equal(t, 1, ov.hits, "an overlay MouseHoverTarget must receive the motion")
	assert.Zero(t, tab.hits, "the active tab must not be hovered while an overlay is present")
	assert.Same(t, component.Component(ov), m.overlay, "motion must not dismiss the overlay")
}

// TestModel_OnMouse_Motion_FooterRow_Swallowed verifies a motion on the footer
// row is swallowed (no active-component hover).
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouse_Motion_FooterRow_Swallowed(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.EnableHover = true
	spy := &hoverSpy{}
	m.tabs.ReplaceComponent(0, spy)
	require.True(t, hoverMove(m, 5, 5), "setup: content hover is active")

	changed := hoverMove(m, 5, m.screenH-1)

	assert.Equal(t, 1, spy.hits, "a motion on the footer row must not add a hover hit")
	assert.True(t, changed, "clearing the retained content hover requests a redraw")
	assert.False(t, spy.hovered, "footer motion clears the retained content hover")
}

// TestModel_HandleKey_ClearsMouseHover verifies keyboard input switches visual
// modality before key routing, even when the key has no bound action.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_HandleKey_ClearsMouseHover(t *testing.T) {
	m := newLeftTabsModel(t)
	m.bundle.Config.Core.EnableHover = true
	spy := &hoverSpy{}
	m.tabs.ReplaceComponent(0, spy)
	require.True(t, hoverMove(m, 5, 5), "setup: content hover is active")

	m.HandleKey(uv.KeyPressEvent{Code: 'x', Text: "x"})

	assert.False(t, spy.hovered, "a key press clears pointer-derived highlight state")
	assert.Equal(t, 1, spy.clears, "the active hover target is cleared once")
}
