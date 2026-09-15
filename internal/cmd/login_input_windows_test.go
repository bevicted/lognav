//go:build windows

package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestWaitForWindowsLoginPasscodeInput_Cancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var waits int
	err := waitForWindowsLoginPasscodeInput(ctx, 0, func(windows.Handle, uint32) (uint32, error) {
		waits++
		cancel()
		return uint32(windows.WAIT_TIMEOUT), nil
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, waits)
}

func TestAppendWindowsLoginPasscodeInput(t *testing.T) {
	t.Parallel()

	result, done, err := appendWindowsLoginPasscodeInput([]byte("pass"), "\b")
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, []byte("pas"), result)

	result, done, err = appendWindowsLoginPasscodeInput(result, "\r")
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, []byte("pas"), result)
}
