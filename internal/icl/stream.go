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

// streamEvents parses a /v1/query Server-Sent-Events body from r, driving cb.
// It mirrors the retired SDK's readEventLoop framing exactly:
//   - lines starting with ":" are comments (ignored),
//   - "data: " lines accumulate (with their trailing newline) into the event
//     buffer,
//   - a blank line flushes the event: empty buffer => OnKeepAlive, otherwise
//     the buffer is JSON-decoded to a StreamItem and passed to OnData,
//   - io.EOF ends the stream cleanly,
//   - any other read error, or an undecodable/unknown line, calls OnError.
//
// streamEvents does NOT call OnClose; the caller (Query) is responsible for it
// so it fires on every termination path. The loop returns when the stream
// ends. A done context returns promptly between lines.
func streamEvents(ctx context.Context, r io.Reader, cb QueryCallback) {
	reader := bufio.NewReader(r)
	var buf bytes.Buffer

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return
		}
		if err != nil {
			cb.OnError(err)
			return
		}

		switch {
		case bytes.HasPrefix(line, []byte(":")):
			// comment / ": success" — ignore
		case bytes.HasPrefix(line, []byte("data: ")):
			buf.Write(bytes.TrimPrefix(line, []byte("data: ")))
		case bytes.Equal(line, []byte("\n")):
			if buf.Len() == 0 {
				cb.OnKeepAlive()
				continue
			}
			var item StreamItem
			if err := jsonutil.API.Unmarshal(buf.Bytes(), &item); err != nil {
				cb.OnError(err)
				return
			}
			buf.Reset()
			cb.OnData(&item)
		default:
			cb.OnError(fmt.Errorf("unknown SSE line: %q", line))
			return
		}
	}
}
