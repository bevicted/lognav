package msgs

import (
	"context"

	uv "github.com/charmbracelet/ultraviolet"
)

// Poster is the subset of the runtime eventPoster that components use to spawn
// tracked goroutines and deliver must-deliver events. It lives here (not in
// internal/ui/runtime) because runtime imports the components, so they cannot
// import runtime; msgs is the shared leaf both sides already import. The message
// param is uv.Event so the runtime's *eventPoster satisfies this structurally.
type Poster interface {
	// Go runs fn on a tracked goroutine; fn's ctx is the runtime's runCtx.
	Go(fn func(ctx context.Context))
	// PostCritical delivers ev, blocking until the loop drains it or ctx/runCtx cancels.
	PostCritical(ctx context.Context, ev uv.Event) error
	// Post delivers ev best-effort (droppable). For redraw ticks only.
	Post(ev uv.Event)
}

// PostAsync delivers ev through p without ever posting on the caller's
// goroutine. It is the one place the loop-deadlock invariant is written down.
//
// The runtime's event channel is unbuffered and the loop goroutine is its only
// receiver, so calling PostCritical *on* the loop goroutine — from a HandleKey /
// On<Event> / Update body, a keybind action, a context-menu Action or a dialog
// Cmd — blocks the only receiver on a send nothing can drain and wedges the
// whole TUI. Handing the post to a tracked poster goroutine keeps the loop free
// to drain it. See docs/dev/design/runtime.md.
//
// Off-loop callers may use PostAsync too: the extra goroutine is harmless, and
// callers that would otherwise have to reason about which goroutine they are on
// do not have to. Callers already inside a poster.Go body should post directly
// with the ctx they were handed instead — they are off-loop by construction.
//
// PostAsync is nil-safe: components take their poster via SetPoster after
// construction, so p is legitimately nil until startup wires it (and in tests
// that never wire it), and a dropped post is the right answer there.
//
// The delivery is must-deliver (PostCritical), and the returned error is
// intentionally dropped: it is non-nil only when the runtime is shutting down,
// where there is no longer anything a caller could do about it.
func PostAsync(p Poster, ev uv.Event) {
	if p == nil {
		return
	}
	p.Go(func(ctx context.Context) { _ = p.PostCritical(ctx, ev) })
}

// RequestQuit asks the runtime for a clean exit. It is PostAsync(p, QuitMsg{})
// under a name, because every component wires it as the Quit keybind's action
// and keybind actions run on the loop goroutine.
func RequestQuit(p Poster) {
	PostAsync(p, QuitMsg{})
}
