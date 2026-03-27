package cmd

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/browser"
	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

func MergeCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge [<pr-or-branch>]",
		Short: "Merge a stack of PRs",
		Long: `Merges the specified PR and all PRs below it in the stack.

Accepts a PR URL, PR number, or branch name. When run without
arguments, operates on the current branch's PR.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var target string
			if len(args) > 0 {
				target = args[0]
			}
			return runMerge(cfg, target)
		},
	}

	return cmd
}

func runMerge(cfg *config.Config, target string) error {
	// Standard stack loading and validation.
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	s := result.Stack
	currentBranch := result.CurrentBranch

	// Sync PR state from GitHub so merge status is up to date.
	syncStackPRs(cfg, s)

	// Persist the refreshed PR state.
	stack.SaveNonBlocking(result.GitDir, result.StackFile)

	// Resolve which branch to operate on.
	var br *stack.BranchRef
	if target != "" {
		_, br, err = resolvePR(result.StackFile, target)
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrNotInStack
		}
	} else {
		idx := s.IndexOf(currentBranch)
		if idx < 0 {
			if s.IsFullyMerged() {
				cfg.Successf("All PRs in this stack have already been merged")
				return nil
			}
			cfg.Errorf("current branch %q is not a stack branch (it may be the trunk)", currentBranch)
			return ErrNotInStack
		}
		br = &s.Branches[idx]
	}

	if br.PullRequest == nil {
		cfg.Errorf("no pull request found for branch %q", br.Branch)
		cfg.Printf("  Run %s to create PRs for this stack.", cfg.ColorCyan("gh stack push"))
		return ErrSilent
	}

	if br.IsMerged() {
		cfg.Successf("PR %s has already been merged", cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
		cfg.Printf("  %s", br.PullRequest.URL)
		return nil
	}

	prURL := br.PullRequest.URL
	prLink := cfg.PRLink(br.PullRequest.Number, prURL)

	cfg.Warningf("Merging stacked PRs from the CLI is not yet supported")

	if cfg.IsInteractive() {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		openWeb, promptErr := p.Confirm(
			fmt.Sprintf("Open %s in your browser?", prLink), true)
		if promptErr != nil {
			if isInterruptError(promptErr) {
				printInterrupt(cfg)
				return nil
			}
			cfg.Errorf("prompt failed: %s", promptErr)
			return nil
		}

		if openWeb {
			b := browser.New("", cfg.Out, cfg.Err)
			if err := b.Browse(prURL); err != nil {
				cfg.Warningf("failed to open browser: %s", err)
			} else {
				cfg.Successf("Opened %s in your browser", prLink)
				return nil
			}
		}
	}

	cfg.Printf("  You can merge this PR at: %s", prURL)
	return nil
}
