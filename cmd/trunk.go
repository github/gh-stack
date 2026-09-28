package cmd

import (
	"errors"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/spf13/cobra"
)

func TrunkCmd(cfg *config.Config) *cobra.Command {
	var printPath bool
	cmd := &cobra.Command{
		Use:   "trunk",
		Short: "Check out the trunk branch of the stack",
		Long: `Check out the trunk branch of the current stack.

The trunk is the base branch that the stack is built on (e.g., main or develop).
You must be on a branch that is part of a stack.`,
		Example: `  # Jump to the trunk branch
  $ gh stack trunk`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrunkWithPath(cfg, printPath)
		},
	}
	cmd.Flags().BoolVar(&printPath, "print-path", false, "Print the trunk worktree path without switching a branch held elsewhere")
	return cmd
}

func runTrunk(cfg *config.Config) error {
	return runTrunkWithPath(cfg, false)
}

func runTrunkWithPath(cfg *config.Config, printPath bool) error {
	if printPath {
		cfg = noninteractiveConfig(cfg)
	}
	result, err := loadNavigationStack(cfg, printPath)
	if err != nil {
		if errors.Is(err, errInterrupt) {
			return ErrSilent
		}
		return stackLookupError(err)
	}
	s := result.Stack
	currentBranch := result.CurrentBranch
	trunk := s.Trunk.Branch

	if currentBranch == trunk {
		cfg.Printf("Already on trunk branch %s", trunk)
		if printPath {
			return checkoutWorktreeBranch(cfg, trunk, true)
		}
		return nil
	}

	owner, err := foreignWorktreePath(trunk)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	if owner != "" {
		return checkoutWorktreeBranch(cfg, trunk, printPath)
	}
	// Ensure trunk exists locally before checkout.
	exists, err := git.BranchExists(trunk)
	if err != nil {
		cfg.Errorf("failed to check trunk branch %s: %s", trunk, err)
		return ErrSilent
	}
	if !exists {
		release, err := beginStackMutation(cfg, "trunk")
		if err != nil {
			return err
		}
		defer release()
		remote, err := pickRemote(cfg, currentBranch, "")
		if err != nil {
			if !errors.Is(err, errInterrupt) {
				cfg.Errorf("failed to resolve remote: %s", err)
			}
			return ErrSilent
		}
		if err := ensureLocalTrunk(cfg, trunk, remote); err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
	}

	if err := checkoutWorktreeBranch(cfg, trunk, printPath); err != nil {
		return err
	}
	if printPath {
		return nil
	}

	cfg.Successf("Switched to %s", trunk)
	return nil
}
