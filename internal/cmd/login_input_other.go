//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
)

// readMaskedLoginPasscode delegates to the platform terminal implementation
// where cancellable terminal input is unavailable. Unix and Windows builds use
// their respective cancellable implementations.
func readMaskedLoginPasscode(context.Context) ([]byte, error) {
	passcode, err := term.ReadPassword(os.Stdin.Fd())
	if err != nil {
		return nil, fmt.Errorf("read passcode input: %w", err)
	}
	return passcode, nil
}
