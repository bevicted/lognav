package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/icl"
)

var (
	errLoginInterrupted    = errors.New("interrupted")
	newLoginAccountManager = icl.NewPasscodeAccountManager
	loginSessionPath       = icl.SessionPath
	loadLoginSession       = icl.LoadSession
	saveLoginSession       = icl.SaveSession
	readLoginPasscode      = readMaskedLoginPasscode
)

// initLogin builds the standalone browser/passcode SSO command. It loads IAM
// discovery endpoints from config but omits configured API keys and 1Password refs.
func initLogin(loadBundle func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	var environments []string
	var noOpen bool

	cmd := &cobra.Command{
		Use:     "login",
		Short:   "Sign in with an IBM Cloud IAM passcode",
		Long:    "Start the standalone IBM Cloud IAM browser/passcode flow and save its refresh token for future TUI and headless queries. It uses configured IAM discovery endpoints but deliberately omits configured API keys, 1Password references, and an IBM Cloud CLI session. Terminal stdin is required: passcodes are read with echo disabled and cannot be passed as an argument or redirected.\n\nWithout an environment selector, login uses `bluemix`. Duplicate configured environments are ignored in first-seen order. Each successful environment is saved immediately in the plaintext session file, while tokens for unselected environments remain; concurrent lognav processes can overwrite one another's updates. The passcode URL is printed and opened by default; browser-launch failure is a warning.\n\nCredential order for fetches is cached access token, then saved refresh token, then configured API key or 1Password reference. If IAM rejects a saved refresh token, lognav clears it and continues to a configured credential. A configured credential failure returns immediately. The TUI may request a passcode; `lognav query` never prompts.",
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
			return runLogin(ctxOrBackground(cmd.Context()), cmd, selected, noOpen, bundle.Config.ICL.Environments)
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
func runLogin(ctx context.Context, cmd *cobra.Command, selected []icl.Environment, noOpen bool, environments map[string]config.ICLEnvironmentConfig) error {
	if !isTTY() {
		return WithExit(ExitUsage, errors.New("login requires an interactive terminal"))
	}

	manager := newLoginAccountManager(environments)
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
		if err := loginEnvironment(ctx, cmd, manager, sessionPath, env, noOpen); err != nil {
			return err
		}
	}
	return nil
}

// loginEnvironment completes one browser/passcode exchange and immediately
// persists the manager's merged refresh-token map.
func loginEnvironment(ctx context.Context, cmd *cobra.Command, manager *icl.AccountManager, sessionPath string, env icl.Environment, noOpen bool) error {
	logger := slog.Default().With("component", "login", "event", "standalone_login", "environment", env)
	if err := ctx.Err(); err != nil {
		return err
	}
	logger.Debug("standalone login checkpoint", "operation", "passcode_discovery", "stage", "started")
	passcodeURL, err := manager.DiscoverPasscodeURL(ctx, env)
	if err != nil {
		return loginIAMError(ctx, env, "IAM passcode discovery", "passcode_discovery", err)
	}
	logger.Debug("standalone login checkpoint", "operation", "passcode_discovery", "stage", "succeeded")
	fmt.Fprintln(cmd.OutOrStdout(), sanitizeStderrText(passcodeURL))

	if !noOpen {
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
	logger.Debug("standalone login checkpoint", "operation", "passcode_input", "stage", "raw_received", "byte_length", len(passcode), "rune_length", utf8.RuneCount(passcode))
	framingRemoved := hasCompleteLoginPasteFrame(passcode)
	normalizedPasscode := normalizeLoginPasscode(passcode)
	logger.Debug("standalone login checkpoint", "operation", "passcode_input", "stage", "normalized", "byte_length", len(normalizedPasscode), "rune_length", utf8.RuneCountInString(normalizedPasscode), "bracketed_framing_removed", framingRemoved)
	logger.Debug("standalone login checkpoint", "operation", "passcode_exchange", "stage", "started")
	if err := manager.SetPasscode(ctx, env, normalizedPasscode); err != nil {
		return loginIAMError(ctx, env, "IAM passcode exchange", "passcode_exchange", err)
	}
	logger.Debug("standalone login checkpoint", "operation", "passcode_exchange", "stage", "succeeded")
	logger.Debug("standalone login checkpoint", "operation", "session_save", "stage", "started")
	if err := saveLoginSession(sessionPath, manager.GetRefreshTokens()); err != nil {
		logger.Debug("standalone login checkpoint", "operation", "session_save", "stage", "failed")
		return fmt.Errorf("save authentication session: %w", err)
	}
	logger.Debug("standalone login checkpoint", "operation", "session_save", "stage", "succeeded")
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s.\n", env)
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
