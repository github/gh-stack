package cmd

import (
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

func AddCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add [branch]",
		Short: "Add a new branch on top of the current stack",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cfg, args)
		},
	}
	return cmd
}

func runAdd(cfg *config.Config, args []string) error {
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

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	s, err := sf.ResolveStack(currentBranch, cfg)
	if err != nil {
		cfg.Errorf("%s", err)
		return nil
	}
	if s == nil {
		cfg.Errorf("current branch %q is not part of a stack; run 'gh stack init' first", currentBranch)
		return nil
	}

	// Re-read current branch in case disambiguation caused a checkout
	currentBranch, err = git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	idx := s.IndexOf(currentBranch)
	if idx >= 0 && idx < len(s.Branches)-1 {
		cfg.Errorf("can only add branches on top of the stack; checkout the top branch %q first", s.Branches[len(s.Branches)-1].Branch)
		return nil
	}

	var branchName string
	if len(args) > 0 {
		branchName = args[0]
	} else {
		fmt.Fprintf(cfg.Err, "Enter a name for the new branch: ")
		if _, err := fmt.Fscan(cfg.In, &branchName); err != nil {
			return fmt.Errorf("could not read branch name: %w", err)
		}
	}

	if branchName == "" {
		cfg.Errorf("branch name cannot be empty")
		return nil
	}

	if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
		cfg.Errorf("branch %q already exists in the stack", branchName)
		return nil
	}

	if git.BranchExists(branchName) {
		cfg.Errorf("branch %q already exists", branchName)
		return nil
	}

	if err := git.CreateBranch(branchName, currentBranch); err != nil {
		cfg.Errorf("failed to create branch: %s", err)
		return nil
	}

	if err := git.CheckoutBranch(branchName); err != nil {
		cfg.Errorf("failed to checkout branch: %s", err)
		return nil
	}

	base, _ := git.HeadSHA(currentBranch)
	s.Branches = append(s.Branches, stack.BranchRef{Branch: branchName, Base: base})

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return nil
	}

	cfg.Successf("Created and checked out branch %q\n", branchName)
	return nil
}
