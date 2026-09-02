package cmd

import (
	"fmt"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/branch"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/modify"
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
the branch name is auto-generated from the commit message.

If a missing local branch has a same-named tracking ref on the selected push
remote, interactive use offers to pull and adopt it. Non-interactive use
aborts rather than creating an unrelated branch.`,
		Example: `  # Add a new named branch to the stack
  $ gh stack add my-feature

  # Add a branch and commit staged changes
  $ gh stack add -Am "Add user authentication" my-feature

  # Auto-generate branch name from the commit message
  $ gh stack add -m "Fix login bug"
  
  # Add a branch and open editor to write commit message
  $ gh stack add -A my-feature`,
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

	result, err := loadStackOptional(cfg, "")
	if err != nil {
		return ErrNotInStack
	}
	gitDir := result.GitDir

	if err := modify.CheckStateGuard(gitDir); err != nil {
		cfg.Errorf("%s", err)
		return ErrModifyRecovery
	}

	if result.Stack == nil {
		branchName, err := addBranchNameFromArgs(cfg, opts, args)
		if err != nil {
			return err
		}
		return initializeStackFromAdd(cfg, opts, branchName, result.CurrentBranch)
	}

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
		cfg.Errorf("can only add branches to the top of the stack; run `%s` then `%s`", cfg.ColorCyan("gh stack top"), cfg.ColorCyan("gh stack add"))
		cfg.Printf("Or to restructure your stack and insert a branch, use `%s`", cfg.ColorCyan("gh stack modify"))
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

	// Resolve branch name:
	//   explicit name    -> used verbatim
	//   -m without a name -> auto-generated from the commit message
	//   neither          -> prompt for a name
	branchName, err := addBranchNameFromArgs(cfg, opts, args)
	if err != nil {
		return err
	}
	if branchName == "" {
		// No -m and no explicit name — prompt for one.
		for {
			input, err := promptInput(cfg, "Enter a name for the new branch:")
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
			branchName = input
			break
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

	// If the branch already exists in git but is not part of any stack,
	// adopt it instead of erroring. This mirrors the init command's behavior.
	localBranchExists := git.BranchExists(branchName)
	adoptRemote := ""
	if !localBranchExists {
		adoptRemote, err = resolveRemoteBranchForCreation(cfg, branchName, currentBranch)
		if err != nil {
			return err
		}
	}

	adopted := localBranchExists || adoptRemote != ""
	var adoptedBase string
	if adopted {
		adoptedRef := branchName
		if adoptRemote != "" {
			adoptedRef = adoptRemote + "/" + branchName
		}
		adoptedBase, err = git.MergeBase(currentBranch, adoptedRef)
		if err != nil {
			cfg.Errorf("failed to determine the common base of %s and %s: %s", currentBranch, adoptedRef, err)
			return ErrSilent
		}
	}

	// Stage changes before creating the branch so we can fail early if
	// there's nothing to commit (avoids leaving an empty orphan branch).
	if wantsCommit {
		if err := stageAndValidate(cfg, opts); err != nil {
			return ErrSilent
		}
	}

	if !localBranchExists {
		// Create the branch from the chosen source and check it out.
		if adoptRemote != "" {
			if err := createLocalBranchFromRemote(cfg, branchName, adoptRemote); err != nil {
				cfg.Errorf("failed to create branch from %s/%s: %s", adoptRemote, branchName, err)
				return ErrSilent
			}
		} else {
			if err := git.CreateBranch(branchName, currentBranch); err != nil {
				cfg.Errorf("failed to create branch: %s", err)
				return ErrSilent
			}
		}
	}

	if err := git.CheckoutBranch(branchName); err != nil {
		cfg.Errorf("failed to checkout branch: %s", err)
		return ErrSilent
	}

	base := adoptedBase
	if !adopted {
		base, err = git.RevParse(currentBranch)
		if err != nil {
			cfg.Warningf("could not resolve base SHA for %s: %s", currentBranch, err)
		}
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
		return handleSaveError(cfg, err)
	}

	// Print summary
	position := len(s.Branches)
	if adopted {
		if commitSHA != "" {
			cfg.Successf("Adopted branch %s (layer %d) with commit %s", cfg.ColorBold(branchName), position, commitSHA)
		} else {
			cfg.Successf("Adopted existing branch %q into the stack", branchName)
		}
	} else {
		if commitSHA != "" {
			cfg.Successf("Created branch %s (layer %d) with commit %s", cfg.ColorBold(branchName), position, commitSHA)
		} else {
			cfg.Successf("Created and checked out branch %q", branchName)
		}
	}

	return nil
}

func addBranchNameFromArgs(cfg *config.Config, opts *addOptions, args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if opts.message == "" {
		return "", nil
	}

	branchName := branch.DateSlug(opts.message)
	if branchName == "" {
		cfg.Errorf("could not generate branch name")
		return "", ErrSilent
	}
	return branchName, nil
}

func initializeStackFromAdd(cfg *config.Config, opts *addOptions, branchName, currentBranch string) error {
	if !cfg.IsInteractive() {
		reportBranchNotInStack(cfg, currentBranch, false)
		return ErrNotInStack
	}

	prompt := "Would you like to initialize a new stack?"
	var confirmed bool
	var err error
	if cfg.ConfirmFn != nil {
		confirmed, err = cfg.ConfirmFn(prompt, true)
	} else {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		confirmed, err = p.Confirm(prompt, true)
	}
	if err != nil {
		if isInterruptError(err) {
			printInterrupt(cfg)
			return ErrSilent
		}
		cfg.Errorf("failed to read confirmation: %s", err)
		return ErrSilent
	}
	if !confirmed {
		reportBranchNotInStack(cfg, currentBranch, false)
		return ErrNotInStack
	}

	wantsCommit := opts.message != "" || opts.stageAll || opts.stageTracked
	if wantsCommit {
		if err := stageAndValidate(cfg, opts); err != nil {
			return ErrSilent
		}
	}

	initOpts := &initOptions{}
	if branchName != "" {
		initOpts.branches = []string{branchName}
	}
	if err := runInit(cfg, initOpts); err != nil {
		return err
	}

	if wantsCommit {
		sha, err := doCommit(opts.message)
		if err != nil {
			cfg.Errorf("failed to commit: %s", err)
			return ErrSilent
		}
		if branchName == "" {
			branchName, _ = git.CurrentBranch()
		}
		cfg.Successf("Created commit %s on %s", cfg.ColorBold(sha), branchName)
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
