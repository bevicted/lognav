package tabs

import (
	"log/slog"

	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/logging"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/numeric"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"
)

type Tab struct {
	Title     string
	Component component.Component
	TitleFunc func() string
}

func (t Tab) GetTitle() string {
	if t.TitleFunc != nil {
		return t.TitleFunc()
	}
	return t.Title
}

// Compile-time seam opt-ins: the authoritative list of the optional
// component seams *Model implements. Every implemented seam belongs here — a
// seam rename or signature change would otherwise silently demote its handler
// to an ordinary method and drop that input with no build error. tabs is
// deliberately NOT a MouseTarget/DoubleClickTarget: its OnMouseClick takes the
// extra isDouble argument and is called directly by the root, not through the
// seam.
var (
	_ component.KeyTarget        = (*Model)(nil)
	_ component.MouseHoverTarget = (*Model)(nil)
)

type Model struct {
	bundle        deps.Bundle
	drawRect      component.Rect
	tabs          []Tab
	activeTab     int
	prevActiveTab int
	hoverTab      int // bar index of the tab under the mouse pointer, -1 = none
	onLeave       func(from, to int)
	logger        *slog.Logger
}

// New constructs a tabs model. Rendering is sized entirely via SetRect
// propagated from the root ui.Model.
func New(bundle deps.Bundle, tabs ...Tab) *Model {
	return &Model{
		bundle:   bundle,
		tabs:     tabs,
		hoverTab: -1,
		logger:   slog.Default().With(logging.KeyComponent, "tabs"),
	}
}

func (m *Model) Init() {
	for _, tab := range m.tabs {
		tab.Component.Init()
	}
}

// SetRect stores the assigned rect, splits it into a 1-row bar at the top and
// a content area below, and propagates the content rect to every sub-component
// so tab switching does not require a resize lag.
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
	contentR := component.Rect{X: r.X, Y: r.Y + 1, W: r.W, H: r.H - 1}
	if r.H < 2 {
		// Degenerate: bar takes everything, no content room.
		contentR = component.Rect{X: r.X, Y: r.Y, W: r.W, H: 0}
	}
	for i := range m.tabs {
		m.tabs[i].Component.SetRect(contentR)
	}
}

// Draw places the tab bar cells at the top of drawRect and forwards rendering
// to the active sub-component (which already received its content rect via
// SetRect). Returns the active component's cursor (already in absolute coords).
func (m *Model) Draw(s component.Screen) *component.Cursor {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return nil
	}
	barR := component.Rect{X: m.drawRect.X, Y: m.drawRect.Y, W: m.drawRect.W, H: 1}
	m.placeBar(s, barR)
	if m.drawRect.H < 2 {
		return nil
	}
	return m.GetActiveComponent().Draw(s)
}

// tabSpan is the absolute column range [start, end) a tab title chunk occupies
// on the bar row (excluding separators), paired with its tab index.
type tabSpan struct {
	idx        int
	start, end int
}

// tabSpans computes the absolute column span of every tab chunk for a bar at
// absolute X = barX and width w, honoring Style.TablineAlign. It is the single
// source of truth for tab geometry, shared by placeBar (rendering) and TabAtX
// (hit-testing).
func (m *Model) tabSpans(barX, w int) []tabSpan {
	totalWidth := 0
	for idx, tab := range m.tabs {
		if idx > 0 {
			totalWidth += uniseg.StringWidth(TabSeparator)
		}
		totalWidth += tabPadding + uniseg.StringWidth(tab.GetTitle()) + tabPadding
	}
	startX := alignStart(w, totalWidth, float64(m.bundle.Config.Style.TablineAlign))
	spans := make([]tabSpan, 0, len(m.tabs))
	col := barX + startX
	for idx, tab := range m.tabs {
		if idx > 0 {
			col += uniseg.StringWidth(TabSeparator)
		}
		chunk := tabPadding + uniseg.StringWidth(tab.GetTitle()) + tabPadding
		spans = append(spans, tabSpan{idx: idx, start: col, end: col + chunk})
		col += chunk
	}
	return spans
}

// TabAtX returns the index of the tab whose chunk contains absolute column x on
// the bar row, or -1 if x is outside every tab chunk (including on a separator).
func (m *Model) TabAtX(x int) int {
	for _, sp := range m.tabSpans(m.drawRect.X, m.drawRect.W) {
		if x >= sp.start && x < sp.end {
			return sp.idx
		}
	}
	return -1
}

// OnMouseClick routes a left click. A click on the bar row selects the tab
// under x (isDouble is ignored on the bar row — double-clicking a tab just
// selects). A click in the content area is dispatched to the active component:
// when isDouble is true and the component implements component.DoubleClickTarget,
// OnMouseDoubleClick is called; otherwise OnMouseClick is called as normal (the
// fallback keeps single-click behaviour for components that have not yet
// implemented the double-click seam). Reports whether the click was consumed.
//
// tabs.Model is intentionally NOT component.MouseTarget: its OnMouseClick takes
// an extra isDouble bool resolved by the root before dispatch.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton, isDouble bool) bool {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return false
	}
	if y == m.drawRect.Y {
		if idx := m.TabAtX(x); idx >= 0 {
			m.Select(idx)
			return true
		}
		return false
	}
	if isDouble {
		if dt, ok := m.GetActiveComponent().(component.DoubleClickTarget); ok {
			return dt.OnMouseDoubleClick(x, y, btn)
		}
	}
	if mt, ok := m.GetActiveComponent().(component.MouseTarget); ok {
		return mt.OnMouseClick(x, y, btn)
	}
	return false
}

// OnMouseHover implements component.MouseHoverTarget: a hover on the bar row
// highlights the tab under the pointer (TabAtX); a hover in the content area
// clears the tab highlight and forwards to the active component (which tints its
// own row). Returns true when the tab highlight or the active component's hover
// changed.
func (m *Model) OnMouseHover(x, y int) bool {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return false
	}
	if y == m.drawRect.Y { // bar row
		changed := m.clearContentHover()
		if m.setHoverTab(m.TabAtX(x)) {
			changed = true
		}
		return changed
	}
	changed := m.setHoverTab(-1)
	if ht, ok := m.GetActiveComponent().(component.MouseHoverTarget); ok {
		if ht.OnMouseHover(x, y) {
			changed = true
		}
	}
	return changed
}

// ClearMouseHover drops the tab-bar highlight and hover state retained by every
// tab component. Clearing inactive tabs prevents old highlights from resurfacing
// after a keyboard-driven tab switch.
func (m *Model) ClearMouseHover() bool {
	changed := m.setHoverTab(-1)
	if m.clearContentHover() {
		changed = true
	}
	return changed
}

func (m *Model) clearContentHover() bool {
	changed := false
	for _, tab := range m.tabs {
		if ht, ok := tab.Component.(component.MouseHoverTarget); ok && ht.ClearMouseHover() {
			changed = true
		}
	}
	return changed
}

// setHoverTab records the hovered tab index, returning true only when it changed.
func (m *Model) setHoverTab(idx int) bool {
	if m.hoverTab == idx {
		return false
	}
	m.hoverTab = idx
	return true
}

// placeBar writes the tab strip cells onto the canvas at the given rect.
// Tabs are placed left-to-right separated by TabSeparator; alignment honours
// config.Style.TablineAlign by adjusting the start column. Column positions are
// derived from tabSpans so rendering and hit-testing share a single geometry source.
func (m *Model) placeBar(s component.Screen, rect component.Rect) {
	w, h := rect.W, rect.H
	if w < 1 || h < 1 {
		return
	}

	tabStyle := tablineCellStyle(m.bundle.Config)
	activeStyle := activeTabCellStyle(m.bundle.Config)

	spans := m.tabSpans(rect.X, w)
	maxX := rect.X + w

	// Pre-fill the row with base style so trailing cells carry the bar background.
	uicanvas.FillRowAt(s, rect.X, rect.Y, w, tabStyle)

	for idx, tab := range m.tabs {
		sp := spans[idx]
		// Draw the separator before the break check so an exactly-fitting
		// separator on the last column is rendered even when the following tab
		// chunk starts off-screen (matches the pre-refactor draw order).
		if idx > 0 {
			sepX := sp.start - uniseg.StringWidth(TabSeparator)
			if sepX < maxX {
				uicanvas.PlaceText(s, sepX, rect.Y, TabSeparator, tabStyle, maxX)
			}
		}
		if sp.start >= maxX {
			break
		}
		style := tabStyle
		switch {
		case idx == m.activeTab:
			style = activeStyle
		case idx == m.hoverTab && m.bundle.Config.Style.HoverRowBg.IsSet():
			// Hover tint — wash the non-active tab's cells with the hover bg.
			style = tabStyle
			style.Bg = m.bundle.Config.Style.HoverRowBg.Color
		}
		col := sp.start
		col = uicanvas.FillSpan(s, col, rect.Y, tabPadding, style, maxX)
		col = uicanvas.PlaceText(s, col, rect.Y, tab.GetTitle(), style, maxX)
		uicanvas.FillSpan(s, col, rect.Y, tabPadding, style, maxX)
	}
}

// PlaceHelpHeader writes a single-row tabbar-styled header containing title
// onto the screen at rect. Used by full-screen overlays (e.g. helpoverlay) to
// render a tab-styled header without going through the tile layout.
func PlaceHelpHeader(s component.Screen, rect component.Rect, bundle deps.Bundle, title string) {
	w, h := rect.W, rect.H
	if w < 1 || h < 1 {
		return
	}

	tabStyle := tablineCellStyle(bundle.Config)
	activeStyle := activeTabCellStyle(bundle.Config)

	uicanvas.FillRowAt(s, rect.X, rect.Y, w, tabStyle)

	totalWidth := tabPadding + uniseg.StringWidth(title) + tabPadding
	startX := alignStart(w, totalWidth, float64(bundle.Config.Style.TablineAlign))

	maxX := rect.X + w
	col := rect.X + startX
	col = uicanvas.FillSpan(s, col, rect.Y, tabPadding, activeStyle, maxX)
	col = uicanvas.PlaceText(s, col, rect.Y, title, activeStyle, maxX)
	uicanvas.FillSpan(s, col, rect.Y, tabPadding, activeStyle, maxX)
}

// alignStart returns the starting column for content of `contentWidth` placed
// inside a row of `rowWidth`, given an alignment fraction in [0,1]
// (0=left, 0.5=center, 1=right).
func alignStart(rowWidth, contentWidth int, pos float64) int {
	if contentWidth >= rowWidth {
		return 0
	}
	gap := rowWidth - contentWidth
	start := int(float64(gap) * pos)
	if start < 0 {
		return 0
	}
	if start > gap {
		return gap
	}
	return start
}

// HandleKey routes a key event to the active component if it implements
// KeyTarget; otherwise it is ignored (the component has no key handling).
// State mutations occur via the active component's pointer receiver; no
// writeback is needed.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if kt, ok := m.GetActiveComponent().(component.KeyTarget); ok {
		return kt.HandleKey(ev)
	}
	return component.KeyIgnored
}

// WithOnLeave registers a callback invoked whenever Select changes the active
// tab. The callback is called synchronously after the active tab index has
// been updated, receiving the previous (from) and new (to) indices. It is not
// called when Select is invoked with the currently-active index.
func (m *Model) WithOnLeave(f func(from, to int)) *Model {
	m.onLeave = f
	return m
}

func (m *Model) Select(idx int) {
	target := numeric.Clamp(idx, 0, len(m.tabs)-1)
	if target == m.activeTab {
		return
	}
	prev := m.activeTab
	m.prevActiveTab = m.activeTab
	m.activeTab = target
	if m.onLeave != nil {
		m.onLeave(prev, target)
	}
}

func (m *Model) SelectPrevActive() {
	m.logger.Debug("tab selected", "from", m.activeTab, "to", m.prevActiveTab, logging.KeyAction, "prev_active")
	m.Select(m.prevActiveTab)
}

func (m *Model) SelectPrev() {
	prev := m.activeTab - 1
	m.logger.Debug("tab selected", "from", m.activeTab, "to", prev, logging.KeyAction, "prev")
	m.Select(prev)
}

func (m *Model) SelectNext() {
	next := m.activeTab + 1
	m.logger.Debug("tab selected", "from", m.activeTab, "to", next, logging.KeyAction, "next")
	m.Select(next)
}

func (m *Model) GetActiveTab() Tab {
	return m.tabs[m.activeTab]
}

// Count returns the number of tabs currently registered. It reflects
// conditionally-registered tabs (e.g. the opt-in Archive tab), so callers can
// gate index-based selection against the real count rather than a compile-time
// maximum.
func (m *Model) Count() int {
	return len(m.tabs)
}

// Titles returns the title of every tab in bar order. Used by callers (and
// tests) that need to reason about which tabs are present without depending on
// hardcoded indices.
func (m *Model) Titles() []string {
	out := make([]string, len(m.tabs))
	for i, t := range m.tabs {
		out[i] = t.GetTitle()
	}
	return out
}

func (m *Model) GetActiveComponent() component.Component {
	return m.GetActiveTab().Component
}

func (m *Model) ReplaceComponent(tab int, c component.Component) {
	m.logger.Debug("component replaced", "tab", tab)
	m.tabs[tab].Component = c
}
