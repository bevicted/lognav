// Package openurl launches URLs with the platform's default browser.
package openurl

import (
	"context"
	"os/exec"
	"runtime"
)

// Open launches the platform's default opener for url and waits for it to
// finish handing the URL off. Waiting prevents a caller's command context from
// cancelling the opener before it dispatches the URL.
func Open(ctx context.Context, url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "cmd", []string{"/c", "start", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	return exec.CommandContext(ctx, name, args...).Run() //nolint:gosec // name is one of three hardcoded platform opener paths
}
