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
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type rebaseOptions struct {
	branch    string
	downstack bool
	upstack   bool
	cont      bool
	abort     bool
	remote    string
}

type rebaseState struct {
	CurrentBranchIndex int               `json:"currentBranchIndex"`
	ConflictBranch     string            `json:"conflictBranch"`
	RemainingBranches  []string          `json:"remainingBranches"`
	OriginalBranch     string            `json:"originalBranch"`
	OriginalRefs       map[string]string `json:"originalRefs"`
	UseOnto            bool              `json:"useOnto,omitempty"`
	OntoOldBase        string            `json:"ontoOldBase,omitempty"`
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
		Example: `  $ gh stack rebase
  $ gh stack rebase --downstack
  $ gh stack rebase --continue
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
	trunk := s.Trunk.Branch
	localSHA, remoteSHA := "", ""
	trunkRefs, trunkErr := git.RevParseMulti([]string{trunk, remote + "/" + trunk})
	if trunkErr == nil {
		localSHA, remoteSHA = trunkRefs[0], trunkRefs[1]
	}

	if trunkErr == nil && localSHA != remoteSHA {
		isAncestor, err := git.IsAncestor(localSHA, remoteSHA)
		if err != nil {
			cfg.Warningf("Could not determine fast-forward status for %s: %v", trunk, err)
		} else if !isAncestor {
			cfg.Warningf("Trunk %s has diverged from %s — skipping trunk update", trunk, remote)
		} else if currentBranch == trunk {
			if err := git.MergeFF(remote + "/" + trunk); err != nil {
				cfg.Warningf("Failed to fast-forward %s: %v", trunk, err)
			} else {
				cfg.Successf("Trunk %s fast-forwarded to %s", trunk, short(remoteSHA))
			}
		} else {
			if err := updateBranchRef(trunk, remoteSHA); err != nil {
				cfg.Warningf("Failed to fast-forward %s: %v", trunk, err)
			} else {
				cfg.Successf("Trunk %s fast-forwarded to %s", trunk, short(remoteSHA))
			}
		}
	}

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
	syncStackPRs(cfg, s)

	branchNames := make([]string, len(s.Branches))
	for i, b := range s.Branches {
		branchNames[i] = b.Branch
	}
	originalRefs, err := git.RevParseMap(branchNames)
	if err != nil {
		cfg.Errorf("failed to resolve branch SHAs: %s", err)
		return ErrSilent
	}

	// Track --onto rebase state for squash-merged branches.
	needsOnto := false
	var ontoOldBase string

	for i, br := range branchesToRebase {
		var base string
		absIdx := startIdx + i
		if absIdx == 0 {
			base = s.Trunk.Branch
		} else {
			base = s.Branches[absIdx-1].Branch
		}

		// Skip branches whose PRs have already been merged (e.g. via squash).
		// Record state so subsequent branches can use --onto rebase.
		if br.IsMerged() {
			ontoOldBase = originalRefs[br.Branch]
			needsOnto = true
			cfg.Successf("Skipping %s (PR %s merged)", br.Branch, cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
			continue
		}

		if needsOnto {
			// Find the proper --onto target: the first non-merged ancestor, or trunk.
			newBase := s.Trunk.Branch
			for j := absIdx - 1; j >= 0; j-- {
				b := s.Branches[j]
				if !b.IsMerged() {
					newBase = b.Branch
					break
				}
			}

			if err := git.RebaseOnto(newBase, ontoOldBase, br.Branch); err != nil {
				cfg.Warningf("Rebasing %s onto %s — conflict", br.Branch, newBase)

				remaining := make([]string, 0)
				for j := i + 1; j < len(branchesToRebase); j++ {
					remaining = append(remaining, branchesToRebase[j].Branch)
				}

				state := &rebaseState{
					CurrentBranchIndex: absIdx,
					ConflictBranch:     br.Branch,
					RemainingBranches:  remaining,
					OriginalBranch:     currentBranch,
					OriginalRefs:       originalRefs,
					UseOnto:            true,
					OntoOldBase:        originalRefs[br.Branch],
				}
				if err := saveRebaseState(gitDir, state); err != nil {
					cfg.Warningf("failed to save rebase state: %s", err)
				}

				printConflictDetails(cfg, newBase)
				cfg.Printf("")

				cfg.Printf("Resolve conflicts on %s, then run `%s`",
					br.Branch, cfg.ColorCyan("gh stack rebase --continue"))
				cfg.Printf("Or abort this operation with `%s`",
					cfg.ColorCyan("gh stack rebase --abort"))
				return ErrConflict
			}

			cfg.Successf("Rebased %s onto %s (squash-merge detected)", br.Branch, newBase)
			// Keep --onto mode; update old base for the next branch.
			ontoOldBase = originalRefs[br.Branch]
		} else {
			var rebaseErr error
			if absIdx > 0 {
				// Use --onto to replay only this branch's unique commits.
				// Without --onto, git may try to replay commits shared with
				// the parent, causing duplicate-patch conflicts when the
				// parent's rebase rewrote those commits.
				rebaseErr = git.RebaseOnto(base, originalRefs[base], br.Branch)
			} else {
				if err := git.CheckoutBranch(br.Branch); err != nil {
					return fmt.Errorf("checking out %s: %w", br.Branch, err)
				}
				// Use regular rebase for the first branch.
				rebaseErr = git.Rebase(base)
			}

			if rebaseErr != nil {
				cfg.Warningf("Rebasing %s onto %s — conflict", br.Branch, base)

				remaining := make([]string, 0)
				for j := i + 1; j < len(branchesToRebase); j++ {
					remaining = append(remaining, branchesToRebase[j].Branch)
				}

				state := &rebaseState{
					CurrentBranchIndex: absIdx,
					ConflictBranch:     br.Branch,
					RemainingBranches:  remaining,
					OriginalBranch:     currentBranch,
					OriginalRefs:       originalRefs,
				}
				if err := saveRebaseState(gitDir, state); err != nil {
					cfg.Warningf("failed to save rebase state: %s", err)
				}

				printConflictDetails(cfg, base)
				cfg.Printf("")

				cfg.Printf("Resolve conflicts on %s, then run `%s`",
					br.Branch, cfg.ColorCyan("gh stack rebase --continue"))
				cfg.Printf("Or abort this operation with `%s`",
					cfg.ColorCyan("gh stack rebase --abort"))
				return ErrConflict
			}

			cfg.Successf("Rebased %s onto %s", br.Branch, base)
		}
	}

	_ = git.CheckoutBranch(currentBranch)

	updateBaseSHAs(s)

	syncStackPRs(cfg, s)

	_ = stack.Save(gitDir, sf)

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
	cfg.Printf("To push up your changes and open/update the stack of PRs, run `%s`",
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
		if err := git.RebaseContinue(); err != nil {
			return fmt.Errorf("rebase continue failed — resolve remaining conflicts and try again: %w", err)
		}
	}

	var baseBranch string
	if state.UseOnto {
		// The --onto path targets the first non-merged ancestor, or trunk.
		baseBranch = s.Trunk.Branch
		for j := state.CurrentBranchIndex - 1; j >= 0; j-- {
			if !s.Branches[j].IsMerged() {
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

	for _, branchName := range state.RemainingBranches {
		idx := s.IndexOf(branchName)
		if idx < 0 {
			return fmt.Errorf("branch %q from saved rebase state is no longer in the stack — the stack may have been modified since the rebase started; consider aborting with --abort", branchName)
		}

		// Skip branches whose PRs have already been merged.
		br := s.Branches[idx]
		if br.IsMerged() {
			state.OntoOldBase = state.OriginalRefs[branchName]
			state.UseOnto = true
			cfg.Successf("Skipping %s (PR %s merged)", branchName, cfg.PRLink(br.PullRequest.Number, br.PullRequest.URL))
			continue
		}

		var base string
		if idx == 0 {
			base = s.Trunk.Branch
		} else {
			base = s.Branches[idx-1].Branch
		}

		if state.UseOnto {
			// Find the proper --onto target: first non-merged ancestor, or trunk.
			newBase := s.Trunk.Branch
			for j := idx - 1; j >= 0; j-- {
				b := s.Branches[j]
				if !b.IsMerged() {
					newBase = b.Branch
					break
				}
			}

			if err := git.RebaseOnto(newBase, state.OntoOldBase, branchName); err != nil {
				remainIdx := -1
				for ri, rb := range state.RemainingBranches {
					if rb == branchName {
						remainIdx = ri
						break
					}
				}
				state.RemainingBranches = state.RemainingBranches[remainIdx+1:]
				state.CurrentBranchIndex = idx
				state.ConflictBranch = branchName
				state.OntoOldBase = state.OriginalRefs[branchName]
				if err := saveRebaseState(gitDir, state); err != nil {
					cfg.Warningf("failed to save rebase state: %s", err)
				}

				cfg.Warningf("Rebasing %s onto %s — conflict", branchName, newBase)
				printConflictDetails(cfg, newBase)
				cfg.Printf("")
				cfg.Printf("Resolve conflicts on %s, then run `%s`",
					branchName, cfg.ColorCyan("gh stack rebase --continue"))
				cfg.Printf("Or abort this operation with `%s`",
					cfg.ColorCyan("gh stack rebase --abort"))
				return ErrConflict
			}

			cfg.Successf("Rebased %s onto %s (squash-merge detected)", branchName, newBase)
			state.OntoOldBase = state.OriginalRefs[branchName]
		} else {
			var rebaseErr error
			if idx > 0 {
				// Use --onto to replay only this branch's unique commits.
				rebaseErr = git.RebaseOnto(base, state.OriginalRefs[base], branchName)
			} else {
				if err := git.CheckoutBranch(branchName); err != nil {
					cfg.Errorf("checking out %s: %s", branchName, err)
					return ErrSilent
				}
				rebaseErr = git.Rebase(base)
			}

			if rebaseErr != nil {
				remainIdx := -1
				for ri, rb := range state.RemainingBranches {
					if rb == branchName {
						remainIdx = ri
						break
					}
				}
				state.RemainingBranches = state.RemainingBranches[remainIdx+1:]
				state.CurrentBranchIndex = idx
				state.ConflictBranch = branchName
				if err := saveRebaseState(gitDir, state); err != nil {
					cfg.Warningf("failed to save rebase state: %s", err)
				}

				cfg.Warningf("Rebasing %s onto %s — conflict", branchName, base)
				printConflictDetails(cfg, base)
				cfg.Printf("")
				cfg.Printf("Resolve conflicts on %s, then run `%s`",
					branchName, cfg.ColorCyan("gh stack rebase --continue"))
				cfg.Printf("Or abort this operation with `%s`",
					cfg.ColorCyan("gh stack rebase --abort"))
				return ErrConflict
			}

			cfg.Successf("Rebased %s onto %s", branchName, base)
		}
	}

	clearRebaseState(gitDir)
	_ = git.CheckoutBranch(state.OriginalBranch)

	updateBaseSHAs(s)

	syncStackPRs(cfg, s)

	_ = stack.Save(gitDir, sf)

	cfg.Printf("All branches in stack rebased locally with %s", s.Trunk.Branch)
	cfg.Printf("To push up your changes and open/update the stack of PRs, run `%s`",
		cfg.ColorCyan("gh stack push"))

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
	files, err := git.ConflictedFiles()
	if err != nil || len(files) == 0 {
		return
	}

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

	cfg.Printf("")
	cfg.Printf("%s", cfg.ColorBold("To resolve:"))
	cfg.Printf("  1. Open each conflicted file and look for conflict markers:")
	cfg.Printf("     %s  (incoming changes from %s)", cfg.ColorCyan("<<<<<<< HEAD"), branch)
	cfg.Printf("     %s", cfg.ColorCyan("======="))
	cfg.Printf("     %s  (changes being rebased)", cfg.ColorCyan(">>>>>>>"))
	cfg.Printf("  2. Edit the file to keep the desired changes and remove the markers")
	cfg.Printf("  3. Stage resolved files: `%s`", cfg.ColorCyan("git add <file>"))
	cfg.Printf("  4. Continue the rebase:  `%s`", cfg.ColorCyan("gh stack rebase --continue"))
}
