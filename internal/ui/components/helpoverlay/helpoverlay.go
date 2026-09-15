// Package helpoverlay implements an overlay component that displays the active
// tab's keybinds. It owns its own header bar and a fuzzy-searchable list of
// bindings, sized to fit the visible items so the surrounding background can
// be dimmed for modal emphasis.
package helpoverlay

import (
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/components/tabs"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
	uv "github.com/charmbracelet/ultraviolet"
)

// Layout invariants for the overlay rect:
//   - row 0:                title header
//   - rows 1..h-borderH-1:  inner list (fuzzy bar + items)
//   - row h-1:              bottom separator border
const (
	headerH = 1
	fuzzyH  = 2
	borderH = 1
)

// Compile-time seam opt-ins: the authoritative list of the optional component
// seams *Model implements. Every implemented seam belongs here — a seam rename
// or signature change would otherwise silently demote its handler to an
// ordinary method and drop that input with no build error.
var (
	_ component.KeyTarget        = (*Model)(nil)
	_ component.MouseTarget      = (*Model)(nil)
	_ component.MousePasteTarget = (*Model)(nil)
	_ component.ScrollTarget     = (*Model)(nil)
	_ component.MouseHoverTarget = (*Model)(nil)
	_ component.PasteTarget      = (*Model)(nil)
	_ component.Sizer            = (*Model)(nil)
	_ component.Positioner       = (*Model)(nil)
)

// Model is a content-sized overlay containing a tab-styled title header and an
// inner list.Model used for fuzzy-searching keybinds. Implements component.Sizer
// so the root can center the overlay and dim the background.
type Model struct {
	bundle   deps.Bundle
	drawRect component.Rect
	title    string
	list     *list.Model
	poster   msgs.Poster
}

// New creates a help overlay with the given title and list items. onSelect is
// called when the user confirms a fuzzy-found item (typically a keybind). It is
// also wired as the inner list's activate hook, so a click on the already-
// selected row runs the focused binding — the click analogue of pressing Enter.
// onSelect resolves the focused item via the inner list's cursor (GetCursor),
// which a cursor-row click does not move, so the click activates the same row.
func New(bundle deps.Bundle, title string, items [][]list.Segment, onSelect keys.Action) *Model {
	l := list.New(bundle).WithItems(items)
	if onSelect != nil {
		l.WithFuzzyConfirmAction(onSelect)
		l.WithActivate(onSelect)
	}
	return &Model{bundle: bundle, title: title, list: l}
}

// SetPoster injects the runtime poster used to post events off-loop (R5a).
func (m *Model) SetPoster(p msgs.Poster) {
	m.poster = p
	m.list.SetPoster(p)
}

func (m *Model) Init() {}

// HandleKey routes the key event to the inner list's HandleKey. The help
// overlay is a modal that consumes all keys — navigation, fuzzy-search, and
// confirm/cancel are all handled by the list. Even when the list returns
// KeyIgnored (no bound action and no focused text-feed), the overlay still
// swallows the key so it does not leak to tab-switching or the root's regular
// handler.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	m.list.HandleKey(ev)
	return component.KeyHandled
}

// OnMouseClick implements component.MouseTarget by forwarding the click to the
// inner list. The list's drawRect (set in SetRect, below the 1-row header)
// scopes the hit-test, so a click on the header or bottom border falls outside
// the list area and is not consumed. A non-cursor item-row click selects it; a
// cursor-row click runs the activate hook (the focused binding, via onSelect).
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	return m.list.OnMouseClick(x, y, btn)
}

// OnMousePaste implements component.MousePasteTarget by forwarding the
// middle-click to the inner list, which pastes into its fuzzy-filter header.
func (m *Model) OnMousePaste(x, y int, content string) bool {
	return m.list.OnMousePaste(x, y, content)
}

// OnMouseScroll implements component.ScrollTarget by forwarding a vertical wheel
// notch to the inner list (which pans the help bindings, drag-at-edge).
func (m *Model) OnMouseScroll(x, y, lines int) bool {
	return m.list.OnMouseScroll(x, y, lines)
}

// OnMouseHover implements component.MouseHoverTarget by forwarding to the inner
// list (which tints the hovered help-binding row).
func (m *Model) OnMouseHover(x, y int) bool {
	return m.list.OnMouseHover(x, y)
}

// ClearMouseHover drops hover retained by the owned list.
func (m *Model) ClearMouseHover() bool {
	return m.list.ClearMouseHover()
}

// OnPaste forwards a bracketed-paste event to the inner list (fuzzy filter).
// The root calls it directly when the help overlay is the active overlay.
func (m *Model) OnPaste(ev uv.Event) {
	m.list.OnPaste(ev)
}

// SetRect stores the assigned rect, splits it into a 1-row header at the top,
// a list area in the middle, and a 1-row separator border at the bottom, and
// propagates the list rect to the inner list.
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
	listH := r.H - headerH - borderH
	listR := component.Rect{X: r.X, Y: r.Y + headerH, W: r.W, H: listH}
	if listH < 1 {
		listR = component.Rect{X: r.X, Y: r.Y + r.H, W: r.W, H: 0}
	}
	m.list.SetRect(listR)
}

// Draw places the header at the top of drawRect, forwards rendering to the
// inner list, and writes a horizontal separator at the bottom row. Returns
// the list's cursor (already in absolute coords).
func (m *Model) Draw(s component.Screen) *component.Cursor {
	r := m.drawRect
	if r.W < 1 || r.H < 1 {
		return nil
	}
	tabs.PlaceHelpHeader(s, component.Rect{X: r.X, Y: r.Y, W: r.W, H: headerH}, m.bundle, m.title)
	if r.H < headerH+1 {
		return nil
	}
	cursor := m.list.Draw(s)
	placeBottomBorder(s, r)
	return cursor
}

// placeBottomBorder writes a horizontal separator on the last row of rect.
func placeBottomBorder(s component.Screen, rect component.Rect) {
	if rect.H < headerH+borderH {
		return
	}
	y := rect.Y + rect.H - 1
	for x := range rect.W {
		s.SetCell(rect.X+x, y, &uv.Cell{Content: "─", Width: 1})
	}
}

// GetKeybinds returns the inner list's keybinds.
func (m *Model) GetKeybinds() []keys.Binding {
	return m.list.GetKeybinds()
}

// GetCursor exposes the inner list cursor so callers (e.g. the dispatcher
// hooked up by ui.Model.doKeybindAction) can resolve the fuzzy-confirmed item.
func (m *Model) GetCursor() int {
	return m.list.GetCursor()
}

// Focus delegates to the inner list's textinput focus toggle.
func (m *Model) Focus() {
	m.list.Focus()
}

// PreferredSize implements component.Sizer. Width fills maxW (header spans
// full width and items can be long); height matches actual content — header
// + fuzzy bar + visible item count + bottom border — so the overlay shrinks
// when the user fuzzy-filters and the surrounding cells dim into view.
func (m *Model) PreferredSize(maxW, maxH int) (w, h int) {
	visible := m.list.VisibleLen()
	minH := headerH + fuzzyH + borderH
	h = max(min(headerH+fuzzyH+visible+borderH, maxH), minH)
	return maxW, h
}

// Position implements component.Positioner. Help overlay anchors to the top
// of the available area instead of centering, so the dim band always covers
// the bottom (revealed when fuzzy filtering shrinks the visible items).
func (m *Model) Position(outer component.Rect, w, h int) component.Rect {
	if w > outer.W {
		w = outer.W
	}
	if h > outer.H {
		h = outer.H
	}
	return component.Rect{X: outer.X, Y: outer.Y, W: w, H: h}
}
