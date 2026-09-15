package msgs

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bevicted/lognav/internal/icl"
)

func TestStreamCallback_OnErrorThenOnClose_SendsSingleDone(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var dones, datas int
	send := func(ev uv.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.(type) {
		case *LogStreamDoneMsg:
			dones++
		case *LogStreamMsg:
			datas++
		}
	}

	cb := NewStreamCallback("inst", "inst", 1, send)
	cb.OnData(&icl.StreamItem{})  // empty item -> one LogStreamMsg, zero logs
	cb.OnError(streamTestError{}) // first terminal signal
	cb.OnClose()                  // must be deduped by sync.Once

	assert.Equal(t, 1, datas)
	require.Equal(t, 1, dones, "OnError+OnClose must yield exactly one LogStreamDoneMsg")
}

func TestStreamCallback_LogsDisplayNameAndRoutesCRN(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	const (
		crn         = "crn:v1:bluemix:public:logs:us-south:a/account:instance::"
		displayName = "production-logs"
	)
	var events []uv.Event
	cb := NewStreamCallback(crn, displayName, 7, func(ev uv.Event) { events = append(events, ev) })
	cb.OnError(streamTestError{})

	require.Len(t, events, 1)
	done, ok := events[0].(*LogStreamDoneMsg)
	require.True(t, ok)
	assert.Equal(t, crn, done.CRN)
	assert.Contains(t, buf.String(), "instance="+displayName)
	assert.NotContains(t, buf.String(), crn)
}

type streamTestError struct{}

func (streamTestError) Error() string { return "boom" }
