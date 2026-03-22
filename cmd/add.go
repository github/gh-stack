package cmd

import (
	"fmt"

	"github.com/github/gh-stack/internal/branch"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type addOptions struct {
	stageAll     bool
	stageTracked bool
	message      string
}

func AddCmd(cfg *config.Config) *cobra.Command {
	opts := &addOptions{}

	cmd := &cobra.Command{
		Use:   "add [branch]",
		Short: "Add a new branch on top of the current stack",
		Long: `Add a new branch on top of the current stack.

Optionally stage changes and create a commit before creating the branch:
  -a    Stage all changes (including untracked) before committing
  -u    Stage tracked file changes before committing
  -m    Create a commit with the given message

When -m is provided without an explicit branch name, the branch name
is auto-generated based on the commit message and stack prefix.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cfg, opts, args)
		},
	}

	cmd.Flags().BoolVarP(&opts.stageAll, "all", "a", false, "Stage all changes including untracked files")
	cmd.Flags().BoolVarP(&opts.stageTracked, "update", "u", false, "Stage changes to tracked files only")
	cmd.Flags().StringVarP(&opts.message, "message", "m", "", "Create a commit with this message")

	return cmd
}

func runAdd(cfg *config.Config, opts *addOptions, args []string) error {
	// Validate flag combinations
	if opts.stageAll && opts.stageTracked {
		cfg.Errorf("flags -a and -u are mutually exclusive")
		return nil
	}
	if (opts.stageAll || opts.stageTracked) && opts.message == "" {
		cfg.Errorf("staging flags (-a, -u) require -m to create a commit")
		return nil
	}

	result, err := loadStack(cfg, "")
	if err != nil {
		return nil
	}
	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	if s.IsFullyMerged() {
		cfg.Warningf("All branches in this stack have been merged")
		cfg.Printf("Consider creating a new stack with %s", cfg.ColorCyan("gh stack init"))
		return nil
	}

	idx := s.IndexOf(currentBranch)
	if idx >= 0 && idx < len(s.Branches)-1 {
		cfg.Errorf("can only add branches on top of the stack; checkout the top branch %q first", s.Branches[len(s.Branches)-1].Branch)
		return nil
	}

	// When -m is provided, check if the current branch is a stack branch with
	// no unique commits relative to its parent. If so, the commit should land
	// on this branch without creating a new one (e.g., right after init).
	var branchIsEmpty bool
	if opts.message != "" && idx >= 0 {
		parentBranch := s.ActiveBaseBranch(currentBranch)
		commits, _ := git.LogRange(parentBranch, currentBranch)
		branchIsEmpty = len(commits) == 0
	}

	// Empty branch path: stage and commit here, don't create a new branch.
	if branchIsEmpty && opts.message != "" {
		if opts.stageAll {
			if err := git.StageAll(); err != nil {
				cfg.Errorf("failed to stage changes: %s", err)
				return nil
			}
		} else if opts.stageTracked {
			if err := git.StageTracked(); err != nil {
				cfg.Errorf("failed to stage changes: %s", err)
				return nil
			}
		}
		if !git.HasStagedChanges() {
			cfg.Errorf("nothing to commit; stage changes first or use -a/-u")
			return nil
		}
		sha, err := git.Commit(opts.message)
		if err != nil {
			cfg.Errorf("failed to commit: %s", err)
			return nil
		}
		cfg.Successf("Created commit %s on %s", cfg.ColorBold(sha[:7]), currentBranch)
		cfg.Warningf("Branch %s has no prior commits — adding your commit here instead of creating a new branch", currentBranch)
		cfg.Printf("When you're ready for the next layer, run %s again", cfg.ColorCyan("gh stack add"))
		return nil
	}

	// Resolve branch name
	var branchName string
	var explicitName string
	if len(args) > 0 {
		explicitName = args[0]
	}

	if opts.message != "" {
		// Auto-naming mode
		existingBranches := s.BranchNames()
		isFirstBranch := len(existingBranches) == 0
		name, info := branch.ResolveBranchName(s.Prefix, opts.message, explicitName, existingBranches, isFirstBranch)
		if name == "" {
			cfg.Errorf("could not generate branch name")
			return nil
		}
		branchName = name
		if info != "" {
			cfg.Infof("%s", info)
		}
	} else if explicitName != "" {
		// No -m, but explicit name given
		if s.Prefix != "" {
			branchName = s.Prefix + "/" + explicitName
			cfg.Infof("Branch name prefixed: %s", branchName)
		} else {
			branchName = explicitName
		}
	} else {
		// No -m, no explicit name — auto-generate if following numbered
		// convention, otherwise prompt for a name.
		existingBranches := s.BranchNames()
		if s.Prefix != "" && len(existingBranches) > 0 &&
			branch.FollowsNumbering(s.Prefix, existingBranches[len(existingBranches)-1]) {
			branchName = branch.NextNumberedName(s.Prefix, existingBranches)
		} else {
			fmt.Fprintf(cfg.Err, "Enter a name for the new branch: ")
			if _, err := fmt.Fscan(cfg.In, &branchName); err != nil {
				return fmt.Errorf("could not read branch name: %w", err)
			}
			if s.Prefix != "" && branchName != "" {
				branchName = s.Prefix + "/" + branchName
				cfg.Infof("Branch name prefixed: %s", branchName)
			}
		}
	}

	if branchName == "" {
		cfg.Errorf("branch name cannot be empty")
		return nil
	}

	if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
		cfg.Errorf("branch %q already exists in the stack", branchName)
		return nil
	}

	if git.BranchExists(branchName) {
		cfg.Errorf("branch %q already exists; provide an explicit name", branchName)
		return nil
	}

	// Create the new branch from the current HEAD and check it out
	if err := git.CreateBranch(branchName, currentBranch); err != nil {
		cfg.Errorf("failed to create branch: %s", err)
		return nil
	}

	if err := git.CheckoutBranch(branchName); err != nil {
		cfg.Errorf("failed to checkout branch: %s", err)
		return nil
	}

	base, _ := git.RevParse(currentBranch)
	s.Branches = append(s.Branches, stack.BranchRef{Branch: branchName, Base: base})

	// Stage and commit on the NEW branch if -m is provided
	var commitSHA string
	if opts.message != "" {
		if opts.stageAll {
			if err := git.StageAll(); err != nil {
				cfg.Errorf("failed to stage changes: %s", err)
				return nil
			}
		} else if opts.stageTracked {
			if err := git.StageTracked(); err != nil {
				cfg.Errorf("failed to stage changes: %s", err)
				return nil
			}
		}
		if !git.HasStagedChanges() {
			cfg.Errorf("nothing to commit; stage changes first or use -a/-u")
			return nil
		}
		sha, err := git.Commit(opts.message)
		if err != nil {
			cfg.Errorf("failed to commit: %s", err)
			return nil
		}
		commitSHA = sha[:7]
	}

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return nil
	}

	// Print summary
	position := len(s.Branches)
	if commitSHA != "" {
		cfg.Successf("Created branch %s (layer %d) with commit %s", cfg.ColorBold(branchName), position, commitSHA)
	} else {
		cfg.Successf("Created and checked out branch %q", branchName)
	}

	return nil
}
