package cmd

import (
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/spf13/cobra"
)

func TrunkCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "trunk",
		Short: "Check out the trunk branch of the stack",
		Long: `Check out the trunk branch of the current stack.

The trunk is the base branch that the stack is built on (e.g., main or develop).
You must be on a branch that is part of a stack.`,
		Example: `  # Jump to the trunk branch
  $ gh stack trunk`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrunk(cfg)
		},
	}
}

func runTrunk(cfg *config.Config) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	s := result.Stack
	currentBranch := result.CurrentBranch
	trunk := s.Trunk.Branch

	if currentBranch == trunk {
		cfg.Printf("Already on trunk branch %s", trunk)
		return nil
	}

	if err := git.CheckoutBranch(trunk); err != nil {
		return err
	}

	cfg.Successf("Switched to %s", trunk)
	return nil
}
