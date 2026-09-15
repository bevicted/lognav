//go:build windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/windows"
)

const (
	loginPasscodeMaxBytes       = 1024
	loginPasscodePollIntervalMS = 10
)

// readMaskedLoginPasscode reads masked terminal input while polling ctx. A
// Windows console input handle is waitable, so it can avoid a blocked reader
// goroutine and promptly return after SIGTERM or SIGINT cancellation.
func readMaskedLoginPasscode(ctx context.Context) (result []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read passcode input: %w", err)
	}

	fd := os.Stdin.Fd()
	previous, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("set passcode terminal mode: %w", err)
	}
	defer func() {
		if restoreErr := term.Restore(fd, previous); err == nil && restoreErr != nil {
			result = nil
			err = fmt.Errorf("restore terminal: %w", restoreErr)
		}
	}()

	handle := windows.Handle(fd)
	for {
		input, err := readWindowsLoginPasscodeInput(ctx, handle)
		if err != nil {
			return nil, fmt.Errorf("read passcode input: %w", err)
		}
		result, done, err := appendWindowsLoginPasscodeInput(result, input)
		if err != nil {
			return nil, fmt.Errorf("process passcode input: %w", err)
		}
		if done {
			return result, nil
		}
	}
}

// readWindowsLoginPasscodeInput waits for a console input event before reading
// one UTF-16 code unit. The bounded wait keeps context cancellation observable
// without leaving a blocked read goroutine behind.
func readWindowsLoginPasscodeInput(ctx context.Context, handle windows.Handle) (string, error) {
	if waitErr := waitForWindowsLoginPasscodeInput(ctx, handle, windows.WaitForSingleObject); waitErr != nil {
		err := fmt.Errorf("wait for passcode input: %w", waitErr)
		return "", err
	}

	var input uint16
	var read uint32
	if readErr := windows.ReadConsole(handle, &input, 1, &read, nil); readErr != nil {
		err := fmt.Errorf("read passcode input: %w", readErr)
		return "", err
	}
	if read == 0 {
		err := errors.New("read passcode input: end of file")
		return "", err
	}
	value := windows.UTF16ToString([]uint16{input})
	return value, nil
}

// waitForWindowsLoginPasscodeInput polls the waitable console input handle so
// the command observes signal cancellation even while no key has been pressed.
func waitForWindowsLoginPasscodeInput(ctx context.Context, handle windows.Handle, wait func(windows.Handle, uint32) (uint32, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := wait(handle, loginPasscodePollIntervalMS)
		if err != nil {
			waitErr := fmt.Errorf("wait for passcode input: %w", err)
			return waitErr
		}
		switch ready {
		case windows.WAIT_OBJECT_0:
			return nil
		case uint32(windows.WAIT_TIMEOUT):
			continue
		default:
			waitErr := fmt.Errorf("wait for passcode input: unexpected result %d", ready)
			return waitErr
		}
	}
}

// appendWindowsLoginPasscodeInput implements the raw-line editing contract
// while retaining the UTF-8 encoding Windows returns for ordinary characters.
func appendWindowsLoginPasscodeInput(result []byte, input string) ([]byte, bool, error) {
	switch input {
	case "\r", "\n":
		return result, true, nil
	case "\x03":
		return nil, false, errLoginInterrupted
	case "\b", "\x7f":
		if len(result) > 0 {
			result = result[:len(result)-1]
		}
		return result, false, nil
	default:
		if len(result)+len(input) > loginPasscodeMaxBytes {
			return nil, false, errors.New("passcode is too long")
		}
		return append(result, input...), false, nil
	}
}
