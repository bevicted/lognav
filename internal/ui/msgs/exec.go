package msgs

import (
	"os/exec"

	uv "github.com/charmbracelet/ultraviolet"
)

// ExecRequest asks the ui/runtime spine to suspend the TUI, run an external
// process against the real TTY (e.g. $EDITOR), then resume and dispatch
// Done(err). It replaces the bubbletea exec-process effect, whose message was
// unexported and invisible to the transitional cmd executor. Produced by
// queryeditor.openEditor; consumed by ui/runtime's dispatch on the loop
// goroutine (terminal release/restore must be serialized with the reader and
// screen). Removed in R5 when the executor is deleted.
type ExecRequest struct {
	Cmd  *exec.Cmd
	Done func(err error) uv.Event
}
