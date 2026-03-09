package cmd

import (
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
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
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil
	}

	target := opts.target
	if target == "" {
		target, err = git.CurrentBranch()
		if err != nil {
			cfg.Errorf("unable to determine current branch: %s", err)
			return nil
		}
	}

	s := sf.FindStackForBranch(target)
	if s == nil {
		cfg.Errorf("branch %q is not part of a stack", target)
		return nil
	}

	cfg.Printf("Stack branches: %v", s.BranchNames())

	// Remove from local tracking
	sf.RemoveStackForBranch(target)
	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return nil
	}
	cfg.Successf("Stack removed from local tracking")

	// Delete the stack on GitHub
	if !opts.local {
		client, err := cfg.GitHubClient()
		if err != nil {
			cfg.Errorf("failed to create GitHub client: %s", err)
			return nil
		}
		if err := client.DeleteStack(); err != nil {
			cfg.Warningf("%v", err)
		} else {
			cfg.Successf("Stack deleted on GitHub")
		}
	}

	return nil
}
