package msgs

import (
	"errors"
	"os/exec"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

type execResult struct{ err error }

func TestExecRequest_DoneRoundTrips(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	cmd := exec.CommandContext(t.Context(), "true")
	req := ExecRequest{
		Cmd:  cmd,
		Done: func(err error) uv.Event { return execResult{err} },
	}
	require.Same(t, cmd, req.Cmd)
	require.NotNil(t, req.Done)
	got, ok := req.Done(want).(execResult)
	require.True(t, ok)
	require.ErrorIs(t, got.err, want)
}
