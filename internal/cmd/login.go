package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
)

var (
	errLoginInterrupted    = errors.New("interrupted")
	newLoginAccountManager = icl.NewAccountManager
	loginSessionPath       = icl.SessionPath
	loadLoginSession       = icl.LoadSession
	saveLoginSession       = icl.SaveSession
	readLoginPasscode      = readMaskedLoginPasscode
	loginGetenv            = os.Getenv
)

// initLogin builds the standalone IAM authentication command.
func initLogin(loadBundle func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	var environments []string
	var noOpen bool

	cmd := &cobra.Command{
		Use:     "login",
		Short:   "Sign in with configured IBM Cloud IAM credentials",
		Long:    "Authenticate with the same credential chain as the TUI and save any refresh token for future TUI and headless queries. The order is saved refresh token, an API key selected by the environment's configured environment variable, configured API key, configured 1Password reference, then the interactive browser/passcode flow. A successful login reports which credential source was used. An IBM Cloud CLI session is not used. Terminal stdin is required only when the chain reaches the passcode flow; passcodes are read with echo disabled and cannot be passed as an argument or redirected.\n\nWithout an environment selector, login uses `bluemix`. Duplicate configured environments are ignored in first-seen order. Each successful environment is saved immediately in the plaintext session file, while tokens for unselected environments remain; concurrent lognav processes can overwrite one another's updates. When a passcode is required, its URL is printed and, when `core.openBrowser` is enabled, opened by default; `--no-open` overrides the config and browser-launch failure is a warning.\n\nIf IAM rejects a saved refresh token, lognav clears it and continues to a configured credential. A configured credential failure returns immediately. `lognav query` uses the same noninteractive sources but never prompts.",
		Example: "  lognav login\n  lognav login --environment my-cloud --no-open\n  lognav login --environment my-cloud --environment bluemix",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			selected, err := loginEnvironments(environments, bundle.Config.ICL.Environments)
			if err != nil {
				return err
			}
			return runLogin(ctxOrBackground(cmd.Context()), cmd, selected, bundle.Config.Core.OpenBrowser && !noOpen, bundle.Config.ICL.Environments)
		},
	}
	cmd.ValidArgsFunction = cobra.NoFileCompletions
	cmd.Flags().StringArrayVarP(&environments, "environment", "e", nil, "configured IAM environment (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("environment", func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		bundle, err := loadBundle(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return configuredEnvironmentNames(bundle.Config.ICL.Environments), cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().BoolVarP(&noOpen, "no-open", "n", false, "Do not open the passcode URL in a browser")
	return cmd
}

// runLogin loads the existing session once, then checkpoints each successful
// environment so a later failure cannot discard an earlier login.
func runLogin(ctx context.Context, cmd *cobra.Command, selected []icl.Environment, shouldOpenBrowser bool, environments map[string]config.ICLEnvironmentConfig) error {
	manager := newLoginAccountManager(environments)
	for cname, environment := range environments {
		if key, ok := environment.APIKeyEnvOverride(loginGetenv); ok {
			manager.SetAPIKey(icl.Environment(cname), key, environment.APIKeyEnvVar)
		}
	}
	sessionPath, err := loginSessionPath()
	if err != nil {
		return fmt.Errorf("resolve authentication session: %w", err)
	}
	tokens, err := loadLoginSession(sessionPath)
	if err != nil {
		return fmt.Errorf("load authentication session: %w", err)
	}
	manager.SetRefreshTokens(tokens)

	for _, env := range selected {
		if err := loginEnvironment(ctx, cmd, manager, sessionPath, env, shouldOpenBrowser); err != nil {
			return err
		}
	}
	return nil
}

// loginEnvironment resolves the configured credential chain, falling back to
// an interactive passcode, and immediately persists the merged refresh tokens.
func loginEnvironment(ctx context.Context, cmd *cobra.Command, manager *icl.AccountManager, sessionPath string, env icl.Environment, shouldOpenBrowser bool) error {
	logger := slog.Default().With("component", "login", "event", "standalone_login", "environment", env)
	if err := ctx.Err(); err != nil {
		return err
	}
	logger.Debug("standalone login checkpoint", "operation", "authentication", "stage", "started")
	method, err := manager.AuthenticateEnvironment(ctx, env)
	if err == nil {
		logger.Debug("standalone login checkpoint", "operation", "authentication", "stage", "succeeded", "method", method)
	} else {
		var passcodeRequired *icl.PasscodeRequired
		if !errors.As(err, &passcodeRequired) {
			return loginIAMError(ctx, env, "IAM authentication", "authentication", err)
		}
		logger.Debug("standalone login checkpoint", "operation", "authentication", "stage", "passcode_required")
		if err := loginWithPasscode(ctx, cmd, manager, env, passcodeRequired.GetPasscodeURL(), shouldOpenBrowser); err != nil {
			return err
		}
		method = icl.AuthenticationMethodPasscode
	}
	logger.Debug("standalone login checkpoint", "operation", "session_save", "stage", "started")
	if err := saveLoginSession(sessionPath, manager.GetRefreshTokens()); err != nil {
		logger.Debug("standalone login checkpoint", "operation", "session_save", "stage", "failed")
		return fmt.Errorf("save authentication session: %w", err)
	}
	logger.Debug("standalone login checkpoint", "operation", "session_save", "stage", "succeeded")
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s via %s.\n", env, method)
	return nil
}

func loginWithPasscode(ctx context.Context, cmd *cobra.Command, manager *icl.AccountManager, env icl.Environment, passcodeURL string, shouldOpenBrowser bool) error {
	if !isTTY() {
		return WithExit(ExitUsage, errors.New("login requires an interactive terminal when passcode authentication is required"))
	}
	fmt.Fprintln(cmd.OutOrStdout(), sanitizeStderrText(passcodeURL))
	if shouldOpenBrowser {
		if err := openBrowser(ctx, passcodeURL); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: could not open browser; open the URL manually.")
		}
	}

	fmt.Fprint(cmd.OutOrStdout(), "Passcode: ")
	passcode, err := readLoginPasscode(ctx)
	fmt.Fprintln(cmd.OutOrStdout())
	if err != nil {
		return loginPasscodeError(err)
	}
	logger := slog.Default().With("component", "login", "event", "standalone_login", "environment", env)
	logger.Debug("standalone login checkpoint", "operation", "passcode_input", "stage", "raw_received", "byte_length", len(passcode), "rune_length", utf8.RuneCount(passcode))
	framingRemoved := hasCompleteLoginPasteFrame(passcode)
	normalizedPasscode := normalizeLoginPasscode(passcode)
	logger.Debug("standalone login checkpoint", "operation", "passcode_input", "stage", "normalized", "byte_length", len(normalizedPasscode), "rune_length", utf8.RuneCountInString(normalizedPasscode), "bracketed_framing_removed", framingRemoved)
	logger.Debug("standalone login checkpoint", "operation", "passcode_exchange", "stage", "started")
	if err := manager.SetPasscode(ctx, env, normalizedPasscode); err != nil {
		return loginIAMError(ctx, env, "IAM passcode exchange", "passcode_exchange", err)
	}
	logger.Debug("standalone login checkpoint", "operation", "passcode_exchange", "stage", "succeeded")
	return nil
}

// loginPasscodeError prevents reader details from exposing a passcode while
// preserving cancellation and the terminal Ctrl+C exit contract.
func loginPasscodeError(err error) error {
	if errors.Is(err, errLoginInterrupted) {
		return WithExit(130, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("read passcode from terminal")
}

// loginEnvironments applies the production default and preserves first use of
// each configured environment.
func loginEnvironments(values []string, environments map[string]config.ICLEnvironmentConfig) ([]icl.Environment, error) {
	if len(values) == 0 {
		if _, ok := environments[string(icl.EnvProd)]; !ok {
			return nil, WithExit(ExitUsage, errors.New("production IAM environment is not configured"))
		}
		return []icl.Environment{icl.EnvProd}, nil
	}

	selected := make([]icl.Environment, 0, len(values))
	seen := make(map[icl.Environment]struct{}, len(values))
	for _, value := range values {
		env := icl.Environment(value)
		if _, ok := environments[value]; !ok {
			return nil, WithExit(ExitUsage, fmt.Errorf("invalid login environment %q", value))
		}
		if _, ok := seen[env]; ok {
			continue
		}
		seen[env] = struct{}{}
		selected = append(selected, env)
	}
	return selected, nil
}

func configuredEnvironmentNames(environments map[string]config.ICLEnvironmentConfig) []string {
	values := make([]string, 0, len(environments))
	for cname := range environments {
		values = append(values, cname)
	}
	slices.Sort(values)
	return values
}

// loginIAMError keeps untrusted IAM response text, which can conceivably echo a
// submitted passcode, out of command errors and logs only static diagnostics.
func loginIAMError(ctx context.Context, env icl.Environment, operation, checkpoint string, err error) error {
	slog.Default().With("component", "login", "event", "standalone_login", "environment", env).
		Debug("standalone login checkpoint", "operation", checkpoint, "stage", "failed")
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return WithExit(ExitUnavailable, errors.New(operation+" failed"))
}
