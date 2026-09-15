//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const loginInputHelperEnv = "LOGNAV_LOGIN_INPUT_HELPER"

type loginInputHelper struct {
	command  *exec.Cmd
	terminal *os.File
	output   <-chan string
	waitOnce sync.Once
	waitDone chan struct{}
}

func (helper *loginInputHelper) wait() {
	helper.waitOnce.Do(func() {
		_ = helper.command.Wait()
		close(helper.waitDone)
	})
	<-helper.waitDone
}

// TestReadMaskedLoginPasscodeHelper exercises the real Unix terminal reader
// in a subprocess whose standard descriptors are connected to a PTY.
func TestReadMaskedLoginPasscodeHelper(t *testing.T) { //nolint:paralleltest // subprocess replaces command construction
	want, ok := os.LookupEnv(loginInputHelperEnv)
	if !ok {
		return
	}

	newExecuteRoot = func() *cobra.Command {
		root := &cobra.Command{
			Use:           "login-input-helper",
			SilenceErrors: true,
			SilenceUsage:  true,
			Args:          cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				fmt.Fprintln(cmd.OutOrStdout(), "ready")
				passcode, err := readMaskedLoginPasscode(cmd.Context())
				if err != nil {
					return loginPasscodeError(err)
				}
				if got := normalizeLoginPasscode(passcode); got != want {
					return errors.New("terminal reader returned an unexpected passcode")
				}
				return nil
			},
		}
		root.SetIn(os.Stdin)
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		return root
	}
	os.Exit(ExitCode(Execute()))
}

func TestReadMaskedLoginPasscodePTY(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{name: "typing", chunks: strings.Split("typed-code\n", ""), want: "typed-code"},
		{name: "paste", chunks: []string{"paste-code\n"}, want: "paste-code"},
		{name: "bracketed paste", chunks: []string{"\x1b[200~paste-code\x1b[201~\n"}, want: "paste-code"},
		{name: "empty", chunks: []string{"\n"}, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			helper := startLoginInputHelper(t, tt.want)
			for _, chunk := range tt.chunks {
				_, err := io.WriteString(helper.terminal, chunk)
				require.NoError(t, err)
			}
			waitLoginInputHelper(t, helper)

			assert.Equal(t, 0, helper.command.ProcessState.ExitCode())
			assertNoLoginInputEcho(t, <-helper.output, tt.want)
		})
	}
}

func TestReadMaskedLoginPasscodePTYCancellation(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		cancel func(*os.File, *os.Process) error
		code   int
	}{
		{name: "Ctrl+C", cancel: func(terminal *os.File, _ *os.Process) error {
			_, err := terminal.Write([]byte{3})
			return err
		}, code: 130},
		{name: "SIGTERM", cancel: func(_ *os.File, process *os.Process) error {
			return process.Signal(syscall.SIGTERM)
		}, code: 143},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			helper := startLoginInputHelper(t, "")
			require.NoError(t, tt.cancel(helper.terminal, helper.command.Process))
			waitLoginInputHelper(t, helper)

			assert.Equal(t, tt.code, helper.command.ProcessState.ExitCode())
		})
	}
}

func startLoginInputHelper(t *testing.T, want string) *loginInputHelper {
	t.Helper()

	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestReadMaskedLoginPasscodeHelper$") // #nosec G204,G702 -- invokes the current test binary
	child.Env = append(os.Environ(), loginInputHelperEnv+"="+want, "GORACE=atexit_sleep_ms=0")
	terminal, err := pty.Start(child)
	require.NoError(t, err)
	helper := &loginInputHelper{
		command:  child,
		terminal: terminal,
		waitDone: make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = helper.command.Process.Kill()
		helper.wait()
		_ = helper.terminal.Close()
	})

	ready := make(chan error, 1)
	output := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(terminal)
		var captured strings.Builder
		readySent := false
		for {
			line, err := reader.ReadString('\n')
			captured.WriteString(line)
			if !readySent && strings.Contains(line, "ready") {
				ready <- nil
				readySent = true
			}
			if err != nil {
				if !readySent {
					ready <- err
				}
				output <- captured.String()
				return
			}
		}
	}()
	require.Eventually(t, func() bool {
		select {
		case err := <-ready:
			require.NoError(t, err)
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "helper did not reach terminal input")

	helper.output = output
	return helper
}

func waitLoginInputHelper(t *testing.T, helper *loginInputHelper) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		helper.wait()
		close(done)
	}()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "terminal reader did not exit promptly")
}

func assertNoLoginInputEcho(t *testing.T, output, passcode string) {
	t.Helper()
	if passcode != "" {
		assert.NotContains(t, output, passcode)
	}
}
