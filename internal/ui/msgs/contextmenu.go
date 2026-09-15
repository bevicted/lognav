package msgs

// ContextMenuItem is one entry in a context menu: a label and the action run
// when it is activated. Action runs on the loop goroutine (like a keybind
// action); if it must post, it does so off-loop via poster.Go. Unlike
// DialogButton.Cmd (func(string), fed the input value), Action takes no argument
// — menu items carry no input field. This divergence is intentional.
type ContextMenuItem struct {
	Label  string
	Action func()
}

// ShowContextMenuMsg triggers a context-menu overlay. Any component can post it
// to the runtime; the root instantiates the contextmenu overlay from Items.
// When Anchored is set, AnchorX/AnchorY give the absolute screen cell the menu's
// top-left corner should sit at (clamped/flipped to stay on-screen); otherwise
// the overlay is centered.
type ShowContextMenuMsg struct {
	Items    []ContextMenuItem
	Anchored bool
	AnchorX  int
	AnchorY  int
	// SuppressCloseRelease, when true, tells the root NOT to release focus
	// (FocusMsg{GrabFocus:false}) when the menu closes *after an item was
	// activated*. Use it for menus whose items re-establish focus themselves
	// (e.g. opening an in-tab mode); the close would otherwise race and clobber
	// the item's focus grab. It is honored only on an item activation — a bare
	// Esc/click-outside dismiss (no item ran, nothing re-grabbed focus) always
	// releases, so focus is never left stranded. Default false keeps the normal
	// release-on-close behavior.
	SuppressCloseRelease bool
}
