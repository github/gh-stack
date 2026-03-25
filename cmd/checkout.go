package cmd

import (
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type checkoutOptions struct {
	target string
}

func CheckoutCmd(cfg *config.Config) *cobra.Command {
	opts := &checkoutOptions{}

	cmd := &cobra.Command{
		Use:   "checkout [<pr-or-branch>]",
		Short: "Checkout a stack from a PR number or branch name",
		Long: `Check out a stack from a pull request number or branch name.

Currently resolves stacks from local tracking only (.git/gh-stack).
Accepts a PR number (e.g. 42) or a branch name that belongs to
a locally tracked stack. When run without arguments, shows a menu of
all locally available stacks to choose from.

Server-side stack discovery will be added in a future release.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.target = args[0]
			}
			return runCheckout(cfg, opts)
		},
	}

	return cmd
}

// runCheckout resolves a stack from local tracking and checks out the target branch.
//
// Future behavior (once the server API is available):
//  1. Resolve the target (PR number, URL, or branch name) to a PR via the API
//  2. If the PR is part of a stack, discover the full set of PRs in the stack
//  3. Fetch and create local tracking branches for every branch in the stack
//  4. Save the stack to local tracking (.git/gh-stack, similar to gh stack init --adopt)
//  5. Switch to the target branch
func runCheckout(cfg *config.Config, opts *checkoutOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	var s *stack.Stack
	var targetBranch string

	if opts.target == "" {
		// Interactive picker mode
		s, err = interactiveStackPicker(cfg, sf)
		if err != nil {
			if !errors.Is(err, errInterrupt) {
				cfg.Errorf("%s", err)
			}
			return ErrSilent
		}
		if s == nil {
			return nil
		}
		// Check out the top active branch of the selected stack
		targetBranch = s.Branches[len(s.Branches)-1].Branch
	} else {
		// Resolve target against local stacks
		var br *stack.BranchRef
		s, br, err = resolvePR(sf, opts.target)
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrNotInStack
		}
		targetBranch = br.Branch
	}

	currentBranch, _ := git.CurrentBranch()
	if targetBranch == currentBranch {
		cfg.Infof("Already on %s", targetBranch)
		cfg.Printf("Stack: %s", s.DisplayChain())
		return nil
	}

	if err := git.CheckoutBranch(targetBranch); err != nil {
		cfg.Errorf("failed to checkout %s: %v", targetBranch, err)
		return ErrSilent
	}

	cfg.Successf("Switched to %s", targetBranch)
	cfg.Printf("Stack: %s", s.DisplayChain())
	return nil
}

// interactiveStackPicker shows a menu of all locally tracked stacks and returns
// the one the user selects. Returns nil, nil if the user has no stacks.
func interactiveStackPicker(cfg *config.Config, sf *stack.StackFile) (*stack.Stack, error) {
	if !cfg.IsInteractive() {
		return nil, fmt.Errorf("no target specified; provide a branch name or PR number, or run interactively to select a stack")
	}

	if len(sf.Stacks) == 0 {
		cfg.Infof("No locally tracked stacks found")
		cfg.Printf("Create a stack with `%s` or check out a specific branch/PR once server-side discovery is available.", cfg.ColorCyan("gh stack init"))
		return nil, nil
	}

	options := make([]string, len(sf.Stacks))
	for i := range sf.Stacks {
		options[i] = sf.Stacks[i].DisplayChain()
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selected, err := p.Select(
		"Select a stack to check out (showing locally tracked stacks only)",
		"",
		options,
	)
	if err != nil {
		if isInterruptError(err) {
			printInterrupt(cfg)
			return nil, errInterrupt
		}
		return nil, fmt.Errorf("stack selection: %w", err)
	}

	return &sf.Stacks[selected], nil
}
