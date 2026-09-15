package msgs

import (
	"log/slog"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/bevicted/lognav/internal/logging"
)

// LogStreamMsg is sent on each OnData() callback with the logs, warnings,
// and errors parsed from that single invocation.
type LogStreamMsg struct {
	CRN   string
	ID    uint64
	Logs  []icl.Log
	Warns []string
	Errs  []string
}

// LogStreamDoneMsg is sent on OnClose() or a terminal OnError() to signal
// that the stream has ended.
type LogStreamDoneMsg struct {
	CRN  string
	ID   uint64
	Errs []string
}

// streamCallback implements icl.QueryCallback, forwarding each callback
// as a uv.Event via the send function.
// done ensures that only one LogStreamDoneMsg is sent per stream, even when
// both OnError and OnClose fire (e.g. on context cancellation).
type streamCallback struct {
	crn         string
	displayName string
	id          uint64
	send        func(uv.Event)
	done        sync.Once
}

// NewStreamCallback returns a new streamCallback that routes events by CRN and
// logs with the separately captured display name.
func NewStreamCallback(crn, displayName string, id uint64, send func(uv.Event)) *streamCallback {
	return &streamCallback{crn: crn, displayName: displayName, id: id, send: send}
}

func (s *streamCallback) OnKeepAlive() {
	logger := slog.Default().With(logging.KeyComponent, "logstream", logging.KeyInstance, s.displayName)
	logger.Debug("keepalive received")
}

func (s *streamCallback) OnClose() {
	logger := slog.Default().With(logging.KeyComponent, "logstream", logging.KeyInstance, s.displayName)
	logger.Debug("stream closed")
	s.done.Do(func() {
		s.send(&LogStreamDoneMsg{CRN: s.crn, ID: s.id})
	})
}

func (s *streamCallback) OnError(err error) {
	logger := slog.Default().With(logging.KeyComponent, "logstream", logging.KeyInstance, s.displayName)
	logger.Error("stream errored", logging.KeyError, err)
	s.done.Do(func() {
		s.send(&LogStreamDoneMsg{CRN: s.crn, ID: s.id, Errs: []string{err.Error()}})
	})
}

func (s *streamCallback) OnData(item *icl.StreamItem) {
	logs, warns, errs := icl.DecodeStreamItem(item)
	logger := slog.Default().With(logging.KeyComponent, "logstream", logging.KeyInstance, s.displayName)
	logger.Debug("stream data received", logging.KeyCount, len(logs))
	s.send(&LogStreamMsg{CRN: s.crn, ID: s.id, Logs: logs, Warns: warns, Errs: errs})
}
