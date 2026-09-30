package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
	"github.com/spf13/cobra"
)

type syncOptions struct {
	remote string
	prune  bool
}

func SyncCmd(cfg *config.Config) *cobra.Command {
	opts := &syncOptions{}

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the current stack with the remote",
		Long: `Fetch, rebase, push, and sync PR state for the current stack.

This command performs a safe synchronization:

  1. Fetches the latest changes from the remote
  2. Reconciles the stack on GitHub with your local stack: pulls down
     branches for any PRs added to the stack on GitHub, or prompts you to
     resolve a divergence in an interactive terminal
  3. Fast-forwards the trunk branch to match the remote
  4. Cascade-rebases stack branches onto their updated parents
  5. Pushes all branches atomically (using --force-with-lease --atomic)
  6. Syncs PR state from GitHub
  7. Links the stack's open PRs into a stack on GitHub (creating or updating
     the remote stack object) when two or more PRs exist

If PRs have been added to the stack on GitHub, their branches are pulled
down and appended to your local stack so it mirrors the remote. A clean
"remote is ahead" update happens automatically without prompting. If the
local and remote stacks have diverged, sync prompts (in an interactive
terminal) to use the remote as the source of truth, delete the stack on
GitHub and recreate it later with sync/submit, or cancel. Cancelling — or a
divergence in a non-interactive terminal — aborts the sync without pushing
branches or updating PRs.

If a rebase conflict is detected, all branches are restored to their
original state and you are advised to run "gh stack rebase" to resolve
conflicts interactively.

Sync never opens pull requests — use "gh stack submit" for that. It only
links PRs that already exist. The final message reflects what happened:
"Stack synced" means the stack object on GitHub now matches your local
stack, while "Branches synced" means the branches were rebased and pushed
but no remote stack object was created or updated (for example, when fewer
than two PRs exist yet).

Use --prune to delete local branches for merged PRs. Stack metadata is
preserved so that rebase and display logic continue to work correctly.
If you are on a branch that would be pruned, your checkout is moved to
the first active branch in the stack, or the trunk if all are merged.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cfg, opts)
		},
	}

	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to fetch from and push to (defaults to auto-detected remote)")
	cmd.Flags().BoolVar(&opts.prune, "prune", false, "Delete local branches for merged PRs")

	return cmd
}

func runSync(cfg *config.Config, opts *syncOptions) error {
	release, err := beginStackMutation(cfg, "sync")
	if err != nil {
		return err
	}
	defer release()
	result, err := loadStack(cfg, "")
	if err != nil {
		return stackLookupError(err)
	}
	gitDir := result.GitDir

	if err := modify.CheckStateGuard(gitDir); err != nil {
		cfg.Errorf("%s", err)
		return ErrModifyRecovery
	}

	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch
	originalTrunk := s.Trunk.Branch

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

	// Fetch trunk + active branches so tracking refs are current for
	// fast-forward detection (Step 2) and --force-with-lease (Step 4).
	if err := normalizeStackTrunk(cfg, s, remote); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	if err := git.FetchBranches(remote, activeBranchNames(s)); err != nil {
		cfg.Errorf("failed to fetch stack branches from %s: %v", remote, err)
		return ErrSilent
	}

	// --- Step 1b: Reconcile remote-ahead stack changes ---
	// Pull in branches for PRs that were added to the stack on GitHub, or
	// resolve a divergence, before rebasing and pushing so pulled branches
	// participate in the normal flow. Best-effort for stacks tracked on the
	// remote; a no-op otherwise.
	reconcileRes, err := reconcileRemoteStack(cfg, sf, s, currentBranch, gitDir, remote)
	if err != nil {
		if errors.Is(err, errInterrupt) {
			return ErrSilent
		}
		return err
	}
	if reconcileRes.stack != nil {
		s = reconcileRes.stack
	}
	if reconcileRes.stop {
		// The reconcile step resolved the situation and there is nothing more to
		// do (the user cancelled or deleted the remote stack, or a divergence was
		// detected non-interactively). The resolving path already reported the
		// outcome, so just exit successfully.
		return nil
	}
	// Reconciling "use remote as source of truth" may have moved us off a
	// branch that is no longer in the stack, so re-read the current branch.
	if cb, cbErr := git.CurrentBranch(); cbErr == nil {
		currentBranch = cb
	}
	_ = syncStackPRs(cfg, s)
	ctx, err := worktree.New()
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	planned := planFastForwardBranches(s, remote)

	// --- Step 2: Resolve trunk ---
	trunk, err := resolveTrunkTarget(cfg, s, remote, currentBranch, trunkResolveOptions{
		Worktrees: ctx,
		Preflight: func(sha string, moved bool) error {
			var required []string
			for _, forward := range planned {
				required = append(required, forward.Branch)
			}
			if moved || len(planned) > 0 || stackNeedsRebase(s, sha) {
				required = append(required, activeBranchNames(s)...)
			}
			if err := ctx.Preflight(required); err != nil {
				cfg.Errorf("%s", err)
				return ErrSilent
			}
			return nil
		},
	})
	if err != nil {
		return err
	}

	// --- Step 2b: Fast-forward stack branches behind their remote tracking branch ---
	updatedBranches, err := fastForwardBranches(cfg, planned, ctx)
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}

	// --- Step 3: Cascade rebase ---
	needsRebase := trunk.Moved || len(updatedBranches) > 0 || stackNeedsRebase(s, trunk.Ref)
	rebased := false
	var originalRefs map[string]string
	var state *rebaseState
	if needsRebase {
		cfg.Printf("")
		cfg.Printf("Rebasing stack ...")

		originalRefs, err = resolveOriginalRefs(s)
		if err != nil {
			cfg.Errorf("Could not resolve branch SHAs: %v", err)
			return ErrSilent
		} else {
			state = newWorktreeRebaseState(s, ctx, currentBranch, originalRefs, trunk, 0, len(s.Branches))
			if s.Trunk.Branch != originalTrunk {
				if err := stack.Save(gitDir, sf); err != nil {
					return handleSaveError(cfg, err)
				}
			}
			if err := saveRebaseState(gitDir, state); err != nil {
				cfg.Errorf("%s", err)
				return ErrSilent
			}
			result := cascadeRebase(cascadeRebaseOpts{
				Cfg:          cfg,
				Stack:        s,
				Branches:     s.Branches,
				StartAbsIdx:  0,
				OriginalRefs: originalRefs,
				TrunkRef:     trunk.Ref,
				TrunkSHA:     trunk.SHA,
				Worktrees:    ctx,
				State:        state,
				StateDir:     gitDir,
			})

			if result.Err != nil {
				cfg.Errorf("%v", result.Err)
				if err := rollbackWorktreeRebase(cfg, gitDir, state); err != nil {
					return errors.Join(ErrSilent, err)
				}
				return ErrSilent
			}

			if result.Conflicted {
				// Abort and restore everything — sync is non-interactive.
				cfg.Errorf("Conflict detected rebasing %s onto %s", result.ConflictBranch, result.ConflictBase)
				if err := rollbackWorktreeRebase(cfg, gitDir, state); err != nil {
					return errors.Join(ErrSilent, err)
				}
				cfg.Printf("Branches restored to their pre-rebase state")
				cfg.Printf("  Run `%s` to resolve conflicts interactively.",
					cfg.ColorCyan("gh stack rebase"))

				// Persist refreshed PR state even on conflict, then bail out
				// before pushing or reporting success.
				if err := stack.Save(gitDir, sf); err != nil {
					cfg.Warningf("Could not save refreshed PR metadata: %v", err)
				}
				return ErrConflict
			}

			if result.Rebased {
				rebased = true
			}
		}
		if err := ctx.RestoreOrigin(currentBranch); err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
	}

	if unstacked := verifyStacked(s, trunk.SHA, 0, len(s.Branches)); len(unstacked) > 0 {
		reportUnstacked(cfg, trunk.Ref, unstacked)
		if state != nil {
			if err := rollbackWorktreeRebase(cfg, gitDir, state); err != nil {
				return errors.Join(ErrSilent, err)
			}
		}
		return ErrSilent
	}
	if state != nil {
		if err := publishCompletedRebase(cfg, gitDir, state, sf, s); err != nil {
			return err
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
	_ = syncStackPRs(cfg, s)

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

	// --- Step 5b: Reconcile the remote stack object ---
	// syncStackPRs above only refreshes local PR associations; it does not touch
	// the stack object on GitHub. When the branches have open PRs, link them into
	// a stack so the remote reflects the local stack. This never opens PRs — that
	// is still `gh stack submit`'s job. stackSynced records whether the remote
	// stack object actually reflects the local stack, which determines the final
	// summary message below.
	stackSynced := false
	if client, err := cfg.GitHubClient(); err == nil {
		stackSynced = syncStack(cfg, client, s)
	}

	// --- Step 6: Prune merged branches (optional) ---
	doPrune := opts.prune
	if !doPrune {
		// --prune was not provided. If interactive, prompt.
		merged := s.MergedBranches()
		var prunableCount int
		for _, b := range merged {
			exists, err := git.BranchExists(b.Branch)
			if err != nil {
				cfg.Errorf("failed to check branch %s for pruning: %s", b.Branch, err)
				return ErrSilent
			}
			if exists {
				prunableCount++
			}
		}
		if prunableCount > 0 && cfg.IsInteractive() {
			prompt := fmt.Sprintf("Prune %d merged %s?",
				prunableCount, plural(prunableCount, "branch", "branches"))
			confirmed, err := confirmPrune(cfg, prompt, true)
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					// Save state before exiting so PR sync isn't lost.
					_ = stack.Save(gitDir, sf)
					return ErrSilent
				}
				// On any other prompt error, skip pruning silently.
			} else {
				doPrune = confirmed
			}
		}
	}

	if doPrune {
		merged := s.MergedBranches()
		var prunable []string
		for _, b := range merged {
			exists, err := git.BranchExists(b.Branch)
			if err != nil {
				cfg.Errorf("failed to check branch %s for pruning: %s", b.Branch, err)
				return ErrSilent
			}
			if exists {
				prunable = append(prunable, b.Branch)
			}
		}

		if len(prunable) > 0 {
			// If the current branch is being pruned, switch away first.
			needsSwitch := false
			for _, name := range prunable {
				if name == currentBranch {
					needsSwitch = true
					break
				}
			}
			if needsSwitch {
				switchTarget := ""
				for _, b := range s.Branches {
					if !b.IsSkipped() {
						if owner := ctx.Owners[b.Branch]; owner != nil && !worktree.SamePath(owner.Path, ctx.Origin.Path) {
							continue
						}
						switchTarget = b.Branch
						break
					}
				}
				if switchTarget == "" {
					if owner := ctx.Owners[trunk.Branch]; owner == nil || worktree.SamePath(owner.Path, ctx.Origin.Path) {
						switchTarget = trunk.Branch
					}
				}
				if switchTarget == "" {
					cfg.Infof("Keeping %s: no available checkout destination", currentBranch)
				} else if err := git.CheckoutBranch(switchTarget); err != nil {
					cfg.Warningf("Failed to switch from %s to %s: %v", currentBranch, switchTarget, err)
				} else {
					currentBranch = switchTarget
				}
			}

			cfg.Printf("")
			pruned := 0
			for _, name := range prunable {
				if owner := ctx.Owners[name]; owner != nil && !worktree.SamePath(owner.Path, ctx.Origin.Path) {
					cfg.Infof("Keeping %s: checked out in worktree %s", name, owner.Path)
					continue
				}
				if name == currentBranch {
					cfg.Infof("Keeping %s: still checked out", name)
					continue
				}
				if err := git.DeleteBranch(name, true); err != nil {
					cfg.Warningf("Failed to delete %s: %v", name, err)
				} else {
					cfg.Successf("Pruned %s (merged)", name)
					pruned++
				}
			}
			if pruned > 0 {
				cfg.Successf("Pruned %d merged %s", pruned, plural(pruned, "branch", "branches"))
			}
		} else if opts.prune {
			cfg.Printf("")
			cfg.Printf("No merged branches to prune")
		}

		// Clean up remote-tracking refs for all merged branches, even if
		// the local branch was already deleted. This prevents
		// `git checkout <name>` from resurrecting the branch.
		for _, b := range merged {
			if owner := ctx.Owners[b.Branch]; owner != nil && !worktree.SamePath(owner.Path, ctx.Origin.Path) {
				continue
			}
			_ = git.DeleteTrackingRef(remote, b.Branch)
		}
	}

	// --- Step 7: Update base SHAs and save ---
	updateBaseSHAsWithTrunk(s, trunk.SHA)

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	cfg.Printf("")
	if stackSynced {
		cfg.Successf("Stack synced")
	} else {
		// The branches were fetched, rebased, and pushed, but no stack object on
		// GitHub was created or updated (no PRs, fewer than two PRs, stacked PRs
		// unavailable, or a divergence). Report only what actually happened.
		cfg.Successf("Branches synced")
	}
	cfg.Printf("  Stacked on %s", trunk.Describe())
	return nil
}

// restoreBranches checks branch availability before resetting any tips.
// Lookup failures stop restoration; individual mutation failures are collected.
func restoreBranches(originalRefs map[string]string) ([]string, error) {
	var branches []string
	for branch := range originalRefs {
		exists, err := git.BranchExists(branch)
		if err != nil {
			return nil, fmt.Errorf("checking branch %s before restoring: %w", branch, err)
		}
		if exists {
			sha, err := git.RevParse(branch)
			if err != nil {
				return nil, fmt.Errorf("reading branch %s before restoring: %w", branch, err)
			}
			if sha != originalRefs[branch] {
				branches = append(branches, branch)
			}
		}
	}
	var errors []string
	for _, branch := range branches {
		sha := originalRefs[branch]
		if err := git.CheckoutBranch(branch); err != nil {
			errors = append(errors, fmt.Sprintf("checkout %s: %s", branch, err))
			continue
		}
		if err := git.ResetHard(sha); err != nil {
			errors = append(errors, fmt.Sprintf("reset %s: %s", branch, err))
		}
	}
	return errors, nil
}

func restoreRebaseRefs(cfg *config.Config, originalBranch string, originalRefs map[string]string) error {
	restoreErrors, err := restoreBranches(originalRefs)
	if err != nil {
		cfg.Errorf("%s", err)
		return errors.Join(ErrSilent, err)
	}
	checkoutErr := restoreRebaseCheckout(originalBranch)
	if checkoutErr != nil {
		restoreErrors = append(restoreErrors, checkoutErr.Error())
	}
	reportRestoreStatus(cfg, restoreErrors)
	if len(restoreErrors) > 0 {
		return errors.Join(ErrSilent, errors.New(strings.Join(restoreErrors, "\n")), checkoutErr)
	}
	return nil
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

// confirmPrune asks the user to confirm pruning via ConfirmFn or a terminal prompt.
func confirmPrune(cfg *config.Config, prompt string, defaultValue bool) (bool, error) {
	if cfg.ConfirmFn != nil {
		return cfg.ConfirmFn(prompt, defaultValue)
	}
	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	return p.Confirm(prompt, defaultValue)
}
