package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/bevicted/lognav/internal/ui/msgs"
)

// eventPoster is the async ingress to the runtime event loop, replacing
// bubbletea's Program.Send. Post is droppable (ticks/keepalives);
// PostCritical must deliver log, stream, jq, and search batches.
// Go runs a tracked goroutine bound to the runtime context; Wait joins them
// at shutdown. Mirrors icb internal/tui/poster.go retyped to uv.Event.
type eventPoster struct {
	events chan uv.Event
	//nolint:containedctx // eventPoster holds runCtx to gate spawned goroutines + PostCritical; not a per-call injection.
	runCtx context.Context
	logger *slog.Logger
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// The runtime poster must satisfy the components-facing msgs.Poster seam
// structurally (both sides use uv.Event), with no change to eventPoster itself.
var _ msgs.Poster = (*eventPoster)(nil)

func newEventPoster(runCtx context.Context, events chan uv.Event) *eventPoster {
	return &eventPoster{events: events, runCtx: runCtx, logger: slog.Default()}
}

// Post enqueues ev, dropping it if the channel is full or the runtime is
// shutting down. Never blocks.
func (p *eventPoster) Post(ev uv.Event) {
	select {
	case p.events <- ev:
	case <-p.runCtx.Done():
	default:
	}
}

// PostCritical delivers ev, blocking until the channel accepts it or a ctx is
// cancelled. Tries a non-blocking send first so a deliverable event is not
// pre-empted by an already-cancelled caller ctx.
func (p *eventPoster) PostCritical(ctx context.Context, ev uv.Event) error {
	select {
	case p.events <- ev:
		return nil
	default:
	}
	select {
	case p.events <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.runCtx.Done():
		return p.runCtx.Err()
	}
}

// Go runs fn in a tracked goroutine bound to the runtime context. It is a
// no-op once the poster is shutting down (closed or runCtx cancelled).
func (p *eventPoster) Go(fn func(ctx context.Context)) {
	p.mu.Lock()
	if p.closed || p.runCtx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		// Worker panics are contained, not fatal: fn is one unit of background
		// work (a query stream, a jq/search chunk, a snapshot flush, an IO op),
		// and none of them owns UI state — the loop goroutine does. Killing the
		// whole TUI over one failed chunk loses every fetched log the user has
		// on screen, so log the fault with its stack and let the session carry
		// on. Unlike the loop guard this does NOT re-panic. See panicguard.go.
		defer func() {
			if r := recover(); r != nil {
				logPanic(p.logger, "poster goroutine", r)
			}
		}()
		if p.runCtx.Err() != nil {
			return
		}
		fn(p.runCtx)
	}()
}

// Wait blocks until all Go-spawned goroutines return or ctx expires. After
// Wait is called, Go is a no-op.
func (p *eventPoster) Wait(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("poster wait: %w", ctx.Err())
	}
}
