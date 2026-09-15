package runtime

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
)

// ctrlC is the key event for a ctrl+c press.
func ctrlC() uv.KeyPressEvent { return uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl} }

// ctrlCClock is a controllable wall clock for the double-press window.
type ctrlCClock struct{ t time.Time }

func (c *ctrlCClock) now() time.Time          { return c.t }
func (c *ctrlCClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newCtrlCRuntime builds a runtime wired to a real ui.Model (dispatch routes
// every non-quitting key press into it) with a controllable clock, so the
// ctrl+c double-press gesture can be driven through the real dispatch seam.
func newCtrlCRuntime(t *testing.T, doubleClickMs uint16) (*runtime, *ctrlCClock) {
	t.Helper()

	bundle := depstest.NewTest(t)
	bundle.Config.Core.DoubleClickMs = doubleClickMs

	model, err := ui.New(t.Context(), bundle)
	require.NoError(t, err)
	model.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = model.Close() })
	model.OnResize(120, 40)

	clock := &ctrlCClock{t: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	rt := &runtime{
		model:       model,
		ctrlCWindow: doublePressWindow(doubleClickMs),
		now:         clock.now,
	}
	return rt, clock
}

// TestDispatch_SingleCtrlCDoesNotQuit pins the headline behavior change: ctrl+c
// is the Keys.Clear binding now, so one press must fall through to the model
// instead of exiting.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestDispatch_SingleCtrlCDoesNotQuit(t *testing.T) {
	rt, _ := newCtrlCRuntime(t, 400)

	rt.dispatch(ctrlC())

	assert.False(t, rt.quit, "a single ctrl+c must not quit: it is the clear key")
}

// TestDispatch_DoubleCtrlCQuits covers the exit gesture: a second ctrl+c inside
// the double-press window sets the loop's quit flag.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestDispatch_DoubleCtrlCQuits(t *testing.T) {
	rt, clock := newCtrlCRuntime(t, 400)

	rt.dispatch(ctrlC())
	clock.advance(100 * time.Millisecond)
	rt.dispatch(ctrlC())

	assert.True(t, rt.quit, "a second ctrl+c within the window must quit")
}

// TestDispatch_CtrlCOutsideWindowDoesNotQuit covers the expiry: two presses
// spaced further apart than the window are two independent clears, so a ctrl+c
// left over from minutes ago must not turn the next one into an exit.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestDispatch_CtrlCOutsideWindowDoesNotQuit(t *testing.T) {
	rt, clock := newCtrlCRuntime(t, 400)

	rt.dispatch(ctrlC())
	clock.advance(401 * time.Millisecond)
	rt.dispatch(ctrlC())

	assert.False(t, rt.quit, "a second ctrl+c after the window elapsed must not quit")
}

// TestDispatch_KeyBetweenCtrlCPressesDisarmsQuit mirrors resetClickTracking on
// the mouse side: anything typed between the two presses breaks the pair, so
// clearing an input, typing, and clearing again does not exit.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestDispatch_KeyBetweenCtrlCPressesDisarmsQuit(t *testing.T) {
	rt, clock := newCtrlCRuntime(t, 400)

	rt.dispatch(ctrlC())
	clock.advance(50 * time.Millisecond)
	rt.dispatch(uv.KeyPressEvent{Code: 'x', Text: "x"})
	clock.advance(50 * time.Millisecond)
	rt.dispatch(ctrlC())

	assert.False(t, rt.quit, "a key pressed between the two ctrl+c presses must break the pair")
}

// TestDispatch_CtrlCWindowFromConfig pins how Core.DoubleClickMs maps onto the
// exit gesture. The load-bearing case is 0: it disables the MOUSE double-click,
// but must not make lognav impossible to quit, so ctrl+c falls back to the same
// 400ms default. Non-zero values clamp to [50, 2000] like the mouse gesture.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestDispatch_CtrlCWindowFromConfig(t *testing.T) {
	tests := []struct {
		name          string
		doubleClickMs uint16
		gap           time.Duration
		wantQuit      bool
	}{
		{"0 falls back to 400ms: inside", 0, 399 * time.Millisecond, true},
		{"0 falls back to 400ms: outside", 0, 401 * time.Millisecond, false},
		{"1 clamps up to 50ms: inside", 1, 49 * time.Millisecond, true},
		{"1 clamps up to 50ms: outside", 1, 51 * time.Millisecond, false},
		{"5000 clamps down to 2000ms: inside", 5000, 1999 * time.Millisecond, true},
		{"5000 clamps down to 2000ms: outside", 5000, 2001 * time.Millisecond, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, clock := newCtrlCRuntime(t, tt.doubleClickMs)

			rt.dispatch(ctrlC())
			clock.advance(tt.gap)
			rt.dispatch(ctrlC())

			assert.Equal(t, tt.wantQuit, rt.quit)
		})
	}
}

// TestNewRuntime_CtrlCWindowReachesTheLoop closes the wiring gap the other
// tests cannot see: they build a runtime by hand, so they would all still pass
// if the production constructor never derived the window from config — leaving
// a zero window, where the second ctrl+c is never "inside" and lognav cannot be
// quit at all. This drives the same gesture through a runtime built the way Run
// builds it.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestNewRuntime_CtrlCWindowReachesTheLoop(t *testing.T) {
	bundle := depstest.NewTest(t)
	bundle.Config.Core.DoubleClickMs = 1000

	model, err := ui.New(t.Context(), bundle)
	require.NoError(t, err)
	model.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = model.Close() })
	model.OnResize(120, 40)

	rt := newRuntime(bundle, model, uv.NewTerminalScreen(io.Discard, os.Environ()), nil, slog.Default(), false)
	clock := &ctrlCClock{t: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	rt.now = clock.now

	rt.dispatch(ctrlC())
	clock.advance(900 * time.Millisecond)
	rt.dispatch(ctrlC())

	assert.True(t, rt.quit, "the configured 1000ms window must reach the loop: 900ms apart is a quit")
}
