# Components and Input Seams

Every TUI component implements the small `component.Component` contract:

```go
type Component interface {
    Init()
    SetRect(Rect)
    Draw(Screen) *Cursor
    GetKeybinds() []keys.Binding
}
```

Components do not have a general `Update` method. The root routes posted events
to typed `On<Event>` methods. Terminal input is opt-in through optional seam
interfaces discovered by type assertion. This keeps a new input behavior from
silently applying to every component.

**Siblings:** [overview](overview.md) ; [runtime](runtime.md).

## Input routing

The root owns precedence. An active overlay receives input first; otherwise
input reaches the tab bar or active component. Key precedence is focused input
or overlay, tab selector, regular bindings, then the active component. Before
routing a key press, the root clears pointer-derived highlights through
`MouseHoverTarget.ClearMouseHover`; the next motion, click, or wheel event may
restore hover at its coordinates. Motion into swallowed areas such as the footer
also clears retained hover. Bracketed
paste has one target: the overlay, or the active component when it implements
`PasteTarget`. It is never broadcast to tabs.

The root owns an optional contextual row supplied by the active view's
`StatusProvider`, immediately above the optional key-hint line. Providers return
ordered declarative pill variants; the shared renderer chooses the first that
fits and caches the exact visible action ranges from its last draw. A left click
on an actionable pill invokes its existing action, while footer whitespace and
non-left footer input are swallowed. The key-hint line remains below contextual
status and derives hints from the active component's `GetKeybinds()` result.
`core.showKeyHints: false` returns only the hint row to tab content. At height
one, contextual status wins over hints; tabs without a provider reclaim that row.

| Seam                                                    | Purpose                                                      |
| ------------------------------------------------------- | ------------------------------------------------------------ |
| `KeyTarget`                                             | key handling with consumed result                            |
| `MouseTarget`, `SecondaryMouseTarget`                   | left-click and explicit right-click behavior                 |
| `MousePasteTarget`, `PasteTarget`                       | pointer middle-click paste and bracketed paste               |
| `ScrollTarget`, `MouseHoverTarget`, `DoubleClickTarget` | wheel pan, hover, and activation gestures                    |
| `FocusReleaser`                                         | blur component-owned input after an out-of-band focus change |
| `ContextMenuOpener`                                     | keyboard context menu at the component's selection           |
| `Sizer`, `Positioner`                                   | overlay size and placement negotiation                       |

A component implementing a seam must add it to its compile-time assertion block.
A new seam must also be added to `component/inputSeams`; startup logs one debug
inventory of each tab's implemented and missing seams. Keep the inventory at
wiring time, not dispatch sites, because hover and scroll would otherwise flood
the log.

List wrappers hold their `filehandler` rather than embedding it. Forward each
seam deliberately so a new filehandler capability is not inherited without a
per-tab decision. The assertion block and startup inventory make a missed
forwarder visible.

`StatusProvider` is an output seam, not an input seam, so it is intentionally
absent from `inputSeams` and the startup input inventory.

## On-loop collaboration

If both producer and consumer run on the loop, inject a callback instead of a
message. Examples include picker status lookup, snapshot eviction, and overlay
close. A post would add a worker hop and event-loop round trip even though the
original input event already makes the frame dirty. See the canonical posting
rules in [runtime](runtime.md#posting-contract).

Overlays never clear the root-owned overlay slot or post a generic close event.
The root injects an `onClose` callback when it creates an overlay, and close
paths call it synchronously on the loop. This makes close-before-follow-up
ordering deterministic: a context-menu action can request a dialog after its
slot is already clear. `contextmenu` alone reports whether focus should be
released because it owns that activation decision.

## Construction and cleanup

Component constructors use:

```go
New(ctx context.Context, bundle deps.Bundle, ...args)
```

`ctx` is first and `bundle` is second. Constructors without a context take
`bundle` first. Pass dependencies through `deps.Bundle`; do not introduce global
state. `Close` is intentionally absent from `Component`: only a type that owns
goroutines, cancelable contexts, or file descriptors implements idempotent
`Close() error` and reports cleanup errors as described in [runtime](runtime.md).
