package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/internal/jsonutil"
)

const outputFlag = "output"

type outputFormat string

const (
	outputText outputFormat = "text"
	outputJSON outputFormat = "json"
)

// addOutputFlag registers the shared --output flag on a command with shell completion.
func addOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP(outputFlag, "o", string(outputText), "output format (text|json)")
	_ = cmd.RegisterFlagCompletionFunc(outputFlag, func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{string(outputText), string(outputJSON)}, cobra.ShellCompDirectiveNoFileComp
	})
}

// getOutputFormat reads --output and validates it against the supported set.
func getOutputFormat(cmd *cobra.Command) (outputFormat, error) {
	raw, err := cmd.Flags().GetString(outputFlag)
	if err != nil {
		return "", err
	}
	switch outputFormat(raw) {
	case outputText, outputJSON:
		return outputFormat(raw), nil
	default:
		return "", WithExit(ExitUsage, fmt.Errorf("invalid --output %q (want text|json)", raw))
	}
}

// writeJSON marshals v as indented JSON followed by a newline.
// jsonutil.API one-shot MarshalIndent; safe per F.2 D4 (sonic streaming forbidden).
func writeJSON(w io.Writer, v any) error {
	b, err := jsonutil.API.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
