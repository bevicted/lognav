package component

import (
	"github.com/bevicted/lognav/internal/ui/keys"
	uv "github.com/charmbracelet/ultraviolet"
)

// Rect is a screen-relative rectangle assigned to a component by its parent.
// X and Y are absolute screen coordinates. W and H are width and height in cells.
type Rect struct {
	X, Y, W, H int
}

// Screen is the lognav-owned alias for the compose-buffer abstraction every
// component draws against (master §5.6 backend seam). It is satisfied by
// *lipgloss.Canvas (transitional test/compose backing), uv.ScreenBuffer (the
// R3 compose buffer), and uv.TerminalScreen — each exposes
// Bounds/CellAt(x,y) *uv.Cell/SetCell/WidthMethod. Aliasing localizes a future
// compose-buffer or backend swap to the spine without touching component
// signatures (it is NOT an insulation layer against uv churn — components build
// uv.Cell literals directly; the churn hedge is the go.mod version pin).
type Screen = uv.Screen

// Component is the interface every TUI component implements. Parents assign a
// rect via SetRect and trigger rendering via Draw. Draw writes cells (and/or
// composes layers) onto the shared screen and returns the cursor in absolute
// screen coordinates if the component owns it, otherwise nil.
type Component interface {
	Init()
	SetRect(rect Rect)
	Draw(s Screen) *Cursor
	GetKeybinds() []keys.Binding
}

// StatusPill is one declarative item in a view-owned contextual status row.
// Action is invoked only for cells drawn in the previous frame.
type StatusPill struct {
	Head       string
	Value      string
	ValueStyle uv.Style
	Action     func()
}

// StatusVariant is an ordered fallback layout for a contextual status row.
type StatusVariant []StatusPill

// StatusProvider supplies ordered contextual-row layouts for the active view.
// It is an output seam, not an input seam, and intentionally is not part of the
// input-seam inventory.
type StatusProvider interface {
	StatusVariants() []StatusVariant
}

// Cursor is the lognav-owned cursor position a component returns from Draw
// (R5a D1). The only production read is runtime.applyCursor: nil hides the
// cursor, else the position is set to (X, Y). Shape/Blink/Color are omitted
// (no component needs them today).
type Cursor struct{ X, Y int }

// NewCursor returns a *Cursor at the given absolute screen coordinates.
func NewCursor(x, y int) *Cursor { return &Cursor{X: x, Y: y} }

// Sizer is an optional interface implemented by components that negotiate
// their own size (typically modals). Parents call PreferredSize before
// assigning a rect, clamping the requested size to maxW/maxH.
type Sizer interface {
	PreferredSize(maxW, maxH int) (w, h int)
}

// Positioner is an optional interface implemented by Sizer components that
// want a custom anchor inside the parent's available area. Without it,
// parents fall back to CenterRect (centered on both axes).
type Positioner interface {
	Position(outer Rect, w, h int) Rect
}

// CenterRect returns a rect of size (w, h) centered inside outer.
// Used by parents positioning Sizer-implementing children.
func CenterRect(outer Rect, w, h int) Rect {
	if w > outer.W {
		w = outer.W
	}
	if h > outer.H {
		h = outer.H
	}
	return Rect{
		X: outer.X + (outer.W-w)/2,
		Y: outer.Y + (outer.H-h)/2,
		W: w,
		H: h,
	}
}

// KeyResult reports whether a KeyTarget consumed a key event.
type KeyResult int

const (
	// KeyIgnored means the event was not handled; the caller should fall
	// through to the next handler in its precedence chain.
	KeyIgnored KeyResult = iota
	// KeyHandled means the event was consumed; the caller stops dispatching.
	KeyHandled
)

// KeyTarget is the optional interface a component implements to receive key
// events directly. It is discovered by type assertion at the dispatch site.
// HandleKey reports whether the event was consumed; actions run their side
// effects directly (and post off-loop via the poster when needed), so no cmd
// is returned (R5a B2c).
type KeyTarget interface {
	HandleKey(ev uv.KeyPressEvent) KeyResult
}

// MouseTarget is the optional interface a component implements to receive
// mouse-click events, discovered by type assertion at the dispatch site (the
// mouse analogue of KeyTarget). x and y are absolute screen coordinates; the
// implementer hit-tests against the rect it draws into plus its own
// scroll/cursor state. It returns true when the click was consumed.
type MouseTarget interface {
	OnMouseClick(x, y int, btn uv.MouseButton) bool
}

// SecondaryMouseTarget is the optional interface a component implements to
// receive a right-click (secondary button) as a context action, discovered by
// type assertion at the dispatch site. It is kept separate from MouseTarget so
// that a right-click reaches only components that explicitly opt in — most
// components ignore the button argument in OnMouseClick, so routing right-clicks
// through the primary path would trigger unwanted select/toggle/activate side
// effects on tabs that never meant to handle them. x and y are absolute screen
// coordinates; it returns true when the click was consumed.
type SecondaryMouseTarget interface {
	OnMouseRight(x, y int) bool
}

// MousePasteTarget is the optional interface a component implements to receive a
// middle-click paste, discovered by type assertion at the dispatch site. The
// root reads the clipboard once and hands the text down as content; the
// implementer focuses the text input under (x, y), places the caret there, and
// inserts content (mirroring the X11 middle-click-pastes-at-the-pointer
// convention). It is kept separate from the bracketed-paste OnPaste broadcast,
// which is unconditional and fans out to every input; a middle-click must land
// only in the single input under the pointer. x and y are absolute screen
// coordinates; it returns true when the content was inserted.
type MousePasteTarget interface {
	OnMousePaste(x, y int, content string) bool
}

// PasteTarget is the optional interface a component implements to receive a
// bracketed-paste event, discovered by type assertion at the dispatch site. It
// has exactly one target — the active overlay if there is one, else the active
// tab's component — and is never broadcast to every tab. Components owning a
// text input gate the forward on that input being focused, matching HandleKey.
// It is the keyboard counterpart to the pointer-anchored MousePasteTarget.
type PasteTarget interface {
	OnPaste(ev uv.Event)
}

// ScrollTarget is the optional interface a component implements to receive a
// vertical mouse-wheel notch as a viewport pan (scroll the view, not the
// cursor), discovered by type assertion at the dispatch site. lines is the
// signed number of display rows to pan: positive scrolls toward later content
// (wheel down), negative toward earlier content (wheel up). It is kept separate
// from the primary OnMouseClick path because most components treat a wheel notch
// as a cursor move (synthetic arrow key) rather than a viewport pan; only
// components that opt in get the decoupled scroll. x and y are absolute screen
// coordinates (for components that route the scroll by sub-region). It returns
// true when the scroll was consumed; a false return lets the root fall back to
// the synthetic arrow-key behaviour. Horizontal wheel notches are not routed
// here — they stay on the synthetic KeyLeft/KeyRight path.
type ScrollTarget interface {
	OnMouseScroll(x, y, lines int) bool
}

// MouseHoverTarget is the optional interface a component implements to receive
// pointer-motion events for hover feedback (e.g. dim the row under the pointer,
// move a button highlight), discovered by type assertion at the dispatch site.
// It is experimental and gated behind Core.EnableHover — motion events are only
// routed here when that flag is on. x and y are absolute screen coordinates. The
// returned bool reports whether the hover changed visible state; callers need
// not act on it (a motion event already triggers a redraw) but it keeps the seam
// symmetric with the other mouse targets and lets implementers gate work to row
// changes. ClearMouseHover drops pointer-derived visual state when keyboard input
// takes over or the pointer moves outside the target's routing area.
type MouseHoverTarget interface {
	OnMouseHover(x, y int) bool
	ClearMouseHover() bool
}

// DoubleClickTarget is the optional interface a component implements to receive
// a double-click (two left-clicks on the same cell within Core.DoubleClickMs) as
// an ACTIVATE action (expand log / open instance / load snapshot), discovered by
// type assertion at the dispatch site. It is the activation counterpart to the
// select-only OnMouseClick: a single click only moves the cursor/selection, and
// activation requires this double-click. x and y are absolute screen coordinates;
// it returns true when the double-click was consumed.
type DoubleClickTarget interface {
	OnMouseDoubleClick(x, y int, btn uv.MouseButton) bool
}

// FocusReleaser is the optional interface a component implements to drop any
// input focus it currently holds when focus is taken away out-of-band — e.g. a
// left click that switches to another tab while one of the component's inputs
// is focused. Discovered by type assertion (like KeyTarget/MouseTarget). The
// caller owns resetting the model's focus gate; ReleaseFocus only blurs the
// component's own input widget(s) and must not post msgs.FocusMsg.
type FocusReleaser interface {
	ReleaseFocus()
}

// ContextMenuOpener is the optional interface a component implements to open its
// own context menu from the keyboard (the global ContextMenu key, `c`),
// discovered by type assertion at the root keybind dispatch site. The component
// opens its menu anchored at its own current cursor/selection (the mouse path
// uses SecondaryMouseTarget instead, anchored at the pointer). The action runs
// on the loop goroutine; the implementer posts ShowContextMenuMsg off-loop via
// poster.Go.
type ContextMenuOpener interface {
	OpenContextMenu()
}
