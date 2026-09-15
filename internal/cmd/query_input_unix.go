//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readQueryFile waits in short poll intervals rather than blocking in Read.
// stdin is never closed, and only this function reads it, so readiness remains
// valid for the following Read call.
func readQueryFile(ctx context.Context, file *os.File) ([]byte, error) {
	fd, err := fileFd(file)
	if err != nil {
		return nil, fmt.Errorf("inspect query input: %w", err)
	}
	var data []byte
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 10)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return nil, fmt.Errorf("wait for query input: %w", err)
		}
		if ready == 0 {
			continue
		}
		n, err := file.Read(buf)
		data = append(data, buf[:n]...)
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		return nil, fmt.Errorf("read query input: %w", err)
	}
}

func fileFd(file *os.File) (uintptr, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var fd uintptr
	if err := conn.Control(func(value uintptr) { fd = value }); err != nil {
		return 0, err
	}
	return fd, nil
}
