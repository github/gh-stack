package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type rebaseOptions struct {
	branch                    string
	downstack                 bool
	upstack                   bool
	cont                      bool
	abort                     bool
	remote                    string
	committerDateIsAuthorDate bool
}

type rebaseState struct {
	CurrentBranchIndex        int               `json:"currentBranchIndex"`
	ConflictBranch            string            `json:"conflictBranch"`
	RemainingBranches         []string          `json:"remainingBranches"`
	OriginalBranch            string            `json:"originalBranch"`
	OriginalRefs              map[string]string `json:"originalRefs"`
	UseOnto                   bool              `json:"useOnto,omitempty"`
	OntoOldBase               string            `json:"ontoOldBase,omitempty"`
	CommitterDateIsAuthorDate bool              `json:"committerDateIsAuthorDate,omitempty"`
}

const rebaseStateFile = "gh-stack-rebase-state"

func RebaseCmd(cfg *config.Config) *cobra.Command {
	opts := &rebaseOptions{}

	cmd := &cobra.Command{
		Use:   "rebase [branch]",
		Short: "Rebase a stack of branches",
		Long: `Pull from remote and do a cascading rebase across the stack.

Ensures that each branch in the stack has the tip of the previous
layer in its commit history, rebasing if necessary.`,
		Example: `  # Rebase the entire stack
  $ gh stack rebase

  # Only rebase from trunk to the current branch
  $ gh stack rebase --downstack

  # Only rebase from current branch to the top
  $ gh stack rebase --upstack

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
	cmd.Flags().BoolVar(&opts.cont, "continue", false, "Continue rebase after resolving conflicts")
	cmd.Flags().BoolVar(&opts.abort, "abort", false, "Abort rebase and restore all branches")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "Remote to fetch from (defaults to auto-detected remote)")
	cmd.Flags().BoolVar(&opts.committerDateIsAuthorDate, "committer-date-is-author-date", false, "Set the committer date to the author date during rebase")
	cmd.Flags().BoolVar(&opts.committerDateIsAuthorDate, "preserve-dates", false, "Alias for --committer-date-is-author-date")

	return cmd
}

func runRebase(cfg *config.Config, opts *rebaseOptions) error {
	gitDir, err := git.GitDir()
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
		return ErrNotInStack
	}
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return ErrSilent
	}

	// Resolve remote for fetch and trunk comparison
	remote, err := pickRemote(cfg, currentBranch, opts.remote)
	if err != nil {
		if !errors.Is(err, errInterrupt) {
			cfg.Errorf("%s", err)
		}
		return ErrSilent
	}

	if err := git.Fetch(remote); err != nil {
		cfg.Warningf("Failed to fetch %s: %v", remote, err)
	} else {
		cfg.Successf("Fetched %s", remote)
	}

	// Fast-forward trunk so the cascade rebase targets the latest upstream.
	fastForwardTrunk(cfg, s.Trunk.Branch, remote, currentBranch)

	// Fast-forward stack branches that are behind their remote tracking branch.
	fastForwardBranches(cfg, s, remote, currentBranch)

	cfg.Printf("Stack detected: %s", s.DisplayChain())

	currentIdx := s.IndexOf(currentBranch)
	if currentIdx < 0 {
		currentIdx = 0
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

	// Get --onto state from merged/queued branches below the rebase range.
	// Ensures that when --upstack excludes skipped branches, we still check
	// the immediate predecessor and use --onto if needed.
	needsOnto := false
	var ontoOldBase string
	if startIdx > 0 {
		prev := s.Branches[startIdx-1]
		if prev.IsSkipped() {
			if sha, ok := originalRefs[prev.Branch]; ok {
				needsOnto = true
				ontoOldBase = sha
			}
		}
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
	})

	if rebaseResult.Err != nil {
		cfg.Errorf("%v", rebaseResult.Err)
		return ErrSilent
	}

	if rebaseResult.Conflicted {
		cfg.Warningf("Rebasing %s onto %s — conflict", rebaseResult.ConflictBranch, rebaseResult.ConflictBase)

		state := &rebaseState{
			CurrentBranchIndex:        rebaseResult.ConflictIdx,
			ConflictBranch:            rebaseResult.ConflictBranch,
			RemainingBranches:         rebaseResult.Remaining,
			OriginalBranch:            currentBranch,
			OriginalRefs:              originalRefs,
			UseOnto:                   rebaseResult.NeedsOnto,
			OntoOldBase:               rebaseResult.OntoOldBase,
			CommitterDateIsAuthorDate: opts.committerDateIsAuthorDate,
		}
		if err := saveRebaseState(gitDir, state); err != nil {
			cfg.Warningf("failed to save rebase state: %s", err)
		}

		printConflictDetails(cfg, rebaseResult.ConflictBase)
		cfg.Printf("")

		cfg.Printf("Resolve conflicts on %s, then run `%s`",
			rebaseResult.ConflictBranch, cfg.ColorCyan("gh stack rebase --continue"))
		cfg.Printf("Or abort this operation with `%s`",
			cfg.ColorCyan("gh stack rebase --abort"))
		return ErrConflict
	}

	_ = git.CheckoutBranch(currentBranch)

	updateBaseSHAs(s)

	_ = syncStackPRs(cfg, s)

	stack.SaveNonBlocking(gitDir, sf)

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
		rangeDesc = fmt.Sprintf("All downstack branches up to %s", currentBranch)
	} else if opts.upstack {
		rangeDesc = fmt.Sprintf("All upstack branches from %s", currentBranch)
	}

	cfg.Printf("%s rebased locally with %s", rangeDesc, s.Trunk.Branch)
	cfg.Printf("To push up your changes, run `%s`",
		cfg.ColorCyan("gh stack push"))

	return nil
}

func continueRebase(cfg *config.Config, gitDir string) error {
	state, err := loadRebaseState(gitDir)
	if err != nil {
		cfg.Errorf("no rebase in progress")
		return ErrSilent
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

	// The branch that had the conflict is stored in state; fall back to
	// looking it up by index for backwards compatibility with older state files.
	conflictBranch := state.ConflictBranch
	if conflictBranch == "" && state.CurrentBranchIndex >= 0 && state.CurrentBranchIndex < len(s.Branches) {
		conflictBranch = s.Branches[state.CurrentBranchIndex].Branch
	}

	cfg.Printf("Continuing rebase of stack, resuming from %s to %s",
		conflictBranch, s.Branches[len(s.Branches)-1].Branch)

	if git.IsRebaseInProgress() {
		rebaseOpts := git.RebaseOpts{CommitterDateIsAuthorDate: state.CommitterDateIsAuthorDate}
		if err := git.RebaseContinue(rebaseOpts); err != nil {
			return fmt.Errorf("rebase continue failed — resolve remaining conflicts and try again: %w", err)
		}
	}

	var baseBranch string
	if state.UseOnto {
		// The --onto path targets the first non-skipped ancestor, or trunk.
		baseBranch = s.Trunk.Branch
		for j := state.CurrentBranchIndex - 1; j >= 0; j-- {
			if !s.Branches[j].IsSkipped() {
				baseBranch = s.Branches[j].Branch
				break
			}
		}
	} else if state.CurrentBranchIndex > 0 {
		baseBranch = s.Branches[state.CurrentBranchIndex-1].Branch
	} else {
		baseBranch = s.Trunk.Branch
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
		})

		if result.Err != nil {
			cfg.Errorf("%v", result.Err)
			return ErrSilent
		}

		if result.Conflicted {
			cfg.Warningf("Rebasing %s onto %s — conflict", result.ConflictBranch, result.ConflictBase)

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

	clearRebaseState(gitDir)
	_ = git.CheckoutBranch(state.OriginalBranch)

	updateBaseSHAs(s)

	_ = syncStackPRs(cfg, s)

	stack.SaveNonBlocking(gitDir, sf)

	cfg.Printf("All branches in stack rebased locally with %s", s.Trunk.Branch)
	cfg.Printf("To push up your changes and open/update the stack of PRs, run `%s`",
		cfg.ColorCyan("gh stack submit"))

	return nil
}

func abortRebase(cfg *config.Config, gitDir string) error {
	state, err := loadRebaseState(gitDir)
	if err != nil {
		cfg.Errorf("no rebase in progress")
		return ErrSilent
	}

	if git.IsRebaseInProgress() {
		_ = git.RebaseAbort()
	}

	var restoreErrors []string
	for branch, sha := range state.OriginalRefs {
		if err := git.CheckoutBranch(branch); err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("checkout %s: %s", branch, err))
			continue
		}
		if err := git.ResetHard(sha); err != nil {
			restoreErrors = append(restoreErrors, fmt.Sprintf("reset %s: %s", branch, err))
		}
	}

	_ = git.CheckoutBranch(state.OriginalBranch)
	clearRebaseState(gitDir)

	if len(restoreErrors) > 0 {
		cfg.Warningf("Rebase aborted but some branches could not be fully restored:")
		for _, e := range restoreErrors {
			cfg.Printf("  %s", e)
		}
		return ErrSilent
	}

	cfg.Successf("Rebase aborted and branches restored")
	return nil
}

func saveRebaseState(gitDir string, state *rebaseState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("error serializing rebase state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, rebaseStateFile), data, 0644); err != nil {
		return fmt.Errorf("error writing rebase state: %w", err)
	}
	return nil
}

func loadRebaseState(gitDir string) (*rebaseState, error) {
	data, err := os.ReadFile(filepath.Join(gitDir, rebaseStateFile))
	if err != nil {
		return nil, err
	}
	var state rebaseState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func clearRebaseState(gitDir string) {
	_ = os.Remove(filepath.Join(gitDir, rebaseStateFile))
}

func printConflictDetails(cfg *config.Config, branch string) {
	printConflictDetailsWithContinue(cfg, branch, "gh stack rebase --continue")
}

func printConflictDetailsWithContinue(cfg *config.Config, branch string, continueCmd string) {
	files, err := git.ConflictedFiles()
	if err == nil && len(files) > 0 {
		cfg.Printf("")
		cfg.Printf("%s", cfg.ColorBold("Conflicted files:"))
		for _, f := range files {
			info, err := git.FindConflictMarkers(f)
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
