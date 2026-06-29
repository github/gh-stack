package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/github"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
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

This command performs a safe, non-interactive synchronization:

  1. Fetches the latest changes from the remote
  2. Fast-forwards the trunk branch to match the remote
  3. Cascade-rebases stack branches onto their updated parents
  4. Pushes all branches atomically (using --force-with-lease --atomic)
  5. Syncs PR state from GitHub
  6. Links the stack's open PRs into a stack on GitHub (creating or updating
     the remote stack object) when two or more PRs exist

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
	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir

	if err := modify.CheckStateGuard(gitDir); err != nil {
		cfg.Errorf("%s", err)
		return ErrModifyRecovery
	}

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

	// Fetch trunk + active branches so tracking refs are current for
	// fast-forward detection (Step 2) and --force-with-lease (Step 4).
	fetchTargets := append([]string{s.Trunk.Branch}, activeBranchNames(s)...)
	_ = git.FetchBranches(remote, fetchTargets)
	cfg.Successf("Fetched latest changes from %s", remote)

	// --- Step 2: Fast-forward trunk ---
	trunk := s.Trunk.Branch
	trunkUpdated := fastForwardTrunk(cfg, trunk, remote, currentBranch)

	// --- Step 2b: Fast-forward stack branches behind their remote tracking branch ---
	updatedBranches := fastForwardBranches(cfg, s, remote, currentBranch)
	branchesUpdated := len(updatedBranches) > 0

	// --- Step 3: Cascade rebase ---
	// Rebase if trunk or any branch moved, or if the stack is stale
	// (branches not yet rebased onto their parent's current tip).
	needsRebase := trunkUpdated || branchesUpdated || stackNeedsRebase(s)
	rebased := false
	if needsRebase {
		cfg.Printf("")
		cfg.Printf("Rebasing stack ...")

		// Sync PR state to detect merged PRs before rebasing.
		_ = syncStackPRs(cfg, s)

		originalRefs, err := resolveOriginalRefs(s)
		if err != nil {
			cfg.Warningf("Could not resolve branch SHAs — skipping rebase: %v", err)
		} else {
			result := cascadeRebase(cascadeRebaseOpts{
				Cfg:          cfg,
				Stack:        s,
				Branches:     s.Branches,
				StartAbsIdx:  0,
				OriginalRefs: originalRefs,
			})

			if result.Err != nil {
				cfg.Errorf("%v", result.Err)
				_ = git.CheckoutBranch(currentBranch)
				stack.SaveNonBlocking(gitDir, sf)
				return ErrSilent
			}

			if result.Conflicted {
				// Abort and restore everything — sync is non-interactive.
				if git.IsRebaseInProgress() {
					_ = git.RebaseAbort()
				}
				restoreErrors := restoreBranches(originalRefs)
				_ = git.CheckoutBranch(currentBranch)

				cfg.Errorf("Conflict detected rebasing %s onto %s", result.ConflictBranch, result.ConflictBase)
				reportRestoreStatus(cfg, restoreErrors)
				cfg.Printf("  Run `%s` to resolve conflicts interactively.",
					cfg.ColorCyan("gh stack rebase"))

				// Persist refreshed PR state even on conflict, then bail out
				// before pushing or reporting success.
				stack.SaveNonBlocking(gitDir, sf)
				return ErrConflict
			}

			if result.Rebased {
				rebased = true
			}
		}
		_ = git.CheckoutBranch(currentBranch)
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
		stackSynced = syncRemoteStack(cfg, client, s)
	}

	// --- Step 6: Prune merged branches (optional) ---
	doPrune := opts.prune
	if !doPrune {
		// --prune was not provided. If interactive, prompt.
		merged := s.MergedBranches()
		var prunableCount int
		for _, b := range merged {
			if git.BranchExists(b.Branch) {
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
			if git.BranchExists(b.Branch) {
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
				switchTarget := trunk
				for _, b := range s.Branches {
					if !b.IsSkipped() {
						switchTarget = b.Branch
						break
					}
				}
				if err := git.CheckoutBranch(switchTarget); err != nil {
					cfg.Warningf("Failed to switch from %s to %s: %v", currentBranch, switchTarget, err)
				} else {
					currentBranch = switchTarget
				}
			}

			cfg.Printf("")
			pruned := 0
			for _, name := range prunable {
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
			_ = git.DeleteTrackingRef(remote, b.Branch)
		}
	}

	// --- Step 7: Update base SHAs and save ---
	updateBaseSHAs(s)

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
	return nil
}

// syncRemoteStack reconciles the stack object on GitHub with the local stack's
// open PRs. It only links existing PRs into a stack — it never opens PRs (use
// `gh stack submit` for that). It returns true when the remote stack object now
// reflects the local stack (created, updated, adopted, or already in sync), and
// false when there is nothing to sync or the remote stack could not be
// reconciled (fewer than two PRs, stacked PRs unavailable, a divergence across
// multiple stacks, or an API failure).
//
// A stack on GitHub requires at least two open PRs, so a single-PR or PR-less
// stack reconciles to false and the caller reports only the branches as synced.
func syncRemoteStack(cfg *config.Config, client github.ClientOps, s *stack.Stack) bool {
	prNumbers := stackPRNumbers(s)
	if len(prNumbers) < 2 {
		return false
	}

	// Inspect the remote stacks first so a routine sync that has not changed the
	// PR membership does not issue a redundant — and misleading — update.
	stacks, err := client.ListStacks()
	if err != nil {
		// Couldn't inspect remote state; let syncStack attempt the operation and
		// surface its own availability/PAT/create errors.
		return syncStack(cfg, client, s)
	}

	if matched, mErr := findMatchingStack(stacks, prNumbers); mErr == nil &&
		matched != nil && slicesEqual(matched.PullRequests, prNumbers) {
		// The remote stack already lists exactly these PRs — record its ID so
		// future operations stay cheap and report it as in sync.
		s.ID = strconv.Itoa(matched.ID)
		cfg.Successf("Stack already up to date on GitHub")
		return true
	}

	// Membership differs (or no stack exists yet): create, adopt, or update.
	return syncStack(cfg, client, s)
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

// confirmPrune asks the user to confirm pruning via ConfirmFn or a terminal prompt.
func confirmPrune(cfg *config.Config, prompt string, defaultValue bool) (bool, error) {
	if cfg.ConfirmFn != nil {
		return cfg.ConfirmFn(prompt, defaultValue)
	}
	p := prompter.New(cfg.In, cfg.Out, cfg.Err)
	return p.Confirm(prompt, defaultValue)
}
