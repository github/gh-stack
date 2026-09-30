package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cligit "github.com/cli/cli/v2/git"
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
	RebaseBase                string            `json:"rebaseBase,omitempty"`
	RebaseOldBase             string            `json:"rebaseOldBase,omitempty"`
	RebaseOnto                bool              `json:"rebaseOnto,omitempty"`
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
branch 3 onto branch 2, etc.).`,
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
		cfg.Errorf("not a git repository")
		return ErrNotInStack
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

	anchor := currentBranch
	if opts.branch != "" {
		anchor = opts.branch
	}
	currentIdx := s.IndexOf(anchor)
	if currentIdx < 0 {
		currentIdx = 0
	}
	startIdx, endIdx := 0, len(s.Branches)
	if endIdx == 0 {
		cfg.Printf("No branches to rebase")
		return nil
	}
	if opts.downstack {
		endIdx = currentIdx + 1
	}
	if opts.upstack {
		startIdx = currentIdx
	}
	if opts.noTrunk && startIdx < 1 {
		startIdx = 1
	}
	branchesToRebase := s.Branches[startIdx:endIdx]
	if len(branchesToRebase) == 0 {
		cfg.Printf("No branches to rebase")
		return nil
	}
	_ = syncStackPRs(cfg, s)
	ctx, err := worktree.New()
	if err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}
	required := rebaseBranchNames(branchesToRebase)

	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return ErrSilent
	}

	var trunk trunkTarget
	if !opts.noTrunk {
		// Resolve remote for fetch and trunk comparison
		remote, err := pickRemote(cfg, anchor, opts.remote)
		if err != nil {
			if !errors.Is(err, errInterrupt) {
				cfg.Errorf("%s", err)
			}
			return ErrSilent
		}

		// Fast-forward stack branches that are behind their remote tracking branch.
		if err := git.FetchBranches(remote, activeBranchNames(s)); err != nil {
			cfg.Errorf("failed to fetch stack branches from %s: %v", remote, err)
			return ErrSilent
		}
		planned := planFastForwardBranches(s, remote)
		for _, forward := range planned {
			required = append(required, forward.Branch)
		}
		if err := ctx.Preflight(required); err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
		trunk, err = resolveTrunkTarget(cfg, s, remote, currentBranch, trunkResolveOptions{Worktrees: ctx})
		if err != nil {
			return err
		}
		if _, err := fastForwardBranches(cfg, planned, ctx); err != nil {
			cfg.Errorf("%s", err)
			return ErrSilent
		}
	} else if err := ctx.Preflight(required); err != nil {
		cfg.Errorf("%s", err)
		return ErrSilent
	}

	cfg.Printf("Stack detected: %s", s.DisplayChain())

	if opts.upstack && currentIdx >= 0 && s.Branches[currentIdx].IsMerged() {
		cfg.Warningf("Current branch %q has already been merged", currentBranch)
	}

	cfg.Printf("Rebasing branches in order, starting from %s to %s",
		branchesToRebase[0].Branch, branchesToRebase[len(branchesToRebase)-1].Branch)

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
		TrunkSHA:                  trunk.SHA,
		Worktrees:                 ctx,
		State:                     state,
		StateDir:                  gitDir,
	})

	if rebaseResult.Err != nil {
		cfg.Errorf("%v", rebaseResult.Err)
		if err := rollbackWorktreeRebase(cfg, gitDir, state); err != nil {
			return errors.Join(ErrSilent, err)
		}
		return ErrSilent
	}

	if rebaseResult.Conflicted {
		cfg.Warningf("Rebasing %s onto %s — conflict", rebaseResult.ConflictBranch, rebaseResult.ConflictBase)

		printWorktreeConflict(cfg, state, rebaseResult.ConflictBase)
		cfg.Printf("")

		cfg.Printf("Resolve conflicts on %s, then run `%s`",
			rebaseResult.ConflictBranch, cfg.ColorCyan("gh stack rebase --continue"))
		cfg.Printf("Or abort this operation with `%s`",
			cfg.ColorCyan("gh stack rebase --abort"))
		return ErrConflict
	}

	if err := finishWorktreeRebase(cfg, gitDir, state, sf, s); err != nil {
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
	if state.Worktrees != nil {
		return continueWorktreeRebase(cfg, gitDir, state)
	}
	if err := validateLegacyRebasePhase(state, true); err != nil {
		return err
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	// Use the saved original branch to find the stack, since git may be in
	// a detached HEAD state during an active rebase.
	s, err := resolveStack(sf, state.OriginalBranch, cfg)
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("no stack found for branch %s", state.OriginalBranch)
	}
	if state.Phase == "complete" {
		return finishLegacyRebase(cfg, gitDir, state, sf, s)
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
			if err := rollbackLegacyRebase(cfg, gitDir, state); err != nil {
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
				cfg.Errorf("failed to save rebase state: %s", err)
				return errors.Join(ErrSilent, err)
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
		if err := rollbackLegacyRebase(cfg, gitDir, state); err != nil {
			return err
		}
		return ErrSilent
	}

	return finishLegacyRebase(cfg, gitDir, state, sf, s)
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
	if state.Worktrees != nil {
		if err := rollbackWorktreeRebase(cfg, gitDir, state); err != nil {
			return errors.Join(ErrSilent, err)
		}
		cfg.Successf("Rebase aborted and branches restored")
		return nil
	}
	if err := validateLegacyRebasePhase(state, false); err != nil {
		return err
	}
	if err := rollbackLegacyRebase(cfg, gitDir, state); err != nil {
		return err
	}
	cfg.Successf("Rebase aborted and branches restored")
	return nil
}

func validateLegacyRebasePhase(state *rebaseState, cont bool) error {
	switch state.Phase {
	case "", "conflict", "complete":
		return nil
	case "applying", "restoring":
		if !cont {
			return nil
		}
		return fmt.Errorf("legacy rebase is in phase %q; run `gh stack rebase --abort` to restore the stack", state.Phase)
	default:
		return fmt.Errorf("unknown legacy rebase phase %q; recovery state was retained", state.Phase)
	}
}

func rollbackLegacyRebase(cfg *config.Config, gitDir string, state *rebaseState) error {
	inProgress, err := git.IsRebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state: %w", err)
	}
	for branch := range state.OriginalRefs {
		exists, err := git.BranchExists(branch)
		if err != nil {
			return fmt.Errorf("checking branch %s before restoring: %w", branch, err)
		}
		if exists {
			if _, err := git.RevParse(branch); err != nil {
				return fmt.Errorf("reading branch %s before restoring: %w", branch, err)
			}
		}
	}
	root, err := git.RootDir()
	if err != nil {
		return fmt.Errorf("finding recovery worktree: %w", err)
	}
	if !inProgress || state.Phase == "complete" {
		if err := worktree.CheckClean(git.CurrentOps(), root); err != nil {
			return err
		}
	}
	state.Phase = "restoring"
	if err := saveRebaseState(gitDir, state); err != nil {
		return err
	}
	if inProgress {
		if err := git.RebaseAbort(); err != nil {
			cfg.Errorf("aborting rebase; recovery state was retained: %s", err)
			return errors.Join(ErrSilent, err)
		}
	}
	if err := worktree.CheckClean(git.CurrentOps(), root); err != nil {
		cfg.Errorf("recovery state was retained: %s", err)
		return errors.Join(ErrSilent, err)
	}

	if err := restoreRebaseRefs(cfg, state.OriginalBranch, state.OriginalRefs); err != nil {
		return err
	}
	return clearRebaseState(gitDir)
}

func restoreRebaseCheckout(branch string) error {
	current, err := git.CurrentBranch()
	if err != nil && !errors.Is(err, cligit.ErrNotOnAnyBranch) {
		return fmt.Errorf("reading original checkout: %w", err)
	}
	if err == nil && current == branch {
		return nil
	}
	root, err := git.RootDir()
	if err != nil {
		return err
	}
	if err := worktree.CheckClean(git.CurrentOps(), root); err != nil {
		return err
	}
	if err := git.CheckoutBranch(branch); err != nil {
		return fmt.Errorf("restoring original checkout %s: %w", branch, err)
	}
	return nil
}

func finishLegacyRebase(cfg *config.Config, gitDir string, state *rebaseState, sf *stack.StackFile, s *stack.Stack) error {
	if state.Phase != "complete" {
		state.Phase = "complete"
		state.RemainingBranches = nil
		if err := saveRebaseState(gitDir, state); err != nil {
			return err
		}
	}
	if err := restoreRebaseCheckout(state.OriginalBranch); err != nil {
		return err
	}
	updateBaseSHAsWithTrunk(s, state.TrunkSHA)
	_ = syncStackPRs(cfg, s)
	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}
	if err := clearRebaseState(gitDir); err != nil {
		return fmt.Errorf("rebase completed but recovery state could not be cleared: %w", err)
	}

	trunkRef := state.TrunkRef
	if trunkRef == "" {
		trunkRef = s.Trunk.Branch
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
	if err := validateRebaseExecutionMode(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func validateRebaseExecutionMode(state *rebaseState) error {
	if state.ExecutionMode == "" {
		return nil
	}
	origin := "its original worktree"
	if state.Worktrees != nil && state.Worktrees.Origin.Path != "" {
		origin = fmt.Sprintf("worktree %q", state.Worktrees.Origin.Path)
	}
	if state.ExecutionMode == originOnlyRebaseMode {
		return fmt.Errorf("origin-only rebase journal requires the matching layer3 gh-stack build in %s to continue or abort; recovery state was retained", origin)
	}
	return fmt.Errorf("unsupported rebase execution mode %q; use the matching gh-stack build in %s to continue or abort; recovery state was retained", state.ExecutionMode, origin)
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
