package cmd

import (
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type pushOptions struct {
	remote string
}

func PushCmd(cfg *config.Config) *cobra.Command {
	opts := &pushOptions{}

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push all branches in the current stack to the remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cfg, opts)
		},
	}

	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to push to (defaults to auto-detected remote)")

	return cmd
}

func runPush(cfg *config.Config, opts *pushOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	if err := modify.CheckStateGuard(gitDir); err != nil {
		cfg.Errorf("%s", err)
		return ErrModifyRecovery
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return ErrNotInStack
	}

	// Find the stack for the current branch without switching branches.
	// Push should never change the user's checked-out branch.
	stacks := sf.FindAllStacksForBranch(currentBranch)
	if len(stacks) == 0 {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return ErrNotInStack
	}
	if len(stacks) > 1 {
		cfg.Errorf("branch %q belongs to multiple stacks; checkout a non-trunk branch first", currentBranch)
		return ErrDisambiguate
	}
	s := stacks[0]

	// Push all active branches atomically
	remote, err := pickRemote(cfg, currentBranch, opts.remote)
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return ErrSilent
	}
	// Sync PR state to detect merged/queued PRs before pushing.
	syncStackPRs(cfg, s)

	merged := s.MergedBranches()
	if len(merged) > 0 {
		cfg.Printf("Skipping %d merged %s", len(merged), plural(len(merged), "branch", "branches"))
	}
	queued := s.QueuedBranches()
	if len(queued) > 0 {
		cfg.Printf("Skipping %d queued %s", len(queued), plural(len(queued), "branch", "branches"))
	}
	activeBranches := activeBranchNames(s)
	if len(activeBranches) == 0 {
		cfg.Printf("No active branches to push (all merged or queued)")
		return nil
	}
	if err := git.FetchBranches(remote, activeBranches); err != nil {
		cfg.Warningf("Failed to fetch branches from %s: %v", remote, err)
	}
	cfg.Printf("Pushing %d %s to %s...", len(activeBranches), plural(len(activeBranches), "branch", "branches"), remote)
	if err := git.Push(remote, activeBranches, true, false); err != nil {
		cfg.Errorf("failed to push: %s", err)
		return ErrSilent
	}

	// Update base commit hashes after push
	updateBaseSHAs(s)

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	cfg.Successf("Pushed %d branches", len(activeBranches))

	// Hint about submit only if there are branches without PRs
	hasBranchWithoutPR := false
	for _, b := range s.ActiveBranches() {
		if b.PullRequest == nil {
			hasBranchWithoutPR = true
			break
		}
	}
	if hasBranchWithoutPR {
		cfg.Printf("To create PRs for this stack, run `%s`",
			cfg.ColorCyan("gh stack submit"))
	}
	return nil
}

// pickRemote determines which remote to push to. If remoteOverride is
// non-empty, it is returned directly. Otherwise it delegates to
// git.ResolveRemote for config-based resolution and remote listing.
// If multiple remotes exist with no configured default, the user is
// prompted to select one interactively.
func pickRemote(cfg *config.Config, branch, remoteOverride string) (string, error) {
	if remoteOverride != "" {
		return remoteOverride, nil
	}

	remote, err := git.ResolveRemote(branch)
	if err == nil {
		return remote, nil
	}

	var multi *git.ErrMultipleRemotes
	if !errors.As(err, &multi) {
		return "", err
	}

	if !cfg.IsInteractive() {
		return "", fmt.Errorf("multiple remotes configured; set remote.pushDefault or use an interactive terminal")
	}

	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	selected, promptErr := p.Select("Multiple remotes found. Which remote should be used?", "", multi.Remotes)
	if promptErr != nil {
		if isInterruptError(promptErr) {
			clearSelectPrompt(cfg, len(multi.Remotes))
			printInterrupt(cfg)
			return "", errInterrupt
		}
		return "", fmt.Errorf("remote selection: %w", promptErr)
	}
	return multi.Remotes[selected], nil
}
