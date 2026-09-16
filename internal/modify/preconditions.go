package modify

import (
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
)

// CheckWorktrees permits modify in a linked worktree, but not a stack whose
// member branches are checked out in other worktrees. Trunk is only read.
func CheckWorktrees(s *stack.Stack) (*worktree.Context, error) {
	ctx, err := worktree.New()
	if err != nil {
		return nil, err
	}
	if err := checkSingleWorktree(ctx, s.BranchNames()); err != nil {
		return nil, err
	}
	ops, err := ctx.OriginOps()
	if err != nil {
		return nil, err
	}
	if err := worktree.CheckClean(ops, ctx.Origin.Path); err != nil {
		return nil, err
	}
	return ctx, nil
}

func checkSingleWorktree(ctx *worktree.Context, branches []string) error {
	if _, err := ctx.OriginOps(); err != nil {
		return err
	}
	trees, err := git.Worktrees()
	if err != nil {
		return fmt.Errorf("checking modify worktree ownership: %w", err)
	}
	members := make(map[string]bool, len(branches))
	for _, name := range branches {
		members[name] = true
	}
	for _, tree := range trees {
		if tree.Branch != "" && members[tree.Branch] && !worktree.SamePath(tree.Path, ctx.Origin.Path) {
			return fmt.Errorf("distributed modify is not supported yet: branch %s is checked out in worktree %s; all stack branches must be unoccupied or in %s", tree.Branch, tree.Path, ctx.Origin.Path)
		}
	}
	return nil
}

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
			cfg.Errorf("failed to check merge commits for %s: %s", b.Branch, err)
			return fmt.Errorf("checking merge commits for %s: %w", b.Branch, err)
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
