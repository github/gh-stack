package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/github/gh-stack/internal/worktree"
	"github.com/spf13/cobra"
)

type rebaseOptions struct {
	branch                    string
	downstack                 bool
	upstack                   bool
	cont                      bool
	abort                     bool
	noTrunk                   bool
	remote                    string
	committerDateIsAuthorDate bool
}

type rebaseState struct {
	ExecutionMode             string            `json:"executionMode,omitempty"`
	Phase                     string            `json:"phase,omitempty"`
	Worktrees                 *worktree.Context `json:"worktrees,omitempty"`
	StackID                   string            `json:"stackId,omitempty"`
	StackTrunk                string            `json:"stackTrunk,omitempty"`
	StackBranches             []string          `json:"stackBranches,omitempty"`
	OriginalStack             *stack.Stack      `json:"originalStack,omitempty"`
	CurrentBranchIndex        int               `json:"currentBranchIndex"`
	ConflictBranch            string            `json:"conflictBranch"`
	RemainingBranches         []string          `json:"remainingBranches"`
	OriginalBranch            string            `json:"originalBranch"`
	OriginalRefs              map[string]string `json:"originalRefs"`
	UseOnto                   bool              `json:"useOnto,omitempty"`
	OntoOldBase               string            `json:"ontoOldBase,omitempty"`
	CommitterDateIsAuthorDate bool              `json:"committerDateIsAuthorDate,omitempty"`
	NoTrunk                   bool              `json:"noTrunk,omitempty"`
	TrunkRef                  string            `json:"trunkRef,omitempty"`
	TrunkSHA                  string            `json:"trunkSha,omitempty"`
	StartIndex                int               `json:"startIndex,omitempty"`
	EndIndex                  int               `json:"endIndex,omitempty"`
}

const (
	rebaseStateFile      = "gh-stack-rebase-state"
	originOnlyRebaseMode = "origin-only"
)

func RebaseCmd(cfg *config.Config) *cobra.Command {
	opts := &rebaseOptions{}

	cmd := &cobra.Command{
		Use:   "rebase [branch]",
		Short: "Rebase a stack of branches",
		Long: `Pull from remote and do a cascading rebase across the stack.

Ensures that each branch in the stack has the tip of the previous
layer in its commit history, rebasing if necessary.

Use --no-trunk to skip fetching and rebasing with the trunk branch.
Only the inter-branch rebases are performed (branch 2 onto branch 1,
branch 3 onto branch 2, etc.).

All stack branches and any trunk being updated must currently be unoccupied
or checked out in this worktree. Cross-worktree rewrites are refused before
requested mutations, after shared-catalog migration. Continue and abort must
be run in the worktree where the rebase started.`,
		Example: `  # Rebase the entire stack
  $ gh stack rebase

  # Only rebase from trunk to the current branch
  $ gh stack rebase --downstack

  # Only rebase from current branch to the top
  $ gh stack rebase --upstack

  # Rebase stack branches without pulling from or rebasing with trunk
  $ gh stack rebase --no-trunk

  # Continue after resolving conflicts
  $ gh stack rebase --continue

  # Abort and restore all branches
  $ gh stack rebase --abort`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.branch = args[0]
			}
			return runRebase(cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.downstack, "downstack", false, "Only rebase branches from trunk to current branch")
	cmd.Flags().BoolVar(&opts.upstack, "upstack", false, "Only rebase branches from current branch to top")
	cmd.Flags().BoolVar(&opts.noTrunk, "no-trunk", false, "Skip trunk — only rebase stack branches onto each other")
	cmd.Flags().BoolVar(&opts.cont, "continue", false, "Continue rebase after resolving conflicts")
	cmd.Flags().BoolVar(&opts.abort, "abort", false, "Abort rebase and restore all branches")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to fetch from (defaults to auto-detected remote)")
	cmd.Flags().BoolVar(&opts.committerDateIsAuthorDate, "committer-date-is-author-date", false, "Set the committer date to the author date during rebase")
	cmd.Flags().BoolVar(&opts.committerDateIsAuthorDate, "preserve-dates", false, "Alias for --committer-date-is-author-date")

	return cmd
}

func runRebase(cfg *config.Config, opts *rebaseOptions) error {
	kind := "rebase"
	if opts.cont {
		kind = "rebase-continue"
	} else if opts.abort {
		kind = "rebase-abort"
	}
	release, err := beginStackMutation(cfg, kind)
	if err != nil {
		return err
	}
	defer release()
	gitDir, err := stackStateDir(cfg)
	if err != nil {
		return err
	}

	if opts.cont {
		return continueRebase(cfg, gitDir)
	}

	if opts.abort {
		return abortRebase(cfg, gitDir)
	}

	if err := modify.CheckStateGuard(gitDir); err != nil {
		cfg.Errorf("%s", err)
		return ErrModifyRecovery
	}

	result, err := loadStack(cfg, opts.branch)
	if err != nil {
		return stackLookupError(err)
	}
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch
	originalTrunk := s.Trunk.Branch
	if err := requireLocalBranches(cfg, s.BranchNames()); err != nil {
		return err
	}
	ctx, err := worktree.New()
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	anchor := currentBranch
	if opts.branch != "" {
		anchor = opts.branch
	}
	var remote string
	if !opts.noTrunk {
		remote, err = pickRemote(cfg, anchor, opts.remote)
		if err != nil {
			if !errors.Is(err, errInterrupt) {
				cfg.Errorf("%s", err)
			}
			return ErrSilent
		}
		trunkBranch, err := normalizeTrunkBranch(s.Trunk.Branch, remote)
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
		if err := requireLocalBranches(cfg, []string{trunkBranch}); err != nil {
			return err
		}
	}
	if err := worktree.CheckClean(git.CurrentOps(), ctx.Origin.Path); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}

	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return ErrSilent
	}

	var trunk trunkTarget
	if !opts.noTrunk {
		trunk, err = resolveTrunkTarget(cfg, s, remote, currentBranch)
		if err != nil {
			return err
		}

		// Fast-forward stack branches that are behind their remote tracking branch.
		if err := git.FetchBranches(remote, activeBranchNames(s)); err != nil {
			cfg.Errorf("failed to fetch stack branches from %s: %v", remote, err)
			return ErrSilent
		}
		fastForwardBranches(cfg, s, remote, currentBranch)
	}

	cfg.Printf("Stack detected: %s", s.DisplayChain())

	currentIdx := s.IndexOf(anchor)
	if currentIdx < 0 {
		currentIdx = 0
	}
	if len(s.Branches) == 0 {
		cfg.Printf("No branches to rebase")
		return nil
	}

	if opts.upstack && currentIdx >= 0 && s.Branches[currentIdx].IsMerged() {
		cfg.Warningf("Current branch %q has already been merged", currentBranch)
	}

	startIdx := 0
	endIdx := len(s.Branches)

	if opts.downstack {
		endIdx = currentIdx + 1
	}
	if opts.upstack {
		startIdx = currentIdx
	}

	// With --no-trunk, skip the first branch (which would rebase onto trunk).
	if opts.noTrunk && startIdx < 1 {
		startIdx = 1
	}

	branchesToRebase := s.Branches[startIdx:endIdx]

	if len(branchesToRebase) == 0 {
		cfg.Printf("No branches to rebase")
		return nil
	}

	cfg.Printf("Rebasing branches in order, starting from %s to %s",
		branchesToRebase[0].Branch, branchesToRebase[len(branchesToRebase)-1].Branch)

	// Sync PR state before rebase so we can detect merged PRs.
	_ = syncStackPRs(cfg, s)

	originalRefs, err := resolveOriginalRefs(s)
	if err != nil {
		return fmt.Errorf("resolving branch refs: %w", err)
	}

	// Get --onto state from a merged branch immediately below the rebase range.
	// Ensures that when --upstack excludes merged branches, we still check the
	// immediate predecessor and use --onto if needed.
	needsOnto := false
	var ontoOldBase string
	if startIdx > 0 {
		prev := s.Branches[startIdx-1]
		if prev.IsMerged() {
			if sha, ok := originalRefs[prev.Branch]; ok {
				needsOnto = true
				ontoOldBase = sha
			}
		}
	}

	state := newWorktreeRebaseState(s, ctx, currentBranch, originalRefs, trunk, startIdx, endIdx)
	state.CommitterDateIsAuthorDate = opts.committerDateIsAuthorDate
	state.NoTrunk = opts.noTrunk
	state.UseOnto, state.OntoOldBase = needsOnto, ontoOldBase
	if s.Trunk.Branch != originalTrunk {
		if err := stack.Save(gitDir, sf); err != nil {
			return handleSaveError(cfg, err)
		}
	}
	if err := saveRebaseState(gitDir, state); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	rebaseResult := cascadeRebase(cascadeRebaseOpts{
		Cfg:                       cfg,
		Stack:                     s,
		Branches:                  branchesToRebase,
		StartAbsIdx:               startIdx,
		OriginalRefs:              originalRefs,
		NeedsOnto:                 needsOnto,
		OntoOldBase:               ontoOldBase,
		CommitterDateIsAuthorDate: opts.committerDateIsAuthorDate,
		TrunkRef:                  trunk.Ref,
	})

	if rebaseResult.Err != nil {
		cfg.Errorf("%v", rebaseResult.Err)
		if err := abortRebase(cfg, gitDir); err != nil {
			return err
		}
		return ErrSilent
	}

	if rebaseResult.Conflicted {
		cfg.Warningf("Rebasing %s onto %s — conflict", rebaseResult.ConflictBranch, rebaseResult.ConflictBase)

		state.Phase = "conflict"
		state.CurrentBranchIndex = rebaseResult.ConflictIdx
		state.ConflictBranch = rebaseResult.ConflictBranch
		state.RemainingBranches = rebaseResult.Remaining
		state.UseOnto, state.OntoOldBase = rebaseResult.NeedsOnto, rebaseResult.OntoOldBase
		if err := saveRebaseState(gitDir, state); err != nil {
			cfg.Errorf("failed to save conflict progress; run `gh stack rebase --abort`: %s", err)
			return ErrSilent
		}

		printConflictDetails(cfg, rebaseResult.ConflictBase)
		cfg.Printf("")

		cfg.Printf("Resolve conflicts on %s, then run `%s`",
			rebaseResult.ConflictBranch, cfg.ColorCyan("gh stack rebase --continue"))
		cfg.Printf("Or abort this operation with `%s`",
			cfg.ColorCyan("gh stack rebase --abort"))
		cfg.Printf("Run recovery in the original worktree: %s", ctx.Origin.Path)
		return ErrConflict
	}

	if unstacked := verifyStacked(s, trunk.Ref, startIdx, endIdx); len(unstacked) > 0 {
		reportUnstacked(cfg, trunk.Ref, unstacked)
		if err := abortRebase(cfg, gitDir); err != nil {
			return err
		}
		return ErrSilent
	}

	if err := finishOriginRebase(cfg, gitDir, state, sf, s); err != nil {
		return err
	}

	merged := s.MergedBranches()
	if len(merged) > 0 {
		names := make([]string, len(merged))
		for i, m := range merged {
			names[i] = m.Branch
		}
		cfg.Printf("Skipped %d merged %s: %s", len(merged), plural(len(merged), "branch", "branches"), strings.Join(names, ", "))
	}

	rangeDesc := "All branches in stack"
	if opts.downstack {
		rangeDesc = fmt.Sprintf("All downstack branches up to %s", anchor)
	} else if opts.upstack {
		rangeDesc = fmt.Sprintf("All upstack branches from %s", anchor)
	}

	if opts.noTrunk {
		cfg.Printf("%s rebased locally (without trunk)", rangeDesc)
	} else {
		cfg.Printf("%s rebased locally with %s", rangeDesc, trunk.Describe())
	}
	cfg.Printf("To push up your changes, run `%s`",
		cfg.ColorCyan("gh stack push"))

	return nil
}

func continueRebase(cfg *config.Config, gitDir string) error {
	state, err := loadRebaseState(gitDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.Errorf("no rebase in progress")
		} else {
			cfg.Errorf("reading rebase recovery state: %s", err)
		}
		return ErrSilent
	}
	if err := requireRebaseOrigin(cfg, gitDir, state); err != nil {
		return err
	}
	if state.Phase != "" && state.Phase != "conflict" && state.Phase != "complete" {
		cfg.Errorf("rebase stopped in phase %q; run `gh stack rebase --abort` in the original worktree", state.Phase)
		return ErrSilent
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	// Use the saved original branch to find the stack, since git may be in
	// a detached HEAD state during an active rebase.
	var s *stack.Stack
	if state.Worktrees != nil {
		s, err = rebaseStackFromState(sf, state)
	} else {
		s, err = resolveStack(sf, state.OriginalBranch, cfg)
	}
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("no stack found for branch %s", state.OriginalBranch)
	}
	if state.Phase == "complete" {
		return finishOriginRebase(cfg, gitDir, state, sf, s)
	}
	trunkRef := state.TrunkRef
	if trunkRef == "" {
		trunkRef = s.Trunk.Branch
	}
	trunkBase := state.TrunkSHA
	if trunkBase == "" {
		trunkBase = trunkRef
	}

	// Refresh PR state before selecting the base and cascading the remaining
	// branches. The queued flag is transient (not persisted), so it was lost
	// when the stack was reloaded from disk above. Without this, a queued
	// branch in the remaining cascade would be treated as active and its
	// frozen merge-queue branch would be rebased. Mirrors the syncStackPRs
	// call in runRebase before its cascade.
	_ = syncStackPRs(cfg, s)

	// The branch that had the conflict is stored in state; fall back to
	// looking it up by index for backwards compatibility with older state files.
	conflictBranch := state.ConflictBranch
	if conflictBranch == "" && state.CurrentBranchIndex >= 0 && state.CurrentBranchIndex < len(s.Branches) {
		conflictBranch = s.Branches[state.CurrentBranchIndex].Branch
	}

	cfg.Printf("Continuing rebase of stack, resuming from %s to %s",
		conflictBranch, s.Branches[len(s.Branches)-1].Branch)

	inProgress, err := git.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state: %w", err)
	}
	if inProgress {
		rebaseOpts := git.RebaseOpts{CommitterDateIsAuthorDate: state.CommitterDateIsAuthorDate}
		if err := git.RebaseContinue(rebaseOpts); err != nil {
			return fmt.Errorf("rebase continue failed — resolve remaining conflicts and try again: %w", err)
		}
	}

	var baseBranch string
	if state.UseOnto {
		// The --onto path targets the first non-merged ancestor, or trunk.
		baseBranch = trunkRef
		for j := state.CurrentBranchIndex - 1; j >= 0; j-- {
			if !s.Branches[j].IsMerged() {
				baseBranch = s.Branches[j].Branch
				break
			}
		}
	} else if state.CurrentBranchIndex > 0 {
		baseBranch = s.Branches[state.CurrentBranchIndex-1].Branch
	} else {
		baseBranch = trunkRef
	}
	cfg.Successf("Rebased %s onto %s", conflictBranch, baseBranch)

	// Rebase remaining branches using the shared cascade helper.
	if len(state.RemainingBranches) > 0 {
		// Validate all remaining branches still exist in the stack,
		// are in contiguous ascending order, and build the BranchRef slice.
		remainingRefs := make([]stack.BranchRef, 0, len(state.RemainingBranches))
		startAbsIdx := -1
		for i, name := range state.RemainingBranches {
			idx := s.IndexOf(name)
			if idx < 0 {
				return fmt.Errorf("branch %q from saved rebase state is no longer in the stack — the stack may have been modified since the rebase started; consider aborting with --abort", name)
			}
			if startAbsIdx < 0 {
				startAbsIdx = idx
			} else if idx != startAbsIdx+i {
				return fmt.Errorf("branch %q is at stack index %d, expected %d — the stack may have been reordered since the rebase started; consider aborting with --abort", name, idx, startAbsIdx+i)
			}
			remainingRefs = append(remainingRefs, s.Branches[idx])
		}

		result := cascadeRebase(cascadeRebaseOpts{
			Cfg:                       cfg,
			Stack:                     s,
			Branches:                  remainingRefs,
			StartAbsIdx:               startAbsIdx,
			OriginalRefs:              state.OriginalRefs,
			NeedsOnto:                 state.UseOnto,
			OntoOldBase:               state.OntoOldBase,
			CommitterDateIsAuthorDate: state.CommitterDateIsAuthorDate,
			TrunkRef:                  trunkBase,
		})

		if result.Err != nil {
			cfg.Errorf("%v", result.Err)
			if err := abortRebase(cfg, gitDir); err != nil {
				return err
			}
			return ErrSilent
		}

		if result.Conflicted {
			cfg.Warningf("Rebasing %s onto %s — conflict", result.ConflictBranch, result.ConflictBase)

			state.Phase = "conflict"
			state.CurrentBranchIndex = result.ConflictIdx
			state.ConflictBranch = result.ConflictBranch
			state.RemainingBranches = result.Remaining
			state.UseOnto = result.NeedsOnto
			state.OntoOldBase = result.OntoOldBase
			if err := saveRebaseState(gitDir, state); err != nil {
				cfg.Warningf("failed to save rebase state: %s", err)
			}

			printConflictDetails(cfg, result.ConflictBase)
			cfg.Printf("")
			cfg.Printf("Resolve conflicts on %s, then run `%s`",
				result.ConflictBranch, cfg.ColorCyan("gh stack rebase --continue"))
			cfg.Printf("Or abort this operation with `%s`",
				cfg.ColorCyan("gh stack rebase --abort"))
			return ErrConflict
		}
	}

	verifyStart, verifyEnd := state.StartIndex, state.EndIndex
	if verifyEnd <= verifyStart {
		verifyStart, verifyEnd = 0, len(s.Branches)
		if state.NoTrunk {
			verifyStart = 1
		}
	}
	if unstacked := verifyStacked(s, trunkBase, verifyStart, verifyEnd); len(unstacked) > 0 {
		reportUnstacked(cfg, trunkRef, unstacked)
		if err := abortRebase(cfg, gitDir); err != nil {
			return err
		}
		return ErrSilent
	}

	if err := finishOriginRebase(cfg, gitDir, state, sf, s); err != nil {
		return err
	}

	if state.NoTrunk {
		cfg.Printf("All branches in stack rebased locally (without trunk)")
	} else if state.TrunkSHA != "" {
		cfg.Printf("All branches in stack rebased locally with %s (%s)", trunkRef, short(state.TrunkSHA))
	} else {
		cfg.Printf("All branches in stack rebased locally with %s", trunkRef)
	}
	cfg.Printf("To push up your changes and open/update the stack of PRs, run `%s`",
		cfg.ColorCyan("gh stack submit"))

	return nil
}

func abortRebase(cfg *config.Config, gitDir string) error {
	state, err := loadRebaseState(gitDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.Errorf("no rebase in progress")
		} else {
			cfg.Errorf("reading rebase recovery state: %s", err)
		}
		return ErrSilent
	}
	if err := requireRebaseOrigin(cfg, gitDir, state); err != nil {
		return err
	}
	if state.OriginalStack != nil {
		sf, err := stack.Load(gitDir)
		if err != nil {
			return err
		}
		if _, err := rebaseStackFromState(sf, state); err != nil {
			return err
		}
	}
	inProgress, err := git.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state: %w", err)
	}
	for branch := range state.OriginalRefs {
		if _, err := git.BranchExists(branch); err != nil {
			return fmt.Errorf("checking branch %s before restoring: %w", branch, err)
		}
	}
	state.Phase = "restoring"
	if err := saveRebaseState(gitDir, state); err != nil {
		cfg.Errorf("saving recovery state before restoration: %s", err)
		return ErrSilent
	}
	if inProgress {
		if err := git.RebaseAbort(); err != nil {
			cfg.Errorf("aborting rebase; recovery state was retained: %s", err)
			return ErrSilent
		}
	}
	root, err := git.RootDir()
	if err != nil {
		cfg.Errorf("finding recovery worktree: %s", err)
		return ErrSilent
	}
	if err := worktree.CheckClean(git.CurrentOps(), root); err != nil {
		cfg.Errorf("recovery state was retained: %s", err)
		return ErrSilent
	}

	var restoreErrors []string
	for branch, sha := range state.OriginalRefs {
		if current, err := git.RevParse(branch); err == nil && current == sha {
			continue
		}
		if err := git.CheckoutBranch(branch); err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("checkout %s: %s", branch, err))
			continue
		}
		if err := git.ResetHard(sha); err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("reset %s: %s", branch, err))
		}
	}
	if err := git.CheckoutBranch(state.OriginalBranch); err != nil {
		restoreErrors = append(restoreErrors, fmt.Sprintf("restoring original checkout: %s", err))
	}
	if len(restoreErrors) > 0 {
		cfg.Warningf("Some branches could not be fully restored; recovery state was retained:")
		for _, e := range restoreErrors {
			cfg.Printf("  %s", e)
		}
		return ErrSilent
	}
	if state.OriginalStack != nil {
		if err := restoreWorktreeRebaseMetadata(gitDir, state); err != nil {
			cfg.Errorf("restoring metadata; recovery state was retained: %s", err)
			return ErrSilent
		}
	}
	if err := clearRebaseState(gitDir); err != nil {
		cfg.Errorf("branches restored, but recovery state could not be cleared: %s", err)
		return ErrSilent
	}

	cfg.Successf("Rebase aborted and branches restored")
	return nil
}

func newWorktreeRebaseState(s *stack.Stack, ctx *worktree.Context, originalBranch string, refs map[string]string, trunk trunkTarget, start, end int) *rebaseState {
	snapshot := *s
	snapshot.Branches = append([]stack.BranchRef{}, s.Branches...)
	return &rebaseState{
		ExecutionMode:      originOnlyRebaseMode,
		Phase:              "applying",
		Worktrees:          ctx,
		StackID:            s.ID,
		StackTrunk:         s.Trunk.Branch,
		StackBranches:      s.BranchNames(),
		OriginalStack:      &snapshot,
		OriginalBranch:     originalBranch,
		OriginalRefs:       refs,
		CurrentBranchIndex: start,
		StartIndex:         start,
		EndIndex:           end,
		TrunkRef:           trunk.Ref,
		TrunkSHA:           trunk.SHA,
	}
}

func requireRebaseOrigin(cfg *config.Config, dir string, state *rebaseState) error {
	localDir, err := git.GitDir()
	if err != nil {
		return err
	}
	if state.Worktrees == nil {
		if !worktree.SamePath(localDir, dir) {
			cfg.Errorf("legacy rebase recovery must run in its original worktree (Git directory %s)", dir)
			return ErrRebaseActive
		}
	} else {
		ops, err := state.Worktrees.OriginOps()
		if err != nil {
			cfg.Errorf("%s", err)
			return ErrRebaseActive
		}
		originDir, err := ops.GitDir()
		if err != nil {
			return err
		}
		if !worktree.SamePath(localDir, originDir) {
			cfg.Errorf("rebase recovery must run in its original worktree: %s", state.Worktrees.Origin.Path)
			cfg.Printf("Return there before running `gh stack rebase --continue` or `gh stack rebase --abort`")
			return ErrRebaseActive
		}
	}
	branches := append([]string{state.OriginalBranch, state.ConflictBranch}, state.RemainingBranches...)
	for name := range state.OriginalRefs {
		branches = append(branches, name)
	}
	if err := requireLocalBranches(cfg, branches); err != nil {
		return err
	}
	if state.Phase == "complete" {
		root, err := git.RootDir()
		if err != nil {
			return err
		}
		if err := worktree.CheckClean(git.CurrentOps(), root); err != nil {
			cfg.Errorf("recovery state was retained: %s", err)
			return ErrSilent
		}
	}
	return nil
}

func rebaseStackFromState(sf *stack.StackFile, state *rebaseState) (*stack.Stack, error) {
	switch state.Phase {
	case "applying", "conflict", "complete", "restoring":
	default:
		return nil, fmt.Errorf("unknown saved rebase phase %q", state.Phase)
	}
	var target *stack.Stack
	for i := range sf.Stacks {
		s := &sf.Stacks[i]
		if s.Trunk.Branch != state.StackTrunk || !slices.Equal(s.BranchNames(), state.StackBranches) {
			continue
		}
		if (state.Phase == "applying" || state.Phase == "conflict") && state.StackID != "" && s.ID != "" && state.StackID != s.ID {
			continue
		}
		if target != nil {
			return nil, fmt.Errorf("saved rebase matches multiple stacks; resolve the catalog before continuing")
		}
		target = s
	}
	if target == nil {
		return nil, fmt.Errorf("the stack changed since this rebase started; restore its original membership or run gh stack rebase --abort")
	}
	if state.StartIndex < 0 || state.EndIndex > len(target.Branches) ||
		state.StartIndex > state.EndIndex || state.CurrentBranchIndex < state.StartIndex ||
		state.CurrentBranchIndex > state.EndIndex {
		return nil, fmt.Errorf("invalid branch range in saved rebase state")
	}
	return target, nil
}

func restoreWorktreeRebaseMetadata(dir string, state *rebaseState) error {
	sf, err := stack.Load(dir)
	if err != nil {
		return err
	}
	target, err := rebaseStackFromState(sf, state)
	if err != nil {
		return err
	}
	if !slices.Equal(state.OriginalStack.BranchNames(), target.BranchNames()) {
		return fmt.Errorf("original stack snapshot does not match the recovery target")
	}
	target.Trunk.Head = state.OriginalStack.Trunk.Head
	for i, before := range state.OriginalStack.Branches {
		target.Branches[i].Base = before.Base
		target.Branches[i].Head = before.Head
		if sha, err := git.RevParse(before.Branch); err == nil {
			target.Branches[i].Head = sha
		} else if !before.IsMerged() {
			return fmt.Errorf("reading restored branch %s: %w", before.Branch, err)
		}
	}
	return stack.Save(dir, sf)
}

func finishOriginRebase(cfg *config.Config, dir string, state *rebaseState, sf *stack.StackFile, s *stack.Stack) error {
	state.Phase = "complete"
	state.RemainingBranches = nil
	if err := saveRebaseState(dir, state); err != nil {
		cfg.Errorf("saving completed rebase; recovery state was retained: %s", err)
		return ErrSilent
	}
	if err := git.CheckoutBranch(state.OriginalBranch); err != nil {
		cfg.Errorf("restoring original checkout; recovery state was retained: %s", err)
		return ErrSilent
	}
	updateBaseSHAsWithTrunk(s, state.TrunkSHA)
	_ = syncStackPRs(cfg, s)
	if err := stack.Save(dir, sf); err != nil {
		return handleSaveError(cfg, err)
	}
	if err := clearRebaseState(dir); err != nil {
		cfg.Errorf("rebase completed but recovery state could not be cleared: %s", err)
		return ErrSilent
	}
	return nil
}

func saveRebaseState(gitDir string, state *rebaseState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("error serializing rebase state: %w", err)
	}
	if err := stack.WriteAtomic(filepath.Join(gitDir, rebaseStateFile), data); err != nil {
		return fmt.Errorf("error writing rebase state: %w", err)
	}
	return nil
}

func loadRebaseState(gitDir string) (*rebaseState, error) {
	data, err := stack.ReadStateFile(filepath.Join(gitDir, rebaseStateFile))
	if err != nil {
		return nil, err
	}
	var state rebaseState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	origin := "its original worktree"
	if state.Worktrees != nil && state.Worktrees.Origin.Path != "" {
		origin = fmt.Sprintf("worktree %q", state.Worktrees.Origin.Path)
	}
	switch state.ExecutionMode {
	case originOnlyRebaseMode:
		if state.Worktrees == nil {
			return nil, fmt.Errorf("origin-only rebase journal has no recorded worktree; recovery state was retained")
		}
	case "":
		if state.Worktrees != nil {
			return nil, fmt.Errorf("this rebase journal uses a different execution lifecycle; use the matching gh-stack build in %s to continue or abort", origin)
		}
	default:
		return nil, fmt.Errorf("unsupported rebase execution mode %q; use the matching gh-stack build in %s to continue or abort", state.ExecutionMode, origin)
	}
	return &state, nil
}

func clearRebaseState(gitDir string) error {
	err := os.Remove(filepath.Join(gitDir, rebaseStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func printConflictDetails(cfg *config.Config, branch string) {
	printConflictDetailsWithContinue(cfg, branch, "gh stack rebase --continue")
}

func printConflictDetailsWithContinue(cfg *config.Config, branch string, continueCmd string) {
	printConflictDetailsAt(cfg, git.CurrentOps(), "", branch, continueCmd)
}

func printConflictDetailsAt(cfg *config.Config, ops git.Ops, path, branch, continueCmd string) {
	if path != "" {
		cfg.Printf("Conflict worktree: %s", path)
		cfg.Printf("Resolve and stage files in that worktree; continuation may be run from any worktree.")
	}
	files, err := ops.ConflictedFiles()
	if err == nil && len(files) > 0 {
		cfg.Printf("")
		cfg.Printf("%s", cfg.ColorBold("Conflicted files:"))
		for _, f := range files {
			info, err := ops.FindConflictMarkers(f)
			if err != nil || len(info.Sections) == 0 {
				cfg.Printf("  %s %s", cfg.ColorWarning("C"), f)
				continue
			}
			for _, sec := range info.Sections {
				cfg.Printf("  %s %s (lines %d–%d)",
					cfg.ColorWarning("C"), f, sec.StartLine, sec.EndLine)
			}
		}
	}

	cfg.Printf("")
	cfg.Printf("%s", cfg.ColorBold("To resolve:"))
	cfg.Printf("  1. Open each conflicted file and look for conflict markers:")
	cfg.Printf("     %s  (incoming changes from %s)", cfg.ColorCyan("<<<<<<< HEAD"), branch)
	cfg.Printf("     %s", cfg.ColorCyan("======="))
	cfg.Printf("     %s  (changes being rebased)", cfg.ColorCyan(">>>>>>>"))
	cfg.Printf("  2. Edit the file to keep the desired changes and remove the markers")
	cfg.Printf("  3. Stage resolved files: `%s`", cfg.ColorCyan("git add <file>"))
	cfg.Printf("  4. Continue:  `%s`", cfg.ColorCyan(continueCmd))
}
