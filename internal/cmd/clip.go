package cmd

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/atotto/clipboard"
	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/internal/snapshot"
)

// Seams (package vars) so tests can stub the platform decision, the osascript
// exec, and the text clipboard write.
var (
	clipUsesFileObject = func() bool { return runtime.GOOS == "darwin" }
	runClipCommand     = func(name string, args ...string) error { return exec.Command(name, args...).Run() } // #nosec G204 -- fixed argv; path is data, not script
	clipboardWrite     = clipboard.WriteAll
)

func newSnapshotClip() *cobra.Command {
	var pathOnly bool
	c := &cobra.Command{
		Use:               "clip <name|id-prefix|latest>",
		Short:             "Copy a snapshot to the clipboard for sharing",
		Long:              "On macOS, the default copies the file object, so pasting attaches the file; other platforms copy the absolute path string. Success reports whether a file or path was copied. Clipboard failures exit 1.\n\nExternal paths are not accepted. " + idPrefixHelp,
		Example:           "  lognav snapshot clip incident\n  lognav snapshot clip latest --path",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSnapshotNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := snapshot.ResolveNameOrID(args[0])
			if err != nil {
				return WithExit(ExitNoInput, err)
			}
			out := cmd.OutOrStdout()
			if !pathOnly && clipUsesFileObject() {
				if err := clipFileObject(e.Path); err != nil {
					return WithExit(ExitGeneral, err)
				}
				fmt.Fprintf(out, "Copied %s (file)\n", e.Name)
				return nil
			}
			abs, err := filepath.Abs(e.Path)
			if err != nil {
				return WithExit(ExitGeneral, err)
			}
			if err := clipboardWrite(abs); err != nil {
				return WithExit(ExitGeneral, err)
			}
			fmt.Fprintf(out, "Copied %s (path)\n", e.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&pathOnly, "path", "p", false, "copy the file path instead of the file object")
	return c
}

// clipFileObject puts the file at path on the macOS clipboard as a file object.
// The path is passed to osascript as argv (item 1 of argv), NEVER interpolated
// into the -e script source, so a quote-bearing path cannot inject AppleScript /
// `do shell script` on the receiver's machine.
func clipFileObject(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return runClipCommand("osascript",
		"-e", "on run argv",
		"-e", "set the clipboard to POSIX file (item 1 of argv)",
		"-e", "end run",
		abs)
}
