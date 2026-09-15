//go:build !windows

package runtime

import (
	"context"
	"os"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/term"
)

// startWinch spawns the resize watcher. It injects the initial window size
// (replacing bubbletea's startup resizeMsg) and one WindowSizeEvent per
// SIGWINCH, both via PostCritical so the first frame is correctly sized and
// no resize is dropped. uv.NotifyWinch auto-includes SIGWINCH.
func (rt *runtime) startWinch() {
	winch := make(chan os.Signal, 1)
	uv.NotifyWinch(winch)
	rt.poster.Go(func(ctx context.Context) {
		inject := func() {
			w, h, err := term.GetSize(os.Stdout.Fd())
			if err == nil {
				_ = rt.poster.PostCritical(ctx, uv.WindowSizeEvent{Width: w, Height: h})
			}
		}
		inject() // initial size
		for {
			select {
			case <-ctx.Done():
				return
			case <-winch:
				inject()
			}
		}
	})
}
