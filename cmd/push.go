package cmd

import (
	"fmt"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/git"
	"github.com/github/gh-stack/internal/stack"
	"github.com/spf13/cobra"
)

type pushOptions struct {
	force  bool
	draft  bool
	dryRun bool
}

func PushCmd(cfg *config.Config) *cobra.Command {
	opts := &pushOptions{}

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push all branches in the current stack and create/update PRs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cfg, opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.force, "force", "f", false, "Force-push branches")
	cmd.Flags().BoolVar(&opts.draft, "draft", false, "Create PRs as drafts")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Show what would be pushed without pushing")

	return cmd
}

func runPush(cfg *config.Config, opts *pushOptions) error {
	gitDir, err := git.GitDir()
	if err != nil {
		cfg.Errorf("not a git repository")
		return nil
	}

	sf, err := stack.Load(gitDir)
	if err != nil {
		cfg.Errorf("failed to load stack state: %s", err)
		return nil
	}

	currentBranch, err := git.CurrentBranch()
	if err != nil {
		cfg.Errorf("failed to get current branch: %s", err)
		return nil
	}

	s := sf.FindStackForBranch(currentBranch)
	if s == nil {
		cfg.Errorf("current branch %q is not part of a stack", currentBranch)
		return nil
	}

	client, err := cfg.GitHubClient()
	if err != nil {
		cfg.Errorf("failed to create GitHub client: %s", err)
		return nil
	}

	// Push all branches
	for _, b := range s.Branches {
		if opts.dryRun {
			cfg.Printf("Would push %s\n", b.Branch)
			continue
		}

		cfg.Printf("Pushing %s...\n", b.Branch)
		if err := git.Push("origin", []string{b.Branch}, opts.force, false); err != nil {
			cfg.Errorf("failed to push %s: %s", b.Branch, err)
			return nil
		}
	}

	if opts.dryRun {
		return nil
	}

	// Create or update PRs
	for i, b := range s.Branches {
		baseBranch := s.BaseBranch(b.Branch)

		pr, err := client.FindPRForBranch(b.Branch)
		if err != nil {
			cfg.Warningf("failed to check PR for %s: %v\n", b.Branch, err)
			continue
		}

		if pr == nil {
			// Create new PR
			title := b.Branch
			body := fmt.Sprintf("Part %d of stack.\n\nBase: `%s`", i+1, baseBranch)

			newPR, createErr := client.CreatePR(baseBranch, b.Branch, title, body, opts.draft)
			if createErr != nil {
				cfg.Warningf("failed to create PR for %s: %v\n", b.Branch, createErr)
				continue
			}
			cfg.Successf("Created PR #%d for %s\n", newPR.Number, b.Branch)
		} else {
			// Update base if needed
			if pr.BaseRefName != baseBranch {
				if err := client.UpdatePRBase(pr.ID, baseBranch); err != nil {
					cfg.Warningf("failed to update PR #%d base: %v\n", pr.Number, err)
				} else {
					cfg.Successf("Updated PR #%d base to %s\n", pr.Number, baseBranch)
				}
			} else {
				cfg.Printf("PR #%d for %s is up to date\n", pr.Number, b.Branch)
			}
		}
	}

	// TODO: Add PRs to a stack
	//
	// We can call an API after all the individual PRs are created/updated to create the stack at once,
	// or we can add a flag to the existing PR API to incrementally build the stack.
	//
	// For now, the PRs are pushed and created individually but are NOT linked as a formal stack on GitHub.
	cfg.Warningf("Stacked PRs is not yet implemented — PRs were created individually.\n")
	fmt.Fprintf(cfg.Err, "  Once the GitHub Stacks API is available, PRs will be automatically\n")
	fmt.Fprintf(cfg.Err, "  grouped into a Stack.\n")

	// Update head SHAs
	for i, b := range s.Branches {
		if sha, err := git.HeadSHA(b.Branch); err == nil {
			s.Branches[i].Head = sha
		}
	}

	if err := stack.Save(gitDir, sf); err != nil {
		cfg.Errorf("failed to save stack state: %s", err)
		return nil
	}

	cfg.Successf("Pushed and synced %d branches\n", len(s.Branches))
	return nil
}
