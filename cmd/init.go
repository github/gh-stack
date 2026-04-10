package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/branch"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type initOptions struct {
	branches []string
	base     string
	adopt    bool
	prefix   string
	numbered bool
}

func InitCmd(cfg *config.Config) *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init [branches...]",
		Short: "Initialize a new stack",
		Long: `Initialize a stack object in the local repo.

Unless specified, prompts user to create/select branch for first layer of the stack.
Trunk defaults to default branch, unless specified otherwise.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.branches = args
			return runInit(cfg, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.base, "base", "b", "", "Trunk branch for stack (defaults to default branch)")
	cmd.Flags().BoolVarP(&opts.adopt, "adopt", "a", false, "Track existing branches as part of a stack")
	cmd.Flags().StringVarP(&opts.prefix, "prefix", "p", "", "Branch name prefix for the stack")
	cmd.Flags().BoolVarP(&opts.numbered, "numbered", "n", false, "Use auto-incrementing numbered branch names (requires --prefix)")

	return cmd
}

func runInit(cfg *config.Config, opts *initOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return ErrNotInStack
	}

	// Determine trunk branch
	trunk := opts.base

	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return ErrSilent
	}

	if trunk == "" {
		trunk, err = git.DefaultBranch()
		if err != nil {
			cfg.Errorf("unable to determine default branch\nUse -b to specify the trunk branch")
			return ErrNotInStack
		}
	}

	// Load existing stack file
	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return ErrNotInStack
	}

	// Set repository context
	repo, err := cfg.Repo()
	if err == nil {
		sf.Repository = repo.Host + ":" + repo.Owner + "/" + repo.Name
	}

	currentBranch, _ := git.CurrentBranch()

	// Don't allow initializing a stack if the current branch is a non-trunk
	// member of another stack. Trunk branches (e.g. "main") can be shared
	// across multiple stacks.
	if currentBranch != "" {
		for _, s := range sf.FindAllStacksForBranch(currentBranch) {
			if s.IndexOf(currentBranch) >= 0 {
				cfg.Errorf("current branch %q is already part of a stack", currentBranch)
				return ErrInvalidArgs
			}
		}
	}

	var branches []string

	// --adopt takes existing branches as-is; --prefix and --numbered don't apply.
	if opts.adopt && (opts.prefix != "" || opts.numbered) {
		cfg.Errorf("--adopt cannot be combined with --prefix or --numbered")
		return ErrInvalidArgs
	}

	// Validate --numbered requires a prefix (either from flag or interactive input,
	// but for non-interactive paths we can check early).
	if opts.numbered && opts.prefix == "" && !cfg.IsInteractive() {
		cfg.Errorf("--numbered requires --prefix")
		return ErrInvalidArgs
	}

	// Prompt for prefix interactively if not provided via flag and we're
	// in interactive mode (not adopt, not explicit branches).
	if opts.prefix == "" && !opts.adopt && len(opts.branches) == 0 && cfg.IsInteractive() {
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)
		if opts.numbered {
			// --numbered requires a prefix; prompt specifically for one
			prefixInput, err := p.Input("Enter a branch prefix (required for --numbered)", "")
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return ErrSilent
				}
				cfg.Errorf("failed to read prefix: %s", err)
				return ErrSilent
			}
			opts.prefix = strings.TrimSpace(prefixInput)
			if opts.prefix == "" {
				cfg.Errorf("--numbered requires a prefix")
				return ErrInvalidArgs
			}
		} else {
			prefixInput, err := p.Input("Set a branch prefix? (leave blank to skip)", "")
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return ErrSilent
				}
				cfg.Errorf("failed to read prefix: %s", err)
				return ErrSilent
			}
			opts.prefix = strings.TrimSpace(prefixInput)
		}
	}

	// Validate prefix, after it has been determined (from flag or prompt),
	// before any branch creation.
	if opts.prefix != "" {
		if err := git.ValidateRefName(opts.prefix); err != nil {
			cfg.Errorf("invalid prefix %q: must be a valid git ref component", opts.prefix)
			return ErrInvalidArgs
		}
	}

	if opts.adopt {
		// Adopt mode: validate all specified branches exist
		if len(opts.branches) == 0 {
			cfg.Errorf("--adopt requires at least one branch name")
			return ErrInvalidArgs
		}
		for _, b := range opts.branches {
			if !git.BranchExists(b) {
				cfg.Errorf("branch %q does not exist", b)
				return ErrInvalidArgs
			}
			if err := sf.ValidateNoDuplicateBranch(b); err != nil {
				cfg.Errorf("branch %q already exists in a stack", b)
				return ErrInvalidArgs
			}
		}
		branches = opts.branches
	} else if len(opts.branches) > 0 {
		// Explicit branch names provided — apply prefix and create them
		prefixed := make([]string, 0, len(opts.branches))
		for _, b := range opts.branches {
			if opts.prefix != "" {
				b = opts.prefix + "/" + b
			}
			if err := sf.ValidateNoDuplicateBranch(b); err != nil {
				cfg.Errorf("branch %q already exists in a stack", b)
				return ErrInvalidArgs
			}
			if !git.BranchExists(b) {
				if err := git.CreateBranch(b, trunk); err != nil {
					cfg.Errorf("creating branch %s: %s", b, err)
					return ErrSilent
				}
			}
			prefixed = append(prefixed, b)
		}
		branches = prefixed
	} else {
		// Interactive mode — prefix was already prompted for above
		if !cfg.IsInteractive() {
			cfg.Errorf("interactive input required; provide branch names or use --adopt")
			return ErrInvalidArgs
		}
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)

		if opts.numbered {
			// Auto-generate numbered branch name
			branchName := branch.NextNumberedName(opts.prefix, nil)
			if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
				cfg.Errorf("branch %q already exists in a stack", branchName)
				return ErrInvalidArgs
			}
			if !git.BranchExists(branchName) {
				if err := git.CreateBranch(branchName, trunk); err != nil {
					cfg.Errorf("creating branch %s: %s", branchName, err)
					return ErrSilent
				}
			}
			branches = []string{branchName}
		} else {
			if currentBranch != "" && currentBranch != trunk {
				// Already on a non-trunk branch — offer to use it
				useCurrentBranch, err := p.Confirm(
					fmt.Sprintf("Would you like to use %s as the first layer of your stack?", currentBranch),
					true,
				)
				if err != nil {
					if isInterruptError(err) {
						printInterrupt(cfg)
						return ErrSilent
					}
					cfg.Errorf("failed to confirm branch selection: %s", err)
					return ErrSilent
				}
				if useCurrentBranch {
					if err := sf.ValidateNoDuplicateBranch(currentBranch); err != nil {
						cfg.Errorf("branch %q already exists in the stack", currentBranch)
						return ErrInvalidArgs
					}
					branches = []string{currentBranch}
				}
			}

			if len(branches) == 0 {
				prompt := "What branch would you like to use as the first layer of your stack?"
				if opts.prefix != "" {
					prompt = fmt.Sprintf("Enter a name for the first branch (will be prefixed with %s/)", opts.prefix)
				}
				branchName, err := p.Input(prompt, "")
				if err != nil {
					if isInterruptError(err) {
						printInterrupt(cfg)
						return ErrSilent
					}
					cfg.Errorf("failed to read branch name: %s", err)
					return ErrSilent
				}
				branchName = strings.TrimSpace(branchName)

				if branchName == "" {
					cfg.Errorf("branch name cannot be empty")
					return ErrInvalidArgs
				}

				if opts.prefix != "" {
					branchName = opts.prefix + "/" + branchName
				}

				if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
					cfg.Errorf("branch %q already exists in a stack", branchName)
					return ErrInvalidArgs
				}
				if !git.BranchExists(branchName) {
					if err := git.CreateBranch(branchName, trunk); err != nil {
						cfg.Errorf("creating branch %s: %s", branchName, err)
						return ErrSilent
					}
				}
				branches = []string{branchName}
			}
		}
	}

	// Build stack
	trunkSHA, _ := git.RevParse(trunk)
	branchRefs := make([]stack.BranchRef, len(branches))
	for i, b := range branches {
		parent := trunk
		if i > 0 {
			parent = branches[i-1]
		}
		base, _ := git.MergeBase(b, parent)
		branchRefs[i] = stack.BranchRef{Branch: b, Base: base}
	}

	newStack := stack.Stack{
		Prefix:   opts.prefix,
		Numbered: opts.numbered,
		Trunk: stack.BranchRef{
			Branch: trunk,
			Head:   trunkSHA,
		},
		Branches: branchRefs,
	}

	sf.AddStack(newStack)

	// Discover existing PRs for the new stack's branches.
	// For adopt, only record open/draft PRs (ignore closed/merged).
	// For non-adopt, use the standard sync which also detects merges.
	latestStack := &sf.Stacks[len(sf.Stacks)-1]
	if opts.adopt {
		if client, clientErr := cfg.GitHubClient(); clientErr == nil {
			for i := range latestStack.Branches {
				b := &latestStack.Branches[i]
				pr, err := client.FindPRForBranch(b.Branch)
				if err != nil || pr == nil {
					continue
				}
				b.PullRequest = &stack.PullRequestRef{
					Number: pr.Number,
					ID:     pr.ID,
					URL:    pr.URL,
				}
			}
		}
	} else {
		syncStackPRs(cfg, latestStack)
	}

	if err := stack.Save(gitDir, sf); err != nil {
		return handleSaveError(cfg, err)
	}

	// Print result
	if opts.adopt {
		cfg.Printf("Adopting stack with trunk %s and %d branches", trunk, len(branches))
		cfg.Printf("Initializing stack: %s", newStack.DisplayChain())
		cfg.Printf("You can continue working on %s", branches[len(branches)-1])
	} else {
		cfg.Successf("Creating stack with trunk %s and branch %s", trunk, branches[len(branches)-1])
		// Switch to last branch if not already there
		lastBranch := branches[len(branches)-1]
		if currentBranch != lastBranch {
			if err := git.CheckoutBranch(lastBranch); err != nil {
				cfg.Errorf("switching to branch %s: %s", lastBranch, err)
				return ErrSilent
			}
			cfg.Printf("Switched to branch %s", lastBranch)
		} else {
			cfg.Printf("You can continue working on %s", lastBranch)
		}
	}

	cfg.Printf("To add a new layer to your stack, run `%s`", cfg.ColorCyan("gh stack add"))
	cfg.Printf("When you're ready to push to GitHub and open a stack of PRs, run `%s`", cfg.ColorCyan("gh stack submit"))

	return nil
}
