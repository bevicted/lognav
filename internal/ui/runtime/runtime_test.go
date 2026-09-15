package runtime

import (
	"context"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/ui/msgs"
)

// TestDispatch_QuitMsgSetsQuit verifies the loop-side quit path: a posted
// msgs.QuitMsg sets the quit flag and reports no redraw.
func TestDispatch_QuitMsgSetsQuit(t *testing.T) {
	t.Parallel()
	rt := &runtime{}
	assert.False(t, rt.dispatch(msgs.QuitMsg{}))
	assert.True(t, rt.quit)
}

// TestNotifyPayload renders the fetch-complete escape sequence for each style,
// outside and inside tmux. The osc* styles get DCS-passthrough-wrapped under
// tmux (\ePtmux;...\e\\, ESC bytes doubled); the bell stays a raw BEL so tmux
// routes it through its own bell-action.
func TestNotifyPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		style  config.NotifyStyle
		inTmux bool
		want   string
	}{
		{name: "bell", style: config.NotifyStyleBell, want: "\a"},
		{name: "bell in tmux not wrapped", style: config.NotifyStyleBell, inTmux: true, want: "\a"},
		{name: "osc9", style: config.NotifyStyleOSC9, want: "\x1b]9;lognav: fetch complete\x07"},
		{name: "osc777", style: config.NotifyStyleOSC777, want: "\x1b]777;notify;lognav;fetch complete\x07"},
		{name: "osc99", style: config.NotifyStyleOSC99, want: "\x1b]99;;lognav: fetch complete\x07"},
		{name: "unknown falls back to bell", style: config.NotifyStyle("bogus"), want: "\a"},
		{name: "osc9 in tmux wrapped", style: config.NotifyStyleOSC9, inTmux: true, want: "\x1bPtmux;\x1b\x1b]9;lognav: fetch complete\x07\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, notifyPayload(tt.style, tt.inTmux))
		})
	}
}

// TestMouseModeFor verifies terminal mouse-mode selection: no mode when mouse is
// off; button-event (drag) tracking when mouse is on but hover is off; any-event
// (motion) tracking when hover is on — because bare-hover motion (no button) is
// only reported under MouseModeMotion (?1003h), never MouseModeDrag (?1002h).
func TestMouseModeFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		enableMouse bool
		enableHover bool
		wantMode    uv.MouseMode
		wantSet     bool
	}{
		{name: "mouse off", enableMouse: false, enableHover: false, wantSet: false},
		{name: "mouse off ignores hover", enableMouse: false, enableHover: true, wantSet: false},
		{name: "mouse on, hover off -> drag", enableMouse: true, enableHover: false, wantMode: uv.MouseModeDrag, wantSet: true},
		{name: "mouse on, hover on -> motion", enableMouse: true, enableHover: true, wantMode: uv.MouseModeMotion, wantSet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mode, set := mouseModeFor(tt.enableMouse, tt.enableHover)
			assert.Equal(t, tt.wantSet, set, "set")
			if tt.wantSet {
				assert.Equal(t, tt.wantMode, mode, "mode")
			}
		})
	}
}

// TestSyncTicker_StartsThenStopsCleanly verifies the shared redraw ticker:
// syncTicker(true) sets tickerRunning and spawns a goroutine that posts tickMsg
// (observed by draining the events channel); syncTicker(false) closes the stop
// channel and no further ticks arrive. Same-state calls are no-ops (idempotent),
// and the goroutine exits cleanly (goleak via main_test.go VerifyTestMain).
func TestSyncTicker_StartsThenStopsCleanly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rt := &runtime{events: make(chan uv.Event, 64), runCtx: ctx}
	rt.poster = newEventPoster(ctx, rt.events)

	// Start the ticker.
	rt.syncTicker(true)
	require.True(t, rt.tickerRunning, "syncTicker(true) must set tickerRunning")
	require.NotNil(t, rt.tickerStop, "syncTicker(true) must create the stop channel")

	// Idempotent: calling true again while running must not spawn a second ticker
	// or replace the stop channel.
	stopBefore := rt.tickerStop
	rt.syncTicker(true)
	assert.Equal(t, stopBefore, rt.tickerStop, "syncTicker(true) twice must not replace the stop channel")

	// Observe at least one tick.
	require.Eventually(t, func() bool {
		for {
			select {
			case ev := <-rt.events:
				if _, ok := ev.(tickMsg); ok {
					return true
				}
			default:
				return false
			}
		}
	}, 2*time.Second, 5*time.Millisecond)

	// Stop the ticker.
	rt.syncTicker(false)
	assert.False(t, rt.tickerRunning, "syncTicker(false) must clear tickerRunning")

	// Idempotent: stopping again while already stopped is a no-op (must not panic
	// by double-closing the stop channel).
	require.NotPanics(t, func() { rt.syncTicker(false) }, "syncTicker(false) twice must be a no-op")

	// Drain anything already queued, then assert the channel goes quiet (no more
	// ticks are produced after stop).
	for {
		select {
		case <-rt.events:
			continue
		default:
		}
		break
	}
	assert.Never(t, func() bool {
		select {
		case ev := <-rt.events:
			_, ok := ev.(tickMsg)
			return ok
		default:
			return false
		}
	}, 250*time.Millisecond, 25*time.Millisecond, "no ticks must arrive after syncTicker(false)")

	require.NoError(t, rt.poster.Wait(ctx))
}

// TestDrainPoster_CancelQueriesUnblocksWait verifies the shutdown drain cancels
// in-flight work BEFORE waiting, so a query goroutine blocked on its own
// (background-derived, non-runCtx) context is aborted and Wait joins promptly
// instead of burning the full timeout. This is the slow-quit-during-fetch fix:
// without the pre-Wait cancel, Wait blocks the entire timeout while the streaming
// query drains (cf. TestEventPoster_Wait_WrapsDeadlineExceeded). cancelRun cannot
// abort the query because StartQuery's ctx is background-derived by design.
func TestDrainPoster_CancelQueriesUnblocksWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rt := &runtime{events: make(chan uv.Event, 1), runCtx: ctx}
	rt.poster = newEventPoster(ctx, rt.events)

	// Mimic an in-flight ICL query: a tracked goroutine that blocks on a
	// background-derived context (NOT runCtx), exactly like Instance.StartQuery.
	qctx, qcancel := context.WithCancel(context.Background())
	defer qcancel()
	rt.poster.Go(func(context.Context) { <-qctx.Done() })

	start := time.Now()
	rt.drainPoster(qcancel, 2*time.Second)
	assert.Less(t, time.Since(start), 500*time.Millisecond,
		"drainPoster must cancel queries first so Wait returns promptly, not after the full timeout")
}

// TestDrainPoster_NilCancelStillWaits verifies drainPoster tolerates a nil cancel
// func and still bounds the wait by the timeout (the backstop path).
func TestDrainPoster_NilCancelStillWaits(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rt := &runtime{events: make(chan uv.Event, 1), runCtx: ctx}
	rt.poster = newEventPoster(ctx, rt.events)

	block := make(chan struct{})
	defer close(block) // release for goleak
	rt.poster.Go(func(context.Context) { <-block })

	start := time.Now()
	rt.drainPoster(nil, 20*time.Millisecond)
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond,
		"with nothing to cancel, drainPoster waits up to the timeout")
}

// TestNewComposeBuffer_GraphemeWidth verifies that newComposeBuffer forces
// GraphemeWidth width measurement (matching uv.NewBuffer behaviour).
// A ZWJ family emoji is width-2 under GraphemeWidth, but mis-measured wider
// under the raw uv WcWidth default; asserting StringWidth==2 catches a drift.
func TestNewComposeBuffer_GraphemeWidth(t *testing.T) {
	t.Parallel()
	buf := newComposeBuffer(10, 1)
	// A ZWJ family emoji is width-2 under GraphemeWidth, but mis-measured wider
	// under the raw uv WcWidth default. Asserts we forced GraphemeWidth.
	assert.Equal(t, 2, buf.WidthMethod().StringWidth("👨‍👩‍👧‍👦"))
}
