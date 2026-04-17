package cmd

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/spf13/cobra"
)

func SwitchCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "switch",
		Short: "Interactively switch to another branch in the stack",
		Long: `Show an interactive picker listing all branches in the current
stack and switch to the selected one.

Branches are displayed from top (furthest from trunk) to bottom
(closest to trunk) with their position number.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSwitch(cfg)
		},
	}
}

func runSwitch(cfg *config.Config) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	s := result.Stack

	if len(s.Branches) == 0 {
		cfg.Errorf("stack has no branches")
		return ErrNotInStack
	}

	if !cfg.IsInteractive() {
		cfg.Errorf("switch requires an interactive terminal")
		return ErrSilent
	}

	// Build options in reverse order (top of stack first) with 1-based numbering.
	n := len(s.Branches)
	options := make([]string, n)
	for i := 0; i < n; i++ {
		branchIdx := n - 1 - i
		options[i] = fmt.Sprintf("%d. %s", branchIdx+1, s.Branches[branchIdx].Branch)
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selectFn := func(prompt, def string, opts []string) (int, error) {
		return p.Select(prompt, def, opts)
	}
	if cfg.SelectFn != nil {
		selectFn = cfg.SelectFn
	}

	selected, err := selectFn("Select a branch in the stack to switch to:", "", options)
	if err != nil {
		if isInterruptError(err) {
			clearSelectPrompt(cfg, len(options))
			printInterrupt(cfg)
			return errInterrupt
		}
		return ErrSilent
	}

	// Map selection back: index 0 in options = branch at n-1, etc.
	branchIdx := n - 1 - selected
	targetBranch := s.Branches[branchIdx].Branch

	currentBranch := result.CurrentBranch
	if targetBranch == currentBranch {
		cfg.Infof("Already on %s", targetBranch)
		return nil
	}

	if err := git.CheckoutBranch(targetBranch); err != nil {
		cfg.Errorf("failed to checkout %s: %v", targetBranch, err)
		return ErrSilent
	}

	cfg.Successf("Switched to %s", targetBranch)
	return nil
}
