package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type syncOptions struct {
	remote string
}

func SyncCmd(cfg *config.Config) *cobra.Command {
	opts := &syncOptions{}

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the current stack with the remote",
		Long: `Fetch, rebase, push, and sync PR state for the current stack.

This command performs a safe, non-interactive synchronization:

  1. Fetches the latest changes from the remote
  2. Fast-forwards the trunk branch to match the remote
  3. Cascade-rebases stack branches onto their updated parents
  4. Pushes all branches atomically (using --force-with-lease --atomic)
  5. Syncs PR state from GitHub

If a rebase conflict is detected, all branches are restored to their
original state and you are advised to run "gh stack rebase" to resolve
conflicts interactively.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cfg, opts)
		},
	}

	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to fetch from and push to (defaults to auto-detected remote)")

	return cmd
}

func runSync(cfg *config.Config, opts *syncOptions) error {
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	// Resolve remote once for fetch and push
	remote, err := pickRemote(cfg, currentBranch, opts.remote)
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return ErrSilent
	}

	// --- Step 1: Fetch ---
	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return ErrSilent
	}

	if err := git.Fetch(remote); err != nil {
		cfg.Warningf("Failed to fetch %s: %v", remote, err)
	} else {
		cfg.Successf("Fetched latest changes from %s", remote)
	}

	// --- Step 2: Fast-forward trunk ---
	trunk := s.Trunk.Branch
	trunkUpdated := false

	localSHA, remoteSHA := "", ""
	trunkRefs, trunkErr := git.RevParseMulti([]string{trunk, remote + "/" + trunk})
	if trunkErr == nil {
		localSHA, remoteSHA = trunkRefs[0], trunkRefs[1]
	}

	if trunkErr != nil {
		cfg.Warningf("Could not compare trunk %s with remote — skipping trunk update", trunk)
	} else if localSHA == remoteSHA {
		cfg.Successf("Trunk %s is already up to date", trunk)
	} else {
		isAncestor, err := git.IsAncestor(localSHA, remoteSHA)
		if err != nil {
			cfg.Warningf("Could not determine fast-forward status for %s: %v", trunk, err)
		} else if !isAncestor {
			cfg.Warningf("Trunk %s has diverged from %s — skipping trunk update", trunk, remote)
			cfg.Printf("  Local and remote %s have diverged. Resolve manually.", trunk)
		} else {
			// Fast-forward the trunk branch
			if currentBranch == trunk {
				if err := git.MergeFF(remote + "/" + trunk); err != nil {
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
		branchNames := make([]string, len(s.Branches))
		for i, b := range s.Branches {
			branchNames[i] = b.Branch
		}
		originalRefs, _ := git.RevParseMap(branchNames)

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
				cfg.Successf("Skipping %s (PR %s merged)", br.Branch, cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
				continue
			}

			// Skip branches whose PRs are currently in a merge queue.
			if br.IsQueued() {
				ontoOldBase = originalRefs[br.Branch]
				needsOnto = true
				cfg.Successf("Skipping %s (PR %s queued)", br.Branch, cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
				continue
			}

			if needsOnto {
				// Find --onto target: first non-merged/queued ancestor, or trunk.
				newBase := trunk
				for j := i - 1; j >= 0; j-- {
					b := s.Branches[j]
					if !b.IsSkipped() {
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
					cfg.Printf("  Run `%s` to resolve conflicts interactively.",
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
					cfg.Printf("  Run `%s` to resolve conflicts interactively.",
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
		} else {
			// Persist refreshed PR state even on conflict, then bail out
			// before pushing or reporting success.
			stack.SaveNonBlocking(gitDir, sf)
			return ErrConflict
		}
	}

	// --- Step 4: Push ---
	cfg.Printf("")
	branches := activeBranchNames(s)

	if mergedCount := len(s.MergedBranches()); mergedCount > 0 {
		cfg.Printf("Skipping %d merged %s", mergedCount, plural(mergedCount, "branch", "branches"))
	}
	if queuedCount := len(s.QueuedBranches()); queuedCount > 0 {
		cfg.Printf("Skipping %d queued %s", queuedCount, plural(queuedCount, "branch", "branches"))
	}

	if len(branches) == 0 {
		cfg.Printf("No active branches to push (all merged)")
	} else {
		// After rebase, force-with-lease is required (history rewritten).
		// Without rebase, try a normal push first.
		force := rebased
		cfg.Printf("Pushing %d %s to %s...", len(branches), plural(len(branches), "branch", "branches"), remote)
		if err := git.Push(remote, branches, force, true); err != nil {
			if !force {
				cfg.Warningf("Push failed — branches may need force push after rebase")
				cfg.Printf("  Run `%s` to push with --force-with-lease.",
					cfg.ColorCyan("gh stack push"))
			} else {
				cfg.Warningf("Push failed: %v", err)
				cfg.Printf("  Run `%s` to retry.", cfg.ColorCyan("gh stack push"))
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
		if b.IsQueued() {
			cfg.Successf("PR %s (%s) — Queued", cfg.PRLink(b.PullRequest.Number, b.PullRequest.URL), b.Branch)
			continue
		}
		if b.PullRequest != nil {
			cfg.Successf("PR %s (%s) — Open", cfg.PRLink(b.PullRequest.Number, b.PullRequest.URL), b.Branch)
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
	updateBaseSHAs(s)

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	cfg.Printf("")
	cfg.Successf("Stack synced")
	return nil
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
