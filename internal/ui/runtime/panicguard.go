package runtime

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/charmbracelet/x/term"

	"github.com/bevicted/lognav/internal/logging"
)

// A panic escaping a goroutine kills the process before any terminal teardown
// runs, so the TUI's raw mode, alt-screen and mouse tracking survive it: the
// trace prints as an unreadable staircase (no CR on newline) and the shell that
// inherits the terminal keeps echoing SGR mouse reports until the user resets
// it. The trace itself only ever reaches stderr, never the log file, so a bug
// report arrives with a session log that simply stops mid-line. These two guards
// close both gaps.
//
// NOTE: recover() catches panics ONLY. Runtime throws — "concurrent map
// iteration and map write", "out of memory", a cgo SIGSEGV — are fatal by
// design: no deferred function runs, so neither guard fires and the terminal is
// still left dirty. That is a Go-level guarantee we cannot work around here; the
// guards narrow the blast radius, they do not eliminate it.

// logPanic writes a recovered panic and its originating stack to the log file.
// debug.Stack() is called from inside the deferred recover, where the panicking
// frames are still on the stack, so the logged trace reaches the real fault site
// rather than stopping at the guard.
func logPanic(logger *slog.Logger, where string, r any) {
	stack := string(debug.Stack())
	// A directly-constructed runtime/poster (tests) may leave logger nil; a guard
	// that itself nil-derefs would replace the reported panic with its own.
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("recovered panic",
		logging.KeyComponent, "runtime",
		"where", where,
		"panic", fmt.Sprint(r),
		"stack", stack,
	)
}

// recoverLoop guards the loop goroutine. It restores the terminal BEFORE
// re-panicking so the trace prints legibly and the shell is not left in raw
// mode with mouse tracking on, and records the panic in the log file so the
// session log ends with the fault instead of stopping mid-line. It deliberately
// re-panics: the loop goroutine owns all UI state, and continuing on top of a
// half-updated model would trade a visible crash for silent corruption.
func (rt *runtime) recoverLoop() {
	r := recover()
	if r == nil {
		return
	}
	// Terminal first: everything after this needs a sane terminal to be readable.
	rt.exitModes()
	if rt.prevState != nil {
		_ = term.Restore(os.Stdin.Fd(), rt.prevState)
	}
	logPanic(rt.logger, "loop goroutine", r)
	panic(r)
}
