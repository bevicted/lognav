package icl

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/bevicted/lognav/internal/jsonutil"
)

// QueryCallback receives streaming Dataprime query events. It mirrors the
// retired logs-go-sdk QueryCallBack, except OnData receives a decoded
// *StreamItem instead of the SDK's *core.DetailedResponse.
type QueryCallback interface {
	OnData(*StreamItem) // one decoded "data:" event
	OnClose()           // stream ended (EOF, ctx-cancel, or after OnError)
	OnError(error)      // a receive/parse error (OnClose still follows)
	OnKeepAlive()       // server keep-alive (empty event)
}

// readSSEEvents parses Server-Sent Events and passes each complete data payload to
// onData. Returning false from onData stops successfully. A done context returns
// promptly between lines.
func readSSEEvents(ctx context.Context, r io.Reader, onData func([]byte) (bool, error), onKeepAlive func()) error {
	reader := bufio.NewReader(r)
	var buf bytes.Buffer

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		switch {
		case bytes.HasPrefix(line, []byte(":")):
			// SSE comment, including keep-alive markers such as ": success".
		case bytes.HasPrefix(line, []byte("data: ")):
			buf.Write(bytes.TrimPrefix(line, []byte("data: ")))
		case bytes.Equal(line, []byte("\n")):
			if buf.Len() == 0 {
				onKeepAlive()
				continue
			}
			keepReading, err := onData(buf.Bytes())
			if err != nil {
				return err
			}
			buf.Reset()
			if !keepReading {
				return nil
			}
		default:
			return fmt.Errorf("unknown SSE line: %q", line)
		}
	}
}

// streamEvents parses a /v1/query SSE body and drives cb. It does not call OnClose;
// Query owns that lifecycle callback.
func streamEvents(ctx context.Context, r io.Reader, cb QueryCallback) {
	err := readSSEEvents(ctx, r, func(data []byte) (bool, error) {
		var item StreamItem
		if err := jsonutil.API.Unmarshal(data, &item); err != nil {
			return false, err
		}
		cb.OnData(&item)
		return true, nil
	}, cb.OnKeepAlive)
	if err != nil {
		cb.OnError(err)
	}
}
