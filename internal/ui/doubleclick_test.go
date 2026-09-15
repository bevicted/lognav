package ui

import (
	"testing"
	"time"

	"github.com/bevicted/lognav/internal/deps/depstest"
	"github.com/bevicted/lognav/internal/ui/msgs/msgstest"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDoubleClickModel builds a minimal model with a controllable now clock and
// a configured DoubleClickMs, ready for isDoubleClick unit tests.
func newDoubleClickModel(t *testing.T, doubleClickMs uint16) *Model {
	t.Helper()
	bundle := depstest.NewTest(t)
	bundle.Config.Core.DoubleClickMs = doubleClickMs
	m, err := New(t.Context(), bundle)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestModel_IsDoubleClick tests the isDoubleClick detection logic with a
// table-driven set of click sequences, all using a fixed base time.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_IsDoubleClick(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		doubleClickMs uint16
		clicks        []struct {
			x, y int
			dt   time.Duration // time after base
		}
		// want[i] = expected return value of the i-th isDoubleClick call
		want []bool
	}{
		{
			name:          "two clicks same cell within window → second is double",
			doubleClickMs: 400,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 300 * time.Millisecond},
			},
			want: []bool{false, true},
		},
		{
			name:          "two clicks same cell outside window → false",
			doubleClickMs: 400,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 500 * time.Millisecond},
			},
			want: []bool{false, false},
		},
		{
			name:          "two clicks different cell within window → false",
			doubleClickMs: 400,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 6, y: 5, dt: 100 * time.Millisecond},
			},
			want: []bool{false, false},
		},
		{
			name:          "three rapid clicks same cell → click2 true, click3 false (chain broken)",
			doubleClickMs: 400,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 100 * time.Millisecond},
				{x: 5, y: 5, dt: 200 * time.Millisecond},
			},
			want: []bool{false, true, false},
		},
		{
			name:          "DoubleClickMs=0 disables double-click, always false",
			doubleClickMs: 0,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 100 * time.Millisecond},
			},
			want: []bool{false, false},
		},
		{
			name:          "DoubleClickMs=1 clamps to 50ms min: 30ms gap is still a double",
			doubleClickMs: 1,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 30 * time.Millisecond},
			},
			want: []bool{false, true},
		},
		{
			name:          "DoubleClickMs=5000 clamps to 2000ms max: 1500ms gap is a double",
			doubleClickMs: 5000,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 1500 * time.Millisecond},
			},
			want: []bool{false, true},
		},
		{
			name:          "DoubleClickMs=5000 clamps to 2000ms max: 2500ms gap is NOT a double",
			doubleClickMs: 5000,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
				{x: 5, y: 5, dt: 2500 * time.Millisecond},
			},
			want: []bool{false, false},
		},
		{
			name:          "two clicks exactly at window boundary → double",
			doubleClickMs: 400,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 3, y: 7, dt: 0},
				{x: 3, y: 7, dt: 400 * time.Millisecond},
			},
			want: []bool{false, true},
		},
		{
			name:          "first click is never a double",
			doubleClickMs: 400,
			clicks: []struct {
				x, y int
				dt   time.Duration
			}{
				{x: 5, y: 5, dt: 0},
			},
			want: []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newDoubleClickModel(t, tt.doubleClickMs)
			for i, click := range tt.clicks {
				got := m.isDoubleClick(click.x, click.y, base.Add(click.dt))
				assert.Equal(t, tt.want[i], got, "click %d", i)
			}
		})
	}
}

// TestModel_IsDoubleClick_ResetBreaksChain verifies that resetClickTracking
// (called when a right/middle click lands between two left-clicks) breaks the
// double-click chain: a left-click on the same cell within the window after a
// reset is a fresh single click, not a double.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_IsDoubleClick_ResetBreaksChain(t *testing.T) {
	m := newDoubleClickModel(t, 400)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	_ = m.isDoubleClick(5, 5, base) // first left-click
	m.resetClickTracking()          // simulate an intervening right/middle click
	got := m.isDoubleClick(5, 5, base.Add(100*time.Millisecond))

	assert.False(t, got, "a left-click after reset must not be a double")
}

// TestModel_OnMouseWheel_BreaksDoubleClickChain verifies that a wheel scroll
// between two same-cell left-clicks resets double-click tracking. Panning the
// viewport moves a different logical row under a fixed screen cell, so the
// second click must NOT be treated as a double activation of whatever scrolled
// under the pointer.
//
//nolint:paralleltest // ui.New writes package-level keys.HelpKeyStyle (race under -race).
func TestModel_OnMouseWheel_BreaksDoubleClickChain(t *testing.T) {
	bundle := depstest.NewTest(t)
	bundle.Config.Core.DoubleClickMs = 400
	bundle.Config.Core.ScrollAxisLockMs = 0 // disable axis lock so the notch always passes
	m, err := New(t.Context(), bundle)
	require.NoError(t, err)
	m.SetPoster(&msgstest.FakePoster{})
	t.Cleanup(func() { _ = m.Close() })
	m.OnResize(80, 24) // size the model so routeScroll/HandleKey route cleanly

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	require.False(t, m.isDoubleClick(5, 5, base), "first click is never a double")

	// A vertical wheel notch through the public path must reset tracking.
	m.OnMouse(uv.MouseWheelEvent{X: 5, Y: 5, Button: uv.MouseWheelDown})

	got := m.isDoubleClick(5, 5, base.Add(100*time.Millisecond))
	assert.False(t, got, "a left-click after a wheel scroll must not be a double")
}
