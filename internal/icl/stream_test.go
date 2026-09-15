package icl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordCB records every QueryCallback invocation for assertions. The mutex
// makes it safe for the streaming Query goroutine to write while a test
// goroutine reads via the accessors (see the ctx-cancel test in query_test.go).
type recordCB struct {
	mu        sync.Mutex
	data      []*StreamItem
	keepalive int
	errs      []error
	closed    int
}

func (r *recordCB) OnData(i *StreamItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, i)
}

func (r *recordCB) OnKeepAlive() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keepalive++
}

func (r *recordCB) OnError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recordCB) OnClose() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
}

// dataLen returns the number of OnData invocations recorded.
func (r *recordCB) dataLen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.data)
}

// dataAt returns the i-th recorded StreamItem.
func (r *recordCB) dataAt(i int) *StreamItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data[i]
}

// keepaliveCount returns the number of OnKeepAlive invocations recorded.
func (r *recordCB) keepaliveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keepalive
}

// errCount returns the number of OnError invocations recorded.
func (r *recordCB) errCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// errAt returns the i-th recorded error.
func (r *recordCB) errAt(i int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errs[i]
}

// closedCount returns the number of OnClose invocations recorded.
func (r *recordCB) closedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// errAfterReader yields data once, then returns err (non-EOF) — simulates a
// transport/ctx-cancel read failure mid-stream.
type errAfterReader struct {
	data []byte
	err  error
	done bool
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	if !e.done {
		e.done = true
		return copy(p, e.data), nil
	}
	return 0, e.err
}

func TestStreamEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sse      string
		wantData int
		wantKeep int
		wantErrs int
		check    func(t *testing.T, cb *recordCB)
	}{
		{
			name:     "result event decodes",
			sse:      ": success\ndata: {\"result\":{\"results\":[{\"user_data\":\"{}\",\"metadata\":[{\"key\":\"logid\",\"value\":\"l1\"}]}]}}\n\n",
			wantData: 1,
			check: func(t *testing.T, cb *recordCB) {
				require.NotNil(t, cb.dataAt(0).Result)
				require.Len(t, cb.dataAt(0).Result.Results, 1)
				assert.Equal(t, "l1", deref(cb.dataAt(0).Result.Results[0].Metadata[0].Value))
			},
		},
		{
			name:     "empty event is keepalive",
			sse:      "\n",
			wantKeep: 1,
		},
		{
			name:     "warning event",
			sse:      "data: {\"warning\":{\"time_range_warning\":{\"warning_message\":\"clipped\"}}}\n\n",
			wantData: 1,
			check: func(t *testing.T, cb *recordCB) {
				require.NotNil(t, cb.dataAt(0).Warning)
				require.NotNil(t, cb.dataAt(0).Warning.TimeRangeWarning)
				assert.Equal(t, "clipped", deref(cb.dataAt(0).Warning.TimeRangeWarning.WarningMessage))
			},
		},
		{
			name:     "inline errors array",
			sse:      "data: {\"errors\":[{\"code\":\"forbidden\",\"message\":\"no\",\"more_info\":\"x\"}]}\n\n",
			wantData: 1,
			check: func(t *testing.T, cb *recordCB) {
				require.Len(t, cb.dataAt(0).Errors, 1)
				assert.Equal(t, "forbidden", deref(cb.dataAt(0).Errors[0].Code))
			},
		},
		{
			name:     "multi-line data reassembled",
			sse:      "data: {\"error\":\ndata: {\"message\":\"boom\"}}\n\n",
			wantData: 1,
			check: func(t *testing.T, cb *recordCB) {
				require.NotNil(t, cb.dataAt(0).Error)
				assert.Equal(t, "boom", deref(cb.dataAt(0).Error.Message))
			},
		},
		{
			name:     "malformed json errors",
			sse:      "data: {not json}\n\n",
			wantErrs: 1,
		},
		{
			name:     "unknown line errors",
			sse:      "bogus line\n",
			wantErrs: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cb := &recordCB{}
			streamEvents(context.Background(), strings.NewReader(tt.sse), cb)
			assert.Equal(t, tt.wantData, cb.dataLen())
			assert.Equal(t, tt.wantKeep, cb.keepaliveCount())
			assert.Equal(t, tt.wantErrs, cb.errCount())
			assert.Equal(t, 0, cb.closedCount(), "streamEvents must not call OnClose")
			if tt.check != nil {
				tt.check(t, cb)
			}
		})
	}
}

func TestStreamEvents_ReadErrorSurfacesViaOnError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("transport boom")
	cb := &recordCB{}
	r := &errAfterReader{data: []byte("data: {\"error\":{\"message\":\"x\"}}"), err: sentinel}
	streamEvents(context.Background(), r, cb)
	require.Equal(t, 1, cb.errCount())
	assert.ErrorIs(t, cb.errAt(0), sentinel)
}

func TestStreamEvents_PreCancelledCtxReturnsClean(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cb := &recordCB{}
	streamEvents(ctx, strings.NewReader("data: {}\n\n"), cb)
	assert.Equal(t, 0, cb.errCount())
	assert.Equal(t, 0, cb.dataLen())
}
