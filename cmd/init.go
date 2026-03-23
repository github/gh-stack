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
}

func InitCmd(cfg *config.Config) *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init [branches...]",
		Short: "Initialize a new stack",
		Long: `Initialize a stack object in the local repo.

Creates an entry in .git/gh-stack to track stack state.
Unless specified, prompts user to create/select branch for first layer of the stack.
Trunk defaults to default branch, unless specified otherwise.`,
		Example: `  $ gh stack init
  $ gh stack init myBranch
  $ gh stack init --adopt branch1 branch2 branch3
  $ gh stack init --base integrationBranch firstBranch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.branches = args
			return runInit(cfg, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.base, "base", "b", "", "Trunk branch for stack (defaults to default branch)")
	cmd.Flags().BoolVarP(&opts.adopt, "adopt", "a", false, "Track existing branches as part of a stack")
	cmd.Flags().StringVarP(&opts.prefix, "prefix", "p", "", "Branch name prefix for the stack")

	return cmd
}

func runInit(cfg *config.Config, opts *initOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil
	}

	// Determine trunk branch
	trunk := opts.base

	// Enable git rerere so conflict resolutions are remembered.
	if err := ensureRerere(cfg); errors.Is(err, errInterrupt) {
		return nil
	}

	if trunk == "" {
		trunk, err = git.DefaultBranch()
		if err != nil {
			cfg.Errorf("unable to determine default branch\nUse -b to specify the trunk branch")
			return nil
		}
	}

	// Load existing stack file
	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil
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
				return nil
			}
		}
	}

	var branches []string

	if opts.adopt {
		// Adopt mode: validate all specified branches exist
		if len(opts.branches) == 0 {
			cfg.Errorf("--adopt requires at least one branch name")
			return nil
		}
		for _, b := range opts.branches {
			if !git.BranchExists(b) {
				cfg.Errorf("branch %q does not exist", b)
				return nil
			}
			if err := sf.ValidateNoDuplicateBranch(b); err != nil {
				cfg.Errorf("branch %q already exists in a stack", b)
				return nil
			}
		}
		branches = opts.branches

		// Check if any adopted branches already have PRs on GitHub.
		// If offline or unable to create client, skip silently.
		if client, clientErr := cfg.GitHubClient(); clientErr == nil {
			for _, b := range branches {
				pr, err := client.FindAnyPRForBranch(b)
				if err != nil {
					continue
				}
				if pr != nil {
					state := "open"
					if pr.Merged {
						state = "merged"
					}
					cfg.Errorf("branch %q already has a %s PR (#%d: %s)", b, state, pr.Number, pr.URL)
					return nil
				}
			}
		}
	} else if len(opts.branches) > 0 {
		// Explicit branch names provided — create them
		for _, b := range opts.branches {
			if err := sf.ValidateNoDuplicateBranch(b); err != nil {
				cfg.Errorf("branch %q already exists in a stack", b)
				return nil
			}
			if !git.BranchExists(b) {
				if err := git.CreateBranch(b, trunk); err != nil {
					cfg.Errorf("creating branch %s: %s", b, err)
					return nil
				}
			}
		}
		branches = opts.branches
	} else {
		// Interactive mode
		if !cfg.IsInteractive() {
			cfg.Errorf("interactive input required; provide branch names or use --adopt")
			return nil
		}
		p := prompter.New(cfg.In, cfg.Out, cfg.Err)

		// Step 1: Ask for prefix
		if opts.prefix == "" {
			prefixInput, err := p.Input("Set a branch prefix? (leave blank to skip)", "")
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return nil
				}
				cfg.Errorf("failed to read prefix: %s", err)
				return nil
			}
			opts.prefix = strings.TrimSpace(prefixInput)
		}

		// Step 2: Ask for branch name
		if currentBranch != "" && currentBranch != trunk {
			// Already on a non-trunk branch — offer to use it
			useCurrentBranch, err := p.Confirm(
				fmt.Sprintf("Would you like to use %s as the first layer of your stack?", currentBranch),
				true,
			)
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return nil
				}
				cfg.Errorf("failed to confirm branch selection: %s", err)
				return nil
			}
			if useCurrentBranch {
				if err := sf.ValidateNoDuplicateBranch(currentBranch); err != nil {
					cfg.Errorf("branch %q already exists in the stack", currentBranch)
					return nil
				}
				branches = []string{currentBranch}
			}
		}

		if len(branches) == 0 {
			prompt := "What branch would you like to use as the first layer of your stack?"
			if opts.prefix != "" {
				prompt = fmt.Sprintf("Name the first branch, or leave blank to use %s", branch.NextNumberedName(opts.prefix, nil))
			}
			branchName, err := p.Input(prompt, "")
			if err != nil {
				if isInterruptError(err) {
					printInterrupt(cfg)
					return nil
				}
				cfg.Errorf("failed to read branch name: %s", err)
				return nil
			}
			branchName = strings.TrimSpace(branchName)

			if branchName == "" && opts.prefix != "" {
				// Auto-generate numbered branch name
				branchName = branch.NextNumberedName(opts.prefix, nil)
			} else if branchName == "" {
				cfg.Errorf("branch name cannot be empty")
				return nil
			} else if opts.prefix != "" {
				// Prepend prefix to the user-provided name
				branchName = opts.prefix + "/" + branchName
			}

			if err := sf.ValidateNoDuplicateBranch(branchName); err != nil {
				cfg.Errorf("branch %q already exists in a stack", branchName)
				return nil
			}
			if !git.BranchExists(branchName) {
				if err := git.CreateBranch(branchName, trunk); err != nil {
					cfg.Errorf("creating branch %s: %s", branchName, err)
					return nil
				}
			}
			branches = []string{branchName}
		}
	}

	// Validate prefix (from flag or interactive input)
	if opts.prefix != "" {
		if err := git.ValidateRefName(opts.prefix); err != nil {
			cfg.Errorf("invalid prefix %q: must be a valid git ref component", opts.prefix)
			return nil
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
		Prefix: opts.prefix,
		Trunk: stack.BranchRef{
			Branch: trunk,
			Head:   trunkSHA,
		},
		Branches: branchRefs,
	}

	sf.AddStack(newStack)

	// Sync PR state for adopted branches
	syncStackPRs(cfg, &sf.Stacks[len(sf.Stacks)-1])

	if err := stack.Save(gitDir, sf); err != nil {
		return err
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
				return nil
			}
			cfg.Printf("Switched to branch %s", lastBranch)
		} else {
			cfg.Printf("You can continue working on %s", lastBranch)
		}
	}

	cfg.Printf("To add a new layer to your stack, run `%s`", cfg.ColorCyan("gh stack add"))
	cfg.Printf("When you're ready to push to GitHub and open a stack of PRs, run `%s`", cfg.ColorCyan("gh stack push"))

	return nil
}
