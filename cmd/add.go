package cmd

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/prompter"
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

When -m is omitted but -A or -u is used, your editor opens for the
commit message. When -m is provided without an explicit branch name,
the branch name is auto-generated based on the commit message and
stack prefix.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cfg, opts, args)
		},
	}

	cmd.Flags().BoolVarP(&opts.stageAll, "all", "A", false, "Stage all changes including untracked files")
	cmd.Flags().BoolVarP(&opts.stageTracked, "update", "u", false, "Stage changes to tracked files only")
	cmd.Flags().StringVarP(&opts.message, "message", "m", "", "Create a commit with this message")

	return cmd
}

func runAdd(cfg *config.Config, opts *addOptions, args []string) error {
	// Validate flag combinations
	if opts.stageAll && opts.stageTracked {
		cfg.Errorf("flags -A and -u are mutually exclusive")
		return ErrInvalidArgs
	}

	result, err := loadStack(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir
	sf := result.StackFile
	s := result.Stack
	currentBranch := result.CurrentBranch

	if s.IsFullyMerged() {
		cfg.Warningf("All branches in this stack have been merged")
		cfg.Printf("Consider creating a new stack with `%s`", cfg.ColorCyan("gh stack init"))
		return nil
	}

	idx := s.IndexOf(currentBranch)
	// idx < 0 means we're on the trunk — that's allowed (we'll create
	// a new branch from it). Only block if we're in the middle of the stack.
	if idx >= 0 && idx < len(s.Branches)-1 {
		cfg.Errorf("can only add branches on top of the stack; run `%s` to switch to %q", cfg.ColorCyan("gh stack top"), s.Branches[len(s.Branches)-1].Branch)
		return ErrInvalidArgs
	}

	// Check if the current branch is a stack branch with no unique commits
	// relative to its parent. If so, the commit should land on this branch
	// without creating a new one (e.g., right after init).
	wantsCommit := opts.message != "" || opts.stageAll || opts.stageTracked
	var branchIsEmpty bool
	if wantsCommit && idx >= 0 {
		parentBranch := s.ActiveBaseBranch(currentBranch)
		shas, err := git.RevParseMulti([]string{parentBranch, currentBranch})
		if err == nil {
			branchIsEmpty = shas[0] == shas[1]
		}
	}

	// Empty branch path: stage and commit here, don't create a new branch.
	if branchIsEmpty {
		if err := stageAndValidate(cfg, opts); err != nil {
			return ErrSilent
		}
		sha, err := doCommit(opts.message)
		if err != nil {
			cfg.Errorf("failed to commit: %s", err)
			return ErrSilent
		}
		cfg.Successf("Created commit %s on %s", cfg.ColorBold(sha), currentBranch)
		cfg.Warningf("Branch %s has no prior commits — adding your commit here instead of creating a new branch", currentBranch)
		cfg.Printf("When you're ready for the next layer, run `%s` again", cfg.ColorCyan("gh stack add"))
		return nil
	}

	// Resolve branch name
	var branchName string
	var explicitName string
	if len(args) > 0 {
		explicitName = args[0]
	}
	existingBranches := s.BranchNames()

	if opts.message != "" {
		// Auto-naming mode
		name, info := branch.ResolveBranchName(s.Prefix, opts.message, explicitName, existingBranches, s.Numbered)
		if name == "" {
			cfg.Errorf("could not generate branch name")
			return ErrSilent
		}
		branchName = name
		if info != "" {
			cfg.Infof("%s", info)
		}
	} else if explicitName != "" {
		branchName = applyPrefix(cfg, s.Prefix, explicitName)
	} else {
		// No -m, no explicit name — auto-generate if using numbered
		// convention, otherwise prompt for a name.
		if s.Numbered && s.Prefix != "" {
			branchName = branch.NextNumberedName(s.Prefix, existingBranches)
		} else {
			p := prompter.New(cfg.In, cfg.Out, cfg.Err)
			for {
				input, err := p.Input("Enter a name for the new branch", "")
				if err != nil {
					if isInterruptError(err) {
						printInterrupt(cfg)
						return ErrSilent
					}
					return fmt.Errorf("could not read branch name: %w", err)
				}
				if input == "" {
					cfg.Warningf("branch name cannot be empty, please try again")
					continue
				}
				branchName = applyPrefix(cfg, s.Prefix, input)
				break
			}
		}
	}

	if branchName == "" {
		cfg.Errorf("branch name cannot be empty")
		return ErrInvalidArgs
	}

	if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
		cfg.Errorf("branch %q already exists in the stack", branchName)
		return ErrInvalidArgs
	}

	if git.BranchExists(branchName) {
		cfg.Errorf("branch %q already exists", branchName)
		return ErrInvalidArgs
	}

	// Stage changes before creating the branch so we can fail early if
	// there's nothing to commit (avoids leaving an empty orphan branch).
	if wantsCommit {
		if err := stageAndValidate(cfg, opts); err != nil {
			return ErrSilent
		}
	}

	// Create the new branch from the current HEAD and check it out
	if err := git.CreateBranch(branchName, currentBranch); err != nil {
		cfg.Errorf("failed to create branch: %s", err)
		return ErrSilent
	}

	if err := git.CheckoutBranch(branchName); err != nil {
		cfg.Errorf("failed to checkout branch: %s", err)
		return ErrSilent
	}

	base, err := git.RevParse(currentBranch)
	if err != nil {
		cfg.Warningf("could not resolve base SHA for %s: %s", currentBranch, err)
	}
	s.Branches = append(s.Branches, stack.BranchRef{Branch: branchName, Base: base})

	// Commit on the NEW branch (staging already done above)
	var commitSHA string
	if wantsCommit {
		sha, err := doCommit(opts.message)
		if err != nil {
			cfg.Errorf("failed to commit: %s", err)
			return ErrSilent
		}
		commitSHA = sha
	}

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return ErrSilent
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

// stageAndValidate stages files (if -A or -u is set) and verifies there are
// staged changes to commit. Prints a user-facing error and returns non-nil
// if staging fails or there is nothing to commit.
func stageAndValidate(cfg *config.Config, opts *addOptions) error {
	if opts.stageAll {
		if err := git.StageAll(); err != nil {
			cfg.Errorf("failed to stage changes: %s", err)
			return err
		}
	} else if opts.stageTracked {
		if err := git.StageTracked(); err != nil {
			cfg.Errorf("failed to stage changes: %s", err)
			return err
		}
	}

	if !git.HasStagedChanges() {
		if opts.stageAll || opts.stageTracked {
			cfg.Errorf("no changes to commit after staging")
		} else {
			cfg.Errorf("nothing to commit; stage changes first or use -A/-u")
		}
		return fmt.Errorf("nothing to commit")
	}
	return nil
}

// doCommit commits staged changes. If message is provided, uses it directly.
// If message is empty, launches the user's editor via git commit.
func doCommit(message string) (string, error) {
	if message != "" {
		return git.Commit(message)
	}
	return git.CommitInteractive()
}

// applyPrefix prepends the stack prefix to a branch name if set.
func applyPrefix(cfg *config.Config, prefix, name string) string {
	if prefix != "" {
		name = prefix + "/" + name
		cfg.Infof("Branch name prefixed: %s", name)
	}
	return name
}
