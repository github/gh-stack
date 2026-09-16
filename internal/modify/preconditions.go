package modify

import (
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
)

// CheckWorktrees resolves the initiating worktree. Action-specific owner
// preflight runs after the TUI has produced its plan.
func CheckWorktrees(s *stack.Stack) (*worktree.Context, error) {
	ctx, err := worktree.New()
	if err != nil {
		return nil, err
	}
	ops, err := originOps(ctx)
	if err != nil {
		return nil, err
	}
	if err := worktree.CheckClean(ops, ctx.Origin.Path); err != nil {
		return nil, err
	}
	return ctx, nil
}

func originOps(ctx *worktree.Context) (git.Ops, error) {
	ops, err := ctx.OriginOps()
	if err != nil {
		return nil, err
	}
	if err := validateWorktreeRoot(ops, ctx.Origin.Path); err != nil {
		return nil, err
	}
	return ops, nil
}

func validateWorktreeRoot(ops git.Ops, path string) error {
	root, err := ops.RootDir()
	if err != nil {
		return fmt.Errorf("resolving the working-tree root at %q: %w", path, err)
	}
	if !worktree.SamePath(root, path) {
		return fmt.Errorf("%q is not a working-tree root; restore or repair the worktree before continuing", path)
	}
	return nil
}

func branchOps(ctx *worktree.Context, branch string) (git.Ops, error) {
	ops, err := ctx.Ops(branch)
	if err != nil {
		return nil, err
	}
	if err := validateWorktreeRoot(ops, ctx.Location(branch).Path); err != nil {
		return nil, err
	}
	return ops, nil
}

func validateOwner(ctx *worktree.Context, branch string, trees []git.Worktree) error {
	location := ctx.Location(branch)
	owner := ""
	for _, tree := range trees {
		if tree.Bare || tree.Branch != branch {
			continue
		}
		if owner != "" && !worktree.SamePath(owner, tree.Path) {
			return fmt.Errorf("branch %s is checked out in multiple worktrees", branch)
		}
		owner = tree.Path
		if tree.Prunable {
			return fmt.Errorf("worktree holding %s is unavailable at %s; repair it before continuing", branch, tree.Path)
		}
	}
	if owner != "" && !worktree.SamePath(owner, location.Path) {
		return fmt.Errorf("branch %s changed worktree owners since modify began (%s instead of %s); leaving it untouched", branch, owner, location.Path)
	}
	if owner == "" && !worktree.SamePath(location.Path, ctx.Origin.Path) {
		return fmt.Errorf("branch %s is no longer checked out in its recorded worktree %s", branch, location.Path)
	}
	return nil
}

func preflightBranches(state *StateFile, branches []string, pending string) error {
	ctx := state.Worktrees
	pendingPath := ""
	if pending != "" {
		if _, err := branchOps(ctx, pending); err != nil {
			return err
		}
		pendingPath = ctx.Location(pending).Path
	}
	trees, err := git.Worktrees()
	if err != nil {
		return fmt.Errorf("checking modify worktree ownership: %w", err)
	}
	if pending != "" {
		if err := validateOwner(ctx, pending, trees); err != nil {
			return err
		}
	}
	checked := make(map[string]bool)
	for _, branch := range branches {
		if checked[branch] {
			continue
		}
		checked[branch] = true
		if _, err := branchOps(ctx, branch); err != nil {
			return err
		}
		// A native conflict intentionally leaves this worktree busy. Its
		// remaining branches are checked immediately before their actions.
		if pendingPath != "" && worktree.SamePath(ctx.Location(branch).Path, pendingPath) {
			continue
		}
		if err := validateOwner(ctx, branch, trees); err != nil {
			return err
		}
		if err := ctx.Preflight([]string{branch}); err != nil {
			return err
		}
		if err := checkExpectedRef(state, branch); err != nil {
			return err
		}
	}
	return nil
}

// Journals without an origin cannot safely acquire distributed owners during
// recovery. Keep their original-worktree compatibility path conservative.
func checkSingleWorktree(ctx *worktree.Context, branches []string) error {
	if _, err := originOps(ctx); err != nil {
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
			return fmt.Errorf("legacy modify recovery cannot acquire another worktree for %s; return its branches to the original worktree first", tree.Branch)
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
