package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/bevicted/lognav/internal/build"
	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/snapshot"
	"github.com/bevicted/lognav/internal/state"
	"github.com/bevicted/lognav/internal/ui"
	"github.com/bevicted/lognav/internal/ui/runtime"
	"github.com/spf13/cobra"
)

// usageArgs marks Cobra positional validation failures as command-line usage
// errors without depending on Cobra's error wording.
func usageArgs(validator cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		err := validator(cmd, args)
		if err == nil || ExitCode(err) == ExitUsage {
			return err
		}
		return WithExit(ExitUsage, err)
	}
}

// tagUsageArgs applies usage tagging to every positional validator in a command tree.
func tagUsageArgs(cmd *cobra.Command) {
	if cmd.Args != nil {
		cmd.Args = usageArgs(cmd.Args)
	}
	for _, child := range cmd.Commands() {
		tagUsageArgs(child)
	}
}

// renderExit prints one sanitized error and, for explicit usage errors, the
// selected command's full usage. Cobra's silence settings make this the sole
// production rendering boundary.
func renderExit(w io.Writer, cmd *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	fmt.Fprintln(w, "Error:", sanitizeStderrError(err.Error()))
	if ExitCode(err) == ExitUsage && cmd != nil {
		fmt.Fprint(w, cmd.UsageString())
	}
	return err
}

const (
	adoptFlag    = "adopt"
	noConfigFlag = "no-config"
	versionFlag  = "version"

	retiredCommand = "m" + "cp"
)

// bundleLoader loads one bundle lazily for one root command tree. It retains
// the first result so config-dependent commands and completion callbacks share
// a single setup operation.
type bundleLoader struct {
	setup  func(noConfig bool) (deps.Bundle, error)
	once   sync.Once
	bundle deps.Bundle
	err    error
}

func newBundleLoader(setup func(noConfig bool) (deps.Bundle, error)) *bundleLoader {
	return &bundleLoader{setup: setup}
}

// Load reads the inherited --no-config flag and initializes the bundle once.
func (l *bundleLoader) Load(cmd *cobra.Command) (deps.Bundle, error) {
	l.once.Do(func() {
		var noConfig bool
		noConfig, l.err = cmd.Flags().GetBool(noConfigFlag)
		if l.err != nil {
			return
		}
		l.bundle, l.err = l.setup(noConfig)
	})
	return l.bundle, l.err
}

// maxLogFiles returns the configured cleanup limit only after successful setup.
func (l *bundleLoader) maxLogFiles() uint8 {
	return bundleMaxLogFiles(l.bundle)
}

// needsConfigCompletion identifies the only dynamic completion callbacks that
// consume config. Cobra completion callbacks cannot return command errors, so
// PersistentPreRunE loads this narrow subset before Cobra invokes them.
func needsConfigCompletion(cmd *cobra.Command, args []string) bool {
	if cmd.Name() != "__complete" && cmd.Name() != "__completeNoDesc" {
		return false
	}
	return hasInstanceCompletion(configCompletionArgs(args))
}

func configCompletionArgs(args []string) []string {
	for i, arg := range args {
		if arg == "query" {
			return args[i+1:]
		}
		if (arg == "snapshot" || arg == "snap" || arg == "snapshots") && i+1 < len(args) && args[i+1] == "logs" {
			return args[i+2:]
		}
	}
	return nil
}

// hasInstanceCompletion recognizes long and shorthand instance flags,
// including attached shorthand values such as -i=prod and -iprod.
func hasInstanceCompletion(args []string) bool {
	for _, arg := range args {
		if arg == "--instance" || strings.HasPrefix(arg, "--instance=") || strings.HasPrefix(arg, "-i") {
			return true
		}
	}
	return false
}

type versionInfo struct {
	Lognav   string `json:"lognav"`
	Config   int    `json:"config"`
	Snapshot uint32 `json:"snapshot"`
}

func currentVersion() versionInfo {
	return versionInfo{
		Lognav:   build.GetVersion(),
		Config:   config.CurrentVersion,
		Snapshot: snapshot.CurrentFileVersion,
	}
}

// printVersion renders the current binary and file-format versions.
func printVersion(w io.Writer, format outputFormat) error {
	v := currentVersion()
	if format == outputJSON {
		return writeJSON(w, v)
	}
	if _, err := fmt.Fprintf(w, "lognav version %s\n", v.Lognav); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "config version %d\n", v.Config); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "snapshot version %d\n", v.Snapshot)
	return err
}

// newVersionCmd builds the config-independent version command.
func newVersionCmd() *cobra.Command {
	version := &cobra.Command{
		Use:     "version",
		Short:   "Print version information",
		Long:    "Print the lognav binary version and the config and snapshot formats it supports. `lognav --version` and `lognav -v` print the text form without reading config.yaml.",
		Example: "  lognav version\n  lognav version -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			return printVersion(cmd.OutOrStdout(), format)
		},
	}
	addOutputFlag(version)
	version.ValidArgsFunction = cobra.NoFileCompletions
	return version
}

// defaultSetup is the production setupBundle: load real config + construct
// the real state manager. Tests inject a different closure via newRootCmd.
func defaultSetup(noConfig bool) (deps.Bundle, error) {
	if noConfig {
		// "do not read config" means use the built-in defaults, NOT a nil
		// Config: ui.New (via keys.KeyBindsFrom) dereferences bundle.Config, so
		// an empty Bundle here panics. config.New() returns the same defaults
		// LoadConfig falls back to when no file exists.
		return deps.New(config.New(), state.New()), nil
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return deps.Bundle{}, WithExit(ExitConfig, err)
	}
	mgr := state.New()
	return deps.New(cfg, mgr), nil
}

// newRootCmd builds the full Cobra command tree. Config consumers explicitly
// call the per-root loader; config-independent commands never invoke it.
func newRootCmd(setupBundle func(noConfig bool) (deps.Bundle, error)) *cobra.Command {
	loader := newBundleLoader(setupBundle)

	root := &cobra.Command{
		Use:           build.Name + " [snapshot-name|path]",
		Short:         build.Description,
		Long:          build.Description + "\n\nLaunch the TUI when run without a command. Optionally open a managed snapshot name, `latest`, or an external .lognav path. An argument containing / or ending in .lognav is a path; otherwise it is a managed name. Use `lognav -- <name>` when a snapshot name conflicts with a subcommand.\n\n`--adopt` cannot be used with a name or `latest`, and fails rather than overwriting an existing snapshot. Use `snapshot adopt --force` to replace one.\n\n`instruct`, `docs`, `logout`, `version`, completion generation and installation, and config's `path`, `set`, `unset`, and `edit` remain available when config.yaml is invalid. `login` loads configured IAM discovery endpoints; use `--no-config` for public defaults.\n\nQuestions or feedback? https://github.com/bevicted/lognav",
		Example:       "  lognav incident\n  lognav latest\n  lognav ~/Downloads/demo.lognav\n  lognav --adopt ~/Downloads/demo.lognav",
		Args:          rootArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if !needsConfigCompletion(cmd, args) {
				return nil
			}
			_, err := loader.Load(cmd)
			return err
		},
		PersistentPostRun: func(_ *cobra.Command, _ []string) {
			logging.Cleanup(loader.maxLogFiles())
		},
		RunE: rootRunE(loader.Load),
	}
	root.PersistentFlags().BoolP(noConfigFlag, "N", false, "do not read config")
	root.Flags().BoolP(adoptFlag, "A", false, "with a snapshot path: move it into the snapshot dir before opening")
	root.Flags().BoolP(versionFlag, "v", false, "print version information")
	root.ValidArgsFunction = completeNameOrPath
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return WithExit(ExitUsage, err)
	})

	root.AddCommand(
		initInstruct(),
		initTopicConfig(loader.Load),
		initQuery(loader.Load),
		initDocs(),
		initLogin(loader.Load),
		initLogout(),
		initSnapshot(loader.Load),
		newVersionCmd(),
		newCompletionCmd(),
	)
	tagUsageArgs(root)

	return root
}

// newExecuteRoot is a test seam for exercising Execute's process-signal path
// with deterministic command dependencies.
var newExecuteRoot = func() *cobra.Command { return newRootCmd(defaultSetup) }

type queryOutcomeGateContextKey struct{}

// queryOutcomeGate atomically arbitrates a process signal against the final
// durable query outcome. Ordinary mode commits immediately before selector
// emission; tee mode commits after its retained snapshot is ready. It is created
// for one Execute invocation and carried in its command context, so no
// process-global state participates in the decision.
type queryOutcomeGate struct {
	mu        sync.Mutex
	committed bool
	cancel    context.CancelFunc
}

func newQueryOutcomeGate(cancel context.CancelFunc) *queryOutcomeGate {
	return &queryOutcomeGate{cancel: cancel}
}

// cancelForSignal cancels unless the query has already committed its final
// outcome. Calling cancel while holding mu makes a winning cancellation visible
// to a waiting commit before it can emit a selector or retain a snapshot.
func (g *queryOutcomeGate) cancelForSignal() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed {
		return false
	}
	g.cancel()
	return true
}

// commit reserves the final query outcome. A signal cannot cancel after this
// returns true, and a prior signal has already canceled ctx while holding mu.
func (g *queryOutcomeGate) commit(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	g.committed = true
	return true
}

func commitQueryOutcome(ctx context.Context) bool {
	gate, _ := ctx.Value(queryOutcomeGateContextKey{}).(*queryOutcomeGate)
	if gate == nil {
		return ctx.Err() == nil
	}
	return gate.commit(ctx)
}

func Execute() error {
	slog.Info("program start", logging.KeyComponent, "cmd", "version", build.GetVersion())
	defer slog.Info("program exit", logging.KeyComponent, "cmd")

	root := newExecuteRoot()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcomeGate := newQueryOutcomeGate(cancel)
	ctx = context.WithValue(ctx, queryOutcomeGateContextKey{}, outcomeGate)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan struct{})
	watcherDone := make(chan struct{})
	codes := make(chan int, 1)
	go func() {
		defer close(watcherDone)
		select {
		case sig := <-signals:
			if outcomeGate.cancelForSignal() {
				if sig == syscall.SIGTERM {
					codes <- 143
				} else {
					codes <- 130
				}
			}
		case <-done:
		}
	}()
	cmd, err := executeRoot(ctx, root)
	close(done)
	<-watcherDone
	select {
	case code := <-codes:
		return renderExit(root.ErrOrStderr(), cmd, WithExit(code, errors.New("interrupted")))
	default:
		return renderExit(root.ErrOrStderr(), cmd, err)
	}
}

// executeRoot initializes Cobra's hidden completion request command while the
// root's streams are configured, then executes the fully tagged command tree.
func executeRoot(ctx context.Context, root *cobra.Command) (*cobra.Command, error) {
	root.InitDefaultCompletionCmd()
	tagUsageArgs(root)
	cmd, err := root.ExecuteContextC(ctx)
	return cmd, tagLateCompletionUsage(cmd, err)
}

// tagLateCompletionUsage types the only validator error from Cobra's hidden
// completion command. Cobra creates that command inside ExecuteContextC, after
// the command-tree walk above, so its MinimumNArgs validator cannot be wrapped
// before execution.
func tagLateCompletionUsage(cmd *cobra.Command, err error) error {
	if err == nil || cmd == nil || cmd.Name() != cobra.ShellCompRequestCmd {
		return err
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return err
	}
	return WithExit(ExitUsage, err)
}

// bundleMaxLogFiles returns the MaxLogFiles value from the bundle's config, or
// 0 (a no-op for logging.Cleanup) when no config was loaded.
func bundleMaxLogFiles(b deps.Bundle) uint8 {
	if b.Config == nil {
		return 0
	}
	return b.Config.Core.MaxLogFiles
}

// runTUI starts the TUI on the ultraviolet runtime spine.
func runTUI(ctx context.Context, bundle deps.Bundle, model *ui.Model) error {
	return runtime.Run(ctx, bundle, model)
}

// rootRunE returns the RunE handler for the root command. Extracted to keep
// newRootCmd below the gocyclo threshold.
// rootArgs preserves receiver-open positional arguments.
func rootArgs(_ *cobra.Command, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("accepts at most 1 arg(s), received %d", len(args))
	}
	if len(args) == 1 && args[0] == retiredCommand {
		return WithExit(ExitUsage, fmt.Errorf("unknown command %q for %q", retiredCommand, build.Name))
	}
	return nil
}

func rootRunE(loadBundle func(*cobra.Command) (deps.Bundle, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		showVersion, err := cmd.Flags().GetBool(versionFlag)
		if err != nil {
			return err
		}
		if showVersion {
			return printVersion(cmd.OutOrStdout(), outputText)
		}
		bundle, err := loadBundle(cmd)
		if err != nil {
			return err
		}

		model, err := ui.New(cmd.Context(), bundle)
		if err != nil {
			return WithExit(ExitConfig, err)
		}

		adopt, err := cmd.Flags().GetBool(adoptFlag)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			path, rerr := resolveLaunchTarget(args[0], adopt)
			if rerr != nil {
				return rerr
			}
			model.SetResumePath(path)
		} else if adopt {
			return WithExit(ExitUsage, errors.New("--adopt requires a snapshot path"))
		}

		return runTUI(cmd.Context(), bundle, model)
	}
}
