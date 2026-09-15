//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const loginPasscodePollIntervalMS = 10

// readMaskedLoginPasscode reads a canonical terminal line with echo disabled.
// It mirrors term.ReadPassword's terminal state, but polls readiness so signal
// cancellation can return without a blocked reader goroutine.
func readMaskedLoginPasscode(ctx context.Context) (result []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read passcode input: %w", err)
	}

	fd := os.Stdin.Fd()
	previous, err := getLoginPasscodeTermios(int(fd))
	if err != nil {
		return nil, fmt.Errorf("get passcode terminal state: %w", err)
	}
	state := *previous
	state.Lflag &^= unix.ECHO
	state.Lflag |= unix.ICANON | unix.ISIG
	state.Iflag |= unix.ICRNL
	if err := setLoginPasscodeTermios(int(fd), &state); err != nil {
		return nil, fmt.Errorf("set passcode terminal mode: %w", err)
	}
	defer func() {
		if restoreErr := setLoginPasscodeTermios(int(fd), previous); err == nil && restoreErr != nil {
			result = nil
			err = fmt.Errorf("restore passcode terminal: %w", restoreErr)
		}
	}()

	for {
		input, err := readCanonicalLoginPasscodeByte(ctx, fd)
		if err != nil {
			return nil, err
		}
		switch input {
		case '\n':
			return result, nil
		case '\r':
			continue
		case '\b':
			if len(result) > 0 {
				result = result[:len(result)-1]
			}
		default:
			result = append(result, input)
		}
	}
}

// readCanonicalLoginPasscodeByte waits for one completed canonical line byte
// while keeping context cancellation observable between bounded waits.
func readCanonicalLoginPasscodeByte(ctx context.Context, fd uintptr) (byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("read passcode input: %w", err)
		}
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, loginPasscodePollIntervalMS)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return 0, fmt.Errorf("wait for passcode input: %w", err)
		}
		if ready == 0 {
			continue
		}

		var input [1]byte
		n, err := unix.Read(int(fd), input[:])
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return 0, fmt.Errorf("read passcode input: %w", err)
		}
		if n == 0 {
			return 0, errors.New("read passcode input: end of file")
		}
		return input[0], nil
	}
}
