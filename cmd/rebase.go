package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
}

type rebaseState struct {
	CurrentBranchIndex int               `json:"currentBranchIndex"`
	ConflictBranch     string            `json:"conflictBranch"`
	RemainingBranches  []string          `json:"remainingBranches"`
	OriginalBranch     string            `json:"originalBranch"`
	OriginalRefs       map[string]string `json:"originalRefs"`
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

	return cmd
}

func runRebase(cfg *config.Config, opts *rebaseOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil
	}

	if opts.cont {
		return continueRebase(cfg, gitDir)
	}

	if opts.abort {
		return abortRebase(cfg, gitDir)
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil
	}

	currentBranch := opts.branch
	if currentBranch == "" {
		currentBranch, err = git.CurrentBranch()
		if err != nil {
			cfg.Errorf("unable to determine current branch: %s", err)
			return nil
		}
	}

	s := sf.FindStackForBranch(currentBranch)
	if s == nil {
		cfg.Errorf("no stack found for branch %s", currentBranch)
		return nil
	}

	cfg.Printf("Fetching origin ...")
	if err := git.Fetch("origin"); err != nil {
		cfg.Warningf("Failed to fetch origin: %v", err)
	} else {
		cfg.Successf("Fetching origin")
	}

	chainParts := []string{s.Trunk.Branch}
	for _, b := range s.Branches {
		chainParts = append(chainParts, b.Branch)
	}
	cfg.Printf("Stack detected: %s", joinChain(chainParts))

	currentIdx := s.IndexOf(currentBranch)
	if currentIdx < 0 {
		currentIdx = 0
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

	originalRefs := make(map[string]string)
	for _, b := range s.Branches {
		sha, _ := git.HeadSHA(b.Branch)
		originalRefs[b.Branch] = sha
	}

	for i, br := range branchesToRebase {
		var base string
		absIdx := startIdx + i
		if absIdx == 0 {
			base = s.Trunk.Branch
		} else {
			base = s.Branches[absIdx-1].Branch
		}

		cfg.Printf("Rebasing %s onto %s ...", br.Branch, base)

		if err := git.CheckoutBranch(br.Branch); err != nil {
			return fmt.Errorf("checking out %s: %w", br.Branch, err)
		}

		if err := git.Rebase(base); err != nil {
			cfg.Warningf("Rebasing %s onto %s ... conflict", br.Branch, base)

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
			saveRebaseState(gitDir, state)

			printConflictDetails(cfg, base)
			cfg.Printf("")

			cfg.Printf("Resolve conflicts on %s, then run %s",
				br.Branch, cfg.ColorCyan("gh stack rebase --continue"))
			cfg.Printf("Or abort this operation with %s",
				cfg.ColorCyan("gh stack rebase --abort"))
			return fmt.Errorf("rebase conflict on %s", br.Branch)
		}

		cfg.Successf("Rebasing %s onto %s", br.Branch, base)
	}

	_ = git.CheckoutBranch(currentBranch)

	for i := range s.Branches {
		sha, _ := git.HeadSHA(s.Branches[i].Branch)
		s.Branches[i].Head = sha
	}
	_ = stack.Save(gitDir, sf)

	rangeDesc := "All branches in stack"
	if opts.downstack {
		rangeDesc = fmt.Sprintf("All downstack branches up to %s", currentBranch)
	} else if opts.upstack {
		rangeDesc = fmt.Sprintf("All upstack branches from %s", currentBranch)
	}

	cfg.Printf("%s rebased locally with %s", rangeDesc, s.Trunk.Branch)
	cfg.Printf("To push up your changes and open/update the stack of PRs, run %s",
		cfg.ColorCyan("gh stack push -f"))

	return nil
}

func continueRebase(cfg *config.Config, gitDir string) error {
	state, err := loadRebaseState(gitDir)
	if err != nil {
		cfg.Errorf("no rebase in progress")
		return nil
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil
	}

	// Use the saved original branch to find the stack, since git may be in
	// a detached HEAD state during an active rebase.
	s := sf.FindStackForBranch(state.OriginalBranch)
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
	if state.CurrentBranchIndex > 0 {
		baseBranch = s.Branches[state.CurrentBranchIndex-1].Branch
	} else {
		baseBranch = s.Trunk.Branch
	}
	cfg.Successf("Rebasing %s onto %s", conflictBranch, baseBranch)

	for _, branchName := range state.RemainingBranches {
		idx := s.IndexOf(branchName)
		var base string
		if idx == 0 {
			base = s.Trunk.Branch
		} else {
			base = s.Branches[idx-1].Branch
		}

		cfg.Printf("Rebasing %s onto %s ...", branchName, base)

		if err := git.CheckoutBranch(branchName); err != nil {
			cfg.Errorf("checking out %s: %s", branchName, err)
			return nil
		}

		if err := git.Rebase(base); err != nil {
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
			saveRebaseState(gitDir, state)

			cfg.Warningf("Rebasing %s onto %s ... conflict", branchName, base)
			printConflictDetails(cfg, base)
			cfg.Printf("")
			cfg.Printf("Resolve conflicts on %s, then run %s",
				branchName, cfg.ColorCyan("gh stack rebase --continue"))
			cfg.Printf("Or abort this operation with %s",
				cfg.ColorCyan("gh stack rebase --abort"))
			return fmt.Errorf("rebase conflict on %s", branchName)
		}

		cfg.Successf("Rebasing %s onto %s", branchName, base)
	}

	clearRebaseState(gitDir)
	_ = git.CheckoutBranch(state.OriginalBranch)

	for i := range s.Branches {
		sha, _ := git.HeadSHA(s.Branches[i].Branch)
		s.Branches[i].Head = sha
	}
	_ = stack.Save(gitDir, sf)

	cfg.Printf("All branches in stack rebased locally with %s", s.Trunk.Branch)
	cfg.Printf("To push up your changes and open/update the stack of PRs, run %s",
		cfg.ColorCyan("gh stack push -f"))

	return nil
}

func abortRebase(cfg *config.Config, gitDir string) error {
	state, err := loadRebaseState(gitDir)
	if err != nil {
		cfg.Errorf("no rebase in progress")
		return nil
	}

	if git.IsRebaseInProgress() {
		_ = git.RebaseAbort()
	}

	for branch, sha := range state.OriginalRefs {
		_ = git.CheckoutBranch(branch)
		_ = git.ResetHard(sha)
	}

	_ = git.CheckoutBranch(state.OriginalBranch)
	clearRebaseState(gitDir)
	cfg.Successf("Rebase aborted and branches restored")

	return nil
}

func saveRebaseState(gitDir string, state *rebaseState) {
	data, _ := json.MarshalIndent(state, "", "  ")
	_ = os.WriteFile(filepath.Join(gitDir, rebaseStateFile), data, 0644)
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
	cfg.Printf("  3. Stage resolved files: %s", cfg.ColorCyan("git add <file>"))
	cfg.Printf("  4. Continue the rebase:  %s", cfg.ColorCyan("gh stack rebase --continue"))
}
