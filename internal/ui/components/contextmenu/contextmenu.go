// Package contextmenu implements a modal overlay presenting a vertical,
// fuzzy-searchable list of actions — the traditional right-click context menu.
// It wraps list.Model (the widget helpoverlay also wraps) for navigation,
// fuzzy-filtering, and rendering, and is content-sized. Placement is centered by
// default but, given an anchor via WithAnchor, the menu's top-left corner is
// pinned just below the caller's cursor (clamped/flipped to stay on-screen) —
// the traditional cursor-anchored placement. Each item carries its own action.
// On the activation/close axes it is a dialog-family overlay: items resolve their
// own Action internally (via the inner list's cursor) and every close calls the
// host close seam (the root clears the overlay slot and releases focus) — unlike
// the help overlay's root-resolved-cursor model.
package contextmenu

import (
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/rivo/uniseg"

	"github.com/bevicted/lognav/internal/deps"
	uicanvas "github.com/bevicted/lognav/internal/ui/canvas"
	"github.com/bevicted/lognav/internal/ui/components/component"
	"github.com/bevicted/lognav/internal/ui/components/list"
	"github.com/bevicted/lognav/internal/ui/keys"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// Layout constants. The inner list occupies the box interior (inset by the
// rounded border + one column of horizontal padding on each side). The list
// reserves fuzzyH rows for its own fuzzy header (input + separator).
const (
	borderW = 1 // rounded border thickness per side
	hpad    = 1 // horizontal breathing room inside the border
	fuzzyH  = 2 // list's fuzzy input + separator rows
)

// fuzzyMin is the placeholder width the list's fuzzy bar wants ("> fuzzyfind").
const fuzzyMin = len("> fuzzyfind")

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

// Model is a content-sized modal wrapping list.Model, centered by default or
// cursor-anchored via WithAnchor. items is kept in ORIGINAL order, parallel to
// the labels handed to the list; activation resolves list.GetCursor() (the
// original index even under fuzzy reorder) into items[i].
type Model struct {
	drawRect component.Rect
	list     *list.Model
	items    []msgs.ContextMenuItem
	kh       *keys.Handler

	// anchored places the menu's top-left at (anchorX, anchorY) instead of
	// centering it (the traditional cursor-anchored placement).
	anchored bool
	anchorX  int
	anchorY  int

	// suppressCloseRelease makes an item activation skip the focus release on
	// close (for menus whose items re-establish focus themselves).
	suppressCloseRelease bool

	// onClose is the host seam: the root's overlay-slot teardown. releaseFocus
	// tells the root whether the close should also drop the focus grab.
	onClose func(releaseFocus bool)
}

// New builds a context menu from items. The inner list shows the labels; the
// parallel items slice supplies each row's action. The list's Accept binding
// (Enter) and a single click both activate the focused/clicked row.
func New(bundle deps.Bundle, items []msgs.ContextMenuItem) *Model {
	m := &Model{items: items}

	labels := make([][]list.Segment, len(items))
	for i, it := range items {
		labels[i] = list.PlainItem(it.Label)
	}
	m.list = list.New(bundle).WithItems(labels)
	m.list.WithFuzzyConfirmAction(m.activateFocused)
	m.list.WithActivate(m.activateFocused)

	m.kh = keys.New().Bind(
		keys.Binding{
			Keys:    bundle.Config.Keys.Cancel,
			Context: "close context menu",
			Action:  m.Dismiss,
		},
	)
	return m
}

// WithAnchor places the menu's top-left corner at the absolute screen cell
// (x, y) instead of centering it. Position clamps/flips the box to keep it
// on-screen. Returns the model for chaining.
func (m *Model) WithAnchor(x, y int) *Model {
	m.anchored = true
	m.anchorX, m.anchorY = x, y
	return m
}

// WithSuppressCloseRelease marks this menu so the root skips releasing focus
// when an item activation closes it. Use it when the menu's items re-establish
// focus themselves (e.g. opening an in-tab picker); without it the close's
// FocusMsg{false} races and can clobber the item's focus grab. Returns the model
// for chaining.
func (m *Model) WithSuppressCloseRelease(suppress bool) *Model {
	m.suppressCloseRelease = suppress
	return m
}

// Position implements component.Positioner. Without an anchor it centers (the
// default). Anchored, it places the top-left at (anchorX, anchorY), shifting the
// box left when it would overflow the right edge and flipping it above the
// selected line (anchorY is the row just below the line) when it would overflow
// the bottom, then clamps to the outer rect.
func (m *Model) Position(outer component.Rect, w, h int) component.Rect {
	if !m.anchored {
		return component.CenterRect(outer, w, h)
	}
	w = min(w, outer.W)
	h = min(h, outer.H)
	x, y := m.anchorX, m.anchorY
	if x+w > outer.X+outer.W {
		x = outer.X + outer.W - w
	}
	if y+h > outer.Y+outer.H {
		y = m.anchorY - 1 - h // flip above the selected line
	}
	x = max(x, outer.X)
	y = max(y, outer.Y)
	return component.Rect{X: x, Y: y, W: w, H: h}
}

// SetPoster injects the runtime poster, forwarded to the inner list for its own
// focus posts.
func (m *Model) SetPoster(p msgs.Poster) {
	m.list.SetPoster(p)
}

// SetOnClose injects the host close seam. The root passes the callback that
// clears its overlay slot and (when releaseFocus is set) drops the focus grab.
func (m *Model) SetOnClose(f func(releaseFocus bool)) {
	m.onClose = f
}

// Init satisfies component.Component.
func (m *Model) Init() {}

// Focus focuses the inner list's fuzzy input so typing filters. The root calls
// it on open; list.Focus posts FocusMsg{GrabFocus:true} off-loop.
func (m *Model) Focus() { m.list.Focus() }

// activateFocused runs the action of the item under the list cursor, then closes
// the menu. list.GetCursor() returns the ORIGINAL item index even when the fuzzy
// filter reordered the display, so items[idx] is always the right action.
//
// The close is synchronous and lands BEFORE anything the Action posted (an
// Action can only reach the root off-loop, via the poster), so a menu item that
// opens a dialog gets a clean slot to open into.
//
// suppressCloseRelease is honored here and only here: an activated item may have
// re-grabbed focus itself, and releasing would clobber that grab.
func (m *Model) activateFocused() {
	idx := m.list.GetCursor()
	if idx >= 0 && idx < len(m.items) && m.items[idx].Action != nil {
		m.items[idx].Action()
	}
	m.close(!m.suppressCloseRelease)
}

// Dismiss closes the menu without running any action (Esc / click-outside). A
// bare dismiss re-grabs nothing, so it always releases focus — even on a
// suppress-marked menu, where keeping the grab would leave focus taken with
// nothing focused and swallow the next key.
func (m *Model) Dismiss() { m.close(true) }

// close invokes the host close seam. Every close path (key handling, mouse
// click, the root calling Dismiss directly) runs on the loop goroutine, and so
// does the root's slot teardown, so the call is a plain synchronous one.
func (m *Model) close(releaseFocus bool) {
	if m.onClose != nil {
		m.onClose(releaseFocus)
	}
}

// HandleKey intercepts the Cancel key (close) before delegating everything else
// to the inner list (navigation, fuzzy typing, and the Accept binding that
// triggers activateFocused). The menu is modal: it always reports KeyHandled.
func (m *Model) HandleKey(ev uv.KeyPressEvent) component.KeyResult {
	if m.kh.Run(ev) {
		return component.KeyHandled
	}
	m.list.HandleKey(ev)
	return component.KeyHandled
}

// OnMouseClick activates an item in a SINGLE click (unlike list's two-state
// default). A click on the fuzzy-bar row forwards to the list (focus + caret); a
// click on an item row moves the cursor there and activates it; a click below
// the last (filtered) item is a consumed no-op. x/y are absolute screen coords.
func (m *Model) OnMouseClick(x, y int, btn uv.MouseButton) bool {
	tiRect, _ := m.list.SubRects()
	if y >= tiRect.Y && y < tiRect.Y+tiRect.H {
		return m.list.OnMouseClick(x, y, btn)
	}
	idx, ok := m.list.RowAtY(y)
	if !ok {
		return true // inside the box but past the last item: consumed no-op
	}
	m.list.SetCursor(idx)
	m.activateFocused()
	return true
}

// OnMousePaste/OnMouseScroll/OnMouseHover/OnPaste forward to the inner list so
// the menu behaves under the root's mouse/paste seams exactly like help.
func (m *Model) OnMousePaste(x, y int, content string) bool {
	return m.list.OnMousePaste(x, y, content)
}
func (m *Model) OnMouseScroll(x, y, lines int) bool { return m.list.OnMouseScroll(x, y, lines) }
func (m *Model) OnMouseHover(x, y int) bool         { return m.list.OnMouseHover(x, y) }
func (m *Model) ClearMouseHover() bool              { return m.list.ClearMouseHover() }

// OnPaste forwards a bracketed-paste event to the inner list (fuzzy filter).
// The root calls it directly when the context menu is the active overlay.
func (m *Model) OnPaste(ev uv.Event) { m.list.OnPaste(ev) }

// GetKeybinds returns the inner list's keybinds, satisfying component.Component
// (and available for keybind-display integration).
func (m *Model) GetKeybinds() []keys.Binding { return m.list.GetKeybinds() }

// SetRect stores the box rect and forwards the interior (inset by the border +
// one column of horizontal padding) to the inner list.
func (m *Model) SetRect(r component.Rect) {
	m.drawRect = r
	inner := uicanvas.Pad(r, borderW, borderW+hpad, borderW, borderW+hpad)
	m.list.SetRect(inner)
}

// Draw paints the rounded box then the inner list. The menu owns no terminal
// cursor; the inner list renders its own reverse-video caret in the fuzzy bar,
// so Draw always returns nil.
func (m *Model) Draw(s component.Screen) *component.Cursor {
	if m.drawRect.W < 1 || m.drawRect.H < 1 {
		return nil
	}
	uicanvas.DrawBox(s, m.drawRect, uicanvas.RoundedSet, uv.Style{})
	m.list.Draw(s)
	return nil
}

// PreferredSize returns the content-fit box size: width = widest label (or the
// fuzzy placeholder) + horizontal padding + border on each side; height = fuzzy
// header + currently-visible item count + border. Shrinks as the user filters.
func (m *Model) PreferredSize(maxW, maxH int) (w, h int) {
	contentW := fuzzyMin
	for _, it := range m.items {
		if lw := uniseg.StringWidth(it.Label); lw > contentW {
			contentW = lw
		}
	}
	w = contentW + 2*(borderW+hpad)
	h = fuzzyH + m.list.VisibleLen() + 2*borderW
	return min(w, maxW), min(h, maxH)
}
