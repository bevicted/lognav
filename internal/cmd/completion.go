package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const (
	completionInstallFlag = "install"
	completionProfileFlag = "profile"
	completionStartMarker = "# >>> lognav completion >>>"
	completionEndMarker   = "# <<< lognav completion <<<"
)

type completionShell string

const (
	completionBash       completionShell = "bash"
	completionZsh        completionShell = "zsh"
	completionFish       completionShell = "fish"
	completionPowerShell completionShell = "powershell"
)

type completionSystem struct {
	getenv  func(string) string
	homeDir func() (string, error)
}

type profileFile interface {
	io.Writer
	Sync() error
	Close() error
}

type profileFilesystem struct {
	readFile func(string) ([]byte, error)
	mkdirAll func(string, fs.FileMode) error
	openFile func(string, int, fs.FileMode) (profileFile, error)
	chmod    func(string, fs.FileMode) error
}

var defaultCompletionSystem = completionSystem{
	getenv:  os.Getenv,
	homeDir: os.UserHomeDir,
}

var defaultProfileFilesystem = profileFilesystem{
	readFile: os.ReadFile,
	mkdirAll: os.MkdirAll,
	openFile: func(name string, flag int, perm fs.FileMode) (profileFile, error) {
		return os.OpenFile(name, flag, perm) // #nosec G304 -- caller-selected shell profile
	},
	chmod: os.Chmod,
}

// newCompletionCmd builds the config-independent completion command tree.
func newCompletionCmd() *cobra.Command {
	completion := &cobra.Command{
		Use:               "completion [shell]",
		Short:             "Generate or install shell completion",
		Long:              "Without an explicit shell, installation detects Bash, Zsh, or Fish from $SHELL. Bash requires the bash-completion package. PowerShell installation requires --profile because its profile path is not available to lognav. Start a new shell session after installation.",
		Example:           "  lognav completion --install\n  lognav completion zsh --install\n  lognav completion powershell --install --profile \"$PROFILE\"",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			install, err := cmd.Flags().GetBool(completionInstallFlag)
			if err != nil {
				return err
			}
			if !install {
				return cmd.Help()
			}
			return installDetectedCompletion(cmd, defaultCompletionSystem, defaultProfileFilesystem)
		},
	}
	completion.PersistentFlags().BoolP(completionInstallFlag, "i", false, "install completion into the shell profile")
	completion.PersistentFlags().StringP(completionProfileFlag, "p", "", "shell profile path for --install")

	for _, shell := range []completionShell{completionBash, completionZsh, completionFish, completionPowerShell} {
		completion.AddCommand(newCompletionShellCmd(shell))
	}
	return completion
}

// newCompletionShellCmd builds one completion generation or installation leaf.
func newCompletionShellCmd(shell completionShell) *cobra.Command {
	var noDescriptions bool
	cmd := &cobra.Command{
		Use:                   string(shell),
		Short:                 "Generate the autocompletion script for " + string(shell),
		Args:                  cobra.NoArgs,
		DisableFlagsInUseLine: true,
		ValidArgsFunction:     cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			install, err := cmd.Flags().GetBool(completionInstallFlag)
			if err != nil {
				return err
			}
			if install {
				return installCompletion(cmd, shell, defaultCompletionSystem, defaultProfileFilesystem)
			}
			return generateCompletion(cmd.Root(), shell, cmd.OutOrStdout(), noDescriptions)
		},
	}
	cmd.Flags().BoolVarP(&noDescriptions, "no-descriptions", "d", false, "disable completion descriptions")
	return cmd
}

// installDetectedCompletion resolves the shell named by SHELL before installing.
func installDetectedCompletion(cmd *cobra.Command, system completionSystem, filesystem profileFilesystem) error {
	shell, err := detectCompletionShell(system.getenv)
	if err != nil {
		return WithExit(ExitUsage, err)
	}
	return installCompletion(cmd, shell, system, filesystem)
}

// detectCompletionShell recognizes the portable interactive shells that expose
// their executable path through SHELL.
func detectCompletionShell(getenv func(string) string) (completionShell, error) {
	name := filepath.Base(getenv("SHELL"))
	for _, shell := range []completionShell{completionBash, completionZsh, completionFish} {
		if name == string(shell) {
			return shell, nil
		}
	}
	return "", errors.New("cannot detect a supported shell from $SHELL; choose one of: bash, zsh, fish")
}

// installCompletion resolves a profile and appends the shell's integration.
func installCompletion(cmd *cobra.Command, shell completionShell, system completionSystem, filesystem profileFilesystem) error {
	profile, err := cmd.Flags().GetString(completionProfileFlag)
	if err != nil {
		return err
	}
	profile, err = resolveCompletionProfile(shell, profile, system)
	if err != nil {
		return err
	}
	installed, err := installCompletionProfile(filesystem, profile, completionBlock(shell), completionCommand(shell))
	if err != nil {
		return err
	}
	if installed {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Installed lognav completion for %s in %s\n", shell, profile)
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Lognav completion for %s is already installed in %s\n", shell, profile)
	}
	return err
}

// resolveCompletionProfile returns the user-selected or shell-default profile.
func resolveCompletionProfile(shell completionShell, profile string, system completionSystem) (string, error) {
	if profile != "" {
		return filepath.Clean(profile), nil
	}
	if shell == completionPowerShell {
		return "", WithExit(ExitUsage, errors.New("PowerShell installation requires --profile"))
	}
	switch shell {
	case completionBash:
		home, err := completionHome(system)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".bashrc"), nil
	case completionZsh:
		if zshDir := system.getenv("ZDOTDIR"); zshDir != "" {
			return filepath.Join(zshDir, ".zshrc"), nil
		}
		home, err := completionHome(system)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".zshrc"), nil
	case completionFish:
		if configHome := system.getenv("XDG_CONFIG_HOME"); configHome != "" {
			return filepath.Join(configHome, "fish", "config.fish"), nil
		}
		home, err := completionHome(system)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

// completionHome uses HOME when set, otherwise it defers to the OS resolver.
func completionHome(system completionSystem) (string, error) {
	if home := system.getenv("HOME"); home != "" {
		return home, nil
	}
	home, err := system.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("resolve home directory: empty path")
	}
	return home, nil
}

// generateCompletion writes Cobra's completion script for shell.
func generateCompletion(root *cobra.Command, shell completionShell, out io.Writer, noDescriptions bool) error {
	switch shell {
	case completionBash:
		return root.GenBashCompletionV2(out, !noDescriptions)
	case completionZsh:
		if noDescriptions {
			return root.GenZshCompletionNoDesc(out)
		}
		return root.GenZshCompletion(out)
	case completionFish:
		return root.GenFishCompletion(out, !noDescriptions)
	case completionPowerShell:
		if noDescriptions {
			return root.GenPowerShellCompletion(out)
		}
		return root.GenPowerShellCompletionWithDesc(out)
	default:
		return fmt.Errorf("unsupported shell %q", shell)
	}
}

// completionCommand is the unmarked integration command for shell.
func completionCommand(shell completionShell) string {
	switch shell {
	case completionBash, completionZsh:
		return "source <(lognav completion " + string(shell) + ")"
	case completionFish:
		return "lognav completion fish | source"
	case completionPowerShell:
		return "lognav completion powershell | Out-String | Invoke-Expression"
	default:
		return ""
	}
}

// completionBlock returns a newline-terminated, marker-delimited integration.
func completionBlock(shell completionShell) string {
	var integration string
	switch shell {
	case completionZsh:
		integration = "if ! (( $+functions[compdef] )); then\n\tautoload -Uz compinit\n\tcompinit\nfi\n" + completionCommand(shell)
	default:
		integration = completionCommand(shell)
	}
	return completionStartMarker + "\n" + integration + "\n" + completionEndMarker + "\n"
}

// installCompletionProfile appends block unless a complete marked block or an
// exact unmarked integration already exists. It never rewrites existing bytes.
func installCompletionProfile(filesystem profileFilesystem, profile, block, unmarked string) (bool, error) {
	contents, missing, err := readCompletionProfile(filesystem, profile)
	if err != nil {
		return false, err
	}
	if !missing {
		if err := validateCompletionMarkers(string(contents)); err != nil {
			return false, err
		}
		if strings.Contains(string(contents), completionStartMarker) || hasUnmarkedCompletion(string(contents), unmarked) {
			return false, nil
		}
	}
	return appendCompletionBlock(filesystem, profile, contents, missing, block)
}

// readCompletionProfile returns the current profile contents without changing it.
func readCompletionProfile(filesystem profileFilesystem, profile string) ([]byte, bool, error) {
	contents, err := filesystem.readFile(profile)
	if err == nil {
		return contents, false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("read completion profile %s: %w", profile, err)
}

// appendCompletionBlock creates or appends to a profile while preserving existing bytes.
func appendCompletionBlock(filesystem profileFilesystem, profile string, contents []byte, missing bool, block string) (bool, error) {
	if err := filesystem.mkdirAll(filepath.Dir(profile), 0o700); err != nil {
		return false, fmt.Errorf("create completion profile directory: %w", err)
	}
	file, err := filesystem.openFile(profile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return false, fmt.Errorf("open completion profile %s: %w", profile, err)
	}
	if missing {
		if err := filesystem.chmod(profile, 0o600); err != nil {
			return false, errors.Join(fmt.Errorf("set completion profile permissions: %w", err), file.Close())
		}
	}

	prefix := ""
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		prefix = "\n"
	}
	writeErr := writeCompletionBlock(file, prefix+block)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return false, errors.Join(writeErr, closeErr)
	}
	return true, nil
}

// hasUnmarkedCompletion reports whether a profile has the exact integration command.
func hasUnmarkedCompletion(contents, command string) bool {
	for line := range strings.SplitSeq(contents, "\n") {
		if line == command {
			return true
		}
	}
	return false
}

// validateCompletionMarkers refuses incomplete or duplicate marker state.
func validateCompletionMarkers(contents string) error {
	starts := strings.Count(contents, completionStartMarker)
	ends := strings.Count(contents, completionEndMarker)
	if starts == 0 && ends == 0 {
		return nil
	}
	if starts != 1 || ends != 1 || strings.Index(contents, completionStartMarker) > strings.Index(contents, completionEndMarker) {
		return errors.New("completion profile has incomplete markers; repair the lognav completion block manually")
	}
	return nil
}

// writeCompletionBlock writes all bytes and reports short writes.
func writeCompletionBlock(w io.Writer, contents string) error {
	n, err := io.WriteString(w, contents)
	if err != nil {
		return fmt.Errorf("write completion profile: %w", err)
	}
	if n != len(contents) {
		return io.ErrShortWrite
	}
	return nil
}
