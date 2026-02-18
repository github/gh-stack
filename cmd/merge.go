package cmd

import (
	"github.com/github/gh-stack/internal/config"
	"github.com/spf13/cobra"
)

func MergeCmd(cfg *config.Config) *cobra.Command {
	opts := struct{}{}

	cmd := &cobra.Command{
		Use:   "merge <pr>",
		Short: "Merge a stack of PRs",
		Long:  "Merges the specified PR and all PRs below it in the stack.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMerge(cfg, opts)
		},
	}

	return cmd
}

// runMerge is a placeholder for the stack merge workflow.
//
// We need a mergeability check for the entire stack
// and an endpoint for merging an entire stack
func runMerge(cfg *config.Config, opts struct{}) error {
	cfg.Warningf("gh stack merge is not yet implemented\n")
	return nil
}
