//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
)

// readQueryFile closes command stdin on cancellation where poll(2) is not
// available. The waiter is joined before returning, so it cannot leak.
func readQueryFile(ctx context.Context, file *os.File) ([]byte, error) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = file.Close()
		case <-done:
		}
	}()
	data, err := io.ReadAll(file)
	close(done)
	<-stopped
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("read query input: %w", err)
	}
	return data, nil
}
