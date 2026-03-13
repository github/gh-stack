package cmd

import (
	"fmt"
	"strings"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type syncOptions struct{}

func SyncCmd(cfg *config.Config) *cobra.Command {
	opts := &syncOptions{}

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the current stack with the remote",
		Long: `Fetch, rebase, push, and sync PR state for the current stack.

This command performs a safe, non-interactive synchronization:

  1. Fetches the latest changes from origin
  2. Fast-forwards the trunk branch to match the remote
  3. Cascade-rebases stack branches onto their updated parents
  4. Pushes all branches (using --force-with-lease)
  5. Syncs PR state from GitHub

If a rebase conflict is detected, all branches are restored to their
original state and you are advised to run "gh stack rebase" to resolve
conflicts interactively.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cfg, opts)
		},
	}

	return cmd
}

func runSync(cfg *config.Config, _ *syncOptions) error {
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

	s, err := resolveStack(sf, currentBranch, cfg)
	if err != nil {
		cfg.Errorf("%s", err)
		return nil
	}
	if s == nil {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return nil
	}

	// Re-read current branch in case disambiguation caused a checkout
	currentBranch, err = git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	// --- Step 1: Fetch ---
	// Enable git rerere so conflict resolutions are remembered.
	_ = git.EnableRerere()

	if err := git.Fetch("origin"); err != nil {
		cfg.Warningf("Failed to fetch origin: %v", err)
	} else {
		cfg.Successf("Fetched latest changes")
	}

	// --- Step 2: Fast-forward trunk ---
	trunk := s.Trunk.Branch
	trunkUpdated := false

	localSHA, localErr := git.HeadSHA(trunk)
	remoteSHA, remoteErr := git.HeadSHA("origin/" + trunk)

	if localErr != nil || remoteErr != nil {
		cfg.Warningf("Could not compare trunk %s with remote — skipping trunk update", trunk)
	} else if localSHA == remoteSHA {
		cfg.Successf("Trunk %s is already up to date", trunk)
	} else {
		isAncestor, err := git.IsAncestor(localSHA, remoteSHA)
		if err != nil {
			cfg.Warningf("Could not determine fast-forward status for %s: %v", trunk, err)
		} else if !isAncestor {
			cfg.Warningf("Trunk %s has diverged from origin — skipping trunk update", trunk)
			cfg.Printf("  Local and remote %s have diverged. Resolve manually.", trunk)
		} else {
			// Fast-forward the trunk branch
			if currentBranch == trunk {
				// Can't update ref of checked-out branch; merge instead
				if err := ffMerge(trunk); err != nil {
					cfg.Warningf("Failed to fast-forward %s: %v", trunk, err)
				} else {
					cfg.Successf("Trunk %s fast-forwarded to %s", trunk, short(remoteSHA))
					trunkUpdated = true
				}
			} else {
				if err := updateBranchRef(trunk, remoteSHA); err != nil {
					cfg.Warningf("Failed to fast-forward %s: %v", trunk, err)
				} else {
					cfg.Successf("Trunk %s fast-forwarded to %s", trunk, short(remoteSHA))
					trunkUpdated = true
				}
			}
		}
	}

	// --- Step 3: Cascade rebase (only if trunk moved) ---
	rebased := false
	if trunkUpdated {
		cfg.Printf("")
		cfg.Printf("Rebasing stack ...")

		// Sync PR state to detect merged PRs before rebasing.
		syncStackPRs(cfg, s)

		// Save original refs so we can restore on conflict
		originalRefs := make(map[string]string)
		for _, b := range s.Branches {
			sha, _ := git.HeadSHA(b.Branch)
			originalRefs[b.Branch] = sha
		}

		needsOnto := false
		var ontoOldBase string

		conflicted := false
		for i, br := range s.Branches {
			var base string
			if i == 0 {
				base = trunk
			} else {
				base = s.Branches[i-1].Branch
			}

			// Skip branches whose PRs have already been merged.
			if br.IsMerged() {
				ontoOldBase = originalRefs[br.Branch]
				needsOnto = true
				cfg.Successf("Skipping %s (PR #%d merged)", br.Branch, br.PullRequest.Number)
				continue
			}

			if needsOnto {
				// Find --onto target: first non-merged ancestor, or trunk.
				newBase := trunk
				for j := i - 1; j >= 0; j-- {
					b := s.Branches[j]
					if !b.IsMerged() {
						newBase = b.Branch
						break
					}
				}

				if err := git.RebaseOnto(newBase, ontoOldBase, br.Branch); err != nil {
					// Conflict detected — abort and restore everything
					if git.IsRebaseInProgress() {
						_ = git.RebaseAbort()
					}
					restoreErrors := restoreBranches(originalRefs)
					_ = git.CheckoutBranch(currentBranch)

					cfg.Errorf("Conflict detected rebasing %s onto %s", br.Branch, newBase)
					reportRestoreStatus(cfg, restoreErrors)
					cfg.Printf("  Run %s to resolve conflicts interactively.",
						cfg.ColorCyan("gh stack rebase"))
					conflicted = true
					break
				}

				cfg.Successf("Rebased %s onto %s (squash-merge detected)", br.Branch, newBase)
				ontoOldBase = originalRefs[br.Branch]
			} else {
				var rebaseErr error
				if i > 0 {
					// Use --onto to replay only this branch's unique commits.
					rebaseErr = git.RebaseOnto(base, originalRefs[base], br.Branch)
				} else {
					if err := git.CheckoutBranch(br.Branch); err != nil {
						cfg.Errorf("Failed to checkout %s: %v", br.Branch, err)
						conflicted = true
						break
					}
					rebaseErr = git.Rebase(base)
				}

				if rebaseErr != nil {
					// Conflict detected — abort and restore everything
					if git.IsRebaseInProgress() {
						_ = git.RebaseAbort()
					}
					restoreErrors := restoreBranches(originalRefs)
					_ = git.CheckoutBranch(currentBranch)

					cfg.Errorf("Conflict detected rebasing %s onto %s", br.Branch, base)
					reportRestoreStatus(cfg, restoreErrors)
					cfg.Printf("  Run %s to resolve conflicts interactively.",
						cfg.ColorCyan("gh stack rebase"))
					conflicted = true
					break
				}

				cfg.Successf("Rebased %s onto %s", br.Branch, base)
			}
		}

		if !conflicted {
			rebased = true
			_ = git.CheckoutBranch(currentBranch)
		}
	}

	// --- Step 4: Push ---
	cfg.Printf("")
	var branches []string
	for _, b := range s.Branches {
		if !b.IsMerged() {
			branches = append(branches, b.Branch)
		}
	}

	if mergedCount := len(s.MergedBranches()); mergedCount > 0 {
		cfg.Printf("Skipping %d merged %s", mergedCount, plural(mergedCount, "branch", "branches"))
	}

	if len(branches) == 0 {
		cfg.Printf("No active branches to push (all merged)")
	} else {
		// After rebase, force-with-lease is required (history rewritten).
		// Without rebase, try a normal push first.
		force := rebased
		cfg.Printf("Pushing branches ...")
		if err := git.Push("origin", branches, force, false); err != nil {
			if !force {
				cfg.Warningf("Push failed — branches may need force push after rebase")
				cfg.Printf("  Run %s to push with --force-with-lease.",
					cfg.ColorCyan("gh stack push"))
			} else {
				cfg.Warningf("Push failed: %v", err)
				cfg.Printf("  Run %s to retry.", cfg.ColorCyan("gh stack push"))
			}
		} else {
			cfg.Successf("Pushed %d branches", len(branches))
		}
	}

	// --- Step 5: Sync PR state ---
	cfg.Printf("")
	cfg.Printf("Syncing PRs ...")
	syncStackPRs(cfg, s)

	// Report PR status for each branch
	for _, b := range s.Branches {
		if b.IsMerged() {
			continue
		}
		if b.PullRequest != nil {
			cfg.Successf("PR #%d (%s) — Open", b.PullRequest.Number, b.Branch)
		} else {
			cfg.Warningf("%s has no PR", b.Branch)
		}
	}
	merged := s.MergedBranches()
	if len(merged) > 0 {
		names := make([]string, len(merged))
		for i, m := range merged {
			if m.PullRequest != nil {
				names[i] = fmt.Sprintf("#%d", m.PullRequest.Number)
			} else {
				names[i] = m.Branch
			}
		}
		cfg.Printf("Merged: %s", strings.Join(names, ", "))
	}

	// --- Step 6: Update base SHAs and save ---
	for i := range s.Branches {
		// Skip merged branches when updating base SHAs.
		if s.Branches[i].IsMerged() {
			continue
		}
		parent := s.ActiveBaseBranch(s.Branches[i].Branch)
		if base, err := git.HeadSHA(parent); err == nil {
			s.Branches[i].Base = base
		}
		if head, err := git.HeadSHA(s.Branches[i].Branch); err == nil {
			s.Branches[i].Head = head
		}
	}

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return nil
	}

	cfg.Printf("")
	cfg.Successf("Stack synced")
	return nil
}

// ffMerge fast-forwards the currently checked-out branch to match origin.
func ffMerge(branch string) error {
	return git.MergeFF("origin/" + branch)
}

// updateBranchRef updates a branch ref to point to a new SHA (for branches not checked out).
func updateBranchRef(branch, sha string) error {
	return git.UpdateBranchRef(branch, sha)
}

// restoreBranches resets each branch to its original SHA, collecting any errors.
func restoreBranches(originalRefs map[string]string) []string {
	var errors []string
	for branch, sha := range originalRefs {
		if err := git.CheckoutBranch(branch); err != nil {
			errors = append(errors, fmt.Sprintf("checkout %s: %s", branch, err))
			continue
		}
		if err := git.ResetHard(sha); err != nil {
			errors = append(errors, fmt.Sprintf("reset %s: %s", branch, err))
		}
	}
	return errors
}

// reportRestoreStatus prints whether branch restoration succeeded or partially failed.
func reportRestoreStatus(cfg *config.Config, restoreErrors []string) {
	if len(restoreErrors) > 0 {
		cfg.Warningf("Some branches could not be fully restored:")
		for _, e := range restoreErrors {
			cfg.Printf("  %s", e)
		}
	} else {
		cfg.Printf("  All branches restored to their original state.")
	}
}

// short returns the first 7 characters of a SHA.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
