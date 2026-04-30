package modify

import (
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
)

// CheckNoMergeQueuePRs checks that no unmerged PR in the stack is currently queued.
func CheckNoMergeQueuePRs(cfg *config.Config, s *stack.Stack) error {
	for _, b := range s.Branches {
		if b.IsQueued() && !b.IsMerged() {
			prLink := ""
			if b.PullRequest != nil {
				prLink = cfg.PRLink(b.PullRequest.Number, b.PullRequest.URL)
			}
			cfg.Errorf("branch %s has a PR (%s) in the merge queue", b.Branch, prLink)
			cfg.Printf("Wait for it to land or remove it from the queue before modifying the stack")
			return fmt.Errorf("merge queue conflict on %s", b.Branch)
		}
	}
	return nil
}

// CheckStackLinearity verifies that the stack has unambiguous commit-to-branch mapping.
// For each adjacent pair (parent, child), checks:
// 1. parent tip is an ancestor of child tip
// 2. no merge commits exist in the range parent..child
func CheckStackLinearity(cfg *config.Config, s *stack.Stack) error {
	for i, b := range s.Branches {
		if b.IsMerged() {
			continue
		}

		var parentBranch string
		if i == 0 {
			parentBranch = s.Trunk.Branch
		} else {
			parentBranch = s.Branches[i-1].Branch
		}

		if i > 0 && s.Branches[i-1].IsMerged() {
			parentBranch = s.ActiveBaseBranch(b.Branch)
		}

		isAnc, err := git.IsAncestor(parentBranch, b.Branch)
		if err != nil {
			cfg.Errorf("failed to check linearity for %s: %s", b.Branch, err)
			return fmt.Errorf("linearity check failed for %s", b.Branch)
		}
		if !isAnc {
			cfg.Errorf("%s has diverged from %s", b.Branch, parentBranch)
			cfg.Printf("Run `%s` to normalize the stack, or `%s` to restructure manually",
				cfg.ColorCyan("gh stack rebase"),
				cfg.ColorCyan("gh stack unstack"))
			return fmt.Errorf("%s has diverged from %s", b.Branch, parentBranch)
		}

		merges, err := git.LogMerges(parentBranch, b.Branch)
		if err != nil {
			continue
		}
		if len(merges) > 0 {
			cfg.Errorf("%s contains a merge commit — modify requires linear history", b.Branch)
			cfg.Printf("Run `%s` to replay without the merge, or `%s` to restructure manually",
				cfg.ColorCyan("gh stack rebase"),
				cfg.ColorCyan("gh stack unstack"))
			return fmt.Errorf("%s contains merge commits", b.Branch)
		}
	}

	return nil
}
