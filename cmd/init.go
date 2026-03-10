package cmd

import (
	"fmt"
	"os"

	"github.com/cli/go-gh/v2/pkg/prompter"
	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type initOptions struct {
	branches []string
	base     string
	adopt    bool
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
  $ gh stack init branch1 branch2 branch3 --adopt
  $ gh stack init firstBranch -b integrationBranch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.branches = args
			return runInit(cfg, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.base, "base", "b", "", "Trunk branch for stack (defaults to default branch)")
	cmd.Flags().BoolVarP(&opts.adopt, "adopt", "a", false, "Track existing branches as part of a stack")

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

	// Don't allow initializing a stack if the current branch is already part of another stack
	if currentBranch != "" {
		if stacks := sf.FindAllStacksForBranch(currentBranch); len(stacks) > 0 {
			cfg.Errorf("current branch %q is already part of a stack", currentBranch)
			return nil
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
		p := prompter.New(os.Stdin, os.Stdout, os.Stderr)

		if currentBranch != "" && currentBranch != trunk {
			// Already on a non-trunk branch — offer to use it
			useCurrentBranch, err := p.Confirm(
				fmt.Sprintf("Would you like to use %s as the first layer of your stack?", currentBranch),
				true,
			)
			if err != nil {
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
			branchName, err := p.Input("What branch would you like to use as the first layer of your stack?", "")
			if err != nil {
				cfg.Errorf("failed to read branch name: %s", err)
				return nil
			}
			if branchName == "" {
				cfg.Errorf("branch name cannot be empty")
				return nil
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

	// Build stack
	trunkSHA, _ := git.HeadSHA(trunk)
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
		Trunk: stack.BranchRef{
			Branch: trunk,
			Head:   trunkSHA,
		},
		Branches: branchRefs,
	}

	sf.AddStack(newStack)
	if err := stack.Save(gitDir, sf); err != nil {
		return err
	}

	// Print result
	if opts.adopt {
		cfg.Printf("Adopting stack with trunk %s and %d branches", trunk, len(branches))
		chainParts := []string{"(" + trunk + ")"}
		for _, b := range branches {
			chainParts = append(chainParts, b)
		}
		cfg.Printf("Initializing stack: %s", joinChain(chainParts))
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

	cfg.Printf("To add a new layer to your stack, run %s", cfg.ColorCyan("gh stack add"))
	cfg.Printf("When you're ready to push to GitHub and open a stack of PRs, run %s", cfg.ColorCyan("gh stack push"))

	return nil
}

// joinChain formats branches as: (trunk) <- branch1 <- branch2
func joinChain(parts []string) string {
	result := parts[0]
	for _, p := range parts[1:] {
		result += " <- " + p
	}
	return result
}
