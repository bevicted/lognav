package cmd

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/icl"
)

// This test replaces command-wide seams and uses the managed snapshot directory.
func TestQuery_StderrTerminalDoesNotAffectStdinOrStdout(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldTTY, oldCaps, oldStream, oldResolver := isTTY, queryStderrCapabilities, queryStream, resolveQueryToken
	defer func() {
		isTTY, queryStderrCapabilities, queryStream, resolveQueryToken = oldTTY, oldCaps, oldStream, oldResolver
	}()
	isTTY = func() bool { return false }
	queryStderrCapabilities = func(io.Writer) queryTerminalCapabilities {
		return queryTerminalCapabilities{Repaint: true, Color: true}
	}
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = queryOneLog

	var stdout bytes.Buffer
	var stderr progressBuffer
	require.NoError(t, runQuery(t.Context(), &stdout, &stderr, cfg, cfg.ICL.Instances, "q"))
	assert.NotContains(t, stdout.String(), "\x1b", "selector output remains machine-readable when stderr is a terminal")
	assert.NotEmpty(t, stdout.String())
	assert.Contains(t, stderr.String(), "\x1b[", "stderr repaint is selected independently from redirected stdin/stdout")
}

// This test replaces command-wide seams and uses the managed snapshot directory.
func TestQuery_TeeSuppressesProgressWhenStderrIsTerminal(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	cfg.Style.AuthInProgressLabel = "AUTH-MARKER"
	cfg.Style.InProgressLabel = "FETCH-MARKER"
	cfg.Style.SuccessLabel = "DONE-MARKER"
	oldCaps, oldStream, oldResolver := queryStderrCapabilities, queryStream, resolveQueryToken
	defer func() { queryStderrCapabilities, queryStream, resolveQueryToken = oldCaps, oldStream, oldResolver }()
	queryStderrCapabilities = func(io.Writer) queryTerminalCapabilities {
		return queryTerminalCapabilities{Repaint: true, Color: true}
	}
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	queryStream = queryOneLog

	var stdout bytes.Buffer
	var stderr progressBuffer
	require.NoError(t, runQueryWithOptions(t.Context(), &stdout, &stderr, cfg, cfg.ICL.Instances, "q", queryRunOptions{tee: true}))
	assertNDJSONMessages(t, stdout.String(), []string{"ok"})
	assert.Equal(t, "1 logs; snapshot retained\n", stderr.String())
}

type blockingProgressWriter struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingProgressWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.reached) })
	<-w.release
	return len(p), nil
}

// This test replaces command-wide seams and uses the managed snapshot directory.
func TestQuery_SlowProgressWriterDoesNotBlockCancellationWorkers(t *testing.T) { //nolint:paralleltest // command seams and XDG are process-wide
	setQueryTestXDG(t)
	cfg := queryTestConfig()
	oldCaps, oldStream, oldResolver := queryStderrCapabilities, queryStream, resolveQueryToken
	defer func() { queryStderrCapabilities, queryStream, resolveQueryToken = oldCaps, oldStream, oldResolver }()
	queryStderrCapabilities = func(io.Writer) queryTerminalCapabilities { return queryTerminalCapabilities{} }
	resolveQueryToken = func(context.Context, *icl.AccountManager, *config.CRN) (string, error) { return "token", nil }
	streamStarted := make(chan struct{})
	streamStopped := make(chan struct{})
	queryStream = func(ctx context.Context, _, _, _ string, _ uint32, _ icl.QueryCallback) error {
		close(streamStarted)
		<-ctx.Done()
		close(streamStopped)
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &blockingProgressWriter{reached: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- runQuery(ctx, io.Discard, stderr, cfg, cfg.ICL.Instances, "q") }()
	<-streamStarted
	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-streamStopped:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	<-stderr.reached
	select {
	case err := <-done:
		t.Fatalf("query returned while its deliberately blocked diagnostic writer was still blocked: %v", err)
	default:
	}
	close(stderr.release)
	require.ErrorIs(t, <-done, context.Canceled)
}
