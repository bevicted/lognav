package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/bevicted/lognav/internal/config"
	"github.com/bevicted/lognav/internal/deps"
	"github.com/bevicted/lognav/internal/externaleditor"
	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"
)

func normalizeYAMLPath(args []string) string {
	if len(args) == 0 {
		return "$"
	}
	return config.NormalizeYAMLPath(args[0])
}

func exactDottedConfigKey(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return WithExit(ExitUsage, errors.New("config get requires exactly one dotted key"))
	}
	if !isSimpleDottedConfigKey(args[0]) {
		return WithExit(ExitUsage, fmt.Errorf("invalid configuration key %q", args[0]))
	}
	return nil
}

func isSimpleDottedConfigKey(key string) bool {
	for part := range strings.SplitSeq(key, ".") {
		if part == "" || !isASCIIAlpha(part[0]) {
			return false
		}
		for i := 1; i < len(part); i++ {
			if !isASCIIAlpha(part[i]) && (part[i] < '0' || part[i] > '9') {
				return false
			}
		}
	}
	return key != ""
}

func isASCIIAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func yamlValue(value any) (string, error) {
	b, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func renderConfigStatus(out io.Writer, report config.StatusReport) error {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "type\tstate\tkeys\tpath"); err != nil {
		return err
	}
	for _, file := range report.Files {
		keys := "-"
		if file.Keys != nil {
			keys = strconv.Itoa(*file.Keys)
		}
		filePath := "-"
		if file.Path != nil {
			filePath = *file.Path
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", file.Type, file.State, keys, filePath); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(table, "effective\t"+report.Effective.State+"\t-\t-"); err != nil {
		return err
	}
	if err := table.Flush(); err != nil {
		return err
	}

	for _, file := range report.Files {
		for _, diagnostic := range file.Errors {
			if _, err := fmt.Fprintf(out, "%s: %s\n", file.Type, diagnostic); err != nil {
				return err
			}
		}
	}
	for _, diagnostic := range report.Effective.Errors {
		if _, err := fmt.Fprintf(out, "effective: %s\n", diagnostic); err != nil {
			return err
		}
	}
	return nil
}

func completeReadableConfigKeys(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return configKeyCompletions(false, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeWritableConfigKeys(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return configKeyCompletions(true, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// configKeyCompletions derives shell candidates from the same metadata used by
// config describe. Writable completion is limited to configurable leaves.
func configKeyCompletions(writableOnly bool, prefix string) []string {
	var completions []string
	var walk func([]config.FieldMeta)
	walk = func(fields []config.FieldMeta) {
		for _, field := range fields {
			key := strings.TrimPrefix(field.YAMLPath, "$.")
			isLeaf := len(field.Children) == 0
			if (!writableOnly || isLeaf && !field.ReadOnly && !field.Computed) && strings.HasPrefix(key, prefix) {
				completion := key
				if field.Description != "" {
					completion += "\t" + field.Description
				}
				completions = append(completions, completion)
			}
			walk(field.Children)
		}
	}
	walk(config.GetFieldMetadata(config.New(), "$"))
	return completions
}

var getConfigPath = config.GetConfigPath

// initTopicConfig returns the "config" Cobra command subtree.
//
//nolint:gocyclo,funlen // Cobra command factory groups independent command handlers.
func initTopicConfig(loadBundle func(*cobra.Command) (deps.Bundle, error)) *cobra.Command {
	// Note: the previous implementation set Version: strconv.Itoa(cfgVersion())
	// on this topic command. That was a latent bug — cfgVersion() closed over
	// the zero-value bundle at construction time and always returned 0. The
	// Version field has been removed rather than silently reporting the wrong
	// value. Wire it up via a dedicated "config version" subcommand if needed
	// in a future cluster.
	topic := &cobra.Command{
		Use:   "config [command]",
		Short: "Manage configuration",
		Long:  "Configuration reads merge public defaults, optional read-only Homebrew defaults, optional system YAML, and sparse `user.yaml` overrides. Writes modify only `user.yaml`. `status`, `set`, `unset`, and `edit` remain available when user.yaml is missing or invalid.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	show := &cobra.Command{
		Use:     "show",
		Short:   "Show the effective configuration",
		Long:    "The output includes merged values, redacted secrets, and computed values, so it is display-only and cannot be edited or round-tripped as user.yaml.",
		Example: "  lognav config show\n  lognav config show -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			if format == outputJSON {
				view, err := config.EffectiveConfigJSONValue(bundle.Config)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), view)
			}
			view, err := config.EffectiveConfig(bundle.Config)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), view)
			return nil
		},
	}
	addOutputFlag(show)
	show.ValidArgsFunction = cobra.NoFileCompletions

	get := &cobra.Command{
		Use:               "get <dotted-key>",
		Short:             "Get one effective configuration value",
		Long:              "Missing keys are errors. `icl.instances` is the writable list of configured targets.",
		Example:           "  lognav config get core.enableMouse\n  lognav config get icl.instances -o json",
		Args:              exactDottedConfigKey,
		ValidArgsFunction: completeReadableConfigKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			value, err := config.EffectiveConfigValue(bundle.Config, args[0])
			if errors.Is(err, config.ErrUnknownConfigurationKey) {
				return WithExit(ExitNoInput, fmt.Errorf("unknown configuration key %q", args[0]))
			}
			if err != nil {
				return err
			}
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			if format == outputJSON {
				return writeJSON(cmd.OutOrStdout(), value)
			}
			out, err := yamlValue(value)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}
	addOutputFlag(get)

	describe := &cobra.Command{
		Use:               "describe [dotted-key]",
		Short:             "Describe configuration fields",
		Long:              "Output includes types, defaults, and current values.",
		Example:           "  lognav config describe\n  lognav config describe core\n  lognav config describe core.enableMouse\n  lognav config describe icl.instances -o json",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeReadableConfigKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			bundle, err := loadBundle(cmd)
			if err != nil {
				return err
			}
			yamlPath := normalizeYAMLPath(args)

			output := config.DescribeConfig(bundle.Config, yamlPath)
			if output == "" {
				return WithExit(ExitNoInput, fmt.Errorf("no config fields found for path %q", yamlPath))
			}
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			if format == outputJSON {
				output, err = config.FieldMetadataJSON(bundle.Config, yamlPath)
				if err != nil {
					return err
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), output, "\n")
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), output)
			return nil
		},
	}
	addOutputFlag(describe)

	set := &cobra.Command{
		Use:               "set <yamlpath> <value>",
		Short:             "Set a configuration value",
		Long:              "A leading $ on the key is optional. Numbers and booleans are typed automatically, and lists use YAML literals such as '[enter, ctrl+y]'. The value is validated before writing; rejected values do not change the file. An invalid value can be replaced in an otherwise invalid file, but the resulting complete document must validate. Only the changed key is written, preserving comments and other fields.",
		Example:           "  lognav config set core.enableMouse false\n  lognav config set style.errorColor '#ff5555'\n  lognav config set keys.accept '[enter, ctrl+y]'",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWritableConfigKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			yamlPath := normalizeYAMLPath(args[:1])
			if err := config.SetConfig(yamlPath, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s\n", strings.TrimPrefix(yamlPath, "$."))
			return nil
		},
	}

	unset := &cobra.Command{
		Use:               "unset <yamlpath>",
		Short:             "Unset a configuration value",
		Long:              "The inherited system, Homebrew, or public default applies afterward. A leading $ on the key is optional. An absent field is a no-op. An invalid value can be removed from an otherwise invalid file, but the resulting complete document must validate.",
		Example:           "  lognav config unset core.redrawIntervalMs",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWritableConfigKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			yamlPath := normalizeYAMLPath(args)
			if err := config.UnsetConfig(yamlPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unset %s\n", strings.TrimPrefix(yamlPath, "$."))
			return nil
		},
	}

	status := &cobra.Command{
		Use:               "status",
		Short:             "Diagnose configuration file layers",
		Long:              "Inspect Homebrew, system, and user configuration files without loading runtime services or changing files. Text output lists each file's sparse-layer state and explicit key count, then reports diagnostics; JSON keeps diagnostics in each record.",
		Example:           "  lognav config status\n  lognav config status -o json",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := getOutputFormat(cmd)
			if err != nil {
				return err
			}
			report := config.InspectStatus()
			if format == outputJSON {
				err = writeJSON(cmd.OutOrStdout(), report)
			} else {
				err = renderConfigStatus(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return err
			}
			if report.HasErrors() {
				return WithExit(ExitConfig, errors.New("configuration status found errors"))
			}
			return nil
		},
	}
	addOutputFlag(status)

	topic.AddCommand(
		show,
		get,
		describe,
		set,
		unset,
		status,
		&cobra.Command{
			Use:               "edit [flags]",
			Short:             "Edit the configuration file",
			Long:              "Open the sparse configuration file with $EDITOR, or vim when unset. $EDITOR may contain an executable and simple whitespace-separated arguments such as `code --wait`; quote parsing and shell expansion are not supported. It works when configuration is missing or invalid.",
			Example:           "  lognav config edit",
			Args:              cobra.NoArgs,
			ValidArgsFunction: cobra.NoFileCompletions,
			RunE: func(cmd *cobra.Command, args []string) error {
				p, err := getConfigPath()
				if err != nil {
					return err
				}

				c := externaleditor.NewCommand(cmd.Context(), p)
				c.Stdin = os.Stdin
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr

				return c.Run()
			},
		},
	)

	return topic
}
