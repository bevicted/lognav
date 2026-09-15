package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/bevicted/lognav/internal/icl"
	"github.com/spf13/cobra"
)

func initLogout() *cobra.Command {
	return &cobra.Command{
		Use:               "logout",
		Short:             "Delete the saved authentication session",
		Long:              "Configured API keys and 1Password references are not removed and can still authenticate later fetches. The command succeeds when no session file exists.",
		Example:           "  lognav logout",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionPath, err := icl.SessionPath()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if err := os.Remove(sessionPath); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					fmt.Fprintln(out, "No session file found.")
					return nil
				}
				return err
			}

			fmt.Fprintln(out, "Session deleted.")
			return nil
		},
	}
}
