package ui

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/bevicted/lognav/internal/deps/depstest"
)

// zeroCells returns the count of untouched (zero) cells in [0,w)x[0,h) of buf.
// Untouched compose cells are NOT copied into the persistent rbuf by uv's
// TerminalScreen.Render, so after a grow they retain stale content.
func zeroCells(buf uv.ScreenBuffer, w, h int) (int, int, int) {
	zeros, fx, fy := 0, -1, -1
	for y := range h {
		for x := range w {
			c := buf.CellAt(x, y)
			if c == nil || c.IsZero() {
				zeros++
				if fx == -1 {
					fx, fy = x, y
				}
			}
		}
	}
	return zeros, fx, fy
}

func compose(w, h int) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(w, h)
	buf.Method = ansi.GraphemeWidth
	return buf
}

// TestModelFillsFullWidthAfterGrow drives the real Model through a narrow->wide
// resize (tmux zoom) and asserts every cell of the compose buffer is filled.
// Untouched cells go stale after a grow (the reported bug).
func TestModelFillsFullWidthAfterGrow(t *testing.T) {
	t.Parallel()
	bundle := depstest.NewTest(t)
	m, err := New(t.Context(), bundle)
	if err != nil {
		t.Fatalf("new model: %v", err)
	}

	const (
		h  = 24
		w1 = 80
		w2 = 160
	)

	for _, tab := range []struct {
		name string
		idx  int
	}{
		{"query", QueryTab},
		{"instances", InstancesTab},
		{"logs", LogsTab},
		{"snapshots", SnapshotTab},
		{"archive", ArchiveTab},
	} {
		m.tabs.Select(tab.idx)

		// Narrow draw, then grow.
		m.OnResize(w1, h)
		_ = m.DrawTo(compose(w1, h))
		m.OnResize(w2, h)

		buf := compose(w2, h)
		_ = m.DrawTo(buf)

		zeros, fx, fy := zeroCells(buf, w2, h)
		// Every cell must be filled each frame: uv's TerminalScreen.Render copies
		// only non-zero compose cells into the persistent rbuf, so an untouched
		// cell retains the prior frame's content and goes stale after a grow. The
		// runtime's reclear-after-resize workaround relies on this invariant.
		if zeros != 0 {
			t.Errorf("tab %s left %d untouched cells after grow to %dx%d (first at %d,%d); would go stale",
				tab.name, zeros, w2, h, fx, fy)
		}
	}
}
