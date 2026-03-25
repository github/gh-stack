package cmd

import (
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type unstackOptions struct {
	target string
	local  bool
}

func UnstackCmd(cfg *config.Config) *cobra.Command {
	opts := &unstackOptions{}

	cmd := &cobra.Command{
		Use:   "unstack [branch]",
		Short: "Delete a stack locally and on GitHub",
		Long:  "Remove a stack from local tracking and delete it on GitHub. Use --local to only remove local tracking.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.target = args[0]
			}
			return runUnstack(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.local, "local", false, "Only delete the stack locally")

	return cmd
}

func runUnstack(cfg *config.Config, opts *unstackOptions) error {
	result, err := loadStack(cfg, opts.target)
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	target := opts.target
	if target == "" {
		target = result.CurrentBranch
	}

	cfg.Printf("Stack branches: %v", s.BranchNames())

	// Remove from local tracking
	sf.RemoveStackForBranch(target)
	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return ErrSilent
	}
	cfg.Successf("Stack removed from local tracking")

	// Delete the stack on GitHub
	if !opts.local {
		client, err := cfg.GitHubClient()
		if err != nil {
			cfg.Errorf("failed to create GitHub client: %s", err)
			return ErrAPIFailure
		}
		if err := client.DeleteStack(); err != nil {
			cfg.Warningf("%v", err)
		} else {
			cfg.Successf("Stack deleted on GitHub")
		}
	}

	return nil
}
