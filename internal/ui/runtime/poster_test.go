package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventPoster_Post_DropsWhenRunCtxCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := newEventPoster(ctx, make(chan uv.Event)) // unbuffered, no reader
	assert.NotPanics(t, func() { p.Post(uv.PasteStartEvent{}) },
		"Post must drop, not block, on cancelled runCtx")
}

func TestEventPoster_PostCritical_DeliversOnFreeChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan uv.Event, 1)
	p := newEventPoster(t.Context(), ch)
	require.NoError(t, p.PostCritical(t.Context(), uv.PasteEndEvent{}))
	assert.IsType(t, uv.PasteEndEvent{}, <-ch)
}

func TestEventPoster_PostCritical_DeliversOnFreeChannelEvenWithCancelledCallerCtx(t *testing.T) {
	t.Parallel()
	ch := make(chan uv.Event, 1)
	p := newEventPoster(t.Context(), ch)
	callerCtx, cancel := context.WithCancel(t.Context())
	cancel()
	// non-blocking first arm wins even though callerCtx is already done
	require.NoError(t, p.PostCritical(callerCtx, uv.PasteEndEvent{}))
	assert.IsType(t, uv.PasteEndEvent{}, <-ch)
}

func TestEventPoster_PostCritical_ReturnsCallerCtxErrOnTimeout(t *testing.T) {
	t.Parallel()
	p := newEventPoster(t.Context(), make(chan uv.Event)) // full (unbuffered, no reader)
	callerCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := p.PostCritical(callerCtx, uv.PasteEndEvent{})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestEventPoster_Go_RunsThenWaitJoins(t *testing.T) {
	t.Parallel()
	p := newEventPoster(t.Context(), make(chan uv.Event))
	var ran sync.WaitGroup
	ran.Add(1)
	p.Go(func(context.Context) { ran.Done() })
	require.NoError(t, p.Wait(t.Context()))
	ran.Wait() // would hang if Go never ran
}

func TestEventPoster_Go_NoopAfterWait(t *testing.T) {
	t.Parallel()
	p := newEventPoster(t.Context(), make(chan uv.Event))
	require.NoError(t, p.Wait(t.Context()))
	var ran bool
	p.Go(func(context.Context) { ran = true })
	require.NoError(t, p.Wait(t.Context()))
	assert.False(t, ran, "Go must be a no-op after Wait sets closed")
}

func TestEventPoster_Wait_WrapsDeadlineExceeded(t *testing.T) {
	t.Parallel()
	p := newEventPoster(t.Context(), make(chan uv.Event))
	block := make(chan struct{})
	defer close(block)                      // release the goroutine so goleak is clean
	p.Go(func(context.Context) { <-block }) // never returns until we close
	waitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := p.Wait(waitCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
